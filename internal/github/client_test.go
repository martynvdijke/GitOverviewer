package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"test_token_123"}`))
	}))
	defer srv.Close()

	c := NewClient("client_id", "client_secret")
	c.TokenURL = srv.URL

	token, err := c.GetAccessToken("code123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "test_token_123" {
		t.Fatalf("expected test_token_123, got %s", token)
	}
}

func TestGetAccessTokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"error":"bad_verification_code"}`))
	}))
	defer srv.Close()

	c := NewClient("client_id", "client_secret")
	c.TokenURL = srv.URL

	_, err := c.GetAccessToken("bad_code")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGetUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test_token" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":12345,"login":"octocat","avatar_url":"https://avatars.example.com/u/12345","name":"Octo Cat"}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	u, err := c.GetUser("test_token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.ID != 12345 || u.Login != "octocat" {
		t.Fatalf("unexpected user: %+v", u)
	}
}

func TestListRepositories(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		w.Header().Set("Content-Type", "application/json")
		if page == 1 {
			w.Write([]byte(`[
				{"id":1,"name":"repo1","full_name":"user/repo1","html_url":"https://github.com/user/repo1","default_branch":"main","owner":{"login":"user"}},
				{"id":2,"name":"repo2","full_name":"user/repo2","html_url":"https://github.com/user/repo2","default_branch":"main","language":"Go","owner":{"login":"user"}}
			]`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	repos, err := c.ListRepositories("test_token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}
	if repos[0].FullName != "user/repo1" {
		t.Errorf("expected user/repo1, got %s", repos[0].FullName)
	}
}

func TestGetLatestCommit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{
				"sha":"abc123def456",
				"commit":{
					"message":"Fix the thing\n\nMore details here",
					"committer":{"date":"2024-06-15T10:30:00Z"}
				}
			}
		]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	commit, err := c.GetLatestCommit("token", "user", "repo", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if commit.SHA != "abc123def456" {
		t.Errorf("expected abc123def456, got %s", commit.SHA)
	}
	if commit.Message != "Fix the thing" {
		t.Errorf("expected 'Fix the thing', got '%s'", commit.Message)
	}
	if commit.Date.IsZero() {
		t.Error("expected non-zero date")
	}
}

func TestGetLatestRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tag_name":"v1.0.0","name":"Version 1.0.0","published_at":"2024-01-15T08:00:00Z"}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	release, err := c.GetLatestRelease("token", "user", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if release.TagName != "v1.0.0" {
		t.Errorf("expected v1.0.0, got %s", release.TagName)
	}
}

func TestGetWorkflowStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"workflow_runs":[{"id":42,"status":"completed","conclusion":"success"}]}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	run, err := c.GetWorkflowStatus("token", "user", "repo", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run.ID != 42 || run.Conclusion != "success" {
		t.Errorf("unexpected run: %+v", run)
	}
}

func TestGetLatestCommit_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	_, err := c.GetLatestCommit("token", "user", "repo", "main")
	if err == nil {
		t.Fatal("expected error for empty commits list")
	}
}

func TestGetLatestRelease_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	_, err := c.GetLatestRelease("token", "user", "repo")
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestGetLatestWorkflowRun_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"workflow_runs":[{"id":99,"status":"completed","conclusion":"success"}]}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	run, err := c.GetLatestWorkflowRun("token", "user", "repo", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run.ID != 99 || run.Conclusion != "success" {
		t.Errorf("expected id=99, conclusion=success, got %+v", run)
	}
}

func TestGetLatestWorkflowRun_WithTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		branch := r.URL.Query().Get("branch")
		if branch != "v1.0.0" {
			t.Errorf("expected branch=v1.0.0, got %s", branch)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"workflow_runs":[{"id":42,"status":"completed","conclusion":"success"}]}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	run, err := c.GetLatestWorkflowRun("token", "user", "repo", "v1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run.ID != 42 {
		t.Errorf("expected id=42, got %d", run.ID)
	}
}

func TestGetLatestWorkflowRun_NoRuns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"workflow_runs":[]}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	_, err := c.GetLatestWorkflowRun("token", "user", "repo", "main")
	if err == nil {
		t.Fatal("expected error for empty workflow runs")
	}
}

