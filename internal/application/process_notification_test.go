package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/infrastructure/correlation"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// processFixture bundles every fake ProcessNotification touches. Test bodies
// stay short by mutating only the slot they care about.
type processFixture struct {
	uc          *application.ProcessNotification
	repo        *fakeNotificationRepo
	logRepo     *fakeNotificationLogRepo
	provider    *fakeProvider
	rateLimiter *fakeRateLimiter
	broadcaster *fakeStatusBroadcaster
}

func newProcessFixture(t *testing.T, providerResult domain.DeliveryResult, rateAllowed bool) processFixture {
	t.Helper()
	repo := newFakeNotificationRepo()
	logRepo := newFakeNotificationLogRepo()
	provider := newFakeProvider(providerResult)
	rateLimiter := newFakeRateLimiter(rateAllowed)
	broadcaster := newFakeStatusBroadcaster()
	idGen := newDefaultFakeIDs()
	clock := newFakeClock(fixedAppNow)

	uc := application.NewProcessNotification(application.ProcessNotificationDeps{
		Repo:        repo,
		LogRepo:     logRepo,
		Provider:    provider,
		RateLimiter: rateLimiter,
		Broadcaster: broadcaster,
		IDGen:       idGen,
		Clock:       clock,
	})

	return processFixture{
		uc:          uc,
		repo:        repo,
		logRepo:     logRepo,
		provider:    provider,
		rateLimiter: rateLimiter,
		broadcaster: broadcaster,
	}
}

// TestProcessNotification_HappyPath: queued notification → provider succeeds
// → delivered. Atomic claim happens before the provider call; status update
// and broadcast happen after.
func TestProcessNotification_HappyPath(t *testing.T) {
	f := newProcessFixture(t,
		domain.DeliveredResult("provider-id-abc", 80*time.Millisecond),
		true,
	)
	n := seedNotificationInStatus(t, f.repo, "01NOTIFPROC", domain.StatusQueued)

	err := f.uc.Execute(context.Background(), application.ProcessNotificationInput{
		NotificationID: n.ID,
	})
	require.NoError(t, err)

	// Final state in the repo.
	require.Equal(t, domain.StatusDelivered, f.repo.store[n.ID].Status)

	// Rate limiter consulted with channel-namespaced bucket.
	require.Equal(t, []string{"channel:sms"}, f.rateLimiter.buckets)

	// Provider called exactly once with the notification's content.
	require.Len(t, f.provider.calls, 1)
	require.Equal(t, domain.ChannelSMS, f.provider.calls[0].Channel)
	require.Equal(t, n.Recipient, f.provider.calls[0].Recipient)
	require.Equal(t, n.Content, f.provider.calls[0].Content)

	// notification_logs: "processing" entry from the claim, "delivered" entry from the result.
	require.Len(t, f.logRepo.entries, 2)
	require.Equal(t, domain.LogEventProcessing, f.logRepo.entries[0].Event)
	require.Equal(t, domain.LogEventDelivered, f.logRepo.entries[1].Event)

	// Status broadcast went out for the terminal state.
	require.Equal(t, []broadcastEntry{
		{NotificationID: n.ID, Status: domain.StatusProcessing},
		{NotificationID: n.ID, Status: domain.StatusDelivered},
	}, f.broadcaster.messages)
}

// TestProcessNotification_AlreadyClaimed: another worker (or a redelivery)
// has already moved the notification into processing. ClaimForProcessing
// returns ErrAlreadyClaimed; Execute swallows it and exits silently — the
// provider must not be called.
func TestProcessNotification_AlreadyClaimed(t *testing.T) {
	f := newProcessFixture(t,
		domain.DeliveredResult("never-used", 0),
		true,
	)
	// Seed the notification already in processing — claim will refuse it.
	n := seedNotificationInStatus(t, f.repo, "01NOTIFPROC", domain.StatusProcessing)

	err := f.uc.Execute(context.Background(), application.ProcessNotificationInput{
		NotificationID: n.ID,
	})
	require.NoError(t, err, "already-claimed is a no-op, not an error")

	// Provider not consulted; rate limiter not consulted; no log/broadcast.
	require.Empty(t, f.provider.calls)
	require.Empty(t, f.rateLimiter.buckets)
	require.Empty(t, f.logRepo.entries)
	require.Empty(t, f.broadcaster.messages)
}

