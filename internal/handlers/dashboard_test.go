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
	"gitlens/ent/repository"
	"gitlens/ent/user"
	"gitlens/internal/github"
	"gitlens/internal/middleware"
	"gitlens/internal/provider"
	"gitlens/internal/sync"
	"gitlens/internal/ws"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

func newTestDashboardHandler(t *testing.T) (*DashboardHandler, *middleware.SessionStore, *ent.Client) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
	store := middleware.NewSessionStore(testSessionDB(t))
	ghClient := github.NewClient("", "")
	hub := ws.NewHub()
	syncer := sync.NewSyncer(client, ghClient, map[string]provider.Provider{"github": provider.NewGitHubAdapter(ghClient)}, hub)
	h := NewDashboardHandler(client, store, ghClient, syncer)
	h.bgCtx = t.Context()
	return h, store, client
}

func serveListReposRequest(handler gin.HandlerFunc, method, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"shortSHA":             func(s string) string { return s },
		"formatTime":           func(t time.Time) string { return "" },
		"truncate":             func(s string, n int) string { return s },
		"workflowIcon":         func(status string) string { return "" },
		"workflowLabel":        func(status string) string { return "" },
		"hasWorkflowRun":       func(status string) bool { return false },
		"printf":               func(format string, args ...any) string { return "" },
		"releaseIcon":          func(conclusion string) string { return "" },
		"releaseLabel":         func(conclusion string) string { return "" },
		"hasReleaseConclusion": func(s string) bool { return false },
	}).Parse(`{{define "repo_list"}}<div>{{range .Repos}}<div class="repo">{{.FullName}}</div>{{end}}</div>{{end}}`)))
	engine.GET("/repos", handler)
	req := httptest.NewRequest(method, path, nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	engine.ServeHTTP(w, req)
	return w
}

func serveDashboardRequest(handler gin.HandlerFunc, method, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"shortSHA":             func(s string) string { return s },
		"formatTime":           func(t time.Time) string { return "" },
		"truncate":             func(s string, n int) string { return s },
		"workflowIcon":         func(status string) string { return "" },
		"workflowLabel":        func(status string) string { return "" },
		"hasWorkflowRun":       func(status string) bool { return false },
		"printf":               func(format string, args ...any) string { return "" },
		"releaseIcon":          func(conclusion string) string { return "" },
		"releaseLabel":         func(conclusion string) string { return "" },
		"hasReleaseConclusion": func(s string) bool { return false },
	}).Parse(`{{define "repos_tab"}}<div>{{range .Repos}}<div class="repo">{{.FullName}}</div>{{end}}</div>{{end}}`)))
	engine.GET("/dashboard", handler)
	req := httptest.NewRequest(method, path, nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	engine.ServeHTTP(w, req)
	return w
}

func TestListRepos_FilterByQuery(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)

	u, err := client.User.Create().
		SetGithubID(100).
		SetLogin("testuser").
		SetAccessToken("token").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	client.Repository.Create().SetGithubID(1).SetOwner("user").SetName("frontend-app").SetFullName("user/frontend-app").SetDescription("UI app").SetHTMLURL("https://github.com/user/frontend-app").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())
	client.Repository.Create().SetGithubID(2).SetOwner("user").SetName("backend-api").SetFullName("user/backend-api").SetDescription("API server").SetHTMLURL("https://github.com/user/backend-api").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())
	client.Repository.Create().SetGithubID(3).SetOwner("user").SetName("docs-site").SetFullName("user/docs-site").SetDescription("Documentation").SetHTMLURL("https://github.com/user/docs-site").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())

	sessionID := store.Set(int64(u.ID))

	// Test: filter by "frontend" should return 1 repo
	listHandler := func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.ListRepos(c)
	}
	w := serveListReposRequest(listHandler, "GET", "/repos?q=frontend", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "frontend-app") {
		t.Errorf("expected frontend-app in response, body: %s", body)
	}
	if strings.Contains(body, "backend-api") {
		t.Errorf("did not expect backend-api in response, body: %s", body)
	}

	// Test: filter by "api" should return only backend-api
	w2 := serveListReposRequest(listHandler, "GET", "/repos?q=api", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if !strings.Contains(w2.Body.String(), "backend-api") {
		t.Errorf("expected backend-api in response, body: %s", w2.Body.String())
	}

	// Test: filter by nonexistent returns empty
	w3 := serveListReposRequest(listHandler, "GET", "/repos?q=nonexistent", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if strings.Contains(w3.Body.String(), "frontend-app") || strings.Contains(w3.Body.String(), "backend-api") {
		t.Errorf("expected empty results for nonexistent query, body: %s", w3.Body.String())
	}

	// Test: no query returns all repos
	w4 := serveListReposRequest(listHandler, "GET", "/repos", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if !strings.Contains(w4.Body.String(), "frontend-app") || !strings.Contains(w4.Body.String(), "backend-api") || !strings.Contains(w4.Body.String(), "docs-site") {
		t.Errorf("expected all repos in response, body: %s", w4.Body.String())
	}

	// Test: case insensitive filter
	w5 := serveListReposRequest(listHandler, "GET", "/repos?q=FrontEnd", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if !strings.Contains(w5.Body.String(), "frontend-app") {
		t.Errorf("expected case-insensitive match for FrontEnd, body: %s", w5.Body.String())
	}
}

func TestDashboard_FilterByQuery(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)

	u, err := client.User.Create().
		SetGithubID(200).
		SetLogin("testuser2").
		SetAccessToken("token").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	client.Repository.Create().SetGithubID(10).SetOwner("org").SetName("alpha").SetFullName("org/alpha").SetDescription("Alpha service").SetHTMLURL("https://github.com/org/alpha").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())
	client.Repository.Create().SetGithubID(11).SetOwner("org").SetName("beta").SetFullName("org/beta").SetDescription("Beta service").SetHTMLURL("https://github.com/org/beta").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())

	sessionID := store.Set(int64(u.ID))

	dashHandler := func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.ReposTab(c)
	}
	w := serveDashboardRequest(dashHandler, "GET", "/dashboard?q=alpha", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "org/alpha") {
		t.Errorf("expected org/alpha in response, body: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "org/beta") {
		t.Errorf("did not expect org/beta in response, body: %s", w.Body.String())
	}
}

func TestComputeMetrics(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
	u, _ := client.User.Create().
		SetGithubID(300).SetLogin("metrictest").SetAccessToken("tok").Save(context.Background())

	repo1, _ := client.Repository.Create().
		SetGithubID(20).SetOwner("u").SetName("r1").SetFullName("u/r1").SetHTMLURL("https://github.com/u/r1").SetDefaultBranch("main").SetUserID(u.ID).
		SetTotalCommitsFetched(10).SetFeatCount(4).SetFixCount(3).SetDocsCount(2).SetChoreCount(1).SetOtherCommitCount(0).
		SetReleaseCount(2).SetWorkflowSuccessCount(5).SetWorkflowFailureCount(1).SetAvgLeadTimeHours(12.5).
		Save(context.Background())
	repo2, _ := client.Repository.Create().
		SetGithubID(21).SetOwner("u").SetName("r2").SetFullName("u/r2").SetHTMLURL("https://github.com/u/r2").SetDefaultBranch("main").SetUserID(u.ID).
		SetTotalCommitsFetched(20).SetFeatCount(2).SetFixCount(2).SetDocsCount(1).SetChoreCount(3).SetOtherCommitCount(2).
		SetReleaseCount(3).SetWorkflowSuccessCount(8).SetWorkflowFailureCount(2).SetAvgLeadTimeHours(24.0).
		Save(context.Background())

	metrics := computeMetrics([]*ent.Repository{repo1, repo2})

	if metrics.TotalRepos != 2 {
		t.Errorf("expected 2 repos, got %d", metrics.TotalRepos)
	}
	if metrics.TotalReleases != 5 {
		t.Errorf("expected 5 releases, got %d", metrics.TotalReleases)
	}
	if metrics.TotalCommits != 30 {
		t.Errorf("expected 30 commits, got %d", metrics.TotalCommits)
	}
	if metrics.FeatCount != 6 {
		t.Errorf("expected 6 feats, got %d", metrics.FeatCount)
	}
	if metrics.FixCount != 5 {
		t.Errorf("expected 5 fixes, got %d", metrics.FixCount)
	}
	if metrics.WorkflowSuccesses != 13 {
		t.Errorf("expected 13 workflow successes, got %d", metrics.WorkflowSuccesses)
	}
	if metrics.WorkflowFailures != 3 {
		t.Errorf("expected 3 workflow failures, got %d", metrics.WorkflowFailures)
	}
	if metrics.AvgLeadTimeHours != 18.3 {
		t.Errorf("expected 18.3 avg lead time, got %.1f", metrics.AvgLeadTimeHours)
	}
}