func TestGetLatestWorkflowRun_EmptyBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		branch := r.URL.Query().Get("branch")
		if branch != "" {
			t.Errorf("expected empty branch, got %s", branch)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"workflow_runs":[{"id":7,"status":"completed","conclusion":"failure"}]}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	run, err := c.GetLatestWorkflowRun("token", "user", "repo", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run.Conclusion != "failure" {
		t.Errorf("expected failure, got %s", run.Conclusion)
	}
}

func TestGetWorkflowStatus_NoRuns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"workflow_runs":[]}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	_, err := c.GetWorkflowStatus("token", "user", "repo", "main")
	if err == nil {
		t.Fatal("expected error for empty workflow runs")
	}
}

func TestTruncateNewlineInCommitMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{
				"sha":"abc",
				"commit":{
					"message":"feat: add stuff\n\nCo-authored-by: someone",
					"committer":{"date":"2024-01-01T00:00:00Z"}
				}
			}
		]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	commit, err := c.GetLatestCommit("token", "user", "repo", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if commit.Message != "feat: add stuff" {
		t.Errorf("expected 'feat: add stuff', got '%s'", commit.Message)
	}
}

func TestParseCommitType_Feat(t *testing.T) {
	if tp := ParseCommitType("feat: add login"); tp != "feat" {
		t.Errorf("expected feat, got %s", tp)
	}
}

func TestParseCommitType_FeatScope(t *testing.T) {
	if tp := ParseCommitType("feat(auth): add login"); tp != "feat" {
		t.Errorf("expected feat, got %s", tp)
	}
}

func TestParseCommitType_Feature(t *testing.T) {
	if tp := ParseCommitType("feature: big thing"); tp != "feat" {
		t.Errorf("expected feat, got %s", tp)
	}
}

func TestParseCommitType_Fix(t *testing.T) {
	if tp := ParseCommitType("fix: crash on load"); tp != "fix" {
		t.Errorf("expected fix, got %s", tp)
	}
}

func TestParseCommitType_FixScope(t *testing.T) {
	if tp := ParseCommitType("fix(parser): handle null"); tp != "fix" {
		t.Errorf("expected fix, got %s", tp)
	}
}

func TestParseCommitType_Bug(t *testing.T) {
	if tp := ParseCommitType("bug: wrong color"); tp != "fix" {
		t.Errorf("expected fix, got %s", tp)
	}
}

func TestParseCommitType_Docs(t *testing.T) {
	if tp := ParseCommitType("docs: update readme"); tp != "docs" {
		t.Errorf("expected docs, got %s", tp)
	}
}

func TestParseCommitType_Chore(t *testing.T) {
	if tp := ParseCommitType("chore: bump deps"); tp != "chore" {
		t.Errorf("expected chore, got %s", tp)
	}
}

func TestParseCommitType_Refactor(t *testing.T) {
	if tp := ParseCommitType("refactor: clean up"); tp != "chore" {
		t.Errorf("expected chore, got %s", tp)
	}
}

func TestParseCommitType_NoPrefix(t *testing.T) {
	if tp := ParseCommitType("fix stuff"); tp != "other" {
		t.Errorf("expected other, got %s", tp)
	}
}

func TestParseCommitType_Empty(t *testing.T) {
	if tp := ParseCommitType(""); tp != "other" {
		t.Errorf("expected other, got %s", tp)
	}
}

func TestParseCommitType_UpperCase(t *testing.T) {
	if tp := ParseCommitType("FEAT: add"); tp != "feat" {
		t.Errorf("expected feat, got %s", tp)
	}
}

