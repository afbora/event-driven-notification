package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// buildTemplateTestServer wires the five template executors in one go
// so the tests below can exercise any verb without re-wiring.
func buildTemplateTestServer(t *testing.T, opts httpadapter.ServerOptions) chi.Router {
	t.Helper()
	server := httpadapter.NewServer(opts)
	r := chi.NewRouter()
	api.HandlerFromMux(api.NewStrictHandlerWithOptions(
		server, nil,
		api.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  httpadapter.RespondWithError,
			ResponseErrorHandlerFunc: httpadapter.RespondWithError,
		},
	), r)
	return r
}

// sampleTemplate is a domain template the fakes return on the happy
// path. Built once and copied because tests mutate the result freely.
func sampleTemplate() *domain.Template {
	return &domain.Template{
		ID:        domain.TemplateID("01940000-0000-7000-8000-00000000aa11"),
		Name:      "welcome",
		Channel:   domain.ChannelSMS,
		Body:      "Hello {{.Name}}!",
		CreatedAt: fixedTime,
		UpdatedAt: fixedTime,
	}
}

// --- POST /api/v1/templates ----------------------------------------------

func TestCreateTemplate_HappyPath_201(t *testing.T) {
	var seen application.CreateTemplateInput
	exec := func(_ context.Context, in application.CreateTemplateInput) (*domain.Template, error) {
		seen = in
		return sampleTemplate(), nil
	}
	r := buildTemplateTestServer(t, httpadapter.ServerOptions{CreateTemplate: exec})

	body, _ := json.Marshal(map[string]any{
		"name":    "welcome",
		"channel": "sms",
		"body":    "Hello {{.Name}}!",
	})
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/templates", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusCreated, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, "welcome", seen.Name)
	require.Equal(t, "sms", seen.Channel)
	require.Equal(t, "Hello {{.Name}}!", seen.Body)
	require.Equal(t, "/api/v1/templates/01940000-0000-7000-8000-00000000aa11",
		rr.Header().Get("Location"))

	var out api.Template
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Equal(t, "welcome", out.Name)
	require.Equal(t, api.Sms, out.Channel)
}

func TestCreateTemplate_InvalidChannel_400(t *testing.T) {
	exec := func(_ context.Context, _ application.CreateTemplateInput) (*domain.Template, error) {
		return nil, domain.ErrInvalidChannel
	}
	r := buildTemplateTestServer(t, httpadapter.ServerOptions{CreateTemplate: exec})

	body, _ := json.Marshal(map[string]any{"name": "x", "channel": "fax", "body": "y"})
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/templates", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusBadRequest, rr.Code, "body=%s", rr.Body.String())
}

// --- GET /api/v1/templates -----------------------------------------------

func TestListTemplates_HappyPath(t *testing.T) {
	exec := func(_ context.Context, _ application.ListTemplatesInput) (application.ListTemplatesOutput, error) {
		t1 := sampleTemplate()
		t2 := sampleTemplate()
		t2.ID = domain.TemplateID("01940000-0000-7000-8000-00000000aa22")
		t2.Name = "second"
		return application.ListTemplatesOutput{Templates: []*domain.Template{t1, t2}}, nil
	}
	r := buildTemplateTestServer(t, httpadapter.ServerOptions{ListTemplates: exec})

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/templates?limit=10", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	var out api.TemplatePage
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Len(t, out.Items, 2)
	require.Equal(t, "welcome", out.Items[0].Name)
	require.Equal(t, "second", out.Items[1].Name)
	require.Nil(t, out.NextCursor,
		"use case does not yet support cursors; next_cursor must be omiaaed")
}

// --- GET /api/v1/templates/{id} ------------------------------------------

func TestGetTemplate_HappyPath(t *testing.T) {
	const idStr = "01940000-0000-7000-8000-00000000aa99"
	var seenID domain.TemplateID
	exec := func(_ context.Context, in application.GetTemplateInput) (*domain.Template, error) {
		seenID = in.ID
		tpl := sampleTemplate()
		tpl.ID = domain.TemplateID(idStr)
		return tpl, nil
	}
	r := buildTemplateTestServer(t, httpadapter.ServerOptions{GetTemplate: exec})

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/templates/"+idStr, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, domain.TemplateID(idStr), seenID)
	var out api.Template
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Equal(t, idStr, out.Id.String())
}

