package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/afbora/event-driven-notification/internal/adapters/postgres/sqlc"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// TemplateRepository is the postgres-backed implementation of
// ports.TemplateRepository. Templates are looked up by id (UUID) or by Name
// (the human-readable handle). Update and Delete are infrastructure
// operations exposed to the HTTP template-management endpoints (phase 4).
type TemplateRepository struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
}

// NewTemplateRepository wires a pgxpool.Pool into a repository.
func NewTemplateRepository(pool *pgxpool.Pool) *TemplateRepository {
	return &TemplateRepository{
		pool: pool,
		q:    sqlc.New(pool),
	}
}

// parseTemplateIDErr formats the standard wrap used wherever a
// TemplateID string fails to parse as a pgtype UUID. Centralizing the
// format satisfies SonarCloud S1192 (literal duplicated four times)
// without scattering a const across the four CRUD methods.
func parseTemplateIDErr(id domain.TemplateID, err error) error {
	return fmt.Errorf("parse template id %q: %w", id, err)
}

// Create persists a new template.
func (r *TemplateRepository) Create(ctx context.Context, t *domain.Template) error {
	id, err := parseUUID(string(t.ID))
	if err != nil {
		return parseTemplateIDErr(t.ID, err)
	}
	if err := r.q.CreateTemplate(ctx, sqlc.CreateTemplateParams{
		ID:        id,
		Name:      t.Name,
		Channel:   string(t.Channel),
		Body:      t.Body,
		CreatedAt: timeToTimestamptz(t.CreatedAt),
		UpdatedAt: timeToTimestamptz(t.UpdatedAt),
	}); err != nil {
		return fmt.Errorf("create template %s: %w", t.ID, err)
	}
	return nil
}

// Get returns the template with the given id, or ports.ErrNotFound.
func (r *TemplateRepository) Get(ctx context.Context, id domain.TemplateID) (*domain.Template, error) {
	pgID, err := parseUUID(string(id))
	if err != nil {
		return nil, parseTemplateIDErr(id, err)
	}
	row, err := r.q.GetTemplate(ctx, pgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: template %s", ports.ErrNotFound, id)
		}
		return nil, fmt.Errorf("get template %s: %w", id, err)
	}
	return templateFromRow(row)
}

// GetByName returns the template with the given name, or ports.ErrNotFound.
// Templates are identified by name in human-facing flows; the unique index
// in db/migrations/000001 enforces uniqueness.
func (r *TemplateRepository) GetByName(ctx context.Context, name string) (*domain.Template, error) {
	row, err := r.q.GetTemplateByName(ctx, name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: template name %q", ports.ErrNotFound, name)
		}
		return nil, fmt.Errorf("get template by name %q: %w", name, err)
	}
	return templateFromRow(row)
}

// List returns up to `limit` templates ordered by name.
func (r *TemplateRepository) List(ctx context.Context, limit int) ([]*domain.Template, error) {
	rows, err := r.q.ListTemplates(ctx, int32(limit)) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	out := make([]*domain.Template, 0, len(rows))
	for _, row := range rows {
		tmpl, err := templateFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, tmpl)
	}
	return out, nil
}

// Update persists changes to name, channel, and body. Returns
// ports.ErrNotFound when the id does not exist.
func (r *TemplateRepository) Update(ctx context.Context, t *domain.Template) error {
	pgID, err := parseUUID(string(t.ID))
	if err != nil {
		return parseTemplateIDErr(t.ID, err)
	}
	if err := r.q.UpdateTemplate(ctx, sqlc.UpdateTemplateParams{
		ID:      pgID,
		Name:    t.Name,
		Channel: string(t.Channel),
		Body:    t.Body,
	}); err != nil {
		return fmt.Errorf("update template %s: %w", t.ID, err)
	}
	return nil
}

// Delete removes the template by id. Notifications that reference it via
// template_id are FK-set-null'd by the schema.
func (r *TemplateRepository) Delete(ctx context.Context, id domain.TemplateID) error {
	pgID, err := parseUUID(string(id))
	if err != nil {
		return fmt.Errorf("parse template id %q: %w", id, err)
	}
	if err := r.q.DeleteTemplate(ctx, pgID); err != nil {
		return fmt.Errorf("delete template %s: %w", id, err)
	}
	return nil
}

// templateFromRow converts a sqlc Template into a domain.Template.
func templateFromRow(row sqlc.Template) (*domain.Template, error) {
	id, err := uuidToString(row.ID)
	if err != nil {
		return nil, fmt.Errorf("template id: %w", err)
	}
	return &domain.Template{
		ID:        domain.TemplateID(id),
		Name:      row.Name,
		Channel:   domain.Channel(row.Channel),
		Body:      row.Body,
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}, nil
}