func TestGetCommitsSince_NoSince(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", "9999999999")
		w.Write([]byte(`[
			{"sha":"a","commit":{"message":"feat: one","committer":{"date":"2024-01-01T00:00:00Z"}}},
			{"sha":"b","commit":{"message":"fix: two","committer":{"date":"2024-01-02T00:00:00Z"}}}
		]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	commits, err := c.GetCommitsSince("token", "user", "repo", "main", time.Time{}, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}
	if commits[0].SHA != "a" || commits[1].SHA != "b" {
		t.Errorf("unexpected commits order: %+v", commits)
	}
}

func TestGetCommitsSince_WithSince(t *testing.T) {
	since := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sinceParam := r.URL.Query().Get("since")
		if sinceParam != "2024-06-01T00:00:00Z" {
			t.Errorf("expected since=2024-06-01T00:00:00Z, got %s", sinceParam)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", "9999999999")
		w.Write([]byte(`[
			{"sha":"c","commit":{"message":"feat: after since","committer":{"date":"2024-06-02T00:00:00Z"}}}
		]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	commits, err := c.GetCommitsSince("token", "user", "repo", "main", since, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(commits))
	}
	if commits[0].SHA != "c" {
		t.Errorf("expected SHA 'c', got %s", commits[0].SHA)
	}
}

func TestGetCommitsSince_Pagination(t *testing.T) {
	pageNum := 0
	perPage := 100
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageNum++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", "9999999999")
		if pageNum <= 3 {
			// Return full page (100 commits) for first 2 pages, partial for third
			count := perPage
			if pageNum == 3 {
				count = 3 // third page with fewer commits
			}
			var commits []string
			for i := 0; i < count; i++ {
				commits = append(commits, `{"sha":"p`+fmt.Sprintf("%d", pageNum)+`c`+fmt.Sprintf("%d", i)+`","commit":{"message":"c`+fmt.Sprintf("%d", pageNum)+`.`+fmt.Sprintf("%d", i)+`","committer":{"date":"2024-06-01T00:00:00Z"}}}`)
			}
			w.Write([]byte(`[` + strings.Join(commits, ",") + `]`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	commits, err := c.GetCommitsSince("token", "user", "repo", "main", time.Time{}, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := 203 // 100 + 100 + 3
	if len(commits) != expected {
		t.Fatalf("expected %d commits (2 full pages + 1 partial), got %d", expected, len(commits))
	}
}

func TestGetCommitsSince_RateLimitHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "42")
		w.Header().Set("X-RateLimit-Reset", "9999999999")
		w.Write([]byte(`[{"sha":"x","commit":{"message":"feat: tracked","committer":{"date":"2024-01-01T00:00:00Z"}}}]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	_, err := c.GetCommitsSince("token", "user", "repo", "main", time.Time{}, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.RateLimitRemaining != 42 {
		t.Errorf("expected RateLimitRemaining=42, got %d", c.RateLimitRemaining)
	}
	if c.RateLimitReset != 9999999999 {
		t.Errorf("expected RateLimitReset=9999999999, got %d", c.RateLimitReset)
	}
}

func TestGetCommitsSince_MaxLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", "9999999999")
		// Return 100 commits each page (full page)
		var commits []string
		for i := range 100 {
			commits = append(commits, `{"sha":"a`+fmt.Sprintf("%d", i)+`","commit":{"message":"commit `+fmt.Sprintf("%d", i)+`","committer":{"date":"2024-01-01T00:00:00Z"}}}`)
		}
		w.Write([]byte(`[` + strings.Join(commits, ",") + `]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	// Max should be capped at 500
	commits, err := c.GetCommitsSince("token", "user", "repo", "main", time.Time{}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 500 {
		t.Errorf("expected 500 commits (max limit), got %d", len(commits))
	}
}

func TestGetCommits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"sha":"a","commit":{"message":"feat: one","committer":{"date":"2024-01-01T00:00:00Z"}}},
			{"sha":"b","commit":{"message":"fix: two","committer":{"date":"2024-01-02T00:00:00Z"}}},
			{"sha":"c","commit":{"message":"chore: three","committer":{"date":"2024-01-03T00:00:00Z"}}}
		]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	commits, err := c.GetCommits("token", "user", "repo", "main", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(commits))
	}
	if commits[0].Message != "feat: one" {
		t.Errorf("expected 'feat: one', got '%s'", commits[0].Message)
	}
}

func TestListReleases(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		w.Header().Set("Content-Type", "application/json")
		if page == 1 {
			w.Write([]byte(`[
				{"tag_name":"v2","name":"Two","published_at":"2024-06-01T00:00:00Z"},
				{"tag_name":"v1","name":"One","published_at":"2024-01-01T00:00:00Z"}
			]`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	releases, err := c.ListReleases("token", "user", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(releases))
	}
	if releases[0].TagName != "v2" {
		t.Errorf("expected v2 first, got %s", releases[0].TagName)
	}
}

func TestGetWorkflowRuns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"workflow_runs":[
			{"id":1,"status":"completed","conclusion":"success"},
			{"id":2,"status":"completed","conclusion":"failure"}
		]}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	runs, err := c.GetWorkflowRuns("token", "user", "repo", "main", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].Conclusion != "success" || runs[1].Conclusion != "failure" {
		t.Errorf("unexpected conclusions: %+v", runs)
	}
}

func TestListReleases_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	releases, err := c.ListReleases("token", "user", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(releases) != 0 {
		t.Fatalf("expected 0 releases, got %d", len(releases))
	}
}

func TestListPullRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{
				"number": 1,
				"title": "Add feature",
				"user": {"login": "dev1"},
				"created_at": "2024-06-01T10:00:00Z",
				"html_url": "https://github.com/user/repo/pull/1",
				"head": {"ref": "feature-branch"},
				"base": {"ref": "main"}
			},
			{
				"number": 2,
				"title": "Fix bug",
				"user": {"login": "dev2"},
				"created_at": "2024-06-02T10:00:00Z",
				"html_url": "https://github.com/user/repo/pull/2",
				"head": {"ref": "bugfix"},
				"base": {"ref": "main"}
			}
		]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	prs, err := c.ListPullRequests("token", "user", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs, got %d", len(prs))
	}
	if prs[0].Number != 1 || prs[0].Title != "Add feature" {
		t.Errorf("unexpected first PR: %+v", prs[0])
	}
	if prs[0].Author != "dev1" {
		t.Errorf("expected author dev1, got %s", prs[0].Author)
	}
	if prs[0].HeadRef != "feature-branch" {
		t.Errorf("expected head ref feature-branch, got %s", prs[0].HeadRef)
	}
	if prs[0].BaseRef != "main" {
		t.Errorf("expected base ref main, got %s", prs[0].BaseRef)
	}
}

