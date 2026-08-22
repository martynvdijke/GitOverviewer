package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// Client sends push notifications via the Telegram Bot API.
type Client struct {
	token  string
	chatID string
	http   *http.Client
}

// sendMessage is the JSON payload for the Bot API sendMessage endpoint.
type sendMessage struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

// NewWith creates a Telegram client from a bot token and chat ID (values
// saved through the admin panel). Returns nil if either is empty.
func NewWith(token, chatID string) *Client {
	if token == "" || chatID == "" {
		return nil
	}
	return &Client{
		token:  token,
		chatID: chatID,
		http:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Send delivers title and messageText as plain text to the configured chat
// via the Bot API sendMessage endpoint. priority is accepted so the client
// satisfies the handlers notifier interface; Telegram has no priority
// concept, so it is ignored. Returns nil if the client is nil (no-op when
// Telegram is not configured).
func (c *Client) Send(ctx context.Context, title, messageText string, priority int) error {
	if c == nil {
		return nil
	}

	payload, err := json.Marshal(sendMessage{
		ChatID: c.chatID,
		Text:   title + "\n" + messageText,
	})
	if err != nil {
		return fmt.Errorf("telegram: marshal: %w", err)
	}

	url := "https://api.telegram.org/bot" + c.token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telegram: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram: %s", resp.Status)
	}

	log.Printf("Telegram: sent notification: %s", title)
	return nil
}
