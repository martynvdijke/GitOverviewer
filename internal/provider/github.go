package provider

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	ghclient "gitlens/internal/github"
)

// GitHubAdapter wraps a *github.Client so it satisfies the Provider
// interface. We can't declare interface conformance inside the github
// package itself because that would create an import cycle (the
// provider package already imports github for its DTOs).
type GitHubAdapter struct {
	*ghclient.Client
}

func NewGitHubAdapter(c *ghclient.Client) *GitHubAdapter {
	return &GitHubAdapter{Client: c}
}

func (a *GitHubAdapter) Name() string { return "github" }

func (a *GitHubAdapter) AuthURL(state, redirectURL string) string {
	return a.Client.AuthorizeURL(state, redirectURL)
}

func (a *GitHubAdapter) ExchangeCode(ctx context.Context, code, redirectURL string) (string, *ghclient.User, error) {
	_ = ctx
	_ = redirectURL
	tok, err := a.Client.GetAccessToken(code)
	if err != nil {
		return "", nil, err
	}
	u, err := a.Client.GetUser(tok)
	if err != nil {
		return "", nil, err
	}
	return tok, u, nil
}

func (a *GitHubAdapter) GetUser(ctx context.Context, token string) (*ghclient.User, error) {
	_ = ctx
	return a.Client.GetUser(token)
}

func (a *GitHubAdapter) ListRepositories(ctx context.Context, token string) ([]*ghclient.Repository, error) {
	_ = ctx
	return a.Client.ListRepositories(token)
}

func (a *GitHubAdapter) GetCommitsSince(ctx context.Context, token, owner, repo, branch string, since time.Time, maxCommits int) ([]*ghclient.Commit, error) {
	_ = ctx
	return a.Client.GetCommitsSince(token, owner, repo, branch, since, maxCommits)
}

func (a *GitHubAdapter) ListCommitsPage(ctx context.Context, token, owner, repo, branch string, page, perPage int) ([]*ghclient.Commit, bool, error) {
	return a.Client.ListCommitsPage(ctx, token, owner, repo, branch, page, perPage)
}

func (a *GitHubAdapter) ListReleases(ctx context.Context, token, owner, repo string) ([]*ghclient.Release, error) {
	_ = ctx
	return a.Client.ListReleases(token, owner, repo)
}

func (a *GitHubAdapter) ListPullRequests(ctx context.Context, token, owner, repo string) ([]*ghclient.PullRequest, error) {
	_ = ctx
	return a.Client.ListPullRequests(token, owner, repo)
}

func (a *GitHubAdapter) ListRecentlyMergedPRs(ctx context.Context, token, owner, repo string) ([]*ghclient.PullRequest, error) {
	_ = ctx
	return a.Client.ListRecentlyMergedPRs(token, owner, repo)
}

func (a *GitHubAdapter) GetLatestWorkflowRun(ctx context.Context, token, owner, repo, branch string) (*ghclient.WorkflowRun, error) {
	_ = ctx
	return a.Client.GetLatestWorkflowRun(token, owner, repo, branch)
}

func (a *GitHubAdapter) MergePullRequest(ctx context.Context, token, owner, repo string, number int) (bool, string, error) {
	_ = ctx
	return a.Client.MergePullRequest(token, owner, repo, number)
}

func (a *GitHubAdapter) ClosePullRequest(ctx context.Context, token, owner, repo string, number int) error {
	_ = ctx
	return a.Client.ClosePullRequest(token, owner, repo, number)
}

// RerunFailedWorkflowRuns re-queues the failed jobs of every completed,
// re-runnable workflow run for the PR's head commit. It matches runs to
// the PR by head SHA (the same strategy the syncer uses for build status)
// and re-runs each qualifying run individually. Per-run failures are
// logged and aggregated rather than aborting the remaining runs; the
// aggregated error, if any, is returned alongside the success count.
func (a *GitHubAdapter) RerunFailedWorkflowRuns(ctx context.Context, token, owner, repo string, prNumber int) (int, error) {
	_ = ctx
	prs, err := a.Client.ListPullRequests(token, owner, repo)
	if err != nil {
		return 0, fmt.Errorf("listing pull requests for %s/%s: %w", owner, repo, err)
	}
	var headSHA string
	for _, pr := range prs {
		if pr.Number == prNumber {
			headSHA = pr.HeadSHA
			break
		}
	}
	if headSHA == "" {
		return 0, fmt.Errorf("pull request #%d not found in %s/%s", prNumber, owner, repo)
	}

	runs, err := a.Client.GetWorkflowRunsForRepo(token, owner, repo, 30)
	if err != nil {
		return 0, fmt.Errorf("listing workflow runs for %s/%s: %w", owner, repo, err)
	}

	var rerunCount int
	var failures []string
	for _, r := range runs {
		if r.HeadSHA != headSHA || r.Status != "completed" || !ghclient.IsRerunnableConclusion(r.Conclusion) {
			continue
		}
		if err := a.Client.RerunFailedJobs(token, owner, repo, r.ID); err != nil {
			log.Printf("Error re-running workflow run %d for %s/%s: %v", r.ID, owner, repo, err)
			failures = append(failures, fmt.Sprintf("run %d: %v", r.ID, err))
			continue
		}
		rerunCount++
	}

	if len(failures) > 0 {
		return rerunCount, fmt.Errorf("failed to re-run %d of %d workflow run(s) for %s/%s#%d: %s",
			len(failures), len(failures)+rerunCount, owner, repo, prNumber, strings.Join(failures, "; "))
	}
	return rerunCount, nil
}

// Compile-time interface check.
var _ Provider = (*GitHubAdapter)(nil)
