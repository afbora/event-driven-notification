package domain

import "time"

// DeliveryResult is the outcome of a single provider call. It carries the
// success / failure verdict, the classification used by the retry policy
// (CLAUDE.md §3.5: permanent vs transient), provider-side context (message
// id on success, response code on failure), and the wall-clock latency for
// observability.
//
// Provider adapters construct DeliveryResult via the package-level factories
// (DeliveredResult / PermanentFailureResult / TransientFailureResult). Direct
// struct literals are legal but skip the niceties of the factory helpers and
// risk inconsistent flag combinations (Success=true with Retryable=true, etc.).
type DeliveryResult struct {
	// Success is true if the provider accepted the message. On success the
	// remaining failure-side fields are zero-valued.
	Success bool

	// MessageID is the provider-assigned identifier (e.g. Twilio SID,
	// SendGrid message ID). Empty on failure.
	MessageID string

	// Retryable, when true, signals the worker to schedule a retry with
	// exponential backoff. Always false on success or permanent failure.
	Retryable bool

	// Reason is a short human-readable failure summary, written to
	// notification_logs.details and surfaced in the trace endpoint. Empty
	// on success.
	Reason string

	// ProviderCode is the HTTP status code (or equivalent provider code).
	// Zero when no HTTP layer was involved (DNS timeout, TCP reset).
	ProviderCode int

	// Latency is the wall-clock duration of the provider call. Exposed as
	// the `notifications_processing_duration_seconds` histogram metric in
	// phase 6.
	Latency time.Duration
}

// DeliveredResult constructs the success-case DeliveryResult. The message id
// is whatever the provider returned (Twilio SID, SendGrid X-Message-Id, FCM
// name, …) — we do not validate the format.
func DeliveredResult(messageID string, latency time.Duration) DeliveryResult {
	return DeliveryResult{
		Success:   true,
		MessageID: messageID,
		Retryable: false,
		Latency:   latency,
	}
}

// PermanentFailureResult constructs a 4xx-class failure. Retryable is forced
// to false; the worker will not re-enqueue, and the notification moves to
// the failed terminal state with the reason recorded.
func PermanentFailureResult(reason string, providerCode int, latency time.Duration) DeliveryResult {
	return DeliveryResult{
		Success:      false,
		Reason:       reason,
		Retryable:    false,
		ProviderCode: providerCode,
		Latency:      latency,
	}
}

// TransientFailureResult constructs a 5xx / timeout / network-error failure.
// Retryable is forced to true so the worker schedules a retry. ProviderCode
// may be 0 when no HTTP response was received (e.g. timeout, TCP reset).
func TransientFailureResult(reason string, providerCode int, latency time.Duration) DeliveryResult {
	return DeliveryResult{
		Success:      false,
		Reason:       reason,
		Retryable:    true,
		ProviderCode: providerCode,
		Latency:      latency,
	}
}
