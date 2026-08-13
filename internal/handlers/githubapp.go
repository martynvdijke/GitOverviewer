package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"gitlens/ent"
	"gitlens/ent/repository"
	"gitlens/ent/user"

	"github.com/gin-gonic/gin"
)

type GitHubAppHandler struct {
	client        *ent.Client
	deployEnabled bool
}

func NewGitHubAppHandler(client *ent.Client) *GitHubAppHandler {
	return &GitHubAppHandler{client: client}
}

// SetDeployEnabled configures whether registered webhooks include the release
// event (used by the deploy subsystem). Call before the server starts.
func (h *GitHubAppHandler) SetDeployEnabled(enabled bool) {
	h.deployEnabled = enabled
}

// githubAPIBase is the GitHub REST API base URL. Overridable in tests.
var githubAPIBase = "https://api.github.com"

type ghAppInstallation struct {
	Action       string `json:"action"`
	Installation struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
			ID    int64  `json:"id"`
		} `json:"account"`
		RepositoriesURL string `json:"repositories_url"`
	} `json:"installation"`
	Repositories []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repositories"`
}

func (h *GitHubAppHandler) HandleInstallation(c *gin.Context) {
	secret := os.Getenv("GITHUB_APP_WEBHOOK_SECRET")
	if secret == "" {
		log.Println("GITHUB_APP_WEBHOOK_SECRET not set, skipping signature verification")
	} else {
		sig := c.GetHeader("X-Hub-Signature-256")
		if sig == "" {
			c.String(http.StatusUnauthorized, "Missing signature")
			return
		}
		body, _ := io.ReadAll(c.Request.Body)
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(sig), []byte(expected)) {
			c.String(http.StatusUnauthorized, "Invalid signature")
			return
		}
	}

	var payload ghAppInstallation
	if err := c.ShouldBindJSON(&payload); err != nil {
		log.Printf("Error parsing GitHub App installation payload: %v", err)
		c.String(http.StatusBadRequest, "Invalid payload")
		return
	}

	log.Printf("GitHub App installation event: action=%s, account=%s", payload.Action, payload.Installation.Account.Login)

	switch payload.Action {
	case "created", "added":
		h.handleInstallCreated(c, &payload)
	case "removed", "deleted":
		h.handleInstallRemoved(c, &payload)
	default:
		c.String(http.StatusOK, "Event received")
	}
}

func (h *GitHubAppHandler) handleInstallCreated(c *gin.Context, payload *ghAppInstallation) {
	ctx := c.Request.Context()
	accountLogin := payload.Installation.Account.Login

	u, err := h.client.User.Query().
		Where(user.Login(accountLogin)).
		Only(ctx)
	if err != nil {
		log.Printf("No matching user found for GitHub App installation on %s: %v", accountLogin, err)
		c.String(http.StatusOK, "Installation recorded, but no matching user found")
		return
	}

	repos := payload.Repositories
	if len(repos) == 0 {
		log.Printf("No repos in GitHub App installation payload for %s", accountLogin)
		c.String(http.StatusOK, "No repositories in payload")
		return
	}

	imported := 0
	configured := 0
	webhookErrors := 0
	hookURL := buildHookURL(c)
	events := h.webhookEvents()
	for _, r := range repos {
		exists, _ := h.client.Repository.Query().
			Where(
				repository.HasUserWith(user.ID(u.ID)),
				repository.GithubID(r.ID),
			).
			Exist(ctx)
		if exists {
			continue
		}

		_, err := h.client.Repository.Create().
			SetGithubID(r.ID).
			SetOwner(r.Owner.Login).
			SetName(r.Name).
			SetFullName(r.FullName).
			SetHTMLURL(fmt.Sprintf("https://github.com/%s", r.FullName)).
			SetDefaultBranch("main").
			SetUserID(u.ID).
			Save(ctx)
		if err != nil {
			log.Printf("Error creating repo %s from GitHub App install: %v", r.FullName, err)
			continue
		}
		imported++

		// Auto-register the webhook for newly imported repos. Failures are
		// logged and never abort the import.
		if err := h.ensureWebhook(u.AccessToken, r.Owner.Login, r.Name, events, hookURL); err != nil {
			log.Printf("Error registering webhook for %s: %v", r.FullName, err)
			webhookErrors++
			continue
		}
		configured++
	}

	log.Printf("GitHub App installation: imported %d repos for user %s (webhooks configured: %d, errors: %d)", imported, accountLogin, configured, webhookErrors)
	c.String(http.StatusOK, "Installation processed")
}

