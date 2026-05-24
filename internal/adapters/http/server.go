package http

import (
	"context"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// CreateNotificationExecutor is the slim function-type contract the
// CreateNotification handler depends on. Mirrors the pattern used by
// the asynq processor: the production wiring (cmd/api) passes
// (*application.CreateNotification).Execute, tests pass a closure that
// records inputs and returns a controlled outcome.
type CreateNotificationExecutor func(ctx context.Context, in application.CreateNotificationInput) (*domain.Notification, error)

// CreateBatchExecutor is the slim contract for POST
// /api/v1/notifications/batch. Production wires
// (*application.CreateBatch).Execute; tests pass a closure.
type CreateBatchExecutor func(ctx context.Context, in application.CreateBatchInput) (*domain.Batch, error)

// ServerOptions bundles the per-operation executors the Server needs.
// Each operation has its own slot so partial wiring is legal — an
// operation without an executor falls through to the embedded
// unimplementedServer and returns 501. Tasks 9-22 in phase 4 add the
// remaining executors as their handlers ship.
type ServerOptions struct {
	CreateNotification CreateNotificationExecutor
	CreateBatch        CreateBatchExecutor
}

// Server is the adapter that implements api.StrictServerInterface by
// dispatching each operation to its application use case. The
// embedded unimplementedServer satisfies every method of the
// generated interface so Server compiles even when only one operation
// is overridden — the test seam each phase 4 task uses.
//
// Concurrency: Server holds only constructor-injected references; it
// is safe to share across goroutines once NewServer returns.
type Server struct {
	unimplementedServer

	createNotification CreateNotificationExecutor
	createBatch        CreateBatchExecutor
}

// NewServer wires the executors carried by opts into a Server. The
// embedded unimplementedServer is zero-value so any operation whose
// executor is nil falls through to a 501 response.
func NewServer(opts ServerOptions) *Server {
	return &Server{
		createNotification: opts.CreateNotification,
		createBatch:        opts.CreateBatch,
	}
}