// TestProcessNotification_PermanentFailure: provider returns a 4xx-class
// result. Notification moves directly to failed (no retry).
func TestProcessNotification_PermanentFailure(t *testing.T) {
	f := newProcessFixture(t,
		domain.PermanentFailureResult("recipient blacklisted", 422, 50*time.Millisecond),
		true,
	)
	n := seedNotificationInStatus(t, f.repo, "01NOTIFPROC", domain.StatusQueued)

	err := f.uc.Execute(context.Background(), application.ProcessNotificationInput{
		NotificationID: n.ID,
	})
	require.NoError(t, err)

	require.Equal(t, domain.StatusFailed, f.repo.store[n.ID].Status)
	require.Equal(t, "recipient blacklisted", f.repo.store[n.ID].LastError)

	// processing + failed log entries; no retrying.
	require.Len(t, f.logRepo.entries, 2)
	require.Equal(t, domain.LogEventFailed, f.logRepo.entries[1].Event)
}

// TestProcessNotification_TransientFailure: provider returns 5xx →
// retrying. NextRetryAt is set, BUT the use case now also returns a
// non-nil sentinel error so asynq's RetryDelayFunc can re-schedule the
// task natively — reconciler-driven retry was the prior shape and is
// being narrowed to a true safety-net role (ADR-0015).
func TestProcessNotification_TransientFailure(t *testing.T) {
	f := newProcessFixture(t,
		domain.TransientFailureResult("provider 503", 503, 2*time.Second),
		true,
	)
	n := seedNotificationInStatus(t, f.repo, "01NOTIFPROC", domain.StatusQueued)

	err := f.uc.Execute(context.Background(), application.ProcessNotificationInput{
		NotificationID: n.ID,
	})
	require.Error(t, err,
		"transient failure must surface as a non-nil error so asynq retries the task natively")
	require.ErrorIs(t, err, application.ErrProviderTransient,
		"the error must be the typed sentinel so the worker's RetryDelayFunc can route by errors.Is")

	final := f.repo.store[n.ID]
	require.Equal(t, domain.StatusRetrying, final.Status)
	require.Equal(t, "provider 503", final.LastError)
	require.NotNil(t, final.NextRetryAt, "transient failure must still record a hint for the reconciler safety-net")
	require.True(t, final.NextRetryAt.After(fixedAppNow), "next retry hint must be in the future")

	require.Len(t, f.logRepo.entries, 2)
	require.Equal(t, domain.LogEventRetrying, f.logRepo.entries[1].Event)
}

// TestProcessNotification_RateLimited: outbound limit rejects this
// attempt. The provider is not called; the notification falls back
// into retrying AND the use case returns a distinct sentinel
// (ErrOutboundRateLimited) so the worker's RetryDelayFunc applies the
// short rate-limit backoff instead of the exponential transient
// schedule.
func TestProcessNotification_RateLimited(t *testing.T) {
	f := newProcessFixture(t,
		domain.DeliveredResult("never-used", 0),
		false, // rate limit denied
	)
	f.rateLimiter.retryAfter = 1 * time.Second
	n := seedNotificationInStatus(t, f.repo, "01NOTIFPROC", domain.StatusQueued)

	err := f.uc.Execute(context.Background(), application.ProcessNotificationInput{
		NotificationID: n.ID,
	})
	require.Error(t, err,
		"rate-limited attempt must surface as a non-nil error so asynq retries the task with the rate-limit delay")
	require.ErrorIs(t, err, application.ErrOutboundRateLimited,
		"the error must be the rate-limit sentinel so RetryDelayFunc applies the short backoff, not the exponential one")
	require.NotErrorIs(t, err, application.ErrProviderTransient,
		"the rate-limit sentinel must NOT also satisfy errors.Is(ErrProviderTransient) — they pick different backoffs")

	// Provider never called.
	require.Empty(t, f.provider.calls)

	// Notification falls back into retrying with the rate-limited reason.
	final := f.repo.store[n.ID]
	require.Equal(t, domain.StatusRetrying, final.Status)

	// Rate limiter was consulted with the channel-namespaced bucket.
	require.Equal(t, []string{"channel:sms"}, f.rateLimiter.buckets)
}

