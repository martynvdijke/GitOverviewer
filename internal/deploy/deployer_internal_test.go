package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

// runRecorder is a mockable execCmd that records invocations and lets tests
// script canned responses per command.
type runRecorder struct {
	mu      sync.Mutex
	calls   [][]string
	respond func(full []string) ([]byte, error)
}

func (r *runRecorder) run(_ context.Context, name string, args ...string) ([]byte, error) {
	full := append([]string{name}, args...)
	r.mu.Lock()
	r.calls = append(r.calls, full)
	r.mu.Unlock()
	if r.respond != nil {
		return r.respond(full)
	}
	return nil, nil
}

func (r *runRecorder) allCmds() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, len(r.calls))
	copy(out, r.calls)
	return out
}

// verbOrder returns the second token of each recorded command (the docker subcommand).
func (r *runRecorder) verbOrder() []string {
	var verbs []string
	for _, c := range r.allCmds() {
		if len(c) > 1 {
			verbs = append(verbs, c[1])
		}
	}
	return verbs
}

func installRecorder(t *testing.T, rec *runRecorder) {
	t.Helper()
	orig := execCmd
	execCmd = rec.run
	t.Cleanup(func() { execCmd = orig })
}

func inspectJSONFor(t *testing.T, c containerInspect) []byte {
	t.Helper()
	b, err := json.Marshal([]containerInspect{c})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// deathstarInspect returns a docker inspect fixture that mirrors the
// production deathstar compose stack (bind mount, volume, user-defined
// network with aliases, restart policy, labels, exposed port).
func deathstarInspect(id string) containerInspect {
	return containerInspect{
		ID:   id,
		Name: "/datey",
		Config: containerConfig{
			Image:      "ghcr.io/martynvdijke/datey:latest",
			Hostname:   "datey",
			WorkingDir: "/app",
			Env: []string{
				"PATH=/usr/local/bin:/usr/bin:/bin",
				"DB_PATH=/db/datey.db",
			},
			Labels: map[string]string{
				"gitlens.deploy.target":      "martynvdijke/datey",
				"com.docker.compose.service": "datey",
			},
			ExposedPorts: map[string]struct{}{"6270/tcp": {}},
		},
		HostConfig: containerHostConfig{
			NetworkMode: "deathstar_default",
			RestartPolicy: containerRestartPolicy{
				Name: "always",
			},
			PortBindings: map[string][]containerPortBinding{
				"6270/tcp": {{HostIP: "", HostPort: "6270"}},
			},
			ExtraHosts: []string{"host.docker.internal:host-gateway"},
		},
		Mounts: []containerMount{
			{Type: "bind", Source: "/config/datey", Destination: "/config"},
			{Type: "volume", Name: "deathstar_db", Destination: "/db"},
		},
		NetworkSettings: containerNetworkSettings{
			Networks: map[string]containerNetwork{
				"deathstar_default": {Aliases: []string{"datey", "datey_1"}},
			},
		},
	}
}

// ---- pure helpers ----

func TestShQuote(t *testing.T) {
	cases := map[string]string{
		"simple":           "'simple'",
		"with space":       "'with space'",
		"it's":             `'it'\''s'`,
		"$(touch /tmp/x)":  "'$(touch /tmp/x)'",
		"back`tick":        "'back`tick'",
		"semi;colon|pipe&": "'semi;colon|pipe&'",
	}
	for in, want := range cases {
		if got := shQuote(in); got != want {
			t.Errorf("shQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestIsSelfContainer(t *testing.T) {
	self, err := os.Hostname()
	if err != nil || self == "" {
		t.Skip("cannot read hostname")
	}
	if !isSelfContainer(self + strings.Repeat("0", 64-len(self))) {
		t.Errorf("expected container ID starting with %q to be self", self)
	}
	if isSelfContainer(strings.Repeat("b", 64)) {
		t.Errorf("expected unrelated ID not to be self")
	}
}

func TestHelperImage_EnvOverride(t *testing.T) {
	t.Setenv("DEPLOY_HELPER_IMAGE", "custom/helper:2")
	if got := helperImage(context.Background()); got != "custom/helper:2" {
		t.Fatalf("expected env override, got %q", got)
	}
}

func TestParseInspect(t *testing.T) {
	raw := inspectJSONFor(t, deathstarInspect(strings.Repeat("a", 64)))
	c, err := parseInspect(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Config.Env) != 2 || c.HostConfig.NetworkMode != "deathstar_default" {
		t.Fatalf("unexpected parse result: %+v", c)
	}
	if len(c.Mounts) != 2 || c.Mounts[0].Type != "bind" {
		t.Fatalf("unexpected mounts: %+v", c.Mounts)
	}
	if _, ok := c.NetworkSettings.Networks["deathstar_default"]; !ok {
		t.Fatalf("missing network settings: %+v", c.NetworkSettings)
	}
}

func TestContainerCreateArgs_PreservesConfig(t *testing.T) {
	c := deathstarInspect(strings.Repeat("a", 64))
	args := containerCreateArgs(&c)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-e PATH=/usr/local/bin:/usr/bin:/bin",
		"-e DB_PATH=/db/datey.db",
		"--label gitlens.deploy.target=martynvdijke/datey",
		"--expose 6270/tcp",
		"--workdir /app",
		"--hostname datey",
		"-v /config/datey:/config",
		"-v deathstar_db:/db",
		"-p 6270:6270/tcp",
		"--restart always",
		"--add-host host.docker.internal:host-gateway",
		"--network deathstar_default",
		"--network-alias datey",
		"--network-alias datey_1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("create args missing %q: %s", want, joined)
		}
	}
}

func TestContainerCreateArgs_ExclusiveNetwork(t *testing.T) {
	for _, mode := range []string{"host", "none"} {
		c := &containerInspect{
			HostConfig: containerHostConfig{NetworkMode: mode},
			NetworkSettings: containerNetworkSettings{
				Networks: map[string]containerNetwork{mode: {Aliases: []string{"x"}}},
			},
		}
		args := containerCreateArgs(c)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--network "+mode) {
			t.Errorf("mode %s: expected --network %s, got %s", mode, mode, joined)
		}
		if strings.Contains(joined, "--network-alias") {
			t.Errorf("mode %s: aliases must not be re-added for exclusive networks, got %s", mode, joined)
		}
	}
}

func TestComposeCommand(t *testing.T) {
	p := &composeProject{Project: "deathstar", WorkingDir: "/root/homelab/deathstar", Service: "gitlens"}
	if got, want := composeCommand(p, "pull"), "docker compose -p 'deathstar' --project-directory '/root/homelab/deathstar' pull 'gitlens'"; got != want {
		t.Errorf("pull command = %s, want %s", got, want)
	}
	if got, want := composeCommand(p, "up"), "docker compose -p 'deathstar' --project-directory '/root/homelab/deathstar' up -d --no-deps 'gitlens'"; got != want {
		t.Errorf("up command = %s, want %s", got, want)
	}
}

func TestComposeProjectFor(t *testing.T) {
	rec := &runRecorder{respond: func(full []string) ([]byte, error) {
		return []byte("deathstar|/root/homelab/deathstar|gitlens"), nil
	}}
	installRecorder(t, rec)

	p, err := composeProjectFor(context.Background(), "gitlens")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil || p.Project != "deathstar" || p.WorkingDir != "/root/homelab/deathstar" || p.Service != "gitlens" {
		t.Fatalf("unexpected project: %+v", p)
	}
}

func TestComposeProjectFor_ServiceFallsBackToContainerName(t *testing.T) {
	rec := &runRecorder{respond: func(full []string) ([]byte, error) {
		return []byte("deathstar|/root/homelab/deathstar|"), nil
	}}
	installRecorder(t, rec)

	p, err := composeProjectFor(context.Background(), "datey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil || p.Service != "datey" {
		t.Fatalf("expected service to fall back to container name, got %+v", p)
	}
}

func TestComposeProjectFor_NotComposeManaged(t *testing.T) {
	rec := &runRecorder{respond: func(full []string) ([]byte, error) {
		return []byte("||"), nil
	}}
	installRecorder(t, rec)

	p, err := composeProjectFor(context.Background(), "standalone")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Fatalf("expected nil project for non-compose container, got %+v", p)
	}
}

