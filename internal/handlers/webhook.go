package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"gitlens/ent"
	"gitlens/ent/repository"
	"gitlens/internal/deploy"
	"gitlens/internal/sync"

	"github.com/gin-gonic/gin"
)

// notifier is the push channel used for deploy notifications.
// *gotify.Client satisfies it; tests inject a fake.
type notifier interface {
	Send(ctx context.Context, title, message string, priority int) error
}

// targetsProvider resolves the current deploy targets. It is called per
// release event, so label-based discovery picks up newly added containers
// without a restart.
type targetsProvider func(ctx context.Context) ([]deploy.Target, error)

type WebhookHandler struct {
	client    *ent.Client
	syncer    *sync.Syncer
	secret    string
	targetsFn targetsProvider
	deployer  deploy.Deployer
	gotify    notifier
}

func NewWebhookHandler(client *ent.Client, syncer *sync.Syncer, secret string) *WebhookHandler {
	return &WebhookHandler{
		client: client,
		syncer: syncer,
		secret: secret,
	}
}

// SetDeployer configures the deploy subsystem. Call before server starts.
func (h *WebhookHandler) SetDeployer(provider targetsProvider, d deploy.Deployer, g notifier) {
	h.targetsFn = provider
	h.deployer = d
	h.gotify = g
}

// SetGotify swaps the notifier (e.g. after Gotify settings change in the
// admin panel). nil disables notifications.
func (h *WebhookHandler) SetGotify(g notifier) {
	h.gotify = g
}

type pushPayload struct {
	Ref        string `json:"ref"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type releasePayload struct {
	Action  string `json:"action"`
	Release struct {
		TagName    string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Name       string `json:"name"`
		HTMLURL    string `json:"html_url"`
		Author     struct {
			Login string `json:"login"`
		} `json:"author"`
	} `json:"release"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// releaseInfo carries the fields of a published release that we surface in
// deploy notifications.
type releaseInfo struct {
	Repo   string // GitHub "owner/repo"
	Tag    string // release tag as published, e.g. "v1.2.3"
	Name   string // release title
	Author string // publishing user's login
	URL    string // link to the release
	Kind   string // "" for releases, "package" for container image publishes
}

func releaseFromPayload(p releasePayload) releaseInfo {
	return releaseInfo{
		Repo:   p.Repository.FullName,
		Tag:    p.Release.TagName,
		Name:   p.Release.Name,
		Author: p.Release.Author.Login,
		URL:    p.Release.HTMLURL,
	}
}

// packagePayload is the registry_package/package webhook event, fired when a
// package is published (e.g. a container image pushed to GHCR). The payload
// shape is identical for both event names, so either key is accepted.
type packagePayload struct {
	Action string `json:"action"`
	// RegistryPackage and Package are aliases of the same payload shape.
	RegistryPackage *packageInfo `json:"registry_package"`
	Package         *packageInfo `json:"package"`
	Repository      struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// packageInfo is the package object common to both event names. For container
// images, package_version.version is a sha256 digest, so the pushed tag must
// come from package_version.container_metadata.tag.name instead.
type packageInfo struct {
	Name        string `json:"name"`
	PackageType string `json:"package_type"`
	Owner       struct {
		Login string `json:"login"`
	} `json:"owner"`
	HTMLURL        string `json:"html_url"`
	PackageVersion struct {
		Version           string `json:"version"`
		HTMLURL           string `json:"html_url"`
		ContainerMetadata struct {
			Tag struct {
				Name string `json:"name"`
			} `json:"tag"`
		} `json:"container_metadata"`
	} `json:"package_version"`
}

// HandlePush dispatches push and release events. It verifies the webhook
// signature when a secret is configured, then routes to the right handler.
func (h *WebhookHandler) HandlePush(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		log.Printf("Webhook: error reading body: %v", err)
		c.Status(http.StatusBadRequest)
		return
	}

	if err := h.verifySignature(c, body); err != nil {
		log.Printf("Webhook: %v", err)
		c.Status(http.StatusUnauthorized)
		return
	}

	event := c.GetHeader("X-GitHub-Event")
	switch event {
	case "release":
		h.handleRelease(c, body)
	case "registry_package", "package":
		h.handlePackage(c, body)
	default:
		// Legacy behavior: treat as push event (or unknown)
		h.handlePushEvent(c, body)
	}
}

func (h *WebhookHandler) verifySignature(c *gin.Context, body []byte) error {
	if h.secret == "" {
		return nil
	}
	sig := c.GetHeader("X-Hub-Signature-256")
	if sig == "" {
		return fmt.Errorf("webhook: missing signature")
	}
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return fmt.Errorf("webhook: invalid signature")
	}
	return nil
}

