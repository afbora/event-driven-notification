package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// Templates have five use cases — one per CRUD verb plus a list — and
// each is a thin orchestration over ports.TemplateRepository plus a
// clock/idgen for Create and Replace. The tests below verify that each
// use case parses its string input into domain types, sets timestamps
// from the injected clock, and propagates repository errors.

// --- Create --------------------------------------------------------------

// TestCreateTemplate_HappyPath: a well-formed input persists a new
// template with a server-allocated id and the clock's timestamp.
func TestCreateTemplate_HappyPath(t *testing.T) {
	repo := newFakeTemplateRepo()
	clock := newFakeClock(fixedAppNow)
	idGen := newDefaultFakeIDs()
	uc := application.NewCreateTemplate(repo, idGen, clock)

	out, err := uc.Execute(context.Background(), application.CreateTemplateInput{
		Name:    "welcome-sms",
		Channel: "sms",
		Body:    "Hello {{.Name}}!",
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.ID)
	require.Equal(t, "welcome-sms", out.Name)
	require.Equal(t, domain.ChannelSMS, out.Channel)
	require.Equal(t, "Hello {{.Name}}!", out.Body)
	require.Equal(t, fixedAppNow, out.CreatedAt)
	require.Equal(t, fixedAppNow, out.UpdatedAt)
}

// TestCreateTemplate_InvalidChannel: a bad channel surfaces the
// domain sentinel and the repo stays untouched.
func TestCreateTemplate_InvalidChannel(t *testing.T) {
	repo := newFakeTemplateRepo()
	uc := application.NewCreateTemplate(repo, newDefaultFakeIDs(), newFakeClock(fixedAppNow))

	_, err := uc.Execute(context.Background(), application.CreateTemplateInput{
		Name:    "bad",
		Channel: "fax",
		Body:    "x",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, domain.ErrInvalidChannel))
}

// --- Get -----------------------------------------------------------------

// TestGetTemplate_HappyPath: returns whatever the repository holds.
func TestGetTemplate_HappyPath(t *testing.T) {
	repo := newFakeTemplateRepo()
	created := mustNewTemplate(t, "01940000-0000-7000-8000-00000000ttt1", "welcome", domain.ChannelSMS)
	require.NoError(t, repo.Create(context.Background(), created))

	uc := application.NewGetTemplate(repo)
	out, err := uc.Execute(context.Background(), application.GetTemplateInput{ID: created.ID})
	require.NoError(t, err)
	require.Equal(t, created.ID, out.ID)
}

// TestGetTemplate_NotFound: an unknown id surfaces ports.ErrNotFound.
func TestGetTemplate_NotFound(t *testing.T) {
	repo := newFakeTemplateRepo()
	uc := application.NewGetTemplate(repo)
	_, err := uc.Execute(context.Background(), application.GetTemplateInput{
		ID: domain.TemplateID("01940000-0000-7000-8000-00000000ttff"),
	})
	require.True(t, errors.Is(err, ports.ErrNotFound))
}

// --- List ----------------------------------------------------------------

// TestListTemplates_AppliesDefaultLimit: zero limit snaps to the use
// case default — the repository sees a non-zero value.
func TestListTemplates_AppliesDefaultLimit(t *testing.T) {
	repo := newFakeTemplateRepo()
	for i := 0; i < 3; i++ {
		require.NoError(t, repo.Create(context.Background(),
			mustNewTemplate(t,
				domain.TemplateID("01940000-0000-7000-8000-00000000tt0"+string(rune('1'+i))),
				"name-"+string(rune('a'+i)),
				domain.ChannelSMS,
			),
		))
	}

	uc := application.NewListTemplates(repo)
	out, err := uc.Execute(context.Background(), application.ListTemplatesInput{Limit: 0})
	require.NoError(t, err)
	require.Len(t, out.Templates, 3)
}

// --- Replace -------------------------------------------------------------

// TestReplaceTemplate_HappyPath: updates name/channel/body while
// preserving the original CreatedAt and bumping UpdatedAt from the
// clock.
func TestReplaceTemplate_HappyPath(t *testing.T) {
	repo := newFakeTemplateRepo()
	original := mustNewTemplate(t,
		"01940000-0000-7000-8000-00000000tt21",
		"original-name",
		domain.ChannelSMS,
	)
	require.NoError(t, repo.Create(context.Background(), original))

	later := fixedAppNow.Add(60)
	uc := application.NewReplaceTemplate(repo, newFakeClock(later))

	out, err := uc.Execute(context.Background(), application.ReplaceTemplateInput{
		ID:      original.ID,
		Name:    "replaced-name",
		Channel: "email",
		Body:    "Hello {{.Name}} — new body",
	})
	require.NoError(t, err)
	require.Equal(t, original.ID, out.ID)
	require.Equal(t, "replaced-name", out.Name)
	require.Equal(t, domain.ChannelEmail, out.Channel)
	require.Equal(t, original.CreatedAt, out.CreatedAt, "CreatedAt must be preserved")
	require.Equal(t, later, out.UpdatedAt, "UpdatedAt comes from the clock")
}

// TestReplaceTemplate_NotFound: cannot replace what does not exist.
func TestReplaceTemplate_NotFound(t *testing.T) {
	repo := newFakeTemplateRepo()
	uc := application.NewReplaceTemplate(repo, newFakeClock(fixedAppNow))
	_, err := uc.Execute(context.Background(), application.ReplaceTemplateInput{
		ID:      domain.TemplateID("01940000-0000-7000-8000-00000000tt99"),
		Name:    "x",
		Channel: "sms",
		Body:    "y",
	})
	require.True(t, errors.Is(err, ports.ErrNotFound))
}

// --- Delete --------------------------------------------------------------

// TestDeleteTemplate_HappyPath: removes the template; subsequent Get
// returns ErrNotFound.
func TestDeleteTemplate_HappyPath(t *testing.T) {
	repo := newFakeTemplateRepo()
	created := mustNewTemplate(t, "01940000-0000-7000-8000-00000000tt33", "doomed", domain.ChannelSMS)
	require.NoError(t, repo.Create(context.Background(), created))

	uc := application.NewDeleteTemplate(repo)
	require.NoError(t, uc.Execute(context.Background(), application.DeleteTemplateInput{ID: created.ID}))

	_, err := repo.Get(context.Background(), created.ID)
	require.True(t, errors.Is(err, ports.ErrNotFound))
}

// TestDeleteTemplate_NotFound: deleting an unknown id surfaces the
// repository's ErrNotFound. Some implementations choose to ignore it
// (idempotent delete); our use case prefers explicit signaling because
// the HTTP layer wants a 404 in that case.
func TestDeleteTemplate_NotFound(t *testing.T) {
	repo := newFakeTemplateRepo()
	uc := application.NewDeleteTemplate(repo)
	err := uc.Execute(context.Background(), application.DeleteTemplateInput{
		ID: domain.TemplateID("01940000-0000-7000-8000-00000000tt66"),
	})
	require.True(t, errors.Is(err, ports.ErrNotFound))
}

// mustNewTemplate is a tiny domain-layer factory used by the template
// tests above so individual cases stay readable.
func mustNewTemplate(t *testing.T, id domain.TemplateID, name string, channel domain.Channel) *domain.Template {
	t.Helper()
	tpl, err := domain.NewTemplate(domain.NewTemplateInput{
		ID:      id,
		Name:    name,
		Channel: channel,
		Body:    "Hello {{.Name}}!",
	}, fixedAppNow)
	require.NoError(t, err)
	return tpl
}
