package deploy

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Deployer pulls a Docker image and updates the target container.
type Deployer interface {
	PullAndUpdate(ctx context.Context, target Target, tag string) error
}

// deployTimeout bounds a single PullAndUpdate run so a hung Docker daemon
// cannot block the deploy goroutine forever.
const deployTimeout = 10 * time.Minute

// execCmd runs a command and returns its combined output. It is a package
// variable so tests can record and stub commands.
var execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %v: %w\n%s", name, args, err, string(out))
	}
	return out, nil
}

// NewDeployer creates a Deployer based on DEPLOY_BACKEND env var.
//   - "api" (default): docker pull + recreate, preserving the target's runtime
//     configuration (volumes, networks, labels, restart policy, env, ...).
//   - "compose": docker compose pull + up -d --no-deps <service>, resolving the
//     compose project from the target container's labels.
func NewDeployer() Deployer {
	switch DeployBackend() {
	case "compose":
		return &composeDeployer{}
	default:
		return &dockerDeployer{
			inflight: make(map[string]*containerLock),
		}
	}
}

// containerLock provides per-container serialization.
type containerLock struct {
	ch chan struct{}
	mu sync.Mutex
}

func newContainerLock() *containerLock {
	return &containerLock{ch: make(chan struct{}, 1)}
}

func (l *containerLock) Lock()   { l.ch <- struct{}{} }
func (l *containerLock) Unlock() { <-l.ch }

type dockerDeployer struct {
	mu       sync.Mutex
	inflight map[string]*containerLock
}

func (d *dockerDeployer) getLock(container string) *containerLock {
	d.mu.Lock()
	defer d.mu.Unlock()
	lk, ok := d.inflight[container]
	if !ok {
		lk = newContainerLock()
		d.inflight[container] = lk
	}
	return lk
}

func (d *dockerDeployer) PullAndUpdate(ctx context.Context, target Target, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, deployTimeout)
	defer cancel()

	lk := d.getLock(target.Container)
	lk.Lock()
	defer lk.Unlock()

	imageRef := target.Image + ":" + tag
	log.Printf("Deploy: pulling image %s", imageRef)
	if _, err := execCmd(ctx, "docker", "pull", imageRef); err != nil {
		return fmt.Errorf("pull failed: %w", err)
	}

	raw, err := execCmd(ctx, "docker", "inspect", target.Container)
	if err != nil {
		// Container doesn't exist — create it fresh.
		return d.createNew(ctx, target.Container, imageRef)
	}

	cfg, err := parseInspect(raw)
	if err != nil {
		return fmt.Errorf("inspect failed: %w", err)
	}

	if isSelfContainer(cfg.ID) {
		log.Printf("Deploy: %s is the container running GitLens — updating via helper", target.Container)
		return d.updateSelf(ctx, target.Container, imageRef, cfg)
	}

	return d.updateDirect(ctx, target.Container, imageRef, cfg)
}

// createNew creates and starts a container that does not exist yet.
func (d *dockerDeployer) createNew(ctx context.Context, container, imageRef string) error {
	log.Printf("Deploy: container %s does not exist, creating...", container)
	if err := execStep(ctx, "docker", "create", "--name", container, imageRef); err != nil {
		return fmt.Errorf("create failed: %w", err)
	}
	if err := execStep(ctx, "docker", "start", container); err != nil {
		return fmt.Errorf("start failed: %w", err)
	}
	log.Printf("Deploy: container %s created with %s", container, imageRef)
	return nil
}

// updateDirect recreates an existing container in place, preserving its
// runtime configuration.
func (d *dockerDeployer) updateDirect(ctx context.Context, container, imageRef string, cfg *containerInspect) error {
	createArgs := containerCreateArgs(cfg)

	log.Printf("Deploy: stopping container %s", container)
	if err := execStep(ctx, "docker", "stop", container); err != nil {
		return fmt.Errorf("stop failed: %w", err)
	}

	log.Printf("Deploy: removing container %s", container)
	if err := execStep(ctx, "docker", "rm", container); err != nil {
		return fmt.Errorf("rm failed: %w", err)
	}

	args := append([]string{"create", "--name", container}, createArgs...)
	args = append(args, imageRef)
	log.Printf("Deploy: creating container %s with %s", container, imageRef)
	if err := execStep(ctx, "docker", args...); err != nil {
		return fmt.Errorf("create failed: %w", err)
	}

	if err := execStep(ctx, "docker", "start", container); err != nil {
		return fmt.Errorf("start failed: %w", err)
	}

	log.Printf("Deploy: container %s updated to %s", container, imageRef)
	return nil
}

