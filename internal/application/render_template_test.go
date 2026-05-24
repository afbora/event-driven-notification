package application_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// seedTemplate persists a template through the fake so RenderTemplate can
// fetch it. Returns the persisted template for further assertions.
func seedTemplate(t *testing.T, repo *fakeTemplateRepo, id domain.TemplateID, body string) *domain.Template {
	t.Helper()
	tmpl, err := domain.NewTemplate(domain.NewTemplateInput{
		ID:      id,
		Name:    "test-" + string(id),
		Channel: domain.ChannelSMS,
		Body:    body,
	}, fixedAppNow)
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), tmpl))
	return tmpl
}

// TestRenderTemplate_HappyPath: template fetched by id, body rendered with
// the supplied variables, content returned.
func TestRenderTemplate_HappyPath(t *testing.T) {
	repo := newFakeTemplateRepo()
	uc := application.NewRenderTemplate(repo)

	seedTemplate(t, repo, "01TMPL01", "Hello {{.Name}}, your code is {{.Code}}.")

	content, err := uc.Execute(context.Background(), application.RenderTemplateInput{
		TemplateID: "01TMPL01",
		Variables:  map[string]any{"Name": "Ahmet", "Code": "1234"},
	})
	require.NoError(t, err)
	require.Equal(t, "Hello Ahmet, your code is 1234.", content)
}

// TestRenderTemplate_NotFound: missing template id surfaces ports.ErrNotFound;
// no render attempt.
func TestRenderTemplate_NotFound(t *testing.T) {
	uc := application.NewRenderTemplate(newFakeTemplateRepo())

	_, err := uc.Execute(context.Background(), application.RenderTemplateInput{
		TemplateID: "01MISSING000000000000000000",
		Variables:  nil,
	})
	require.ErrorIs(t, err, ports.ErrNotFound)
}

// TestRenderTemplate_MissingVariable: caller forgot a variable the body
// references. Strict missingkey policy from domain.Template.Render surfaces
// ErrTemplateRenderFailed so the use case does not silently ship a half-
// rendered message.
func TestRenderTemplate_MissingVariable(t *testing.T) {
	repo := newFakeTemplateRepo()
	uc := application.NewRenderTemplate(repo)

	seedTemplate(t, repo, "01TMPL01", "Hello {{.Name}}!")

	_, err := uc.Execute(context.Background(), application.RenderTemplateInput{
		TemplateID: "01TMPL01",
		Variables:  map[string]any{}, // no Name
	})
	require.ErrorIs(t, err, domain.ErrTemplateRenderFailed)
}

// TestRenderTemplate_NoVariables: template without placeholders renders to
// itself, even with a nil variables map.
func TestRenderTemplate_NoVariables(t *testing.T) {
	repo := newFakeTemplateRepo()
	uc := application.NewRenderTemplate(repo)

	seedTemplate(t, repo, "01TMPL01", "Service is back online.")

	content, err := uc.Execute(context.Background(), application.RenderTemplateInput{
		TemplateID: "01TMPL01",
		Variables:  nil,
	})
	require.NoError(t, err)
	require.Equal(t, "Service is back online.", content)
}
