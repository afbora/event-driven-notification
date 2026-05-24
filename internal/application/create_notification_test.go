package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// fixedNow is the synthetic clock used by every CreateNotification test.
var fixedAppNow = time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

// newCreateNotification wires every fake in one place to keep individual
// tests focused on assertions.
func newCreateNotification(t *testing.T) (
	*application.CreateNotification,
	*fakeNotificationRepo,
	*fakeNotificationLogRepo,
	*fakeQueue,
	*fakeIDGenerator,
) {
	t.Helper()
	repo := newFakeNotificationRepo()
	logRepo := newFakeNotificationLogRepo()
	tmplRepo := newFakeTemplateRepo()
	queue := newFakeQueue()
	idGen := newDefaultFakeIDs()
	clock := newFakeClock(fixedAppNow)
	uc := application.NewCreateNotification(repo, logRepo, tmplRepo, queue, idGen, clock)
	return uc, repo, logRepo, queue, idGen
}

// TestCreateNotification_HappyPath exercises the immediate-delivery path:
// SMS notification with a caller-supplied correlation ID.
func TestCreateNotification_HappyPath(t *testing.T) {
	uc, repo, logRepo, queue, _ := newCreateNotification(t)

	n, err := uc.Execute(context.Background(), application.CreateNotificationInput{
		Channel:       "sms",
		Priority:      "normal",
		Recipient:     "+905551234567",
		Content:       "Hello",
		CorrelationID: "01HXYZINBOUNDCORR0000000000",
	})
	require.NoError(t, err)

	// CreateNotification persists pending, enqueues, then advances
	// pending → queued so the worker's atomic claim accepts the
	// notification.
	require.Equal(t, domain.NotificationID("01NOTIF01"), n.ID)
	require.Equal(t, domain.StatusQueued, n.Status)
	require.Equal(t, domain.ChannelSMS, n.Channel)
	require.Equal(t, "01HXYZINBOUNDCORR0000000000", n.CorrelationID)
	require.True(t, n.CreatedAt.Equal(fixedAppNow))

	// Persisted exactly once.
	require.Len(t, repo.store, 1)

	// Two notification_logs entries: "created" before the enqueue and
	// "queued" after the pending → queued transition.
	require.Len(t, logRepo.entries, 2)
	require.Equal(t, domain.LogEventCreated, logRepo.entries[0].Event)
	require.Equal(t, domain.LogEventQueued, logRepo.entries[1].Event)
	require.Equal(t, n.ID, logRepo.entries[0].NotificationID)
	require.Equal(t, n.CorrelationID, logRepo.entries[0].CorrelationID)

	// Enqueued immediately, not scheduled.
	require.Len(t, queue.items, 1)
	require.Equal(t, n.ID, queue.items[0].NotificationID)
	require.Equal(t, domain.PriorityNormal, queue.items[0].Priority)
	require.Nil(t, queue.items[0].ScheduledAt)
}

// TestCreateNotification_GeneratesCorrelationIDIfMissing confirms that an
// empty correlation ID triggers server-side generation via IDGenerator. This
// preserves the invariant in CLAUDE.md §2.3 that every notification has one.
func TestCreateNotification_GeneratesCorrelationIDIfMissing(t *testing.T) {
	uc, _, logRepo, _, _ := newCreateNotification(t)

	n, err := uc.Execute(context.Background(), application.CreateNotificationInput{
		Channel:   "email",
		Priority:  "high",
		Recipient: "user@example.com",
		Content:   "Test body",
		// CorrelationID intentionally empty.
	})
	require.NoError(t, err)
	require.Equal(t, "01CORR01", n.CorrelationID, "use case must generate one when not provided")

	// Log entry carries the generated correlation ID too.
	require.Equal(t, n.CorrelationID, logRepo.entries[0].CorrelationID)
}

// TestCreateNotification_InvalidChannel confirms input parsing fails fast
// before any side effects (no repo write, no log entry, no enqueue).
func TestCreateNotification_InvalidChannel(t *testing.T) {
	uc, repo, logRepo, queue, _ := newCreateNotification(t)

	_, err := uc.Execute(context.Background(), application.CreateNotificationInput{
		Channel:       "fax",
		Priority:      "normal",
		Recipient:     "+1",
		Content:       "x",
		CorrelationID: "01CORR",
	})
	require.ErrorIs(t, err, domain.ErrInvalidChannel)

	require.Empty(t, repo.store, "no side effects on validation failure")
	require.Empty(t, logRepo.entries)
	require.Empty(t, queue.items)
}

// TestCreateNotification_Scheduled exercises the future-delivery path: the
// queue receives EnqueueScheduled, not Enqueue.
func TestCreateNotification_Scheduled(t *testing.T) {
	uc, _, _, queue, _ := newCreateNotification(t)
	scheduled := fixedAppNow.Add(6 * time.Hour)

	n, err := uc.Execute(context.Background(), application.CreateNotificationInput{
		Channel:       "push",
		Priority:      "normal",
		Recipient:     "fcm-token-abc",
		Content:       "Reminder",
		CorrelationID: "01CORRSCHED",
		ScheduledAt:   &scheduled,
	})
	require.NoError(t, err)

	require.Len(t, queue.items, 1)
	require.NotNil(t, queue.items[0].ScheduledAt, "scheduled enqueue, not immediate")
	require.Equal(t, scheduled, *queue.items[0].ScheduledAt)
	require.Equal(t, n.ID, queue.items[0].NotificationID)
}