func TestImportAllRepos_Success(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)

	u, err := client.User.Create().
		SetGithubID(100).
		SetLogin("testuser").
		SetAccessToken("test_token").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Pre-create one existing repo to test dedup
	client.Repository.Create().
		SetGithubID(1).SetOwner("user").SetName("existing").
		SetFullName("user/existing").SetHTMLURL("https://github.com/user/existing").
		SetDefaultBranch("main").SetUserID(u.ID).
		Save(context.Background())

	pageCounters := make(map[string]int)
	apiCalls := 0
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		w.Header().Set("Content-Type", "application/json")
		key := r.Method + " " + r.URL.Path
		pageCounters[key]++
		page := pageCounters[key]
		switch {
		case r.URL.Path == "/user/repos":
			if page == 1 {
				w.Write([]byte(`[
					{"id":1,"name":"existing","full_name":"user/existing","html_url":"https://github.com/user/existing","default_branch":"main","owner":{"login":"user"}},
					{"id":2,"name":"newrepo1","full_name":"user/newrepo1","html_url":"https://github.com/user/newrepo1","default_branch":"main","language":"Go","owner":{"login":"user"}}
				]`))
			} else {
				w.Write([]byte(`[]`))
			}
		case strings.Contains(r.URL.Path, "/repos/user/newrepo1/commits"):
			w.Write([]byte(`[{"sha":"abc","commit":{"message":"feat: initial commit","committer":{"date":"2024-06-01T10:00:00Z"}}}]`))
		case strings.Contains(r.URL.Path, "/repos/user/newrepo1/releases"):
			if page == 1 {
				w.Write([]byte(`[{"tag_name":"v1.0.0","name":"Version 1","published_at":"2024-06-15T10:00:00Z"}]`))
			} else {
				w.Write([]byte(`[]`))
			}
		case strings.Contains(r.URL.Path, "/repos/user/newrepo1/actions/runs"):
			w.Write([]byte(`{"workflow_runs":[{"id":1,"status":"completed","conclusion":"success"}]}`))
		case strings.Contains(r.URL.Path, "/repos/user/newrepo1/pulls"):
			w.Write([]byte(`[]`))
		default:
			t.Logf("Unexpected API call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiSrv.Close()
	handler.gh.APIURL = apiSrv.URL

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"shortSHA":             func(s string) string { return s },
		"formatTime":           func(t time.Time) string { return "" },
		"truncate":             func(s string, n int) string { return s },
		"workflowIcon":         func(status string) string { return "" },
		"workflowLabel":        func(status string) string { return "" },
		"hasWorkflowRun":       func(status string) bool { return false },
		"printf":               func(format string, args ...any) string { return "" },
		"releaseIcon":          func(conclusion string) string { return "" },
		"releaseLabel":         func(conclusion string) string { return "" },
		"hasReleaseConclusion": func(s string) bool { return false },
	}).Parse(`{{define "repo_list"}}<div>{{range .Repos}}<div class="repo">{{.FullName}}</div>{{end}}</div>{{end}}`)))
	engine.POST("/repos/import-all", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.ImportAllRepos(c)
	})
	req := httptest.NewRequest("POST", "/repos/import-all", nil)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d. Body: %s", w.Code, w.Body.String())
	}

	// Repos are created synchronously, sync happens in background
	repos, _ := client.Repository.Query().All(context.Background())
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}

	if !strings.Contains(w.Body.String(), "user/existing") {
		t.Errorf("response should contain existing repo name")
	}
	if !strings.Contains(w.Body.String(), "user/newrepo1") {
		t.Errorf("response should contain new repo name")
	}
}

func TestImportAllRepos_NoRepos(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)

	u, err := client.User.Create().
		SetGithubID(200).
		SetLogin("norepos").
		SetAccessToken("token").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer apiSrv.Close()
	handler.gh.APIURL = apiSrv.URL

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"shortSHA":             func(s string) string { return s },
		"formatTime":           func(t time.Time) string { return "" },
		"truncate":             func(s string, n int) string { return s },
		"workflowIcon":         func(status string) string { return "" },
		"workflowLabel":        func(status string) string { return "" },
		"hasWorkflowRun":       func(status string) bool { return false },
		"printf":               func(format string, args ...any) string { return "" },
		"releaseIcon":          func(conclusion string) string { return "" },
		"releaseLabel":         func(conclusion string) string { return "" },
		"hasReleaseConclusion": func(s string) bool { return false },
	}).Parse(`{{define "repo_list"}}<div>{{range .Repos}}<div class="repo">{{.FullName}}</div>{{end}}</div>{{end}}`)))
	engine.POST("/repos/import-all", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.ImportAllRepos(c)
	})
	req := httptest.NewRequest("POST", "/repos/import-all", nil)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	repos, _ := client.Repository.Query().All(context.Background())
	if len(repos) != 0 {
		t.Fatalf("expected 0 repos, got %d", len(repos))
	}
}

func TestListPullRequests_Success(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	ctx := context.Background()

	u, err := client.User.Create().
		SetGithubID(300).
		SetLogin("pruser").
		SetAccessToken("pr_token").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, err = client.Repository.Create().
		SetGithubID(10).SetOwner("org").SetName("pr-repo").
		SetFullName("org/pr-repo").SetHTMLURL("https://github.com/org/pr-repo").
		SetDefaultBranch("main").SetUserID(u.ID).
		SetPullRequests(`[{"n":1,"t":"Fix bug","a":"dev1","c":"2024-06-01T10:00:00Z","h":"https://github.com/org/pr-repo/pull/1","hr":"fix-bug","br":"main"}]`).
		SetOpenPrCount(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"shortSHA":             func(s string) string { return s },
		"formatTime":           func(t time.Time) string { return "" },
		"truncate":             func(s string, n int) string { return s },
		"workflowIcon":         func(status string) string { return "" },
		"workflowLabel":        func(status string) string { return "" },
		"hasWorkflowRun":       func(status string) bool { return false },
		"printf":               func(format string, args ...any) string { return "" },
		"releaseIcon":          func(conclusion string) string { return "" },
		"releaseLabel":         func(conclusion string) string { return "" },
		"hasReleaseConclusion": func(s string) bool { return false },
	}).Parse(`{{define "pr_list"}}<div>{{range .PRs}}<div class="pr-item">{{.Number}} {{.Title}}</div>{{end}}</div>{{end}}`)))
	engine.GET("/repos/:id/prs", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.ListPullRequests(c)
	})
	req := httptest.NewRequest("GET", "/repos/1/prs", nil)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d. Body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Fix bug") {
		t.Errorf("expected PR title in response, body: %s", w.Body.String())
	}
}

func TestListPullRequests_Empty(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	ctx := context.Background()

	u, err := client.User.Create().
		SetGithubID(400).
		SetLogin("emptypru").
		SetAccessToken("token").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, err = client.Repository.Create().
		SetGithubID(20).SetOwner("org").SetName("empty-pr").
		SetFullName("org/empty-pr").SetHTMLURL("https://github.com/org/empty-pr").
		SetDefaultBranch("main").SetUserID(u.ID).
		SetPullRequests("[]").
		SetOpenPrCount(0).
		Save(ctx)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"shortSHA":             func(s string) string { return s },
		"formatTime":           func(t time.Time) string { return "" },
		"truncate":             func(s string, n int) string { return s },
		"workflowIcon":         func(status string) string { return "" },
		"workflowLabel":        func(status string) string { return "" },
		"hasWorkflowRun":       func(status string) bool { return false },
		"printf":               func(format string, args ...any) string { return "" },
		"releaseIcon":          func(conclusion string) string { return "" },
		"releaseLabel":         func(conclusion string) string { return "" },
		"hasReleaseConclusion": func(s string) bool { return false },
	}).Parse(`{{define "pr_list"}}<div>{{if .PRs}}{{range .PRs}}{{.n}}{{else}}empty{{end}}{{else}}empty{{end}}</div>{{end}}`)))
	engine.GET("/repos/:id/prs", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.ListPullRequests(c)
	})
	req := httptest.NewRequest("GET", "/repos/1/prs", nil)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestComputeMetrics_Empty(t *testing.T) {
	metrics := computeMetrics(nil)
	if metrics.TotalRepos != 0 {
		t.Errorf("expected 0 repos, got %d", metrics.TotalRepos)
	}
}