func TestListPullRequests_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	prs, err := c.ListPullRequests("token", "user", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prs) != 0 {
		t.Fatalf("expected 0 PRs, got %d", len(prs))
	}
}

func TestMergePullRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"merged": true, "message": "Pull Request successfully merged"}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	merged, msg, err := c.MergePullRequest("token", "user", "repo", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !merged {
		t.Error("expected merged to be true")
	}
	if msg == "" {
		t.Error("expected non-empty message")
	}
}

func TestMergePullRequest_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte(`{"message": "Merge conflict"}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	_, _, err := c.MergePullRequest("token", "user", "repo", 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestClosePullRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		if r.URL.Path != "/repos/user/repo/pulls/7" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"state":"closed"`) {
			t.Errorf("expected state closed in body, got: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state": "closed"}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	if err := c.ClosePullRequest("token", "user", "repo", 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClosePullRequest_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message": "Not Found"}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	err := c.ClosePullRequest("token", "user", "repo", 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRerunFailedJobs_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/repos/user/repo/actions/runs/42/rerun-failed-jobs" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	if err := c.RerunFailedJobs("token", "user", "repo", 42); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRerunFailedJobs_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"message": "Cannot re-run a workflow run that is not completed"}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	err := c.RerunFailedJobs("token", "user", "repo", 42)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestIsRerunnableConclusion(t *testing.T) {
	rerunnable := []string{"failure", "cancelled", "timed_out", "action_required", "stale"}
	for _, c := range rerunnable {
		if !IsRerunnableConclusion(c) {
			t.Errorf("expected %q to be rerunnable", c)
		}
	}
	notRerunnable := []string{"success", "neutral", "skipped", "in_progress", "", "unknown"}
	for _, c := range notRerunnable {
		if IsRerunnableConclusion(c) {
			t.Errorf("expected %q to NOT be rerunnable", c)
		}
	}
}

func TestGetWorkflowRunsForRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("branch") != "" {
			t.Errorf("expected no branch filter, got %q", r.URL.Query().Get("branch"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"workflow_runs":[
			{"id":1,"status":"completed","conclusion":"success","head_sha":"abc123"},
			{"id":2,"status":"in_progress","conclusion":null,"head_sha":"def456"}
		]}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	runs, err := c.GetWorkflowRunsForRepo("token", "user", "repo", 30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].HeadSHA != "abc123" || runs[0].Status != "completed" {
		t.Errorf("unexpected first run: %+v", runs[0])
	}
	if runs[1].HeadSHA != "def456" || runs[1].Status != "in_progress" {
		t.Errorf("unexpected second run: %+v", runs[1])
	}
}

func TestGetWorkflowRunsForRepo_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"workflow_runs":[]}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	runs, err := c.GetWorkflowRunsForRepo("token", "user", "repo", 30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs, got %d", len(runs))
	}
}

