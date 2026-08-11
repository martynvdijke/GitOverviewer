// Package provider defines the Provider interface that abstracts over
// Git hosting backends (currently GitHub and Forgejo). The concrete
// implementations live in internal/github and internal/forgejo.
//
// The interface deliberately reuses the DTOs declared in internal/github
// (Repository, Commit, Release, PullRequest, User, WorkflowRun). The
// Forgejo client maps Gitea-shaped JSON into these same types so the
// syncer and handlers can stay provider-agnostic.
package provider

import (
	"context"
	"time"

	ghclient "gitlens/internal/github"
)

// Provider is the surface area a git host must implement to be a
// drop-in target for GitLens. All per-repo read methods take a token
// (the user's access token for this provider) plus the standard
// owner/repo coordinates.
type Provider interface {
	// Name returns the canonical provider key ("github" or "forgejo").
	Name() string

	// AuthURL returns the URL the browser should be redirected to in
	// order to start an OAuth flow. `state` is an opaque CSRF token;
	// `redirectURL` is the callback URL registered with the OAuth app.
	AuthURL(state, redirectURL string) string

	// ExchangeCode exchanges an OAuth authorization code for an
	// access token. It returns the access token plus the user
	// identity fetched from `/user`.
	ExchangeCode(ctx context.Context, code, redirectURL string) (accessToken string, user *ghclient.User, err error)

	// GetUser returns the authenticated user for the given token.
	GetUser(ctx context.Context, token string) (*ghclient.User, error)

	// ListRepositories returns every repository the token has access to
	// (owned + collaborator).
	ListRepositories(ctx context.Context, token string) ([]*ghclient.Repository, error)

	// GetCommitsSince returns commits on `branch` newer than `since`.
	// If since is the zero time, returns up to `maxCommits` recent commits.
	GetCommitsSince(ctx context.Context, token, owner, repo, branch string, since time.Time, maxCommits int) ([]*ghclient.Commit, error)

	// ListCommitsPage fetches a single page of commits (newest first)
	// from `branch`. page is 1-indexed; perPage is clamped to the
	// provider's maximum. hasMore reports whether another page likely
	// exists. Used by the historical backfill walker.
	ListCommitsPage(ctx context.Context, token, owner, repo, branch string, page, perPage int) (commits []*ghclient.Commit, hasMore bool, err error)

	// ListReleases returns all releases for the repo, newest first.
	ListReleases(ctx context.Context, token, owner, repo string) ([]*ghclient.Release, error)

	// ListPullRequests returns the open pull requests for the repo.
	ListPullRequests(ctx context.Context, token, owner, repo string) ([]*ghclient.PullRequest, error)

	// ListRecentlyMergedPRs returns the most recently merged pull
	// requests for the repo (closed + merged=true).
	ListRecentlyMergedPRs(ctx context.Context, token, owner, repo string) ([]*ghclient.PullRequest, error)

	// MergePullRequest merges an open PR using the provider's default
	// merge method. Returns (false, message, nil) when the server
	// refuses the merge (conflicts, failing checks, etc.).
	MergePullRequest(ctx context.Context, token, owner, repo string, number int) (merged bool, message string, err error)

	// ClosePullRequest closes an open PR without merging it.
	ClosePullRequest(ctx context.Context, token, owner, repo string, number int) error

	// GetLatestWorkflowRun returns the most recent completed workflow
	// run for `branch`. Providers without a workflow concept (e.g.
	// Forgejo Actions may be disabled) may return ("", nil) to mean
	// "unknown".
	GetLatestWorkflowRun(ctx context.Context, token, owner, repo, branch string) (*ghclient.WorkflowRun, error)
}
