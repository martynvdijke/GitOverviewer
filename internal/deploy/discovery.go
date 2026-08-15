package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// execCommand is a mockable exec.CommandContext for testing.
var execCommand = exec.CommandContext

// DiscoverTargets inspects running Docker containers for the label
// gitlens.deploy.target and returns corresponding deploy targets.
// Containers are skipped if the label value is invalid or unparsable.
// Returns nil, nil when Docker is unavailable or no labels are found.
func DiscoverTargets(ctx context.Context) ([]Target, error) {
	containers, err := listLabeledContainers(ctx)
	if err != nil {
		return nil, err
	}

	var targets []Target
	for _, c := range containers {
		t, err := containerToTarget(c)
		if err != nil {
			log.Printf("Deploy: skip container %s: %v", c.Name, err)
			continue
		}
		if t != nil {
			targets = append(targets, *t)
		}
	}
	return targets, nil
}

// DiscoveredContainer describes a running container that carries the
// gitlens.deploy.target label, plus whether GitLens currently tracks it
// as a deploy target.
type DiscoveredContainer struct {
	Container string // docker container name
	Image     string // image without tag
	Tag       string // currently running image tag
	Label     string // gitlens.deploy.target value (owner/repo)
	Tracked   bool   // whether GitLens tracks this container via its label
	Reason    string // why it is or is not tracked
}

// DiscoverContainerStatus inspects running containers for the
// gitlens.deploy.target label and reports each one's tracking status
// relative to the explicit DEPLOY_TARGETS. A labeled container is tracked
// unless its label value is invalid (not owner/repo) or the same repository
// is configured explicitly (explicit targets win).
// Returns nil, nil when Docker is unavailable or no labels are found.
func DiscoverContainerStatus(ctx context.Context) ([]DiscoveredContainer, error) {
	explicit, err := LoadTargets()
	if err != nil {
		return nil, err
	}
	return discoverContainerStatus(ctx, explicit)
}

func discoverContainerStatus(ctx context.Context, explicit []Target) ([]DiscoveredContainer, error) {
	containers, err := listLabeledContainers(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]DiscoveredContainer, 0, len(containers))
	for _, c := range containers {
		img, tag := splitImageTag(c.Config.Image)
		dc := DiscoveredContainer{
			Container: strings.TrimPrefix(c.Name, "/"),
			Image:     img,
			Tag:       tag,
			Label:     c.Config.Labels["gitlens.deploy.target"],
		}

		switch {
		case !validRepoLabel(dc.Label):
			dc.Reason = fmt.Sprintf("invalid label value %q — expected owner/repo", dc.Label)
		case MatchTarget(explicit, dc.Label) != nil:
			dc.Reason = "repository configured explicitly — explicit target wins"
		default:
			dc.Tracked = true
			dc.Reason = "tracked via gitlens.deploy.target label"
		}
		out = append(out, dc)
	}
	return out, nil
}

// listLabeledContainers returns inspect data for every running container
// that carries the gitlens.deploy.target label.
func listLabeledContainers(ctx context.Context) ([]containerInspect, error) {
	// Step 1: find container IDs with the label.
	ps := execCommand(ctx, "docker", "ps", "-q", "--filter", "label=gitlens.deploy.target")
	out, err := ps.Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps failed: %w", err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil, nil
	}

	// Step 2: inspect all matched containers at once.
	args := append([]string{"inspect"}, ids...)
	insp := execCommand(ctx, "docker", args...)
	raw, err := insp.Output()
	if err != nil {
		return nil, fmt.Errorf("docker inspect failed: %w", err)
	}

	var containers []containerInspect
	if err := json.Unmarshal(raw, &containers); err != nil {
		return nil, fmt.Errorf("docker inspect parse failed: %w", err)
	}
	return containers, nil
}

// validRepoLabel reports whether v is a valid gitlens.deploy.target value
// (owner/repo format).
func validRepoLabel(v string) bool {
	return v != "" && strings.Count(v, "/") == 1
}

// containerToTarget converts a docker inspect result to a Target.
// Returns nil, nil if the container does not carry a valid gitlens.deploy.target label.
func containerToTarget(c containerInspect) (*Target, error) {
	repo, ok := c.Config.Labels["gitlens.deploy.target"]
	if !ok || repo == "" {
		return nil, nil
	}

	// Validate: must be owner/repo format.
	if !validRepoLabel(repo) {
		return nil, fmt.Errorf("invalid label value %q: expected owner/repo format", repo)
	}

	name := strings.TrimPrefix(c.Name, "/")
	image, tag := splitImageTag(c.Config.Image)
	strategy := tagStrategyFromTag(tag)

	return &Target{
		Repository:  repo,
		Image:       image,
		Container:   name,
		TagStrategy: strategy,
	}, nil
}

// splitImageTag splits "repo/image:tag" into ("repo/image", "tag").
// If there's no tag portion, "latest" is returned.
func splitImageTag(img string) (string, string) {
	idx := strings.LastIndex(img, ":")
	if idx < 0 {
		return img, "latest"
	}
	// If there's a colon but it's part of a port/registry (e.g. "host:port/image"),
	// the tag comes after the LAST colon.
	return img[:idx], img[idx+1:]
}

// tagStrategyFromTag returns TagStrategyLatest if tag is "latest", else TagStrategyReleaseTag.
func tagStrategyFromTag(tag string) TagStrategy {
	if tag == "latest" {
		return TagStrategyLatest
	}
	return TagStrategyReleaseTag
}

// MergeTargets merges explicit and discovered targets.
// Explicit targets take priority when the same repository appears in both.
func MergeTargets(explicit, discovered []Target) []Target {
	seen := make(map[string]bool, len(explicit))
	result := make([]Target, 0, len(explicit)+len(discovered))

	for _, t := range explicit {
		seen[t.Repository] = true
		result = append(result, t)
	}
	for _, t := range discovered {
		if !seen[t.Repository] {
			seen[t.Repository] = true
			result = append(result, t)
		}
	}
	return result
}

// LoadAllTargets combines explicit DEPLOY_TARGETS with auto-discovered
// container label targets. Discovery is best-effort — if Docker is
// unavailable, explicit targets are returned with a warning log.
func LoadAllTargets() ([]Target, error) {
	explicit, err := LoadTargets()
	if err != nil {
		return nil, err
	}

	discovered, err := DiscoverTargets(context.Background())
	if err != nil {
		log.Printf("Deploy: container label discovery failed: %v (using explicit targets only)", err)
		return explicit, nil
	}

	return MergeTargets(explicit, discovered), nil
}
