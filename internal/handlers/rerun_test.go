package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"gitlens/ent"
	"gitlens/internal/provider"
)

func TestRerunPRBuilds_Success(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", rerunFn: func(n int) (int, error) {
		return 2, nil
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := postJSON(engine, "/prs/rerun", fmt.Sprintf(`{"repo_id":%d,"pr_number":5}`, repo.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "TYPE=success") {
		t.Errorf("expected success type, got: %s", body)
	}
	if !strings.Contains(body, "MSG=Re-queued 2 failed build(s) for #5") {
		t.Errorf("expected success toast, got: %s", body)
	}
	if len(fake.rerunCalls) != 1 || fake.rerunCalls[0] != 5 {
		t.Errorf("expected provider rerun called for PR 5, got %v", fake.rerunCalls)
	}
}

func TestRerunPRBuilds_NoFailedBuildsReturnsWarning(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", rerunFn: func(n int) (int, error) {
		return 0, nil
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := postJSON(engine, "/prs/rerun", fmt.Sprintf(`{"repo_id":%d,"pr_number":7}`, repo.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (HTMX partial), got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "TYPE=warning") || !strings.Contains(body, "MSG=No failed builds for #7") {
		t.Errorf("expected warning toast, got: %s", body)
	}
	if len(fake.rerunCalls) != 1 {
		t.Errorf("expected provider rerun called once, got %v", fake.rerunCalls)
	}
}

func TestRerunPRBuilds_UnsupportedReturnsWarning(t *testing.T) {
	fake := &mergeFakeProvider{name: "forgejo", rerunFn: func(n int) (int, error) {
		return 0, provider.ErrUnsupported
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"forgejo": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "forgejo")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := postJSON(engine, "/prs/rerun", fmt.Sprintf(`{"repo_id":%d,"pr_number":4}`, repo.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (no 5xx), got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "TYPE=warning") || !strings.Contains(body, "not supported for this provider") {
		t.Errorf("expected warning toast, got: %s", body)
	}
}

func TestRerunPRBuilds_ProviderErrorReturnsDanger(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", rerunFn: func(n int) (int, error) {
		return 0, fmt.Errorf("403 Forbidden")
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	repo := createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := postJSON(engine, "/prs/rerun", fmt.Sprintf(`{"repo_id":%d,"pr_number":3}`, repo.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (HTMX partial), got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "TYPE=danger") || !strings.Contains(body, "Permission denied") {
		t.Errorf("expected danger toast with friendly message, got: %s", body)
	}
}

func TestRerunPRBuilds_InvalidBodyReturns400(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", rerunFn: func(n int) (int, error) {
		return 0, nil
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})
	u := createMergeUser(t, client)
	createMergeRepo(t, client, u.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(u.ID))

	w := postJSON(engine, "/prs/rerun", `not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if len(fake.rerunCalls) != 0 {
		t.Errorf("expected no provider calls, got %v", fake.rerunCalls)
	}
}

func TestRerunPRBuilds_RepoNotOwnedReturns404(t *testing.T) {
	fake := &mergeFakeProvider{name: "fake", rerunFn: func(n int) (int, error) {
		return 0, nil
	}}
	h, client := newMergeTestHandler(t, map[string]provider.Provider{"fake": fake})

	// The acting user has no relation to the repo.
	acting := createMergeUser(t, client)
	other := createMergeUserWithID(t, client, 8888, "otheruser")
	repo := createMergeRepo(t, client, other.ID, "fake")
	engine := newMergeTestEngine(t, h, int64(acting.ID))

	w := postJSON(engine, "/prs/rerun", fmt.Sprintf(`{"repo_id":%d,"pr_number":1}`, repo.ID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	if len(fake.rerunCalls) != 0 {
		t.Errorf("expected no provider calls, got %v", fake.rerunCalls)
	}
}

// createMergeUserWithID is createMergeUser with a distinct github ID and
// login so two users can coexist in one test DB.
func createMergeUserWithID(t *testing.T, client *ent.Client, githubID int64, login string) *ent.User {
	t.Helper()
	u, err := client.User.Create().
		SetGithubID(githubID).
		SetLogin(login).
		SetAccessToken("token").
		Save(t.Context())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}