func TestComputeOverview(t *testing.T) {
	now := time.Now()
	repos := []*ent.Repository{
		{OpenPrCount: 2, ReleaseCount: 3, WorkflowStatus: "success", SyncedAt: now},
		{OpenPrCount: 1, ReleaseCount: 1, WorkflowStatus: "failure", SyncedAt: now.Add(-time.Hour)},
		{OpenPrCount: 0, ReleaseCount: 0, WorkflowStatus: "failure", SyncedAt: now.Add(-2 * time.Hour)},
	}
	o := computeOverview(repos)
	if o.TotalRepos != 3 {
		t.Errorf("expected 3 repos, got %d", o.TotalRepos)
	}
	if o.OpenPRs != 3 {
		t.Errorf("expected 3 open PRs, got %d", o.OpenPRs)
	}
	if o.FailingRepos != 2 {
		t.Errorf("expected 2 failing repos, got %d", o.FailingRepos)
	}
	if o.TotalReleases != 4 {
		t.Errorf("expected 4 releases, got %d", o.TotalReleases)
	}
}

func TestComputeOverview_Empty(t *testing.T) {
	o := computeOverview(nil)
	if o.TotalRepos != 0 {
		t.Errorf("expected 0 repos, got %d", o.TotalRepos)
	}
	if o.LastSyncTime != "" {
		t.Errorf("expected empty LastSyncTime, got %q", o.LastSyncTime)
	}
}

func TestComputeOverview_SingleRepoNoSync(t *testing.T) {
	repos := []*ent.Repository{
		{OpenPrCount: 0, ReleaseCount: 0, WorkflowStatus: ""},
	}
	o := computeOverview(repos)
	if o.TotalRepos != 1 {
		t.Errorf("expected 1 repo, got %d", o.TotalRepos)
	}
	if o.LastSyncTime != "" {
		t.Errorf("expected empty LastSyncTime for zero sync time, got %q", o.LastSyncTime)
	}
}

func TestComputeOverview_LastSyncTime(t *testing.T) {
	now := time.Now()
	repos := []*ent.Repository{
		{SyncedAt: now.Add(-30 * time.Minute)},
		{SyncedAt: now},
		{SyncedAt: now.Add(-time.Hour)},
	}
	o := computeOverview(repos)
	if o.LastSyncTime != now.Format("Jan 2 15:04") {
		t.Errorf("expected LastSyncTime %q, got %q", now.Format("Jan 2 15:04"), o.LastSyncTime)
	}
}

func TestComputeMetrics_SingleRepo(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
	u, _ := client.User.Create().
		SetGithubID(400).SetLogin("single").SetAccessToken("tok").Save(context.Background())

	repo1, _ := client.Repository.Create().
		SetGithubID(30).SetOwner("u").SetName("single").SetFullName("u/single").SetHTMLURL("https://github.com/u/single").SetDefaultBranch("main").SetUserID(u.ID).
		SetTotalCommitsFetched(5).SetFeatCount(5).SetFixCount(0).SetDocsCount(0).SetChoreCount(0).SetOtherCommitCount(0).
		SetReleaseCount(1).SetWorkflowSuccessCount(2).SetWorkflowFailureCount(0).
		Save(context.Background())

	metrics := computeMetrics([]*ent.Repository{repo1})
	if metrics.FeatPct != 100.0 {
		t.Errorf("expected 100%% feat, got %.1f%%", metrics.FeatPct)
	}
	if metrics.WorkflowPassRate != 100.0 {
		t.Errorf("expected 100%% pass rate, got %.1f%%", metrics.WorkflowPassRate)
	}
}

func TestImportAllRepos_EmptyGitHubResponse(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	ctx := context.Background()

	u, err := client.User.Create().
		SetGithubID(900).SetLogin("importempty").SetAccessToken("tok").Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer apiSrv.Close()
	handler.gh.APIURL = apiSrv.URL

	sessionID := store.Set(int64(u.ID))
	w := serveImportAllRequest(handler, sessionID, u.ID)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestImportAllRepos_DuplicateNotCreated(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	ctx := context.Background()

	u, err := client.User.Create().
		SetGithubID(901).SetLogin("importdup").SetAccessToken("tok").Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Create a repo that matches what the mock GitHub API would return
	client.Repository.Create().
		SetGithubID(1).SetOwner("user").SetName("existing").
		SetFullName("user/existing").SetHTMLURL("https://github.com/user/existing").
		SetDefaultBranch("main").SetUserID(u.ID).
		Save(ctx)

	callCount := 0
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++
		if callCount == 1 {
			w.Write([]byte(`[{"id":1,"name":"existing","full_name":"user/existing","description":"","html_url":"","language":"","default_branch":"main","owner":{"login":"user"}}]`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	defer apiSrv.Close()
	handler.gh.APIURL = apiSrv.URL

	sessionID := store.Set(int64(u.ID))
	w := serveImportAllRequest(handler, sessionID, u.ID)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Should still have only 1 repo (no duplicate created)
	count, _ := client.Repository.Query().Where(repository.HasUserWith(user.ID(u.ID))).Count(ctx)
	if count != 1 {
		t.Errorf("expected 1 repo (no duplicate), got %d", count)
	}
}

func serveImportAllRequest(handler *DashboardHandler, sessionID string, userID int) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"shortSHA":             func(s string) string { return s },
		"formatTime":           func(t time.Time) string { return "" },
		"truncate":             func(s string, n int) string { return s },
		"workflowIcon":         func(status string) string { return "" },
		"workflowLabel":        func(status string) string { return "" },
		"hasWorkflowRun":       func(status string) bool { return false },
		"printf":               func(format string, args ...any) string { return "" },
		"releaseIcon":          func(conclusion string) string { return "" },
		"releaseLabel":         func(conclusion string) string { return "" },
		"hasReleaseConclusion": func(s string) bool { return false },
	}).Parse(`{{define "repo_list"}}<div>{{range .Repos}}<div>{{.FullName}}</div>{{end}}</div>{{end}}`)))
	engine.POST("/repos/import-all", func(c *gin.Context) {
		c.Set("user_id", int64(userID))
		handler.ImportAllRepos(c)
	})
	req := httptest.NewRequest("POST", "/repos/import-all", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)
	return w
}

func TestMergePR_InvalidRepoID(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().
		SetGithubID(902).SetLogin("mergetest").SetAccessToken("tok").Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/repos/:id/prs/:number/merge", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.MergePR(c)
	})
	req := httptest.NewRequest("POST", "/repos/invalid/prs/1/merge", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid repo ID, got %d", w.Code)
	}
}

func TestMergePR_RepoNotFound(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().
		SetGithubID(903).SetLogin("mergenotfound").SetAccessToken("tok").Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/repos/:id/prs/:number/merge", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.MergePR(c)
	})
	req := httptest.NewRequest("POST", "/repos/999/prs/1/merge", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent repo, got %d", w.Code)
	}
}

func TestMergePR_InvalidPRNumber(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().
		SetGithubID(904).SetLogin("mergeinvalidpr").SetAccessToken("tok").Save(context.Background())

	client.Repository.Create().
		SetGithubID(1).SetOwner("user").SetName("mergepr").
		SetFullName("user/mergepr").SetHTMLURL("https://github.com/user/mergepr").
		SetDefaultBranch("main").SetUserID(u.ID).
		Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/repos/:id/prs/:number/merge", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.MergePR(c)
	})
	req := httptest.NewRequest("POST", "/repos/1/prs/invalid/merge", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid PR number, got %d", w.Code)
	}
}

