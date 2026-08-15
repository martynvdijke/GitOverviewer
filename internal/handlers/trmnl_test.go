package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlens/ent/enttest"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

func newTRMNLTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/trmnl/summary", nil)
	return c, w
}

func TestTRMNLSummary_EmptyDatabase(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
	handler := NewTRMNLSummaryHandler(client)

	c, w := newTRMNLTestContext()
	handler.Summary(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected application/json Content-Type, got %s", ct)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}

	for key, want := range map[string]string{
		"total_repos":        "0",
		"total_releases":     "0",
		"failing_repos":      "0",
		"workflow_pass_rate": "0",
	} {
		if got := strings.TrimSpace(string(body[key])); got != want {
			t.Errorf("expected %s == %s, got %s", key, want, got)
		}
	}
	if string(body["generated_at"]) == "" {
		t.Error("expected generated_at to be set")
	}
	if string(body["last_sync"]) != "null" {
		t.Errorf("expected last_sync null on empty db, got %s", body["last_sync"])
	}
	if string(body["latest_releases"]) != "[]" {
		t.Errorf("expected empty latest_releases, got %s", body["latest_releases"])
	}
	if string(body["failing_repo_list"]) != "[]" {
		t.Errorf("expected empty failing_repo_list, got %s", body["failing_repo_list"])
	}
}

func TestTRMNLSummary_SeededData(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
	handler := NewTRMNLSummaryHandler(client)

	u, _ := client.User.Create().
		SetGithubID(700).SetLogin("trmnluser").SetAccessToken("tok").Save(context.Background())

	older := time.Now().UTC().Add(-48 * time.Hour)
	newer := time.Now().UTC().Add(-1 * time.Hour)

	// Older release first — the payload must order newest first.
	client.Repository.Create().
		SetGithubID(100).SetOwner("org-a").SetName("repo-a").
		SetFullName("org-a/repo-a").SetHTMLURL("https://github.com/org-a/repo-a").
		SetDefaultBranch("main").SetUserID(u.ID).
		SetLatestReleaseTag("v1.0.0").SetLatestReleaseName("v1.0.0").
		SetLatestReleaseDate(older).
		SetReleaseCount(2).
		SetWorkflowStatus("success").
		SetWorkflowSuccessCount(5).SetWorkflowFailureCount(0).
		SetSyncedAt(older).
		Save(context.Background())

	// Newer release + failing workflow.
	client.Repository.Create().
		SetGithubID(101).SetOwner("org-b").SetName("repo-b").
		SetFullName("org-b/repo-b").SetHTMLURL("https://github.com/org-b/repo-b").
		SetDefaultBranch("main").SetUserID(u.ID).
		SetLatestReleaseTag("v2.3.1").SetLatestReleaseName("v2.3.1").
		SetLatestReleaseDate(newer).
		SetReleaseCount(1).
		SetWorkflowStatus("failure").
		SetWorkflowSuccessCount(2).SetWorkflowFailureCount(3).
		SetSyncedAt(newer).
		Save(context.Background())

	c, w := newTRMNLTestContext()
	handler.Summary(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body struct {
		TotalRepos       int     `json:"total_repos"`
		TotalReleases    int     `json:"total_releases"`
		FailingRepos     int     `json:"failing_repos"`
		WorkflowPassRate float64 `json:"workflow_pass_rate"`
		GeneratedAt      string  `json:"generated_at"`
		LastSync         string  `json:"last_sync"`
		LatestReleases   []struct {
			FullName string `json:"full_name"`
			Tag      string `json:"tag"`
			Name     string `json:"name"`
			Date     string `json:"date"`
			HTMLURL  string `json:"html_url"`
		} `json:"latest_releases"`
		FailingRepoList []struct {
			FullName       string `json:"full_name"`
			WorkflowStatus string `json:"workflow_status"`
		} `json:"failing_repo_list"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}

	if body.TotalRepos != 2 {
		t.Errorf("expected total_repos 2, got %d", body.TotalRepos)
	}
	if body.TotalReleases != 3 {
		t.Errorf("expected total_releases 3, got %d", body.TotalReleases)
	}
	if body.FailingRepos != 1 {
		t.Errorf("expected failing_repos 1, got %d", body.FailingRepos)
	}
	// 5 successes (repo-a) + 2 successes (repo-b) of 10 total workflows → 70.0
	if body.WorkflowPassRate != 70.0 {
		t.Errorf("expected workflow_pass_rate 70.0, got %v", body.WorkflowPassRate)
	}
	if body.GeneratedAt == "" {
		t.Error("expected generated_at to be set")
	}
	if body.LastSync == "" {
		t.Error("expected last_sync to be set from newest SyncedAt")
	}

	if len(body.LatestReleases) != 2 {
		t.Fatalf("expected 2 latest releases, got %d", len(body.LatestReleases))
	}
	if body.LatestReleases[0].FullName != "org-b/repo-b" {
		t.Errorf("expected newest release first, got %s", body.LatestReleases[0].FullName)
	}
	if body.LatestReleases[0].Tag != "v2.3.1" || body.LatestReleases[1].Tag != "v1.0.0" {
		t.Errorf("unexpected release order: %s, %s", body.LatestReleases[0].Tag, body.LatestReleases[1].Tag)
	}
	if body.LatestReleases[1].Date == "" {
		t.Error("expected release date to be formatted")
	}

	if len(body.FailingRepoList) != 1 {
		t.Fatalf("expected 1 failing repo, got %d", len(body.FailingRepoList))
	}
	if body.FailingRepoList[0].FullName != "org-b/repo-b" {
		t.Errorf("expected org-b/repo-b in failing list, got %s", body.FailingRepoList[0].FullName)
	}
	if body.FailingRepoList[0].WorkflowStatus != "failure" {
		t.Errorf("expected workflow_status failure, got %s", body.FailingRepoList[0].WorkflowStatus)
	}
}

func TestTRMNLSummary_ListsCapped(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
	handler := NewTRMNLSummaryHandler(client)

	u, _ := client.User.Create().
		SetGithubID(900).SetLogin("capuser").SetAccessToken("tok").Save(context.Background())

	base := time.Now().UTC().Add(-time.Duration(1) * time.Hour)
	for i := 0; i < 10; i++ {
		client.Repository.Create().
			SetGithubID(int64(1000 + i)).SetOwner("cap-org").SetName(fmt.Sprintf("repo-%d", i)).
			SetFullName(fmt.Sprintf("cap-org/repo-%d", i)).
			SetHTMLURL(fmt.Sprintf("https://github.com/cap-org/repo-%d", i)).
			SetDefaultBranch("main").SetUserID(u.ID).
			SetLatestReleaseTag(fmt.Sprintf("v1.%d.0", i)).
			SetLatestReleaseDate(base.Add(time.Duration(i) * time.Minute)).
			SetReleaseCount(1).
			SetWorkflowStatus("failure").
			Save(context.Background())
	}

	c, w := newTRMNLTestContext()
	handler.Summary(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body struct {
		LatestReleases  []json.RawMessage `json:"latest_releases"`
		FailingRepoList []json.RawMessage `json:"failing_repo_list"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if len(body.LatestReleases) != maxTRMNLLists {
		t.Errorf("expected latest_releases capped at %d, got %d", maxTRMNLLists, len(body.LatestReleases))
	}
	if len(body.FailingRepoList) != maxTRMNLLists {
		t.Errorf("expected failing_repo_list capped at %d, got %d", maxTRMNLLists, len(body.FailingRepoList))
	}
}

func TestTRMNLSummary_NoPII(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
	handler := NewTRMNLSummaryHandler(client)

	// User carries sensitive data (access token, name) that must never leak.
	client.User.Create().
		SetGithubID(800).SetLogin("secret-user").SetAccessToken("super-secret-token").
		SetName("Jane Doe").
		Save(context.Background())

	c, w := newTRMNLTestContext()
	handler.Summary(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	raw := w.Body.String()
	for _, leaked := range []string{"super-secret-token", "Jane Doe", "secret-user"} {
		if strings.Contains(raw, leaked) {
			t.Errorf("response leaked sensitive data: %q", leaked)
		}
	}
}
