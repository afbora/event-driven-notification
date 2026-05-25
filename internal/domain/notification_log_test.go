package domain_test

import (
	"errors"
	"testing"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// TestNewLogEvent mirrors the Channel/Priority/Status parser pattern. The
// `created` event is the only one that does not coincide with a Status
// name — it marks the very first row written when a notification is
// persisted, before any status transition.
func TestNewLogEvent(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    domain.LogEvent
		wantErr error
	}{
		{name: "created", input: "created", want: domain.LogEventCreated},
		{name: "queued", input: "queued", want: domain.LogEventQueued},
		{name: "processing", input: "processing", want: domain.LogEventProcessing},
		{name: "delivered", input: "delivered", want: domain.LogEventDelivered},
		{name: "retrying", input: "retrying", want: domain.LogEventRetrying},
		{name: "failed", input: "failed", want: domain.LogEventFailed},
		{name: "cancelled", input: "cancelled", want: domain.LogEventCancelled},
		{name: "uppercase normalized", input: "CREATED", want: domain.LogEventCreated},
		{name: "whitespace trimmed", input: "  failed  ", want: domain.LogEventFailed},
		{name: "empty", input: "", wantErr: domain.ErrInvalidLogEvent},
		{name: "unknown", input: "sent", wantErr: domain.ErrInvalidLogEvent},
		{name: "near-miss", input: "creating", wantErr: domain.ErrInvalidLogEvent},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.NewLogEvent(tc.input)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("event = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLogEvent_String(t *testing.T) {
	cases := map[domain.LogEvent]string{
		domain.LogEventCreated:    "created",
		domain.LogEventQueued:     "queued",
		domain.LogEventProcessing: "processing",
		domain.LogEventDelivered:  "delivered",
		domain.LogEventRetrying:   "retrying",
		domain.LogEventFailed:     "failed",
		domain.LogEventCancelled:  "cancelled",
	}
	for e, want := range cases {
		t.Run(want, func(t *testing.T) {
			if got := e.String(); got != want {
				t.Errorf("LogEvent.String() = %q, want %q", got, want)
			}
		})
	}
}

func TestLogEvent_IsValid(t *testing.T) {
	valid := []domain.LogEvent{
		domain.LogEventCreated,
		domain.LogEventQueued,
		domain.LogEventProcessing,
		domain.LogEventDelivered,
		domain.LogEventRetrying,
		domain.LogEventFailed,
		domain.LogEventCancelled,
	}
	for _, e := range valid {
		if !e.IsValid() {
			t.Errorf("LogEvent(%q).IsValid() = false, want true", e)
		}
	}

	invalid := []domain.LogEvent{
		domain.LogEvent(""),
		domain.LogEvent("CREATED"), // not normalised
		domain.LogEvent("sent"),
	}
	for _, e := range invalid {
		if e.IsValid() {
			t.Errorf("LogEvent(%q).IsValid() = true, want false", e)
		}
	}
}

// validLogInput returns a known-good NewNotificationLogInput.
func validLogInput() domain.NewNotificationLogInput {
	return domain.NewNotificationLogInput{
		ID:             "01HXYZLOG00000000000000000",
		NotificationID: "01HXYZNOTIF000000000000000",
		CorrelationID:  "01HXYZCORR000000000000000",
		Event:          domain.LogEventCreated,
		Details:        map[string]any{"channel": "sms"},
	}
}

// TestNewNotificationLog covers constructor validation.
func TestNewNotificationLog(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*domain.NewNotificationLogInput)
		wantErr error
	}{
		{name: "valid with details", mutate: func(_ *domain.NewNotificationLogInput) {}},
		{
			name: "valid with nil details",
			mutate: func(in *domain.NewNotificationLogInput) {
				in.Details = nil
			},
		},
		{
			name: "valid with empty details",
			mutate: func(in *domain.NewNotificationLogInput) {
				in.Details = map[string]any{}
			},
		},

		// Required fields
		{
			name:    "empty log id",
			mutate:  func(in *domain.NewNotificationLogInput) { in.ID = "" },
			wantErr: domain.ErrInvalidLogID,
		},
		{
			name:    "empty notification id",
			mutate:  func(in *domain.NewNotificationLogInput) { in.NotificationID = "" },
			wantErr: domain.ErrInvalidNotificationID,
		},
		{
			name:    "empty correlation id",
			mutate:  func(in *domain.NewNotificationLogInput) { in.CorrelationID = "" },
			wantErr: domain.ErrInvalidCorrelationID,
		},
		{
			name:    "invalid event",
			mutate:  func(in *domain.NewNotificationLogInput) { in.Event = domain.LogEvent("nope") },
			wantErr: domain.ErrInvalidLogEvent,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := validLogInput()
			tc.mutate(&in)
			assertNewNotificationLog(t, in, tc.wantErr)
		})
	}
}

// assertNewNotificationLog drives NewNotificationLog and applies the
// per-case assertion: on wantErr the function checks errors.Is and
// stops; on success it validates the surfaced fields (ID,
// NotificationID, Event) and CreatedAt parity with the injected clock.
// Splitting the assertions out keeps TestNewNotificationLog's body at
// a single flow-control step.
func assertNewNotificationLog(t *testing.T, in domain.NewNotificationLogInput, wantErr error) {
	t.Helper()
	entry, err := domain.NewNotificationLog(in, fixedNow)

	if wantErr != nil {
		if !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want errors.Is(_, %v)", err, wantErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if entry == nil {
		t.Fatal("entry is nil")
	}
	if entry.ID != in.ID {
		t.Errorf("ID = %q, want %q", entry.ID, in.ID)
	}
	if entry.NotificationID != in.NotificationID {
		t.Errorf("NotificationID = %q, want %q", entry.NotificationID, in.NotificationID)
	}
	if entry.Event != in.Event {
		t.Errorf("Event = %q, want %q", entry.Event, in.Event)
	}
	if !entry.CreatedAt.Equal(fixedNow) {
		t.Errorf("CreatedAt = %v, want %v", entry.CreatedAt, fixedNow)
	}
}
