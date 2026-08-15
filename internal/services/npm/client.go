package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrNotConfigured is returned when the NPM env vars are not set.
var ErrNotConfigured = errors.New("npm: not configured")

// ErrDomainConflict is returned when the requested domain is already in use
// on the NPM server (HTTP 409).
var ErrDomainConflict = errors.New("npm: domain already in use")

// Client talks to an Nginx Proxy Manager REST API.
type Client struct {
	baseURL  string
	identity string
	secret   string
	http     *http.Client
}

// New creates an NPM client from env vars.
// Returns nil if NPM_URL, NPM_IDENTITY or NPM_SECRET is not set.
func New() *Client {
	return NewWith(os.Getenv("NPM_URL"), os.Getenv("NPM_IDENTITY"), os.Getenv("NPM_SECRET"))
}

// NewWith creates an NPM client from explicit url, identity and secret
// (e.g. values from an admin panel). Returns nil if any is empty.
func NewWith(baseURL, identity, secret string) *Client {
	if baseURL == "" || identity == "" || secret == "" {
		return nil
	}
	return &Client{
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		identity: identity,
		secret:   secret,
		http:     &http.Client{Timeout: 10 * time.Second},
	}
}

// tokenResponse is the body of POST /api/tokens.
type tokenResponse struct {
	Token string `json:"token"`
}

// login authenticates against the NPM API and returns a bearer token.
// Returns nil if the client is nil (no-op when NPM is not configured).
func (c *Client) login(ctx context.Context) (string, error) {
	if c == nil {
		return "", ErrNotConfigured
	}

	payload, err := json.Marshal(map[string]string{
		"identity": c.identity,
		"secret":   c.secret,
	})
	if err != nil {
		return "", fmt.Errorf("npm: marshal login: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/tokens", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("npm: new login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("npm: login request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("npm: login: %s", resp.Status)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("npm: login decode: %w", err)
	}
	if tr.Token == "" {
		return "", fmt.Errorf("npm: login: empty token")
	}
	return tr.Token, nil
}

// proxyHostRequest is the body of POST /api/nginx/proxy-hosts.
type proxyHostRequest struct {
	DomainNames      []string `json:"domain_names"`
	ForwardScheme    string   `json:"forward_scheme"`
	ForwardHost      string   `json:"forward_host"`
	ForwardPort      int      `json:"forward_port"`
	WebsocketSupport bool     `json:"websocket_support"`
}

// proxyHostResponse is the body of POST /api/nginx/proxy-hosts.
type proxyHostResponse struct {
	ID int `json:"id"`
}

// CreateProxyHost creates a proxy host that forwards domain to host:port
// (with WebSocket support) and returns the created proxy-host ID.
// Returns nil if the client is nil (no-op when NPM is not configured).
func (c *Client) CreateProxyHost(ctx context.Context, domain, host string, port int) (int, error) {
	if c == nil {
		return 0, ErrNotConfigured
	}

	token, err := c.login(ctx)
	if err != nil {
		return 0, err
	}

	payload, err := json.Marshal(proxyHostRequest{
		DomainNames:      []string{domain},
		ForwardScheme:    "http",
		ForwardHost:      host,
		ForwardPort:      port,
		WebsocketSupport: true,
	})
	if err != nil {
		return 0, fmt.Errorf("npm: marshal proxy host: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/nginx/proxy-hosts", bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("npm: new proxy host request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("npm: create proxy host: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return 0, ErrDomainConflict
	}
	if resp.StatusCode >= 300 {
		return 0, fmt.Errorf("npm: create proxy host: %s", resp.Status)
	}

	var pr proxyHostResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return 0, fmt.Errorf("npm: create proxy host decode: %w", err)
	}

	log.Printf("NPM: created proxy host %d for %s -> %s:%d", pr.ID, domain, host, port)
	return pr.ID, nil
}

// proxyHost is one entry of GET /api/nginx/proxy-hosts.
type proxyHost struct {
	ID          int      `json:"id"`
	DomainNames []string `json:"domain_names"`
	Enabled     bool     `json:"enabled"`
}

// ProxyHostState reports the enabled/disabled state of the proxy host that
// serves the given hostname. Returns an error if no proxy host matches.
// Returns nil if the client is nil (no-op when NPM is not configured).
func (c *Client) ProxyHostState(ctx context.Context, hostname string) (string, error) {
	if c == nil {
		return "", ErrNotConfigured
	}

	token, err := c.login(ctx)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/nginx/proxy-hosts", nil)
	if err != nil {
		return "", fmt.Errorf("npm: new proxy hosts request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("npm: list proxy hosts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("npm: list proxy hosts: %s", resp.Status)
	}

	var hosts []proxyHost
	if err := json.NewDecoder(resp.Body).Decode(&hosts); err != nil {
		return "", fmt.Errorf("npm: list proxy hosts decode: %w", err)
	}

	for _, h := range hosts {
		for _, dn := range h.DomainNames {
			if dn == hostname {
				if h.Enabled {
					return "enabled", nil
				}
				return "disabled", nil
			}
		}
	}
	return "", fmt.Errorf("npm: no proxy host for hostname %q", hostname)
}