func (h *WebhookHandler) handlePushEvent(c *gin.Context, body []byte) {
	// If event header says "push", parse push payload.
	// For unknown events (e.g. ping), return OK silently.
	if c.GetHeader("X-GitHub-Event") != "push" {
		c.Status(http.StatusOK)
		return
	}

	var payload pushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("Webhook: error parsing push payload: %v", err)
		c.Status(http.StatusBadRequest)
		return
	}

	if payload.Ref == "" || payload.Repository.ID == 0 {
		c.Status(http.StatusOK)
		return
	}

	ctx := c.Request.Context()
	repo, err := h.client.Repository.Query().
		Where(repository.GithubID(payload.Repository.ID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			c.Status(http.StatusOK)
			return
		}
		log.Printf("Webhook: error finding repo %d: %v", payload.Repository.ID, err)
		c.Status(http.StatusInternalServerError)
		return
	}

	log.Printf("Webhook: push to %s/%s — syncing", repo.Owner, repo.Name)
	go h.syncer.SyncOne(context.Background(), repo)

	c.Status(http.StatusOK)
}

func (h *WebhookHandler) handleRelease(c *gin.Context, body []byte) {
	var payload releasePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("Webhook: error parsing release payload: %v", err)
		c.Status(http.StatusBadRequest)
		return
	}

	// Only act on published releases
	if payload.Action != "published" {
		log.Printf("Webhook: release action=%s — ignoring", payload.Action)
		c.Status(http.StatusOK)
		return
	}

	repoFullName := payload.Repository.FullName
	tagName := payload.Release.TagName

	// No deploy targets configured — skip
	if h.deployer == nil || h.targetsFn == nil {
		log.Printf("Webhook: release for %s but no deploy targets configured", repoFullName)
		c.Status(http.StatusOK)
		return
	}

	// Resolve targets live so newly labeled containers deploy without a restart.
	targets, err := h.targetsFn(c.Request.Context())
	if err != nil {
		log.Printf("Webhook: error resolving deploy targets: %v", err)
		c.Status(http.StatusInternalServerError)
		return
	}

	// Check allowlist
	target := deploy.MatchTarget(targets, repoFullName)
	if target == nil {
		log.Printf("Webhook: release for %s — no matching deploy target", repoFullName)
		c.Status(http.StatusOK)
		return
	}

	// Prerelease check
	if payload.Release.Prerelease && !deploy.PrereleasesAllowed() {
		log.Printf("Webhook: release for %s is prerelease — skipping", repoFullName)
		c.Status(http.StatusOK)
		return
	}

	tag := deploy.NormalizeTag(tagName, target.TagStrategy)
	log.Printf("Webhook: release for %s (%s) — deploying image %s:%s", repoFullName, tagName, target.Image, tag)

	// Acknowledge immediately, deploy async
	go h.runDeploy(releaseFromPayload(payload), *target, tag)

	c.Status(http.StatusOK)
}

