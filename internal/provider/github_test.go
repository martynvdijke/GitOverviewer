package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	ghclient "gitlens/internal/github"
)

// testRerunServer fakes the GitHub REST endpoints the adapter touches
// while re-running PR builds: list open PRs, list workflow runs, and the
// rerun-failed-jobs action. It records which run IDs were re-run.
func testRerunServer(t *testing.T, prsJSON, runsJSON string, rerunStatus map[int64]int) (*httptest.Server, *[]int64) {
	t.Helper()
	var rerunIDs []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Must match rerun-failed-jobs before actions/runs: the rerun
		// path contains the runs path as a prefix.
		if strings.Contains(r.URL.Path, "/rerun-failed-jobs") {
			if r.Method != "POST" {
				t.Errorf("expected POST for rerun, got %s", r.Method)
			}
			parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/rerun-failed-jobs"), "/")
			id, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
			if err != nil {
				t.Errorf("cannot parse run id from %q", r.URL.Path)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			rerunIDs = append(rerunIDs, id)
			if status, ok := rerunStatus[id]; ok {
				w.WriteHeader(status)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/actions/runs"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, runsJSON)
		case strings.Contains(r.URL.Path, "/pulls"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, prsJSON)
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, &rerunIDs
}

func TestGitHubAdapter_RerunFailedWorkflowRuns(t *testing.T) {
	prs := `[
		{"number":1,"title":"one","user":{"login":"a"},"created_at":"2026-01-01T00:00:00Z",
		 "html_url":"https://x/1","head":{"ref":"f1","sha":"sha-fail"},"base":{"ref":"main"},"mergeable_state":"clean"},
		{"number":2,"title":"two","user":{"login":"a"},"created_at":"2026-01-01T00:00:00Z",
		 "html_url":"https://x/2","head":{"ref":"f2","sha":"sha-other"},"base":{"ref":"main"},"mergeable_state":"clean"}
	]`
	runs := `{"workflow_runs":[
		{"id":11,"status":"completed","conclusion":"failure","head_sha":"sha-fail"},
		{"id":12,"status":"completed","conclusion":"success","head_sha":"sha-fail"},
		{"id":13,"status":"completed","conclusion":"failure","head_sha":"sha-other"},
		{"id":14,"status":"in_progress","conclusion":"","head_sha":"sha-fail"}
	]}`
	srv, rerunIDs := testRerunServer(t, prs, runs, nil)
	defer srv.Close()

	c := ghclient.NewClient("", "")
	c.APIURL = srv.URL
	a := NewGitHubAdapter(c)

	count, err := a.RerunFailedWorkflowRuns(context.Background(), "token", "owner", "repo", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 run re-queued, got %d", count)
	}
	if len(*rerunIDs) != 1 || (*rerunIDs)[0] != 11 {
		t.Errorf("expected only run 11 to be re-run, got %v", *rerunIDs)
	}
}

func TestGitHubAdapter_RerunFailedWorkflowRuns_NoQualifying(t *testing.T) {
	prs := `[
		{"number":1,"title":"one","user":{"login":"a"},"created_at":"2026-01-01T00:00:00Z",
		 "html_url":"https://x/1","head":{"ref":"f1","sha":"sha-ok"},"base":{"ref":"main"},"mergeable_state":"clean"}
	]`
	runs := `{"workflow_runs":[
		{"id":21,"status":"completed","conclusion":"success","head_sha":"sha-ok"},
		{"id":22,"status":"in_progress","conclusion":"","head_sha":"sha-ok"}
	]}`
	srv, rerunIDs := testRerunServer(t, prs, runs, nil)
	defer srv.Close()

	c := ghclient.NewClient("", "")
	c.APIURL = srv.URL
	a := NewGitHubAdapter(c)

	count, err := a.RerunFailedWorkflowRuns(context.Background(), "token", "owner", "repo", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 runs re-queued, got %d", count)
	}
	if len(*rerunIDs) != 0 {
		t.Errorf("expected no rerun calls, got %v", *rerunIDs)
	}
}

func TestGitHubAdapter_RerunFailedWorkflowRuns_PartialFailure(t *testing.T) {
	prs := `[
		{"number":1,"title":"one","user":{"login":"a"},"created_at":"2026-01-01T00:00:00Z",
		 "html_url":"https://x/1","head":{"ref":"f1","sha":"sha-fail"},"base":{"ref":"main"},"mergeable_state":"clean"}
	]`
	runs := `{"workflow_runs":[
		{"id":31,"status":"completed","conclusion":"failure","head_sha":"sha-fail"},
		{"id":32,"status":"completed","conclusion":"cancelled","head_sha":"sha-fail"}
	]}`
	// Run 32 refuses the re-run with 409 (not in a re-runnable state).
	srv, rerunIDs := testRerunServer(t, prs, runs, map[int64]int{32: http.StatusConflict})
	defer srv.Close()

	c := ghclient.NewClient("", "")
	c.APIURL = srv.URL
	a := NewGitHubAdapter(c)

	count, err := a.RerunFailedWorkflowRuns(context.Background(), "token", "owner", "repo", 1)
	if err == nil {
		t.Fatal("expected aggregated error, got nil")
	}
	if count != 1 {
		t.Errorf("expected 1 successful re-run, got %d", count)
	}
	if len(*rerunIDs) != 2 {
		t.Errorf("expected both runs attempted, got %v", *rerunIDs)
	}
	if !strings.Contains(err.Error(), "failed to re-run 1 of 2") {
		t.Errorf("expected aggregated error to mention '1 of 2', got: %v", err)
	}
}

func TestGitHubAdapter_RerunFailedWorkflowRuns_PRNotFound(t *testing.T) {
	prs := `[
		{"number":1,"title":"one","user":{"login":"a"},"created_at":"2026-01-01T00:00:00Z",
		 "html_url":"https://x/1","head":{"ref":"f1","sha":"sha-fail"},"base":{"ref":"main"},"mergeable_state":"clean"}
	]`
	runs := `{"workflow_runs":[]}`
	srv, rerunIDs := testRerunServer(t, prs, runs, nil)
	defer srv.Close()

	c := ghclient.NewClient("", "")
	c.APIURL = srv.URL
	a := NewGitHubAdapter(c)

	count, err := a.RerunFailedWorkflowRuns(context.Background(), "token", "owner", "repo", 99)
	if err == nil {
		t.Fatal("expected error for missing PR, got nil")
	}
	if count != 0 {
		t.Errorf("expected 0 runs re-queued, got %d", count)
	}
	if len(*rerunIDs) != 0 {
		t.Errorf("expected no rerun calls, got %v", *rerunIDs)
	}
}
