package application

import (
	"errors"
	"time"
)

// Application-level sentinel errors. ProcessNotification returns these
// from its terminal helpers so the asynq processor — and through it
// the worker's RetryDelayFunc — can route the retry timing per cause
// without parsing strings (CLAUDE.md §3.5 forbids string compares on
// errors; use errors.Is / errors.As).
//
// These are NOT domain errors — they encode infrastructure-level retry
// signals the use case sends back to the asynq layer. Domain errors
// live in internal/domain/errors.go.
var (
	// ErrProviderTransient signals the upstream provider failed in a
	// retryable way (5xx-class, timeout, network blip). Asynq's
	// RetryDelayFunc maps this to the exponential backoff schedule.
	ErrProviderTransient = errors.New("provider transient failure")

	// ErrOutboundRateLimited signals the worker's per-channel rate
	// limiter rejected the send. Asynq's RetryDelayFunc maps this to
	// the short rate-limit backoff so the throttled task re-fires
	// quickly once the window rolls forward — distinct from the
	// exponential schedule transient failures use.
	ErrOutboundRateLimited = errors.New("outbound rate limit exceeded")
)

// RetryDelayFor returns how long asynq should wait before re-running
// a failed task, given the attempt count (1-indexed — the attempt
// that just failed) and the error the use case returned. It is the
// single API the worker's asynq.Config.RetryDelayFunc calls — every
// retry-timing policy lives here so it stays beside the sentinels
// the policy keys on.
//
// Mapping:
//
//   - errors.Is(err, ErrOutboundRateLimited) → rateLimitBackoff (1s)
//     The throttled task should re-fire quickly so it can re-check
//     the limiter when the window rolls forward.
//   - everything else                        → backoffFor(attempts)
//     Exponential schedule from CLAUDE.md §5 (30s, 60s, 120s, ...).
//     Covers ErrProviderTransient plus any unwrapped infrastructure
//     error from the claim / DB / rate-limiter calls.
func RetryDelayFor(attempts int, err error) time.Duration {
	if errors.Is(err, ErrOutboundRateLimited) {
		return rateLimitBackoff
	}
	return backoffFor(attempts)
}
