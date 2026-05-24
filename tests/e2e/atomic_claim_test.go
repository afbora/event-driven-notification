//go:build e2e

package e2e_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	pgadapter "github.com/afbora/event-driven-notification/internal/adapters/postgres"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// TestAtomicClaim_ConcurrentRace_ExactlyOneWinner: spawning N
// goroutines that race to claim the same notification must end with
// exactly one win and N-1 ErrAlreadyClaimed returns. This is the
// load-bearing contract from CLAUDE.md §3.10 / ADR-0009 — without
// it, a redelivery from asynq or a horizontally-scaled worker pool
// would double-send.
func TestAtomicClaim_ConcurrentRace_ExactlyOneWinner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	repo := pgadapter.NewNotificationRepository(h.Pool)
	now := time.Now().UTC()

	// Seed a notification directly in `queued` state. We bypass the
	// HTTP path so the harness's worker never sees the row — the
	// only contenders for the claim are the goroutines in this test.
	id := domain.NotificationID("019e5b00-0000-7000-8000-00000000c111")
	seedQueuedNotification(ctx, t, h, id, now)

	const racers = 16
	var wins, losses atomic.Int32
	var unexpected atomic.Int32
	var firstWinner atomic.Pointer[domain.Notification]

	var wg sync.WaitGroup
	wg.Add(racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			<-start // pack the racers tightly to maximize contention
			n, err := repo.ClaimForProcessing(ctx, id, now)
			switch {
			case err == nil:
				wins.Add(1)
				firstWinner.CompareAndSwap(nil, n)
			case errors.Is(err, ports.ErrAlreadyClaimed):
				losses.Add(1)
			default:
				unexpected.Add(1)
				t.Logf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	require.Zero(t, unexpected.Load(),
		"every racer must return either nil or ErrAlreadyClaimed; unexpected count=%d", unexpected.Load())
	require.Equal(t, int32(1), wins.Load(),
		"exactly one goroutine must claim the notification; got %d wins / %d losses",
		wins.Load(), losses.Load())
	require.Equal(t, int32(racers-1), losses.Load(),
		"every non-winner must see ErrAlreadyClaimed; got %d losses", losses.Load())

	winner := firstWinner.Load()
	require.NotNil(t, winner)
	require.Equal(t, domain.StatusProcessing, winner.Status,
		"the winner's notification must come back in processing state")
}

// seedQueuedNotification inserts a notification row directly in
// `queued` state via the production repository. Using the repo's
// Create followed by an UpdateStatus mirrors what CreateNotification
// does end-to-end, but without involving asynq — so the harness's
// worker never touches the row.
func seedQueuedNotification(ctx context.Context, t *testing.T, h *Harness, id domain.NotificationID, now time.Time) {
	t.Helper()
	repo := pgadapter.NewNotificationRepository(h.Pool)

	n, err := domain.NewNotification(domain.NewNotificationInput{
		ID:            id,
		CorrelationID: "01HXYZATOMICCLAIM00000001",
		Channel:       domain.ChannelSMS,
		Priority:      domain.PriorityNormal,
		Recipient:     "+15555550090",
		Content:       "atomic-claim-test",
	}, now)
	require.NoError(t, err)

	require.NoError(t, repo.Create(ctx, n))

	// Advance pending → queued so ClaimForProcessing accepts the row.
	require.NoError(t, n.MarkQueued(now))
	require.NoError(t, repo.UpdateStatus(ctx, n, domain.StatusPending))
}
