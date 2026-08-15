package npm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testIdentity = "admin@example.com"
	testSecret   = "s3cret"
)

// newTestServer returns an httptest server emulating the NPM REST API.
// taken lists domain names that already exist (POST returns 409).
// hosts maps existing hostname -> enabled state for GET /api/nginx/proxy-hosts.
func newTestServer(t *testing.T, taken []string, hosts map[string]bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var creds struct {
			Identity string `json:"identity"`
			Secret   string `json:"secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if creds.Identity != testIdentity || creds.Secret != testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "test-token"})
	})

	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			var out []proxyHost
			id := 1
			for hostname, enabled := range hosts {
				out = append(out, proxyHost{ID: id, DomainNames: []string{hostname}, Enabled: enabled})
				id++
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		case http.MethodPost:
			var req proxyHostRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if len(req.DomainNames) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for _, dn := range req.DomainNames {
				for _, takenDomain := range taken {
					if dn == takenDomain {
						w.WriteHeader(http.StatusConflict)
						return
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(proxyHostResponse{ID: 42})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestNew_NilWhenUnset(t *testing.T) {
	t.Setenv("NPM_URL", "")
	t.Setenv("NPM_IDENTITY", "")
	t.Setenv("NPM_SECRET", "")
	if c := New(); c != nil {
		t.Fatalf("expected nil client when all env vars unset, got %+v", c)
	}
}

func TestNew_Configured(t *testing.T) {
	t.Setenv("NPM_URL", "https://npm.example.com")
	t.Setenv("NPM_IDENTITY", testIdentity)
	t.Setenv("NPM_SECRET", testSecret)
	c := New()
	if c == nil {
		t.Fatal("expected non-nil client when env vars set")
	}
	if c.baseURL != "https://npm.example.com" {
		t.Fatalf("unexpected baseURL %q", c.baseURL)
	}
}

func TestNewWith_NilWhenEmpty(t *testing.T) {
	if c := NewWith("", testIdentity, testSecret); c != nil {
		t.Fatal("expected nil when baseURL empty")
	}
	if c := NewWith("https://npm.example.com", "", testSecret); c != nil {
		t.Fatal("expected nil when identity empty")
	}
	if c := NewWith("https://npm.example.com", testIdentity, ""); c != nil {
		t.Fatal("expected nil when secret empty")
	}
}

func TestNilClient_NoOp(t *testing.T) {
	ctx := context.Background()
	if _, err := (*Client)(nil).CreateProxyHost(ctx, "app.example.com", "app", 8080); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	if _, err := (*Client)(nil).ProxyHostState(ctx, "app.example.com"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestCreateProxyHost_Success(t *testing.T) {
	srv := newTestServer(t, nil, nil)
	c := NewWith(srv.URL, testIdentity, testSecret)
	if c == nil {
		t.Fatal("expected client")
	}
	id, err := c.CreateProxyHost(context.Background(), "app.example.com", "app", 8080)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 42 {
		t.Fatalf("expected proxy host ID 42, got %d", id)
	}
}

func TestCreateProxyHost_DuplicateDomain(t *testing.T) {
	srv := newTestServer(t, []string{"app.example.com"}, nil)
	c := NewWith(srv.URL, testIdentity, testSecret)
	_, err := c.CreateProxyHost(context.Background(), "app.example.com", "app", 8080)
	if !errors.Is(err, ErrDomainConflict) {
		t.Fatalf("expected ErrDomainConflict, got %v", err)
	}
}

func TestCreateProxyHost_LoginFailure(t *testing.T) {
	srv := newTestServer(t, nil, nil)
	c := NewWith(srv.URL, "wrong@example.com", "wrong")
	if c == nil {
		t.Fatal("expected client")
	}
	_, err := c.CreateProxyHost(context.Background(), "app.example.com", "app", 8080)
	if err == nil {
		t.Fatal("expected login failure")
	}
	if !strings.Contains(err.Error(), "login") {
		t.Fatalf("expected login error, got: %v", err)
	}
}

func TestProxyHostState(t *testing.T) {
	srv := newTestServer(t, nil, map[string]bool{
		"up.example.com":   true,
		"down.example.com": false,
	})
	c := NewWith(srv.URL, testIdentity, testSecret)

	state, err := c.ProxyHostState(context.Background(), "up.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != "enabled" {
		t.Fatalf("expected enabled, got %q", state)
	}

	state, err = c.ProxyHostState(context.Background(), "down.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != "disabled" {
		t.Fatalf("expected disabled, got %q", state)
	}

	_, err = c.ProxyHostState(context.Background(), "missing.example.com")
	if err == nil {
		t.Fatal("expected error for unknown hostname")
	}
}

func TestProxyHostState_ServerUnreachable(t *testing.T) {
	// No server: connection refused -> error surfaces, caller degrades to unknown.
	c := NewWith("http://127.0.0.1:1", testIdentity, testSecret)
	_, err := c.ProxyHostState(context.Background(), "app.example.com")
	if err == nil {
		t.Fatal("expected error when server unreachable")
	}
}
