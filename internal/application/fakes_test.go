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
	"fmt"
	"sync"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// copyNotifications returns shallow copies of every notification in the
// slice so caller mutations do not leak back into the fake's internal
// store. Mirrors what a real repository would do when it scans rows out of
// the database into fresh structs.
func copyNotifications(in []*domain.Notification) []*domain.Notification {
	out := make([]*domain.Notification, len(in))
	for i, n := range in {
		if n == nil {
			continue
		}
		copied := *n
		out[i] = &copied
	}
	return out
}

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
	logs := make([]domain.LogID, 32)
	for i := range logs {
		logs[i] = domain.LogID(fmt.Sprintf("01LOG%02d", i+1))
	}
	return &fakeIDGenerator{
		notifications: []domain.NotificationID{"01NOTIF01", "01NOTIF02", "01NOTIF03", "01NOTIF04"},
		batches:       []domain.BatchID{"01BATCH01", "01BATCH02"},
		templates:     []domain.TemplateID{"01TMPL01"},
		logs:          logs,
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

	// List behavior — primed via SetListResult; calls accumulate in listCalls.
	listCalls  []listCallParams
	listResult []*domain.Notification
	nextCursor string

	// Reconciler queries — primed via Set*Reconciler.
	orphanedPending []*domain.Notification
	stuckProcessing []*domain.Notification
	overdueRetrying []*domain.Notification
	stuckQueued     []*domain.Notification

	// Reconciler query errors — set per test to drive the error
	// branches in ReconcileStuckNotifications.Execute. Each is
	// returned verbatim from the matching Find* fake method.
	orphanedPendingErr error
	stuckProcessingErr error
	overdueRetryingErr error
	stuckQueuedErr     error
}

// SetReconcilerResults primes the next reconciliation sweep's return values.
// Slices are positional — pass nil for any sweep the test does not care
// about. stuckQueued covers the dual-write race documented in
// CLAUDE.md §3.11 (notification ends up in queued with no asynq task
// waiting for it).
func (r *fakeNotificationRepo) SetReconcilerResults(orphanedPending, stuckProcessing, overdueRetrying, stuckQueued []*domain.Notification) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orphanedPending = orphanedPending
	r.stuckProcessing = stuckProcessing
	r.overdueRetrying = overdueRetrying
	r.stuckQueued = stuckQueued
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
	// Shallow-copy so subsequent mutations of n in the caller (e.g.
	// MarkQueued) do not retroactively change the value stored in the
	// fake — real PG would persist the row's data, not a live pointer.
	copied := *n
	r.store[n.ID] = &copied
	return nil
}

