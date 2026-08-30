package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlens/ent"
	"gitlens/ent/enttest"
	"gitlens/internal/middleware"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

func newApiTokenTestServer(t *testing.T) (*gin.Engine, *ent.Client, *middleware.SessionStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	client := enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/sessions.db?_fk=1")
	if err != nil {
		t.Fatal(err)
	}
	store := middleware.NewSessionStore(db)

	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"formatTime": func(t time.Time) string { return "t" },
	}).Parse(`
{{define "tokens_list"}}<div class="tokens">{{range .ApiTokens}}<span class="tok">{{.Name}}</span>{{end}}</div>{{end}}
{{define "token_created"}}<div class="secret">{{.Secret}}|{{.Name}}</div>{{end}}
`))

	engine := gin.New()
	engine.SetHTMLTemplate(tmpl)
	h := NewApiTokenHandler(client)
	auth := engine.Group("/")
	auth.Use(middleware.SessionOrToken(store, client))
	{
		auth.POST("/api/tokens", h.Create)
		auth.GET("/api/tokens", h.List)
		auth.DELETE("/api/tokens/:id", h.Revoke)
		auth.POST("/api/tokens/:id/rotate", h.Rotate)
	}
	return engine, client, store
}

func seedTokenUser(t *testing.T, client *ent.Client, githubID int64, login string) *ent.User {
	t.Helper()
	u, err := client.User.Create().
		SetGithubID(githubID).
		SetLogin(login).
		SetAccessToken("tok").
		Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func tokenRequest(engine *gin.Engine, store *middleware.SessionStore, userID int64, method, path, body string, hx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" && method == "POST" && strings.HasPrefix(body, "{") {
		req.Header.Set("Content-Type", "application/json")
	}
	if body != "" && method == "POST" && !strings.HasPrefix(body, "{") {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	if userID != 0 {
		req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: store.Set(userID)})
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

func TestApiTokens_Create_ShowsSecretOnce(t *testing.T) {
	engine, client, store := newApiTokenTestServer(t)
	u := seedTokenUser(t, client, 10, "alice")

	w := tokenRequest(engine, store, int64(u.ID), "POST", "/api/tokens", `{"name":"ci-bot"}`, false)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		ID    int    `json:"id"`
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Token, "glt_") || resp.Name != "ci-bot" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	// Stored value must be a hash, never the plaintext.
	row, err := client.ApiToken.Get(context.Background(), resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.TokenHash == resp.Token || row.TokenHash == "" {
		t.Fatal("stored TokenHash must not equal plaintext secret")
	}

	// Listing must not contain the secret.
	w = tokenRequest(engine, store, int64(u.ID), "GET", "/api/tokens", "", false)
	if strings.Contains(w.Body.String(), resp.Token) || strings.Contains(w.Body.String(), "hash") {
		t.Fatalf("list leaked secret material: %s", w.Body.String())
	}
}

func TestApiTokens_Create_FormHX(t *testing.T) {
	engine, client, store := newApiTokenTestServer(t)
	u := seedTokenUser(t, client, 11, "bob")

	w := tokenRequest(engine, store, int64(u.ID), "POST", "/api/tokens", "name=deploy-key", true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "glt_") {
		t.Fatalf("expected HX partial with secret, got %d: %s", w.Code, w.Body.String())
	}
}

func TestApiTokens_Create_Validation(t *testing.T) {
	engine, client, store := newApiTokenTestServer(t)
	u := seedTokenUser(t, client, 12, "carol")

	for name, body := range map[string]string{
		"empty":    `{"name":"  "}`,
		"too-long": `{"name":"` + strings.Repeat("x", 101) + `"}`,
	} {
		w := tokenRequest(engine, store, int64(u.ID), "POST", "/api/tokens", body, false)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", name, w.Code)
		}
	}
}

func TestApiTokens_Revoke_CrossUserAndOwner(t *testing.T) {
	engine, client, store := newApiTokenTestServer(t)
	ctx := context.Background()
	alice := seedTokenUser(t, client, 13, "alice2")
	mallory := seedTokenUser(t, client, 14, "mallory")

	plain, hash, _ := middleware.GenerateToken()
	tok := client.ApiToken.Create().SetUserID(alice.ID).SetName("k").SetTokenHash(hash).SaveX(ctx)

	// Cross-user revoke is indistinguishable from missing.
	w := tokenRequest(engine, store, int64(mallory.ID), "DELETE", "/api/tokens/"+strconv.Itoa(tok.ID), "", false)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-user revoke, got %d", w.Code)
	}

	// Owner revoke succeeds and invalidates the token.
	w = tokenRequest(engine, store, int64(alice.ID), "DELETE", "/api/tokens/"+strconv.Itoa(tok.ID), "", false)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on owner revoke, got %d: %s", w.Code, w.Body.String())
	}
	if _, _, err := middleware.VerifyToken(ctx, client, plain); err == nil {
		t.Fatal("revoked token must stop authenticating")
	}
}

func TestApiTokens_Rotate_InvalidatesOld(t *testing.T) {
	engine, client, store := newApiTokenTestServer(t)
	ctx := context.Background()
	u := seedTokenUser(t, client, 15, "dave")

	plain, hash, _ := middleware.GenerateToken()
	tok := client.ApiToken.Create().SetUserID(u.ID).SetName("rot").SetTokenHash(hash).SaveX(ctx)

	w := tokenRequest(engine, store, int64(u.ID), "POST", "/api/tokens/"+strconv.Itoa(tok.ID)+"/rotate", "", false)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token == plain || !strings.HasPrefix(resp.Token, "glt_") {
		t.Fatalf("expected fresh distinct secret, got %q", resp.Token)
	}
	if _, _, err := middleware.VerifyToken(ctx, client, plain); err == nil {
		t.Fatal("old secret must stop working after rotate")
	}
	if _, _, err := middleware.VerifyToken(ctx, client, resp.Token); err != nil {
		t.Fatalf("new secret must authenticate: %v", err)
	}
}