func TestMergeAllPRs_InvalidRepoID(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().
		SetGithubID(905).SetLogin("mergeall").SetAccessToken("tok").Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/repos/:id/prs/merge-all", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.MergeAllPRs(c)
	})
	req := httptest.NewRequest("POST", "/repos/invalid/prs/merge-all", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid repo ID, got %d", w.Code)
	}
}

// ─── ReposTab Tests ───────────────────────────────────────────────

func TestReposTab_Renders(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().
		SetGithubID(500).SetLogin("repostab").SetAccessToken("tok").Save(context.Background())
	client.Repository.Create().SetGithubID(30).SetOwner("u").SetName("rep1").SetFullName("u/rep1").SetHTMLURL("https://github.com/u/rep1").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())
	client.Repository.Create().SetGithubID(31).SetOwner("u").SetName("rep2").SetFullName("u/rep2").SetHTMLURL("https://github.com/u/rep2").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	h := func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.ReposTab(c) }
	w := serveDashboardRequest(h, "GET", "/dashboard", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "u/rep1") || !strings.Contains(w.Body.String(), "u/rep2") {
		t.Errorf("expected both repos in response, body: %s", w.Body.String())
	}
}

func TestReposTab_OverviewCardRenders(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().
		SetGithubID(501).SetLogin("overviewtest").SetAccessToken("tok").Save(context.Background())
	client.Repository.Create().
		SetGithubID(40).SetOwner("u").SetName("r1").SetFullName("u/r1").
		SetHTMLURL("https://github.com/u/r1").SetDefaultBranch("main").SetUserID(u.ID).
		SetOpenPrCount(2).SetReleaseCount(3).SetWorkflowStatus("failure").
		SetSyncedAt(time.Now()).
		Save(context.Background())
	client.Repository.Create().
		SetGithubID(41).SetOwner("u").SetName("r2").SetFullName("u/r2").
		SetHTMLURL("https://github.com/u/r2").SetDefaultBranch("main").SetUserID(u.ID).
		SetOpenPrCount(1).SetReleaseCount(1).SetWorkflowStatus("success").
		SetSyncedAt(time.Now().Add(-time.Hour)).
		Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"shortSHA":             func(s string) string { return s },
		"formatTime":           func(t time.Time) string { return "" },
		"truncate":             func(s string, n int) string { return s },
		"workflowIcon":         func(status string) string { return "" },
		"workflowLabel":        func(status string) string { return "" },
		"hasWorkflowRun":       func(status string) bool { return false },
		"printf":               func(format string, args ...any) string { return "" },
		"releaseIcon":          func(conclusion string) string { return "" },
		"releaseLabel":         func(conclusion string) string { return "" },
		"hasReleaseConclusion": func(s string) bool { return false },
		"timeSince":            func(t time.Time) string { return "" },
		"eventIcon":            func(eventType string) string { return "" },
		"contains":             func(s, substr string) bool { return strings.Contains(s, substr) },
		"gt":                   func(a, b int) bool { return a > b },
	}).Parse(`{{define "repo_overview_card"}}<div class="overview-card"><span class="ov-total">{{.TotalRepos}}</span><span class="ov-prs">{{.OpenPRs}}</span><span class="ov-fail">{{.FailingRepos}}</span><span class="ov-rel">{{.TotalReleases}}</span><span class="ov-sync">{{.LastSyncTime}}</span></div>{{end}}{{define "repos_tab"}}{{if .Repos}}{{template "repo_overview_card" .Overview}}{{end}}{{range .Repos}}<div class="repo">{{.FullName}}</div>{{end}}{{end}}`)))
	engine.GET("/dashboard", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.ReposTab(c)
	})
	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "overview-card") {
		t.Errorf("expected overview card in response, body: %s", body)
	}
	if !strings.Contains(body, "ov-total\">2") {
		t.Errorf("expected 2 total repos, body: %s", body)
	}
	if !strings.Contains(body, "ov-prs\">3") {
		t.Errorf("expected 3 open PRs, body: %s", body)
	}
	if !strings.Contains(body, "ov-fail\">1") {
		t.Errorf("expected 1 failing repo, body: %s", body)
	}
	if !strings.Contains(body, "ov-rel\">4") {
		t.Errorf("expected 4 releases, body: %s", body)
	}
	if !strings.Contains(body, "ov-sync") {
		t.Errorf("expected last sync time in response, body: %s", body)
	}
}

func TestReposTab_OverviewCard_HidesWhenNoRepos(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().
		SetGithubID(502).SetLogin("overviewempty").SetAccessToken("tok").Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"shortSHA":             func(s string) string { return s },
		"formatTime":           func(t time.Time) string { return "" },
		"truncate":             func(s string, n int) string { return s },
		"workflowIcon":         func(status string) string { return "" },
		"workflowLabel":        func(status string) string { return "" },
		"hasWorkflowRun":       func(status string) bool { return false },
		"printf":               func(format string, args ...any) string { return "" },
		"releaseIcon":          func(conclusion string) string { return "" },
		"releaseLabel":         func(conclusion string) string { return "" },
		"hasReleaseConclusion": func(s string) bool { return false },
		"timeSince":            func(t time.Time) string { return "" },
		"eventIcon":            func(eventType string) string { return "" },
		"contains":             func(s, substr string) bool { return strings.Contains(s, substr) },
		"gt":                   func(a, b int) bool { return a > b },
	}).Parse(`{{define "repo_overview_card"}}<div class="overview-card">{{.TotalRepos}}</div>{{end}}{{define "repos_tab"}}{{if .Repos}}{{template "repo_overview_card" .Overview}}{{end}}<div class="repo-list">{{range .Repos}}{{.FullName}}{{end}}</div>{{end}}`)))
	engine.GET("/dashboard", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.ReposTab(c)
	})
	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "overview-card") {
		t.Errorf("expected no overview card when no repos, body: %s", body)
	}
}

// ─── PRsTab Tests ─────────────────────────────────────────────────

func servePRsTabRequest(handler gin.HandlerFunc, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"printf": func(format string, args ...any) string { return "" },
	}).Parse(`{{define "prs_tab"}}<div>{{range .PRs}}<div class="pr-item" data-repo="{{.RepoFullName}}">#{{.Number}} {{.Title}} ({{.RepoFullName}})</div>{{else}}<div class="empty">no PRs</div>{{end}}</div>{{end}}`)))
	engine.GET("/prs", handler)
	req := httptest.NewRequest("GET", path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	engine.ServeHTTP(w, req)
	return w
}

func TestPRsTab_Empty(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(600).SetLogin("prsempty").SetAccessToken("tok").Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	h := func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.PRsTab(c) }
	w := servePRsTabRequest(h, "/prs", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no PRs") {
		t.Errorf("expected empty state, body: %s", w.Body.String())
	}
}

func TestPRsTab_WithPRs(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(601).SetLogin("prswith").SetAccessToken("tok").Save(context.Background())
	client.Repository.Create().SetGithubID(40).SetOwner("u").SetName("repo-a").SetFullName("u/repo-a").SetHTMLURL("https://github.com/u/repo-a").SetDefaultBranch("main").SetUserID(u.ID).
		SetPullRequests(`[{"n":1,"t":"PR one","a":"dev1","c":"2024-06-02T10:00:00Z","h":"https://github.com/u/repo-a/pull/1","hr":"br1","br":"main"}]`).SetOpenPrCount(1).Save(context.Background())
	client.Repository.Create().SetGithubID(41).SetOwner("u").SetName("repo-b").SetFullName("u/repo-b").SetHTMLURL("https://github.com/u/repo-b").SetDefaultBranch("main").SetUserID(u.ID).
		SetPullRequests(`[{"n":2,"t":"PR two","a":"dev2","c":"2024-06-01T10:00:00Z","h":"https://github.com/u/repo-b/pull/2","hr":"br2","br":"main"}]`).SetOpenPrCount(1).Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	h := func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.PRsTab(c) }
	w := servePRsTabRequest(h, "/prs", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "PR one") || !strings.Contains(body, "PR two") {
		t.Errorf("expected both PRs, body: %s", body)
	}
}

