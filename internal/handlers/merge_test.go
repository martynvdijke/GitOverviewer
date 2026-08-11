package handlers

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlens/ent"
	"gitlens/ent/enttest"
	ghclient "gitlens/internal/github"
	"gitlens/internal/middleware"
	"gitlens/internal/provider"
	"gitlens/internal/sync"
	"gitlens/internal/ws"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

// mergeFakeProvider is a test double for provider.Provider whose merge and
// close behavior is scriptable per PR number.
type mergeFakeProvider struct {
	name       string
	mergeFn    func(number int) (bool, string, error)
	mergeCalls []int
	closeFn    func(number int) error
	closeCalls []int
	listPRs    []*ghclient.PullRequest
}

func (f *mergeFakeProvider) Name() string { return f.name }
func (f *mergeFakeProvider) AuthURL(state, redirectURL string) string {
	return ""
}
func (f *mergeFakeProvider) ExchangeCode(ctx context.Context, code, redirectURL string) (string, *ghclient.User, error) {
	return "", nil, fmt.Errorf("not implemented")
}
func (f *mergeFakeProvider) GetUser(ctx context.Context, token string) (*ghclient.User, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *mergeFakeProvider) ListRepositories(ctx context.Context, token string) ([]*ghclient.Repository, error) {
	return nil, nil
}
func (f *mergeFakeProvider) GetCommitsSince(ctx context.Context, token, owner, repo, branch string, since time.Time, maxCommits int) ([]*ghclient.Commit, error) {
	return nil, nil
}
func (f *mergeFakeProvider) ListCommitsPage(ctx context.Context, token, owner, repo, branch string, page, perPage int) ([]*ghclient.Commit, bool, error) {
	return nil, false, nil
}
func (f *mergeFakeProvider) ListReleases(ctx context.Context, token, owner, repo string) ([]*ghclient.Release, error) {
	return nil, nil
}
func (f *mergeFakeProvider) ListPullRequests(ctx context.Context, token, owner, repo string) ([]*ghclient.PullRequest, error) {
	return f.listPRs, nil
}
func (f *mergeFakeProvider) ListRecentlyMergedPRs(ctx context.Context, token, owner, repo string) ([]*ghclient.PullRequest, error) {
	return nil, nil
}
func (f *mergeFakeProvider) MergePullRequest(ctx context.Context, token, owner, repo string, number int) (bool, string, error) {
	f.mergeCalls = append(f.mergeCalls, number)
	return f.mergeFn(number)
}
func (f *mergeFakeProvider) ClosePullRequest(ctx context.Context, token, owner, repo string, number int) error {
	f.closeCalls = append(f.closeCalls, number)
	if f.closeFn == nil {
		return nil
	}
	return f.closeFn(number)
}
func (f *mergeFakeProvider) GetLatestWorkflowRun(ctx context.Context, token, owner, repo, branch string) (*ghclient.WorkflowRun, error) {
	return nil, fmt.Errorf("not implemented")
}

// newMergeTestHandler builds a DashboardHandler wired to the given
// providers, plus a gin engine serving the merge routes with a minimal
// prs_tab_with_toast template.
func newMergeTestHandler(t *testing.T, providers map[string]provider.Provider) (*DashboardHandler, *ent.Client) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
	store := middleware.NewSessionStore(testSessionDB(t))
	ghClient := ghclient.NewClient("", "")
	syncer := sync.NewSyncer(client, ghClient, providers, ws.NewHub())
	h := NewDashboardHandler(client, store, ghClient, syncer)
	h.bgCtx = t.Context()
	return h, client
}

func newMergeTestEngine(t *testing.T, h *DashboardHandler, userID int64) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Parse(
		`{{define "prs_tab_with_toast"}}TYPE={{.ToastType}} MSG={{.ToastMessage}} DET={{.ToastDetails}} MERGED={{range $k, $v := .MergedSet}}{{$k}};{{end}} CLOSED={{range $k, $v := .ClosedSet}}{{$k}};{{end}}{{end}}`)))
	authed := engine.Group("/", func(c *gin.Context) { c.Set("user_id", userID); c.Next() })
	authed.POST("/prs/merge", h.MergeSinglePR)
	authed.POST("/prs/batch-merge", h.BatchMergePRs)
	authed.POST("/prs/close", h.CloseSinglePR)
	authed.POST("/prs/batch-close", h.BatchClosePRs)
	return engine
}

