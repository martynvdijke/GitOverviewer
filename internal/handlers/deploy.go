package handlers

import (
	"context"
	"log"
	"net/http"
	"time"

	"gitlens/internal/deploy"
	"gitlens/internal/services"
	"gitlens/internal/services/uptimekuma"

	"github.com/gin-gonic/gin"
)

// deployDashboardData is passed to the deploy_tab template.
type deployDashboardData struct {
	ActiveTab       string
	Enabled         bool
	Backend         string
	GotifyOn        bool
	Total           int
	Targets         []deployTargetRow
	Discovered      []deployContainerRow // containers carrying gitlens.deploy.target
	DockerErr       string               // non-empty if Docker discovery failed
	KumaOn          bool                 // Uptime Kuma configured
	NPMOn           bool                 // NPM configured
	ContainerLinks  []deployContainerLinksRow
	Flash           string // optional alert text rendered in the Service Links section
	FlashType       string // "success", "danger" or "warning"
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
	Port      int // first published container port (0 if none)
}

// deployServiceState describes the link state of one container for one
// external service (Uptime Kuma, NPM or Authelia).
type deployServiceState struct {
	Service      string // "uptime_kuma", "npm" or "authelia"
	ServiceLabel string // display name
	Reference    string // monitor ID, proxy-host hostname or domain
	LiveState    string // up/down/paused, enabled/disabled, linked, or unknown
	Linked       bool
	Configured   bool // service has enough config to be actionable
}

// deployContainerLinksRow is one row of the Service Links table: all
// service states for a single discovered container.
type deployContainerLinksRow struct {
	Container string
	Port      int
	States    []deployServiceState // fixed order: uptime_kuma, npm, authelia
}

// linkStore is the persistence surface the Deploy tab needs for service
// links. *services.Store satisfies it; tests provide fakes.
type linkStore interface {
	Upsert(ctx context.Context, container string, service services.Service, reference string) (*services.Link, error)
	Delete(ctx context.Context, container string, service services.Service) error
	All(ctx context.Context) ([]services.Link, error)
}

// kumaClient is the subset of the Uptime Kuma client used by the Deploy tab.
type kumaClient interface {
	AddHTTPMonitor(ctx context.Context, name, url string) (int64, error)
	MonitorStatus(ctx context.Context, monitorID int64) (string, error)
}

// npmClient is the subset of the NPM client used by the Deploy tab.
type npmClient interface {
	CreateProxyHost(ctx context.Context, domain, host string, port int) (int, error)
	ProxyHostState(ctx context.Context, hostname string) (string, error)
}

// DeployHandler renders the deploy dashboard.
type DeployHandler struct {
	gotifyOn  bool
	targetsFn func() ([]deploy.Target, error)
	statusFn  func(context.Context) ([]deploy.DiscoveredContainer, error)

	store linkStore  // nil when no service links feature wired
	kuma  kumaClient // nil when Uptime Kuma is not configured
	npm   npmClient  // nil when NPM is not configured
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

// SetServiceClients wires the service-link store and the optional Uptime
// Kuma / NPM clients. store may be nil (links section stays empty) and the
// clients may be nil (services shown as "Not configured").
func (h *DeployHandler) SetServiceClients(store linkStore, kuma kumaClient, npm npmClient) {
	h.store = store
	h.kuma = kuma
	h.npm = npm
}

// SetGotifyOn updates the Gotify connectivity flag shown on the deploy tab
// (e.g. after Gotify settings change in the admin panel).
func (h *DeployHandler) SetGotifyOn(on bool) {
	h.gotifyOn = on
}

// Dashboard renders the deploy tab content.
// GET /deploy
func (h *DeployHandler) Dashboard(c *gin.Context) {
	h.renderDashboard(c, "", "")
}

// renderDashboard builds the deploy_tab data and renders it. flash carries an
// optional alert rendered in the Service Links section (used by the link
// mutation handlers which re-render the whole tab).
func (h *DeployHandler) renderDashboard(c *gin.Context, flash, flashType string) {
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
			Port:      dc.Port,
		})
	}

	data := deployDashboardData{
		ActiveTab:      "deploy",
		Enabled:        len(targets) > 0,
		Backend:        deploy.DeployBackend(),
		GotifyOn:       h.gotifyOn,
		Total:          len(targets),
		Targets:        rows,
		Discovered:     containerRows,
		DockerErr:      dockerErr,
		KumaOn:         h.kuma != nil,
		NPMOn:          h.npm != nil,
		ContainerLinks: h.linkRowsFor(c.Request.Context(), labeled),
		Flash:          flash,
		FlashType:      flashType,
	}

	c.HTML(http.StatusOK, "deploy_tab", data)
}

