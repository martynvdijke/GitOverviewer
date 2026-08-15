package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gitlens/internal/deploy"
	"gitlens/internal/services"
	"gitlens/internal/services/npm"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// fakes for the link store / kuma / npm interfaces
// ---------------------------------------------------------------------------

type fakeLinkStore struct {
	links     map[string]*services.Link // key: container + "/" + service
	upsertErr error
	deleteErr error
	allErr    error
}

func newFakeLinkStore() *fakeLinkStore {
	return &fakeLinkStore{links: map[string]*services.Link{}}
}

func (f *fakeLinkStore) Upsert(ctx context.Context, container string, service services.Service, reference string) (*services.Link, error) {
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	l := &services.Link{Container: container, Service: service, Reference: reference}
	f.links[container+"/"+string(service)] = l
	return l, nil
}

func (f *fakeLinkStore) Delete(ctx context.Context, container string, service services.Service) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.links, container+"/"+string(service))
	return nil
}

func (f *fakeLinkStore) All(ctx context.Context) ([]services.Link, error) {
	if f.allErr != nil {
		return nil, f.allErr
	}
	out := make([]services.Link, 0, len(f.links))
	for _, l := range f.links {
		out = append(out, *l)
	}
	return out, nil
}

type fakeKuma struct {
	createID  int64
	createErr error
	status    string
	statusErr error
}

func (f *fakeKuma) AddHTTPMonitor(ctx context.Context, name, url string) (int64, error) {
	return f.createID, f.createErr
}

func (f *fakeKuma) MonitorStatus(ctx context.Context, monitorID int64) (string, error) {
	return f.status, f.statusErr
}

type fakeNPM struct {
	createID  int
	createErr error
	state     string
	stateErr  error
}

func (f *fakeNPM) CreateProxyHost(ctx context.Context, domain, host string, port int) (int, error) {
	return f.createID, f.createErr
}

func (f *fakeNPM) ProxyHostState(ctx context.Context, hostname string) (string, error) {
	return f.state, f.stateErr
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newLinkTestDeployHandler returns a DeployHandler whose statusFn reports a
// single labeled container "c1" with published port 8080.
func newLinkTestDeployHandler() *DeployHandler {
	h := NewDeployHandler(false)
	h.targetsFn = func() ([]deploy.Target, error) { return nil, nil }
	h.statusFn = func(context.Context) ([]deploy.DiscoveredContainer, error) {
		return []deploy.DiscoveredContainer{
			{Container: "c1", Image: "img", Tag: "latest", Label: "org/app", Tracked: true, Port: 8080},
		}, nil
	}
	return h
}

// serveDeployLinks registers the service-link routes on a fresh gin engine
// and serves the request.
func serveDeployLinks(t *testing.T, h *DeployHandler, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	engine := gin.New()
	engine.SetHTMLTemplate(realDeployTabTmpl(t))
	engine.POST("/deploy/links", h.CreateLink)
	engine.DELETE("/deploy/links", h.DeleteLink)
	engine.POST("/deploy/links/uptime-kuma/add-monitor", h.AddKumaMonitor)
	engine.POST("/deploy/links/npm/add-proxy-host", h.AddNPMProxyHost)
	engine.GET("/deploy/links/authelia/yaml", h.AutheliaYAML)
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	engine.ServeHTTP(w, req)
	return w
}

// ---------------------------------------------------------------------------
// badge states
// ---------------------------------------------------------------------------

func TestDeployDashboard_ServiceBadges_AllNotConfigured(t *testing.T) {
	h := newLinkTestDeployHandler() // no store/kuma/npm set
	w := serveDeployDashboard(t, h.Dashboard)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"Service Links", "Uptime Kuma", "NPM", "Authelia", "Not configured"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected rendered deploy_tab to contain %q, got: %s", want, body)
		}
	}
}

func TestDeployDashboard_ServiceBadges_ConfiguredNotLinked(t *testing.T) {
	h := newLinkTestDeployHandler()
	h.SetServiceClients(newFakeLinkStore(), &fakeKuma{}, &fakeNPM{})
	w := serveDeployDashboard(t, h.Dashboard)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"Service Links", "Not linked", "Add monitor", "Add proxy host", "Link"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected rendered deploy_tab to contain %q, got: %s", want, body)
		}
	}
}

func TestDeployDashboard_ServiceBadges_Linked(t *testing.T) {
	store := newFakeLinkStore()
	store.links["c1/uptime_kuma"] = &services.Link{Container: "c1", Service: "uptime_kuma", Reference: "42"}
	h := newLinkTestDeployHandler()
	h.SetServiceClients(store, &fakeKuma{status: "up"}, &fakeNPM{state: "enabled"})
	w := serveDeployDashboard(t, h.Dashboard)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"Linked", "42", "up", "Unlink"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected rendered deploy_tab to contain %q, got: %s", want, body)
		}
	}
}