// ---- dockerDeployer command sequences ----

func TestPullAndUpdate_ExistingContainer_PreservesConfig(t *testing.T) {
	rec := &runRecorder{respond: func(full []string) ([]byte, error) {
		switch {
		case full[1] == "pull":
			return nil, nil
		case full[1] == "inspect" && len(full) == 3:
			return inspectJSONFor(t, deathstarInspect(strings.Repeat("a", 64))), nil
		}
		return nil, nil
	}}
	installRecorder(t, rec)

	d := &dockerDeployer{inflight: make(map[string]*containerLock)}
	err := d.PullAndUpdate(context.Background(), Target{
		Repository: "martynvdijke/datey",
		Image:      "ghcr.io/martynvdijke/datey",
		Container:  "datey",
	}, "1.2.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got, want := strings.Join(rec.verbOrder(), ","), "pull,inspect,stop,rm,create,start"; got != want {
		t.Fatalf("command order = %s, want %s", got, want)
	}

	var create []string
	for _, c := range rec.allCmds() {
		if c[1] == "create" {
			create = c
			break
		}
	}
	if create == nil {
		t.Fatal("no create command recorded")
	}
	createS := strings.Join(create, " ")
	for _, want := range []string{
		"create --name datey",
		"-e DB_PATH=/db/datey.db",
		"--label gitlens.deploy.target=martynvdijke/datey",
		"-v /config/datey:/config",
		"-v deathstar_db:/db",
		"-p 6270:6270/tcp",
		"--restart always",
		"--network deathstar_default",
		"--network-alias datey",
		"ghcr.io/martynvdijke/datey:1.2.3",
	} {
		if !strings.Contains(createS, want) {
			t.Errorf("create command missing %q: %s", want, createS)
		}
	}
}

