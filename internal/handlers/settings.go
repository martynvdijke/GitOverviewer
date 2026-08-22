package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"gitlens/ent"
	"gitlens/ent/repository"
	"gitlens/ent/user"
	"gitlens/internal/github"
	mw "gitlens/internal/middleware"
	"gitlens/internal/provider"
	"gitlens/internal/sync"

	"github.com/gin-gonic/gin"
)

type SettingsHandler struct {
	client    *ent.Client
	store     *mw.SessionStore
	gh        *github.Client
	providers map[string]provider.Provider
	syncer    *sync.Syncer
	bgCtx     context.Context
}

func NewSettingsHandler(client *ent.Client, store *mw.SessionStore, gh *github.Client, providers map[string]provider.Provider, syncer *sync.Syncer) *SettingsHandler {
	return &SettingsHandler{client: client, store: store, gh: gh, providers: providers, syncer: syncer, bgCtx: context.Background()}
}

func (h *SettingsHandler) Index(c *gin.Context) {
	userID := c.GetInt64("user_id")
	u, err := h.client.User.Get(c.Request.Context(), int(userID))
	if err != nil {
		c.String(http.StatusInternalServerError, "User not found")
		return
	}

	repos, _ := h.client.Repository.Query().
		Where(repository.HasUserWith(user.ID(u.ID))).
		Order(ent.Desc(repository.FieldUpdatedAt)).
		All(c.Request.Context())

	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	webhookURL := scheme + "://" + c.Request.Host + "/webhook/github"

	forgejoWarning := h.computeForgejoWarning(c.Request.Context(), u, repos)

	data := gin.H{
		"User":           u,
		"Repos":          repos,
		"WebhookURL":     webhookURL,
		"ForgejoWarning": forgejoWarning,
	}

	// Admins also see the instance-wide settings (OTEL, Gotify, Users) on
	// this page, so load the admin config and user list for them.
	if u.IsAdmin {
		cfg, err := h.client.AdminConfig.Get(c.Request.Context(), 1)
		if err != nil && !ent.IsNotFound(err) {
			log.Printf("settings: error loading admin config: %v", err)
		}
		users, _ := h.client.User.Query().All(c.Request.Context())
		data["Config"] = cfg
		data["Users"] = users
		data["UserID"] = userID
	}

	c.HTML(http.StatusOK, "settings", data)
}

func (h *SettingsHandler) UpdateInterval(c *gin.Context) {
	userID := c.GetInt64("user_id")
	minutesStr := c.PostForm("interval")
	minutes, err := strconv.Atoi(minutesStr)
	if err != nil || minutes < 1 || minutes > 1440 {
		c.String(http.StatusBadRequest, "Invalid interval (1-1440 minutes)")
		return
	}

	_, err = h.client.User.UpdateOneID(int(userID)).
		SetSyncIntervalMinutes(minutes).
		Save(c.Request.Context())
	if err != nil {
		log.Printf("Error updating interval: %v", err)
		c.String(http.StatusInternalServerError, "Failed to update interval")
		return
	}

	c.String(http.StatusOK, "Sync interval updated to %d minutes", minutes)
}

func (h *SettingsHandler) UpdateUmami(c *gin.Context) {
	userID := c.GetInt64("user_id")
	umamiURL := c.PostForm("umami_url")
	umamiSiteID := c.PostForm("umami_site_id")

	// Both or none — clear config if both empty
	if umamiURL == "" && umamiSiteID == "" {
		_, err := h.client.User.UpdateOneID(int(userID)).
			ClearUmamiURL().
			ClearUmamiSiteID().
			Save(c.Request.Context())
		if err != nil {
			log.Printf("Error clearing umami config: %v", err)
			c.String(http.StatusInternalServerError, "Failed to clear Umami configuration")
			return
		}
		c.String(http.StatusOK, "Umami analytics configuration cleared")
		return
	}

	// Partial input is not allowed
	if umamiURL == "" || umamiSiteID == "" {
		c.String(http.StatusBadRequest, "Both Umami URL and Site ID are required to enable analytics")
		return
	}

	_, err := h.client.User.UpdateOneID(int(userID)).
		SetUmamiURL(umamiURL).
		SetUmamiSiteID(umamiSiteID).
		Save(c.Request.Context())
	if err != nil {
		log.Printf("Error updating umami config: %v", err)
		c.String(http.StatusInternalServerError, "Failed to update Umami configuration")
		return
	}

	c.String(http.StatusOK, "Umami analytics configuration saved")
}

