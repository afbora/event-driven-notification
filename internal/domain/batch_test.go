package domain_test

import (
	"errors"
	"strconv"
	"testing"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// makeNotifications creates `n` notifications that all share `correlationID`.
// Each gets a unique NotificationID derived from `i` so Batch construction
// has something realistic to validate.
func makeNotifications(t *testing.T, n int, correlationID string) []*domain.Notification {
	t.Helper()
	out := make([]*domain.Notification, n)
	for i := range n {
		in := validInput()
		in.ID = domain.NotificationID("01HXYZNOTIF" + strconv.Itoa(i))
		in.CorrelationID = correlationID
		notif, err := domain.NewNotification(in, fixedNow)
		if err != nil {
			t.Fatalf("setup notif %d: %v", i, err)
		}
		out[i] = notif
	}
	return out
}

// validBatchInput returns a known-good NewBatchInput with 3 notifications
// that share the batch's correlation ID. Tests mutate only the field they
// are exercising.
func validBatchInput(t *testing.T) domain.NewBatchInput {
	t.Helper()
	corrID := "01HXYZBATCHCORR9N6V0V9N6V0V"
	return domain.NewBatchInput{
		ID:            "01HXYZBATCH00000000000000000",
		CorrelationID: corrID,
		Notifications: makeNotifications(t, 3, corrID),
	}
}

// TestNewBatch exercises validation and the auto-set BatchID behaviour.
func TestNewBatch(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, *domain.NewBatchInput)
		wantErr error
	}{
		{
			name:   "valid 3 notifications",
			mutate: func(_ *testing.T, _ *domain.NewBatchInput) {},
		},
		{
			name: "min 1 notification",
			mutate: func(t *testing.T, in *domain.NewBatchInput) {
				in.Notifications = makeNotifications(t, 1, in.CorrelationID)
			},
		},
		{
			name: "max 1000 notifications",
			mutate: func(t *testing.T, in *domain.NewBatchInput) {
				in.Notifications = makeNotifications(t, 1000, in.CorrelationID)
			},
		},
		{
			name: "empty notifications",
			mutate: func(_ *testing.T, in *domain.NewBatchInput) {
				in.Notifications = nil
			},
			wantErr: domain.ErrInvalidBatchSize,
		},
		{
			name: "1001 notifications (over limit)",
			mutate: func(t *testing.T, in *domain.NewBatchInput) {
				in.Notifications = makeNotifications(t, 1001, in.CorrelationID)
			},
			wantErr: domain.ErrInvalidBatchSize,
		},
		{
			name: "empty id",
			mutate: func(_ *testing.T, in *domain.NewBatchInput) {
				in.ID = ""
			},
			wantErr: domain.ErrInvalidBatchID,
		},
		{
			name: "empty correlation id",
			mutate: func(_ *testing.T, in *domain.NewBatchInput) {
				in.CorrelationID = ""
			},
			wantErr: domain.ErrInvalidCorrelationID,
		},
		{
			name: "mismatched correlation id across notifications",
			mutate: func(_ *testing.T, in *domain.NewBatchInput) {
				// Change one notification's correlation ID to break uniformity.
				in.Notifications[1].CorrelationID = "01HXYZOTHER0000000000000000"
			},
			wantErr: domain.ErrBatchInconsistentCorrelation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := validBatchInput(t)
			tc.mutate(t, &in)

			batch, err := domain.NewBatch(in, fixedNow)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if batch == nil {
				t.Fatal("batch is nil")
			}
			if batch.ID != in.ID {
				t.Errorf("batch.ID = %q, want %q", batch.ID, in.ID)
			}
			if !batch.CreatedAt.Equal(fixedNow) {
				t.Errorf("CreatedAt = %v, want %v", batch.CreatedAt, fixedNow)
			}
			// Every notification's BatchID is auto-set to the batch's ID.
			for i, n := range batch.Notifications {
				if n.BatchID == nil {
					t.Errorf("notification %d: BatchID is nil", i)
					continue
				}
				if *n.BatchID != in.ID {
					t.Errorf("notification %d: BatchID = %q, want %q", i, *n.BatchID, in.ID)
				}
			}
		})
	}
}

