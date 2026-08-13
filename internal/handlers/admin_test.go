package handlers

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlens/ent"
	"gitlens/ent/enttest"
	"gitlens/internal/otel"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

func newTestAdminHandler(t *testing.T) (*AdminHandler, *ent.Client) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
	om := otel.NewManager(client)
	h := NewAdminHandler(client, om)
	return h, client
}

func createAdminUser(t *testing.T, client *ent.Client) int {
	t.Helper()
	u, err := client.User.Create().
		SetGithubID(100).
		SetLogin("admin").
		SetAccessToken("tok").
		SetIsAdmin(true).
		Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func createRegularUser(t *testing.T, client *ent.Client) int {
	t.Helper()
	u, err := client.User.Create().
		SetGithubID(101).
		SetLogin("regular").
		SetAccessToken("tok").
		SetIsAdmin(false).
		Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

var adminTestTmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"printf": fmt.Sprintf,
}).Parse(`
{{define "admin_panel"}}<div>admin_panel {{template "admin_otel_form" .}} {{template "admin_gotify_form" .}} users={{len .Users}} user_id={{.UserID}}</div>{{end}}
{{define "admin_otel_form"}}<div>otel_form endpoint={{if .Config}}{{.Config.OtelEndpoint}}{{end}}</div>{{end}}
{{define "admin_gotify_form"}}<div>gotify_form url={{if .Config}}{{.Config.GotifyURL}}{{end}}</div>{{end}}
{{define "admin_users_tab"}}<div>users_tab {{range .Users}}user={{.Login}}:admin={{.IsAdmin}} {{end}}current={{.UserID}}</div>{{end}}
`))

func adminRequest(h *AdminHandler, userID int64, method, path, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	engine := gin.New()
	engine.SetHTMLTemplate(adminTestTmpl)

	var handlerFn gin.HandlerFunc
	var routeMethod string
	var routePath string

	switch {
	case method == "GET" && path == "/admin":
		handlerFn = h.Index
		routeMethod, routePath = "GET", "/admin"
	case method == "POST" && path == "/admin/otel":
		handlerFn = h.UpdateOTEL
		routeMethod, routePath = "POST", "/admin/otel"
	case method == "POST" && path == "/admin/gotify":
		handlerFn = h.UpdateGotify
		routeMethod, routePath = "POST", "/admin/gotify"
	case method == "GET" && path == "/api/settings":
		handlerFn = h.ListSettings
		routeMethod, routePath = "GET", "/api/settings"
	case method == "GET" && path == "/admin/users":
		handlerFn = h.ListUsers
		routeMethod, routePath = "GET", "/admin/users"
	case method == "POST" && strings.HasPrefix(path, "/admin/users/"):
		handlerFn = h.ToggleAdmin
		routeMethod, routePath = "POST", "/admin/users/:id/toggle-admin"
	}

	wrapper := func(c *gin.Context) {
		c.Set("user_id", userID)
		handlerFn(c)
	}

	switch {
	case routeMethod == "GET":
		engine.GET(routePath, wrapper)
	case routeMethod == "POST":
		engine.POST(routePath, wrapper)
	}

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	engine.ServeHTTP(w, req)
	return w
}

func TestAdminHandler_Index(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	// Create a second user to verify user count in response
	createRegularUser(t, client)

	w := adminRequest(h, int64(adminID), "GET", "/admin", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "admin_panel") {
		t.Fatal("expected admin_panel in response")
	}
	if !strings.Contains(body, "otel_form") {
		t.Fatal("expected otel_form in response")
	}
	if !strings.Contains(body, "users=2") {
		t.Fatal("expected users=2 in response, got:", body)
	}
}

