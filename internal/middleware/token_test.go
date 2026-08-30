package middleware

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlens/ent"
	"gitlens/ent/enttest"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

func newTokenTestClient(t *testing.T) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+t.TempDir()+"/test.db?_fk=1")
}

func ginTestEngine(mw gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.GET("/protected", mw, func(c *gin.Context) {
		uid := int64(c.GetInt("user_id"))
		if v := c.GetInt64("user_id"); v != 0 {
			uid = v
		}
		c.String(http.StatusOK, "uid=%d", uid)
	})
	return e
}

func TestGenerateToken_Format(t *testing.T) {
	plain1, hash1, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain1, "glt_") {
		t.Fatalf("expected glt_ prefix, got %q", plain1)
	}
	if hash1 == plain1 || len(hash1) != 64 {
		t.Fatalf("hash must be sha256 hex of plaintext, got %q", hash1)
	}
	if HashToken(plain1) != hash1 {
		t.Fatal("HashToken must derive the stored hash")
	}

	plain2, _, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if plain1 == plain2 {
		t.Fatal("tokens must be unique")
	}
}

func TestVerifyToken_Lifecycle(t *testing.T) {
	client := newTokenTestClient(t)
	ctx := context.Background()

	u := client.User.Create().SetGithubID(1).SetLogin("tokuser").SetAccessToken("t").SaveX(ctx)
	plain, hash, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	tok := client.ApiToken.Create().SetUserID(u.ID).SetName("ci").SetTokenHash(hash).SaveX(ctx)

	id, userID, err := VerifyToken(ctx, client, plain)
	if err != nil {
		t.Fatalf("expected valid token, got %v", err)
	}
	if id != tok.ID || userID != int64(u.ID) {
		t.Fatalf("expected id=%d userID=%d, got %d/%d", tok.ID, u.ID, id, userID)
	}

	// Revoked tokens are rejected.
	client.ApiToken.UpdateOneID(tok.ID).SetRevokedAt(time.Now()).ExecX(ctx)
	if _, _, err := VerifyToken(ctx, client, plain); err == nil {
		t.Fatal("expected revoked token rejection")
	}

	// Expired tokens are rejected.
	plainE, hashE, _ := GenerateToken()
	past := time.Now().Add(-time.Hour)
	client.ApiToken.Create().SetUserID(u.ID).SetName("old").SetTokenHash(hashE).SetExpiresAt(past).SaveX(ctx)
	if _, _, err := VerifyToken(ctx, client, plainE); err == nil {
		t.Fatal("expected expired token rejection")
	}

	// Unknown tokens are rejected.
	plainU, _, _ := GenerateToken()
	if _, _, err := VerifyToken(ctx, client, plainU); err == nil {
		t.Fatal("expected unknown token rejection")
	}
}

func TestRequireToken_Middleware(t *testing.T) {
	client := newTokenTestClient(t)
	ctx := context.Background()

	u := client.User.Create().SetGithubID(2).SetLogin("mw").SetAccessToken("t").SaveX(ctx)
	plain, hash, _ := GenerateToken()
	client.ApiToken.Create().SetUserID(u.ID).SetName("k").SetTokenHash(hash).SaveX(ctx)

	engine := ginTestEngine(RequireToken(client))

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("GET", "/protected", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without header, got %d", w.Code)
	}

	w = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Basic abc")
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for non-bearer scheme, got %d", w.Code)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid bearer, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "uid="+strconv.Itoa(u.ID)) {
		t.Fatalf("expected authenticated principal in response, got %s", w.Body.String())
	}
}

func TestSessionOrToken_Fallback(t *testing.T) {
	client := newTokenTestClient(t)
	ctx := context.Background()

	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/sessions.db?_fk=1")
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStore(db)

	u := client.User.Create().SetGithubID(3).SetLogin("fallback").SetAccessToken("t").SaveX(ctx)
	plain, hash, _ := GenerateToken()
	client.ApiToken.Create().SetUserID(u.ID).SetName("fb").SetTokenHash(hash).SaveX(ctx)

	sessionID := store.Set(int64(u.ID))
	engine := ginTestEngine(SessionOrToken(store, client))

	// Bearer works without a cookie.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 via bearer, got %d", w.Code)
	}

	// Session cookie works without a bearer.
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/protected", nil)
	req.AddCookie(&http.Cookie{Name: "gitlens_session", Value: sessionID})
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 via session cookie, got %d", w.Code)
	}

	// Neither → 401.
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("GET", "/protected", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no credentials, got %d", w.Code)
	}
}
