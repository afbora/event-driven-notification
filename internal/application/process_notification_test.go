package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
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

	uc := application.NewProcessNotification(repo, logRepo, provider, rateLimiter, broadcaster, idGen, clock)

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

// TestProcessNotification_TransientFailure: provider returns 5xx → retrying.
// NextRetryAt is set; the worker hands control back to asynq which respects
// the schedule.
func TestProcessNotification_TransientFailure(t *testing.T) {
	f := newProcessFixture(t,
		domain.TransientFailureResult("provider 503", 503, 2*time.Second),
		true,
	)
	n := seedNotificationInStatus(t, f.repo, "01NOTIFPROC", domain.StatusQueued)

	err := f.uc.Execute(context.Background(), application.ProcessNotificationInput{
		NotificationID: n.ID,
	})
	require.NoError(t, err)

	final := f.repo.store[n.ID]
	require.Equal(t, domain.StatusRetrying, final.Status)
	require.Equal(t, "provider 503", final.LastError)
	require.NotNil(t, final.NextRetryAt, "transient failure must schedule a retry time")
	require.True(t, final.NextRetryAt.After(fixedAppNow), "next retry must be in the future")

	require.Len(t, f.logRepo.entries, 2)
	require.Equal(t, domain.LogEventRetrying, f.logRepo.entries[1].Event)
}

// TestProcessNotification_RateLimited: outbound limit rejects this attempt.
// The provider is not called; the notification stays in retrying so the
// queue can re-deliver it after a short delay.
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
	require.NoError(t, err)

	// Provider never called.
	require.Empty(t, f.provider.calls)

	// Notification falls back into retrying with the rate-limited reason.
	final := f.repo.store[n.ID]
	require.Equal(t, domain.StatusRetrying, final.Status)

	// Rate limiter was consulted with the channel-namespaced bucket.
	require.Equal(t, []string{"channel:sms"}, f.rateLimiter.buckets)
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