func createMergeRepo(t *testing.T, client *ent.Client, userID int, providerName string) *ent.Repository {
	t.Helper()
	ctx := context.Background()
	repo, err := client.Repository.Create().
		SetGithubID(777).SetOwner("user").SetName("m-repo").
		SetFullName("user/m-repo").SetHTMLURL("https://example.com/user/m-repo").
		SetDefaultBranch("main").SetUserID(userID).
		SetProvider(providerName).
		Save(ctx)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	return repo
}

func createMergeUser(t *testing.T, client *ent.Client) *ent.User {
	t.Helper()
	u, err := client.User.Create().
		SetGithubID(7777).
		SetLogin("mergeuser").
		SetAccessToken("token").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func postJSON(engine *gin.Engine, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, req)
	return w
}

func TestMergeSinglePR_SuccessReturns200Partial(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", mergeFn: func(n int) (bool, string, error) {
		return true, "", nil
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := postJSON(engine, "/prs/merge", fmt.Sprintf(`{"repo_id":%d,"pr_number":5}`, repo.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "MSG=PR #5 merged successfully") {
		t.Errorf("expected success toast, got: %s", body)
	}
	if !strings.Contains(body, "TYPE=success") {
		t.Errorf("expected success type, got: %s", body)
	}
	if len(fake.mergeCalls) != 1 || fake.mergeCalls[0] != 5 {
		t.Errorf("expected provider merge called for PR 5, got %v", fake.mergeCalls)
	}
}

func TestMergeSinglePR_RefusedReturns200Warning(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", mergeFn: func(n int) (bool, string, error) {
		return false, "not mergeable: conflicts", nil
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := postJSON(engine, "/prs/merge", fmt.Sprintf(`{"repo_id":%d,"pr_number":9}`, repo.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (HTMX partial), got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "TYPE=warning") || !strings.Contains(body, "not mergeable") {
		t.Errorf("expected warning toast with provider reason, got: %s", body)
	}
}

func TestMergeSinglePR_ProviderErrorReturns200Danger(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", mergeFn: func(n int) (bool, string, error) {
		return false, "", fmt.Errorf("403 Forbidden")
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := postJSON(engine, "/prs/merge", fmt.Sprintf(`{"repo_id":%d,"pr_number":3}`, repo.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (HTMX partial), got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "TYPE=danger") || !strings.Contains(body, "Permission denied") {
		t.Errorf("expected danger toast with friendly message, got: %s", body)
	}
}

func TestMergeSinglePR_UsesReposProvider(t *testing.T) {
	// Repo is hosted on Forgejo: the forgejo provider must be used,
	// not the GitHub client.
	fake := &mergeFakeProvider{name: "forgejo", mergeFn: func(n int) (bool, string, error) {
		return true, "", nil
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"forgejo": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "forgejo")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := postJSON(engine, "/prs/merge", fmt.Sprintf(`{"repo_id":%d,"pr_number":11}`, repo.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(fake.mergeCalls) != 1 {
		t.Fatalf("expected forgejo provider to be called once, got %v", fake.mergeCalls)
	}
}

func TestBatchMergePRs_PartialFailureSummary(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", mergeFn: func(n int) (bool, string, error) {
		if n == 2 {
			return false, "not mergeable: conflicts", nil
		}
		return true, "", nil
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/prs/batch-merge",
		strings.NewReader(fmt.Sprintf("pr_ids=%d%%3A1&pr_ids=%d%%3A2", repo.ID, repo.ID)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "MSG=Merged 1/2") {
		t.Errorf("expected partial summary, got: %s", body)
	}
	if !strings.Contains(body, "TYPE=warning") {
		t.Errorf("expected warning type, got: %s", body)
	}
	if !strings.Contains(body, "conflicts") {
		t.Errorf("expected failure reason in details, got: %s", body)
	}
	// Merged PR is optimistically marked for the re-rendered queue.
	if !strings.Contains(body, fmt.Sprintf("MERGED=%d:1;", repo.ID)) {
		t.Errorf("expected merged set to contain repoID:1, got: %s", body)
	}
}

func TestCloseSinglePR_Success(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", mergeFn: func(n int) (bool, string, error) {
		return true, "", nil
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := postJSON(engine, "/prs/close", fmt.Sprintf(`{"repo_id":%d,"pr_number":7}`, repo.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "MSG=PR #7 closed") {
		t.Errorf("expected success toast, got: %s", body)
	}
	if !strings.Contains(body, "TYPE=success") {
		t.Errorf("expected success type, got: %s", body)
	}
	if len(fake.closeCalls) != 1 || fake.closeCalls[0] != 7 {
		t.Errorf("expected provider close called for PR 7, got %v", fake.closeCalls)
	}
	if !strings.Contains(body, fmt.Sprintf("CLOSED=%d:7;", repo.ID)) {
		t.Errorf("expected closed set to contain repoID:7, got: %s", body)
	}
}

func TestCloseSinglePR_ProviderErrorReturns200Danger(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", closeFn: func(n int) error {
		return fmt.Errorf("404 Not Found")
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := postJSON(engine, "/prs/close", fmt.Sprintf(`{"repo_id":%d,"pr_number":3}`, repo.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (HTMX partial), got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "TYPE=danger") || !strings.Contains(body, "Failed to close #3") {
		t.Errorf("expected danger toast, got: %s", body)
	}
	if len(fake.closeCalls) != 1 || fake.closeCalls[0] != 3 {
		t.Errorf("expected provider close called for PR 3, got %v", fake.closeCalls)
	}
}

func TestCloseSinglePR_InvalidRequest(t *testing.T) {
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": &mergeFakeProvider{name: "fake"}})
	u := createMergeUser(t, client)
	createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := postJSON(engine, "/prs/close", `not-json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBatchClosePRs_PartialFailureSummary(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", closeFn: func(n int) error {
		if n == 2 {
			return fmt.Errorf("403 Forbidden")
		}
		return nil
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/prs/batch-close",
		strings.NewReader(fmt.Sprintf("pr_ids=%d%%3A1&pr_ids=%d%%3A2", repo.ID, repo.ID)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "MSG=Closed 1/2") {
		t.Errorf("expected partial summary, got: %s", body)
	}
	if !strings.Contains(body, "TYPE=warning") {
		t.Errorf("expected warning type, got: %s", body)
	}
	if !strings.Contains(body, "Permission denied") {
		t.Errorf("expected friendly failure reason in details, got: %s", body)
	}
	if !strings.Contains(body, fmt.Sprintf("CLOSED=%d:1;", repo.ID)) {
		t.Errorf("expected closed set to contain repoID:1, got: %s", body)
	}
}

func TestBatchClosePRs_AllSuccess(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake"}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/prs/batch-close",
		strings.NewReader(fmt.Sprintf("pr_ids=%d%%3A1&pr_ids=%d%%3A2", repo.ID, repo.ID)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "MSG=All 2 PR(s) closed successfully!") {
		t.Errorf("expected success summary, got: %s", body)
	}
	if !strings.Contains(body, "TYPE=success") {
		t.Errorf("expected success type, got: %s", body)
	}
	if len(fake.closeCalls) != 2 {
		t.Errorf("expected provider close called twice, got %v", fake.closeCalls)
	}
}

func TestBatchClosePRs_NoSelection(t *testing.T) {
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": &mergeFakeProvider{name: "fake"}})
	u := createMergeUser(t, client)
	createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/prs/batch-close", strings.NewReader(``))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestClosePR_Success(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", mergeFn: func(n int) (bool, string, error) {
		return true, "", nil
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Parse(
		`{{define "repo_card_with_toast"}}TYPE={{.ToastType}} MSG={{.ToastMessage}} DET={{.ToastDetails}}{{end}}`)))
	authed := engine.Group("/", func(c *gin.Context) { c.Set("user_id", int64(u.ID)); c.Next() })
	authed.POST("/repos/:id/prs/:number/close", h.ClosePR)

	w := postJSON(engine, fmt.Sprintf("/repos/%d/prs/7/close", repo.ID), `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "MSG=PR #7 closed") || !strings.Contains(body, "TYPE=success") {
		t.Errorf("expected success toast, got: %s", body)
	}
	if len(fake.closeCalls) != 1 || fake.closeCalls[0] != 7 {
		t.Errorf("expected provider close called for PR 7, got %v", fake.closeCalls)
	}
}

func TestClosePR_ProviderErrorReturns200Danger(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", closeFn: func(n int) error {
		return fmt.Errorf("403 Forbidden")
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Parse(
		`{{define "repo_card_with_toast"}}TYPE={{.ToastType}} MSG={{.ToastMessage}} DET={{.ToastDetails}}{{end}}`)))
	authed := engine.Group("/", func(c *gin.Context) { c.Set("user_id", int64(u.ID)); c.Next() })
	authed.POST("/repos/:id/prs/:number/close", h.ClosePR)

	w := postJSON(engine, fmt.Sprintf("/repos/%d/prs/3/close", repo.ID), `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "TYPE=danger") || !strings.Contains(body, "Permission denied") {
		t.Errorf("expected danger toast with friendly message, got: %s", body)
	}
}

func TestCloseAllPRs_Success(t *testing.T) {
	fake := &mergeFakeProvider{
		name: "fake",
		closeFn: func(n int) error {
			return nil
		},
		listPRs: []*ghclient.PullRequest{
			{Number: 1, Title: "one", Author: "a", CreatedAt: time.Now(), HeadRef: "f1", BaseRef: "main"},
			{Number: 2, Title: "two", Author: "a", CreatedAt: time.Now(), HeadRef: "f2", BaseRef: "main"},
		},
	}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Parse(
		`{{define "repo_card_with_toast"}}TYPE={{.ToastType}} MSG={{.ToastMessage}} DET={{.ToastDetails}}{{end}}`)))
	authed := engine.Group("/", func(c *gin.Context) { c.Set("user_id", int64(u.ID)); c.Next() })
	authed.POST("/repos/:id/prs/close-all", h.CloseAllPRs)

	w := postJSON(engine, fmt.Sprintf("/repos/%d/prs/close-all", repo.ID), `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "MSG=All 2 PR(s) closed successfully!") || !strings.Contains(body, "TYPE=success") {
		t.Errorf("expected success toast, got: %s", body)
	}
	if len(fake.closeCalls) != 2 {
		t.Errorf("expected provider close called twice, got %v", fake.closeCalls)
	}
}

func TestCloseAllPRs_PartialFailure(t *testing.T) {
	fake := &mergeFakeProvider{
		name: "fake",
		closeFn: func(n int) error {
			if n == 2 {
				return fmt.Errorf("403 Forbidden")
			}
			return nil
		},
		listPRs: []*ghclient.PullRequest{
			{Number: 1, Title: "one", Author: "a", CreatedAt: time.Now(), HeadRef: "f1", BaseRef: "main"},
			{Number: 2, Title: "two", Author: "a", CreatedAt: time.Now(), HeadRef: "f2", BaseRef: "main"},
		},
	}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Parse(
		`{{define "repo_card_with_toast"}}TYPE={{.ToastType}} MSG={{.ToastMessage}} DET={{.ToastDetails}}{{end}}`)))
	authed := engine.Group("/", func(c *gin.Context) { c.Set("user_id", int64(u.ID)); c.Next() })
	authed.POST("/repos/:id/prs/close-all", h.CloseAllPRs)

	w := postJSON(engine, fmt.Sprintf("/repos/%d/prs/close-all", repo.ID), `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "MSG=Closed 1 PR(s)") || !strings.Contains(body, "TYPE=warning") {
		t.Errorf("expected warning toast, got: %s", body)
	}
	if !strings.Contains(body, "Failed: [2]") {
		t.Errorf("expected failed PR numbers in details, got: %s", body)
	}
}