func TestPullAndUpdate_ContainerMissing_CreatesBare(t *testing.T) {
	rec := &runRecorder{respond: func(full []string) ([]byte, error) {
		if full[1] == "inspect" {
			return nil, errors.New("No such object: missing")
		}
		return nil, nil
	}}
	installRecorder(t, rec)

	d := &dockerDeployer{inflight: make(map[string]*containerLock)}
	err := d.PullAndUpdate(context.Background(), Target{Image: "img", Container: "missing"}, "1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got, want := strings.Join(rec.verbOrder(), ","), "pull,inspect,create,start"; got != want {
		t.Fatalf("command order = %s, want %s", got, want)
	}
}

func TestPullAndUpdate_PullFailure_Stops(t *testing.T) {
	rec := &runRecorder{respond: func(full []string) ([]byte, error) {
		if full[1] == "pull" {
			return []byte("error pulling image"), errors.New("pull failed")
		}
		return nil, nil
	}}
	installRecorder(t, rec)

	d := &dockerDeployer{inflight: make(map[string]*containerLock)}
	err := d.PullAndUpdate(context.Background(), Target{Image: "img", Container: "c"}, "1.0.0")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := rec.verbOrder(); len(got) != 1 {
		t.Fatalf("expected only the pull attempt, got %v", got)
	}
}

func TestPullAndUpdate_SelfUpdate_UsesHelper(t *testing.T) {
	t.Setenv("DEPLOY_HELPER_IMAGE", "test/helper:1")

	self, err := os.Hostname()
	if err != nil || self == "" {
		t.Skip("cannot read hostname")
	}
	selfID := self + strings.Repeat("0", 64-len(self))

	rec := &runRecorder{respond: func(full []string) ([]byte, error) {
		switch {
		case full[1] == "inspect" && len(full) == 3:
			return inspectJSONFor(t, deathstarInspect(selfID)), nil
		case full[1] == "wait":
			return []byte("0"), nil
		}
		return nil, nil
	}}
	installRecorder(t, rec)

	d := &dockerDeployer{inflight: make(map[string]*containerLock)}
	err = d.PullAndUpdate(context.Background(), Target{
		Image:     "ghcr.io/martynvdijke/gitlens",
		Container: "gitlens",
	}, "latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var run []string
	for _, c := range rec.allCmds() {
		if c[1] == "run" {
			run = c
			break
		}
	}
	if run == nil {
		t.Fatalf("expected a helper docker run, got %v", rec.allCmds())
	}
	joined := strings.Join(run, " ")
	for _, want := range []string{
		"docker run -d --name gitlens-deploy-",
		"-v /var/run/docker.sock:/var/run/docker.sock",
		"test/helper:1 sh -c",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("helper run missing %q: %s", want, joined)
		}
	}
	script := run[len(run)-1]
	for _, want := range []string{
		"docker stop 'gitlens'",
		"docker rm 'gitlens'",
		"docker create --name 'gitlens'",
		"'--network' 'deathstar_default'",
		"'-v' '/config/datey:/config'",
		"docker start 'gitlens'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("helper script missing %q: %s", want, script)
		}
	}
	// The helper sequence must wait and then clean up.
	for _, want := range []string{"wait", "logs", "rm"} {
		if !hasVerb(rec.allCmds(), want) {
			t.Errorf("expected docker %s after launching the helper", want)
		}
	}
}

