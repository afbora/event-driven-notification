package domain

import (
	"bytes"
	"errors"
	"fmt"
	"text/template"
	"time"
)

// TemplateID is the unique identifier for a message template. The string form
// is the UUID v7 representation, parallel to NotificationID and BatchID.
type TemplateID string

// Sentinel errors for Template construction and rendering.
var (
	ErrInvalidTemplateID    = errors.New("invalid template id")
	ErrInvalidTemplateName  = errors.New("invalid template name")
	ErrInvalidTemplateBody  = errors.New("invalid template body")
	ErrTemplateRenderFailed = errors.New("template render failed")
)

// Template represents a reusable message body with variable placeholders.
// The body uses Go's text/template syntax — e.g. "Hello {{.Name}}, your
// code is {{.Code}}." Variables are substituted at render time via Render.
//
// Templates are channel-scoped: a Template carries the Channel for which it
// was authored, so callers can prevent rendering a 10000-char email body
// into a 160-char SMS by mistake.
type Template struct {
	ID        TemplateID
	Name      string
	Channel   Channel
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewTemplateInput bundles parameters for NewTemplate.
type NewTemplateInput struct {
	ID      TemplateID
	Name    string
	Channel Channel
	Body    string
}

// NewTemplate constructs a fully validated Template. The body is parsed at
// construction time so syntax errors surface here, not later at render time
// when a real notification depends on the template.
func NewTemplate(in NewTemplateInput, now time.Time) (*Template, error) {
	if in.ID == "" {
		return nil, ErrInvalidTemplateID
	}
	if in.Name == "" {
		return nil, ErrInvalidTemplateName
	}
	if !in.Channel.IsValid() {
		return nil, ErrInvalidChannel
	}
	if in.Body == "" {
		return nil, fmt.Errorf("%w: empty", ErrInvalidTemplateBody)
	}
	if _, err := template.New(string(in.ID)).Parse(in.Body); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTemplateBody, err)
	}
	return &Template{
		ID:        in.ID,
		Name:      in.Name,
		Channel:   in.Channel,
		Body:      in.Body,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// Render substitutes the supplied variables into the template body and
// returns the resulting string. Missing variables are an error
// (`missingkey=error`) so a half-rendered message never reaches a user;
// extra variables are silently ignored.
//
// Parse cost is paid on every call. Phase 2 keeps the implementation simple;
// if profiling shows render to be a hot path, NewTemplate can be extended to
// cache the parsed *template.Template inside the struct (private field).
func (t *Template) Render(vars map[string]any) (string, error) {
	parsed, err := template.New(string(t.ID)).Option("missingkey=error").Parse(t.Body)
	if err != nil {
		// Defensive: NewTemplate already validated syntax. If we reach here
		// the Body was mutated post-construction (which mark methods do not
		// permit) or the parser changed semantics under us.
		return "", fmt.Errorf("%w: parse: %v", ErrTemplateRenderFailed, err)
	}
	var buf bytes.Buffer
	if err := parsed.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("%w: %v", ErrTemplateRenderFailed, err)
	}
	return buf.String(), nil
}
