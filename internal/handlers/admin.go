package handlers

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"gitlens/ent"
	"gitlens/internal/otel"

	"github.com/gin-gonic/gin"
)

type AdminHandler struct {
	client      *ent.Client
	otelManager *otel.Manager
	// gotifyReload re-reads Gotify settings from the DB and re-configures
	// the runtime notifier. Set by main; nil in tests.
	gotifyReload func()
}

func NewAdminHandler(client *ent.Client, otelManager *otel.Manager) *AdminHandler {
	return &AdminHandler{client: client, otelManager: otelManager}
}

// SetGotifyReload registers a callback invoked after Gotify settings are
// saved so the runtime notifier picks up changes without a restart.
func (h *AdminHandler) SetGotifyReload(fn func()) {
	h.gotifyReload = fn
}

// Index renders the admin panel page with the OTEL config form and user list.
func (h *AdminHandler) Index(c *gin.Context) {
	ctx := c.Request.Context()

	cfg, err := h.client.AdminConfig.Get(ctx, 1)
	if err != nil && !ent.IsNotFound(err) {
		log.Printf("admin: error loading config: %v", err)
	}

	users, _ := h.client.User.Query().All(ctx)
	userID := c.GetInt64("user_id")

	c.HTML(http.StatusOK, "admin_panel", gin.H{
		"Config": cfg,
		"Users":  users,
		"UserID": userID,
	})
}

// UpdateOTEL parses the OTEL settings form, saves to DB, and reloads providers.
func (h *AdminHandler) UpdateOTEL(c *gin.Context) {
	ctx := c.Request.Context()

	endpoint := c.PostForm("otel_endpoint")
	traces := c.PostForm("traces_enabled") == "on"
	metrics := c.PostForm("metrics_enabled") == "on"
	logs := c.PostForm("logs_enabled") == "on"
	logSeverity := c.DefaultPostForm("log_severity", "warning")

	_, err := h.client.AdminConfig.Get(ctx, 1)
	if ent.IsNotFound(err) {
		// Create singleton row
		_, err = h.client.AdminConfig.Create().
			SetID(1).
			SetOtelEndpoint(endpoint).
			SetTracesEnabled(traces).
			SetMetricsEnabled(metrics).
			SetLogsEnabled(logs).
			SetLogSeverity(logSeverity).
			Save(ctx)
	} else if err == nil {
		// Update existing
		_, err = h.client.AdminConfig.UpdateOneID(1).
			SetOtelEndpoint(endpoint).
			SetTracesEnabled(traces).
			SetMetricsEnabled(metrics).
			SetLogsEnabled(logs).
			SetLogSeverity(logSeverity).
			Save(ctx)
	}
	if err != nil {
		log.Printf("admin: error saving OTEL config: %v", err)
		c.String(http.StatusInternalServerError, "Failed to save OTEL configuration")
		return
	}

	// Reload OTEL providers with new config
	if err := h.otelManager.Reload(ctx); err != nil {
		log.Printf("admin: error reloading OTEL: %v", err)
		c.String(http.StatusOK, "Config saved, but OTEL reload had an error: "+err.Error())
		return
	}

	// Render the updated form partial
	h.renderOTELForm(c)
}

// ListUsers renders the users table partial (HTMX).
func (h *AdminHandler) ListUsers(c *gin.Context) {
	users, err := h.client.User.Query().All(c.Request.Context())
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to load users")
		return
	}
	userID := c.GetInt64("user_id")
	c.HTML(http.StatusOK, "admin_users_tab", gin.H{
		"Users":  users,
		"UserID": userID,
	})
}

// ToggleAdmin promotes or demotes a user. Prevents self-demotion.
func (h *AdminHandler) ToggleAdmin(c *gin.Context) {
	ctx := c.Request.Context()
	currentUserID := c.GetInt64("user_id")
	targetIDStr := c.Param("id")
	targetID, err := strconv.Atoi(targetIDStr)
	if err != nil {
		c.String(http.StatusBadRequest, "Invalid user ID")
		return
	}

	if targetID == int(currentUserID) {
		c.String(http.StatusForbidden, "You cannot change your own admin status")
		return
	}

	u, err := h.client.User.Get(ctx, targetID)
	if err != nil {
		c.String(http.StatusNotFound, "User not found")
		return
	}

	_, err = h.client.User.UpdateOneID(targetID).
		SetIsAdmin(!u.IsAdmin).
		Save(ctx)
	if err != nil {
		log.Printf("admin: error toggling admin for user %d: %v", targetID, err)
		c.String(http.StatusInternalServerError, "Failed to update user")
		return
	}

	// Re-render user rows partial
	h.renderUserRows(c)
}

