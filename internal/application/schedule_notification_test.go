package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/application"
)

// newScheduleNotification wires the inner CreateNotification with real fakes
// so the composition pattern is exercised end-to-end. Returns the queue so
// tests can assert which Enqueue variant was called.
func newScheduleNotification(t *testing.T) (
	*application.ScheduleNotification,
	*fakeNotificationRepo,
	*fakeQueue,
) {
	t.Helper()
	repo := newFakeNotificationRepo()
	logRepo := newFakeNotificationLogRepo()
	tmplRepo := newFakeTemplateRepo()
	queue := newFakeQueue()
	idGen := newDefaultFakeIDs()
	clock := newFakeClock(fixedAppNow)

	createUC := application.NewCreateNotification(repo, logRepo, tmplRepo, queue, idGen, clock, nil)
	uc := application.NewScheduleNotification(createUC, clock)
	return uc, repo, queue
}

// TestScheduleNotification_HappyPath: future scheduled_at → notification
// persisted + scheduled-enqueue (not immediate).
func TestScheduleNotification_HappyPath(t *testing.T) {
	uc, repo, queue := newScheduleNotification(t)

	scheduledAt := fixedAppNow.Add(6 * time.Hour)

	n, err := uc.Execute(context.Background(), application.ScheduleNotificationInput{
		Channel:       "sms",
		Priority:      "normal",
		Recipient:     "+905551234567",
		Content:       "Reminder",
		CorrelationID: "01CORRSCHED000000000000000",
		ScheduledAt:   scheduledAt,
	})
	require.NoError(t, err)

	// Notification carries the scheduled_at pointer.
	require.NotNil(t, n.ScheduledAt)
	require.Equal(t, scheduledAt, *n.ScheduledAt)

	// Persisted exactly once.
	require.Len(t, repo.store, 1)

	// Scheduled enqueue, not immediate.
	require.Len(t, queue.items, 1)
	require.NotNil(t, queue.items[0].ScheduledAt)
	require.Equal(t, scheduledAt, *queue.items[0].ScheduledAt)
}

// TestScheduleNotification_InPastRejected: scheduled_at strictly before now
// fails fast — no repo write, no enqueue.
func TestScheduleNotification_InPastRejected(t *testing.T) {
	uc, repo, queue := newScheduleNotification(t)

	pastTime := fixedAppNow.Add(-1 * time.Hour)

	_, err := uc.Execute(context.Background(), application.ScheduleNotificationInput{
		Channel:       "sms",
		Priority:      "normal",
		Recipient:     "+905551234567",
		Content:       "Reminder",
		CorrelationID: "01CORRSCHED000000000000000",
		ScheduledAt:   pastTime,
	})
	require.ErrorIs(t, err, application.ErrScheduledInPast)
	require.Empty(t, repo.store, "no side effects on validation failure")
	require.Empty(t, queue.items)
}

// TestScheduleNotification_AtPresentRejected: scheduled_at equal to clock.Now
// is also rejected — callers wanting immediate delivery use
// CreateNotification, not ScheduleNotification.
func TestScheduleNotification_AtPresentRejected(t *testing.T) {
	uc, _, _ := newScheduleNotification(t)

	_, err := uc.Execute(context.Background(), application.ScheduleNotificationInput{
		Channel:       "sms",
		Priority:      "normal",
		Recipient:     "+905551234567",
		Content:       "Reminder",
		CorrelationID: "01CORRSCHED000000000000000",
		ScheduledAt:   fixedAppNow, // exactly now
	})
	require.ErrorIs(t, err, application.ErrScheduledInPast)
}

// TestScheduleNotification_PropagatesCreateError: validation errors from the
// inner CreateNotification (e.g. invalid channel) bubble up unchanged.
func TestScheduleNotification_PropagatesCreateError(t *testing.T) {
	uc, _, _ := newScheduleNotification(t)

	_, err := uc.Execute(context.Background(), application.ScheduleNotificationInput{
		Channel:       "fax", // invalid
		Priority:      "normal",
		Recipient:     "+905551234567",
		Content:       "x",
		CorrelationID: "01CORRSCHED000000000000000",
		ScheduledAt:   fixedAppNow.Add(1 * time.Hour),
	})
	require.Error(t, err)
}
