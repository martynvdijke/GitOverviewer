package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"gitlens/ent"
	"gitlens/ent/apitoken"
)

const bearerPrefix = "Bearer "

// errTokenInvalid is returned uniformly for unknown, revoked, and expired
// tokens so responses never reveal which case occurred.
var errTokenInvalid = errors.New("api token invalid")

// GenerateToken returns a new one-time-visible plaintext token (prefixed
// glt_) and its SHA-256 hex hash for storage.
func GenerateToken() (plain string, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	plain = "glt_" + hex.EncodeToString(b)
	return plain, HashToken(plain), nil
}

// HashToken derives the stored hash for a plaintext token.
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// VerifyToken resolves a plaintext token to (tokenID, ownerUserID). It
// rejects unknown, revoked, and expired tokens uniformly.
func VerifyToken(ctx context.Context, client *ent.Client, plain string) (int, int64, error) {
	h := HashToken(plain)
	t, err := client.ApiToken.Query().
		Where(apitoken.TokenHash(h)).
		Only(ctx)
	if err != nil {
		return 0, 0, err
	}
	if hmac.Equal([]byte(h), []byte(t.TokenHash)) == false {
		return 0, 0, errTokenInvalid
	}
	if t.RevokedAt != nil {
		return 0, 0, errTokenInvalid
	}
	if t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt) {
		return 0, 0, errTokenInvalid
	}
	_, _ = client.ApiToken.UpdateOneID(t.ID).SetLastUsedAt(time.Now()).Save(ctx)
	return t.ID, int64(t.UserID), nil
}

// bearerToken extracts the raw token from the Authorization header.
func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if !strings.HasPrefix(h, bearerPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, bearerPrefix))
}

func abortUnauthorized(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
}

// RequireToken guards machine-facing API mutations: a valid bearer token
// identifying its owner is mandatory; the owner becomes user_id.
func RequireToken(client *ent.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := bearerToken(c)
		if raw == "" {
			abortUnauthorized(c)
			return
		}
		id, userID, err := VerifyToken(c.Request.Context(), client, raw)
		if err != nil {
			abortUnauthorized(c)
			return
		}
		c.Set("user_id", userID)
		c.Set("api_token_id", id)
		c.Next()
	}
}

// SessionOrToken authenticates via bearer token when present, otherwise
// falls back to the browser session cookie. Used by token-management
// endpoints so users can bootstrap their first token from the UI.
func SessionOrToken(store *SessionStore, client *ent.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		if raw := bearerToken(c); raw != "" {
			id, userID, err := VerifyToken(c.Request.Context(), client, raw)
			if err != nil {
				abortUnauthorized(c)
				return
			}
			c.Set("user_id", userID)
			c.Set("api_token_id", id)
			c.Next()
			return
		}
		cookie, err := c.Cookie(sessionCookieName)
		if err != nil {
			abortUnauthorized(c)
			return
		}
		userID, ok := store.Get(cookie)
		if !ok {
			abortUnauthorized(c)
			return
		}
		c.Set("user_id", userID)
		c.Next()
	}
}