// TestProcessNotification_LogsTerminalOutcomeWithCorrelationAndNoPII:
// E2E_REPORT.md §D flagged that the worker emits no per-task log line,
// so a correlation_id thread spans only the API side of a request.
// CLAUDE.md §3.8 / §12.2 mandate structured logs at every notable
// transition; this test pins the contract:
//
//	level=INFO  msg="processed notification"
//	fields:    notification_id, channel, priority, attempts, outcome,
//	           duration_ms, correlation_id (auto from contextHandler)
//	never:     recipient, content  (PII — CLAUDE.md §3.5, Sonar S5145)
//
// Capturing through slog.Default keeps production code free of a
// logger port — the project's logger plumbing
// (internal/infrastructure/logger.contextHandler) attaches
// correlation_id for any call site that uses slog.InfoContext.
func TestProcessNotification_LogsTerminalOutcomeWithCorrelationAndNoPII(t *testing.T) {
	logged := captureProcessLogs(t)

	f := newProcessFixture(t,
		domain.DeliveredResult("provider-id-xyz", 80*time.Millisecond),
		true,
	)
	n := seedNotificationInStatus(t, f.repo, "01NOTIFLOG0", domain.StatusQueued)

	const corr = "01CORRLOG0000000000000000000"
	ctx := correlation.WithContext(context.Background(), corr)
	err := f.uc.Execute(ctx, application.ProcessNotificationInput{NotificationID: n.ID})
	require.NoError(t, err)

	entry := findLogLine(t, logged, "processed notification")
	require.Equal(t, "INFO", entry["level"], "outcome line must be INFO")
	require.Equal(t, string(n.ID), entry["notification_id"])
	require.Equal(t, "sms", entry["channel"])
	require.Equal(t, "normal", entry["priority"])
	require.Equal(t, "delivered", entry["outcome"])
	require.Equal(t, corr, entry["correlation_id"],
		"contextHandler must propagate the correlation id from ctx")
	require.Contains(t, entry, "duration_ms",
		"duration_ms key must be present so dashboards can group by it (value may be 0 under a static test clock)")

	// PII guard: the seeded recipient string must NEVER appear in any
	// captured log line. Sonar S5145 (log injection / sensitive data
	// in logs) is the framing; CLAUDE.md §3.5 the explicit rule.
	all := logged.String()
	require.NotContains(t, all, n.Recipient,
		"recipient is PII and must never be logged; saw it in:\n%s", all)
}

// TestProcessNotification_LogsCorrelationFromNotificationEntity:
// when the worker is invoked by the asynq processor (queue handoff)
// the incoming ctx is "bare" — it does NOT carry the correlation_id
// the API stamped on the request. The use case must derive it from
// the notification entity (notif.CorrelationID) and inject it into
// ctx so every downstream log line, provider call, and broadcast
// publish carries the same id end-to-end.
//
// Without this, E2E_REPORT.md §D would still be partially red: the
// worker logs come out but with no correlation_id, breaking the
// "one ULID, end-to-end traceable" promise in CLAUDE.md §2.3.
func TestProcessNotification_LogsCorrelationFromNotificationEntity(t *testing.T) {
	logged := captureProcessLogs(t)

	f := newProcessFixture(t,
		domain.DeliveredResult("provider-id-zz", 70*time.Millisecond),
		true,
	)
	n := seedNotificationInStatus(t, f.repo, "01NOTIFLOG3", domain.StatusQueued)
	// seedNotificationInStatus sets CorrelationID to a known constant.
	require.NotEmpty(t, n.CorrelationID, "fixture must seed a correlation id")

	// Deliberately pass a BARE context — this mirrors the asynq
	// processor handoff in production (the worker does not inherit
	// the API request's ctx).
	err := f.uc.Execute(context.Background(),
		application.ProcessNotificationInput{NotificationID: n.ID})
	require.NoError(t, err)

	entry := findLogLine(t, logged, "processed notification")
	require.Equal(t, string(n.CorrelationID), entry["correlation_id"],
		"worker log must carry the notification's correlation id even when ctx is bare")
}