// serviceOrder is the fixed column order of the Service Links table.
var serviceOrder = []services.Service{
	services.ServiceUptimeKuma,
	services.ServiceNPM,
	services.ServiceAuthelia,
}

// serviceLabels maps a service to its display name.
var serviceLabels = map[services.Service]string{
	services.ServiceUptimeKuma: "Uptime Kuma",
	services.ServiceNPM:         "NPM",
	services.ServiceAuthelia:    "Authelia",
}

func serviceLabel(s services.Service) string {
	if l, ok := serviceLabels[s]; ok {
		return l
	}
	return string(s)
}

// configured reports whether a service can perform actions. Authelia needs
// no server config; Uptime Kuma and NPM need their clients.
func (h *DeployHandler) configured(s services.Service) bool {
	switch s {
	case services.ServiceUptimeKuma:
		return h.kuma != nil
	case services.ServiceNPM:
		return h.npm != nil
	default:
		return true
	}
}

// linkRowsFor builds the Service Links table rows: for every discovered
// container, one state entry per service, with links loaded from the store
// and live state fetched best-effort (failures degrade to "unknown").
func (h *DeployHandler) linkRowsFor(ctx context.Context, containers []deploy.DiscoveredContainer) []deployContainerLinksRow {
	rows := make([]deployContainerLinksRow, 0, len(containers))
	if len(containers) == 0 {
		return rows
	}

	links := map[string]map[string]services.Link{}
	if h.store != nil {
		all, err := h.store.All(ctx)
		if err != nil {
			log.Printf("Deploy: loading service links failed: %v", err)
		} else {
			for _, l := range all {
				if links[l.Container] == nil {
					links[l.Container] = map[string]services.Link{}
				}
				links[l.Container][string(l.Service)] = l
			}
		}
	}

	for _, dc := range containers {
		row := deployContainerLinksRow{Container: dc.Container, Port: dc.Port}
		for _, svc := range serviceOrder {
			state := deployServiceState{
				Service:      string(svc),
				ServiceLabel: serviceLabel(svc),
				Configured:   h.configured(svc),
			}
			if l, ok := links[dc.Container][string(svc)]; ok {
				state.Linked = true
				state.Reference = l.Reference
				state.LiveState = h.liveState(ctx, svc, l.Reference)
			}
			row.States = append(row.States, state)
		}
		rows = append(rows, row)
	}
	return rows
}

// liveState fetches the current state of a linked service, best-effort with a
// short timeout. Any failure degrades to "unknown".
func (h *DeployHandler) liveState(ctx context.Context, svc services.Service, reference string) string {
	switch svc {
	case services.ServiceUptimeKuma:
		if h.kuma == nil {
			return "unknown"
		}
		id, err := uptimekuma.ParseMonitorID(reference)
		if err != nil {
			return "unknown"
		}
		sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		s, err := h.kuma.MonitorStatus(sctx, id)
		if err != nil {
			return "unknown"
		}
		return s
	case services.ServiceNPM:
		if h.npm == nil {
			return "unknown"
		}
		sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		s, err := h.npm.ProxyHostState(sctx, reference)
		if err != nil {
			return "unknown"
		}
		return s
	default:
		return "linked"
	}
}

// findContainer returns the discovered container matching name, if any.
func (h *DeployHandler) findContainer(ctx context.Context, name string) (*deploy.DiscoveredContainer, bool) {
	containers, err := h.statusFn(ctx)
	if err != nil {
		return nil, false
	}
	for i := range containers {
		if containers[i].Container == name {
			return &containers[i], true
		}
	}
	return nil, false
}

// validService reports whether s is one of the three supported services.
func validService(s services.Service) bool {
	switch s {
	case services.ServiceUptimeKuma, services.ServiceNPM, services.ServiceAuthelia:
		return true
	}
	return false
}
