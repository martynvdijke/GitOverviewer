package sync

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gitlens/ent"
	"gitlens/ent/enttest"
	ghclient "gitlens/internal/github"
	"gitlens/internal/provider"
	"gitlens/internal/ws"

	_ "github.com/mattn/go-sqlite3"
)

// fakeProvider is a test double for provider.Provider. Only the methods
// exercised by the backfill walker and commit sync are functional.
type fakeProvider struct {
	pages        map[int]fakePage // page -> result
	errPages     map[int]error    // page -> injected error
	commitsSince []*ghclient.Commit
	listPRs      []*ghclient.PullRequest
}

type fakePage struct {
	commits []*ghclient.Commit
	hasMore bool
}

func (f *fakeProvider) Name() string                             { return "fake" }
func (f *fakeProvider) AuthURL(state, redirectURL string) string { return "" }
func (f *fakeProvider) ExchangeCode(ctx context.Context, code, redirectURL string) (string, *ghclient.User, error) {
	return "", nil, fmt.Errorf("not implemented")
}
func (f *fakeProvider) GetUser(ctx context.Context, token string) (*ghclient.User, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeProvider) ListRepositories(ctx context.Context, token string) ([]*ghclient.Repository, error) {
	return nil, nil
}
func (f *fakeProvider) GetCommitsSince(ctx context.Context, token, owner, repo, branch string, since time.Time, maxCommits int) ([]*ghclient.Commit, error) {
	return f.commitsSince, nil
}
func (f *fakeProvider) ListCommitsPage(ctx context.Context, token, owner, repo, branch string, page, perPage int) ([]*ghclient.Commit, bool, error) {
	if err, ok := f.errPages[page]; ok {
		return nil, false, err
	}
	p, ok := f.pages[page]
	if !ok {
		return nil, false, nil
	}
	return p.commits, p.hasMore, nil
}
func (f *fakeProvider) ListReleases(ctx context.Context, token, owner, repo string) ([]*ghclient.Release, error) {
	return nil, nil
}
func (f *fakeProvider) ListPullRequests(ctx context.Context, token, owner, repo string) ([]*ghclient.PullRequest, error) {
	return f.listPRs, nil
}
func (f *fakeProvider) ListRecentlyMergedPRs(ctx context.Context, token, owner, repo string) ([]*ghclient.PullRequest, error) {
	return nil, nil
}
func (f *fakeProvider) MergePullRequest(ctx context.Context, token, owner, repo string, number int) (bool, string, error) {
	return false, "", fmt.Errorf("not implemented")
}
func (f *fakeProvider) ClosePullRequest(ctx context.Context, token, owner, repo string, number int) error {
	return fmt.Errorf("not implemented")
}
func (f *fakeProvider) GetLatestWorkflowRun(ctx context.Context, token, owner, repo, branch string) (*ghclient.WorkflowRun, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeProvider) RerunFailedWorkflowRuns(ctx context.Context, token, owner, repo string, prNumber int) (int, error) {
	return 0, fmt.Errorf("not implemented")
}

func newFakeSyncer(t *testing.T, fake provider.Provider) (*Syncer, *ent.Client) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
	ghClient := ghclient.NewClient("", "")
	syncer := NewSyncer(client, ghClient, map[string]provider.Provider{"fake": fake}, ws.NewHub())
	return syncer, client
}

func createBackfillRepo(t *testing.T, client *ent.Client) *ent.Repository {
	t.Helper()
	ctx := context.Background()
	u, err := client.User.Create().
		SetGithubID(42).
		SetLogin("bfuser").
		SetAccessToken("token").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	repo, err := client.Repository.Create().
		SetGithubID(4242).SetOwner("bfuser").SetName("bf-repo").
		SetFullName("bfuser/bf-repo").SetHTMLURL("https://example.com/bfuser/bf-repo").
		SetDefaultBranch("main").SetUserID(u.ID).
		SetProvider("fake").
		Save(ctx)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	return repo
}

func commitOn(sha, day string) *ghclient.Commit {
	d, _ := time.Parse("2006-01-02", day)
	return &ghclient.Commit{SHA: sha, Message: "x", Date: d}
}