// TestProcessNotification_LogsFailedOutcome: terminal failed path must
// emit the same outcome line, with outcome=failed and the error
// reason attached. Guards the failure branch from silently regressing
// when the happy-path log assertion lands.
func TestProcessNotification_LogsFailedOutcome(t *testing.T) {
	logged := captureProcessLogs(t)

	f := newProcessFixture(t,
		domain.PermanentFailureResult("blacklisted-recipient", 422, 50*time.Millisecond),
		true,
	)
	n := seedNotificationInStatus(t, f.repo, "01NOTIFLOG1", domain.StatusQueued)

	err := f.uc.Execute(context.Background(),
		application.ProcessNotificationInput{NotificationID: n.ID})
	require.NoError(t, err)

	entry := findLogLine(t, logged, "processed notification")
	require.Equal(t, "failed", entry["outcome"])
	require.Equal(t, "blacklisted-recipient", entry["error"],
		"failed outcome must carry the provider's reason as the error field")
}

// captureProcessLogs swaps slog.Default with a JSON handler writing
// to an in-test buffer for the duration of the test. Returned pointer
// is the underlying buffer — read via Bytes() / String() once the
// use case has run. Cleanup restores the previous default.
func captureProcessLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	// Use the project's contextHandler shape so correlation_id pulled
	// from ctx survives the capture — wrapping a plain JSON handler is
	// enough because contextHandler logic lives on the slog handler
	// chain installed by infrastructure/logger.New. For this test the
	// minimal JSON handler combined with the slog.InfoContext call
	// site is sufficient; the production logger uses the same shape.
	logger := slog.New(newCorrelationCapturingHandler(&buf))
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// findLogLine scans the JSON-lines buffer for the first entry whose
// "msg" field matches want and returns it as a map. Fails the test
// when no matching line is present — keeps assertions terse.
func findLogLine(t *testing.T, buf *bytes.Buffer, want string) map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	require.NotEmpty(t, lines, "no log output captured")
	for _, line := range lines {
		var entry map[string]any
		if json.Unmarshal(line, &entry) != nil {
			continue // skip non-JSON noise; the captured handler should not emit any
		}
		if msg, _ := entry["msg"].(string); msg == want {
			return entry
		}
	}
	t.Fatalf("no log line with msg=%q in:\n%s", want, buf.String())
	return nil
}

// correlationCapturingHandler is a tiny slog.Handler that mirrors the
// project's production contextHandler behavior for the duration of a
// test: every record gets the correlation id pulled from the calling
// ctx attached as a top-level field, so log assertions can pin it.
type correlationCapturingHandler struct {
	inner slog.Handler
}

func newCorrelationCapturingHandler(buf *bytes.Buffer) *correlationCapturingHandler {
	return &correlationCapturingHandler{
		inner: slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
	}
}

func (h *correlationCapturingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *correlationCapturingHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := correlation.FromContext(ctx); id != "" {
		r.AddAttrs(slog.String("correlation_id", id))
	}
	return h.inner.Handle(ctx, r)
}

func (h *correlationCapturingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &correlationCapturingHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *correlationCapturingHandler) WithGroup(name string) slog.Handler {
	return &correlationCapturingHandler{inner: h.inner.WithGroup(name)}
}

