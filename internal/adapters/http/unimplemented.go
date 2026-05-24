package http

import (
	"context"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
)

// unimplementedServer satisfies api.StrictServerInterface with stubs
// that all return ErrNotImplemented. Embed in Server so each operation
// only needs an override once its handler ships — partial wiring is
// the test seam every phase 4 task uses.
//
// The strict-server error handler (RespondWithError) detects
// ErrNotImplemented via errors.Is and emits a 501 problem response.
type unimplementedServer struct{}

func (unimplementedServer) GetJSONMetrics(_ context.Context, _ api.GetJSONMetricsRequestObject) (api.GetJSONMetricsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) ListNotifications(_ context.Context, _ api.ListNotificationsRequestObject) (api.ListNotificationsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) CreateNotification(_ context.Context, _ api.CreateNotificationRequestObject) (api.CreateNotificationResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) CreateBatch(_ context.Context, _ api.CreateBatchRequestObject) (api.CreateBatchResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) GetBatch(_ context.Context, _ api.GetBatchRequestObject) (api.GetBatchResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) GetNotification(_ context.Context, _ api.GetNotificationRequestObject) (api.GetNotificationResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) CancelNotification(_ context.Context, _ api.CancelNotificationRequestObject) (api.CancelNotificationResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) GetNotificationTrace(_ context.Context, _ api.GetNotificationTraceRequestObject) (api.GetNotificationTraceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) ListTemplates(_ context.Context, _ api.ListTemplatesRequestObject) (api.ListTemplatesResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) CreateTemplate(_ context.Context, _ api.CreateTemplateRequestObject) (api.CreateTemplateResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) DeleteTemplate(_ context.Context, _ api.DeleteTemplateRequestObject) (api.DeleteTemplateResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) GetTemplate(_ context.Context, _ api.GetTemplateRequestObject) (api.GetTemplateResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) ReplaceTemplate(_ context.Context, _ api.ReplaceTemplateRequestObject) (api.ReplaceTemplateResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) GetHealthzLive(_ context.Context, _ api.GetHealthzLiveRequestObject) (api.GetHealthzLiveResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) GetHealthzReady(_ context.Context, _ api.GetHealthzReadyRequestObject) (api.GetHealthzReadyResponseObject, error) {
	return nil, ErrNotImplemented
}

func (unimplementedServer) GetPrometheusMetrics(_ context.Context, _ api.GetPrometheusMetricsRequestObject) (api.GetPrometheusMetricsResponseObject, error) {
	return nil, ErrNotImplemented
}
