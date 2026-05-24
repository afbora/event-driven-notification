package http

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// GetNotificationTrace overrides the embedded stub and dispatches to
// the wired executor. It is the operation behind `GET
// /api/v1/notifications/{id}/trace`.
//
// The use case verifies the notification exists (so an unknown id
// produces 404 instead of an empty 200) — no special handling here.
// The handler maps each domain.NotificationLog into its wire shape
// and packages them under the notification_id top-level field.
func (s *Server) GetNotificationTrace(ctx context.Context, req api.GetNotificationTraceRequestObject) (api.GetNotificationTraceResponseObject, error) {
	if s.getNotificationTrace == nil {
		return nil, ErrNotImplemented
	}

	entries, err := s.getNotificationTrace(ctx, application.GetNotificationTraceInput{
		NotificationID: domain.NotificationID(req.Id.String()),
	})
	if err != nil {
		return nil, err
	}

	out, err := toAPINotificationTrace(domain.NotificationID(req.Id.String()), entries)
	if err != nil {
		return nil, fmt.Errorf("map notification trace: %w", err)
	}
	return api.GetNotificationTrace200JSONResponse{
		Body: out,
	}, nil
}

// toAPINotificationTrace bundles the notification id and the ordered
// log entries into the wire shape. Per the spec, an empty Entries
// slice is preferred over a null when there are no logs — clients
// iterate without nil checks.
func toAPINotificationTrace(notifID domain.NotificationID, entries []*domain.NotificationLog) (api.NotificationTrace, error) {
	id, err := uuid.Parse(string(notifID))
	if err != nil {
		return api.NotificationTrace{}, fmt.Errorf("notification id is not a uuid: %w", err)
	}

	items := make([]api.NotificationTraceEntry, 0, len(entries))
	for _, log := range entries {
		entry, ierr := toAPINotificationTraceEntry(log)
		if ierr != nil {
			return api.NotificationTrace{}, fmt.Errorf("map trace entry: %w", ierr)
		}
		items = append(items, entry)
	}

	return api.NotificationTrace{
		NotificationId: id,
		Entries:        items,
	}, nil
}

// toAPINotificationTraceEntry converts a single domain.NotificationLog
// into its wire shape. Nil / empty Details survive as an omitted JSON
// field (clients distinguish "no details recorded" from "details was
// an empty object").
func toAPINotificationTraceEntry(log *domain.NotificationLog) (api.NotificationTraceEntry, error) {
	id, err := uuid.Parse(string(log.ID))
	if err != nil {
		return api.NotificationTraceEntry{}, fmt.Errorf("log id is not a uuid: %w", err)
	}

	out := api.NotificationTraceEntry{
		Id:        id,
		Event:     api.LogEvent(log.Event),
		CreatedAt: log.CreatedAt,
	}
	if log.CorrelationID != "" {
		cid := log.CorrelationID
		out.CorrelationId = &cid
	}
	if len(log.Details) > 0 {
		details := map[string]interface{}(log.Details)
		out.Details = &details
	}
	return out, nil
}