// TestProcessNotification_RateLimited_IncrementsOutboundHitMetric:
// when the outbound limiter denies a Send the worker must increment
// notifications_outbound_rate_limit_hits_total tagged by channel.
// Surfaced during live re-verification of E2E_REPORT.md §H: the
// throttling itself works (the live load test pushed 6001 requests
// at 200 rps and saw ~3700 land in retrying with last_error
// "outbound rate limit exceeded"), but the counter stayed at zero
// because production code never called the metric. This test
// closes the observability gap.
func TestProcessNotification_RateLimited_IncrementsOutboundHitMetric(t *testing.T) {
	recorder := &recordingProcessMetrics{}
	f := newProcessFixtureWithMetrics(t,
		domain.DeliveredResult("never-used", 0),
		false, // rate limit denies
		recorder,
	)
	n := seedNotificationInStatus(t, f.repo, "01NOTIFRATE", domain.StatusQueued)

	err := f.uc.Execute(context.Background(),
		application.ProcessNotificationInput{NotificationID: n.ID})
	require.ErrorIs(t, err, application.ErrOutboundRateLimited,
		"asynq-native retry contract — rate-limited path returns the sentinel so RetryDelayFunc can route the timing")

	require.Equal(t, []string{"sms"}, recorder.outboundHits,
		"the rate-limited path must increment outbound_rate_limit_hits_total tagged by channel")
}

// newProcessFixtureWithMetrics is a variant of newProcessFixture that
// wires a caller-supplied MetricsRecorder. Kept separate so the
// existing tests stay short — they pass nil and skip emit.
func newProcessFixtureWithMetrics(t *testing.T, providerResult domain.DeliveryResult, rateAllowed bool, metrics application.MetricsRecorder) processFixture {
	t.Helper()
	repo := newFakeNotificationRepo()
	logRepo := newFakeNotificationLogRepo()
	provider := newFakeProvider(providerResult)
	rateLimiter := newFakeRateLimiter(rateAllowed)
	broadcaster := newFakeStatusBroadcaster()
	idGen := newDefaultFakeIDs()
	clock := newFakeClock(fixedAppNow)

	uc := application.NewProcessNotification(application.ProcessNotificationDeps{
		Repo:        repo,
		LogRepo:     logRepo,
		Provider:    provider,
		RateLimiter: rateLimiter,
		Broadcaster: broadcaster,
		IDGen:       idGen,
		Clock:       clock,
		Metrics:     metrics,
	})

	return processFixture{
		uc:          uc,
		repo:        repo,
		logRepo:     logRepo,
		provider:    provider,
		rateLimiter: rateLimiter,
		broadcaster: broadcaster,
	}
}

// recordingProcessMetrics is an in-memory MetricsRecorder for the
// process-notification tests. Only the call lists the tests inspect
// are populated; the other methods are kept for interface compliance.
type recordingProcessMetrics struct {
	outboundHits []string
}

func (r *recordingProcessMetrics) NotificationCreated(string, string)      {}
func (r *recordingProcessMetrics) NotificationDelivered(string)            {}
func (r *recordingProcessMetrics) NotificationFailed(string, string)       {}
func (r *recordingProcessMetrics) NotificationAttempt(string, string)      {}
func (r *recordingProcessMetrics) ObserveProcessing(string, time.Duration) {}
func (r *recordingProcessMetrics) OutboundRateLimitHit(channel string) {
	r.outboundHits = append(r.outboundHits, channel)
}

// TestProcessNotification_StartsProviderSendSpan: the worker must
// open the "provider.send" span through ports.Tracer, stamp the
// notification id + channel as initial attributes, decorate the span
// with provider.success / provider.retryable once the call returns,
// and End the span exactly once.
//
// The application layer used to call go.opentelemetry.io/otel directly
// — CLAUDE.md §3.3 forbids third-party imports inside
// internal/application, so the span work now flows through a port
// (E2E_REPORT.md §N gap). This test is the contract: production code
// MUST drive the injected ports.Tracer; without the refactor the
// recording fake stays empty and the assertion fails.
func TestProcessNotification_StartsProviderSendSpan(t *testing.T) {
	tracer := newRecordingTracer()
	f := newProcessFixtureWithTracer(t,
		domain.DeliveredResult("provider-id-trace", 25*time.Millisecond),
		true,
		tracer,
	)
	n := seedNotificationInStatus(t, f.repo, "01NOTIFTRC0", domain.StatusQueued)

	err := f.uc.Execute(context.Background(),
		application.ProcessNotificationInput{NotificationID: n.ID})
	require.NoError(t, err)

	spans := tracer.snapshot()
	require.Len(t, spans, 1, "exactly one span must be opened per Execute")

	got := spans[0]
	require.Equal(t, "provider.send", got.name)
	require.True(t, got.ended, "span must be End()-ed before Execute returns")

	require.Equal(t, string(n.ID), got.stringAttrs["notification.id"],
		"notification.id must be stamped as an initial string attribute")
	require.Equal(t, "sms", got.stringAttrs["notification.channel"],
		"notification.channel must be stamped as an initial string attribute")

	require.Equal(t, true, got.boolAttrs["provider.success"],
		"provider.success must be stamped after the provider call returns")
	require.Equal(t, false, got.boolAttrs["provider.retryable"],
		"provider.retryable must be stamped after the provider call returns")
}