// ListSettings returns the admin configuration (OTEL + Gotify) as JSON.
// The Gotify token is never returned; only a gotify_configured flag.
func (h *AdminHandler) ListSettings(c *gin.Context) {
	ctx := c.Request.Context()

	cfg, err := h.client.AdminConfig.Get(ctx, 1)
	if err != nil && !ent.IsNotFound(err) {
		log.Printf("admin: error loading config for API: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load settings"})
		return
	}

	resp := gin.H{
		"otel_endpoint":     "",
		"traces_enabled":    false,
		"metrics_enabled":   false,
		"logs_enabled":      false,
		"log_severity":      "warning",
		"gotify_url":        "",
		"gotify_configured": false,
	}
	if cfg != nil {
		resp = gin.H{
			"otel_endpoint":     cfg.OtelEndpoint,
			"traces_enabled":    cfg.TracesEnabled,
			"metrics_enabled":   cfg.MetricsEnabled,
			"logs_enabled":      cfg.LogsEnabled,
			"log_severity":      cfg.LogSeverity,
			"gotify_url":        cfg.GotifyURL,
			"gotify_configured": cfg.GotifyToken != "",
		}
	}
	c.JSON(http.StatusOK, resp)
}

// UpdateGotify saves the Gotify URL/token from the admin form, reloads the
// runtime notifier, and re-renders the form. A blank token keeps the
// previously saved token.
func (h *AdminHandler) UpdateGotify(c *gin.Context) {
	ctx := c.Request.Context()

	url := strings.TrimSpace(c.PostForm("gotify_url"))
	token := strings.TrimSpace(c.PostForm("gotify_token"))

	_, err := h.client.AdminConfig.Get(ctx, 1)
	if ent.IsNotFound(err) {
		_, err = h.client.AdminConfig.Create().
			SetID(1).
			SetGotifyURL(url).
			SetGotifyToken(token).
			Save(ctx)
	} else if err == nil {
		u := h.client.AdminConfig.UpdateOneID(1).SetGotifyURL(url)
		if token != "" {
			u = u.SetGotifyToken(token)
		}
		_, err = u.Save(ctx)
	}
	if err != nil {
		log.Printf("admin: error saving Gotify config: %v", err)
		c.String(http.StatusInternalServerError, "Failed to save Gotify configuration")
		return
	}

	if h.gotifyReload != nil {
		h.gotifyReload()
	}

	h.renderGotifyForm(c)
}

// ---- partial render helpers ----

func (h *AdminHandler) renderOTELForm(c *gin.Context) {
	ctx := c.Request.Context()
	cfg, err := h.client.AdminConfig.Get(ctx, 1)
	if err != nil && !ent.IsNotFound(err) {
		log.Printf("admin: error loading config for form: %v", err)
	}
	c.HTML(http.StatusOK, "admin_otel_form", gin.H{"Config": cfg})
}

func (h *AdminHandler) renderGotifyForm(c *gin.Context) {
	ctx := c.Request.Context()
	cfg, err := h.client.AdminConfig.Get(ctx, 1)
	if err != nil && !ent.IsNotFound(err) {
		log.Printf("admin: error loading config for gotify form: %v", err)
	}
	c.HTML(http.StatusOK, "admin_gotify_form", gin.H{"Config": cfg})
}

func (h *AdminHandler) renderUserRows(c *gin.Context) {
	users, _ := h.client.User.Query().All(c.Request.Context())
	userID := c.GetInt64("user_id")
	c.HTML(http.StatusOK, "admin_users_tab", gin.H{
		"Users":  users,
		"UserID": userID,
	})
}
