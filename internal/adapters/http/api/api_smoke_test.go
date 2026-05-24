package api_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
)

// TestGeneratedSpec_LoadsAndDescribesEveryEndpoint: the embedded
// openapi spec is shipped with the binary (oapi-codegen.yaml has
// `embedded-spec: true`) so the API can serve Swagger UI at /docs
// without reading from disk. The smoke check confirms the embed
// captured every path we declared — if a path is removed from
// openapi.yaml, this test fails so the deletion is intentional.
func TestGeneratedSpec_LoadsAndDescribesEveryEndpoint(t *testing.T) {
	spec, err := api.GetSwagger()
	require.NoError(t, err)
	require.NotNil(t, spec)

	want := []string{
		"/api/v1/notifications",
		"/api/v1/notifications/{id}",
		"/api/v1/notifications/{id}/cancel",
		"/api/v1/notifications/{id}/trace",
		"/api/v1/notifications/batch",
		"/api/v1/notifications/batch/{id}",
		"/api/v1/templates",
		"/api/v1/templates/{id}",
		"/api/v1/metrics",
		"/healthz/live",
		"/healthz/ready",
		"/metrics",
	}
	for _, path := range want {
		require.NotNil(t, spec.Paths.Find(path),
			"openapi.yaml must declare path %q", path)
	}
}

// stubStrictServer fully implements api.StrictServerInterface by
// returning nil for every operation. Compile-time check: if
// oapi-codegen ever drops or renames an operation method, this
// implementation stops satisfying the interface and the test fails
// to build — exactly the canary we want when the spec changes.
type stubStrictServer struct{}

func (stubStrictServer) GetJSONMetrics(_ context.Context, _ api.GetJSONMetricsRequestObject) (api.GetJSONMetricsResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) ListNotifications(_ context.Context, _ api.ListNotificationsRequestObject) (api.ListNotificationsResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) CreateNotification(_ context.Context, _ api.CreateNotificationRequestObject) (api.CreateNotificationResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) CreateBatch(_ context.Context, _ api.CreateBatchRequestObject) (api.CreateBatchResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) GetBatch(_ context.Context, _ api.GetBatchRequestObject) (api.GetBatchResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) GetNotification(_ context.Context, _ api.GetNotificationRequestObject) (api.GetNotificationResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) CancelNotification(_ context.Context, _ api.CancelNotificationRequestObject) (api.CancelNotificationResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) GetNotificationTrace(_ context.Context, _ api.GetNotificationTraceRequestObject) (api.GetNotificationTraceResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) ListTemplates(_ context.Context, _ api.ListTemplatesRequestObject) (api.ListTemplatesResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) CreateTemplate(_ context.Context, _ api.CreateTemplateRequestObject) (api.CreateTemplateResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) DeleteTemplate(_ context.Context, _ api.DeleteTemplateRequestObject) (api.DeleteTemplateResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) GetTemplate(_ context.Context, _ api.GetTemplateRequestObject) (api.GetTemplateResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) ReplaceTemplate(_ context.Context, _ api.ReplaceTemplateRequestObject) (api.ReplaceTemplateResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) GetHealthzLive(_ context.Context, _ api.GetHealthzLiveRequestObject) (api.GetHealthzLiveResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) GetHealthzReady(_ context.Context, _ api.GetHealthzReadyRequestObject) (api.GetHealthzReadyResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}
func (stubStrictServer) GetPrometheusMetrics(_ context.Context, _ api.GetPrometheusMetricsRequestObject) (api.GetPrometheusMetricsResponseObject, error) {
	return nil, nil //nolint:nilnil // smoke stub
}

// compile-time assertion that the stub satisfies the generated
// interface. If oapi-codegen drops or renames an operation method,
// this line fails to build.
var _ api.StrictServerInterface = stubStrictServer{}

// TestStubSatisfiesStrictServerInterface: runtime no-op companion to
// the compile-time check above. Its mere presence keeps go vet from
// flagging the var as unused in some build configurations.
func TestStubSatisfiesStrictServerInterface(t *testing.T) {
	var _ api.StrictServerInterface = stubStrictServer{}
	require.NotNil(t, t)
}