func (h *SettingsHandler) AvailableRepos(c *gin.Context) {
	userID := c.GetInt64("user_id")
	u, err := h.client.User.Get(c.Request.Context(), int(userID))
	if err != nil {
		c.String(http.StatusInternalServerError, "User not found")
		return
	}

	ghRepos, err := h.gh.ListRepositories(u.AccessToken)
	if err != nil {
		log.Printf("Error listing GitHub repos: %v", err)
		c.HTML(http.StatusInternalServerError, "available_repos", gin.H{"Error": "Failed to fetch repositories from GitHub"})
		return
	}

	trackedIDs := make(map[int64]bool)
	tracked, _ := h.client.Repository.Query().
		Where(repository.HasUserWith(user.ID(u.ID))).
		All(c.Request.Context())
	for _, r := range tracked {
		trackedIDs[r.GithubID] = true
	}

	var untracked []*github.Repository
	for _, r := range ghRepos {
		if !trackedIDs[r.ID] {
			untracked = append(untracked, r)
		}
	}

	c.HTML(http.StatusOK, "available_repos", gin.H{
		"Repos": untracked,
	})
}

func (h *SettingsHandler) SelectRepos(c *gin.Context) {
	userID := c.GetInt64("user_id")
	u, err := h.client.User.Get(c.Request.Context(), int(userID))
	if err != nil {
		c.String(http.StatusInternalServerError, "User not found")
		return
	}

	ids := c.PostFormArray("repo_ids")
	if len(ids) == 0 {
		c.String(http.StatusBadRequest, "No repositories selected")
		return
	}

	ghRepos, err := h.gh.ListRepositories(u.AccessToken)
	if err != nil {
		log.Printf("Error listing repos: %v", err)
		c.String(http.StatusInternalServerError, "Failed to fetch repositories")
		return
	}

	selected := make(map[int64]*github.Repository)
	for _, r := range ghRepos {
		for _, idStr := range ids {
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				continue
			}
			if r.ID == id {
				selected[id] = r
				break
			}
		}
	}

	ctx := c.Request.Context()
	var newIDs []int
	for _, r := range selected {
		exists, _ := h.client.Repository.Query().
			Where(
				repository.HasUserWith(user.ID(u.ID)),
				repository.GithubID(r.ID),
			).
			Exist(ctx)
		if exists {
			continue
		}

		repo, err := h.client.Repository.Create().
			SetGithubID(r.ID).
			SetOwner(r.Owner).
			SetName(r.Name).
			SetFullName(r.FullName).
			SetDescription(r.Description).
			SetHTMLURL(r.HTMLURL).
			SetLanguage(r.Language).
			SetDefaultBranch(r.DefaultBranch).
			SetUserID(u.ID).
			Save(ctx)
		if err != nil {
			log.Printf("Error saving repo %s: %v", r.FullName, err)
		} else {
			log.Printf("Added repo %s", r.FullName)
			newIDs = append(newIDs, repo.ID)
		}
	}

	// Sync new repos in background so the HTTP request returns quickly.
	// WebSocket broadcasts push updated repo cards as each sync completes.
	go func() {
		for _, id := range newIDs {
			r, err := h.client.Repository.Get(h.bgCtx, id)
			if err != nil {
				log.Printf("Error fetching new repo ID %d: %v", id, err)
				continue
			}
			h.syncer.SyncOne(h.bgCtx, r)
		}
	}()

	repos, _ := h.client.Repository.Query().
		Where(repository.HasUserWith(user.ID(u.ID))).
		Order(ent.Desc(repository.FieldUpdatedAt)).
		All(ctx)

	c.HTML(http.StatusOK, "repos_tab", gin.H{
		"User":       u,
		"Repos":      repos,
		"Metrics":    computeMetrics(repos),
		"ActiveTab":  "repos",
		"Sort":       "",
		"Query":      "",
		"Filters":    repoFilters{},
		"HasFilters": false,
	})
}

func (h *SettingsHandler) RemoveRepo(c *gin.Context) {
	userID := c.GetInt64("user_id")
	repoID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.String(http.StatusBadRequest, "Invalid repo ID")
		return
	}

	ctx := c.Request.Context()
	n, err := h.client.Repository.Delete().
		Where(
			repository.ID(repoID),
			repository.HasUserWith(user.ID(int(userID))),
		).
		Exec(ctx)
	if err != nil {
		log.Printf("Error removing repo %d: %v", repoID, err)
		c.String(http.StatusInternalServerError, "Failed to remove repository")
		return
	}
	if n == 0 {
		c.String(http.StatusNotFound, "Repository not found")
		return
	}

	c.Status(http.StatusOK)
}

// ForgejoWarningData is template data for the cross-provider warning banner.
type ForgejoWarningData struct {
	Show         bool
	MissingRepos []string
	ForgejoURL   string
	Capped       bool
	TotalMissing int
}

