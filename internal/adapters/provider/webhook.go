package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// webhookRequest is the JSON body posted to the webhook endpoint. The shape
// matches the brief: { "to": ..., "channel": ..., "content": ... }.
type webhookRequest struct {
	To      string `json:"to"`
	Channel string `json:"channel"`
	Content string `json:"content"`
}

// webhookResponse is the canonical 202 response body — the brief specifies
// { "messageId": ..., "status": ..., "timestamp": ISO8601 }. We tolerate
// missing or malformed bodies on 2xx (success is the HTTP status code; the
// body is best-effort context).
type webhookResponse struct {
	MessageID string `json:"messageId"`
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

// WebhookProvider is the production ports.Provider implementation that POSTs
// the notification payload to a configured URL — webhook.site for the
// assessment, or a real Twilio/SendGrid/FCM-fronting webhook in production.
//
// Status-code → DeliveryResult mapping:
//
//   - 2xx     → DeliveredResult (MessageID extracted from body when present)
//   - 4xx     → PermanentFailureResult (no retry)
//   - 5xx     → TransientFailureResult (retry with backoff)
//   - error   → TransientFailureResult with ProviderCode=0 (network/timeout)
type WebhookProvider struct {
	client *http.Client
	url    string
}

// NewWebhookProvider wires the target URL and per-request timeout. The
// http.Client is reused across calls (connection pooling); construct one
// per process and share.
func NewWebhookProvider(url string, timeout time.Duration) *WebhookProvider {
	return &WebhookProvider{
		client: &http.Client{Timeout: timeout},
		url:    url,
	}
}

// Send POSTs the notification payload and classifies the response into a
// DeliveryResult. Implements ports.Provider.
func (p *WebhookProvider) Send(ctx context.Context, channel domain.Channel, recipient, content string) domain.DeliveryResult {
	start := time.Now()

	body, err := json.Marshal(webhookRequest{
		To:      recipient,
		Channel: string(channel),
		Content: content,
	})
	if err != nil {
		// Marshal failure is a permanent bug, not a transient one — but the
		// worker treats it the same way (no retry would help). Return a
		// permanent failure so the notification short-circuits to failed.
		return domain.PermanentFailureResult(
			fmt.Sprintf("webhook: marshal request: %v", err), 0, time.Since(start),
		)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return domain.TransientFailureResult(
			fmt.Sprintf("webhook: build request: %v", err), 0, time.Since(start),
		)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return domain.TransientFailureResult(
			fmt.Sprintf("webhook: %v", err), 0, time.Since(start),
		)
	}
	defer func() { _ = resp.Body.Close() }()

	return p.classify(resp, start)
}

// classify converts the HTTP response into a DeliveryResult. Body parsing
// failures on a 2xx are tolerated (status code is the authoritative signal);
// for non-2xx the body is included verbatim in the reason for debugging.
func (p *WebhookProvider) classify(resp *http.Response, start time.Time) domain.DeliveryResult {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		var parsed webhookResponse
		_ = json.NewDecoder(resp.Body).Decode(&parsed) // best-effort
		return domain.DeliveredResult(parsed.MessageID, time.Since(start))

	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return domain.PermanentFailureResult(
			fmt.Sprintf("webhook 4xx: %s", readBodyForReason(resp)),
			resp.StatusCode,
			time.Since(start),
		)

	default:
		// 5xx and anything else unexpected — treat as transient so retry
		// machinery decides whether to keep trying.
		return domain.TransientFailureResult(
			fmt.Sprintf("webhook %d: %s", resp.StatusCode, readBodyForReason(resp)),
			resp.StatusCode,
			time.Since(start),
		)
	}
}

// readBodyForReason pulls the response body into a string for the failure
// reason field. Capped at 256 bytes so a misbehaving provider sending a
// huge HTML error page does not bloat log lines.
func readBodyForReason(resp *http.Response) string {
	const maxReason = 256
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxReason))
	if err != nil {
		return resp.Status
	}
	return string(b)
}