func TestGetTemplate_NotFound_404(t *testing.T) {
	exec := func(_ context.Context, _ application.GetTemplateInput) (*domain.Template, error) {
		return nil, ports.ErrNotFound
	}
	r := buildTemplateTestServer(t, httpadapter.ServerOptions{GetTemplate: exec})
	req := httptest.NewRequest(nethttp.MethodGet,
		"/api/v1/templates/01940000-0000-7000-8000-00000000aaff", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotFound, rr.Code, "body=%s", rr.Body.String())
}

// --- PUT /api/v1/templates/{id} ------------------------------------------

func TestReplaceTemplate_HappyPath(t *testing.T) {
	const idStr = "01940000-0000-7000-8000-00000000aa77"
	var seen application.ReplaceTemplateInput
	exec := func(_ context.Context, in application.ReplaceTemplateInput) (*domain.Template, error) {
		seen = in
		tpl := sampleTemplate()
		tpl.ID = domain.TemplateID(idStr)
		tpl.Name = in.Name
		tpl.Body = in.Body
		return tpl, nil
	}
	r := buildTemplateTestServer(t, httpadapter.ServerOptions{ReplaceTemplate: exec})

	body, _ := json.Marshal(map[string]any{
		"name":    "renamed",
		"channel": "email",
		"body":    "Updated body",
	})
	req := httptest.NewRequest(nethttp.MethodPut, "/api/v1/templates/"+idStr, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, domain.TemplateID(idStr), seen.ID, "path id flows to use case")
	require.Equal(t, "renamed", seen.Name)
	require.Equal(t, "email", seen.Channel)
}

func TestReplaceTemplate_NotFound_404(t *testing.T) {
	exec := func(_ context.Context, _ application.ReplaceTemplateInput) (*domain.Template, error) {
		return nil, ports.ErrNotFound
	}
	r := buildTemplateTestServer(t, httpadapter.ServerOptions{ReplaceTemplate: exec})

	body, _ := json.Marshal(map[string]any{"name": "x", "channel": "sms", "body": "y"})
	req := httptest.NewRequest(nethttp.MethodPut,
		"/api/v1/templates/01940000-0000-7000-8000-00000000aaab", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotFound, rr.Code, "body=%s", rr.Body.String())
}

// --- DELETE /api/v1/templates/{id} ---------------------------------------

func TestDeleteTemplate_HappyPath_204(t *testing.T) {
	const idStr = "01940000-0000-7000-8000-00000000aa55"
	var seenID domain.TemplateID
	exec := func(_ context.Context, in application.DeleteTemplateInput) error {
		seenID = in.ID
		return nil
	}
	r := buildTemplateTestServer(t, httpadapter.ServerOptions{DeleteTemplate: exec})

	req := httptest.NewRequest(nethttp.MethodDelete, "/api/v1/templates/"+idStr, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNoContent, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, domain.TemplateID(idStr), seenID)
	require.Empty(t, rr.Body.String(), "204 must have an empty body")
}

func TestDeleteTemplate_NotFound_404(t *testing.T) {
	exec := func(_ context.Context, _ application.DeleteTemplateInput) error {
		return ports.ErrNotFound
	}
	r := buildTemplateTestServer(t, httpadapter.ServerOptions{DeleteTemplate: exec})
	req := httptest.NewRequest(nethttp.MethodDelete,
		"/api/v1/templates/01940000-0000-7000-8000-00000000aabc", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotFound, rr.Code, "body=%s", rr.Body.String())
}

// --- Unimplemented -------------------------------------------------------

func TestTemplateOperations_NilExecutor_NotImplemented(t *testing.T) {
	r := buildTemplateTestServer(t, httpadapter.ServerOptions{})

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"create", nethttp.MethodPost, "/api/v1/templates"},
		{"list", nethttp.MethodGet, "/api/v1/templates"},
		{"get", nethttp.MethodGet, "/api/v1/templates/01940000-0000-7000-8000-00000000aaaa"},
		{"put", nethttp.MethodPut, "/api/v1/templates/01940000-0000-7000-8000-00000000aaaa"},
		{"delete", nethttp.MethodDelete, "/api/v1/templates/01940000-0000-7000-8000-00000000aaaa"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			if tc.method == nethttp.MethodPost || tc.method == nethttp.MethodPut {
				body, _ = json.Marshal(map[string]any{"name": "x", "channel": "sms", "body": "y"})
			}
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			require.Equal(t, nethttp.StatusNotImplemented, rr.Code, "body=%s", rr.Body.String())
		})
	}
}
