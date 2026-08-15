package handlers

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gitlens/internal/services"
	"gitlens/internal/services/authelia"
	"gitlens/internal/services/npm"
	"gitlens/internal/services/uptimekuma"

	"github.com/gin-gonic/gin"
)

// linkError is used to signal a validation/action failure that should be
// shown to the user as an inline alert instead of a server error.
type linkError struct {
	msg string
}

func (e *linkError) Error() string { return e.msg }

// renderLinksTab re-renders the full deploy tab after a link mutation,
// carrying an optional inline alert.
func (h *DeployHandler) renderLinksTab(c *gin.Context, flash, flashType string) {
	h.renderDashboard(c, flash, flashType)
}

// CreateLink handles POST /deploy/links: manual link of a labeled container
// to an existing monitor/proxy-host/domain.
// Form fields: container, service (uptime_kuma|npm|authelia), reference.
func (h *DeployHandler) CreateLink(c *gin.Context) {
	container := strings.TrimSpace(c.PostForm("container"))
	serviceStr := strings.TrimSpace(c.PostForm("service"))
	reference := strings.TrimSpace(c.PostForm("reference"))

	if err := h.validateLinkInput(c, container, serviceStr, reference); err != nil {
		h.renderLinksTab(c, err.Error(), "danger")
		return
	}

	svc := services.Service(serviceStr)
	if !h.configured(svc) {
		h.renderLinksTab(c, fmt.Sprintf("%s is not configured.", serviceLabel(svc)), "warning")
		return
	}

	if _, err := h.store.Upsert(c.Request.Context(), container, svc, reference); err != nil {
		log.Printf("Deploy: saving link failed: %v", err)
		h.renderLinksTab(c, "Failed to save the link.", "danger")
		return
	}

	h.renderLinksTab(c, fmt.Sprintf("Linked %s → %s (%s).", container, reference, serviceLabel(svc)), "success")
}

// validateLinkInput checks that the container is currently discovered via
// the gitlens.deploy.target label, the service is one of the three, and the
// reference is non-empty (and a valid monitor ID for uptime_kuma).
func (h *DeployHandler) validateLinkInput(c *gin.Context, container, serviceStr, reference string) error {
	if container == "" || serviceStr == "" || reference == "" {
		return &linkError{msg: "Container, service and reference are required."}
	}
	svc := services.Service(serviceStr)
	if !validService(svc) {
		return &linkError{msg: "Unknown service."}
	}
	if svc == services.ServiceUptimeKuma {
		if _, err := uptimekuma.ParseMonitorID(reference); err != nil {
			return &linkError{msg: "Monitor ID must be a positive number."}
		}
	}
	if _, ok := h.findContainer(c.Request.Context(), container); !ok {
		return &linkError{msg: fmt.Sprintf("Container %q is not a labeled container.", container)}
	}
	return nil
}

// DeleteLink handles DELETE /deploy/links: removes a stored link.
// Form fields: container, service.
func (h *DeployHandler) DeleteLink(c *gin.Context) {
	container := strings.TrimSpace(c.PostForm("container"))
	serviceStr := strings.TrimSpace(c.PostForm("service"))

	// Go's net/http only parses form bodies for POST/PUT/PATCH, so on a
	// DELETE (as sent by hx-delete) PostForm is empty. Parse the body
	// manually as a fallback.
	if container == "" && serviceStr == "" {
		body, _ := io.ReadAll(c.Request.Body)
		if vals, err := url.ParseQuery(string(body)); err == nil {
			container = strings.TrimSpace(vals.Get("container"))
			serviceStr = strings.TrimSpace(vals.Get("service"))
		}
	}

	svc := services.Service(serviceStr)
	if !validService(svc) {
		h.renderLinksTab(c, "Unknown service.", "danger")
		return
	}
	if container == "" {
		h.renderLinksTab(c, "Container is required.", "danger")
		return
	}
	if h.store == nil {
		h.renderLinksTab(c, "Service links are not available.", "warning")
		return
	}

	if err := h.store.Delete(c.Request.Context(), container, svc); err != nil {
		log.Printf("Deploy: deleting link failed: %v", err)
		h.renderLinksTab(c, "Failed to delete the link.", "danger")
		return
	}

	h.renderLinksTab(c, fmt.Sprintf("Unlinked %s from %s.", container, serviceLabel(svc)), "success")
}

