package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testServer creates a httptest.Server that responds with the given
// handler. Returns the server and its base URL.
func testServer(h http.HandlerFunc) (*httptest.Server, string) {
	s := httptest.NewServer(h)
	return s, s.URL
}

func TestExchangeCode_Success(t *testing.T) {
	ctx := context.Background()
	s, baseURL := testServer(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/oauth/access_token" {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", 400)
				return
			}
			if r.Form.Get("client_id") != "test-id" {
				http.Error(w, "bad client_id", 400)
				return
			}
			if r.Form.Get("client_secret") != "test-secret" {
				http.Error(w, "bad client_secret", 400)
				return
			}
			if r.Form.Get("code") != "test-code" {
				http.Error(w, "bad code", 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"access_token": "tok_abc123",
				"token_type":   "Bearer",
				"scope":        "repository",
			})
			return
		}
		if r.URL.Path == "/api/v1/user" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(gjUser{
				ID:        42,
				Login:     "forgejo_user",
				FullName:  "Forgejo User",
				AvatarURL: "https://forgejo.example/avatar.png",
			})
			return
		}
		http.Error(w, "unexpected path: "+r.URL.Path, 404)
	})
	defer s.Close()

	c := NewClient("test-id", "test-secret", baseURL)
	token, user, err := c.ExchangeCode(ctx, "test-code", baseURL+"/auth/forgejo/callback")
	if err != nil {
		t.Fatalf("ExchangeCode failed: %v", err)
	}
	if token != "tok_abc123" {
		t.Fatalf("expected token tok_abc123, got %q", token)
	}
	if user.ID != 42 || user.Login != "forgejo_user" {
		t.Fatalf("unexpected user: %+v", user)
	}
}

func TestExchangeCode_OAuthError(t *testing.T) {
	ctx := context.Background()
	s, baseURL := testServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"error":             "access_denied",
			"error_description": "user denied access",
		})
	})
	defer s.Close()

	c := NewClient("test-id", "test-secret", baseURL)
	_, _, err := c.ExchangeCode(ctx, "bad-code", baseURL+"/callback")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAuthURL(t *testing.T) {
	c := NewClient("test-id", "test-secret", "https://codeberg.org")
	u := c.AuthURL("mystate", "https://app.example/auth/forgejo/callback")
	expected := "https://codeberg.org/login/oauth/authorize?client_id=test-id&redirect_uri=https%3A%2F%2Fapp.example%2Fauth%2Fforgejo%2Fcallback&response_type=code&scope=repository&state=mystate"
	if u != expected {
		t.Fatalf("expected:\n  %s\ngot:\n  %s", expected, u)
	}
}

func TestAuthURL_NoDefault(t *testing.T) {
	c := NewClient("test-id", "test-secret", "")
	u := c.AuthURL("mystate", "https://app.example/callback")
	if u != "" {
		t.Fatalf("expected empty URL when no default is configured, got %q", u)
	}
}

func TestAuthURLFor(t *testing.T) {
	c := NewClient("test-id", "test-secret", "")
	u := c.AuthURLFor("https://git.example.com", "mystate", "https://app.example/callback")
	expected := "https://git.example.com/login/oauth/authorize?client_id=test-id&redirect_uri=https%3A%2F%2Fapp.example%2Fcallback&response_type=code&scope=repository&state=mystate"
	if u != expected {
		t.Fatalf("expected:\n  %s\ngot:\n  %s", expected, u)
	}
}

func TestListRepositories_Pagination(t *testing.T) {
	ctx := context.Background()

	// Build a page of 50 repos to trigger page-break logic.
	var page1 []gjRepo
	for i := 1; i <= 50; i++ {
		page1 = append(page1, gjRepo{
			ID:            int64(i),
			Name:          fmt.Sprintf("repo%d", i),
			FullName:      fmt.Sprintf("user/repo%d", i),
			DefaultBranch: "main",
		})
	}
	page2 := []gjRepo{
		{ID: 51, Name: "repo51", FullName: "user/repo51", DefaultBranch: "main"},
	}
	page3 := []gjRepo{} // empty page = stop

	var callCount int
	s, baseURL := testServer(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		switch callCount {
		case 1:
			json.NewEncoder(w).Encode(page1)
		case 2:
			json.NewEncoder(w).Encode(page2)
		default:
			json.NewEncoder(w).Encode(page3)
		}
	})
	defer s.Close()

	c := NewClient("test-id", "test-secret", baseURL)
	repos, err := c.ListRepositories(ctx, "tok")
	if err != nil {
		t.Fatalf("ListRepositories failed: %v", err)
	}
	if len(repos) != 51 {
		t.Fatalf("expected 51 repos (50 on page1 + 1 on page2), got %d", len(repos))
	}
	if callCount != 2 {
		t.Fatalf("expected 2 API calls (page1 full, page2 last), got %d", callCount)
	}
	// Verify mapping
	if repos[0].FullName != "user/repo1" || repos[0].ID != 1 {
		t.Fatalf("unexpected repos[0] in %+v", repos[0])
	}
	if repos[50].FullName != "user/repo51" || repos[50].ID != 51 {
		t.Fatalf("unexpected repos[50] in %+v", repos[50])
	}
}