func TestListPullRequests_IncludesHeadSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{
				"number": 1,
				"title": "Add feature",
				"user": {"login": "dev1"},
				"created_at": "2024-06-01T10:00:00Z",
				"html_url": "https://github.com/user/repo/pull/1",
				"head": {"ref": "feature-branch", "sha": "abc123"},
				"base": {"ref": "main"},
				"mergeable_state": "clean"
			}
		]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	prs, err := c.ListPullRequests("token", "user", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	if prs[0].HeadSHA != "abc123" {
		t.Errorf("expected head sha abc123, got %q", prs[0].HeadSHA)
	}
	if prs[0].MergeableState != "clean" {
		t.Errorf("expected mergeable state clean, got %q", prs[0].MergeableState)
	}
}

func TestParseCommitType_CI(t *testing.T) {
	if tp := ParseCommitType("ci: update pipeline"); tp != "chore" {
		t.Errorf("expected chore, got %s", tp)
	}
}

func TestParseCommitType_Perf(t *testing.T) {
	if tp := ParseCommitType("perf: optimize query"); tp != "chore" {
		t.Errorf("expected chore, got %s", tp)
	}
}

func TestParseCommitType_Style(t *testing.T) {
	if tp := ParseCommitType("style: format code"); tp != "chore" {
		t.Errorf("expected chore, got %s", tp)
	}
}

func TestParseCommitType_Revert(t *testing.T) {
	if tp := ParseCommitType("revert: undo change"); tp != "chore" {
		t.Errorf("expected chore, got %s", tp)
	}
}

func TestParseCommitType_Build(t *testing.T) {
	if tp := ParseCommitType("build: update deps"); tp != "chore" {
		t.Errorf("expected chore, got %s", tp)
	}
}

func TestParseCommitType_Test(t *testing.T) {
	if tp := ParseCommitType("test: add unit tests"); tp != "chore" {
		t.Errorf("expected chore, got %s", tp)
	}
}

func TestParseCommitType_Bugfix(t *testing.T) {
	if tp := ParseCommitType("bugfix: resolve crash"); tp != "fix" {
		t.Errorf("expected fix, got %s", tp)
	}
}

func TestParseCommitType_Documentation(t *testing.T) {
	if tp := ParseCommitType("documentation: update API docs"); tp != "docs" {
		t.Errorf("expected docs, got %s", tp)
	}
}

func TestParseCommitType_Whitespace(t *testing.T) {
	if tp := ParseCommitType("  feat: add thing  "); tp != "feat" {
		t.Errorf("expected feat, got %s", tp)
	}
}

func TestParseCommitType_NoColon(t *testing.T) {
	if tp := ParseCommitType("just a message"); tp != "other" {
		t.Errorf("expected other, got %s", tp)
	}
}