func TestBackfillRepo_FullHistory(t *testing.T) {
	fake := &fakeProvider{
		pages: map[int]fakePage{
			1: {commits: []*ghclient.Commit{
				commitOn("a1", "2025-03-11"), commitOn("a2", "2025-03-10"), commitOn("a3", "2025-03-10"),
			}, hasMore: true},
			2: {commits: []*ghclient.Commit{
				commitOn("a4", "2025-01-05"),
				commitOn("a5", "2019-12-31"), // pre-horizon: skipped, stops the walk
			}, hasMore: true},
		},
	}
	syncer, client := newFakeSyncer(t, fake)
	repo := createBackfillRepo(t, client)
	ctx := context.Background()

	syncer.BackfillRepo(ctx, repo.ID)

	repo = client.Repository.GetX(ctx, repo.ID)
	if repo.BackfillStatus != "done" {
		t.Fatalf("expected status done, got %q (err=%q)", repo.BackfillStatus, repo.BackfillError)
	}
	if repo.BackfillCursorPage != 0 {
		t.Errorf("expected cursor reset to 0, got %d", repo.BackfillCursorPage)
	}

	acts, err := client.CommitActivity.Query().All(ctx)
	if err != nil {
		t.Fatalf("query activities: %v", err)
	}
	got := map[string]int{}
	for _, a := range acts {
		got[a.Date.UTC().Format("2006-01-02")] = a.CommitCount
	}
	want := map[string]int{"2025-03-11": 1, "2025-03-10": 2, "2025-01-05": 1}
	if len(got) != len(want) {
		t.Fatalf("expected %d activity rows, got %d (%v)", len(want), len(got), got)
	}
	for day, count := range want {
		if got[day] != count {
			t.Errorf("day %s: expected %d, got %d", day, count, got[day])
		}
	}
}

func TestBackfillRepo_RateLimitPauseAndResume(t *testing.T) {
	fake := &fakeProvider{
		pages: map[int]fakePage{
			1: {commits: []*ghclient.Commit{
				commitOn("b1", "2025-05-02"), commitOn("b2", "2025-05-01"),
			}, hasMore: true},
			2: {commits: []*ghclient.Commit{
				commitOn("b3", "2025-04-30"),
			}, hasMore: false},
		},
		errPages: map[int]error{
			2: &ghclient.RateLimitError{RetryAfter: time.Now().Add(time.Minute), Status: "403"},
		},
	}
	syncer, client := newFakeSyncer(t, fake)
	repo := createBackfillRepo(t, client)
	ctx := context.Background()

	// First run hits the rate limit on page 2.
	syncer.BackfillRepo(ctx, repo.ID)

	repo = client.Repository.GetX(ctx, repo.ID)
	if repo.BackfillStatus != "pending" {
		t.Fatalf("expected status pending after rate limit, got %q", repo.BackfillStatus)
	}
	if repo.BackfillError == "" {
		t.Error("expected rate-limit message in backfill_error")
	}
	// The oldest unflushed day (2025-05-01) first appeared on page 1 and
	// may span into page 2, so the resume cursor restarts at page 1.
	if repo.BackfillCursorPage != 1 {
		t.Errorf("expected resume cursor 1, got %d", repo.BackfillCursorPage)
	}

	// Resume with the limit lifted: completes from the cursor.
	delete(fake.errPages, 2)
	syncer.BackfillRepo(ctx, repo.ID)

	repo = client.Repository.GetX(ctx, repo.ID)
	if repo.BackfillStatus != "done" {
		t.Fatalf("expected status done after resume, got %q (err=%q)", repo.BackfillStatus, repo.BackfillError)
	}
	acts, err := client.CommitActivity.Query().All(ctx)
	if err != nil {
		t.Fatalf("query activities: %v", err)
	}
	got := map[string]int{}
	for _, a := range acts {
		got[a.Date.UTC().Format("2006-01-02")] = a.CommitCount
	}
	want := map[string]int{"2025-05-02": 1, "2025-05-01": 1, "2025-04-30": 1}
	for day, count := range want {
		if got[day] != count {
			t.Errorf("day %s: expected %d, got %d", day, count, got[day])
		}
	}
}

func TestBackfillRepo_ProviderError(t *testing.T) {
	fake := &fakeProvider{
		errPages: map[int]error{1: fmt.Errorf("boom")},
	}
	syncer, client := newFakeSyncer(t, fake)
	repo := createBackfillRepo(t, client)
	ctx := context.Background()

	syncer.BackfillRepo(ctx, repo.ID)

	repo = client.Repository.GetX(ctx, repo.ID)
	if repo.BackfillStatus != "error" {
		t.Fatalf("expected status error, got %q", repo.BackfillStatus)
	}
	if repo.BackfillError == "" {
		t.Error("expected error message recorded")
	}
}

