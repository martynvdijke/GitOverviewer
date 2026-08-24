package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitlens/ent"
	"gitlens/ent/apitoken"
	"gitlens/internal/middleware"

	"github.com/gin-gonic/gin"
)

// ApiTokenHandler serves personal API token management: create, list,
// revoke, and rotate. Secrets are returned exactly once at creation or
// rotation; only metadata is ever listed afterwards.
type ApiTokenHandler struct {
	client *ent.Client
}

func NewApiTokenHandler(client *ent.Client) *ApiTokenHandler {
	return &ApiTokenHandler{client: client}
}

func isHXRequest(c *gin.Context) bool {
	return c.GetHeader("HX-Request") == "true"
}

// listOwn returns the caller's active (non-revoked) tokens, newest first.
func (h *ApiTokenHandler) listOwn(c *gin.Context, userID int) ([]*ent.ApiToken, error) {
	return h.client.ApiToken.Query().
		Where(
			apitoken.UserIDEQ(userID),
			apitoken.RevokedAtIsNil(),
		).
		Order(ent.Desc(apitoken.FieldCreatedAt)).
		All(c.Request.Context())
}

// Create mints a new token for the authenticated user. The plaintext secret
// is shown once (HTML partial for HTMX, JSON otherwise) and never again.
func (h *ApiTokenHandler) Create(c *gin.Context) {
	userID := int(c.GetInt64("user_id"))

	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" && c.Request.Body != nil {
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(c.Request.Body).Decode(&body); err == nil {
			name = strings.TrimSpace(body.Name)
		}
	}
	if len(name) < 1 || len(name) > 100 {
		c.String(http.StatusBadRequest, "token name must be 1-100 characters")
		return
	}

	plain, hash, err := middleware.GenerateToken()
	if err != nil {
		log.Printf("api_tokens: generate: %v", err)
		c.String(http.StatusInternalServerError, "failed to generate token")
		return
	}

	tok, err := h.client.ApiToken.Create().
		SetUserID(userID).
		SetName(name).
		SetTokenHash(hash).
		Save(c.Request.Context())
	if err != nil {
		log.Printf("api_tokens: create: %v", err)
		c.String(http.StatusInternalServerError, "failed to save token")
		return
	}

	if isHXRequest(c) {
		c.HTML(http.StatusOK, "token_created", gin.H{"Secret": plain, "Name": tok.Name})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": tok.ID, "name": tok.Name, "token": plain})
}

// List returns the caller's tokens as metadata only — never secrets.
func (h *ApiTokenHandler) List(c *gin.Context) {
	userID := int(c.GetInt64("user_id"))

	tokens, err := h.listOwn(c, userID)
	if err != nil {
		log.Printf("api_tokens: list: %v", err)
		c.String(http.StatusInternalServerError, "failed to list tokens")
		return
	}

	if isHXRequest(c) {
		c.HTML(http.StatusOK, "tokens_list", gin.H{"ApiTokens": tokens})
		return
	}
	out := make([]gin.H, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, gin.H{
			"id":           t.ID,
			"name":         t.Name,
			"created_at":   t.CreatedAt,
			"last_used_at": t.LastUsedAt,
			"expires_at":   t.ExpiresAt,
		})
	}
	c.JSON(http.StatusOK, out)
}

// loadOwned fetches a non-revoked token owned by userID; missing and
// cross-user lookups are indistinguishable (404 both).
func (h *ApiTokenHandler) loadOwned(c *gin.Context, userID int) (*ent.ApiToken, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return nil, false
	}
	tok, err := h.client.ApiToken.Query().
		Where(
			apitoken.IDEQ(id),
			apitoken.UserIDEQ(userID),
			apitoken.RevokedAtIsNil(),
		).
		Only(c.Request.Context())
	if err != nil {
		return nil, false
	}
	return tok, true
}

// Revoke disables a token immediately. The hash stays for audit but the
// token can no longer authenticate.
func (h *ApiTokenHandler) Revoke(c *gin.Context) {
	userID := int(c.GetInt64("user_id"))
	tok, ok := h.loadOwned(c, userID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	now := time.Now()
	if _, err := h.client.ApiToken.UpdateOneID(tok.ID).
		SetRevokedAt(now).
		Save(c.Request.Context()); err != nil {
		log.Printf("api_tokens: revoke: %v", err)
		c.String(http.StatusInternalServerError, "failed to revoke token")
		return
	}

	if isHXRequest(c) {
		tokens, _ := h.listOwn(c, userID)
		c.HTML(http.StatusOK, "tokens_list", gin.H{"ApiTokens": tokens})
		return
	}
	c.Status(http.StatusNoContent)
}

// Rotate revokes a token and issues a replacement with the same name in one
// transaction. The new plaintext is shown once; the old secret stops working.
func (h *ApiTokenHandler) Rotate(c *gin.Context) {
	userID := int(c.GetInt64("user_id"))
	tok, ok := h.loadOwned(c, userID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	plain, hash, err := middleware.GenerateToken()
	if err != nil {
		log.Printf("api_tokens: generate: %v", err)
		c.String(http.StatusInternalServerError, "failed to generate token")
		return
	}

	tx, err := h.client.Tx(c.Request.Context())
	if err != nil {
		log.Printf("api_tokens: tx: %v", err)
		c.String(http.StatusInternalServerError, "failed to rotate token")
		return
	}
	now := time.Now()
	if _, err := tx.ApiToken.UpdateOneID(tok.ID).
		SetRevokedAt(now).
		Save(c.Request.Context()); err != nil {
		tx.Rollback()
		log.Printf("api_tokens: rotate revoke: %v", err)
		c.String(http.StatusInternalServerError, "failed to rotate token")
		return
	}
	created, err := tx.ApiToken.Create().
		SetUserID(userID).
		SetName(tok.Name).
		SetTokenHash(hash).
		Save(c.Request.Context())
	if err != nil {
		tx.Rollback()
		log.Printf("api_tokens: rotate create: %v", err)
		c.String(http.StatusInternalServerError, "failed to rotate token")
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("api_tokens: rotate commit: %v", err)
		c.String(http.StatusInternalServerError, "failed to rotate token")
		return
	}

	if isHXRequest(c) {
		c.HTML(http.StatusOK, "token_created", gin.H{"Secret": plain, "Name": created.Name})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": created.ID, "name": created.Name, "token": plain})
}
