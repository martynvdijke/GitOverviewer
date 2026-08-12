package handlers

import (
	"context"
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
	h.statusFn = func(context.Context) ([]deploy.DiscoveredContainer, error) {
		return nil, nil
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

func TestDeployDashboard_LabeledContainers(t *testing.T) {
	targets := []deploy.Target{
		{Repository: "org/tracked", Image: "img", Container: "c1", TagStrategy: deploy.TagStrategyLatest},
	}
	h := NewDeployHandler(false)
	h.targetsFn = func() ([]deploy.Target, error) { return targets, nil }
	h.statusFn = func(context.Context) ([]deploy.DiscoveredContainer, error) {
		return []deploy.DiscoveredContainer{
			{Container: "c1", Image: "img", Tag: "latest", Label: "org/tracked", Tracked: true, Reason: "tracked via gitlens.deploy.target label"},
			{Container: "c2", Image: "img2", Tag: "v1.0", Label: "org/other", Tracked: false, Reason: "invalid label value"},
			{Container: "c3", Image: "img3", Tag: "v2.0", Label: "org/dup", Tracked: false, Reason: "repository configured explicitly"},
		}, nil
	}
	w := serveDeployDashboard(t, h.Dashboard)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"Labeled Containers",
		"gitlens.deploy.target",
		"c1",
		"org/tracked",
		"img:latest",
		"Tracked",
		"Not tracked",
		"c2",
		"org/dup",
		"repository configured explicitly",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected rendered deploy_tab to contain %q, got: %s", want, body)
		}
	}
	// A tracked labeled container should also mark its target row source as "label".
	if !strings.Contains(body, ">label</span>") {
		t.Fatalf("expected a 'label' source badge on the target card, got: %s", body)
	}
}

func TestDeployDashboard_StatusError(t *testing.T) {
	h := NewDeployHandler(true)
	h.targetsFn = func() ([]deploy.Target, error) {
		return []deploy.Target{{Repository: "org/app", Image: "img", Container: "c"}}, nil
	}
	h.statusFn = func(context.Context) ([]deploy.DiscoveredContainer, error) {
		return nil, errors.New("docker not available")
	}
	w := serveDeployDashboard(t, h.Dashboard)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Docker label discovery unavailable") {
		t.Fatalf("expected Docker warning banner, got: %s", w.Body.String())
	}
}
