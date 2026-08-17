package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gitlens/ent/enttest"
	"gitlens/internal/deploy"
	"gitlens/internal/github"
	"gitlens/internal/provider"
	"gitlens/internal/sync"
	"gitlens/internal/ws"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

func newTestWebhookHandler(t *testing.T, secret string) *WebhookHandler {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
	ghClient := github.NewClient("", "")
	hub := ws.NewHub()
	go hub.Run()
	syncer := sync.NewSyncer(client, ghClient, map[string]provider.Provider{"github": provider.NewGitHubAdapter(ghClient)}, hub)
	handler := NewWebhookHandler(client, syncer, secret)
	// Stub the commit lookup so tests never hit the network.
	handler.commitMsgFn = func(context.Context, string, string) string {
		return "fix: test commit message"
	}
	return handler
}

func signPayload(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// ---- Push event tests ----

func TestHandlePush_NonPushEvent(t *testing.T) {
	handler := newTestWebhookHandler(t, "")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(`{}`)))
	c.Request.Header.Set("X-GitHub-Event", "pull_request")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandlePush_InvalidJSON(t *testing.T) {
	handler := newTestWebhookHandler(t, "")

	w := httptest.NewRecorder()
	c, engine := gin.CreateTestContext(w)
	engine.POST("/webhook/github", handler.HandlePush)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(`not json`)))
	c.Request.Header.Set("X-GitHub-Event", "push")
	engine.ServeHTTP(w, c.Request)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandlePush_EmptyPayload(t *testing.T) {
	handler := newTestWebhookHandler(t, "")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(`{"ref":"","repository":{"id":0}}`)))
	c.Request.Header.Set("X-GitHub-Event", "push")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandlePush_UnknownRepo(t *testing.T) {
	handler := newTestWebhookHandler(t, "")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(`{"ref":"refs/heads/main","repository":{"id":999999}}`)))
	c.Request.Header.Set("X-GitHub-Event", "push")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandlePush_WithSecret_ValidSignature(t *testing.T) {
	secret := "test-secret"
	payload := `{"ref":"refs/heads/main","repository":{"id":1}}`

	handler := newTestWebhookHandler(t, secret)

	client := handler.client
	u, _ := client.User.Create().
		SetGithubID(700).SetLogin("webhookuser").SetAccessToken("tok").Save(context.Background())
	client.Repository.Create().
		SetGithubID(1).SetOwner("test").SetName("repo").
		SetFullName("test/repo").SetHTMLURL("https://github.com/test/repo").
		SetDefaultBranch("main").SetUserID(u.ID).
		Save(context.Background())

	sig := signPayload(secret, payload)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "push")
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandlePush_WithSecret_InvalidSignature(t *testing.T) {
	handler := newTestWebhookHandler(t, "test-secret")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	engine := gin.New()
	engine.POST("/webhook/github", handler.HandlePush)
	req := httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(`{"ref":"refs/heads/main","repository":{"id":1}}`)))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", "sha256=invalid")
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandlePush_WithSecret_MissingSignature(t *testing.T) {
	handler := newTestWebhookHandler(t, "test-secret")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	engine := gin.New()
	engine.POST("/webhook/github", handler.HandlePush)
	req := httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(`{"ref":"refs/heads/main","repository":{"id":1}}`)))
	req.Header.Set("X-GitHub-Event", "push")
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandlePush_NoSecret(t *testing.T) {
	handler := newTestWebhookHandler(t, "")

	client := handler.client
	u, _ := client.User.Create().
		SetGithubID(701).SetLogin("webhookuser2").SetAccessToken("tok").Save(context.Background())
	client.Repository.Create().
		SetGithubID(2).SetOwner("test2").SetName("repo2").
		SetFullName("test2/repo2").SetHTMLURL("https://github.com/test2/repo2").
		SetDefaultBranch("main").SetUserID(u.ID).
		Save(context.Background())

	payload := `{"ref":"refs/heads/main","repository":{"id":2}}`

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "push")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// ---- Release event tests ----

// fakeDeployer captures deploy calls for testing.
// done is closed after the first PullAndUpdate call completes.
type fakeDeployer struct {
	calls []struct {
		Target deploy.Target
		Tag    string
	}
	err  error
	done chan struct{}
}

func newFakeDeployer() *fakeDeployer {
	return &fakeDeployer{done: make(chan struct{})}
}

