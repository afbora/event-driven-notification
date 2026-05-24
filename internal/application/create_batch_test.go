package application_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// newCreateBatch wires the fakes for a CreateBatch test.
func newCreateBatch(t *testing.T) (
	*application.CreateBatch,
	*fakeBatchRepo,
	*fakeNotificationLogRepo,
	*fakeQueue,
) {
	t.Helper()
	notifRepo := newFakeNotificationRepo()
	batchRepo := newFakeBatchRepo().WithNotifSink(notifRepo)
	logRepo := newFakeNotificationLogRepo()
	queue := newFakeQueue()
	idGen := newDefaultFakeIDs()
	clock := newFakeClock(fixedAppNow)
	uc := application.NewCreateBatch(batchRepo, notifRepo, logRepo, queue, idGen, clock, nil)
	return uc, batchRepo, logRepo, queue
}

// TestCreateBatch_HappyPath: 3 SMS notifications, single correlation id,
// every notification linked to the batch, every one persisted + logged +
// enqueued.
func TestCreateBatch_HappyPath(t *testing.T) {
	uc, batchRepo, logRepo, queue := newCreateBatch(t)

	items := []application.CreateBatchItem{
		{Channel: "sms", Priority: "normal", Recipient: "+905551111111", Content: "Hello 1"},
		{Channel: "sms", Priority: "normal", Recipient: "+905552222222", Content: "Hello 2"},
		{Channel: "sms", Priority: "normal", Recipient: "+905553333333", Content: "Hello 3"},
	}

	batch, err := uc.Execute(context.Background(), application.CreateBatchInput{
		CorrelationID: "01HXYZBATCHCORR00000000000",
		Notifications: items,
	})
	require.NoError(t, err)

	// Batch shape.
	require.Equal(t, domain.BatchID("01BATCH01"), batch.ID)
	require.Len(t, batch.Notifications, 3)
	require.Equal(t, "01HXYZBATCHCORR00000000000", batch.CorrelationID)
	require.True(t, batch.CreatedAt.Equal(fixedAppNow))

	// Every notification auto-linked, same correlation id, initial state.
	for i, n := range batch.Notifications {
		require.NotNilf(t, n.BatchID, "notification %d: BatchID is nil", i)
		require.Equalf(t, batch.ID, *n.BatchID, "notification %d: BatchID mismatch", i)
		require.Equalf(t, batch.CorrelationID, n.CorrelationID, "notification %d: correlation id mismatch", i)
		require.Equalf(t, domain.StatusQueued, n.Status,
			"notification %d: CreateBatch advances pending → queued so the worker's atomic claim accepts the task", i)
	}

	// Persisted atomically (one batch entry; postgres adapter will fan out).
	require.Len(t, batchRepo.store, 1)

	// Two log entries per notification: "created" before enqueue,
	// "queued" after the pending → queued transition. The loop
	// alternates created/queued because CreateBatch records both for
	// each notification before moving on to the next.
	require.Len(t, logRepo.entries, 6)
	for i, entry := range logRepo.entries {
		want := domain.LogEventCreated
		if i%2 == 1 {
			want = domain.LogEventQueued
		}
		require.Equalf(t, want, entry.Event, "log %d: event mismatch", i)
		require.Equalf(t, batch.CorrelationID, entry.CorrelationID, "log %d: correlation id mismatch", i)
	}

	// Every notification enqueued for immediate delivery.
	require.Len(t, queue.items, 3)
	for i, item := range queue.items {
		require.NotNilf(t, item.NotificationID, "queue item %d: notification id missing", i)
		require.Nilf(t, item.ScheduledAt, "queue item %d: should not be scheduled", i)
	}
}

// TestCreateBatch_GeneratesCorrelationIDIfMissing parallels the same case in
// CreateNotification: empty CorrelationID → IDGenerator fills it in, and
// every notification in the batch shares it.
func TestCreateBatch_GeneratesCorrelationIDIfMissing(t *testing.T) {
	uc, _, _, _ := newCreateBatch(t)

	batch, err := uc.Execute(context.Background(), application.CreateBatchInput{
		// CorrelationID intentionally empty.
		Notifications: []application.CreateBatchItem{
			{Channel: "email", Priority: "normal", Recipient: "a@example.com", Content: "Body 1"},
			{Channel: "email", Priority: "normal", Recipient: "b@example.com", Content: "Body 2"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "01CORR01", batch.CorrelationID)
	for _, n := range batch.Notifications {
		require.Equal(t, "01CORR01", n.CorrelationID)
	}
}

// TestCreateBatch_EmptyNotifications: zero-length input rejected by the
// domain (Batch min size is 1). No side effects.
func TestCreateBatch_EmptyNotifications(t *testing.T) {
	uc, batchRepo, logRepo, queue := newCreateBatch(t)

	_, err := uc.Execute(context.Background(), application.CreateBatchInput{
		CorrelationID: "01CORR",
		Notifications: nil,
	})
	require.ErrorIs(t, err, domain.ErrInvalidBatchSize)
	require.Empty(t, batchRepo.store)
	require.Empty(t, logRepo.entries)
	require.Empty(t, queue.items)
}

// TestCreateBatch_InvalidNotificationInItem: a single bad item aborts the
// whole batch; no partial persistence, no orphan enqueues.
func TestCreateBatch_InvalidNotificationInItem(t *testing.T) {
	uc, batchRepo, logRepo, queue := newCreateBatch(t)

	_, err := uc.Execute(context.Background(), application.CreateBatchInput{
		CorrelationID: "01CORR",
		Notifications: []application.CreateBatchItem{
			{Channel: "sms", Priority: "normal", Recipient: "+905551111111", Content: "ok"},
			{Channel: "fax", Priority: "normal", Recipient: "+1", Content: "bad channel"},
			{Channel: "sms", Priority: "normal", Recipient: "+905552222222", Content: "never reached"},
		},
	})
	require.ErrorIs(t, err, domain.ErrInvalidChannel)
	require.Empty(t, batchRepo.store, "no partial persistence")
	require.Empty(t, logRepo.entries)
	require.Empty(t, queue.items)
}