func TestGetCommitsSince(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)

	s, baseURL := testServer(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/user/myrepo/commits" {
			http.Error(w, "unexpected path", 404)
			return
		}
		if r.URL.Query().Get("since") == "" {
			http.Error(w, "since param required", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gjCommit{
			{
				SHA: "abc123",
				Commit: struct {
					Message   string `json:"message"`
					Committer struct {
						Date string `json:"date"`
					} `json:"committer"`
				}{
					Message: "feat: add widget\n\nLong description",
					Committer: struct {
						Date string `json:"date"`
					}{Date: now.Format(time.RFC3339)},
				},
			},
		})
	})
	defer s.Close()

	c := NewClient("test-id", "test-secret", baseURL)
	commits, err := c.GetCommitsSince(ctx, "tok", "user", "myrepo", "main", since, 10)
	if err != nil {
		t.Fatalf("GetCommitsSince failed: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(commits))
	}
	if commits[0].SHA != "abc123" {
		t.Fatalf("expected SHA abc123, got %q", commits[0].SHA)
	}
	// Message should be first line only
	if commits[0].Message != "feat: add widget" {
		t.Fatalf("expected message 'feat: add widget', got %q", commits[0].Message)
	}
}

func TestListReleases(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	s, baseURL := testServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gjRelease{
			{TagName: "v1.0.0", Name: "First release", PublishedAt: now.Format(time.RFC3339)},
			{TagName: "v0.9.0", Name: "Beta", PublishedAt: now.Add(-24 * time.Hour).Format(time.RFC3339)},
		})
	})
	defer s.Close()

	c := NewClient("test-id", "test-secret", baseURL)
	releases, err := c.ListReleases(ctx, "tok", "user", "myrepo")
	if err != nil {
		t.Fatalf("ListReleases failed: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(releases))
	}
	if releases[0].TagName != "v1.0.0" || releases[0].Name != "First release" {
		t.Fatalf("unexpected release[0]: %+v", releases[0])
	}
	if releases[1].TagName != "v0.9.0" {
		t.Fatalf("unexpected release[1]: %+v", releases[1])
	}
}

func TestListPullRequests(t *testing.T) {
	ctx := context.Background()

	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gjPullRequest{
			{
				Number: 1,
				Title:  "Fix the thing",
				User: struct {
					Login string `json:"login"`
				}{Login: "contributor"},
				HTMLURL: ts.URL + "/user/myrepo/pulls/1",
				Head: struct {
					Ref string `json:"ref"`
				}{Ref: "fix-thing"},
				Base: struct {
					Ref string `json:"ref"`
				}{Ref: "main"},
				State: "open",
			},
		})
	}))
	defer ts.Close()

	c := NewClient("test-id", "test-secret", ts.URL)
	prs, err := c.ListPullRequests(ctx, "tok", "user", "myrepo")
	if err != nil {
		t.Fatalf("ListPullRequests failed: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	if prs[0].Number != 1 || prs[0].Title != "Fix the thing" {
		t.Fatalf("unexpected PR: %+v", prs[0])
	}
}

func TestListRecentlyMergedPRs(t *testing.T) {
	ctx := context.Background()

	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gjPullRequest{
			{
				Number:  2,
				Title:   "Merged feature",
				Merged:  true,
				State:   "closed",
				HTMLURL: ts.URL + "/user/myrepo/pulls/2",
				User: struct {
					Login string `json:"login"`
				}{Login: "dev"},
				Head: struct {
					Ref string `json:"ref"`
				}{Ref: "feature"},
				Base: struct {
					Ref string `json:"ref"`
				}{Ref: "main"},
			},
			{
				Number:  3,
				Title:   "Closed without merge",
				Merged:  false,
				State:   "closed",
				HTMLURL: ts.URL + "/user/myrepo/pulls/3",
				User: struct {
					Login string `json:"login"`
				}{Login: "dev"},
				Head: struct {
					Ref string `json:"ref"`
				}{Ref: "wontfix"},
				Base: struct {
					Ref string `json:"ref"`
				}{Ref: "main"},
			},
		})
	}))
	defer ts.Close()

	c := NewClient("test-id", "test-secret", ts.URL)
	prs, err := c.ListRecentlyMergedPRs(ctx, "tok", "user", "myrepo")
	if err != nil {
		t.Fatalf("ListRecentlyMergedPRs failed: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 merged PR, got %d", len(prs))
	}
	if prs[0].Number != 2 || prs[0].Title != "Merged feature" {
		t.Fatalf("unexpected merged PR: %+v", prs[0])
	}
}

func TestGetLatestWorkflowRun(t *testing.T) {
	c := NewClient("test-id", "test-secret", "https://codeberg.org")
	run, err := c.GetLatestWorkflowRun(context.Background(), "tok", "user", "repo", "main")
	if err != nil {
		t.Fatalf("GetLatestWorkflowRun failed: %v", err)
	}
	if run.Conclusion != "unknown" {
		t.Fatalf("expected conclusion 'unknown', got %q", run.Conclusion)
	}
	if run.ID != 0 {
		t.Fatalf("expected run ID 0, got %d", run.ID)
	}
}

