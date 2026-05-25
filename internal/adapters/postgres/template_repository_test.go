//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/adapters/postgres"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// integrationTemplateID returns a UUID-v7-shaped template id.
func integrationTemplateID(suffix string) domain.TemplateID {
	return domain.TemplateID("01940000-0000-7000-8000-0000000000" + suffix)
}

// makeIntegrationTemplate builds a known-good template.
func makeIntegrationTemplate(t *testing.T, id domain.TemplateID, name string) *domain.Template {
	t.Helper()
	tmpl, err := domain.NewTemplate(domain.NewTemplateInput{
		ID:      id,
		Name:    name,
		Channel: domain.ChannelSMS,
		Body:    "Hello {{.Name}}",
	}, fixedIntegrationNow)
	require.NoError(t, err)
	return tmpl
}

// TestTemplateRepository_MalformedID covers the parseTemplateIDErr call
// sites in Create, Get, Update, and Delete — each rejects ids that do not
// parse as a UUID before touching the database. Bundled because the
// assertion shape is identical across methods.
func TestTemplateRepository_MalformedID(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewTemplateRepository(pool)
	ctx := context.Background()
	badID := domain.TemplateID("not-a-uuid")

	t.Run("Get", func(t *testing.T) {
		_, err := repo.Get(ctx, badID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "parse template id")
	})

	t.Run("Create", func(t *testing.T) {
		tmpl := &domain.Template{ID: badID, Name: "x", Channel: domain.ChannelSMS, Body: "x"}
		err := repo.Create(ctx, tmpl)
		require.Error(t, err)
		require.Contains(t, err.Error(), "parse template id")
	})

	t.Run("Update", func(t *testing.T) {
		tmpl := &domain.Template{ID: badID, Name: "x", Channel: domain.ChannelSMS, Body: "x"}
		err := repo.Update(ctx, tmpl)
		require.Error(t, err)
		require.Contains(t, err.Error(), "parse template id")
	})

	t.Run("Delete", func(t *testing.T) {
		err := repo.Delete(ctx, badID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "parse template id")
	})
}

// TestTemplateRepository_CreateAndGet: round-trip by id.
func TestTemplateRepository_CreateAndGet(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewTemplateRepository(pool)
	ctx := context.Background()

	original := makeIntegrationTemplate(t, integrationTemplateID("a1"), "welcome-sms")
	require.NoError(t, repo.Create(ctx, original))

	got, err := repo.Get(ctx, original.ID)
	require.NoError(t, err)
	require.Equal(t, original.ID, got.ID)
	require.Equal(t, original.Name, got.Name)
	require.Equal(t, original.Channel, got.Channel)
	require.Equal(t, original.Body, got.Body)
}

// TestTemplateRepository_GetByName: lookup by the human handle.
func TestTemplateRepository_GetByName(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewTemplateRepository(pool)
	ctx := context.Background()

	original := makeIntegrationTemplate(t, integrationTemplateID("a2"), "by-name-test")
	require.NoError(t, repo.Create(ctx, original))

	got, err := repo.GetByName(ctx, "by-name-test")
	require.NoError(t, err)
	require.Equal(t, original.ID, got.ID)
}

// TestTemplateRepository_Get_NotFound: missing id → ErrNotFound.
func TestTemplateRepository_Get_NotFound(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewTemplateRepository(pool)
	_, err := repo.Get(context.Background(), integrationTemplateID("ff"))
	require.ErrorIs(t, err, ports.ErrNotFound)
}

// TestTemplateRepository_GetByName_NotFound: unknown name → ErrNotFound.
func TestTemplateRepository_GetByName_NotFound(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewTemplateRepository(pool)
	_, err := repo.GetByName(context.Background(), "does-not-exist")
	require.ErrorIs(t, err, ports.ErrNotFound)
}

// TestTemplateRepository_List: returns multiple, capped by limit.
func TestTemplateRepository_List(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewTemplateRepository(pool)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, makeIntegrationTemplate(t, integrationTemplateID("b1"), "tmpl-alpha")))
	require.NoError(t, repo.Create(ctx, makeIntegrationTemplate(t, integrationTemplateID("b2"), "tmpl-beta")))
	require.NoError(t, repo.Create(ctx, makeIntegrationTemplate(t, integrationTemplateID("b3"), "tmpl-gamma")))

	items, err := repo.List(ctx, 10)
	require.NoError(t, err)
	require.Len(t, items, 3)

	// Limit clamps the result set.
	items2, err := repo.List(ctx, 2)
	require.NoError(t, err)
	require.Len(t, items2, 2)
}

// TestTemplateRepository_Update: mutate name + body + channel; round-trip
// reflects the change.
func TestTemplateRepository_Update(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewTemplateRepository(pool)
	ctx := context.Background()

	tmpl := makeIntegrationTemplate(t, integrationTemplateID("c1"), "original-name")
	require.NoError(t, repo.Create(ctx, tmpl))

	tmpl.Name = "renamed"
	tmpl.Channel = domain.ChannelEmail
	tmpl.Body = "Hi {{.Name}}, this is the new body"
	require.NoError(t, repo.Update(ctx, tmpl))

	got, err := repo.Get(ctx, tmpl.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed", got.Name)
	require.Equal(t, domain.ChannelEmail, got.Channel)
	require.Equal(t, "Hi {{.Name}}, this is the new body", got.Body)
}

// TestTemplateRepository_Delete: after Delete the id is gone.
func TestTemplateRepository_Delete(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewTemplateRepository(pool)
	ctx := context.Background()

	tmpl := makeIntegrationTemplate(t, integrationTemplateID("d1"), "to-delete")
	require.NoError(t, repo.Create(ctx, tmpl))
	require.NoError(t, repo.Delete(ctx, tmpl.ID))

	_, err := repo.Get(ctx, tmpl.ID)
	require.ErrorIs(t, err, ports.ErrNotFound)
}
