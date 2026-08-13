package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gitlens/ent"
	"gitlens/ent/enttest"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

// webhookStub simulates the GitHub hooks REST API so tests can assert on
// webhook registration without hitting api.github.com.
type webhookStub struct {
	mu        sync.Mutex
	existing  string // hook config.url returned by GET ("" = empty list)
	posts     []createHookRequest
	failPosts int // number of POSTs to reject with 500 before succeeding
}

func newWebhookStub(t *testing.T) *webhookStub {
	t.Helper()
	stub := &webhookStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			if stub.existing != "" {
				fmt.Fprintf(w, `[{"config":{"url":%q}}]`, stub.existing)
			} else {
				w.Write([]byte(`[]`))
			}
		case http.MethodPost:
			if stub.failPosts > 0 {
				stub.failPosts--
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"message":"boom"}`))
				return
			}
			var req createHookRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			stub.posts = append(stub.posts, req)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":1}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)

	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
	return stub
}

func newTestGitHubAppHandler(t *testing.T) (*GitHubAppHandler, *ent.Client) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
	return NewGitHubAppHandler(client), client
}

func installPayload(account string, accountID int64, repos ...map[string]any) []byte {
	body, _ := json.Marshal(map[string]any{
		"action": "created",
		"installation": map[string]any{
			"id": 12345,
			"account": map[string]any{
				"login": account,
				"id":    accountID,
			},
			"repositories_url": "https://api.github.com/installations/12345/repos",
		},
		"repositories": repos,
	})
	return body
}

func repoPayload(id int64, owner, name string) map[string]any {
	return map[string]any{
		"id":        id,
		"name":      name,
		"full_name": owner + "/" + name,
		"owner":     map[string]any{"login": owner},
	}
}

func TestGitHubApp_HandleInstallation_Created(t *testing.T) {
	stub := newWebhookStub(t)
	handler, client := newTestGitHubAppHandler(t)

	u, _ := client.User.Create().
		SetGithubID(1000).SetLogin("testuser").SetAccessToken("token").Save(context.Background())

	payload := installPayload("testuser", 1000,
		repoPayload(200, "testuser", "app-repo-1"),
		repoPayload(201, "testuser", "app-repo-2"),
	)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github-app", bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.HandleInstallation(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	count, err := client.Repository.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("count error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 repos created, got %d", count)
	}

	// Verify user association
	repos, _ := client.Repository.Query().All(context.Background())
	for _, r := range repos {
		uid, err := r.QueryUser().OnlyID(context.Background())
		if err != nil {
			t.Fatalf("query user edge: %v", err)
		}
		if uid != u.ID {
			t.Errorf("expected repo user ID %d, got %d", u.ID, uid)
		}
	}

	// Webhooks should have been auto-registered for both new repos
	if len(stub.posts) != 2 {
		t.Fatalf("expected 2 webhook registrations, got %d", len(stub.posts))
	}
	for _, p := range stub.posts {
		if p.Config.URL != "http://example.com/webhook/github" {
			t.Errorf("unexpected webhook URL: %s", p.Config.URL)
		}
		if len(p.Events) != 1 || p.Events[0] != "push" {
			t.Errorf("expected events [push], got %v", p.Events)
		}
	}
}

func TestGitHubApp_HandleInstallation_ReinstallNoDuplicate(t *testing.T) {
	stub := newWebhookStub(t)
	handler, client := newTestGitHubAppHandler(t)

	u, _ := client.User.Create().
		SetGithubID(1500).SetLogin("retest").SetAccessToken("token").Save(context.Background())
	client.Repository.Create().
		SetGithubID(250).SetOwner("retest").SetName("existing-repo").
		SetFullName("retest/existing-repo").SetHTMLURL("https://github.com/retest/existing-repo").
		SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())

	// Same repo arrives again in a new installation event
	body := installPayload("retest", 1500, repoPayload(250, "retest", "existing-repo"))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github-app", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.HandleInstallation(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	count, _ := client.Repository.Query().Count(context.Background())
	if count != 1 {
		t.Fatalf("expected 1 repo (no re-import), got %d", count)
	}
	if len(stub.posts) != 0 {
		t.Fatalf("expected no webhook registration for existing repo, got %d", len(stub.posts))
	}
}

func TestGitHubApp_HandleInstallation_NoMatchingUser(t *testing.T) {
	handler, _ := newTestGitHubAppHandler(t)

	payload := map[string]any{
		"action": "created",
		"installation": map[string]any{
			"id": 12345,
			"account": map[string]any{
				"login": "nonexistent-user",
				"id":    99999,
			},
		},
		"repositories": []map[string]any{
			{
				"id":        300,
				"name":      "some-repo",
				"full_name": "nonexistent-user/some-repo",
				"owner":     map[string]any{"login": "nonexistent-user"},
			},
		},
	}

	body, _ := json.Marshal(payload)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github-app", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.HandleInstallation(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestGitHubApp_HandleInstallation_Removed(t *testing.T) {
	handler, client := newTestGitHubAppHandler(t)

	u, _ := client.User.Create().
		SetGithubID(2000).SetLogin("removaltest").SetAccessToken("token").Save(context.Background())

	client.Repository.Create().
		SetGithubID(400).SetOwner("removaltest").SetName("gone-repo").
		SetFullName("removaltest/gone-repo").SetHTMLURL("https://github.com/removaltest/gone-repo").
		SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())

	payload := map[string]any{
		"action": "removed",
		"installation": map[string]any{
			"id": 12345,
			"account": map[string]any{
				"login": "removaltest",
				"id":    2000,
			},
		},
		"repositories": []map[string]any{
			{
				"id":        400,
				"name":      "gone-repo",
				"full_name": "removaltest/gone-repo",
				"owner":     map[string]any{"login": "removaltest"},
			},
		},
	}

	body, _ := json.Marshal(payload)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github-app", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.HandleInstallation(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	count, _ := client.Repository.Query().Count(context.Background())
	if count != 0 {
		t.Fatalf("expected 0 repos after removal, got %d", count)
	}
}

func TestGitHubApp_HandleInstallation_Deleted(t *testing.T) {
	handler, _ := newTestGitHubAppHandler(t)

	payload := map[string]any{
		"action": "deleted",
		"installation": map[string]any{
			"id": 12345,
			"account": map[string]any{
				"login": "anyone",
				"id":    3000,
			},
		},
	}

	body, _ := json.Marshal(payload)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github-app", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.HandleInstallation(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestGitHubApp_HandleInstallation_InvalidPayload(t *testing.T) {
	handler, _ := newTestGitHubAppHandler(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github-app", bytes.NewReader([]byte(`{invalid json`)))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.HandleInstallation(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestGitHubApp_SetupAutoWebhooks_NoAppID(t *testing.T) {
	handler, client := newTestGitHubAppHandler(t)

	u, _ := client.User.Create().
		SetGithubID(3000).SetLogin("webhooktest").SetAccessToken("token").Save(context.Background())

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/repos/setup-webhooks", nil)
	c.Set("user_id", int64(u.ID))
	handler.SetupAutoWebhooks(c)

	if !strings.Contains(w.Body.String(), "GITHUB_APP_ID not set") {
		t.Errorf("expected GITHUB_APP_ID error, got: %s", w.Body.String())
	}
}

func setupUserWithRepos(t *testing.T, client *ent.Client, login string, githubID int64) *ent.User {
	t.Helper()
	u, _ := client.User.Create().
		SetGithubID(githubID).SetLogin(login).SetAccessToken("token").Save(context.Background())
	client.Repository.Create().
		SetGithubID(500).SetOwner(login).SetName("repo-a").
		SetFullName(login + "/repo-a").SetHTMLURL("https://github.com/" + login + "/repo-a").
		SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())
	client.Repository.Create().
		SetGithubID(501).SetOwner(login).SetName("repo-b").
		SetFullName(login + "/repo-b").SetHTMLURL("https://github.com/" + login + "/repo-b").
		SetDefaultBranch("main").SetUserID(u.ID).Save(context.Background())
	return u
}

func callSetupAutoWebhooks(t *testing.T, handler *GitHubAppHandler, userID int64) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/repos/setup-webhooks", nil)
	c.Set("user_id", userID)
	handler.SetupAutoWebhooks(c)
	return w
}

func TestGitHubApp_SetupAutoWebhooks_CreatesWebhooks(t *testing.T) {
	stub := newWebhookStub(t)
	t.Setenv("GITHUB_APP_ID", "123")
	handler, client := newTestGitHubAppHandler(t)
	u := setupUserWithRepos(t, client, "manualtest", 4000)

	w := callSetupAutoWebhooks(t, handler, int64(u.ID))

	if !strings.Contains(w.Body.String(), "Webhooks configured: 2, errors: 0") {
		t.Errorf("unexpected response: %s", w.Body.String())
	}
	if len(stub.posts) != 2 {
		t.Fatalf("expected 2 webhook registrations, got %d", len(stub.posts))
	}
	for _, p := range stub.posts {
		if p.Config.URL != "http://example.com/webhook/github" {
			t.Errorf("unexpected webhook URL: %s", p.Config.URL)
		}
		if len(p.Events) != 1 || p.Events[0] != "push" {
			t.Errorf("expected events [push], got %v", p.Events)
		}
	}
}

func TestGitHubApp_SetupAutoWebhooks_Idempotent(t *testing.T) {
	stub := newWebhookStub(t)
	t.Setenv("GITHUB_APP_ID", "123")
	handler, client := newTestGitHubAppHandler(t)
	u := setupUserWithRepos(t, client, "idemtest", 4001)

	// Existing hook already points at this instance's webhook URL
	stub.existing = "http://example.com/webhook/github"

	w := callSetupAutoWebhooks(t, handler, int64(u.ID))

	if !strings.Contains(w.Body.String(), "Webhooks configured: 2, errors: 0") {
		t.Errorf("unexpected response: %s", w.Body.String())
	}
	if len(stub.posts) != 0 {
		t.Fatalf("expected no duplicate webhooks, got %d POSTs", len(stub.posts))
	}
}

func TestGitHubApp_SetupAutoWebhooks_DeployEnabled(t *testing.T) {
	stub := newWebhookStub(t)
	t.Setenv("GITHUB_APP_ID", "123")
	handler, client := newTestGitHubAppHandler(t)
	handler.SetDeployEnabled(true)
	u := setupUserWithRepos(t, client, "deploytest", 4002)

	w := callSetupAutoWebhooks(t, handler, int64(u.ID))

	if !strings.Contains(w.Body.String(), "Webhooks configured: 2, errors: 0") {
		t.Errorf("unexpected response: %s", w.Body.String())
	}
	if len(stub.posts) != 2 {
		t.Fatalf("expected 2 webhook registrations, got %d", len(stub.posts))
	}
	for _, p := range stub.posts {
		if len(p.Events) != 2 || p.Events[0] != "push" || p.Events[1] != "release" {
			t.Errorf("expected events [push release], got %v", p.Events)
		}
	}
}

func TestGitHubApp_HandleInstallation_WebhookFailureContinues(t *testing.T) {
	stub := newWebhookStub(t)
	stub.failPosts = 1 // first repo's webhook POST fails
	handler, client := newTestGitHubAppHandler(t)

	u, _ := client.User.Create().
		SetGithubID(5000).SetLogin("failtest").SetAccessToken("token").Save(context.Background())
	_ = u

	body := installPayload("failtest", 5000,
		repoPayload(600, "failtest", "fail-repo"),
		repoPayload(601, "failtest", "ok-repo"),
	)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github-app", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.HandleInstallation(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 even with a webhook failure, got %d", w.Code)
	}
	count, _ := client.Repository.Query().Count(context.Background())
	if count != 2 {
		t.Fatalf("expected both repos imported despite webhook failure, got %d", count)
	}
	if len(stub.posts) != 1 {
		t.Fatalf("expected 1 successful webhook registration (other failed), got %d", len(stub.posts))
	}
}