func (f *fakeDeployer) PullAndUpdate(_ context.Context, target deploy.Target, tag string) (*deploy.DeployResult, error) {
	defer func() { close(f.done) }()
	f.calls = append(f.calls, struct {
		Target deploy.Target
		Tag    string
	}{target, tag})
	return &deploy.DeployResult{Steps: []string{
		"pulled image " + target.Image + ":" + tag,
		"recreated container " + target.Container,
	}}, f.err
}

func (f *fakeDeployer) waitForCall() {
	<-f.done
}

// staticTargets returns a targetsProvider that always returns ts.
func staticTargets(ts []deploy.Target) targetsProvider {
	return func(context.Context) ([]deploy.Target, error) { return ts, nil }
}

// fakeNotifier captures Gotify sends for testing.
type fakeNotifier struct {
	sends []struct {
		Title    string
		Message  string
		Priority int
	}
}

func (f *fakeNotifier) Send(_ context.Context, title, message string, priority int) error {
	f.sends = append(f.sends, struct {
		Title    string
		Message  string
		Priority int
	}{title, message, priority})
	return nil
}

func makeReleasePayload(action, tag, repo string, prerelease bool) string {
	return makeReleasePayloadFull(action, tag, repo, prerelease,
		"My Awesome Release", "octocat", "https://github.com/"+repo+"/releases/tag/"+tag)
}

func makeReleasePayloadFull(action, tag, repo string, prerelease bool, name, author, url string) string {
	p, _ := json.Marshal(map[string]interface{}{
		"action": action,
		"release": map[string]interface{}{
			"tag_name":   tag,
			"prerelease": prerelease,
			"name":       name,
			"html_url":   url,
			"author": map[string]interface{}{
				"login": author,
			},
		},
		"repository": map[string]interface{}{
			"full_name": repo,
		},
	})
	return string(p)
}

func TestHandleRelease_Published_MatchingTarget(t *testing.T) {
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{
		Repository:  "test/repo",
		Image:       "ghcr.io/test/repo",
		Container:   "test-app",
		TagStrategy: deploy.TagStrategyReleaseTag,
	}
	fake := newFakeDeployer()
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := makeReleasePayload("published", "v1.2.3", "test/repo", false)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "release")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	fake.waitForCall()
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 deploy call, got %d", len(fake.calls))
	}
	if fake.calls[0].Tag != "1.2.3" {
		t.Fatalf("expected tag 1.2.3, got %s", fake.calls[0].Tag)
	}
}

func TestHandleRelease_WithSecret_ValidSignature(t *testing.T) {
	secret := "deploy-secret"
	handler := newTestWebhookHandler(t, secret)
	target := deploy.Target{
		Repository:  "test/repo",
		Image:       "img",
		Container:   "c",
		TagStrategy: deploy.TagStrategyLatest,
	}
	fake := newFakeDeployer()
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := makeReleasePayload("published", "v2.0.0", "test/repo", false)

	sig := signPayload(secret, payload)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "release")
	c.Request.Header.Set("X-Hub-Signature-256", sig)
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	fake.waitForCall()
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 deploy call, got %d", len(fake.calls))
	}
	if fake.calls[0].Tag != "latest" {
		t.Fatalf("expected tag latest, got %s", fake.calls[0].Tag)
	}
}

func TestHandleRelease_NoTargets(t *testing.T) {
	handler := newTestWebhookHandler(t, "")
	// No SetDeployer called — deployer is nil, targets empty

	payload := makeReleasePayload("published", "v1.0.0", "any/repo", false)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "release")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleRelease_UnmatchedRepo(t *testing.T) {
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{
		Repository:  "org/alpha",
		Image:       "img",
		Container:   "c",
		TagStrategy: deploy.TagStrategyReleaseTag,
	}
	fake := newFakeDeployer()
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := makeReleasePayload("published", "v1.0.0", "org/gamma", false)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "release")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected 0 deploy calls, got %d", len(fake.calls))
	}
}

func TestHandleRelease_NonPublishedAction(t *testing.T) {
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{
		Repository:  "test/repo",
		Image:       "img",
		Container:   "c",
		TagStrategy: deploy.TagStrategyReleaseTag,
	}
	fake := &fakeDeployer{}
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := makeReleasePayload("created", "v1.0.0", "test/repo", false)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "release")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected 0 deploy calls for non-published action, got %d", len(fake.calls))
	}
}

func TestHandleRelease_PrereleaseSkipped(t *testing.T) {
	os.Unsetenv("DEPLOY_ALLOW_PRERELEASE")

	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{
		Repository:  "test/repo",
		Image:       "img",
		Container:   "c",
		TagStrategy: deploy.TagStrategyReleaseTag,
	}
	fake := &fakeDeployer{}
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := makeReleasePayload("published", "v1.0.0-rc.1", "test/repo", true)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "release")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected 0 deploy calls for prerelease, got %d", len(fake.calls))
	}
}