func TestFindDependencyDashboard_Found(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		w.Header().Set("Content-Type", "application/json")
		if page == 1 {
			w.Write([]byte(`[
				{"number":1,"title":"Bug report","body":"Something is broken","state":"open"},
				{"number":2,"title":"Dependency Dashboard","body":"## Pending\n- [ ] Update deps","state":"open"},
				{"number":3,"title":"Feature request","body":"Add new feature","state":"open"}
			]`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	num, body, err := c.FindDependencyDashboard("token", "user", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if num != 2 {
		t.Errorf("expected issue number 2, got %d", num)
	}
	if !strings.Contains(body, "Update deps") {
		t.Errorf("expected body to contain 'Update deps', got %s", body)
	}
}

func TestFindDependencyDashboard_NotFound(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		w.Header().Set("Content-Type", "application/json")
		if page == 1 {
			w.Write([]byte(`[
				{"number":1,"title":"Bug report","body":"Something is broken","state":"open"},
				{"number":2,"title":"Feature request","body":"Add new feature","state":"open"}
			]`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	_, _, err := c.FindDependencyDashboard("token", "user", "repo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no Dependency Dashboard issue found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestFindDependencyDashboard_NoIssues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	_, _, err := c.FindDependencyDashboard("token", "user", "repo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no Dependency Dashboard issue found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestUpdateIssueBody_Success(t *testing.T) {
	var receivedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		if r.URL.Path != "/repos/user/repo/issues/42" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var payload struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		receivedBody = payload.Body
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"number":42}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	err := c.UpdateIssueBody("token", "user", "repo", 42, "Updated body content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedBody != "Updated body content" {
		t.Errorf("expected 'Updated body content', got %s", receivedBody)
	}
}

func TestUpdateIssueBody_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	err := c.UpdateIssueBody("token", "user", "repo", 999, "body")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRebaseAllOpenPRs_Success(t *testing.T) {
	var updatedBody string
	issuePage := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/issues") {
			issuePage++
			w.Header().Set("Content-Type", "application/json")
			if issuePage == 1 {
				w.Write([]byte(`[
					{"number":1,"title":"Bug report","body":"Something is broken","state":"open"},
					{"number":2,"title":"Dependency Dashboard","body":"## Pending\n- [ ] Rebase all open PRs <!-- rebase-all-open-prs-checkbox -->\n- [ ] Other task","state":"open"}
				]`))
			} else {
				w.Write([]byte(`[]`))
			}
		} else if r.Method == "PATCH" {
			var payload struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("failed to decode request body: %v", err)
			}
			updatedBody = payload.Body
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"number":2}`))
		}
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	err := c.RebaseAllOpenPRs("token", "user", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(updatedBody, "- [x] Rebase all open PRs") {
		t.Errorf("expected checkbox to be checked, got: %s", updatedBody)
	}
	if strings.Contains(updatedBody, "- [ ] Rebase all open PRs") {
		t.Errorf("expected unchecked checkbox to be replaced, got: %s", updatedBody)
	}
}

func TestRebaseAllOpenPRs_AlreadyChecked(t *testing.T) {
	updateCalled := false
	issuePage := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/issues") {
			issuePage++
			w.Header().Set("Content-Type", "application/json")
			if issuePage == 1 {
				w.Write([]byte(`[
					{"number":2,"title":"Dependency Dashboard","body":"## Pending\n- [x] Rebase all open PRs <!-- rebase-all-open-prs-checkbox -->","state":"open"}
				]`))
			} else {
				w.Write([]byte(`[]`))
			}
		} else if r.Method == "PATCH" {
			updateCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"number":2}`))
		}
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	err := c.RebaseAllOpenPRs("token", "user", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updateCalled {
		t.Error("expected UpdateIssueBody not to be called when checkbox already checked")
	}
}

func TestRebaseAllOpenPRs_MarkerNotFound(t *testing.T) {
	issuePage := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/issues") {
			issuePage++
			w.Header().Set("Content-Type", "application/json")
			if issuePage == 1 {
				w.Write([]byte(`[
					{"number":2,"title":"Dependency Dashboard","body":"## Pending\n- [ ] Some other task","state":"open"}
				]`))
			} else {
				w.Write([]byte(`[]`))
			}
		}
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	err := c.RebaseAllOpenPRs("token", "user", "repo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "rebase-all checkbox marker not found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestRebaseAllOpenPRs_NoDashboard(t *testing.T) {
	issuePage := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/issues") {
			issuePage++
			w.Header().Set("Content-Type", "application/json")
			if issuePage == 1 {
				w.Write([]byte(`[
					{"number":1,"title":"Bug report","body":"Something is broken","state":"open"}
				]`))
			} else {
				w.Write([]byte(`[]`))
			}
		}
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	err := c.RebaseAllOpenPRs("token", "user", "repo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no Dependency Dashboard issue found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestListCommitsPage_Pagination(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"sha":"a","commit":{"message":"m","committer":{"date":"2025-01-02T00:00:00Z"}}}]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	commits, hasMore, err := c.ListCommitsPage(context.Background(), "token", "user", "repo", "main", 3, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(commits))
	}
	if !hasMore {
		t.Error("expected hasMore=true when page is full")
	}
	if gotQuery == "" || !strings.Contains(gotQuery, "page=3") || !strings.Contains(gotQuery, "per_page=1") || !strings.Contains(gotQuery, "sha=main") {
		t.Errorf("unexpected query: %s", gotQuery)
	}
}

func TestListCommitsPage_HasMoreFalseOnShortPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	commits, hasMore, err := c.ListCommitsPage(context.Background(), "token", "user", "repo", "main", 1, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 0 {
		t.Fatalf("expected 0 commits, got %d", len(commits))
	}
	if hasMore {
		t.Error("expected hasMore=false on short page")
	}
}

func TestListCommitsPage_RateLimitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1893456000")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.APIURL = srv.URL

	_, _, err := c.ListCommitsPage(context.Background(), "token", "user", "repo", "main", 1, 100)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rlErr *RateLimitError
	if !errors.As(err, &rlErr) {
		t.Fatalf("expected *RateLimitError, got %T: %v", err, err)
	}
}
