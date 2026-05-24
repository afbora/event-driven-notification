package asynq_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	hibikenasynq "github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"

	asynqadapter "github.com/afbora/event-driven-notification/internal/adapters/asynq"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// makeTask is a tiny helper to build an asynq.Task with the canonical
// payload shape this adapter uses.
func makeTask(t *testing.T, notifID string) *hibikenasynq.Task {
	t.Helper()
	payload, err := json.Marshal(asynqadapter.ProcessNotificationPayload{NotificationID: notifID})
	require.NoError(t, err)
	return hibikenasynq.NewTask(asynqadapter.TypeProcessNotification, payload)
}

// TestProcessor_HandleProcessNotification_Success: payload decodes, use
// case invoked with the right NotificationID, returns nil.
func TestProcessor_HandleProcessNotification_Success(t *testing.T) {
	var captured application.ProcessNotificationInput
	var called int
	processor := asynqadapter.NewProcessor(func(_ context.Context, in application.ProcessNotificationInput) error {
		captured = in
		called++
		return nil
	})

	task := makeTask(t, "01940000-0000-7000-8000-000000000001")
	err := processor.HandleProcessNotification(context.Background(), task)
	require.NoError(t, err)
	require.Equal(t, 1, called, "executor must run exactly once")
	require.Equal(t, domain.NotificationID("01940000-0000-7000-8000-000000000001"), captured.NotificationID)
}

// TestProcessor_HandleProcessNotification_UseCaseError: when the use case
// returns an error, the handler propagates it verbatim so asynq can retry.
func TestProcessor_HandleProcessNotification_UseCaseError(t *testing.T) {
	wantErr := errors.New("provider 503")
	processor := asynqadapter.NewProcessor(func(_ context.Context, _ application.ProcessNotificationInput) error {
		return wantErr
	})

	task := makeTask(t, "01940000-0000-7000-8000-000000000002")
	err := processor.HandleProcessNotification(context.Background(), task)
	require.ErrorIs(t, err, wantErr)
}

// TestProcessor_HandleProcessNotification_MalformedPayload: bad JSON in the
// payload surfaces as an error before the use case is touched.
func TestProcessor_HandleProcessNotification_MalformedPayload(t *testing.T) {
	var called int
	processor := asynqadapter.NewProcessor(func(_ context.Context, _ application.ProcessNotificationInput) error {
		called++
		return nil
	})

	task := hibikenasynq.NewTask(asynqadapter.TypeProcessNotification, []byte("not-json"))
	err := processor.HandleProcessNotification(context.Background(), task)
	require.Error(t, err)
	require.Zero(t, called, "use case must not run on payload decode failure")
}

// TestProcessor_HandleProcessNotification_EmptyNotificationID: empty id in
// a structurally valid payload is rejected so a malformed enqueue cannot
// confuse the use case with a zero-value id.
func TestProcessor_HandleProcessNotification_EmptyNotificationID(t *testing.T) {
	var called int
	processor := asynqadapter.NewProcessor(func(_ context.Context, _ application.ProcessNotificationInput) error {
		called++
		return nil
	})

	task := makeTask(t, "")
	err := processor.HandleProcessNotification(context.Background(), task)
	require.Error(t, err)
	require.Zero(t, called)
}

// TestProcessor_Register: registering the handler against an asynq ServeMux
// does not panic and the mux survives subsequent ServeMux operations.
// This is a smoke test for the wiring layer cmd/worker depends on.
func TestProcessor_Register(t *testing.T) {
	processor := asynqadapter.NewProcessor(func(_ context.Context, _ application.ProcessNotificationInput) error {
		return nil
	})

	mux := hibikenasynq.NewServeMux()
	require.NotPanics(t, func() { processor.Register(mux) }, "Register must not panic")
}
