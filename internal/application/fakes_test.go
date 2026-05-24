package application_test

// Hand-written fakes that satisfy the port interfaces from internal/ports.
// Use cases under test inject these in place of real adapters; assertions
// poke at the public fields (store, entries, items) directly.
//
// CLAUDE.md §6 favors hand-written fakes over generated mocks: small, clear,
// and immune to reflection surprises. These live in fakes_test.go so every
// use-case test file in the application_test package can share them.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

var errFakeNotImplemented = errors.New("fake: method not used by this test")

// --- ID generator ---------------------------------------------------------

// fakeIDGenerator returns deterministic IDs from pre-seeded slices. Tests
// can construct one with custom IDs or use newDefaultFakeIDs() for the
// common case.
type fakeIDGenerator struct {
	notifications []domain.NotificationID
	batches       []domain.BatchID
	templates     []domain.TemplateID
	logs          []domain.LogID
	correlations  []string

	nIdx, bIdx, tIdx, lIdx, cIdx int
}

func newDefaultFakeIDs() *fakeIDGenerator {
	return &fakeIDGenerator{
		notifications: []domain.NotificationID{"01NOTIF01", "01NOTIF02", "01NOTIF03", "01NOTIF04"},
		batches:       []domain.BatchID{"01BATCH01", "01BATCH02"},
		templates:     []domain.TemplateID{"01TMPL01"},
		logs:          []domain.LogID{"01LOG01", "01LOG02", "01LOG03", "01LOG04"},
		correlations:  []string{"01CORR01", "01CORR02"},
	}
}

func (g *fakeIDGenerator) NewNotificationID() domain.NotificationID {
	id := g.notifications[g.nIdx]
	g.nIdx++
	return id
}

func (g *fakeIDGenerator) NewBatchID() domain.BatchID {
	id := g.batches[g.bIdx]
	g.bIdx++
	return id
}

func (g *fakeIDGenerator) NewTemplateID() domain.TemplateID {
	id := g.templates[g.tIdx]
	g.tIdx++
	return id
}

func (g *fakeIDGenerator) NewLogID() domain.LogID {
	id := g.logs[g.lIdx]
	g.lIdx++
	return id
}

func (g *fakeIDGenerator) NewCorrelationID() string {
	id := g.correlations[g.cIdx]
	g.cIdx++
	return id
}

// --- Clock ----------------------------------------------------------------

type fakeClock struct {
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }
func (c *fakeClock) Now() time.Time       { return c.now }

// --- NotificationRepository ----------------------------------------------

type fakeNotificationRepo struct {
	mu        sync.Mutex
	store     map[domain.NotificationID]*domain.Notification
	createErr error // optional injection
}

func newFakeNotificationRepo() *fakeNotificationRepo {
	return &fakeNotificationRepo{store: make(map[domain.NotificationID]*domain.Notification)}
}

func (r *fakeNotificationRepo) Create(_ context.Context, n *domain.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.store[n.ID] = n
	return nil
}

func (r *fakeNotificationRepo) Get(_ context.Context, id domain.NotificationID) (*domain.Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.store[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return n, nil
}

// The methods below are not used by CreateNotification; later use-case tests
// will exercise them. Returning errFakeNotImplemented makes accidental
// reliance loud.

func (r *fakeNotificationRepo) ClaimForProcessing(_ context.Context, _ domain.NotificationID, _ time.Time) (*domain.Notification, error) {
	return nil, errFakeNotImplemented
}

func (r *fakeNotificationRepo) UpdateStatus(_ context.Context, n *domain.Notification, expectedSource domain.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.store[n.ID]
	if !ok {
		return ports.ErrNotFound
	}
	if existing.Status != expectedSource {
		return ports.ErrConcurrentUpdate
	}
	r.store[n.ID] = n
	return nil
}

func (r *fakeNotificationRepo) List(_ context.Context, _ ports.NotificationFilter, _ string, _ int) ([]*domain.Notification, string, error) {
	return nil, "", errFakeNotImplemented
}

func (r *fakeNotificationRepo) FindOrphanedPending(_ context.Context, _ time.Time, _ int) ([]*domain.Notification, error) {
	return nil, errFakeNotImplemented
}

func (r *fakeNotificationRepo) FindStuckProcessing(_ context.Context, _ time.Time, _ int) ([]*domain.Notification, error) {
	return nil, errFakeNotImplemented
}

func (r *fakeNotificationRepo) FindOverdueRetrying(_ context.Context, _ time.Time, _ int) ([]*domain.Notification, error) {
	return nil, errFakeNotImplemented
}

// --- BatchRepository -----------------------------------------------------

type fakeBatchRepo struct {
	mu        sync.Mutex
	store     map[domain.BatchID]*domain.Batch
	createErr error
}

func newFakeBatchRepo() *fakeBatchRepo {
	return &fakeBatchRepo{store: make(map[domain.BatchID]*domain.Batch)}
}

func (r *fakeBatchRepo) Create(_ context.Context, b *domain.Batch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.store[b.ID] = b
	return nil
}

func (r *fakeBatchRepo) Get(_ context.Context, id domain.BatchID) (*domain.Batch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.store[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return b, nil
}

// --- NotificationLogRepository -------------------------------------------

type fakeNotificationLogRepo struct {
	mu      sync.Mutex
	entries []*domain.NotificationLog
}

func newFakeNotificationLogRepo() *fakeNotificationLogRepo { return &fakeNotificationLogRepo{} }

func (r *fakeNotificationLogRepo) Append(_ context.Context, entry *domain.NotificationLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry)
	return nil
}

func (r *fakeNotificationLogRepo) List(_ context.Context, _ domain.NotificationID) ([]*domain.NotificationLog, error) {
	return nil, errFakeNotImplemented
}

// --- Queue ----------------------------------------------------------------

// enqueuedItem captures the parameters of one Enqueue / EnqueueScheduled call
// so assertions can verify shape without mocking the asynq client.
type enqueuedItem struct {
	NotificationID domain.NotificationID
	Priority       domain.Priority
	IdempotencyKey string
	ScheduledAt    *time.Time
}

type fakeQueue struct {
	mu        sync.Mutex
	items     []enqueuedItem
	cancelled []domain.NotificationID
	enqErr    error // optional injection
}

func newFakeQueue() *fakeQueue { return &fakeQueue{} }

func (q *fakeQueue) Enqueue(_ context.Context, id domain.NotificationID, p domain.Priority, idem string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.enqErr != nil {
		return q.enqErr
	}
	q.items = append(q.items, enqueuedItem{NotificationID: id, Priority: p, IdempotencyKey: idem})
	return nil
}

func (q *fakeQueue) EnqueueScheduled(_ context.Context, id domain.NotificationID, p domain.Priority, at time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.enqErr != nil {
		return q.enqErr
	}
	scheduledAt := at
	q.items = append(q.items, enqueuedItem{NotificationID: id, Priority: p, ScheduledAt: &scheduledAt})
	return nil
}

func (q *fakeQueue) Cancel(_ context.Context, id domain.NotificationID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancelled = append(q.cancelled, id)
	return nil
}