func TestTryStartBackfill_ConcurrencyGuard(t *testing.T) {
	syncer, client := newFakeSyncer(t, &fakeProvider{})
	repo := createBackfillRepo(t, client)
	ctx := context.Background()

	// Default status "pending" → claim succeeds and flips to running.
	if !syncer.tryStartBackfill(ctx, repo.ID) {
		t.Fatal("expected claim of pending repo to succeed")
	}
	repo = client.Repository.GetX(ctx, repo.ID)
	if repo.BackfillStatus != "running" {
		t.Fatalf("expected running, got %q", repo.BackfillStatus)
	}

	// Fresh running job → second claim fails.
	if syncer.tryStartBackfill(ctx, repo.ID) {
		t.Fatal("expected claim of fresh running repo to fail")
	}

	// Stale running job → claim succeeds (crash recovery).
	_, err := client.Repository.UpdateOneID(repo.ID).
		SetBackfillUpdatedAt(time.Now().Add(-backfillStaleAfter - time.Minute)).
		Save(ctx)
	if err != nil {
		t.Fatalf("make stale: %v", err)
	}
	if !syncer.tryStartBackfill(ctx, repo.ID) {
		t.Fatal("expected claim of stale running repo to succeed")
	}
}

func TestSetCommitActivity_AbsoluteSemantics(t *testing.T) {
	syncer, client := newFakeSyncer(t, &fakeProvider{})
	repo := createBackfillRepo(t, client)
	ctx := context.Background()

	syncer.setCommitActivity(ctx, repo.ID, map[string]int{"2025-02-01": 3})
	syncer.setCommitActivity(ctx, repo.ID, map[string]int{"2025-02-01": 3})
	syncer.setCommitActivity(ctx, repo.ID, map[string]int{"2025-02-01": 5})

	acts, err := client.CommitActivity.Query().All(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(acts) != 1 {
		t.Fatalf("expected 1 row, got %d", len(acts))
	}
	if acts[0].CommitCount != 5 {
		t.Errorf("expected absolute count 5 (overwrite, not increment), got %d", acts[0].CommitCount)
	}
}

func TestSyncCommits_TrimAtLatestSHA(t *testing.T) {
	fake := &fakeProvider{
		commitsSince: []*ghclient.Commit{
			commitOn("new1", "2025-06-03"),
			commitOn("new2", "2025-06-02"),
			commitOn("old1", "2025-06-01"), // already seen (LatestCommitSha)
			commitOn("old2", "2025-05-31"), // older than latest: must be trimmed
		},
	}
	syncer, client := newFakeSyncer(t, fake)
	ctx := context.Background()

	repo := createBackfillRepo(t, client)
	var err error
	repo, err = client.Repository.UpdateOne(repo).
		SetSyncedAt(time.Now()).
		SetLatestCommitSha("old1").
		SetLatestCommitDate(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)).
		Save(ctx)
	if err != nil {
		t.Fatalf("update repo: %v", err)
	}
	// Prevent MaybeStartBackfill from racing the test.
	repo.BackfillStatus = "done"
	repo, err = client.Repository.UpdateOne(repo).SetBackfillStatus("done").Save(ctx)
	if err != nil {
		t.Fatalf("set backfill done: %v", err)
	}

	u, err := repo.QueryUser().Only(ctx)
	if err != nil {
		t.Fatalf("query user: %v", err)
	}
	updated := client.Repository.UpdateOne(repo)
	syncer.syncCommits(ctx, fake, "token", u, repo, updated)
	if _, err := updated.Save(ctx); err != nil {
		t.Fatalf("save: %v", err)
	}

	acts, err := client.CommitActivity.Query().All(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	got := map[string]int{}
	for _, a := range acts {
		got[a.Date.UTC().Format("2006-01-02")] = a.CommitCount
	}
	want := map[string]int{"2025-06-03": 1, "2025-06-02": 1}
	if len(got) != len(want) {
		t.Fatalf("expected only strictly-new commits counted, got %v", got)
	}
	for day, count := range want {
		if got[day] != count {
			t.Errorf("day %s: expected %d, got %d", day, count, got[day])
		}
	}
}
