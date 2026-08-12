package provider

import (
	"context"
	"time"

	"gitlens/internal/forgejo"
	ghclient "gitlens/internal/github"
)

// ForgejoAdapter wraps a *forgejo.Client so it satisfies the Provider
// interface. Forgejo-specific extras (instance URL picker) are exposed
// via the wrapper rather than the interface, so callers that need them
// can type-assert.
type ForgejoAdapter struct {
	*forgejo.Client
}

func NewForgejoAdapter(c *forgejo.Client) *ForgejoAdapter {
	return &ForgejoAdapter{Client: c}
}

func (a *ForgejoAdapter) Name() string { return "forgejo" }

// RerunFailedWorkflowRuns implements provider.Provider. Forgejo Actions
// is opt-in and GitLens does not surface Forgejo workflow runs, so
// re-running builds is not supported here.
func (a *ForgejoAdapter) RerunFailedWorkflowRuns(ctx context.Context, token, owner, repo string, prNumber int) (int, error) {
	_ = ctx
	_ = token
	_ = owner
	_ = repo
	_ = prNumber
	return 0, ErrUnsupported
}

// All other methods are inherited from *forgejo.Client and already
// match the Provider interface. The compile-time check below
// confirms it.
var _ Provider = (*ForgejoAdapter)(nil)

// Keep imports alive that are only referenced by the embedded client's
// own interface conformance (checked indirectly via ForgejoAdapter).
var (
	_ = context.Background
	_ = time.Now
	_ = (*ghclient.User)(nil)
)
