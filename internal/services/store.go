package services

import (
	"context"
	"fmt"
	"time"

	"gitlens/ent"
	"gitlens/ent/servicelink"
)

// Service identifies an external service a container can be linked to.
type Service string

// Service values used by the ServiceLink store. They mirror the
// servicelink.Service enum values so handlers and templates can compare
// against plain strings.
const (
	ServiceUptimeKuma Service = "uptime_kuma"
	ServiceNPM        Service = "npm"
	ServiceAuthelia   Service = "authelia"
)

// Link is a persisted container ↔ external-service link.
type Link struct {
	Container string    // docker container name
	Service   Service   // which external service the container is linked to
	Reference string    // service-side reference: monitor ID, proxy host ID, or domain
	LiveState string    // last known live status ("up", "down", "enabled", ...); best-effort
	UpdatedAt time.Time // when the link (or its live state) was last updated
}

// Store persists container ↔ service links in the database.
type Store struct {
	client *ent.Client
}

// NewStore creates a Store backed by the given ent client.
func NewStore(client *ent.Client) *Store {
	return &Store{client: client}
}

// Upsert creates the container↔service link or, if it already exists,
// updates its reference and clears the cached live state so it is
// re-resolved on the next dashboard render.
func (s *Store) Upsert(ctx context.Context, container string, service Service, reference string) (*Link, error) {
	if s == nil || s.client == nil {
		return nil, nil
	}
	if err := s.client.ServiceLink.Create().
		SetContainer(container).
		SetService(servicelink.Service(service)).
		SetReference(reference).
		OnConflictColumns(servicelink.FieldContainer, servicelink.FieldService).
		UpdateNewValues().
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("services: upsert link: %w", err)
	}
	return s.Get(ctx, container, service)
}

// Delete removes the container↔service link. It is a no-op when the link
// does not exist.
func (s *Store) Delete(ctx context.Context, container string, service Service) error {
	if s == nil || s.client == nil {
		return nil
	}
	if _, err := s.client.ServiceLink.Delete().
		Where(
			servicelink.Container(container),
			servicelink.ServiceEQ(servicelink.Service(service)),
		).
		Exec(ctx); err != nil {
		return fmt.Errorf("services: delete link: %w", err)
	}
	return nil
}

// Get returns a single link by container+service, or nil when absent.
func (s *Store) Get(ctx context.Context, container string, service Service) (*Link, error) {
	if s == nil || s.client == nil {
		return nil, nil
	}
	row, err := s.client.ServiceLink.Query().
		Where(
			servicelink.Container(container),
			servicelink.ServiceEQ(servicelink.Service(service)),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("services: get link: %w", err)
	}
	return toLink(row), nil
}

// ByContainer returns every link for the given container, sorted by service.
func (s *Store) ByContainer(ctx context.Context, container string) ([]Link, error) {
	if s == nil || s.client == nil {
		return nil, nil
	}
	rows, err := s.client.ServiceLink.Query().
		Where(servicelink.Container(container)).
		Order(servicelink.ByService()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("services: query links by container: %w", err)
	}
	return toLinks(rows), nil
}

// ByService returns every link for the given service, sorted by container.
func (s *Store) ByService(ctx context.Context, service Service) ([]Link, error) {
	if s == nil || s.client == nil {
		return nil, nil
	}
	rows, err := s.client.ServiceLink.Query().
		Where(servicelink.ServiceEQ(servicelink.Service(service))).
		Order(servicelink.ByContainer()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("services: query links by service: %w", err)
	}
	return toLinks(rows), nil
}

// All returns every link, sorted by container then service.
func (s *Store) All(ctx context.Context) ([]Link, error) {
	if s == nil || s.client == nil {
		return nil, nil
	}
	rows, err := s.client.ServiceLink.Query().
		Order(servicelink.ByContainer(), servicelink.ByService()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("services: query links: %w", err)
	}
	return toLinks(rows), nil
}

// SetLiveState persists the last-known live status of a link.
func (s *Store) SetLiveState(ctx context.Context, container string, service Service, state string) error {
	if s == nil || s.client == nil || state == "" {
		return nil
	}
	if _, err := s.client.ServiceLink.Update().
		Where(
			servicelink.Container(container),
			servicelink.ServiceEQ(servicelink.Service(service)),
		).
		SetLiveState(state).
		Save(ctx); err != nil {
		return fmt.Errorf("services: update live state: %w", err)
	}
	return nil
}

func toLink(row *ent.ServiceLink) *Link {
	if row == nil {
		return nil
	}
	return &Link{
		Container: row.Container,
		Service:   Service(row.Service),
		Reference: row.Reference,
		LiveState: row.LiveState,
		UpdatedAt: row.UpdatedAt,
	}
}

func toLinks(rows []*ent.ServiceLink) []Link {
	out := make([]Link, 0, len(rows))
	for _, r := range rows {
		out = append(out, *toLink(r))
	}
	return out
}