// ---------------------------------------------------------------------------
// link / unlink flow
// ---------------------------------------------------------------------------

func TestCreateLink_Success(t *testing.T) {
	store := newFakeLinkStore()
	h := newLinkTestDeployHandler()
	h.SetServiceClients(store, &fakeKuma{}, &fakeNPM{})

	form := url.Values{"container": {"c1"}, "service": {"uptime_kuma"}, "reference": {"42"}}
	w := serveDeployLinks(t, h, http.MethodPost, "/deploy/links", form)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if _, ok := store.links["c1/uptime_kuma"]; !ok {
		t.Fatal("expected link to be stored")
	}
	if !strings.Contains(w.Body.String(), "Linked c1") {
		t.Fatalf("expected success flash, got: %s", w.Body.String())
	}
}

func TestCreateLink_UnconfiguredService(t *testing.T) {
	store := newFakeLinkStore()
	h := newLinkTestDeployHandler()
	h.SetServiceClients(store, nil, nil) // kuma/npm unconfigured

	form := url.Values{"container": {"c1"}, "service": {"uptime_kuma"}, "reference": {"42"}}
	w := serveDeployLinks(t, h, http.MethodPost, "/deploy/links", form)
	if _, ok := store.links["c1/uptime_kuma"]; ok {
		t.Fatal("expected no link stored for unconfigured service")
	}
	if !strings.Contains(w.Body.String(), "not configured") {
		t.Fatalf("expected 'not configured' flash, got: %s", w.Body.String())
	}
}

func TestCreateLink_Validation(t *testing.T) {
	store := newFakeLinkStore()
	h := newLinkTestDeployHandler()
	h.SetServiceClients(store, &fakeKuma{}, &fakeNPM{})

	cases := []struct {
		name      string
		form      url.Values
		wantFlash string
	}{
		{"missing reference", url.Values{"container": {"c1"}, "service": {"npm"}}, "reference"},
		{"unknown service", url.Values{"container": {"c1"}, "service": {"grafana"}, "reference": {"x"}}, "Unknown service"},
		{"bad monitor id", url.Values{"container": {"c1"}, "service": {"uptime_kuma"}, "reference": {"abc"}}, "Monitor ID"},
		{"unknown container", url.Values{"container": {"nope"}, "service": {"authelia"}, "reference": {"app.example.com"}}, "not a labeled container"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := serveDeployLinks(t, h, http.MethodPost, "/deploy/links", tc.form)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			if !strings.Contains(w.Body.String(), tc.wantFlash) {
				t.Fatalf("expected flash containing %q, got: %s", tc.wantFlash, w.Body.String())
			}
		})
	}
}