func TestPRsTab_FilterByRepo(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(602).SetLogin("prsfilter").SetAccessToken("tok").Save(context.Background())
	client.Repository.Create().SetGithubID(50).SetOwner("u").SetName("repo-x").SetFullName("u/repo-x").SetHTMLURL("https://github.com/u/repo-x").SetDefaultBranch("main").SetUserID(u.ID).
		SetPullRequests(`[{"n":10,"t":"PR ten","a":"dev1","c":"2024-06-01T10:00:00Z","h":"","hr":"b","br":"m"}]`).SetOpenPrCount(1).Save(context.Background())
	client.Repository.Create().SetGithubID(51).SetOwner("u").SetName("repo-y").SetFullName("u/repo-y").SetHTMLURL("https://github.com/u/repo-y").SetDefaultBranch("main").SetUserID(u.ID).
		SetPullRequests(`[{"n":11,"t":"PR eleven","a":"dev2","c":"2024-06-01T10:00:00Z","h":"","hr":"b","br":"m"}]`).SetOpenPrCount(1).Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	h := func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.PRsTab(c) }
	w := servePRsTabRequest(h, "/prs?repo=u/repo-x", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "PR ten") {
		t.Errorf("expected PR ten in filtered results, body: %s", body)
	}
	if strings.Contains(body, "PR eleven") {
		t.Errorf("did not expect PR eleven in filtered results, body: %s", body)
	}
}

// ─── MergeSinglePR Tests ──────────────────────────────────────────

func TestMergeSinglePR_InvalidBody(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(700).SetLogin("mergeinv").SetAccessToken("tok").Save(context.Background())
	sessionID := store.Set(int64(u.ID))

	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/prs/merge", func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.MergeSinglePR(c) })
	req := httptest.NewRequest("POST", "/prs/merge", strings.NewReader(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestMergeSinglePR_NoRepo(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(701).SetLogin("mergenorepo").SetAccessToken("tok").Save(context.Background())
	sessionID := store.Set(int64(u.ID))

	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/prs/merge", func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.MergeSinglePR(c) })
	body := strings.NewReader(`{"repo_id":999,"pr_number":1}`)
	req := httptest.NewRequest("POST", "/prs/merge", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMergeSinglePR_Success(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(702).SetLogin("mergesucc").SetAccessToken("tok").Save(context.Background())
	r, _ := client.Repository.Create().SetGithubID(60).SetOwner("u").SetName("mergerepo").SetFullName("u/mergerepo").SetHTMLURL("https://github.com/u/mergerepo").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"merged":true,"message":"Pull Request successfully merged"}`))
	}))
	defer apiSrv.Close()
	handler.gh.APIURL = apiSrv.URL

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"printf": fmt.Sprintf,
	}).Parse(`{{define "prs_tab"}}<div>{{range .PRs}}<div class="pr-item">#{{.Number}}</div>{{else}}<div class="empty">no PRs</div>{{end}}</div>{{end}}{{define "merge_toast"}}{{if .ToastType}}<div id="toast-container" hx-swap-oob="beforeend"><div class="toast">{{.ToastMessage}}{{if .ToastDetails}} <small>{{.ToastDetails}}</small>{{end}}</div></div>{{end}}{{end}}{{define "prs_tab_with_toast"}}{{template "merge_toast" .}}{{template "prs_tab" .}}{{end}}`)))
	engine.POST("/prs/merge", func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.MergeSinglePR(c) })
	body := strings.NewReader(fmt.Sprintf(`{"repo_id":%d,"pr_number":42}`, r.ID))
	req := httptest.NewRequest("POST", "/prs/merge", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "merged successfully") {
		t.Errorf("expected success message, got: %s", w.Body.String())
	}
}

func TestMergeSinglePR_SuccessFormEncoded(t *testing.T) {
	// HTMX sends hx-vals data as URL-encoded form data (not JSON).
	// Verify ShouldBind (not ShouldBindJSON) correctly handles this.
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(703).SetLogin("mergeform").SetAccessToken("tok").Save(context.Background())
	r, _ := client.Repository.Create().SetGithubID(61).SetOwner("u").SetName("formrepo").SetFullName("u/formrepo").SetHTMLURL("https://github.com/u/formrepo").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"merged":true,"message":"Pull Request successfully merged"}`))
	}))
	defer apiSrv.Close()
	handler.gh.APIURL = apiSrv.URL

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"printf": fmt.Sprintf,
	}).Parse(`{{define "prs_tab"}}<div>{{range .PRs}}<div class="pr-item">#{{.Number}}</div>{{else}}<div class="empty">no PRs</div>{{end}}</div>{{end}}{{define "merge_toast"}}{{if .ToastType}}<div id="toast-container" hx-swap-oob="beforeend"><div class="toast">{{.ToastMessage}}{{if .ToastDetails}} <small>{{.ToastDetails}}</small>{{end}}</div></div>{{end}}{{end}}{{define "prs_tab_with_toast"}}{{template "merge_toast" .}}{{template "prs_tab" .}}{{end}}`)))
	engine.POST("/prs/merge", func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.MergeSinglePR(c) })
	// Send URL-encoded form data (matching HTMX's hx-vals behavior)
	body := strings.NewReader(fmt.Sprintf("repo_id=%d&pr_number=42", r.ID))
	req := httptest.NewRequest("POST", "/prs/merge", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "merged successfully") {
		t.Errorf("expected success message, got: %s", w.Body.String())
	}
}

// ─── BatchMergePRs Tests ──────────────────────────────────────────

func TestBatchMergePRs_NoSelection(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(800).SetLogin("batchno").SetAccessToken("tok").Save(context.Background())
	sessionID := store.Set(int64(u.ID))

	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/prs/batch-merge", func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.BatchMergePRs(c) })
	req := httptest.NewRequest("POST", "/prs/batch-merge", strings.NewReader(``))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty selection, got %d", w.Code)
	}
}

func TestBatchMergePRs_Success(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(801).SetLogin("batchsucc").SetAccessToken("tok").Save(context.Background())
	r1, _ := client.Repository.Create().SetGithubID(70).SetOwner("u").SetName("batch-a").SetFullName("u/batch-a").SetHTMLURL("https://github.com/u/batch-a").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())
	r2, _ := client.Repository.Create().SetGithubID(71).SetOwner("u").SetName("batch-b").SetFullName("u/batch-b").SetHTMLURL("https://github.com/u/batch-b").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())

	callCount := 0
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		// Return appropriate data based on request path
		if strings.Contains(r.URL.Path, "/pulls/") && strings.Contains(r.URL.Path, "/merge") {
			w.Write([]byte(`{"merged":true,"message":"ok"}`))
		} else if strings.Contains(r.URL.Path, "/pulls") {
			w.Write([]byte(`[]`))
		} else if strings.Contains(r.URL.Path, "/commits") {
			w.Write([]byte(`[]`))
		} else if strings.Contains(r.URL.Path, "/releases") {
			w.Write([]byte(`[]`))
		} else if strings.Contains(r.URL.Path, "/actions/runs") {
			w.Write([]byte(`{"workflow_runs":[]}`))
		} else {
			w.Write([]byte(`{"merged":true,"message":"ok"}`))
		}
	}))
	defer apiSrv.Close()
	handler.gh.APIURL = apiSrv.URL

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"printf": fmt.Sprintf,
	}).Parse(`{{define "prs_tab"}}<div>{{range .PRs}}<div class="pr-item">#{{.Number}}</div>{{else}}<div class="empty">no PRs</div>{{end}}</div>{{end}}{{define "merge_toast"}}{{if .ToastType}}<div id="toast-container" hx-swap-oob="beforeend"><div class="toast">{{.ToastMessage}}{{if .ToastDetails}} <small>{{.ToastDetails}}</small>{{end}}</div></div>{{end}}{{end}}{{define "prs_tab_with_toast"}}{{template "merge_toast" .}}{{template "prs_tab" .}}{{end}}`)))
	engine.POST("/prs/batch-merge", func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.BatchMergePRs(c) })
	form := fmt.Sprintf("pr_ids=%d:1&pr_ids=%d:2", r1.ID, r2.ID)
	req := httptest.NewRequest("POST", "/prs/batch-merge", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "All 2 PR") {
		t.Errorf("expected all 2 merged, got: %s", w.Body.String())
	}
	// Post-merge syncs now run in the background (deduped, async), so
	// only the 2 merge calls are guaranteed synchronously.
	if callCount < 2 {
		t.Errorf("expected at least 2 merge API calls, got %d", callCount)
	}
}

