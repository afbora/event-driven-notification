package http

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// Template handlers below dispatch the five CRUD verbs the API
// exposes on /api/v1/templates. Each one is a thin orchestration over
// the matching application use case; the only domain-specific work is
// in the toAPITemplate / fromAPI* mappers.

// wrapMapTemplate annotates a toAPITemplate failure with a consistent
// prefix. Centralizing the format satisfies SonarCloud S1192 (literal
// duplicated four times across the CRUD handlers and the page mapper).
func wrapMapTemplate(err error) error {
	return fmt.Errorf("map template: %w", err)
}

// --- POST /api/v1/templates ----------------------------------------------

// CreateTemplate creates a new template and returns 201 with the new
// resource and a Location header.
func (s *Server) CreateTemplate(ctx context.Context, req api.CreateTemplateRequestObject) (api.CreateTemplateResponseObject, error) {
	if s.createTemplate == nil {
		return nil, ErrNotImplemented
	}
	if req.Body == nil {
		return nil, &domain.ValidationError{Field: "body", Reason: "request body is required"}
	}

	tpl, err := s.createTemplate(ctx, application.CreateTemplateInput{
		Name:    req.Body.Name,
		Channel: string(req.Body.Channel),
		Body:    req.Body.Body,
	})
	if err != nil {
		return nil, err
	}

	out, err := toAPITemplate(tpl)
	if err != nil {
		return nil, wrapMapTemplate(err)
	}
	location := "/api/v1/templates/" + string(tpl.ID)
	return api.CreateTemplate201JSONResponse{
		Body: out,
		Headers: api.CreateTemplate201ResponseHeaders{
			Location: &location,
		},
	}, nil
}

// --- GET /api/v1/templates -----------------------------------------------

// ListTemplates returns a page of templates. The use case currently
// applies only the limit filter; cursor and channel filters are
// forward-compatible (the use case ignores them and the response
// omits next_cursor).
func (s *Server) ListTemplates(ctx context.Context, req api.ListTemplatesRequestObject) (api.ListTemplatesResponseObject, error) {
	if s.listTemplates == nil {
		return nil, ErrNotImplemented
	}

	in := application.ListTemplatesInput{}
	if req.Params.Channel != nil {
		in.Channel = string(*req.Params.Channel)
	}
	if req.Params.Cursor != nil {
		in.Cursor = *req.Params.Cursor
	}
	if req.Params.Limit != nil {
		in.Limit = *req.Params.Limit
	}

	out, err := s.listTemplates(ctx, in)
	if err != nil {
		return nil, err
	}

	page, err := toAPITemplatePage(out)
	if err != nil {
		return nil, fmt.Errorf("map template page: %w", err)
	}
	return api.ListTemplates200JSONResponse{Body: page}, nil
}

// --- GET /api/v1/templates/{id} ------------------------------------------

// GetTemplate returns a single template by id, or a 404 problem when
// the id is unknown.
func (s *Server) GetTemplate(ctx context.Context, req api.GetTemplateRequestObject) (api.GetTemplateResponseObject, error) {
	if s.getTemplate == nil {
		return nil, ErrNotImplemented
	}

	tpl, err := s.getTemplate(ctx, application.GetTemplateInput{
		ID: domain.TemplateID(req.Id.String()),
	})
	if err != nil {
		return nil, err
	}

	out, err := toAPITemplate(tpl)
	if err != nil {
		return nil, wrapMapTemplate(err)
	}
	return api.GetTemplate200JSONResponse{Body: out}, nil
}

// --- PUT /api/v1/templates/{id} ------------------------------------------

// ReplaceTemplate updates a template's fields. PUT semantics: every
// field in the request replaces the existing value; CreatedAt is
// preserved by the use case.
func (s *Server) ReplaceTemplate(ctx context.Context, req api.ReplaceTemplateRequestObject) (api.ReplaceTemplateResponseObject, error) {
	if s.replaceTemplate == nil {
		return nil, ErrNotImplemented
	}
	if req.Body == nil {
		return nil, &domain.ValidationError{Field: "body", Reason: "request body is required"}
	}

	tpl, err := s.replaceTemplate(ctx, application.ReplaceTemplateInput{
		ID:      domain.TemplateID(req.Id.String()),
		Name:    req.Body.Name,
		Channel: string(req.Body.Channel),
		Body:    req.Body.Body,
	})
	if err != nil {
		return nil, err
	}

	out, err := toAPITemplate(tpl)
	if err != nil {
		return nil, wrapMapTemplate(err)
	}
	return api.ReplaceTemplate200JSONResponse{Body: out}, nil
}

// --- DELETE /api/v1/templates/{id} ---------------------------------------

// DeleteTemplate removes a template. Returns 204 on success, 404 when
// the id is unknown.
func (s *Server) DeleteTemplate(ctx context.Context, req api.DeleteTemplateRequestObject) (api.DeleteTemplateResponseObject, error) {
	if s.deleteTemplate == nil {
		return nil, ErrNotImplemented
	}

	if err := s.deleteTemplate(ctx, application.DeleteTemplateInput{
		ID: domain.TemplateID(req.Id.String()),
	}); err != nil {
		return nil, err
	}
	return api.DeleteTemplate204Response{}, nil
}

// --- mapping -------------------------------------------------------------

// toAPITemplate converts a domain.Template into its wire shape. The
// Variables field is left nil because the domain does not yet store
// declared variable names — when the domain grows that field, the
// mapping picks it up.
func toAPITemplate(t *domain.Template) (api.Template, error) {
	id, err := uuid.Parse(string(t.ID))
	if err != nil {
		return api.Template{}, fmt.Errorf("template id is not a uuid: %w", err)
	}
	return api.Template{
		Id:        id,
		Name:      t.Name,
		Channel:   api.Channel(t.Channel),
		Body:      t.Body,
		CreatedAt: t.CreatedAt,
		UpdatedAt: t.UpdatedAt,
	}, nil
}

// toAPITemplatePage wraps a use case output into the wire page shape.
// Empty templates serialize as `[]`, not null; an empty NextCursor is
// omitted (clients use the absence as the "last page" signal).
func toAPITemplatePage(out application.ListTemplatesOutput) (api.TemplatePage, error) {
	items := make([]api.Template, 0, len(out.Templates))
	for _, t := range out.Templates {
		item, err := toAPITemplate(t)
		if err != nil {
			return api.TemplatePage{}, wrapMapTemplate(err)
		}
		items = append(items, item)
	}
	page := api.TemplatePage{Items: items}
	if out.NextCursor != "" {
		nc := out.NextCursor
		page.NextCursor = &nc
	}
	return page, nil
}