// updateSelf recreates the container this process runs in. The whole
// stop/rm/create/start sequence runs inside a detached helper container, so
// stopping the target does not kill the deployer mid-sequence.
func (d *dockerDeployer) updateSelf(ctx context.Context, container, imageRef string, cfg *containerInspect) error {
	createArgs := containerCreateArgs(cfg)
	var b strings.Builder
	b.WriteString("docker stop ")
	b.WriteString(shQuote(container))
	b.WriteString(" && docker rm ")
	b.WriteString(shQuote(container))
	b.WriteString(" && docker create --name ")
	b.WriteString(shQuote(container))
	for _, a := range createArgs {
		b.WriteByte(' ')
		b.WriteString(shQuote(a))
	}
	b.WriteString(" ")
	b.WriteString(shQuote(imageRef))
	b.WriteString(" && docker start ")
	b.WriteString(shQuote(container))
	return runHelper(ctx, b.String(), nil)
}

// composeDeployer updates a compose-managed service via docker compose.
type composeDeployer struct{}

func (d *composeDeployer) PullAndUpdate(ctx context.Context, target Target, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, deployTimeout)
	defer cancel()

	proj, err := composeProjectFor(ctx, target.Container)
	if err != nil || proj == nil {
		// Not (or no longer) compose-managed — fall back to the legacy
		// behavior of running docker compose from the current directory.
		log.Printf("Deploy (compose): container %s not compose-managed (%v), falling back to cwd", target.Container, err)
		return d.runFromCwd(ctx, target.Container)
	}

	// Run pull + up inside a detached helper that mounts the Docker socket and
	// the compose project directory, so it works regardless of this process's
	// working directory and survives the service being recreated.
	script := composeCommand(proj, "pull") + " && " + composeCommand(proj, "up")
	mount := proj.WorkingDir + ":" + proj.WorkingDir
	log.Printf("Deploy (compose): project=%s dir=%s service=%s", proj.Project, proj.WorkingDir, proj.Service)
	return runHelper(ctx, script, []string{mount})
}

func (d *composeDeployer) runFromCwd(ctx context.Context, service string) error {
	log.Printf("Deploy (compose): pulling service %s", service)
	if err := execStep(ctx, "docker", "compose", "pull", service); err != nil {
		return fmt.Errorf("compose pull failed: %w", err)
	}
	log.Printf("Deploy (compose): recreating service %s", service)
	if err := execStep(ctx, "docker", "compose", "up", "-d", "--no-deps", service); err != nil {
		return fmt.Errorf("compose up failed: %w", err)
	}
	log.Printf("Deploy (compose): service %s updated", service)
	return nil
}

// composeProject describes how a container's compose project can be driven.
type composeProject struct {
	Project    string // com.docker.compose.project
	WorkingDir string // com.docker.compose.project.working_dir
	Service    string // com.docker.compose.service (falls back to container name)
}

// composeProjectFor resolves the compose project that manages container by
// reading its labels. Returns nil, nil when the container carries no compose
// labels (i.e. it is not compose-managed).
func composeProjectFor(ctx context.Context, container string) (*composeProject, error) {
	out, err := execCmd(ctx, "docker", "inspect",
		"--format", `{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.docker.compose.project.working_dir"}}|{{index .Config.Labels "com.docker.compose.service"}}`,
		container)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
	p := &composeProject{Service: container}
	if len(parts) > 0 {
		p.Project = parts[0]
	}
	if len(parts) > 1 {
		p.WorkingDir = parts[1]
	}
	if len(parts) > 2 && parts[2] != "" {
		p.Service = parts[2]
	}
	if p.Project == "" && p.WorkingDir == "" {
		return nil, nil
	}
	return p, nil
}

// composeCommand renders a `docker compose` invocation with all arguments
// shell-quoted, e.g.
//
//	docker compose -p 'deathstar' --project-directory '/root/homelab/deathstar' pull 'gitlens'
func composeCommand(p *composeProject, verb string) string {
	var b strings.Builder
	b.WriteString("docker compose")
	if p.Project != "" {
		b.WriteString(" -p ")
		b.WriteString(shQuote(p.Project))
	}
	if p.WorkingDir != "" {
		b.WriteString(" --project-directory ")
		b.WriteString(shQuote(p.WorkingDir))
	}
	if verb == "pull" {
		b.WriteString(" pull ")
	} else {
		b.WriteString(" up -d --no-deps ")
	}
	b.WriteString(shQuote(p.Service))
	return b.String()
}

// execStep runs a command, returning only its error.
func execStep(ctx context.Context, name string, args ...string) error {
	_, err := execCmd(ctx, name, args...)
	return err
}
