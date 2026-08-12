package deploy

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestDiscoverTargets_NoLabeledContainers(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", "")
	}

	targets, err := DiscoverTargets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("expected 0 targets, got %d", len(targets))
	}
}

func TestDiscoverTargets_DockerUnavailable(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "exit 1")
	}

	targets, err := DiscoverTargets(context.Background())
	if err == nil {
		t.Fatal("expected error for docker ps failure")
	}
	if targets != nil {
		t.Fatalf("expected nil targets, got %v", targets)
	}
}

func TestDiscoverTargets_InvalidLabel(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && len(args) >= 2 && args[0] == "ps" {
			return exec.CommandContext(ctx, "echo", "abc123")
		}
		if name == "docker" && args[0] == "inspect" {
			// Container with empty label value
			return exec.CommandContext(ctx, "echo", `[{"Name":"/test","Config":{"Image":"img:latest","Labels":{"gitlens.deploy.target":""}}}]`)
		}
		return exec.CommandContext(ctx, "echo", "")
	}

	targets, err := DiscoverTargets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("expected 0 targets for empty label, got %d", len(targets))
	}
}

func TestDiscoverTargets_InvalidLabelNoSlash(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && len(args) >= 2 && args[0] == "ps" {
			return exec.CommandContext(ctx, "echo", "abc123")
		}
		if name == "docker" && args[0] == "inspect" {
			return exec.CommandContext(ctx, "echo", `[{"Name":"/test","Config":{"Image":"img:latest","Labels":{"gitlens.deploy.target":"noslash"}}}]`)
		}
		return exec.CommandContext(ctx, "echo", "")
	}

	targets, err := DiscoverTargets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("expected 0 targets for invalid label value, got %d", len(targets))
	}
}

func TestDiscoverTargets_LatestTag(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && len(args) >= 2 && args[0] == "ps" {
			return exec.CommandContext(ctx, "echo", "abc123")
		}
		if name == "docker" && args[0] == "inspect" {
			return exec.CommandContext(ctx, "echo", `[{"Name":"/gitlens","Config":{"Image":"ghcr.io/martynvdijke/gitlens:latest","Labels":{"gitlens.deploy.target":"martynvdijke/gitlens"}}}]`)
		}
		return exec.CommandContext(ctx, "echo", "")
	}

	targets, err := DiscoverTargets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].Repository != "martynvdijke/gitlens" {
		t.Fatalf("expected repo martynvdijke/gitlens, got %s", targets[0].Repository)
	}
	if targets[0].Image != "ghcr.io/martynvdijke/gitlens" {
		t.Fatalf("expected image ghcr.io/martynvdijke/gitlens, got %s", targets[0].Image)
	}
	if targets[0].Container != "gitlens" {
		t.Fatalf("expected container gitlens, got %s", targets[0].Container)
	}
	if targets[0].TagStrategy != TagStrategyLatest {
		t.Fatalf("expected TagStrategyLatest, got %s", targets[0].TagStrategy)
	}
}

func TestDiscoverTargets_VersionedTag(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && len(args) >= 2 && args[0] == "ps" {
			return exec.CommandContext(ctx, "echo", "def456")
		}
		if name == "docker" && args[0] == "inspect" {
			return exec.CommandContext(ctx, "echo", `[{"Name":"/my-app","Config":{"Image":"my-registry.io/org/app:v1.2.3","Labels":{"gitlens.deploy.target":"org/app"}}}]`)
		}
		return exec.CommandContext(ctx, "echo", "")
	}

	targets, err := DiscoverTargets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].Image != "my-registry.io/org/app" {
		t.Fatalf("expected image my-registry.io/org/app, got %s", targets[0].Image)
	}
	if targets[0].TagStrategy != TagStrategyReleaseTag {
		t.Fatalf("expected TagStrategyReleaseTag, got %s", targets[0].TagStrategy)
	}
}

func TestMergeTargets_NoOverlap(t *testing.T) {
	explicit := []Target{
		{Repository: "org/alpha", Image: "img1", Container: "c1"},
	}
	discovered := []Target{
		{Repository: "org/beta", Image: "img2", Container: "c2"},
	}
	merged := MergeTargets(explicit, discovered)
	if len(merged) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(merged))
	}
}

