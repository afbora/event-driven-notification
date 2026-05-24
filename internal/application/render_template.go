package application

import (
	"context"
	"fmt"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// RenderTemplateInput is the parameter bundle for RenderTemplate.
type RenderTemplateInput struct {
	TemplateID domain.TemplateID
	Variables  map[string]any
}

// RenderTemplate fetches a template by id and substitutes the supplied
// variables into its body via Go's text/template. The actual rendering
// (parse + execute + missingkey=error) lives on the domain entity; this
// use case is the thin layer that wires the repository to it.
//
// Used by the HTTP layer (POST /api/v1/templates/{id}/render) and any
// future use case that wants to materialize a notification body from a
// stored template — e.g. CreateNotification with a TemplateID could call
// this internally instead of duplicating the parse/execute logic.
type RenderTemplate struct {
	repo ports.TemplateRepository
}

// NewRenderTemplate wires the dependency.
func NewRenderTemplate(repo ports.TemplateRepository) *RenderTemplate {
	return &RenderTemplate{repo: repo}
}

// Execute runs the use case. Either of two error categories propagates
// unchanged: ports.ErrNotFound (template id unknown) and
// domain.ErrTemplateRenderFailed (missing variable, execute failure).
func (uc *RenderTemplate) Execute(ctx context.Context, in RenderTemplateInput) (string, error) {
	tmpl, err := uc.repo.Get(ctx, in.TemplateID)
	if err != nil {
		return "", fmt.Errorf("get template %s: %w", in.TemplateID, err)
	}
	content, err := tmpl.Render(in.Variables)
	if err != nil {
		return "", fmt.Errorf("render template %s: %w", in.TemplateID, err)
	}
	return content, nil
}