// newProcessFixtureWithTracer mirrors newProcessFixtureWithMetrics
// but injects a caller-supplied Tracer instead of a MetricsRecorder.
// Kept separate so existing tests stay short — they implicitly use
// ports.NoopTracer.
func newProcessFixtureWithTracer(t *testing.T, providerResult domain.DeliveryResult, rateAllowed bool, tracer ports.Tracer) processFixture {
	t.Helper()
	repo := newFakeNotificationRepo()
	logRepo := newFakeNotificationLogRepo()
	provider := newFakeProvider(providerResult)
	rateLimiter := newFakeRateLimiter(rateAllowed)
	broadcaster := newFakeStatusBroadcaster()
	idGen := newDefaultFakeIDs()
	clock := newFakeClock(fixedAppNow)

	uc := application.NewProcessNotification(application.ProcessNotificationDeps{
		Repo:        repo,
		LogRepo:     logRepo,
		Provider:    provider,
		RateLimiter: rateLimiter,
		Broadcaster: broadcaster,
		IDGen:       idGen,
		Clock:       clock,
		Tracer:      tracer,
	})

	return processFixture{
		uc:          uc,
		repo:        repo,
		logRepo:     logRepo,
		provider:    provider,
		rateLimiter: rateLimiter,
		broadcaster: broadcaster,
	}
}

// recordingTracer is an in-memory ports.Tracer the test uses to assert
// span lifecycle. Every StartSpan call appends a *recordedSpan; every
// SetXxxAttr / End mutates that span in place so the test can read
// back what the production code stamped.
type recordingTracer struct {
	mu    sync.Mutex
	spans []*recordedSpan
}

func newRecordingTracer() *recordingTracer {
	return &recordingTracer{}
}

func (r *recordingTracer) StartSpan(ctx context.Context, name string) (context.Context, ports.Span) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := &recordedSpan{
		name:        name,
		stringAttrs: map[string]string{},
		boolAttrs:   map[string]bool{},
	}
	r.spans = append(r.spans, s)
	return ctx, s
}

// snapshot returns a copy of the recorded spans safe to read outside
// the tracer's lock.
func (r *recordingTracer) snapshot() []*recordedSpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*recordedSpan, len(r.spans))
	copy(out, r.spans)
	return out
}

type recordedSpan struct {
	mu          sync.Mutex
	name        string
	stringAttrs map[string]string
	boolAttrs   map[string]bool
	ended       bool
}

func (s *recordedSpan) SetStringAttr(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stringAttrs[key] = value
}

func (s *recordedSpan) SetBoolAttr(key string, value bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.boolAttrs[key] = value
}

func (s *recordedSpan) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
}

// TestProcessNotification_NotFound: missing id surfaces ErrNotFound; nothing
// downstream runs.
func TestProcessNotification_NotFound(t *testing.T) {
	f := newProcessFixture(t,
		domain.DeliveredResult("never", 0),
		true,
	)

	err := f.uc.Execute(context.Background(), application.ProcessNotificationInput{
		NotificationID: "01MISSING000000000000000000",
	})
	require.ErrorIs(t, err, ports.ErrNotFound)
	require.Empty(t, f.provider.calls)
	require.Empty(t, f.rateLimiter.buckets)
}
