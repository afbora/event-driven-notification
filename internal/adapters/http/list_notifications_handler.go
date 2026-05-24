package http

import (
	"context"
	"fmt"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// ListNotifications overrides the embedded stub and dispatches to the
// wired executor. It is the operation behind `GET /api/v1/notifications`.
//
// The use case owns:
//
//   - String parsing of status and channel (an invalid value surfaces
//     domain.ErrInvalidStatus / ErrInvalidChannel which the translator
//     turns into a 400).
//   - Limit clamping (defaults and ceilings are policy, not transport).
//
// The handler only ferries query params into ListNotificationsInput and
// the resulting page out into the wire-level NotificationPage.
func (s *Server) ListNotifications(ctx context.Context, req api.ListNotificationsRequestObject) (api.ListNotificationsResponseObject, error) {
	if s.listNotifications == nil {
		return nil, ErrNotImplemented
	}

	in := fromAPIListNotificationsParams(req.Params)
	out, err := s.listNotifications(ctx, in)
	if err != nil {
		return nil, err
	}

	page, err := toAPINotificationPage(out)
	if err != nil {
		return nil, fmt.Errorf("map notification page: %w", err)
	}
	return api.ListNotifications200JSONResponse{
		Body: page,
	}, nil
}

// fromAPIListNotificationsParams maps the parsed query parameters into
// the use case input. Optional fields stay zero-valued when the caller
// omitted them; the use case treats those as "no filter".
func fromAPIListNotificationsParams(p api.ListNotificationsParams) application.ListNotificationsInput {
	in := application.ListNotificationsInput{}
	if p.Status != nil {
		in.Status = string(*p.Status)
	}
	if p.Channel != nil {
		in.Channel = string(*p.Channel)
	}
	if p.BatchId != nil {
		bid := domain.BatchID(p.BatchId.String())
		in.BatchID = &bid
	}
	if p.CreatedAfter != nil {
		t := *p.CreatedAfter
		in.CreatedAfter = &t
	}
	if p.CreatedBefore != nil {
		t := *p.CreatedBefore
		in.CreatedBefore = &t
	}
	if p.Cursor != nil {
		in.Cursor = *p.Cursor
	}
	if p.Limit != nil {
		in.Limit = *p.Limit
	}
	return in
}

// toAPINotificationPage converts a use case output page into its wire
// shape. NextCursor is emitted only when the use case returned a
// non-empty value — clients use the absence as the "no more pages"
// signal.
func toAPINotificationPage(out application.ListNotificationsOutput) (api.NotificationPage, error) {
	items := make([]api.Notification, 0, len(out.Notifications))
	for _, n := range out.Notifications {
		item, err := toAPINotification(n)
		if err != nil {
			return api.NotificationPage{}, fmt.Errorf("map notification: %w", err)
		}
		items = append(items, item)
	}

	page := api.NotificationPage{Items: items}
	if out.NextCursor != "" {
		nc := out.NextCursor
		page.NextCursor = &nc
	}
	return page, nil
}