func TestMergeTargets_ExplicitWins(t *testing.T) {
	explicit := []Target{
		{Repository: "org/repo", Image: "explicit-img", Container: "explicit-c", TagStrategy: TagStrategyReleaseTag},
	}
	discovered := []Target{
		{Repository: "org/repo", Image: "discovered-img", Container: "discovered-c", TagStrategy: TagStrategyLatest},
	}
	merged := MergeTargets(explicit, discovered)
	if len(merged) != 1 {
		t.Fatalf("expected 1 target, got %d", len(merged))
	}
	if merged[0].Image != "explicit-img" {
		t.Fatalf("expected explicit-img, got %s", merged[0].Image)
	}
	if merged[0].Container != "explicit-c" {
		t.Fatalf("expected explicit-c, got %s", merged[0].Container)
	}
}

func TestLoadAllTargets_DiscoveryErrorFallsBack(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	// Make docker ps fail
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "exit 1")
	}

	targets, err := LoadAllTargets()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return whatever LoadTargets returns (no env set = nil, nil)
	if targets != nil {
		t.Fatalf("expected nil targets when no config and docker fails, got %v", targets)
	}
}

// ---- Cross-registry / cross-org scaling tests ----

func TestDiscoverTargets_GHCRDiffrentOrgs(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && len(args) >= 2 && args[0] == "ps" {
			return exec.CommandContext(ctx, "echo", "c1\nc2")
		}
		if name == "docker" && args[0] == "inspect" {
			return exec.CommandContext(ctx, "echo", `[
				{"Name":"/app1","Config":{"Image":"ghcr.io/martynvandijke/app-one:latest","Labels":{"gitlens.deploy.target":"martynvandijke/app-one"}}},
				{"Name":"/app2","Config":{"Image":"ghcr.io/martynvandijke/app-two:v1.5.0","Labels":{"gitlens.deploy.target":"martynvandijke/app-two"}}}
			]`)
		}
		return exec.CommandContext(ctx, "echo", "")
	}

	targets, err := DiscoverTargets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets from ghcr.io/martynvandijke/*, got %d", len(targets))
	}
	// app-one: :latest → TagStrategyLatest
	if targets[0].Repository != "martynvandijke/app-one" {
		t.Fatalf("expected martynvandijke/app-one, got %s", targets[0].Repository)
	}
	if targets[0].Image != "ghcr.io/martynvandijke/app-one" {
		t.Fatalf("expected ghcr.io/martynvandijke/app-one, got %s", targets[0].Image)
	}
	if targets[0].TagStrategy != TagStrategyLatest {
		t.Fatalf("expected TagStrategyLatest for :latest tag, got %s", targets[0].TagStrategy)
	}
	// app-two: v1.5.0 → TagStrategyReleaseTag
	if targets[1].Repository != "martynvandijke/app-two" {
		t.Fatalf("expected martynvandijke/app-two, got %s", targets[1].Repository)
	}
	if targets[1].Image != "ghcr.io/martynvandijke/app-two" {
		t.Fatalf("expected ghcr.io/martynvandijke/app-two, got %s", targets[1].Image)
	}
	if targets[1].TagStrategy != TagStrategyReleaseTag {
		t.Fatalf("expected TagStrategyReleaseTag for versioned tag, got %s", targets[1].TagStrategy)
	}
}

func TestDiscoverTargets_DockerHubAndQuay(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && len(args) >= 2 && args[0] == "ps" {
			return exec.CommandContext(ctx, "echo", "c1\nc2\nc3")
		}
		if name == "docker" && args[0] == "inspect" {
			return exec.CommandContext(ctx, "echo", `[
				{"Name":"/nginx-proxy","Config":{"Image":"nginx:latest","Labels":{"gitlens.deploy.target":"nginx/nginx-proxy"}}},
				{"Name":"/postgres","Config":{"Image":"postgres:16-alpine","Labels":{"gitlens.deploy.target":"postgres/postgres"}}},
				{"Name":"/quay-app","Config":{"Image":"quay.io/org/my-app:v3.0.0","Labels":{"gitlens.deploy.target":"org/my-app"}}}
			]`)
		}
		return exec.CommandContext(ctx, "echo", "")
	}

	targets, err := DiscoverTargets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("expected 3 targets across Docker Hub + quay, got %d", len(targets))
	}
	// Docker Hub official image: nginx:latest
	if targets[0].Image != "nginx" {
		t.Fatalf("expected nginx (Docker Hub), got %s", targets[0].Image)
	}
	if targets[0].TagStrategy != TagStrategyLatest {
		t.Fatalf("expected TagStrategyLatest for :latest, got %s", targets[0].TagStrategy)
	}
	// Docker Hub with tag: postgres:16-alpine
	if targets[1].Image != "postgres" {
		t.Fatalf("expected postgres (Docker Hub), got %s", targets[1].Image)
	}
	if targets[1].TagStrategy != TagStrategyReleaseTag {
		t.Fatalf("expected TagStrategyReleaseTag for :16-alpine, got %s", targets[1].TagStrategy)
	}
	// Quay.io
	if targets[2].Image != "quay.io/org/my-app" {
		t.Fatalf("expected quay.io/org/my-app, got %s", targets[2].Image)
	}
	if targets[2].Container != "quay-app" {
		t.Fatalf("expected container quay-app, got %s", targets[2].Container)
	}
}

func TestDiscoverTargets_PrivateRegistryWithPort(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && len(args) >= 2 && args[0] == "ps" {
			return exec.CommandContext(ctx, "echo", "c1")
		}
		if name == "docker" && args[0] == "inspect" {
			return exec.CommandContext(ctx, "echo", `[{"Name":"/my-service","Config":{"Image":"registry.example.com:5000/my-team/service:latest","Labels":{"gitlens.deploy.target":"my-team/service"}}}]`)
		}
		return exec.CommandContext(ctx, "echo", "")
	}

	targets, err := DiscoverTargets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target from private registry, got %d", len(targets))
	}
	// splitImageTag must correctly handle host:port syntax.
	// "registry.example.com:5000/my-team/service:latest" — last colon is before "latest"
	if targets[0].Image != "registry.example.com:5000/my-team/service" {
		t.Fatalf("expected registry.example.com:5000/my-team/service (port preserved), got %s", targets[0].Image)
	}
	if targets[0].TagStrategy != TagStrategyLatest {
		t.Fatalf("expected TagStrategyLatest, got %s", targets[0].TagStrategy)
	}
	if targets[0].Container != "my-service" {
		t.Fatalf("expected container my-service, got %s", targets[0].Container)
	}
}

func TestDiscoverTargets_NoExplicitTagDefaultsToLatest(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && len(args) >= 2 && args[0] == "ps" {
			return exec.CommandContext(ctx, "echo", "c1")
		}
		if name == "docker" && args[0] == "inspect" {
			// Image "alpine" has no colon — no explicit tag → defaults to :latest
			return exec.CommandContext(ctx, "echo", `[{"Name":"/alpine-box","Config":{"Image":"alpine","Labels":{"gitlens.deploy.target":"alpine/box"}}}]`)
		}
		return exec.CommandContext(ctx, "echo", "")
	}

	targets, err := DiscoverTargets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target from tagless image, got %d", len(targets))
	}
	if targets[0].Image != "alpine" {
		t.Fatalf("expected alpine, got %s", targets[0].Image)
	}
	if targets[0].TagStrategy != TagStrategyLatest {
		t.Fatalf("expected TagStrategyLatest for tagless image (defaults to :latest), got %s", targets[0].TagStrategy)
	}
}

func TestDiscoverTargets_MixedRegistriesSingleRun(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && len(args) >= 2 && args[0] == "ps" {
			return exec.CommandContext(ctx, "echo", "c1\nc2\nc3\nc4")
		}
		if name == "docker" && args[0] == "inspect" {
			return exec.CommandContext(ctx, "echo", `[
				{"Name":"/a","Config":{"Image":"ghcr.io/martynvandijke/apple:latest","Labels":{"gitlens.deploy.target":"martynvandijke/apple"}}},
				{"Name":"/b","Config":{"Image":"docker.io/library/busybox:1.36","Labels":{"gitlens.deploy.target":"busybox/busybox"}}},
				{"Name":"/c","Config":{"Image":"quay.io/prometheus/node-exporter:v1.7.0","Labels":{"gitlens.deploy.target":"prometheus/node-exporter"}}},
				{"Name":"/d","Config":{"Image":"registry.internal:5000/team/db:latest","Labels":{"gitlens.deploy.target":"team/db"}}}
			]`)
		}
		return exec.CommandContext(ctx, "echo", "")
	}

	targets, err := DiscoverTargets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 4 {
		t.Fatalf("expected 4 targets from 4 different registries, got %d", len(targets))
	}
	// Verify all four registries are preserved
	images := map[string]string{
		targets[0].Image: targets[0].Container,
		targets[1].Image: targets[1].Container,
		targets[2].Image: targets[2].Container,
		targets[3].Image: targets[3].Container,
	}
	if images["ghcr.io/martynvandijke/apple"] != "a" {
		t.Fatal("missing or wrong ghcr.io image")
	}
	if images["docker.io/library/busybox"] != "b" {
		t.Fatal("missing or wrong docker.io image")
	}
	if images["quay.io/prometheus/node-exporter"] != "c" {
		t.Fatal("missing or wrong quay.io image")
	}
	if images["registry.internal:5000/team/db"] != "d" {
		t.Fatal("missing or wrong private registry image")
	}
}

// ---- DiscoverContainerStatus ----

func TestDiscoverContainerStatus_TrackedAndInvalid(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && len(args) >= 2 && args[0] == "ps" {
			return exec.CommandContext(ctx, "echo", "id1\nid2\nid3")
		}
		if name == "docker" && args[0] == "inspect" {
			return exec.CommandContext(ctx, "echo", `[
				{"Name":"/app1","Config":{"Image":"img1:latest","Labels":{"gitlens.deploy.target":"org/app1"}}},
				{"Name":"/bad","Config":{"Image":"img2:latest","Labels":{"gitlens.deploy.target":"noslash"}}},
				{"Name":"/app3","Config":{"Image":"img3:v1.0","Labels":{"gitlens.deploy.target":"org/app3"}}}
			]`)
		}
		return exec.CommandContext(ctx, "echo", "")
	}

	statuses, err := DiscoverContainerStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(statuses) != 3 {
		t.Fatalf("expected 3 statuses, got %d", len(statuses))
	}
	if statuses[0].Container != "app1" || !statuses[0].Tracked {
		t.Fatalf("expected app1 tracked, got %+v", statuses[0])
	}
	if statuses[0].Image != "img1" || statuses[0].Tag != "latest" {
		t.Fatalf("expected img1:latest, got %s:%s", statuses[0].Image, statuses[0].Tag)
	}
	if statuses[1].Tracked {
		t.Fatalf("expected bad label not tracked, got %+v", statuses[1])
	}
	if !strings.Contains(statuses[1].Reason, "invalid label") {
		t.Fatalf("expected invalid-label reason, got %q", statuses[1].Reason)
	}
	if !statuses[2].Tracked || statuses[2].Tag != "v1.0" {
		t.Fatalf("expected app3 tracked with tag v1.0, got %+v", statuses[2])
	}
}

func TestDiscoverContainerStatus_ExplicitOverrides(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	t.Setenv("DEPLOY_TARGETS", `[{"repository":"org/app1","image":"explicit-img","container":"explicit-c","tag_strategy":"latest"}]`)

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && len(args) >= 2 && args[0] == "ps" {
			return exec.CommandContext(ctx, "echo", "id1")
		}
		if name == "docker" && args[0] == "inspect" {
			return exec.CommandContext(ctx, "echo", `[{"Name":"/app1","Config":{"Image":"img1:latest","Labels":{"gitlens.deploy.target":"org/app1"}}}]`)
		}
		return exec.CommandContext(ctx, "echo", "")
	}

	statuses, err := DiscoverContainerStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].Tracked {
		t.Fatalf("expected labeled container overridden by explicit target to be untracked, got %+v", statuses[0])
	}
	if !strings.Contains(statuses[0].Reason, "explicit") {
		t.Fatalf("expected explicit-override reason, got %q", statuses[0].Reason)
	}
}

func TestDiscoverContainerStatus_NoLabeledContainers(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", "")
	}

	statuses, err := DiscoverContainerStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("expected 0 statuses, got %d", len(statuses))
	}
}

// ---- splitImageTag edge cases ----

func TestSplitImageTag_RegistryPort(t *testing.T) {
	img, tag := splitImageTag("registry.example.com:5000/my-image:v1.0")
	if img != "registry.example.com:5000/my-image" {
		t.Fatalf("expected registry.example.com:5000/my-image, got %s", img)
	}
	if tag != "v1.0" {
		t.Fatalf("expected tag v1.0, got %s", tag)
	}
}

func TestSplitImageTag_NoTag(t *testing.T) {
	img, tag := splitImageTag("alpine")
	if img != "alpine" {
		t.Fatalf("expected alpine, got %s", img)
	}
	if tag != "latest" {
		t.Fatalf("expected default tag latest, got %s", tag)
	}
}

func TestSplitImageTag_EmptyTag(t *testing.T) {
	// Trailing colon with empty tag — edge case
	img, tag := splitImageTag("image:")
	if img != "image" {
		t.Fatalf("expected image, got %s", img)
	}
	if tag != "" {
		t.Fatalf("expected empty tag, got %s", tag)
	}
}

func TestSplitImageTag_LongPath(t *testing.T) {
	img, tag := splitImageTag("ghcr.io/org/team/sub-image:v3.2.1-rc.1+build42")
	if img != "ghcr.io/org/team/sub-image" {
		t.Fatalf("expected ghcr.io/org/team/sub-image, got %s", img)
	}
	if tag != "v3.2.1-rc.1+build42" {
		t.Fatalf("expected tag v3.2.1-rc.1+build42, got %s", tag)
	}
}

// ---- containerToTarget direct unit tests ----

func TestContainerToTarget_MissingLabel(t *testing.T) {
	c := containerInspect{
		Name: "/my-container",
		Config: struct {
			Image  string
			Labels map[string]string
		}{Image: "img:latest", Labels: map[string]string{"other": "value"}},
	}
	target, err := containerToTarget(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != nil {
		t.Fatalf("expected nil target for container without label, got %+v", target)
	}
}

func TestContainerToTarget_EmptyRepoValue(t *testing.T) {
	c := containerInspect{
		Name: "/c",
		Config: struct {
			Image  string
			Labels map[string]string
		}{Image: "img:latest", Labels: map[string]string{"gitlens.deploy.target": ""}},
	}
	target, err := containerToTarget(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != nil {
		t.Fatalf("expected nil target for empty label value, got %+v", target)
	}
}

func TestDiscoverTargets_MultipleContainers(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && len(args) >= 2 && args[0] == "ps" {
			return exec.CommandContext(ctx, "echo", "id1\nid2")
		}
		if name == "docker" && args[0] == "inspect" {
			return exec.CommandContext(ctx, "echo", `[
				{"Name":"/app1","Config":{"Image":"img1:latest","Labels":{"gitlens.deploy.target":"org/app1"}}},
				{"Name":"/app2","Config":{"Image":"img2:v2.0","Labels":{"gitlens.deploy.target":"org/app2"}}}
			]`)
		}
		return exec.CommandContext(ctx, "echo", "")
	}

	targets, err := DiscoverTargets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
	if targets[0].Repository != "org/app1" || targets[0].TagStrategy != TagStrategyLatest {
		t.Fatalf("unexpected first target: %+v", targets[0])
	}
	if targets[1].Repository != "org/app2" || targets[1].TagStrategy != TagStrategyReleaseTag {
		t.Fatalf("unexpected second target: %+v", targets[1])
	}
}