// ─── MetricsTab Tests ─────────────────────────────────────────────

func serveMetricsTabRequest(handler gin.HandlerFunc, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"printf":         func(format string, args ...any) string { return "" },
		"workflowIcon":   func(status string) string { return "" },
		"workflowLabel":  func(status string) string { return "" },
		"hasWorkflowRun": func(status string) bool { return false },
	}).Parse(`{{define "metrics_tab"}}<div class="metrics">{{if .Metrics}}<span class="total-repos">{{.Metrics.TotalRepos}}</span>{{end}}{{range .Repos}}<span class="repo-name">{{.FullName}}</span>{{end}}</div>{{end}}`)))
	engine.GET("/metrics", handler)
	req := httptest.NewRequest("GET", path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	engine.ServeHTTP(w, req)
	return w
}

func TestMetricsTab_Renders(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(900).SetLogin("metrictab").SetAccessToken("tok").Save(context.Background())
	client.Repository.Create().SetGithubID(80).SetOwner("u").SetName("m1").SetFullName("u/m1").SetHTMLURL("https://github.com/u/m1").SetDefaultBranch("main").SetUserID(u.ID).
		SetTotalCommitsFetched(10).SetFeatCount(4).SetFixCount(3).SetDocsCount(2).SetChoreCount(1).SetReleaseCount(2).SetWorkflowSuccessCount(5).SetWorkflowFailureCount(1).Save(context.Background())
	client.Repository.Create().SetGithubID(81).SetOwner("u").SetName("m2").SetFullName("u/m2").SetHTMLURL("https://github.com/u/m2").SetDefaultBranch("main").SetUserID(u.ID).
		SetTotalCommitsFetched(20).SetFeatCount(2).SetFixCount(2).SetDocsCount(1).SetChoreCount(3).SetReleaseCount(3).SetWorkflowSuccessCount(8).SetWorkflowFailureCount(2).Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	h := func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.MetricsTab(c) }
	w := serveMetricsTabRequest(h, "/metrics", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "u/m1") || !strings.Contains(body, "u/m2") {
		t.Errorf("expected both repos in metrics, body: %s", body)
	}
	if !strings.Contains(body, "2") {
		t.Errorf("expected total repos count (2) in response, body: %s", body)
	}
}

// ─── Index Tests ──────────────────────────────────────────────────

func serveIndexRequest(handler gin.HandlerFunc, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	engine := gin.New()
	tmpl := template.New("index.html")
	tmpl.Funcs(template.FuncMap{
		"appVersion":           func() string { return "test" },
		"shortSHA":             func(s string) string { return s },
		"formatTime":           func(t time.Time) string { return "" },
		"truncate":             func(s string, n int) string { return s },
		"workflowIcon":         func(status string) string { return "" },
		"workflowLabel":        func(status string) string { return "" },
		"hasWorkflowRun":       func(status string) bool { return false },
		"printf":               func(format string, args ...any) string { return "" },
		"releaseIcon":          func(conclusion string) string { return "" },
		"releaseLabel":         func(conclusion string) string { return "" },
		"hasReleaseConclusion": func(s string) bool { return false },
		"contains":             func(s, substr string) bool { return strings.Contains(s, substr) },
		"timeSince":            func(t time.Time) string { return "" },
		"eventIcon":            func(eventType string) string { return "" },
		"sub":                  func(a, b int) int { return a - b },
	})
	template.Must(tmpl.ParseFiles("../../static/index.html"))
	engine.SetHTMLTemplate(tmpl)
	engine.GET("/", handler)
	req := httptest.NewRequest("GET", path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	engine.ServeHTTP(w, req)
	return w
}

func TestIndex_RendersFullPage(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(1000).SetLogin("indexuser").SetAvatarURL("https://avatars.example.com/u.png").SetAccessToken("tok").Save(context.Background())
	client.Repository.Create().SetGithubID(90).SetOwner("u").SetName("idx").SetFullName("u/idx").SetHTMLURL("https://github.com/u/idx").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	w := serveIndexRequest(handler.Index, "/", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `nav-link active`) {
		t.Errorf("expected active-tab=repos, body: %s", body)
	}
	if !strings.Contains(body, `hx-get="/dashboard"`) {
		t.Errorf("expected htmx lazy-load for repos, body: %s", body)
	}
	if !strings.Contains(body, "Loading repositories") {
		t.Errorf("expected loading indicator, body: %s", body)
	}
}

func TestIndex_Unauthenticated(t *testing.T) {
	handler, _, _ := newTestDashboardHandler(t)
	w := serveIndexRequest(handler.Index, "/")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, `nav-link active`) {
		t.Errorf("expected no active tab for unauthenticated user, body: %s", body)
	}
}

func TestIndex_ShowSetupWhenNoRepos(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, err := client.User.Create().
		SetGithubID(1100).SetLogin("setupuser").SetAvatarURL("https://avatars.example.com/u.png").SetAccessToken("tok").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	pageCount := 0
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageCount++
		w.Header().Set("Content-Type", "application/json")
		if pageCount == 1 {
			w.Write([]byte(`[
				{"id":1,"name":"repo1","full_name":"user/repo1","description":"First repo","html_url":"https://github.com/user/repo1","language":"Go","default_branch":"main","owner":{"login":"user"}},
				{"id":2,"name":"repo2","full_name":"user/repo2","description":"Second repo","html_url":"https://github.com/user/repo2","language":"Python","default_branch":"main","owner":{"login":"user"}}
			]`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	defer apiSrv.Close()
	handler.gh.APIURL = apiSrv.URL

	sessionID := store.Set(int64(u.ID))

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"shortSHA":             func(s string) string { return s },
		"formatTime":           func(t time.Time) string { return "" },
		"truncate":             func(s string, n int) string { return s },
		"workflowIcon":         func(status string) string { return "" },
		"workflowLabel":        func(status string) string { return "" },
		"hasWorkflowRun":       func(status string) bool { return false },
		"printf":               func(format string, args ...any) string { return "" },
		"releaseIcon":          func(conclusion string) string { return "" },
		"releaseLabel":         func(conclusion string) string { return "" },
		"hasReleaseConclusion": func(s string) bool { return false },
		"contains":             func(s, substr string) bool { return strings.Contains(s, substr) },
		"timeSince":            func(t time.Time) string { return "" },
		"eventIcon":            func(eventType string) string { return "" },
	}).Parse(`{{define "available_repos"}}<h2>Select Repositories</h2><div hx-get="{{if .ShowSetup}}/{{else}}/settings{{end}}">back</div>{{range .Repos}}<div class="repo">{{.FullName}}</div>{{end}}{{end}}{{define "repos_tab"}}<div>Import All</div>{{end}}{{define "index.html"}}<div>{{if .User}}{{if .ShowSetup}}{{template "available_repos" .}}{{else}}{{template "repos_tab" .}}{{end}}{{end}}</div>{{end}}`)))
	engine.GET("/", handler.Index)
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()

	if !strings.Contains(body, "Select Repositories") {
		t.Errorf("expected 'Select Repositories' heading, body: %s", body)
	}
	if !strings.Contains(body, "user/repo1") || !strings.Contains(body, "user/repo2") {
		t.Errorf("expected both available repos, body: %s", body)
	}
	if strings.Contains(body, "Import All") {
		t.Errorf("did not expect Import All button in setup mode, body: %s", body)
	}
}