// computeForgejoWarning checks whether the user has GitHub-tracked repos that
// have no counterpart on their connected Forgejo instance.
func (h *SettingsHandler) computeForgejoWarning(ctx context.Context, u *ent.User, repos []*ent.Repository) *ForgejoWarningData {
	w := &ForgejoWarningData{}
	if u.ForgejoID == 0 {
		return w
	}

	var githubFullNames, forgejoFullNames []string
	for _, r := range repos {
		switch r.Provider {
		case "github":
			githubFullNames = append(githubFullNames, strings.ToLower(r.FullName))
		case "forgejo":
			if r.ForgejoFullName != "" {
				forgejoFullNames = append(forgejoFullNames, strings.ToLower(r.ForgejoFullName))
			}
		}
	}

	if len(githubFullNames) == 0 || len(forgejoFullNames) == 0 {
		return w
	}

	dismissed := make(map[string]bool)
	if u.DismissedForgejoWarningFor != "" {
		var list []string
		if err := json.Unmarshal([]byte(u.DismissedForgejoWarningFor), &list); err == nil {
			for _, n := range list {
				dismissed[strings.ToLower(n)] = true
			}
		}
	}

	forgejoSet := make(map[string]bool)
	for _, n := range forgejoFullNames {
		forgejoSet[n] = true
	}

	var missing []string
	for _, n := range githubFullNames {
		if !forgejoSet[n] && !dismissed[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return w
	}

	w.Show = true
	w.ForgejoURL = u.ForgejoURL
	w.TotalMissing = len(missing)
	if len(missing) > 10 {
		w.MissingRepos = missing[:10]
		w.Capped = true
	} else {
		w.MissingRepos = missing
	}
	return w
}

// UpdateEinkMode toggles e-ink mode for the current user.
func (h *SettingsHandler) UpdateEinkMode(c *gin.Context) {
	userID := c.GetInt64("user_id")
	enabled := c.PostForm("enabled") == "true"

	_, err := h.client.User.UpdateOneID(int(userID)).
		SetEinkMode(enabled).
		Save(c.Request.Context())
	if err != nil {
		log.Printf("Error updating e-ink mode: %v", err)
		c.String(http.StatusInternalServerError, "Failed to update e-ink mode")
		return
	}

	// Return HTML (not plain text) so HTMX executes the inline script,
	// toggling the eink-mode class on <html> without a full page reload.
	state := map[bool]string{true: "enabled", false: "disabled"}[enabled]
	op := map[bool]string{true: "add", false: "remove"}[enabled]
	body := fmt.Sprintf("E-ink mode %s<script>document.documentElement.classList.%s('eink-mode')</script>", state, op)
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(body))
}

// DisconnectForgejo clears all forgejo fields on the user.
func (h *SettingsHandler) DisconnectForgejo(c *gin.Context) {
	userID := c.GetInt64("user_id")
	_, err := h.client.User.UpdateOneID(int(userID)).
		ClearForgejoID().
		ClearForgejoLogin().
		ClearForgejoAvatarURL().
		ClearForgejoName().
		ClearForgejoAccessToken().
		ClearForgejoURL().
		Save(c.Request.Context())
	if err != nil {
		log.Printf("Error disconnecting Forgejo: %v", err)
		c.String(http.StatusInternalServerError, "Failed to disconnect Forgejo")
		return
	}
	c.String(http.StatusOK, "Forgejo disconnected")
}

// AvailableForgejoRepos lists untracked repos from the user's Forgejo account.
func (h *SettingsHandler) AvailableForgejoRepos(c *gin.Context) {
	userID := c.GetInt64("user_id")
	u, err := h.client.User.Get(c.Request.Context(), int(userID))
	if err != nil {
		c.String(http.StatusInternalServerError, "User not found")
		return
	}
	if u.ForgejoID == 0 || u.ForgejoAccessToken == "" {
		c.HTML(http.StatusOK, "available_repos", gin.H{
			"NoForgejo":  true,
			"ForgejoURL": os.Getenv("FORGEJO_DEFAULT_URL"),
		})
		return
	}

	p, ok := h.providers["forgejo"]
	if !ok || p == nil {
		c.HTML(http.StatusInternalServerError, "available_repos", gin.H{"Error": "Forgejo provider not configured"})
		return
	}

	fjRepos, err := p.ListRepositories(c.Request.Context(), u.ForgejoAccessToken)
	if err != nil {
		log.Printf("Error listing Forgejo repos: %v", err)
		c.HTML(http.StatusInternalServerError, "available_repos", gin.H{"Error": "Failed to fetch repositories from Forgejo"})
		return
	}

	tracked, _ := h.client.Repository.Query().
		Where(repository.HasUserWith(user.ID(u.ID))).
		All(c.Request.Context())
	trackedFullNames := make(map[string]bool)
	for _, r := range tracked {
		if r.ForgejoFullName != "" {
			trackedFullNames[strings.ToLower(r.ForgejoFullName)] = true
		}
	}

	var untracked []*github.Repository
	for _, r := range fjRepos {
		if !trackedFullNames[strings.ToLower(r.FullName)] {
			untracked = append(untracked, r)
		}
	}

	c.HTML(http.StatusOK, "available_repos", gin.H{
		"Repos":      untracked,
		"Forgejo":    true,
		"ForgejoURL": u.ForgejoURL,
	})
}