func TestDeleteLink_Success(t *testing.T) {
	store := newFakeLinkStore()
	store.links["c1/uptime_kuma"] = &services.Link{Container: "c1", Service: "uptime_kuma", Reference: "42"}
	h := newLinkTestDeployHandler()
	h.SetServiceClients(store, &fakeKuma{}, &fakeNPM{})

	form := url.Values{"container": {"c1"}, "service": {"uptime_kuma"}}
	w := serveDeployLinks(t, h, http.MethodDelete, "/deploy/links", form)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if _, ok := store.links["c1/uptime_kuma"]; ok {
		t.Fatal("expected link to be deleted")
	}
	if !strings.Contains(w.Body.String(), "Unlinked c1") {
		t.Fatalf("expected success flash, got: %s", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// add-monitor / add-proxy-host
// ---------------------------------------------------------------------------

func TestAddKumaMonitor_Success(t *testing.T) {
	store := newFakeLinkStore()
	h := newLinkTestDeployHandler()
	h.SetServiceClients(store, &fakeKuma{createID: 7}, &fakeNPM{})

	form := url.Values{"container": {"c1"}}
	w := serveDeployLinks(t, h, http.MethodPost, "/deploy/links/uptime-kuma/add-monitor", form)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	l, ok := store.links["c1/uptime_kuma"]
	if !ok {
		t.Fatal("expected link stored after monitor creation")
	}
	if l.Reference != "7" {
		t.Fatalf("expected reference 7, got %q", l.Reference)
	}
	if !strings.Contains(w.Body.String(), "Created Uptime Kuma monitor 7") {
		t.Fatalf("expected success flash, got: %s", w.Body.String())
	}
}

func TestAddKumaMonitor_Failure(t *testing.T) {
	store := newFakeLinkStore()
	h := newLinkTestDeployHandler()
	h.SetServiceClients(store, &fakeKuma{createErr: errors.New("kuma unreachable")}, &fakeNPM{})

	form := url.Values{"container": {"c1"}}
	w := serveDeployLinks(t, h, http.MethodPost, "/deploy/links/uptime-kuma/add-monitor", form)
	if _, ok := store.links["c1/uptime_kuma"]; ok {
		t.Fatal("expected no link stored on failure")
	}
	if !strings.Contains(w.Body.String(), "Failed to create monitor") {
		t.Fatalf("expected failure flash, got: %s", w.Body.String())
	}
}

func TestAddKumaMonitor_Unconfigured(t *testing.T) {
	store := newFakeLinkStore()
	h := newLinkTestDeployHandler()
	h.SetServiceClients(store, nil, &fakeNPM{})

	form := url.Values{"container": {"c1"}}
	w := serveDeployLinks(t, h, http.MethodPost, "/deploy/links/uptime-kuma/add-monitor", form)
	if !strings.Contains(w.Body.String(), "not configured") {
		t.Fatalf("expected 'not configured' flash, got: %s", w.Body.String())
	}
}

func TestAddNPMProxyHost_Success(t *testing.T) {
	store := newFakeLinkStore()
	h := newLinkTestDeployHandler()
	h.SetServiceClients(store, &fakeKuma{}, &fakeNPM{createID: 11})

	form := url.Values{"container": {"c1"}, "domain": {"app.example.com"}}
	w := serveDeployLinks(t, h, http.MethodPost, "/deploy/links/npm/add-proxy-host", form)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	l, ok := store.links["c1/npm"]
	if !ok {
		t.Fatal("expected link stored after proxy host creation")
	}
	if l.Reference != "app.example.com" {
		t.Fatalf("expected reference app.example.com, got %q", l.Reference)
	}
	if !strings.Contains(w.Body.String(), "Created NPM proxy host 11") {
		t.Fatalf("expected success flash, got: %s", w.Body.String())
	}
}

func TestAddNPMProxyHost_Conflict(t *testing.T) {
	store := newFakeLinkStore()
	h := newLinkTestDeployHandler()
	h.SetServiceClients(store, &fakeKuma{}, &fakeNPM{createErr: npm.ErrDomainConflict})

	form := url.Values{"container": {"c1"}, "domain": {"app.example.com"}}
	w := serveDeployLinks(t, h, http.MethodPost, "/deploy/links/npm/add-proxy-host", form)
	if _, ok := store.links["c1/npm"]; ok {
		t.Fatal("expected no link stored on conflict")
	}
	if !strings.Contains(w.Body.String(), "already in use") {
		t.Fatalf("expected conflict flash, got: %s", w.Body.String())
	}
}

func TestAddNPMProxyHost_MissingDomain(t *testing.T) {
	store := newFakeLinkStore()
	h := newLinkTestDeployHandler()
	h.SetServiceClients(store, &fakeKuma{}, &fakeNPM{})

	form := url.Values{"container": {"c1"}}
	w := serveDeployLinks(t, h, http.MethodPost, "/deploy/links/npm/add-proxy-host", form)
	if !strings.Contains(w.Body.String(), "A domain is required") {
		t.Fatalf("expected domain-required flash, got: %s", w.Body.String())
	}
}

func TestAddNPMProxyHost_Unconfigured(t *testing.T) {
	store := newFakeLinkStore()
	h := newLinkTestDeployHandler()
	h.SetServiceClients(store, &fakeKuma{}, nil)

	form := url.Values{"container": {"c1"}, "domain": {"app.example.com"}}
	w := serveDeployLinks(t, h, http.MethodPost, "/deploy/links/npm/add-proxy-host", form)
	if !strings.Contains(w.Body.String(), "not configured") {
		t.Fatalf("expected 'not configured' flash, got: %s", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// authelia yaml endpoint
// ---------------------------------------------------------------------------

func TestAutheliaYAML(t *testing.T) {
	h := newLinkTestDeployHandler()
	w := serveDeployLinks(t, h, http.MethodGet, "/deploy/links/authelia/yaml?domain=app.example.com", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("expected text/plain content type, got %q", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"app.example.com", "default_policy: deny", "bypass"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected YAML containing %q, got: %s", want, body)
		}
	}
}

func TestAutheliaYAML_MissingDomain(t *testing.T) {
	h := newLinkTestDeployHandler()
	w := serveDeployLinks(t, h, http.MethodGet, "/deploy/links/authelia/yaml", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
