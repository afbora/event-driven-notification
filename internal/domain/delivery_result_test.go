package domain_test

import (
	"testing"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// TestDeliveredResult confirms the success factory sets the right shape.
// Success=true, Retryable=false (irrelevant on success), MessageID populated,
// Reason empty.
func TestDeliveredResult(t *testing.T) {
	latency := 250 * time.Millisecond
	got := domain.DeliveredResult("provider-msg-123", latency)

	if !got.Success {
		t.Error("Success = false, want true")
	}
	if got.Retryable {
		t.Error("Retryable = true on success, want false")
	}
	if got.MessageID != "provider-msg-123" {
		t.Errorf("MessageID = %q, want %q", got.MessageID, "provider-msg-123")
	}
	if got.Reason != "" {
		t.Errorf("Reason = %q on success, want empty", got.Reason)
	}
	if got.ProviderCode != 0 {
		t.Errorf("ProviderCode = %d on success, want 0", got.ProviderCode)
	}
	if got.Latency != latency {
		t.Errorf("Latency = %v, want %v", got.Latency, latency)
	}
}

// TestPermanentFailureResult covers the 4xx-style failure path. Retryable
// must be false so the worker does not enqueue a retry that will fail again.
func TestPermanentFailureResult(t *testing.T) {
	latency := 90 * time.Millisecond
	got := domain.PermanentFailureResult("recipient blacklisted", 422, latency)

	if got.Success {
		t.Error("Success = true on permanent failure, want false")
	}
	if got.Retryable {
		t.Error("Retryable = true on permanent failure, want false (4xx must not retry)")
	}
	if got.Reason != "recipient blacklisted" {
		t.Errorf("Reason = %q, want %q", got.Reason, "recipient blacklisted")
	}
	if got.ProviderCode != 422 {
		t.Errorf("ProviderCode = %d, want 422", got.ProviderCode)
	}
	if got.MessageID != "" {
		t.Errorf("MessageID = %q on failure, want empty", got.MessageID)
	}
	if got.Latency != latency {
		t.Errorf("Latency = %v, want %v", got.Latency, latency)
	}
}

// TestTransientFailureResult covers the 5xx / timeout / network path. The
// worker reads Retryable=true and re-enqueues with exponential backoff.
func TestTransientFailureResult(t *testing.T) {
	latency := 5 * time.Second
	got := domain.TransientFailureResult("provider 503", 503, latency)

	if got.Success {
		t.Error("Success = true on transient failure, want false")
	}
	if !got.Retryable {
		t.Error("Retryable = false on transient failure, want true (5xx must retry)")
	}
	if got.Reason != "provider 503" {
		t.Errorf("Reason = %q, want %q", got.Reason, "provider 503")
	}
	if got.ProviderCode != 503 {
		t.Errorf("ProviderCode = %d, want 503", got.ProviderCode)
	}
}

// TestTransientFailureResult_NoHTTPCode confirms that network-level failures
// (timeouts, DNS errors) can use code 0 — the field is optional context, not
// a hard signal.
func TestTransientFailureResult_NoHTTPCode(t *testing.T) {
	got := domain.TransientFailureResult("connection timeout", 0, 30*time.Second)
	if !got.Retryable {
		t.Error("Retryable = false on timeout, want true")
	}
	if got.ProviderCode != 0 {
		t.Errorf("ProviderCode = %d, want 0", got.ProviderCode)
	}
}
