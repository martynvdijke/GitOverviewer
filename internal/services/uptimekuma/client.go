// Package uptimekuma provides a client for the Uptime Kuma monitoring
// server's socket.io API. Credentials come from environment variables and
// the client is nil-safe: New() returns nil when not configured, and
// calling methods on a nil client is a no-op (mirrors internal/gotify).
package uptimekuma

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	kuma "github.com/breml/go-uptime-kuma-client"
	"github.com/breml/go-uptime-kuma-client/monitor"
)

// ErrNotConfigured is returned when the client is nil (env vars unset) or
// the session factory is unavailable.
var ErrNotConfigured = errors.New("uptimekuma: not configured")

// session abstracts the socket.io connection to Uptime Kuma so the
// underlying client library stays swappable and mockable in tests.
type session interface {
	CreateMonitor(ctx context.Context, mon monitor.Monitor) (int64, error)
	GetMonitorAs(ctx context.Context, monitorID int64, target any) error
	Disconnect() error
}

// sessionFactory opens a logged-in session. The default implementation
// wraps the socket.io client library; tests substitute a fake.
type sessionFactory func(ctx context.Context, baseURL, username, password string) (session, error)

// Client talks to an Uptime Kuma server.
type Client struct {
	baseURL    string
	username   string
	password   string
	newSession sessionFactory
	timeout    time.Duration
}

// New creates a Client from environment variables.
// Returns nil if KUMA_URL, KUMA_USERNAME or KUMA_PASSWORD is not set.
func New() *Client {
	return NewWith(
		os.Getenv("KUMA_URL"),
		os.Getenv("KUMA_USERNAME"),
		os.Getenv("KUMA_PASSWORD"),
	)
}

// NewWith creates a Client from explicit credentials (used by tests and
// future admin-panel configuration). Returns nil if any value is empty.
func NewWith(baseURL, username, password string) *Client {
	if baseURL == "" || username == "" || password == "" {
		return nil
	}
	return &Client{
		baseURL:  baseURL,
		username: username,
		password: password,
		newSession: func(ctx context.Context, baseURL, username, password string) (session, error) {
			return kuma.New(ctx, baseURL, username, password,
				kuma.WithConnectTimeout(3*time.Second))
		},
		timeout: 3 * time.Second,
	}
}

// connect opens a fresh logged-in session. Each operation uses its own
// short-lived connection so failures (and credential rotation) are
// isolated; the dashboard also degrades gracefully when Kuma is down.
func (c *Client) connect(ctx context.Context) (session, error) {
	if c == nil {
		return nil, ErrNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	s, err := c.newSession(ctx, c.baseURL, c.username, c.password)
	if err != nil {
		return nil, fmt.Errorf("uptimekuma: connect/login: %w", err)
	}
	return s, nil
}

// AddHTTPMonitor creates an HTTP monitor for url and returns its ID.
// Returns ErrNotConfigured on a nil client.
func (c *Client) AddHTTPMonitor(ctx context.Context, name, url string) (int64, error) {
	if c == nil {
		return 0, ErrNotConfigured
	}
	s, err := c.connect(ctx)
	if err != nil {
		return 0, err
	}
	defer s.Disconnect()

	mon := &monitor.HTTP{
		Base: monitor.Base{
			Name:     name,
			Interval: 60, // seconds between checks
		},
		HTTPDetails: monitor.HTTPDetails{
			URL:                 url,
			Method:              "GET",
			AcceptedStatusCodes: []string{"200-299"},
		},
	}

	id, err := s.CreateMonitor(ctx, mon)
	if err != nil {
		return 0, fmt.Errorf("uptimekuma: create monitor: %w", err)
	}
	log.Printf("Uptime Kuma: created monitor %d for %s (%s)", id, name, url)
	return id, nil
}

// monitorStatus mirrors the subset of the Uptime Kuma monitor object we
// need for status display. The socket.io client exposes it through the
// raw JSON of the getMonitor response.
type monitorStatus struct {
	Active bool `json:"active"`
	Status int  `json:"status"` // 0=up, 1=down, 2=pending, 3=maintenance
}

// MonitorStatus resolves a monitor ID to its live state:
// "up", "down", "pending", "paused" (active=false), or "unknown".
// Returns ErrNotConfigured on a nil client.
func (c *Client) MonitorStatus(ctx context.Context, monitorID int64) (string, error) {
	if c == nil {
		return "", ErrNotConfigured
	}
	s, err := c.connect(ctx)
	if err != nil {
		return "", err
	}
	defer s.Disconnect()

	var st monitorStatus
	if err := s.GetMonitorAs(ctx, monitorID, &st); err != nil {
		return "", fmt.Errorf("uptimekuma: get monitor %d: %w", monitorID, err)
	}

	switch {
	case !st.Active:
		return "paused", nil
	case st.Status == 0:
		return "up", nil
	case st.Status == 1:
		return "down", nil
	case st.Status == 2:
		return "pending", nil
	default:
		return "unknown", nil
	}
}

// ParseMonitorID converts a user-supplied monitor ID string to int64.
func ParseMonitorID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid monitor ID %q", s)
	}
	return id, nil
}
