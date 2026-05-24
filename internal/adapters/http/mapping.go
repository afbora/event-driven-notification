package http

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// defaultMaxAttempts is what the mapping emits when the domain has not
// yet recorded a per-notification max. The retry logic in the worker
// uses 5 as the default; phase 5/6 may push the value down into the
// domain entity, at which point this constant goes away.
const defaultMaxAttempts = 5

// toAPINotification converts a domain.Notification into the wire-level
// shape generated from openapi.yaml. The two structs are kept apart on
// purpose — domain fields use richer Go types (pointers,
// channel/status/priority enums); the api struct uses what the spec
// declared, with optional fields as pointers and UUIDs in canonical
// string form.
func toAPINotification(n *domain.Notification) (api.Notification, error) {
	id, err := uuid.Parse(string(n.ID))
	if err != nil {
		return api.Notification{}, fmt.Errorf("notification id is not a uuid: %w", err)
	}

	out := api.Notification{
		Id:          id,
		Channel:     api.Channel(n.Channel),
		Priority:    api.Priority(n.Priority),
		Status:      api.Status(n.Status),
		Recipient:   n.Recipient,
		Content:     n.Content,
		Attempts:    n.Attempts,
		MaxAttempts: defaultMaxAttempts,
		CreatedAt:   n.CreatedAt,
		UpdatedAt:   n.UpdatedAt,
	}

	if n.CorrelationID != "" {
		cid := n.CorrelationID
		out.CorrelationId = &cid
	}
	if n.BatchID != nil {
		bid, perr := uuid.Parse(string(*n.BatchID))
		if perr != nil {
			return api.Notification{}, fmt.Errorf("batch id is not a uuid: %w", perr)
		}
		out.BatchId = &bid
	}
	if n.TemplateID != nil {
		tid, perr := uuid.Parse(*n.TemplateID)
		if perr != nil {
			return api.Notification{}, fmt.Errorf("template id is not a uuid: %w", perr)
		}
		out.TemplateId = &tid
	}
	if n.IdempotencyKey != "" {
		k := n.IdempotencyKey
		out.IdempotencyKey = &k
	}
	if n.ScheduledAt != nil {
		t := *n.ScheduledAt
		out.ScheduledAt = &t
	}
	if n.NextRetryAt != nil {
		t := *n.NextRetryAt
		out.NextRetryAt = &t
	}
	if n.LastError != "" {
		reason := n.LastError
		out.FailureReason = &reason
	}

	return out, nil
}

// toAPIBatch converts a domain.Batch into the wire-level shape. The
// `withNotifications` switch controls whether the member notifications
// are inlined — false for POST 202 responses (the spec omits them to
// keep the payload small), true for GET responses.
func toAPIBatch(b *domain.Batch, withNotifications bool) (api.Batch, error) {
	id, err := uuid.Parse(string(b.ID))
	if err != nil {
		return api.Batch{}, fmt.Errorf("batch id is not a uuid: %w", err)
	}

	out := api.Batch{
		Id:        id,
		Size:      len(b.Notifications),
		CreatedAt: b.CreatedAt,
	}
	if b.CorrelationID != "" {
		cid := b.CorrelationID
		out.CorrelationId = &cid
	}

	if withNotifications && len(b.Notifications) > 0 {
		items := make([]api.Notification, 0, len(b.Notifications))
		for _, n := range b.Notifications {
			item, ierr := toAPINotification(n)
			if ierr != nil {
				return api.Batch{}, fmt.Errorf("map batch member: %w", ierr)
			}
			items = append(items, item)
		}
		out.Notifications = &items
	}

	return out, nil
}

// fromAPICreateBatchRequest maps the wire-level batch request into the
// CreateBatch use case input. Each api.CreateNotificationRequest in
// the array becomes a CreateBatchItem; the optional X-Correlation-ID
// header drives the shared correlation id (the use case generates one
// when absent).
func fromAPICreateBatchRequest(body *api.CreateBatchRequest, params api.CreateBatchParams) application.CreateBatchInput {
	in := application.CreateBatchInput{
		Notifications: make([]application.CreateBatchItem, 0, len(body.Notifications)),
	}
	if params.XCorrelationID != nil {
		in.CorrelationID = *params.XCorrelationID
	}

	for _, item := range body.Notifications {
		batchItem := application.CreateBatchItem{
			Channel:   string(item.Channel),
			Recipient: item.Recipient,
			Content:   item.Content,
		}
		if item.Priority != nil {
			batchItem.Priority = string(*item.Priority)
		} else {
			batchItem.Priority = string(domain.PriorityNormal)
		}
		if item.TemplateId != nil {
			s := item.TemplateId.String()
			batchItem.TemplateID = &s
		}
		in.Notifications = append(in.Notifications, batchItem)
	}

	return in
}

// fromAPICreateNotificationRequest maps the wire-level request struct
// into the application use case's input shape. The use case is the
// authoritative layer for parsing channel/priority strings — this
// function only ferries values, no domain validation here.
func fromAPICreateNotificationRequest(body *api.CreateNotificationRequest, params api.CreateNotificationParams) application.CreateNotificationInput {
	in := application.CreateNotificationInput{
		Channel:   string(body.Channel),
		Recipient: body.Recipient,
		Content:   body.Content,
	}

	if body.Priority != nil {
		in.Priority = string(*body.Priority)
	} else {
		in.Priority = string(domain.PriorityNormal)
	}

	if body.ScheduledAt != nil {
		t := *body.ScheduledAt
		in.ScheduledAt = &t
	}
	if body.TemplateId != nil {
		s := body.TemplateId.String()
		in.TemplateID = &s
	}

	if params.IdempotencyKey != nil {
		in.IdempotencyKey = *params.IdempotencyKey
	}
	if params.XCorrelationID != nil {
		in.CorrelationID = *params.XCorrelationID
	}

	return in
}