func TestIndex_RendersNormalWhenHasRepos(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().
		SetGithubID(1101).SetLogin("normaluser").SetAvatarURL("https://avatars.example.com/u.png").SetAccessToken("tok").
		Save(context.Background())
	client.Repository.Create().SetGithubID(95).SetOwner("u").SetName("myrepo").SetFullName("u/myrepo").SetHTMLURL("https://github.com/u/myrepo").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	w := serveIndexRequest(handler.Index, "/", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	// Should NOT show setup heading
	if strings.Contains(body, "<h2>Select Repositories</h2>") {
		t.Errorf("did not expect setup heading when user has tracked repos, body: %s", body)
	}
	// Should show htmx lazy-load for repos instead of server-rendered content
	if !strings.Contains(body, `hx-get="/dashboard"`) {
		t.Errorf("expected htmx lazy-load for repos, body: %s", body)
	}
}

// ─── RenovateRebaseAll Tests ─────────────────────────────────────────

// ─── Sort Param Tests ──────────────────────────────────────────────

func TestParseSortParam_ValidKeys(t *testing.T) {
	tests := []struct {
		key            string
		expectPostSort bool
	}{
		{"updated_at", false},
		{"build_status", true},
		{"latest_commit", false},
		{"latest_release", false},
		{"pass_rate", false},
		{"name_asc", false},
		{"name_desc", false},
		{"synced_at", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			_, needsPostSort := parseSortParam(tt.key)
			if needsPostSort != tt.expectPostSort {
				t.Errorf("parseSortParam(%q) needsPostSort = %v, want %v", tt.key, needsPostSort, tt.expectPostSort)
			}
		})
	}
}

func TestParseSortParam_InvalidKeyFallsBack(t *testing.T) {
	order, needsPostSort := parseSortParam("invalid_key")
	if needsPostSort {
		t.Error("expected needsPostSort=false for invalid key")
	}
	if order == nil {
		t.Error("expected non-nil order for default fallback")
	}
}

func TestSortByBuildStatus(t *testing.T) {
	repos := []*ent.Repository{
		{WorkflowStatus: "success", FullName: "b/repo"},
		{WorkflowStatus: "failure", FullName: "a/repo"},
		{WorkflowStatus: "unknown", FullName: "c/repo"},
		{WorkflowStatus: "failure", FullName: "z/repo"},
		{WorkflowStatus: "", FullName: "m/repo"},
	}
	sortByBuildStatus(repos)

	expected := []string{"a/repo", "z/repo", "c/repo", "b/repo", "m/repo"}
	for i, r := range repos {
		if r.FullName != expected[i] {
			t.Errorf("position %d: expected %s, got %s", i, expected[i], r.FullName)
		}
	}
}

func TestGroupEventsByDate(t *testing.T) {
	now := time.Now()
	today := now.Truncate(24 * time.Hour)
	yesterday := today.Add(-24 * time.Hour)

	events := []feedEvent{
		{Timestamp: today.Add(2 * time.Hour), Title: "Today event 1", Type: "release"},
		{Timestamp: today.Add(1 * time.Hour), Title: "Today event 2", Type: "workflow_failure"},
		{Timestamp: yesterday.Add(3 * time.Hour), Title: "Yesterday event", Type: "pr_merge"},
		{Timestamp: yesterday.Add(-48 * time.Hour), Title: "Older event", Type: "release"},
	}
	groups := groupEventsByDate(events)
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	if groups[0].Date != "Today" {
		t.Errorf("first group: expected Today, got %s", groups[0].Date)
	}
	if groups[1].Date != "Yesterday" {
		t.Errorf("second group: expected Yesterday, got %s", groups[1].Date)
	}
	if len(groups[0].Events) != 2 {
		t.Errorf("expected 2 today events, got %d", len(groups[0].Events))
	}
}

func TestGroupEventsByDate_Empty(t *testing.T) {
	groups := groupEventsByDate(nil)
	if groups != nil {
		t.Error("expected nil for empty input")
	}
	groups = groupEventsByDate([]feedEvent{})
	if groups != nil {
		t.Error("expected nil for empty slice")
	}
}

// ─── ReposTab Sort Integration Tests ───────────────────────────────

func serveDashboardWithTimeline(handler gin.HandlerFunc, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	engine := gin.New()
	engine.SetHTMLTemplate(template.Must(template.New("").Funcs(template.FuncMap{
		"shortSHA":             func(s string) string { return s },
		"formatTime":           func(t time.Time) string { return "" },
		"truncate":             func(s string, n int) string { return s },
		"workflowIcon":         func(status string) string { return "" },
		"workflowLabel":        func(status string) string { return "" },
		"hasWorkflowRun":       func(status string) bool { return false },
		"printf":               func(format string, args ...any) string { return "" },
		"releaseIcon":          func(conclusion string) string { return "" },
		"releaseLabel":         func(conclusion string) string { return "" },
		"hasReleaseConclusion": func(s string) bool { return false },
		"timeSince":            func(t time.Time) string { return "" },
		"eventIcon":            func(eventType string) string { return "" },
		"contains":             func(s, substr string) bool { return strings.Contains(s, substr) },
	}).Parse(`{{define "repos_tab"}}<div>{{range .Repos}}<div class="repo">{{.FullName}}</div>{{end}}</div>{{template "homepage_timeline" .}}{{end}}{{define "homepage_timeline"}}{{if .TimelineGroups}}{{range .TimelineGroups}}<div class="tl-group" data-date="{{.Date}}">{{range .Events}}<div class="tl-event" data-type="{{.Type}}">{{.Title}}</div>{{end}}</div>{{end}}{{else}}<div class="tl-empty">No recent activity</div>{{end}}{{end}}`)))
	engine.GET("/dashboard", handler)
	req := httptest.NewRequest("GET", path, nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	engine.ServeHTTP(w, req)
	return w
}

func TestReposTab_SortParam_Default(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(1200).SetLogin("sortdefault").SetAccessToken("tok").Save(context.Background())
	client.Repository.Create().SetGithubID(100).SetOwner("u").SetName("a").SetFullName("u/a").SetHTMLURL("https://github.com/u/a").SetDefaultBranch("main").SetUpdatedAt(time.Now()).SetUserID(u.ID).Save(context.Background())
	client.Repository.Create().SetGithubID(101).SetOwner("u").SetName("b").SetFullName("u/b").SetHTMLURL("https://github.com/u/b").SetDefaultBranch("main").SetUpdatedAt(time.Now().Add(-time.Hour)).SetUserID(u.ID).Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	h := func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.ReposTab(c) }
	w := serveDashboardWithTimeline(h, "/dashboard", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestReposTab_SortParam_Valid(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(1201).SetLogin("sortvalid").SetAccessToken("tok").Save(context.Background())
	client.Repository.Create().SetGithubID(102).SetOwner("u").SetName("x").SetFullName("u/x").SetHTMLURL("https://github.com/u/x").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	h := func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.ReposTab(c) }
	w := serveDashboardWithTimeline(h, "/dashboard?sort=name_asc", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid sort, got %d", w.Code)
	}
}

// ─── Timeline Integration Tests ────────────────────────────────────

func TestReposTab_Timeline_RendersEvents(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(1300).SetLogin("timeline").SetAccessToken("tok").Save(context.Background())
	r, _ := client.Repository.Create().SetGithubID(110).SetOwner("u").SetName("timeline-repo").SetFullName("u/timeline-repo").SetHTMLURL("https://github.com/u/timeline-repo").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())
	client.Event.Create().SetRepoID(r.ID).SetType("release").SetTitle("v1.0.0").SetURL("https://github.com/u/timeline-repo/releases/v1.0.0").SetTimestamp(time.Now()).Save(context.Background())
	client.Event.Create().SetRepoID(r.ID).SetType("workflow_failure").SetTitle("CI failed").SetURL("https://github.com/u/timeline-repo/actions/1").SetTimestamp(time.Now()).Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	h := func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.ReposTab(c) }
	w := serveDashboardWithTimeline(h, "/dashboard", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "v1.0.0") {
		t.Errorf("expected release event in response, body: %s", body)
	}
	if !strings.Contains(body, "CI failed") {
		t.Errorf("expected workflow failure event in response, body: %s", body)
	}
	if !strings.Contains(body, "tl-group") {
		t.Errorf("expected timeline group in response, body: %s", body)
	}
}

