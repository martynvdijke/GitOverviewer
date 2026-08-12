package handlers

import (
	"context"
	"log"
	"net/http"

	"gitlens/internal/deploy"

	"github.com/gin-gonic/gin"
)

// deployDashboardData is passed to the deploy_tab template.
type deployDashboardData struct {
	ActiveTab  string
	Enabled    bool
	Backend    string
	GotifyOn   bool
	Total      int
	Targets    []deployTargetRow
	Discovered []deployContainerRow // containers carrying gitlens.deploy.target
	DockerErr  string               // non-empty if Docker discovery failed
}

type deployTargetRow struct {
	Repository  string
	Image       string
	Container   string
	TagStrategy string
	Source      string // "config" or "label"
}

type deployContainerRow struct {
	Container string
	Image     string
	Tag       string
	Label     string
	Tracked   bool
	Reason    string
}

// DeployHandler renders the deploy dashboard.
type DeployHandler struct {
	gotifyOn  bool
	targetsFn func() ([]deploy.Target, error)
	statusFn  func(context.Context) ([]deploy.DiscoveredContainer, error)
}

// NewDeployHandler creates a DeployHandler.
// gotifyOn indicates whether Gotify is configured.
// targetsFn defaults to deploy.LoadAllTargets and statusFn to
// deploy.DiscoverContainerStatus; both are replaced in tests.
func NewDeployHandler(gotifyOn bool) *DeployHandler {
	return &DeployHandler{
		gotifyOn:  gotifyOn,
		targetsFn: deploy.LoadAllTargets,
		statusFn:  deploy.DiscoverContainerStatus,
	}
}

// Dashboard renders the deploy tab content.
// GET /deploy
func (h *DeployHandler) Dashboard(c *gin.Context) {
	targets, err := h.targetsFn()
	if err != nil {
		log.Printf("Deploy: loading targets failed: %v", err)
	}

	// Best-effort label discovery: which containers carry gitlens.deploy.target
	// and whether GitLens tracks them. Independent of targetsFn above.
	labeled, statusErr := h.statusFn(c.Request.Context())
	if statusErr != nil {
		log.Printf("Deploy: container label discovery failed: %v", statusErr)
		labeled = nil
	}

	var dockerErr string
	if err != nil {
		dockerErr = err.Error()
	} else if statusErr != nil {
		dockerErr = statusErr.Error()
	}

	rows := make([]deployTargetRow, 0, len(targets))
	for _, t := range targets {
		source := "config"
		for _, dc := range labeled {
			if dc.Tracked && dc.Label == t.Repository {
				source = "label"
				break
			}
		}
		rows = append(rows, deployTargetRow{
			Repository:  t.Repository,
			Image:       t.Image,
			Container:   t.Container,
			TagStrategy: string(t.TagStrategy),
			Source:      source,
		})
	}

	containerRows := make([]deployContainerRow, 0, len(labeled))
	for _, dc := range labeled {
		containerRows = append(containerRows, deployContainerRow{
			Container: dc.Container,
			Image:     dc.Image,
			Tag:       dc.Tag,
			Label:     dc.Label,
			Tracked:   dc.Tracked,
			Reason:    dc.Reason,
		})
	}

	data := deployDashboardData{
		ActiveTab:  "deploy",
		Enabled:    len(targets) > 0,
		Backend:    deploy.DeployBackend(),
		GotifyOn:   h.gotifyOn,
		Total:      len(targets),
		Targets:    rows,
		Discovered: containerRows,
		DockerErr:  dockerErr,
	}

	c.HTML(http.StatusOK, "deploy_tab", data)
}