func TestHandleRelease_BadSignature(t *testing.T) {
	handler := newTestWebhookHandler(t, "secret")
	fake := &fakeDeployer{}
	handler.SetDeployer(staticTargets([]deploy.Target{{Repository: "test/repo", Image: "img", Container: "c"}}), fake, nil)

	payload := makeReleasePayload("published", "v1.0.0", "test/repo", false)

	w := httptest.NewRecorder()
	engine := gin.New()
	engine.POST("/webhook/github", handler.HandlePush)
	req := httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	req.Header.Set("X-GitHub-Event", "release")
	req.Header.Set("X-Hub-Signature-256", "sha256=badsig")
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected 0 deploy calls after bad sig, got %d", len(fake.calls))
	}
}

func TestHandleRelease_GotifySuccessMentionsRelease(t *testing.T) {
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{
		Repository:  "test/repo",
		Image:       "ghcr.io/test/repo",
		Container:   "test-app",
		TagStrategy: deploy.TagStrategyReleaseTag,
	}
	fake := newFakeDeployer()
	notif := &fakeNotifier{}
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, notif)

	payload := makeReleasePayloadFull("published", "v1.2.3", "test/repo", false,
		"New Charting", "octocat", "https://github.com/test/repo/releases/tag/v1.2.3")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "release")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	fake.waitForCall()
	if len(notif.sends) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notif.sends))
	}
	s := notif.sends[0]
	for _, want := range []string{
		`"New Charting"`,                   // release name
		"v1.2.3",                           // release tag
		"octocat",                          // author
		"Commit: fix: test commit message", // deployed commit message
		"https://github.com/test/repo/releases/tag/v1.2.3", // release link
		"test-app",                // container
		"ghcr.io/test/repo:1.2.3", // image:tag
	} {
		if !strings.Contains(s.Message, want) {
			t.Errorf("notification should mention %q, got: %s", want, s.Message)
		}
	}
	for _, notWant := range []string{
		"What happened:",
		"pulled image ghcr.io/test/repo:1.2.3",
		"recreated container test-app",
	} {
		if strings.Contains(s.Message, notWant) {
			t.Errorf("notification should not mention deploy steps %q, got: %s", notWant, s.Message)
		}
	}
	if !strings.Contains(s.Title, "v1.2.3") {
		t.Errorf("title should mention the release tag, got: %s", s.Title)
	}
	if s.Priority != 2 {
		t.Errorf("expected priority 2 for success, got %d", s.Priority)
	}
}

// makeRegistryPackagePayload builds a registry_package webhook payload.
// An empty repo omits the top-level repository object.
func makeRegistryPackagePayload(action, tag, name, owner, repo, pkgType string) string {
	reg := map[string]interface{}{
		"name":         name,
		"package_type": pkgType,
		"owner":        map[string]interface{}{"login": owner},
		"html_url":     "https://github.com/" + owner + "/" + name + "/pkgs/container/" + name,
		"package_version": map[string]interface{}{
			// Real GHCR payloads carry a sha256 digest here; the pushed tag
			// lives in container_metadata.tag.name.
			"version":  "sha256:3da1996a8115d7616457760d9920b815241d0a03b34cf5f04e9a0e9d8de37498",
			"html_url": "https://github.com/" + owner + "/" + name + "/pkgs/container/" + name + "/1234",
			"container_metadata": map[string]interface{}{
				"tag": map[string]interface{}{"name": tag},
			},
		},
	}
	payload := map[string]interface{}{
		"action":           action,
		"registry_package": reg,
	}
	if repo != "" {
		payload["repository"] = map[string]interface{}{"full_name": repo}
	}
	p, _ := json.Marshal(payload)
	return string(p)
}

// ---- Registry package event tests ----

func TestHandlePackage_Published_MatchingTarget(t *testing.T) {
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{
		Repository:  "test/repo",
		Image:       "ghcr.io/test/repo",
		Container:   "test-app",
		TagStrategy: deploy.TagStrategyReleaseTag,
	}
	fake := newFakeDeployer()
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := makeRegistryPackagePayload("published", "v1.2.3", "repo", "test", "test/repo", "container")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "registry_package")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	fake.waitForCall()
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 deploy call, got %d", len(fake.calls))
	}
	if fake.calls[0].Tag != "1.2.3" {
		t.Fatalf("expected tag 1.2.3, got %s", fake.calls[0].Tag)
	}
}