// SelectForgejoRepos creates Repository rows for selected forgejo repos.
func (h *SettingsHandler) SelectForgejoRepos(c *gin.Context) {
	userID := c.GetInt64("user_id")
	u, err := h.client.User.Get(c.Request.Context(), int(userID))
	if err != nil {
		c.String(http.StatusInternalServerError, "User not found")
		return
	}

	ids := c.PostFormArray("repo_ids")
	if len(ids) == 0 {
		c.String(http.StatusBadRequest, "No repositories selected")
		return
	}

	p, ok := h.providers["forgejo"]
	if !ok || p == nil {
		c.String(http.StatusInternalServerError, "Forgejo provider not configured")
		return
	}

	fjRepos, err := p.ListRepositories(c.Request.Context(), u.ForgejoAccessToken)
	if err != nil {
		log.Printf("Error listing Forgejo repos: %v", err)
		c.String(http.StatusInternalServerError, "Failed to fetch repositories")
		return
	}

	selected := make(map[int64]*github.Repository)
	for _, r := range fjRepos {
		for _, idStr := range ids {
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				continue
			}
			if r.ID == id {
				selected[id] = r
				break
			}
		}
	}

	ctx := c.Request.Context()
	var newIDs []int
	for _, r := range selected {
		repo, err := h.client.Repository.Create().
			SetProvider("forgejo").
			SetGithubID(r.ID).
			SetForgejoID(r.ID).
			SetOwner(r.Owner).
			SetForgejoOwner(r.Owner).
			SetName(r.Name).
			SetForgejoName(r.Name).
			SetFullName(r.FullName).
			SetForgejoFullName(r.FullName).
			SetDescription(r.Description).
			SetHTMLURL(r.HTMLURL).
			SetForgejoHTMLURL(r.HTMLURL).
			SetLanguage(r.Language).
			SetDefaultBranch(r.DefaultBranch).
			SetForgejoURL(u.ForgejoURL).
			SetUserID(u.ID).
			Save(ctx)
		if err != nil {
			log.Printf("Error saving forgejo repo %s: %v", r.FullName, err)
		} else {
			log.Printf("Added forgejo repo %s", r.FullName)
			newIDs = append(newIDs, repo.ID)
		}
	}

	go func() {
		for _, id := range newIDs {
			r, err := h.client.Repository.Get(h.bgCtx, id)
			if err != nil {
				log.Printf("Error fetching new forgejo repo ID %d: %v", id, err)
				continue
			}
			h.syncer.SyncOne(h.bgCtx, r)
		}
	}()

	repos, _ := h.client.Repository.Query().
		Where(repository.HasUserWith(user.ID(u.ID))).
		Order(ent.Desc(repository.FieldUpdatedAt)).
		All(ctx)

	c.HTML(http.StatusOK, "repos_tab", gin.H{
		"User":       u,
		"Repos":      repos,
		"Metrics":    computeMetrics(repos),
		"ActiveTab":  "repos",
		"Sort":       "",
		"Query":      "",
		"Filters":    repoFilters{},
		"HasFilters": false,
	})
}

// DismissForgejoWarning adds a repo full_name to the dismissed list.
func (h *SettingsHandler) DismissForgejoWarning(c *gin.Context) {
	userID := c.GetInt64("user_id")
	fullName := c.PostForm("full_name")
	if fullName == "" {
		c.String(http.StatusBadRequest, "Missing full_name")
		return
	}

	u, err := h.client.User.Get(c.Request.Context(), int(userID))
	if err != nil {
		c.String(http.StatusInternalServerError, "User not found")
		return
	}

	var dismissed []string
	if u.DismissedForgejoWarningFor != "" {
		json.Unmarshal([]byte(u.DismissedForgejoWarningFor), &dismissed)
	}
	dismissed = append(dismissed, fullName)
	b, _ := json.Marshal(dismissed)

	_, err = h.client.User.UpdateOneID(int(userID)).
		SetDismissedForgejoWarningFor(string(b)).
		Save(c.Request.Context())
	if err != nil {
		log.Printf("Error dismissing forgejo warning: %v", err)
		c.String(http.StatusInternalServerError, "Failed to dismiss warning")
		return
	}
	c.Status(http.StatusOK)
}
