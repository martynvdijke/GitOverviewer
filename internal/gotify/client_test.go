package gotify_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"gitlens/internal/gotify"
)

func TestNew_NilWhenUnconfigured(t *testing.T) {
	os.Unsetenv("GOTIFY_URL")
	os.Unsetenv("GOTIFY_TOKEN")
	if c := gotify.New(); c != nil {
		t.Fatal("expected nil client when env unset")
	}
}

func TestNew_NilWhenTokenMissing(t *testing.T) {
	os.Setenv("GOTIFY_URL", "http://gotify:8080")
	os.Unsetenv("GOTIFY_TOKEN")
	defer os.Unsetenv("GOTIFY_URL")
	if c := gotify.New(); c != nil {
		t.Fatal("expected nil client when token unset")
	}
}

func TestNew_NilWhenURLMissing(t *testing.T) {
	os.Unsetenv("GOTIFY_URL")
	os.Setenv("GOTIFY_TOKEN", "abc123")
	defer os.Unsetenv("GOTIFY_TOKEN")
	if c := gotify.New(); c != nil {
		t.Fatal("expected nil client when url unset")
	}
}

func TestNewWith_NilWhenEitherEmpty(t *testing.T) {
	if c := gotify.NewWith("", "tok"); c != nil {
		t.Fatal("expected nil client when url empty")
	}
	if c := gotify.NewWith("http://gotify:8080", ""); c != nil {
		t.Fatal("expected nil client when token empty")
	}
	if c := gotify.NewWith("", ""); c != nil {
		t.Fatal("expected nil client when both empty")
	}
}

func TestNewWith_NonNilWhenBothSet(t *testing.T) {
	if c := gotify.NewWith("http://gotify:8080", "tok"); c == nil {
		t.Fatal("expected non-nil client when both set")
	}
}

func TestSend_NilClient(t *testing.T) {
	// Sending on nil client is a no-op
	var c *gotify.Client
	if err := c.Send(context.Background(), "t", "m", 0); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestSend_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/message" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("token") != "testtoken" {
			t.Fatalf("unexpected token: %s", r.URL.Query().Get("token"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	os.Setenv("GOTIFY_URL", srv.URL)
	os.Setenv("GOTIFY_TOKEN", "testtoken")
	defer os.Unsetenv("GOTIFY_URL")
	defer os.Unsetenv("GOTIFY_TOKEN")

	c := gotify.New()
	if c == nil {
		t.Fatal("expected non-nil client")
	}

	if err := c.Send(context.Background(), "Test Title", "Test body", 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSend_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	os.Setenv("GOTIFY_URL", srv.URL)
	os.Setenv("GOTIFY_TOKEN", "tok")
	defer os.Unsetenv("GOTIFY_URL")
	defer os.Unsetenv("GOTIFY_TOKEN")

	c := gotify.New()
	if err := c.Send(context.Background(), "t", "m", 1); err == nil {
		t.Fatal("expected error for 502")
	}
}

func TestSend_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	os.Setenv("GOTIFY_URL", srv.URL)
	os.Setenv("GOTIFY_TOKEN", "tok")
	defer os.Unsetenv("GOTIFY_URL")
	defer os.Unsetenv("GOTIFY_TOKEN")

	c := gotify.New()
	if err := c.Send(context.Background(), "t", "m", 5); err == nil {
		t.Fatal("expected error for 500")
	}
}