func TestHandlePackage_EventNamePackage(t *testing.T) {
	// GitHub Apps deliver the same payload under the "package" event name and
	// the "package" key.
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{
		Repository:  "test/repo",
		Image:       "img",
		Container:   "c",
		TagStrategy: deploy.TagStrategyLatest,
	}
	fake := newFakeDeployer()
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := `{"action":"published","package":{"name":"repo","package_type":"container","owner":{"login":"test"},"package_version":{"version":"sha256:abc","container_metadata":{"tag":{"name":"latest"}}}},"repository":{"full_name":"test/repo"}}`

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "package")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	fake.waitForCall()
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 deploy call, got %d", len(fake.calls))
	}
}

func TestHandlePackage_LatestStrategy_DeploysLatestTag(t *testing.T) {
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{
		Repository:  "test/repo",
		Image:       "img",
		Container:   "c",
		TagStrategy: deploy.TagStrategyLatest,
	}
	fake := newFakeDeployer()
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := makeRegistryPackagePayload("published", "latest", "repo", "test", "test/repo", "container")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "registry_package")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	fake.waitForCall()
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 deploy call, got %d", len(fake.calls))
	}
	if fake.calls[0].Tag != "latest" {
		t.Fatalf("expected tag latest, got %s", fake.calls[0].Tag)
	}
}

func TestHandlePackage_LatestStrategy_SkipsVersionedTag(t *testing.T) {
	// A single image push with several tags fires one event per tag; only the
	// "latest" tag event should deploy for the latest strategy.
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{
		Repository:  "test/repo",
		Image:       "img",
		Container:   "c",
		TagStrategy: deploy.TagStrategyLatest,
	}
	fake := &fakeDeployer{}
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := makeRegistryPackagePayload("published", "v1.2.3", "repo", "test", "test/repo", "container")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "registry_package")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected 0 deploy calls, got %d", len(fake.calls))
	}
}

func TestHandlePackage_ReleaseTagStrategy_SkipsLatest(t *testing.T) {
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{
		Repository:  "test/repo",
		Image:       "img",
		Container:   "c",
		TagStrategy: deploy.TagStrategyReleaseTag,
	}
	fake := &fakeDeployer{}
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := makeRegistryPackagePayload("published", "latest", "repo", "test", "test/repo", "container")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "registry_package")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected 0 deploy calls, got %d", len(fake.calls))
	}
}

func TestHandlePackage_NonContainerType(t *testing.T) {
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{Repository: "test/repo", Image: "img", Container: "c"}
	fake := &fakeDeployer{}
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := makeRegistryPackagePayload("published", "v1.0.0", "repo", "test", "test/repo", "npm")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "registry_package")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected 0 deploy calls for non-container package, got %d", len(fake.calls))
	}
}

func TestHandlePackage_NonPublishedAction(t *testing.T) {
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{Repository: "test/repo", Image: "img", Container: "c"}
	fake := &fakeDeployer{}
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := makeRegistryPackagePayload("updated", "v1.0.0", "repo", "test", "test/repo", "container")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "registry_package")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected 0 deploy calls for non-published action, got %d", len(fake.calls))
	}
}

func TestHandlePackage_UnmatchedRepo(t *testing.T) {
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{Repository: "org/alpha", Image: "img", Container: "c"}
	fake := &fakeDeployer{}
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := makeRegistryPackagePayload("published", "v1.0.0", "beta", "org", "org/beta", "container")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "registry_package")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected 0 deploy calls, got %d", len(fake.calls))
	}
}

func TestHandlePackage_FallsBackToOwnerName(t *testing.T) {
	// Payloads without the top-level repository object match via owner/name.
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{
		Repository:  "test/repo",
		Image:       "img",
		Container:   "c",
		TagStrategy: deploy.TagStrategyReleaseTag,
	}
	fake := newFakeDeployer()
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := makeRegistryPackagePayload("published", "v1.2.3", "repo", "test", "", "container")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "registry_package")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	fake.waitForCall()
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 deploy call, got %d", len(fake.calls))
	}
	if fake.calls[0].Tag != "1.2.3" {
		t.Fatalf("expected tag 1.2.3, got %s", fake.calls[0].Tag)
	}
}

func TestHandlePackage_DigestOnlyVersion_Ignored(t *testing.T) {
	// Without container_metadata the only version info is a sha256 digest,
	// which is not a usable image tag.
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{Repository: "test/repo", Image: "img", Container: "c"}
	fake := &fakeDeployer{}
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, nil)

	payload := `{"action":"published","registry_package":{"name":"repo","package_type":"container","owner":{"login":"test"},"package_version":{"version":"sha256:3da1996a8115d7616457760d9920b815241d0a03b34cf5f04e9a0e9d8de37498"}},"repository":{"full_name":"test/repo"}}`

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "registry_package")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected 0 deploy calls, got %d", len(fake.calls))
	}
}