func TestReposTab_Timeline_Empty(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().SetGithubID(1301).SetLogin("timelineempty").SetAccessToken("tok").Save(context.Background())
	client.Repository.Create().SetGithubID(111).SetOwner("u").SetName("empty-repo").SetFullName("u/empty-repo").SetHTMLURL("https://github.com/u/empty-repo").SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	h := func(c *gin.Context) { c.Set("user_id", int64(u.ID)); handler.ReposTab(c) }
	w := serveDashboardWithTimeline(h, "/dashboard", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "No recent activity") {
		t.Errorf("expected empty timeline state, body: %s", body)
	}
}

func TestReposTab_Timeline_FiltersByUser(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u1, _ := client.User.Create().SetGithubID(1400).SetLogin("user1").SetAccessToken("tok").Save(context.Background())
	u2, _ := client.User.Create().SetGithubID(1401).SetLogin("user2").SetAccessToken("tok2").Save(context.Background())
	r1, _ := client.Repository.Create().SetGithubID(120).SetOwner("u1").SetName("r1").SetFullName("u1/r1").SetHTMLURL("https://github.com/u1/r1").SetDefaultBranch("main").SetUserID(u1.ID).Save(context.Background())
	client.Repository.Create().SetGithubID(121).SetOwner("u2").SetName("r2").SetFullName("u2/r2").SetHTMLURL("https://github.com/u2/r2").SetDefaultBranch("main").SetUserID(u2.ID).Save(context.Background())
	// Event for user1's repo
	client.Event.Create().SetRepoID(r1.ID).SetType("release").SetTitle("v1.0").SetURL("https://github.com/u1/r1/releases/v1.0").SetTimestamp(time.Now()).Save(context.Background())
	// Event for user2's repo (should not appear for user1)
	client.Event.Create().SetRepoID(999).SetType("release").SetTitle("v2.0").SetURL("https://github.com/u2/r2/releases/v2.0").SetTimestamp(time.Now()).Save(context.Background())

	sessionID := store.Set(int64(u1.ID))
	h := func(c *gin.Context) { c.Set("user_id", int64(u1.ID)); handler.ReposTab(c) }
	w := serveDashboardWithTimeline(h, "/dashboard", &http.Cookie{Name: "gitlens_session", Value: sessionID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "v1.0") {
		t.Errorf("expected user1's event in response, body: %s", body)
	}
	if strings.Contains(body, "v2.0") {
		t.Errorf("did not expect user2's event in response, body: %s", body)
	}
}

// ─── RenovateRebaseAll Tests ─────────────────────────────────────────

func TestRenovateRebaseAll_InvalidRepoID(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().
		SetGithubID(1200).SetLogin("rebaseinvalid").SetAccessToken("tok").Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/repos/:id/renovate/rebase-all", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.RenovateRebaseAll(c)
	})
	req := httptest.NewRequest("POST", "/repos/invalid/renovate/rebase-all", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid repo ID, got %d", w.Code)
	}
}

func TestRenovateRebaseAll_RepoNotFound(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().
		SetGithubID(1201).SetLogin("rebasenotfound").SetAccessToken("tok").Save(context.Background())

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/repos/:id/renovate/rebase-all", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.RenovateRebaseAll(c)
	})
	req := httptest.NewRequest("POST", "/repos/99999/renovate/rebase-all", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent repo, got %d", w.Code)
	}
}

func TestRenovateRebaseAll_Success(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, err := client.User.Create().
		SetGithubID(1202).SetLogin("rebasesuccess").SetAccessToken("tok").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	r, err := client.Repository.Create().
		SetGithubID(100).SetOwner("user").SetName("rebasetest").
		SetFullName("user/rebasetest").SetHTMLURL("https://github.com/user/rebasetest").
		SetDefaultBranch("main").SetUserID(u.ID).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	callCount := 0
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/issues") && r.Method == "GET" {
			w.Write([]byte(`[
				{"number":5,"title":"Dependency Dashboard","body":"## Pending\n- [ ] Rebase all open PRs <!-- rebase-all-open-prs-checkbox -->","state":"open"}
			]`))
		} else if strings.Contains(r.URL.Path, "/issues") && r.Method == "PATCH" {
			w.Write([]byte(`{"number":5,"title":"Dependency Dashboard"}`))
		} else {
			t.Logf("Unexpected API call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiSrv.Close()
	handler.gh.APIURL = apiSrv.URL

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/repos/:id/renovate/rebase-all", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.RenovateRebaseAll(c)
	})
	req := httptest.NewRequest("POST", fmt.Sprintf("/repos/%d/renovate/rebase-all", r.ID), http.NoBody)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Renovate rebase-all triggered successfully") {
		t.Errorf("expected success message, got: %s", w.Body.String())
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls (list issues + update issue), got %d", callCount)
	}
}

func TestRenovateRebaseAll_GitHubError(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().
		SetGithubID(1203).SetLogin("rebasegherr").SetAccessToken("tok").
		Save(context.Background())

	r, _ := client.Repository.Create().
		SetGithubID(101).SetOwner("user").SetName("rebaseerr").
		SetFullName("user/rebaseerr").SetHTMLURL("https://github.com/user/rebaseerr").
		SetDefaultBranch("main").SetUserID(u.ID).
		Save(context.Background())

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer apiSrv.Close()
	handler.gh.APIURL = apiSrv.URL

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/repos/:id/renovate/rebase-all", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.RenovateRebaseAll(c)
	})
	req := httptest.NewRequest("POST", fmt.Sprintf("/repos/%d/renovate/rebase-all", r.ID), http.NoBody)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for GitHub API error, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Failed to trigger Renovate rebase") {
		t.Errorf("expected failure message, got: %s", w.Body.String())
	}
}

func TestRenovateRebaseAll_NoDashboard(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().
		SetGithubID(1204).SetLogin("rebasenodash").SetAccessToken("tok").
		Save(context.Background())

	r, _ := client.Repository.Create().
		SetGithubID(102).SetOwner("user").SetName("nodash").
		SetFullName("user/nodash").SetHTMLURL("https://github.com/user/nodash").
		SetDefaultBranch("main").SetUserID(u.ID).
		Save(context.Background())

	pageCount := 0
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageCount++
		w.Header().Set("Content-Type", "application/json")
		if pageCount == 1 {
			w.Write([]byte(`[
				{"number":1,"title":"Bug report","body":"Something is broken","state":"open"}
			]`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	defer apiSrv.Close()
	handler.gh.APIURL = apiSrv.URL

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/repos/:id/renovate/rebase-all", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.RenovateRebaseAll(c)
	})
	req := httptest.NewRequest("POST", fmt.Sprintf("/repos/%d/renovate/rebase-all", r.ID), http.NoBody)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for missing dashboard, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no Dependency Dashboard issue found") {
		t.Errorf("expected dashboard-not-found message, got: %s", w.Body.String())
	}
}

func TestRenovateRebaseAll_CheckboxAlreadyChecked(t *testing.T) {
	handler, store, client := newTestDashboardHandler(t)
	u, _ := client.User.Create().
		SetGithubID(1205).SetLogin("rebasechecked").SetAccessToken("tok").
		Save(context.Background())

	r, _ := client.Repository.Create().
		SetGithubID(103).SetOwner("user").SetName("alreadychecked").
		SetFullName("user/alreadychecked").SetHTMLURL("https://github.com/user/alreadychecked").
		SetDefaultBranch("main").SetUserID(u.ID).
		Save(context.Background())

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"number":10,"title":"Dependency Dashboard","body":"## Pending\n- [x] Rebase all open PRs <!-- rebase-all-open-prs-checkbox -->","state":"open"}
		]`))
	}))
	defer apiSrv.Close()
	handler.gh.APIURL = apiSrv.URL

	sessionID := store.Set(int64(u.ID))
	w := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/repos/:id/renovate/rebase-all", func(c *gin.Context) {
		c.Set("user_id", int64(u.ID))
		handler.RenovateRebaseAll(c)
	})
	req := httptest.NewRequest("POST", fmt.Sprintf("/repos/%d/renovate/rebase-all", r.ID), http.NoBody)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (already checked is a no-op success), got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Renovate rebase-all triggered successfully") {
		t.Errorf("expected success message for already-checked, got: %s", w.Body.String())
	}
}