func (r *fakeNotificationRepo) Get(_ context.Context, id domain.NotificationID) (*domain.Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.store[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	// Shallow copy so use cases mutating the returned pointer (e.g. n.Cancel)
	// do not retroactively change the value stored in the fake — production
	// repositories return a freshly-scanned struct from the database, and
	// tests rely on that semantics for concurrency checks like UpdateStatus.
	copied := *n
	return &copied, nil
}

// The methods below are not used by CreateNotification; later use-case tests
// will exercise them. Returning errFakeNotImplemented makes accidental
// reliance loud.

func (r *fakeNotificationRepo) ClaimForProcessing(_ context.Context, id domain.NotificationID, now time.Time) (*domain.Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.store[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	// Only queued or retrying notifications can be claimed; everything else
	// (including processing — would be a redelivery race) is rejected with
	// ErrAlreadyClaimed.
	if n.Status != domain.StatusQueued && n.Status != domain.StatusRetrying {
		return nil, ports.ErrAlreadyClaimed
	}
	if err := n.MarkProcessing(now); err != nil {
		return nil, err
	}
	copied := *n
	return &copied, nil
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

// listCallParams captures every parameter set passed to List so tests can
// assert that the use case translated its input correctly.
type listCallParams struct {
	Filter ports.NotificationFilter
	Cursor string
	Limit  int
}

// SetListResult primes the next List call's return values.
func (r *fakeNotificationRepo) SetListResult(items []*domain.Notification, nextCursor string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listResult = items
	r.nextCursor = nextCursor
}

func (r *fakeNotificationRepo) List(_ context.Context, filter ports.NotificationFilter, cursor string, limit int) ([]*domain.Notification, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls = append(r.listCalls, listCallParams{Filter: filter, Cursor: cursor, Limit: limit})
	return r.listResult, r.nextCursor, nil
}

func (r *fakeNotificationRepo) FindOrphanedPending(_ context.Context, _ time.Time, _ int) ([]*domain.Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.orphanedPendingErr != nil {
		return nil, r.orphanedPendingErr
	}
	return copyNotifications(r.orphanedPending), nil
}

func (r *fakeNotificationRepo) FindStuckProcessing(_ context.Context, _ time.Time, _ int) ([]*domain.Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stuckProcessingErr != nil {
		return nil, r.stuckProcessingErr
	}
	return copyNotifications(r.stuckProcessing), nil
}

func (r *fakeNotificationRepo) FindOverdueRetrying(_ context.Context, _ time.Time, _ int) ([]*domain.Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.overdueRetryingErr != nil {
		return nil, r.overdueRetryingErr
	}
	return copyNotifications(r.overdueRetrying), nil
}

func (r *fakeNotificationRepo) FindStuckQueued(_ context.Context, _ time.Time, _ int) ([]*domain.Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stuckQueuedErr != nil {
		return nil, r.stuckQueuedErr
	}
	return copyNotifications(r.stuckQueued), nil
}

// --- BatchRepository -----------------------------------------------------

type fakeBatchRepo struct {
	mu        sync.Mutex
	store     map[domain.BatchID]*domain.Batch
	createErr error

	// notifRepo, when non-nil, is also populated on Create — production
	// BatchRepository.Create persists into the notifications table in
	// one transaction, so a subsequent UpdateStatus on a member sees
	// the row. Tests that exercise the CreateBatch → mark-queued path
	// link the two fakes via WithNotifSink.
	notifRepo *fakeNotificationRepo
}

func newFakeBatchRepo() *fakeBatchRepo {
	return &fakeBatchRepo{store: make(map[domain.BatchID]*domain.Batch)}
}

// WithNotifSink wires a notification repo into the batch repo so
// Create also seeds each member into the notification store. Mirrors
// the production semantics where one transaction writes both tables.
func (r *fakeBatchRepo) WithNotifSink(notifRepo *fakeNotificationRepo) *fakeBatchRepo {
	r.notifRepo = notifRepo
	return r
}

func (r *fakeBatchRepo) Create(_ context.Context, b *domain.Batch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.store[b.ID] = b
	if r.notifRepo != nil {
		for _, n := range b.Notifications {
			copied := *n
			r.notifRepo.mu.Lock()
			r.notifRepo.store[n.ID] = &copied
			r.notifRepo.mu.Unlock()
		}
	}
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
	mu        sync.Mutex
	entries   []*domain.NotificationLog
	appendErr error // optional injection — drives the recordEvent error branch
}

func newFakeNotificationLogRepo() *fakeNotificationLogRepo { return &fakeNotificationLogRepo{} }

func (r *fakeNotificationLogRepo) Append(_ context.Context, entry *domain.NotificationLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.appendErr != nil {
		return r.appendErr
	}
	r.entries = append(r.entries, entry)
	return nil
}

func (r *fakeNotificationLogRepo) List(_ context.Context, notificationID domain.NotificationID) ([]*domain.NotificationLog, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.NotificationLog
	for _, e := range r.entries {
		if e.NotificationID == notificationID {
			out = append(out, e)
		}
	}
	return out, nil
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

// --- TemplateRepository --------------------------------------------------

type fakeTemplateRepo struct {
	mu        sync.Mutex
	store     map[domain.TemplateID]*domain.Template
	createErr error // optional injection
	listErr   error // optional injection
	updateErr error // optional injection
}

func newFakeTemplateRepo() *fakeTemplateRepo {
	return &fakeTemplateRepo{store: make(map[domain.TemplateID]*domain.Template)}
}

func (r *fakeTemplateRepo) Create(_ context.Context, t *domain.Template) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.store[t.ID] = t
	return nil
}

func (r *fakeTemplateRepo) Get(_ context.Context, id domain.TemplateID) (*domain.Template, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.store[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	copied := *t
	return &copied, nil
}

func (r *fakeTemplateRepo) GetByName(_ context.Context, name string) (*domain.Template, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.store {
		if t.Name == name {
			copied := *t
			return &copied, nil
		}
	}
	return nil, ports.ErrNotFound
}

func (r *fakeTemplateRepo) List(_ context.Context, _ int) ([]*domain.Template, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := make([]*domain.Template, 0, len(r.store))
	for _, t := range r.store {
		copied := *t
		out = append(out, &copied)
	}
	return out, nil
}

func (r *fakeTemplateRepo) Update(_ context.Context, t *domain.Template) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return r.updateErr
	}
	if _, ok := r.store[t.ID]; !ok {
		return ports.ErrNotFound
	}
	r.store[t.ID] = t
	return nil
}

func (r *fakeTemplateRepo) Delete(_ context.Context, id domain.TemplateID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.store[id]; !ok {
		return ports.ErrNotFound
	}
	delete(r.store, id)
	return nil
}

// --- Provider -------------------------------------------------------------

type providerCall struct {
	Channel   domain.Channel
	Recipient string
	Content   string
}

type fakeProvider struct {
	mu     sync.Mutex
	calls  []providerCall
	result domain.DeliveryResult
}

func newFakeProvider(result domain.DeliveryResult) *fakeProvider {
	return &fakeProvider{result: result}
}

func (p *fakeProvider) Send(_ context.Context, channel domain.Channel, recipient, content string) domain.DeliveryResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, providerCall{Channel: channel, Recipient: recipient, Content: content})
	return p.result
}

// --- RateLimiter ----------------------------------------------------------

type fakeRateLimiter struct {
	mu         sync.Mutex
	buckets    []string
	allowed    bool
	retryAfter time.Duration
}

func newFakeRateLimiter(allowed bool) *fakeRateLimiter {
	return &fakeRateLimiter{allowed: allowed}
}

func (l *fakeRateLimiter) Allow(_ context.Context, bucket string) (bool, time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buckets = append(l.buckets, bucket)
	return l.allowed, l.retryAfter, nil
}

// --- StatusBroadcaster ----------------------------------------------------

type broadcastEntry struct {
	NotificationID domain.NotificationID
	Status         domain.Status
}

type fakeStatusBroadcaster struct {
	mu       sync.Mutex
	messages []broadcastEntry
}

func newFakeStatusBroadcaster() *fakeStatusBroadcaster {
	return &fakeStatusBroadcaster{}
}

func (b *fakeStatusBroadcaster) Publish(_ context.Context, id domain.NotificationID, status domain.Status) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.messages = append(b.messages, broadcastEntry{NotificationID: id, Status: status})
	return nil
}