func TestHandlePackage_GotifySuccessMentionsImage(t *testing.T) {
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{
		Repository:  "test/repo",
		Image:       "ghcr.io/test/repo",
		Container:   "test-app",
		TagStrategy: deploy.TagStrategyReleaseTag,
	}
	fake := newFakeDeployer()
	notif := &fakeNotifier{}
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, notif)

	payload := makeRegistryPackagePayload("published", "v1.2.3", "repo", "test", "test/repo", "container")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "registry_package")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	fake.waitForCall()
	if len(notif.sends) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notif.sends))
	}
	s := notif.sends[0]
	for _, want := range []string{
		"Image v1.2.3 published",
		"test-app",                // container
		"ghcr.io/test/repo:1.2.3", // image:tag
	} {
		if !strings.Contains(s.Message, want) {
			t.Errorf("notification should mention %q, got: %s", want, s.Message)
		}
	}
	if strings.Contains(s.Message, "What happened:") {
		t.Errorf("notification should not mention deploy steps, got: %s", s.Message)
	}
	if !strings.Contains(s.Title, "image deploy") {
		t.Errorf("title should say image deploy, got: %s", s.Title)
	}
	if s.Priority != 2 {
		t.Errorf("expected priority 2 for success, got %d", s.Priority)
	}
}

func TestHandleRelease_GotifyFailureMentionsReleaseAndError(t *testing.T) {
	handler := newTestWebhookHandler(t, "")
	target := deploy.Target{
		Repository:  "test/repo",
		Image:       "img",
		Container:   "c",
		TagStrategy: deploy.TagStrategyReleaseTag,
	}
	fake := newFakeDeployer()
	fake.err = errors.New("pull failed")
	notif := &fakeNotifier{}
	handler.SetDeployer(staticTargets([]deploy.Target{target}), fake, notif)

	payload := makeReleasePayload("published", "v1.2.3", "test/repo", false)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(payload)))
	c.Request.Header.Set("X-GitHub-Event", "release")
	handler.HandlePush(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	fake.waitForCall()
	if len(notif.sends) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notif.sends))
	}
	s := notif.sends[0]
	if !strings.Contains(s.Message, "pull failed") {
		t.Errorf("notification should mention the error, got: %s", s.Message)
	}
	if !strings.Contains(s.Message, "octocat") {
		t.Errorf("notification should mention the release author, got: %s", s.Message)
	}
	if !strings.Contains(s.Message, "Commit: fix: test commit message") {
		t.Errorf("notification should mention the deployed commit, got: %s", s.Message)
	}
	if strings.Contains(s.Message, "What happened:") {
		t.Errorf("notification should not mention deploy steps, got: %s", s.Message)
	}
	if s.Priority != 5 {
		t.Errorf("expected priority 5 for failure, got %d", s.Priority)
	}
}

func TestDeployMessage_IncludesCommitOmitsSteps(t *testing.T) {
	msg := deployMessage(releaseInfo{
		Repo:   "test/repo",
		Tag:    "v1.2.3",
		Name:   "New Charting",
		Author: "octocat",
		URL:    "https://github.com/test/repo/releases/tag/v1.2.3",
		Commit: "fix: improve compact layouts",
	}, deploy.Target{Container: "test-app", Image: "ghcr.io/test/repo"}, "1.2.3", nil)

	for _, want := range []string{
		"Commit: fix: improve compact layouts",
		"Container test-app updated to ghcr.io/test/repo:1.2.3",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected %q in message, got: %s", want, msg)
		}
	}
	for _, notWant := range []string{"What happened:", "• "} {
		if strings.Contains(msg, notWant) {
			t.Errorf("message should not contain %q, got: %s", notWant, msg)
		}
	}
}

func TestDeployMessage_OmitsCommitWhenUnknown(t *testing.T) {
	msg := deployMessage(releaseInfo{
		Repo: "test/repo",
		Tag:  "v1.2.3",
	}, deploy.Target{Container: "test-app", Image: "img"}, "1.2.3", nil)

	if strings.Contains(msg, "Commit:") {
		t.Errorf("message should not mention a commit when none is known, got: %s", msg)
	}
	if !strings.Contains(msg, "Container test-app updated to img:1.2.3") {
		t.Errorf("expected container update line, got: %s", msg)
	}
}