// AddKumaMonitor handles POST /deploy/links/uptime-kuma/add-monitor:
// creates an HTTP monitor on Uptime Kuma for the container (using the
// container's published port, falling back to the form-provided port) and
// stores the resulting link.
// Form fields: container, port (fallback).
func (h *DeployHandler) AddKumaMonitor(c *gin.Context) {
	container := strings.TrimSpace(c.PostForm("container"))
	if h.kuma == nil {
		h.renderLinksTab(c, "Uptime Kuma is not configured.", "warning")
		return
	}

	port, err := h.containerPort(c, container, c.PostForm("port"))
	if err != nil {
		h.renderLinksTab(c, err.Error(), "danger")
		return
	}

	// Monitor URL: Uptime Kuma must be able to reach the container. On a
	// shared Docker network the container hostname resolves; the published
	// port is used as fallback for cross-network setups.
	url := fmt.Sprintf("http://%s:%d", container, port)
	id, err := h.kuma.AddHTTPMonitor(c.Request.Context(), container, url)
	if err != nil {
		log.Printf("Deploy: creating Uptime Kuma monitor failed: %v", err)
		h.renderLinksTab(c, fmt.Sprintf("Failed to create monitor: %v", err), "danger")
		return
	}

	if _, err := h.store.Upsert(c.Request.Context(), container, services.ServiceUptimeKuma, strconv.FormatInt(id, 10)); err != nil {
		log.Printf("Deploy: saving monitor link failed: %v", err)
		h.renderLinksTab(c, "Monitor created but storing the link failed.", "danger")
		return
	}

	h.renderLinksTab(c, fmt.Sprintf("Created Uptime Kuma monitor %d for %s.", id, container), "success")
}

// AddNPMProxyHost handles POST /deploy/links/npm/add-proxy-host: creates a
// proxy host on NPM forwarding the given domain to the container and stores
// the resulting link.
// Form fields: container, domain, port (fallback).
func (h *DeployHandler) AddNPMProxyHost(c *gin.Context) {
	container := strings.TrimSpace(c.PostForm("container"))
	domain := strings.TrimSpace(c.PostForm("domain"))
	if h.npm == nil {
		h.renderLinksTab(c, "NPM is not configured.", "warning")
		return
	}
	if domain == "" {
		h.renderLinksTab(c, "A domain is required.", "danger")
		return
	}

	port, err := h.containerPort(c, container, c.PostForm("port"))
	if err != nil {
		h.renderLinksTab(c, err.Error(), "danger")
		return
	}

	id, err := h.npm.CreateProxyHost(c.Request.Context(), domain, container, port)
	if err != nil {
		if errors.Is(err, npm.ErrDomainConflict) {
			h.renderLinksTab(c, fmt.Sprintf("Domain %q is already in use on the NPM server.", domain), "danger")
			return
		}
		log.Printf("Deploy: creating NPM proxy host failed: %v", err)
		h.renderLinksTab(c, fmt.Sprintf("Failed to create proxy host: %v", err), "danger")
		return
	}

	if _, err := h.store.Upsert(c.Request.Context(), container, services.ServiceNPM, domain); err != nil {
		log.Printf("Deploy: saving proxy-host link failed: %v", err)
		h.renderLinksTab(c, "Proxy host created but storing the link failed.", "danger")
		return
	}

	h.renderLinksTab(c, fmt.Sprintf("Created NPM proxy host %d for %s → %s.", id, domain, container), "success")
}

// containerPort resolves the container's published port from discovery,
// falling back to the user-provided port form value.
func (h *DeployHandler) containerPort(c *gin.Context, container, fallbackPort string) (int, error) {
	if dc, ok := h.findContainer(c.Request.Context(), container); ok && dc.Port > 0 {
		return dc.Port, nil
	}
	if fallbackPort != "" {
		p, err := strconv.Atoi(strings.TrimSpace(fallbackPort))
		if err == nil && p > 0 && p <= 65535 {
			return p, nil
		}
	}
	return 0, &linkError{msg: fmt.Sprintf("No published port found for %q — provide one explicitly.", container)}
}

// AutheliaYAML handles GET /deploy/links/authelia/yaml?domain=... and
// returns the access_control.rules snippet as text/plain for copy.
func (h *DeployHandler) AutheliaYAML(c *gin.Context) {
	domain := strings.TrimSpace(c.Query("domain"))
	out, err := authelia.AccessRuleYAML(domain)
	if err != nil {
		c.String(http.StatusBadRequest, "invalid domain")
		return
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusOK, "%s", out)
}