func (h *GitHubAppHandler) handleInstallRemoved(c *gin.Context, payload *ghAppInstallation) {
	ctx := c.Request.Context()
	accountLogin := payload.Installation.Account.Login

	u, err := h.client.User.Query().
		Where(user.Login(accountLogin)).
		Only(ctx)
	if err != nil {
		c.String(http.StatusOK, "Removal recorded")
		return
	}

	repoIDs := make([]int64, len(payload.Repositories))
	for i, r := range payload.Repositories {
		repoIDs[i] = r.ID
	}

	deleted, err := h.client.Repository.Delete().
		Where(
			repository.HasUserWith(user.ID(u.ID)),
			repository.GithubIDIn(repoIDs...),
		).
		Exec(ctx)
	if err != nil {
		log.Printf("Error removing repos after GitHub App uninstall: %v", err)
		c.String(http.StatusInternalServerError, "Failed to remove repos")
		return
	}

	log.Printf("GitHub App uninstallation: removed %d repos for user %s", deleted, accountLogin)
	c.String(http.StatusOK, "Repos removed")
}

func (h *GitHubAppHandler) SetupAutoWebhooks(c *gin.Context) {
	userID := c.GetInt64("user_id")
	u, err := h.client.User.Get(c.Request.Context(), int(userID))
	if err != nil {
		c.String(http.StatusInternalServerError, "User not found")
		return
	}

	appID := os.Getenv("GITHUB_APP_ID")
	if appID == "" {
		c.String(http.StatusBadRequest, "GitHub App not configured (GITHUB_APP_ID not set)")
		return
	}

	repos, err := h.client.Repository.Query().
		Where(repository.HasUserWith(user.ID(u.ID))).
		All(c.Request.Context())
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to fetch repos")
		return
	}

	hookURL := buildHookURL(c)
	events := h.webhookEvents()

	configured := 0
	errors := 0

	for _, r := range repos {
		err := h.ensureWebhook(u.AccessToken, r.Owner, r.Name, events, hookURL)
		if err != nil {
			log.Printf("Error registering webhook for %s: %v", r.FullName, err)
			errors++
			continue
		}
		configured++
	}

	c.String(http.StatusOK, fmt.Sprintf("Webhooks configured: %d, errors: %d", configured, errors))
}

// buildHookURL computes the externally reachable /webhook/github URL from the
// incoming request. Shared by automatic and manual webhook registration.
func buildHookURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host + "/webhook/github"
}

// webhookEvents returns the GitHub events this instance subscribes to. The
// release event is added when the deploy subsystem is enabled.
func (h *GitHubAppHandler) webhookEvents() []string {
	events := []string{"push"}
	if h.deployEnabled {
		events = append(events, "release")
	}
	return events
}

type createHookRequest struct {
	Name   string   `json:"name"`
	Active bool     `json:"active"`
	Events []string `json:"events"`
	Config struct {
		URL         string `json:"url"`
		ContentType string `json:"content_type"`
		Secret      string `json:"secret"`
		InsecureSSL string `json:"insecure_ssl"`
	} `json:"config"`
}

// listHooksResponse mirrors the subset of the GitHub hooks list API response
// needed to detect an existing webhook for this instance.
type listHooksResponse []struct {
	Config struct {
		URL string `json:"url"`
	} `json:"config"`
}

// ensureWebhook registers a webhook for a repo unless one pointing at hookURL
// already exists. Idempotent: re-runs do not create duplicate hooks.
func (h *GitHubAppHandler) ensureWebhook(token, owner, repo string, events []string, hookURL string) error {
	existing, err := h.listWebhooks(token, owner, repo)
	if err != nil {
		return err
	}
	for _, hook := range existing {
		if hook.Config.URL == hookURL {
			return nil // already registered
		}
	}

	payload := createHookRequest{
		Name:   "web",
		Active: true,
		Events: events,
	}
	payload.Config.URL = hookURL
	payload.Config.ContentType = "json"
	payload.Config.Secret = os.Getenv("GITHUB_WEBHOOK_SECRET")
	payload.Config.InsecureSSL = "0"

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return fmt.Errorf("encoding hook payload: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/%s/hooks", githubAPIBase, owner, repo)
	req, err := http.NewRequest("POST", url, &buf)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (h *GitHubAppHandler) listWebhooks(token, owner, repo string) (listHooksResponse, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/hooks", githubAPIBase, owner, repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Authorization", "Bearer "+token)

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error %d: %s", resp.StatusCode, string(body))
	}

	var hooks listHooksResponse
	if err := json.NewDecoder(resp.Body).Decode(&hooks); err != nil {
		return nil, fmt.Errorf("decoding hooks response: %w", err)
	}
	return hooks, nil
}