// TestBatch_Size exposes the simple count accessor.
func TestBatch_Size(t *testing.T) {
	in := validBatchInput(t)
	batch, err := domain.NewBatch(in, fixedNow)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if got := batch.Size(); got != 3 {
		t.Errorf("Size() = %d, want 3", got)
	}
}

// markIntoStatus walks a fresh pending notification through the FSM until it
// reaches `target`. Used by TestBatch_StatusSummary to populate a batch with
// notifications in every status. Returns the notification it produced.
func markIntoStatus(t *testing.T, n *domain.Notification, target domain.Status) {
	t.Helper()
	switch target {
	case domain.StatusPending:
		// already pending
	case domain.StatusQueued:
		mustMark(t, n.MarkQueued(fixedNow))
	case domain.StatusProcessing:
		mustMark(t, n.MarkQueued(fixedNow))
		mustMark(t, n.MarkProcessing(fixedNow))
	case domain.StatusDelivered:
		mustMark(t, n.MarkQueued(fixedNow))
		mustMark(t, n.MarkProcessing(fixedNow))
		mustMark(t, n.MarkDelivered(fixedNow))
	case domain.StatusFailed:
		mustMark(t, n.MarkQueued(fixedNow))
		mustMark(t, n.MarkProcessing(fixedNow))
		mustMark(t, n.MarkFailed(fixedNow, "test"))
	case domain.StatusRetrying:
		mustMark(t, n.MarkQueued(fixedNow))
		mustMark(t, n.MarkProcessing(fixedNow))
		mustMark(t, n.MarkRetrying(fixedNow, "test", fixedNow))
	case domain.StatusCancelled:
		mustMark(t, n.Cancel(fixedNow))
	default:
		t.Fatalf("markIntoStatus: unsupported target %q", target)
	}
}

func mustMark(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("mark: %v", err)
	}
}

// TestBatch_StatusSummary populates a batch with one notification per status
// and asserts the derived counts.
func TestBatch_StatusSummary(t *testing.T) {
	corrID := "01HXYZSUMMARYCORR000000000000"
	statuses := []domain.Status{
		domain.StatusPending,
		domain.StatusQueued,
		domain.StatusProcessing,
		domain.StatusDelivered,
		domain.StatusFailed,
		domain.StatusRetrying,
		domain.StatusCancelled,
	}

	notifs := makeNotifications(t, len(statuses), corrID)
	for i, target := range statuses {
		markIntoStatus(t, notifs[i], target)
	}

	batch, err := domain.NewBatch(domain.NewBatchInput{
		ID:            "01HXYZSUMMARYBATCH00000000000",
		CorrelationID: corrID,
		Notifications: notifs,
	}, fixedNow)
	if err != nil {
		t.Fatalf("setup batch: %v", err)
	}

	summary := batch.StatusSummary()

	if summary.Total != len(statuses) {
		t.Errorf("Total = %d, want %d", summary.Total, len(statuses))
	}
	if summary.Pending != 1 {
		t.Errorf("Pending = %d, want 1", summary.Pending)
	}
	if summary.Queued != 1 {
		t.Errorf("Queued = %d, want 1", summary.Queued)
	}
	if summary.Processing != 1 {
		t.Errorf("Processing = %d, want 1", summary.Processing)
	}
	if summary.Delivered != 1 {
		t.Errorf("Delivered = %d, want 1", summary.Delivered)
	}
	if summary.Failed != 1 {
		t.Errorf("Failed = %d, want 1", summary.Failed)
	}
	if summary.Retrying != 1 {
		t.Errorf("Retrying = %d, want 1", summary.Retrying)
	}
	if summary.Cancelled != 1 {
		t.Errorf("Cancelled = %d, want 1", summary.Cancelled)
	}
}
