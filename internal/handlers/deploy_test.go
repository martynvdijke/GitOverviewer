package handlers

import (
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlens/internal/deploy"

	"github.com/gin-gonic/gin"
)

// realDeployTabTmpl parses the production deploy_tab template from
// views/deploy.html so handler tests exercise the real rendering path.
func realDeployTabTmpl(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("").ParseFiles("../../views/deploy.html"))
}

// serveDeployDashboard sets up a gin engine with the deploy_tab template and
// registers the given handler at GET /deploy.
func serveDeployDashboard(t *testing.T, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	engine := gin.New()
	engine.SetHTMLTemplate(realDeployTabTmpl(t))
	engine.GET("/deploy", handler)
	req := httptest.NewRequest("GET", "/deploy", nil)
	engine.ServeHTTP(w, req)
	return w
}

func newTestDeployHandler(targets []deploy.Target, err error, gotifyOn bool) *DeployHandler {
	h := NewDeployHandler(gotifyOn)
	h.targetsFn = func() ([]deploy.Target, error) {
		return targets, err
	}
	return h
}

func TestDeployDashboard_WithTargets(t *testing.T) {
	targets := []deploy.Target{
		{Repository: "martynvdijke/gitlens", Image: "ghcr.io/martynvdijke/gitlens", Container: "gitlens", TagStrategy: deploy.TagStrategyReleaseTag},
		{Repository: "org/app", Image: "ghcr.io/org/app", Container: "app-svc", TagStrategy: deploy.TagStrategyLatest},
	}
	h := newTestDeployHandler(targets, nil, true)
	w := serveDeployDashboard(t, h.Dashboard)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"Deploy Targets",
		"martynvdijke/gitlens",
		"org/app",
		"ghcr.io/org/app",
		"app-svc",
		"Backend:",
		"2 target(s)",
		"release_tag",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected rendered deploy_tab to contain %q, got: %s", want, body)
		}
	}
}

func TestDeployDashboard_NoTargets(t *testing.T) {
	h := newTestDeployHandler(nil, nil, false)
	w := serveDeployDashboard(t, h.Dashboard)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Deploy Subsystem Not Configured") {
		t.Fatalf("expected 'not configured' state, got: %s", w.Body.String())
	}
}

func TestDeployDashboard_EmptyTargets(t *testing.T) {
	h := newTestDeployHandler([]deploy.Target{}, nil, false)
	w := serveDeployDashboard(t, h.Dashboard)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Deploy Subsystem Not Configured") {
		t.Fatalf("expected 'not configured' state, got: %s", w.Body.String())
	}
}

func TestDeployDashboard_GotifyOff(t *testing.T) {
	targets := []deploy.Target{
		{Repository: "org/app", Image: "img", Container: "c", TagStrategy: deploy.TagStrategyLatest},
	}
	h := newTestDeployHandler(targets, nil, false)
	w := serveDeployDashboard(t, h.Dashboard)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Not configured") {
		t.Fatalf("expected 'Not configured' Gotify badge, got: %s", w.Body.String())
	}
}

func TestDeployDashboard_DockerError(t *testing.T) {
	targets := []deploy.Target{
		{Repository: "org/fallback", Image: "img", Container: "c", TagStrategy: deploy.TagStrategyReleaseTag},
	}
	h := newTestDeployHandler(targets, errors.New("docker not available"), true)
	w := serveDeployDashboard(t, h.Dashboard)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Docker label discovery unavailable") {
		t.Fatalf("expected Docker warning banner, got: %s", w.Body.String())
	}
}

func TestDeployDashboard_ErrorNoTargets(t *testing.T) {
	h := newTestDeployHandler(nil, errors.New("config failed"), false)
	w := serveDeployDashboard(t, h.Dashboard)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
