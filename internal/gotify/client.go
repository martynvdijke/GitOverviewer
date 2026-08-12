package gotify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
)

// Client sends push notifications via a Gotify server.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// message is the JSON payload for Gotify's /message endpoint.
type message struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
}

// New creates a Gotify client from env vars.
// Returns nil if GOTIFY_URL or GOTIFY_TOKEN is not set.
func New() *Client {
	return NewWith(os.Getenv("GOTIFY_URL"), os.Getenv("GOTIFY_TOKEN"))
}

// NewWith creates a Gotify client from explicit url and token (e.g. values
// saved through the admin panel). Returns nil if either is empty.
func NewWith(baseURL, token string) *Client {
	if baseURL == "" || token == "" {
		return nil
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{},
	}
}

// Send sends a push notification. Priority 0-10 (Gotify default: 5).
// Returns nil if the client is nil (no-op when Gotify is not configured).
func (c *Client) Send(ctx context.Context, title, messageText string, priority int) error {
	if c == nil {
		return nil
	}

	body := message{
		Title:    title,
		Message:  messageText,
		Priority: priority,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("gotify: marshal: %w", err)
	}

	url := c.baseURL + "/message?token=" + c.token
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("gotify: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("gotify: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("gotify: %s", resp.Status)
	}

	log.Printf("Gotify: sent notification: %s", title)
	return nil
}
