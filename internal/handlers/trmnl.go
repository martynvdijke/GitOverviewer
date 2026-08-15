package handlers

import (
	"net/http"
	"sort"
	"time"

	"gitlens/ent"

	"github.com/gin-gonic/gin"
)

// trmnlRelease is a single latest-release entry in the TRMNL summary payload.
type trmnlRelease struct {
	FullName string `json:"full_name"`
	Tag      string `json:"tag"`
	Name     string `json:"name"`
	Date     string `json:"date"`
	HTMLURL  string `json:"html_url"`
}

// trmnlFailingRepo is a single failing-repository entry in the TRMNL summary payload.
type trmnlFailingRepo struct {
	FullName       string `json:"full_name"`
	WorkflowStatus string `json:"workflow_status"`
}

// trmnlSummary is the full payload served at GET /api/trmnl/summary.
type trmnlSummary struct {
	GeneratedAt      string             `json:"generated_at"`
	TotalRepos       int                `json:"total_repos"`
	TotalReleases    int                `json:"total_releases"`
	FailingRepos     int                `json:"failing_repos"`
	WorkflowPassRate float64            `json:"workflow_pass_rate"`
	LastSync         *string            `json:"last_sync"`
	LatestReleases   []trmnlRelease     `json:"latest_releases"`
	FailingRepoList  []trmnlFailingRepo `json:"failing_repo_list"`
}

// maxTRMNLLists caps the per-repo lists so the payload stays small for the device.
const maxTRMNLLists = 8

type TRMNLSummaryHandler struct {
	client *ent.Client
}

func NewTRMNLSummaryHandler(client *ent.Client) *TRMNLSummaryHandler {
	return &TRMNLSummaryHandler{client: client}
}

// Summary returns a compact, public payload for TRMNL e-ink device polling:
// the latest releases and CI failure state across all tracked repositories.
// It always responds 200, with zero aggregates and empty lists when no data exists.
func (h *TRMNLSummaryHandler) Summary(c *gin.Context) {
	ctx := c.Request.Context()
	repos, err := h.client.Repository.Query().All(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	metrics := computeMetrics(repos)
	overview := computeOverview(repos)

	var lastSync *string
	var latestSync time.Time
	for _, r := range repos {
		if r.SyncedAt.After(latestSync) {
			latestSync = r.SyncedAt
		}
	}
	if !latestSync.IsZero() {
		formatted := latestSync.Format(time.RFC3339)
		lastSync = &formatted
	}

	// Latest releases: repos with a release tag, newest first, capped.
	releases := []trmnlRelease{}
	var withReleases []*ent.Repository
	for _, r := range repos {
		if r.LatestReleaseTag != "" {
			withReleases = append(withReleases, r)
		}
	}
	sort.SliceStable(withReleases, func(i, j int) bool {
		di, dj := withReleases[i].LatestReleaseDate, withReleases[j].LatestReleaseDate
		if di.IsZero() {
			return false
		}
		if dj.IsZero() {
			return true
		}
		return di.After(dj)
	})
	for _, r := range withReleases {
		if len(releases) >= maxTRMNLLists {
			break
		}
		rel := trmnlRelease{
			FullName: r.FullName,
			Tag:      r.LatestReleaseTag,
			Name:     r.LatestReleaseName,
			HTMLURL:  r.HTMLURL,
		}
		if !r.LatestReleaseDate.IsZero() {
			rel.Date = r.LatestReleaseDate.Format(time.RFC3339)
		}
		releases = append(releases, rel)
	}

	// Failing repos: workflow_status == "failure", capped.
	failing := []trmnlFailingRepo{}
	for _, r := range repos {
		if r.WorkflowStatus == "failure" {
			failing = append(failing, trmnlFailingRepo{
				FullName:       r.FullName,
				WorkflowStatus: r.WorkflowStatus,
			})
		}
		if len(failing) >= maxTRMNLLists {
			break
		}
	}

	c.JSON(http.StatusOK, trmnlSummary{
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
		TotalRepos:       metrics.TotalRepos,
		TotalReleases:    overview.TotalReleases,
		FailingRepos:     overview.FailingRepos,
		WorkflowPassRate: metrics.WorkflowPassRate,
		LastSync:         lastSync,
		LatestReleases:   releases,
		FailingRepoList:  failing,
	})
}