func hasVerb(cmds [][]string, verb string) bool {
	for _, c := range cmds {
		if len(c) > 1 && c[1] == verb {
			return true
		}
	}
	return false
}

// ---- composeDeployer ----

func TestComposeDeployer_UsesHelper(t *testing.T) {
	t.Setenv("DEPLOY_HELPER_IMAGE", "test/helper:1")

	rec := &runRecorder{respond: func(full []string) ([]byte, error) {
		switch {
		case full[1] == "inspect":
			return []byte("deathstar|/root/homelab/deathstar|gitlens"), nil
		case full[1] == "wait":
			return []byte("0"), nil
		}
		return nil, nil
	}}
	installRecorder(t, rec)

	d := &composeDeployer{}
	err := d.PullAndUpdate(context.Background(), Target{Container: "gitlens"}, "latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var run []string
	for _, c := range rec.allCmds() {
		if c[1] == "run" {
			run = c
			break
		}
	}
	if run == nil {
		t.Fatalf("expected a helper docker run, got %v", rec.allCmds())
	}
	joined := strings.Join(run, " ")
	for _, want := range []string{
		"-v /var/run/docker.sock:/var/run/docker.sock",
		"-v /root/homelab/deathstar:/root/homelab/deathstar",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("helper run missing %q: %s", want, joined)
		}
	}
	script := run[len(run)-1]
	for _, want := range []string{
		"docker compose -p 'deathstar' --project-directory '/root/homelab/deathstar' pull 'gitlens'",
		"docker compose -p 'deathstar' --project-directory '/root/homelab/deathstar' up -d --no-deps 'gitlens'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("helper script missing %q: %s", want, script)
		}
	}
}

func TestComposeDeployer_FallsBackToCwd(t *testing.T) {
	rec := &runRecorder{respond: func(full []string) ([]byte, error) {
		if full[1] == "inspect" {
			return []byte("||"), nil // not compose-managed
		}
		return nil, nil
	}}
	installRecorder(t, rec)

	d := &composeDeployer{}
	err := d.PullAndUpdate(context.Background(), Target{Container: "standalone"}, "latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got, want := strings.Join(rec.verbOrder(), ","), "inspect,compose,compose"; got != want {
		t.Fatalf("command order = %s, want %s", got, want)
	}
	cmds := rec.allCmds()
	if !strings.Contains(strings.Join(cmds[1], " "), "compose pull standalone") {
		t.Errorf("expected legacy compose pull, got %v", cmds[1])
	}
	if !strings.Contains(strings.Join(cmds[2], " "), "compose up -d --no-deps standalone") {
		t.Errorf("expected legacy compose up, got %v", cmds[2])
	}
}

// ---- helper exit handling ----

func TestRunHelper_NonZeroExit_ReturnsError(t *testing.T) {
	t.Setenv("DEPLOY_HELPER_IMAGE", "test/helper:1")

	rec := &runRecorder{respond: func(full []string) ([]byte, error) {
		switch {
		case full[1] == "wait":
			return []byte("1"), nil
		case full[1] == "logs":
			return []byte("docker: error pulling image"), nil
		}
		return nil, nil
	}}
	installRecorder(t, rec)

	err := runHelper(context.Background(), "exit 1", nil)
	if err == nil {
		t.Fatal("expected error for non-zero helper exit")
	}
	if !strings.Contains(err.Error(), "docker: error pulling image") {
		t.Errorf("error should include helper logs, got: %v", err)
	}
}
