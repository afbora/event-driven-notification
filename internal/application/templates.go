package application

import (
	"context"
	"fmt"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// The five template use cases below mirror the five REST verbs the
// HTTP adapter exposes. Each is intentionally thin — a stable seam
// between the HTTP boundary and the storage port, with room to grow
// when authorization or audit logging arrives.

// Pagination knobs for ListTemplates. ports.TemplateRepository.List
// does not yet expose a cursor, so the use case applies the limit only
// and the HTTP response never carries a next_cursor. When the
// repository grows cursor support (phase 5/6) the use case picks it
// up via the same input field.
const (
	defaultTemplateListLimit = 20
	maxTemplateListLimit     = 100
)

// --- CreateTemplate ------------------------------------------------------

// CreateTemplateInput is the payload accepted by CreateTemplate.
type CreateTemplateInput struct {
	Name    string
	Channel string
	Body    string
}

// CreateTemplate persists a new template under a server-allocated id.
// Channel is parsed via domain.NewChannel so an invalid value
// surfaces ErrInvalidChannel before the repository is touched.
type CreateTemplate struct {
	repo  ports.TemplateRepository
	idGen ports.IDGenerator
	clock ports.Clock
}

// NewCreateTemplate wires the dependencies.
func NewCreateTemplate(repo ports.TemplateRepository, idGen ports.IDGenerator, clock ports.Clock) *CreateTemplate {
	return &CreateTemplate{repo: repo, idGen: idGen, clock: clock}
}

// Execute parses the channel, builds a domain.Template, and persists it.
func (uc *CreateTemplate) Execute(ctx context.Context, in CreateTemplateInput) (*domain.Template, error) {
	channel, err := domain.NewChannel(in.Channel)
	if err != nil {
		return nil, err
	}

	tpl, err := domain.NewTemplate(domain.NewTemplateInput{
		ID:      uc.idGen.NewTemplateID(),
		Name:    in.Name,
		Channel: channel,
		Body:    in.Body,
	}, uc.clock.Now())
	if err != nil {
		return nil, err
	}

	if err := uc.repo.Create(ctx, tpl); err != nil {
		return nil, fmt.Errorf("create template: %w", err)
	}
	return tpl, nil
}

// --- GetTemplate ---------------------------------------------------------

// GetTemplateInput is the parameter bundle for GetTemplate.
type GetTemplateInput struct {
	ID domain.TemplateID
}

// GetTemplate is a thin passthrough — the repository decides what
// "found" means. ports.ErrNotFound bubbles up unchanged.
type GetTemplate struct {
	repo ports.TemplateRepository
}

// NewGetTemplate wires the dependency.
func NewGetTemplate(repo ports.TemplateRepository) *GetTemplate {
	return &GetTemplate{repo: repo}
}

// Execute delegates to the repository.
func (uc *GetTemplate) Execute(ctx context.Context, in GetTemplateInput) (*domain.Template, error) {
	return uc.repo.Get(ctx, in.ID)
}

// --- ListTemplates -------------------------------------------------------

// ListTemplatesInput is the parameter bundle for ListTemplates. Channel
// filtering and cursor pagination are accepted on the input side for
// API compatibility but the repository does not yet honor them — they
// are reserved for a follow-up that extends the port.
type ListTemplatesInput struct {
	Channel string
	Cursor  string
	Limit   int
}

// ListTemplatesOutput carries one page of templates plus a (currently
// always empty) next cursor for forward compatibility.
type ListTemplatesOutput struct {
	Templates  []*domain.Template
	NextCursor string
}

// ListTemplates returns one page of templates. Out-of-range limits
// snap to the use case default so API callers cannot exhaust the
// database with a single wide query.
type ListTemplates struct {
	repo ports.TemplateRepository
}

// NewListTemplates wires the dependency.
func NewListTemplates(repo ports.TemplateRepository) *ListTemplates {
	return &ListTemplates{repo: repo}
}

// Execute clamps the limit and delegates to the repository.
func (uc *ListTemplates) Execute(ctx context.Context, in ListTemplatesInput) (ListTemplatesOutput, error) {
	limit := in.Limit
	if limit <= 0 || limit > maxTemplateListLimit {
		limit = defaultTemplateListLimit
	}

	items, err := uc.repo.List(ctx, limit)
	if err != nil {
		return ListTemplatesOutput{}, fmt.Errorf("list templates: %w", err)
	}
	return ListTemplatesOutput{Templates: items, NextCursor: ""}, nil
}

// --- ReplaceTemplate -----------------------------------------------------

// ReplaceTemplateInput is the payload accepted by ReplaceTemplate
// (PUT semantics — every field is required and replaces the existing
// value).
type ReplaceTemplateInput struct {
	ID      domain.TemplateID
	Name    string
	Channel string
	Body    string
}

// ReplaceTemplate updates an existing template's fields. CreatedAt is
// preserved from the original record; UpdatedAt is set from the clock.
// Channel parsing happens before any side effect.
type ReplaceTemplate struct {
	repo  ports.TemplateRepository
	clock ports.Clock
}

// NewReplaceTemplate wires the dependencies.
func NewReplaceTemplate(repo ports.TemplateRepository, clock ports.Clock) *ReplaceTemplate {
	return &ReplaceTemplate{repo: repo, clock: clock}
}

// Execute fetches the existing template, builds a new validated
// Template with the same id, restores the original CreatedAt, then
// calls repo.Update.
func (uc *ReplaceTemplate) Execute(ctx context.Context, in ReplaceTemplateInput) (*domain.Template, error) {
	existing, err := uc.repo.Get(ctx, in.ID)
	if err != nil {
		return nil, err
	}

	channel, err := domain.NewChannel(in.Channel)
	if err != nil {
		return nil, err
	}

	now := uc.clock.Now()
	updated, err := domain.NewTemplate(domain.NewTemplateInput{
		ID:      in.ID,
		Name:    in.Name,
		Channel: channel,
		Body:    in.Body,
	}, now)
	if err != nil {
		return nil, err
	}
	// NewTemplate sets CreatedAt = now; PUT semantics preserve the
	// original creation timestamp so callers can audit how long the
	// resource has existed regardless of how many times it was edited.
	updated.CreatedAt = existing.CreatedAt

	if err := uc.repo.Update(ctx, updated); err != nil {
		return nil, fmt.Errorf("update template: %w", err)
	}
	return updated, nil
}

// --- DeleteTemplate ------------------------------------------------------

// DeleteTemplateInput is the parameter bundle for DeleteTemplate.
type DeleteTemplateInput struct {
	ID domain.TemplateID
}

// DeleteTemplate removes a template. ports.ErrNotFound from the
// repository propagates so the HTTP layer can return a 404.
type DeleteTemplate struct {
	repo ports.TemplateRepository
}

// NewDeleteTemplate wires the dependency.
func NewDeleteTemplate(repo ports.TemplateRepository) *DeleteTemplate {
	return &DeleteTemplate{repo: repo}
}

// Execute delegates to the repository.
func (uc *DeleteTemplate) Execute(ctx context.Context, in DeleteTemplateInput) error {
	return uc.repo.Delete(ctx, in.ID)
}
