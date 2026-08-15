package services

import (
	"context"
	"testing"

	"gitlens/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })
	return NewStore(client)
}

func TestStore_UpsertAndGet(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	got, err := s.Upsert(ctx, "gitlens", ServiceUptimeKuma, "42")
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got == nil || got.Container != "gitlens" || got.Service != ServiceUptimeKuma || got.Reference != "42" {
		t.Fatalf("unexpected link after upsert: %+v", got)
	}

	// Upserting the same pair updates the reference (unique container+service).
	got, err = s.Upsert(ctx, "gitlens", ServiceUptimeKuma, "99")
	if err != nil {
		t.Fatalf("Upsert (update): %v", err)
	}
	if got.Reference != "99" {
		t.Fatalf("expected reference updated to 99, got %q", got.Reference)
	}

	// Get reflects the update.
	link, err := s.Get(ctx, "gitlens", ServiceUptimeKuma)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if link == nil || link.Reference != "99" {
		t.Fatalf("unexpected link: %+v", link)
	}
}

func TestStore_GetMissing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	link, err := s.Get(ctx, "nope", ServiceAuthelia)
	if err != nil {
		t.Fatalf("Get missing: %v", err)
	}
	if link != nil {
		t.Fatalf("expected nil for missing link, got %+v", link)
	}
}

func TestStore_Delete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Upsert(ctx, "app", ServiceNPM, "host1"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := s.Delete(ctx, "app", ServiceNPM); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	link, err := s.Get(ctx, "app", ServiceNPM)
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if link != nil {
		t.Fatalf("expected nil after delete, got %+v", link)
	}

	// Deleting a non-existent link is a no-op.
	if err := s.Delete(ctx, "app", ServiceNPM); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}

func TestStore_ByContainerAndService(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for _, tc := range []struct {
		container string
		service   Service
		reference string
	}{
		{"a", ServiceUptimeKuma, "1"},
		{"a", ServiceAuthelia, "a.example.com"},
		{"b", ServiceNPM, "b.example.com"},
		{"b", ServiceUptimeKuma, "2"},
	} {
		if _, err := s.Upsert(ctx, tc.container, tc.service, tc.reference); err != nil {
			t.Fatalf("Upsert(%s,%s): %v", tc.container, tc.service, err)
		}
	}

	byContainer, err := s.ByContainer(ctx, "a")
	if err != nil {
		t.Fatalf("ByContainer: %v", err)
	}
	if len(byContainer) != 2 {
		t.Fatalf("expected 2 links for container a, got %d", len(byContainer))
	}
	if byContainer[0].Service != ServiceAuthelia || byContainer[1].Service != ServiceUptimeKuma {
		t.Fatalf("expected links sorted by service, got %+v", byContainer)
	}

	byService, err := s.ByService(ctx, ServiceUptimeKuma)
	if err != nil {
		t.Fatalf("ByService: %v", err)
	}
	if len(byService) != 2 {
		t.Fatalf("expected 2 uptime_kuma links, got %d", len(byService))
	}
	if byService[0].Container != "a" || byService[1].Container != "b" {
		t.Fatalf("expected links sorted by container, got %+v", byService)
	}

	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 links total, got %d", len(all))
	}
}

func TestStore_SetLiveState(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Upsert(ctx, "app", ServiceUptimeKuma, "7"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := s.SetLiveState(ctx, "app", ServiceUptimeKuma, "up"); err != nil {
		t.Fatalf("SetLiveState: %v", err)
	}
	link, err := s.Get(ctx, "app", ServiceUptimeKuma)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if link.LiveState != "up" {
		t.Fatalf("expected live_state up, got %q", link.LiveState)
	}
}
