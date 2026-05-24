//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTemplate_VariableSubstitution_RenderedAtDelivery: a template
// with a `{{.Name}}` placeholder is created, a notification
// references it via template_id + template_variables, and the worker
// hands the *rendered* string (variables substituted) to the
// provider — not the raw template body. End-to-end proof that the
// template feature is wired through every layer.
func TestTemplate_VariableSubstitution_RenderedAtDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	// --- POST /api/v1/templates ----------------------------------------
	tmplBody, err := json.Marshal(map[string]any{
		"name":    "e2e-welcome",
		"channel": "sms",
		"body":    "Hello {{.Name}}, your code is {{.Code}}.",
	})
	require.NoError(t, err)

	tmplResp := mustPost(ctx, t, h.BaseURL+"/api/v1/templates", tmplBody)
	require.Equal(t, http.StatusCreated, tmplResp.status, "template body=%s", tmplResp.body)

	var tmpl struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(tmplResp.body, &tmpl))
	require.NotEmpty(t, tmpl.ID)

	// --- POST /api/v1/notifications with template_id + variables ------
	notifBody, err := json.Marshal(map[string]any{
		"channel":     "sms",
		"recipient":   "+15555550050",
		"content":     "placeholder",
		"template_id": tmpl.ID,
		"template_variables": map[string]any{
			"Name": "Ada",
			"Code": 4242,
		},
	})
	require.NoError(t, err)

	notifResp := mustPost(ctx, t, h.BaseURL+"/api/v1/notifications", notifBody)
	require.Equal(t, http.StatusAccepted, notifResp.status, "notif body=%s", notifResp.body)

	var notif struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(notifResp.body, &notif))
	require.Equal(t, "Hello Ada, your code is 4242.", notif.Content,
		"notification content must be the rendered template, not the placeholder we posted")

	// --- Wait for delivery + assert the provider received the render --
	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, notif.ID) == "delivered"
	}, 30*time.Second, 200*time.Millisecond, "notification never reached delivered")

	require.Len(t, h.Provider.Calls(), 1)
	require.Equal(t, "Hello Ada, your code is 4242.", h.Provider.Calls()[0].Content,
		"the provider must see the rendered string, not the template body or the placeholder")
}

// TestTemplate_UnknownTemplateID_404: referencing a template that
// does not exist surfaces as a 404 — the use case fails before the
// notification is persisted, so the API never enqueues a half-built
// record.
func TestTemplate_UnknownTemplateID_404(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	body, err := json.Marshal(map[string]any{
		"channel":            "sms",
		"recipient":          "+15555550051",
		"content":            "placeholder",
		"template_id":        "019e0000-0000-0000-0000-000000000000",
		"template_variables": map[string]any{"Name": "Ada"},
	})
	require.NoError(t, err)

	resp := mustPost(ctx, t, h.BaseURL+"/api/v1/notifications", body)
	require.Equal(t, http.StatusNotFound, resp.status, "body=%s", resp.body)

	// Confirm no notification was created.
	require.Empty(t, h.Provider.Calls(),
		"failed template lookup must not produce a provider call")
}

// mustPostResponse bundles status + body for an HTTP POST so test
// helpers can quote them on assertion failures.
type mustPostResponse struct {
	status int
	body   []byte
}

func mustPost(ctx context.Context, t *testing.T, url string, body []byte) mustPostResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return mustPostResponse{status: resp.StatusCode, body: b}
}
