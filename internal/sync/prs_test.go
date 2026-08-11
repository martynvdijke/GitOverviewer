package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ghclient "gitlens/internal/github"
)

func TestSyncPullRequests_BuildStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"workflow_runs":[
			{"id":1,"status":"completed","conclusion":"success","head_sha":"sha-success"},
			{"id":2,"status":"completed","conclusion":"failure","head_sha":"sha-failure"},
			{"id":3,"status":"in_progress","conclusion":null,"head_sha":"sha-running"},
			{"id":4,"status":"completed","conclusion":"cancelled","head_sha":"sha-cancelled"}
		]}`))
	}))
	defer srv.Close()

	pr := func(n int, headSHA string) *ghclient.PullRequest {
		return &ghclient.PullRequest{
			Number: n, Title: "pr", Author: "a",
			CreatedAt: time.Now(), HeadRef: "f", BaseRef: "main", HeadSHA: headSHA,
		}
	}
	fake := &fakeProvider{listPRs: []*ghclient.PullRequest{
		pr(1, "sha-success"),
		pr(2, "sha-failure"),
		pr(3, "sha-running"),
		pr(4, "sha-cancelled"),
		pr(5, "sha-unknown"), // no run matches -> no badge
	}}

	syncer, client := newFakeSyncer(t, fake)
	syncer.gh.APIURL = srv.URL

	ctx := context.Background()
	u, err := client.User.Create().
		SetGithubID(4243).SetLogin("prsuser").SetAccessToken("token").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	repo, err := client.Repository.Create().
		SetGithubID(4243).SetOwner("u").SetName("prs-repo").
		SetFullName("u/prs-repo").SetHTMLURL("https://example.com/u/prs-repo").
		SetDefaultBranch("main").SetUserID(u.ID).
		SetProvider("github").
		Save(ctx)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	updated := client.Repository.UpdateOneID(repo.ID)
	syncer.syncPullRequests(ctx, fake, "token", repo, updated)
	if _, err := updated.Save(ctx); err != nil {
		t.Fatalf("save updated repo: %v", err)
	}

	got := client.Repository.GetX(ctx, repo.ID)
	var summaries []struct {
		Number      int    `json:"n"`
		BuildStatus string `json:"bs"`
	}
	if err := json.Unmarshal([]byte(got.PullRequests), &summaries); err != nil {
		t.Fatalf("unmarshal pull requests: %v (raw=%s)", err, got.PullRequests)
	}
	want := map[int]string{
		1: "success",
		2: "failure",
		3: "in_progress",
		4: "cancelled",
		5: "",
	}
	if len(summaries) != 5 {
		t.Fatalf("expected 5 summaries, got %d: %s", len(summaries), got.PullRequests)
	}
	for _, s := range summaries {
		if want[s.Number] != s.BuildStatus {
			t.Errorf("PR #%d: expected build status %q, got %q", s.Number, want[s.Number], s.BuildStatus)
		}
	}
}
