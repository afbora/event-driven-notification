package domain

import "time"

// BatchID is the unique identifier for a batch — a group of 1 to 1000
// notifications created in a single API call. The string form is the UUID v7
// representation, parallel to NotificationID. Domain doesn't generate IDs;
// they arrive from a ports.IDGenerator adapter (CLAUDE.md §3.3).
type BatchID string

// Batch size limits per the brief: "Support batch creation (up to 1000
// notifications per request)". One-shot creation requires at least one
// notification, hence the minimum of 1.
const (
	MinBatchSize = 1
	MaxBatchSize = 1000
)

// Sentinel errors used by this file (ErrInvalidBatchID, ErrInvalidBatchSize,
// ErrBatchInconsistentCorrelation) are declared in errors.go.

// Batch is a group of notifications created together. Every notification in
// a batch shares the batch's correlation ID (CLAUDE.md §2.3) so a single
// business action is traceable end-to-end through the system. The batch
// itself has no Status field — callers derive aggregate status via
// StatusSummary, which is what the batch-status API endpoint returns.
type Batch struct {
	ID            BatchID
	CorrelationID string
	Notifications []*Notification
	CreatedAt     time.Time
}

// NewBatchInput is the parameter bundle for NewBatch.
type NewBatchInput struct {
	ID            BatchID
	CorrelationID string
	Notifications []*Notification
}

// NewBatch constructs a fully validated Batch. Validation:
//
//   - ID and CorrelationID are non-empty.
//   - Size is within [MinBatchSize, MaxBatchSize].
//   - Every notification's CorrelationID matches the batch's CorrelationID.
//
// On success NewBatch also sets every notification's BatchID to the batch's
// own ID, so callers do not need to remember to wire that link.
func NewBatch(in NewBatchInput, now time.Time) (*Batch, error) {
	if in.ID == "" {
		return nil, ErrInvalidBatchID
	}
	if in.CorrelationID == "" {
		return nil, ErrInvalidCorrelationID
	}
	if len(in.Notifications) < MinBatchSize || len(in.Notifications) > MaxBatchSize {
		return nil, ErrInvalidBatchSize
	}
	for _, n := range in.Notifications {
		if n.CorrelationID != in.CorrelationID {
			return nil, ErrBatchInconsistentCorrelation
		}
	}

	// All checks passed — wire each notification to this batch.
	for _, n := range in.Notifications {
		bid := in.ID
		n.BatchID = &bid
	}

	return &Batch{
		ID:            in.ID,
		CorrelationID: in.CorrelationID,
		Notifications: in.Notifications,
		CreatedAt:     now,
	}, nil
}

// Size returns the number of notifications in the batch.
func (b *Batch) Size() int {
	return len(b.Notifications)
}

// BatchStatusSummary aggregates the statuses of every notification in the
// batch. Returned by Batch.StatusSummary; consumed by the batch-status API
// endpoint (PLAN.md task 4-19) so callers can see at-a-glance progress.
type BatchStatusSummary struct {
	Total      int
	Pending    int
	Queued     int
	Processing int
	Delivered  int
	Failed     int
	Retrying   int
	Cancelled  int
}

// StatusSummary computes the per-status counts across the batch. The sum of
// every individual field equals Total — callers can use Delivered+Failed+
// Cancelled to check terminal progress, or Total-(those three) for in-flight.
func (b *Batch) StatusSummary() BatchStatusSummary {
	s := BatchStatusSummary{Total: len(b.Notifications)}
	for _, n := range b.Notifications {
		switch n.Status {
		case StatusPending:
			s.Pending++
		case StatusQueued:
			s.Queued++
		case StatusProcessing:
			s.Processing++
		case StatusDelivered:
			s.Delivered++
		case StatusFailed:
			s.Failed++
		case StatusRetrying:
			s.Retrying++
		case StatusCancelled:
			s.Cancelled++
		}
	}
	return s
}
