package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNewWith_BlankCredentials(t *testing.T) {
	if NewWith("", "123") != nil {
		t.Error("expected nil client when token is blank")
	}
	if NewWith("tok", "") != nil {
		t.Error("expected nil client when chat ID is blank")
	}
	if NewWith("tok", "123") == nil {
		t.Error("expected non-nil client with both credentials set")
	}
}

func TestSend_Success(t *testing.T) {
	var gotPath string
	var gotPayload sendMessage
	c := NewWith("sekrit", "42")
	c.http = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotPayload)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}

	err := c.Send(context.Background(), "[GitLens]", "deploy ok", 5)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !strings.HasSuffix(gotPath, "/botsekrit/sendMessage") {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotPayload.ChatID != "42" {
		t.Errorf("unexpected chat_id: %q", gotPayload.ChatID)
	}
	if !strings.Contains(gotPayload.Text, "[GitLens]") || !strings.Contains(gotPayload.Text, "deploy ok") {
		t.Errorf("expected title and body in text, got %q", gotPayload.Text)
	}
}

func TestSend_APIError(t *testing.T) {
	c := NewWith("bad", "1")
	c.http = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}

	if err := c.Send(context.Background(), "t", "m", 0); err == nil {
		t.Fatal("expected error on 401 response")
	}
}

func TestSend_NetworkError(t *testing.T) {
	c := NewWith("t", "1")
	c.http = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}

	if err := c.Send(context.Background(), "t", "m", 0); err == nil {
		t.Fatal("expected error on network failure")
	}
}

func TestSend_NilClientNoOp(t *testing.T) {
	var c *Client
	if err := c.Send(context.Background(), "t", "m", 0); err != nil {
		t.Fatalf("expected nil error from nil client, got %v", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