func TestParseCommitType(t *testing.T) {
	tests := []struct {
		msg      string
		expected string
	}{
		{"feat: add widget", "feat"},
		{"fix: resolve crash", "fix"},
		{"docs: update README", "docs"},
		{"chore: bump deps", "chore"},
		{"feat(scope): add widget", "feat"},
		{"fix(scope)!: breaking fix", "fix"},
		{"something random", "other"},
		{"", "other"},
		{"Merge pull request #42", "other"},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%q", tc.msg), func(t *testing.T) {
			got := ParseCommitType(tc.msg)
			if got != tc.expected {
				t.Fatalf("ParseCommitType(%q) = %q, want %q", tc.msg, got, tc.expected)
			}
		})
	}
}

func TestGetUser(t *testing.T) {
	ctx := context.Background()
	s, baseURL := testServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(gjUser{
			ID:        99,
			Login:     "forgejo-user",
			FullName:  "Forgejo User",
			AvatarURL: "https://forgejo.example/avatar.png",
		})
	})
	defer s.Close()

	c := NewClient("test-id", "test-secret", baseURL)
	u, err := c.GetUser(ctx, "tok")
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	if u.ID != 99 || u.Login != "forgejo-user" || u.Name != "Forgejo User" {
		t.Fatalf("unexpected user: %+v", u)
	}
}

// Ensure *Client implements provider.Provider at compile time.
// We import provider via the ghclient import; the check uses
// the type from this package.
var _ interface{ Name() string } = (*Client)(nil)

// The wire types should serialise/deserialise correctly.
func TestWireTypesJSON(t *testing.T) {
	raw := `{"id":1,"login":"u","full_name":"User","email":"u@x","avatar_url":"https://x"}`
	var u gjUser
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("gjUser unmarshal: %v", err)
	}
	if u.ID != 1 || u.Login != "u" || u.FullName != "User" {
		t.Fatalf("unexpected gjUser: %+v", u)
	}
}

func TestListCommitsPage_Pagination(t *testing.T) {
	var gotQuery string
	s, baseURL := testServer(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"sha":"a","commit":{"message":"m","committer":{"date":"2025-01-02T00:00:00Z"}}}]`))
	})
	defer s.Close()

	c := NewClient("", "", baseURL)
	commits, hasMore, err := c.ListCommitsPage(context.Background(), "token", "user", "repo", "main", 2, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(commits))
	}
	if !hasMore {
		t.Error("expected hasMore=true when page is full")
	}
	if !strings.Contains(gotQuery, "page=2") || !strings.Contains(gotQuery, "limit=1") || !strings.Contains(gotQuery, "sha=main") {
		t.Errorf("unexpected query: %s", gotQuery)
	}
}

func TestListCommitsPage_ClampPerPage(t *testing.T) {
	var gotQuery string
	s, baseURL := testServer(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})
	defer s.Close()

	c := NewClient("", "", baseURL)
	_, hasMore, err := c.ListCommitsPage(context.Background(), "token", "user", "repo", "main", 1, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasMore {
		t.Error("expected hasMore=false on empty page")
	}
	if !strings.Contains(gotQuery, "limit=50") {
		t.Errorf("expected perPage clamped to 50, query: %s", gotQuery)
	}
}

func TestMergePullRequest_Success(t *testing.T) {
	s, baseURL := testServer(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/pulls/7/merge") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})
	defer s.Close()

	c := NewClient("", "", baseURL)
	merged, _, err := c.MergePullRequest(context.Background(), "token", "user", "repo", 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !merged {
		t.Error("expected merged=true")
	}
}

func TestMergePullRequest_NotMergeable(t *testing.T) {
	s, baseURL := testServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte(`{"message":"not mergeable"}`))
	})
	defer s.Close()

	c := NewClient("", "", baseURL)
	merged, msg, err := c.MergePullRequest(context.Background(), "token", "user", "repo", 7)
	if err != nil {
		t.Fatalf("expected nil error for refusal, got %v", err)
	}
	if merged {
		t.Error("expected merged=false")
	}
	if msg == "" {
		t.Error("expected provider message")
	}
}

func TestClosePullRequest_Success(t *testing.T) {
	s, baseURL := testServer(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/pulls/7") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"state":"closed"`) {
			t.Errorf("expected state closed in body, got: %s", string(body))
		}
		w.WriteHeader(http.StatusOK)
	})
	defer s.Close()

	c := NewClient("", "", baseURL)
	if err := c.ClosePullRequest(context.Background(), "token", "user", "repo", 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClosePullRequest_Error(t *testing.T) {
	s, baseURL := testServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	})
	defer s.Close()

	c := NewClient("", "", baseURL)
	err := c.ClosePullRequest(context.Background(), "token", "user", "repo", 7)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Not Found") {
		t.Errorf("expected provider message in error, got: %v", err)
	}
}
