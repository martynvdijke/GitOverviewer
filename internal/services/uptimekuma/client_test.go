package uptimekuma

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/breml/go-uptime-kuma-client/monitor"
)

// fakeSession records calls and returns canned results.
type fakeSession struct {
	createErr     error
	createID      int64
	statusActive  bool // canned status returned for GetMonitorAs
	statusValue   int
	statusErr     error
	disconnected  bool
}

func (f *fakeSession) CreateMonitor(ctx context.Context, mon monitor.Monitor) (int64, error) {
	return f.createID, f.createErr
}

func (f *fakeSession) GetMonitorAs(ctx context.Context, monitorID int64, target any) error {
	if st, ok := target.(*monitorStatus); ok {
		st.Active = f.statusActive
		st.Status = f.statusValue
	}
	return f.statusErr
}

func (f *fakeSession) Disconnect() error {
	f.disconnected = true
	return nil
}

// fakeFactory returns a client wired to the given fake session.
func fakeFactory(t *testing.T, s session) *Client {
	t.Helper()
	return &Client{
		baseURL:  "http://kuma:3001",
		username: "user",
		password: "pass",
		timeout:  time.Second,
		newSession: func(ctx context.Context, baseURL, username, password string) (session, error) {
			return s, nil
		},
	}
}

func TestNew_NilWhenUnset(t *testing.T) {
	t.Setenv("KUMA_URL", "")
	t.Setenv("KUMA_USERNAME", "")
	t.Setenv("KUMA_PASSWORD", "")
	if c := New(); c != nil {
		t.Fatalf("expected nil client when env unset, got %+v", c)
	}

	t.Setenv("KUMA_URL", "http://kuma:3001")
	t.Setenv("KUMA_USERNAME", "u")
	if c := New(); c != nil {
		t.Fatalf("expected nil client when password unset, got %+v", c)
	}

	t.Setenv("KUMA_PASSWORD", "p")
	if c := New(); c == nil {
		t.Fatal("expected non-nil client when all env set")
	}
}

func TestNilClient_NoOp(t *testing.T) {
	var c *Client
	if _, err := c.AddHTTPMonitor(context.Background(), "n", "http://x"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	if _, err := c.MonitorStatus(context.Background(), 1); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestAddHTTPMonitor_Success(t *testing.T) {
	fs := &fakeSession{createID: 42}
	c := fakeFactory(t, fs)

	id, err := c.AddHTTPMonitor(context.Background(), "my-app", "http://my-app:8080")
	if err != nil {
		t.Fatalf("AddHTTPMonitor: %v", err)
	}
	if id != 42 {
		t.Fatalf("expected id 42, got %d", id)
	}
	if !fs.disconnected {
		t.Fatal("expected session to be disconnected")
	}
}

func TestAddHTTPMonitor_Failure(t *testing.T) {
	fs := &fakeSession{createErr: errors.New("kuma exploded")}
	c := fakeFactory(t, fs)

	if _, err := c.AddHTTPMonitor(context.Background(), "n", "http://x"); err == nil {
		t.Fatal("expected error from failed monitor creation")
	}
}

func TestAddHTTPMonitor_LoginFailure(t *testing.T) {
	c := &Client{
		baseURL:  "http://kuma:3001",
		username: "u",
		password: "p",
		timeout:  time.Second,
		newSession: func(ctx context.Context, baseURL, username, password string) (session, error) {
			return nil, errors.New("login failed")
		},
	}
	if _, err := c.AddHTTPMonitor(context.Background(), "n", "http://x"); err == nil {
		t.Fatal("expected login error")
	}
}

func TestMonitorStatus(t *testing.T) {
	cases := []struct {
		name   string
		active bool
		status int
		want   string
	}{
		{"up", true, 0, "up"},
		{"down", true, 1, "down"},
		{"pending", true, 2, "pending"},
		{"maintenance", true, 3, "unknown"},
		{"paused", false, 1, "paused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeSession{statusActive: tc.active, statusValue: tc.status}
			c := fakeFactory(t, fs)

			got, err := c.MonitorStatus(context.Background(), 5)
			if err != nil {
				t.Fatalf("MonitorStatus: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestMonitorStatus_Error(t *testing.T) {
	fs := &fakeSession{statusErr: errors.New("kuma down")}
	c := fakeFactory(t, fs)
	if _, err := c.MonitorStatus(context.Background(), 5); err == nil {
		t.Fatal("expected error when status lookup fails")
	}
}

func TestParseMonitorID(t *testing.T) {
	if id, err := ParseMonitorID("123"); err != nil || id != 123 {
		t.Fatalf("expected 123, got %d (%v)", id, err)
	}
	for _, bad := range []string{"", "abc", "0", "-5", "1.5"} {
		if _, err := ParseMonitorID(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}