func TestAdminHandler_Index_NoConfig(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	w := adminRequest(h, int64(adminID), "GET", "/admin", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	// Should render without error even without AdminConfig
	if !strings.Contains(w.Body.String(), "admin_panel") {
		t.Fatal("expected admin_panel template to render")
	}
}

func TestAdminHandler_UpdateOTEL_Create(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	body := "otel_endpoint=localhost%3A4318&traces_enabled=on&metrics_enabled=on&logs_enabled=on&log_severity=debug"
	w := adminRequest(h, int64(adminID), "POST", "/admin/otel", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// Verify config was saved
	cfg, err := client.AdminConfig.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("expected AdminConfig to be created: %v", err)
	}
	if cfg.OtelEndpoint != "localhost:4318" {
		t.Fatalf("expected endpoint 'localhost:4318', got '%s'", cfg.OtelEndpoint)
	}
	if !cfg.TracesEnabled {
		t.Fatal("expected TracesEnabled to be true")
	}
	if !cfg.MetricsEnabled {
		t.Fatal("expected MetricsEnabled to be true")
	}
	if !cfg.LogsEnabled {
		t.Fatal("expected LogsEnabled to be true")
	}
	if cfg.LogSeverity != "debug" {
		t.Fatalf("expected log_severity 'debug', got '%s'", cfg.LogSeverity)
	}
}

func TestAdminHandler_UpdateOTEL_Update(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	// Create initial config
	_, err := client.AdminConfig.Create().
		SetID(1).
		SetOtelEndpoint("old:4318").
		SetTracesEnabled(true).
		SetMetricsEnabled(false).
		SetLogsEnabled(false).
		SetLogSeverity("warning").
		Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Update via handler
	body := "otel_endpoint=new%3A4318&traces_enabled=false&log_severity=error"
	w := adminRequest(h, int64(adminID), "POST", "/admin/otel", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// Verify config was updated
	cfg, err := client.AdminConfig.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("expected AdminConfig to exist: %v", err)
	}
	if cfg.OtelEndpoint != "new:4318" {
		t.Fatalf("expected endpoint 'new:4318', got '%s'", cfg.OtelEndpoint)
	}
}

func TestAdminHandler_UpdateOTEL_EmptyEndpoint(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	body := "otel_endpoint=&traces_enabled=false&metrics_enabled=false&logs_enabled=false"
	w := adminRequest(h, int64(adminID), "POST", "/admin/otel", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	configCount, err := client.AdminConfig.Query().Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if configCount != 1 {
		t.Fatalf("expected 1 AdminConfig row, got %d", configCount)
	}
}

func TestAdminHandler_ListUsers(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)
	createRegularUser(t, client)

	w := adminRequest(h, int64(adminID), "GET", "/admin/users", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "users_tab") {
		t.Fatal("expected users_tab in response")
	}
	if !strings.Contains(body, "admin=true") {
		t.Fatalf("expected admin user in response, body: %s", body)
	}
	if !strings.Contains(body, "regular") {
		t.Fatal("expected regular user in response")
	}
}

func TestAdminHandler_ListUsers_Empty(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	// Only the admin exists, should render without error
	w := adminRequest(h, int64(adminID), "GET", "/admin/users", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
}

func TestAdminHandler_ToggleAdmin_Promote(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)
	regularID := createRegularUser(t, client)

	w := adminRequest(h, int64(adminID), "POST", fmt.Sprintf("/admin/users/%d/toggle-admin", regularID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// Verify user was promoted
	u, err := client.User.Get(context.Background(), regularID)
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsAdmin {
		t.Fatal("expected user to be promoted to admin")
	}
}

func TestAdminHandler_ToggleAdmin_Demote(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	// Create another admin
	otherAdmin := createAdminUser2(t, client)

	w := adminRequest(h, int64(adminID), "POST", fmt.Sprintf("/admin/users/%d/toggle-admin", otherAdmin), "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// Verify other admin was demoted
	u, err := client.User.Get(context.Background(), otherAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if u.IsAdmin {
		t.Fatal("expected other admin to be demoted")
	}
}

func TestAdminHandler_UpdateGotify_Create(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	body := "gotify_url=https%3A%2F%2Fgotify.example.com&gotify_token=secret123"
	w := adminRequest(h, int64(adminID), "POST", "/admin/gotify", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "gotify_form") {
		t.Fatal("expected gotify_form in response")
	}

	cfg, err := client.AdminConfig.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("expected AdminConfig to be created: %v", err)
	}
	if cfg.GotifyURL != "https://gotify.example.com" {
		t.Fatalf("expected GotifyURL, got '%s'", cfg.GotifyURL)
	}
	if cfg.GotifyToken != "secret123" {
		t.Fatalf("expected GotifyToken, got '%s'", cfg.GotifyToken)
	}
}

func TestAdminHandler_UpdateGotify_BlankTokenKeepsExisting(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	_, err := client.AdminConfig.Create().
		SetID(1).
		SetGotifyURL("https://old.example.com").
		SetGotifyToken("oldtoken").
		Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Blank token must preserve the previously saved token.
	body := "gotify_url=https%3A%2F%2Fnew.example.com&gotify_token="
	w := adminRequest(h, int64(adminID), "POST", "/admin/gotify", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	cfg, err := client.AdminConfig.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GotifyURL != "https://new.example.com" {
		t.Fatalf("expected updated GotifyURL, got '%s'", cfg.GotifyURL)
	}
	if cfg.GotifyToken != "oldtoken" {
		t.Fatalf("expected token preserved, got '%s'", cfg.GotifyToken)
	}
}

func TestAdminHandler_UpdateGotify_ReloadInvoked(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	reloaded := false
	h.SetGotifyReload(func() { reloaded = true })

	body := "gotify_url=https%3A%2F%2Fgotify.example.com&gotify_token=secret123"
	w := adminRequest(h, int64(adminID), "POST", "/admin/gotify", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !reloaded {
		t.Fatal("expected gotifyReload callback to be invoked after save")
	}
}

func TestAdminHandler_ListSettings_Success(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	_, err := client.AdminConfig.Create().
		SetID(1).
		SetOtelEndpoint("collector:4318").
		SetTracesEnabled(true).
		SetMetricsEnabled(false).
		SetLogsEnabled(true).
		SetLogSeverity("debug").
		SetGotifyURL("https://gotify.example.com").
		SetGotifyToken("secret123").
		Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	w := adminRequest(h, int64(adminID), "GET", "/api/settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	for _, want := range []string{
		`"otel_endpoint":"collector:4318"`,
		`"traces_enabled":true`,
		`"metrics_enabled":false`,
		`"logs_enabled":true`,
		`"log_severity":"debug"`,
		`"gotify_url":"https://gotify.example.com"`,
		`"gotify_configured":true`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %s in body, got: %s", want, body)
		}
	}
	if strings.Contains(body, "secret123") || strings.Contains(body, "gotify_token") {
		t.Fatalf("settings API must not expose the gotify token, got: %s", body)
	}
}

func TestAdminHandler_ListSettings_NoConfig(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	w := adminRequest(h, int64(adminID), "GET", "/api/settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	for _, want := range []string{
		`"otel_endpoint":""`,
		`"traces_enabled":false`,
		`"log_severity":"warning"`,
		`"gotify_configured":false`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %s in body, got: %s", want, body)
		}
	}
}

func TestAdminHandler_ListSettings_TokenNotConfigured(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	// Config exists but no gotify token saved yet.
	_, err := client.AdminConfig.Create().
		SetID(1).
		SetGotifyURL("https://gotify.example.com").
		Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	w := adminRequest(h, int64(adminID), "GET", "/api/settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, `"gotify_url":"https://gotify.example.com"`) {
		t.Fatalf("expected gotify_url in body, got: %s", body)
	}
	if !strings.Contains(body, `"gotify_configured":false`) {
		t.Fatalf("expected gotify_configured:false, got: %s", body)
	}
}

func createAdminUser2(t *testing.T, client *ent.Client) int {
	t.Helper()
	u, err := client.User.Create().
		SetGithubID(102).
		SetLogin("admin2").
		SetAccessToken("tok").
		SetIsAdmin(true).
		Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestAdminHandler_ToggleAdmin_PreventsSelfDemotion(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	w := adminRequest(h, int64(adminID), "POST", fmt.Sprintf("/admin/users/%d/toggle-admin", adminID), "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d, body: %s", w.Code, w.Body.String())
	}

	// Verify admin still has admin status
	u, err := client.User.Get(context.Background(), adminID)
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsAdmin {
		t.Fatal("expected admin to still have admin status")
	}
}

func TestAdminHandler_ToggleAdmin_InvalidID(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	w := adminRequest(h, int64(adminID), "POST", "/admin/users/notanumber/toggle-admin", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body: %s", w.Code, w.Body.String())
	}
}

func TestAdminHandler_ToggleAdmin_NonexistentUser(t *testing.T) {
	h, client := newTestAdminHandler(t)
	adminID := createAdminUser(t, client)

	w := adminRequest(h, int64(adminID), "POST", "/admin/users/99999/toggle-admin", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d, body: %s", w.Code, w.Body.String())
	}
}
