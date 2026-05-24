package asynq

import (
	"context"
	"encoding/json"
	"fmt"

	hibikenasynq "github.com/hibiken/asynq"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// ProcessNotificationExecutor is the slim contract the processor depends on
// — anything that can run the ProcessNotification use case. Defined as a
// function type so tests can pass a closure without standing up the full
// application.ProcessNotification + every port it consumes.
type ProcessNotificationExecutor func(ctx context.Context, in application.ProcessNotificationInput) error

// Processor is the consumer-side glue between asynq tasks and the
// ProcessNotification use case. cmd/worker wires one Processor into an
// asynq.Server and calls Register on the server's ServeMux.
type Processor struct {
	process ProcessNotificationExecutor
}

// NewProcessor wires the executor (typically *application.ProcessNotification.Execute)
// into a Processor.
func NewProcessor(executor ProcessNotificationExecutor) *Processor {
	return &Processor{process: executor}
}

// Register attaches every supported task type to the supplied ServeMux.
// Adding a new task type means registering its handler here, never
// switch-on-type inside an existing handler (CLAUDE.md anti-patterns).
func (p *Processor) Register(mux *hibikenasynq.ServeMux) {
	mux.HandleFunc(TypeProcessNotification, p.HandleProcessNotification)
}

// HandleProcessNotification is the asynq HandlerFunc for the
// TypeProcessNotification task. It decodes the payload, invokes the use
// case, and returns its error verbatim so asynq's retry policy can react
// (a non-nil error from the use case triggers asynq to re-schedule with
// exponential backoff, up to MaxRetry attempts).
func (p *Processor) HandleProcessNotification(ctx context.Context, t *hibikenasynq.Task) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "queue.dequeue")
	defer span.End()

	var payload ProcessNotificationPayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("unmarshal process notification payload: %w", err)
	}
	if payload.NotificationID == "" {
		err := fmt.Errorf("process notification: empty notification id in payload")
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	span.SetAttributes(attributeNotificationID(domain.NotificationID(payload.NotificationID)))

	if err := p.process(ctx, application.ProcessNotificationInput{
		NotificationID: domain.NotificationID(payload.NotificationID),
	}); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}