// handlePackage deploys when a container image is published. Unlike release
// events, this fires only after the image has actually landed in the registry,
// so the deploy always pulls the freshly published image. It is the fix for
// workflows that publish the GitHub release before pushing the image.
func (h *WebhookHandler) handlePackage(c *gin.Context, body []byte) {
	var payload packagePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("Webhook: error parsing package payload: %v", err)
		c.Status(http.StatusBadRequest)
		return
	}

	// Only act on published packages
	if payload.Action != "published" {
		log.Printf("Webhook: package action=%s — ignoring", payload.Action)
		c.Status(http.StatusOK)
		return
	}

	pkg := payload.RegistryPackage
	if pkg == nil {
		pkg = payload.Package
	}
	if pkg == nil || !strings.EqualFold(pkg.PackageType, "container") {
		log.Printf("Webhook: package is not a container image — ignoring")
		c.Status(http.StatusOK)
		return
	}

	// For container images the pushed tag lives in container_metadata; the
	// version field is a sha256 digest, which is useless as an image tag.
	tagName := pkg.PackageVersion.ContainerMetadata.Tag.Name
	if tagName == "" {
		tagName = pkg.PackageVersion.Version
	}
	if tagName == "" || strings.HasPrefix(strings.ToLower(tagName), "sha256:") {
		log.Printf("Webhook: package for %s has no usable tag — ignoring", pkg.Name)
		c.Status(http.StatusOK)
		return
	}

	// Prefer the source repository, fall back to owner/name for packages that
	// are not linked to a repository.
	repoFullName := payload.Repository.FullName
	if repoFullName == "" {
		if pkg.Owner.Login == "" || pkg.Name == "" {
			log.Printf("Webhook: package has no repository or owner/name — ignoring")
			c.Status(http.StatusOK)
			return
		}
		repoFullName = pkg.Owner.Login + "/" + pkg.Name
	}

	// No deploy targets configured — skip
	if h.deployer == nil || h.targetsFn == nil {
		log.Printf("Webhook: package for %s but no deploy targets configured", repoFullName)
		c.Status(http.StatusOK)
		return
	}

	targets, err := h.targetsFn(c.Request.Context())
	if err != nil {
		log.Printf("Webhook: error resolving deploy targets: %v", err)
		c.Status(http.StatusInternalServerError)
		return
	}

	target := deploy.MatchTarget(targets, repoFullName)
	if target == nil {
		log.Printf("Webhook: package for %s — no matching deploy target", repoFullName)
		c.Status(http.StatusOK)
		return
	}

	// Publishing an image with several tags fires one event per tag. Only
	// react to the tag the target's strategy deploys, so a single push does
	// not trigger duplicate deploys of the same image.
	if target.TagStrategy == deploy.TagStrategyLatest && tagName != "latest" {
		log.Printf("Webhook: package for %s (tag %s) — skipping, latest strategy", repoFullName, tagName)
		c.Status(http.StatusOK)
		return
	}
	if target.TagStrategy == deploy.TagStrategyReleaseTag && tagName == "latest" {
		log.Printf("Webhook: package for %s (tag latest) — skipping, release tag strategy", repoFullName)
		c.Status(http.StatusOK)
		return
	}

	tag := deploy.NormalizeTag(tagName, target.TagStrategy)
	log.Printf("Webhook: package for %s (%s) — deploying image %s:%s", repoFullName, tagName, target.Image, tag)

	info := releaseInfo{
		Repo: repoFullName,
		Tag:  tagName,
		Kind: "package",
	}
	if pkg.PackageVersion.HTMLURL != "" {
		info.URL = pkg.PackageVersion.HTMLURL
	} else if pkg.HTMLURL != "" {
		info.URL = pkg.HTMLURL
	}

	// Acknowledge immediately, deploy async
	go h.runDeploy(info, *target, tag)

	c.Status(http.StatusOK)
}

func (h *WebhookHandler) runDeploy(release releaseInfo, target deploy.Target, imageTag string) {
	ctx := context.Background()
	result, err := h.deployer.PullAndUpdate(ctx, target, imageTag)

	subject := "release deploy"
	if release.Kind == "package" {
		subject = "image deploy"
	}
	title := fmt.Sprintf("%s %s %s", release.Repo, release.Tag, subject)
	if err != nil {
		log.Printf("Deploy: %s failed: %v", release.Repo, err)
		if h.gotify != nil {
			h.gotify.Send(ctx, title, deployMessage(release, target, imageTag, result, err), 5)
		}
		return
	}

	log.Printf("Deploy: %s succeeded", release.Repo)
	if h.gotify != nil {
		h.gotify.Send(ctx, title, deployMessage(release, target, imageTag, result, nil), 2)
	}
}

// deployMessage renders the Gotify notification body for a deploy. It always
// mentions the release — title, tag, author, and link — plus the target
// container and image, the steps the deploy performed, and the error when the
// deploy failed.
func deployMessage(release releaseInfo, target deploy.Target, imageTag string, result *deploy.DeployResult, deployErr error) string {
	var b strings.Builder

	if release.Kind == "package" {
		fmt.Fprintf(&b, "Image %s published\n", release.Tag)
	} else if release.Name != "" && release.Name != release.Tag {
		fmt.Fprintf(&b, "Release %q (%s)\n", release.Name, release.Tag)
	} else {
		fmt.Fprintf(&b, "Release %s\n", release.Tag)
	}
	if release.Author != "" {
		fmt.Fprintf(&b, "By: %s\n", release.Author)
	}

	if deployErr != nil {
		fmt.Fprintf(&b, "Deploy FAILED: %s -> %s:%s\nError: %v", target.Container, target.Image, imageTag, deployErr)
	} else {
		fmt.Fprintf(&b, "Container %s updated to %s:%s", target.Container, target.Image, imageTag)
	}

	if result != nil && len(result.Steps) > 0 {
		b.WriteString("\n\nWhat happened:")
		for _, s := range result.Steps {
			b.WriteString("\n• ")
			b.WriteString(s)
		}
	}

	if release.URL != "" {
		b.WriteString("\n")
		b.WriteString(release.URL)
	}
	return b.String()
}
