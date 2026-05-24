package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// buildBatchTestServer is the batch-handler analog of buildTestServer
// — wires only the CreateBatch executor and leaves the rest as 501
// stubs.
func buildBatchTestServer(t *testing.T, exec httpadapter.CreateBatchExecutor) chi.Router {
	t.Helper()
	server := httpadapter.NewServer(httpadapter.ServerOptions{
		CreateBatch: exec,
	})
	r := chi.NewRouter()
	api.HandlerFromMux(api.NewStrictHandlerWithOptions(
		server, nil,
		api.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  httpadapter.RespondWithError,
			ResponseErrorHandlerFunc: httpadapter.RespondWithError,
		},
	), r)
	return r
}

// sampleBatch is the domain batch a happy-path fake returns. Three
// notifications, all sharing the correlation id the caller passed in.
func sampleBatch(correlationID string) *domain.Batch {
	batchID := domain.BatchID("01940000-0000-7000-8000-000000000bbb")
	now := fixedTime

	mk := func(i int, idStr string) *domain.Notification {
		bid := batchID
		return &domain.Notification{
			ID:            domain.NotificationID(idStr),
			BatchID:       &bid,
			CorrelationID: correlationID,
			Channel:       domain.ChannelSMS,
			Priority:      domain.PriorityNormal,
			Status:        domain.StatusPending,
			Recipient:     "+1555555000" + string(rune('0'+i)),
			Content:       "hello",
			CreatedAt:     now,
			UpdatedAt:     now,
		}
	}

	return &domain.Batch{
		ID:            batchID,
		CorrelationID: correlationID,
		Notifications: []*domain.Notification{
			mk(0, "01940000-0000-7000-8000-000000000a01"),
			mk(1, "01940000-0000-7000-8000-000000000a02"),
			mk(2, "01940000-0000-7000-8000-000000000a03"),
		},
		CreatedAt: now,
	}
}

// TestCreateBatch_HappyPath: a valid batch request reaches the use
// case with three items, the response carries the batch id, size,
// shared correlation id, and a Location header.
func TestCreateBatch_HappyPath(t *testing.T) {
	const correlation = "01HXYZBATCHCORR000001"

	captured := struct {
		called bool
		input  application.CreateBatchInput
	}{}
	exec := func(_ context.Context, in application.CreateBatchInput) (*domain.Batch, error) {
		captured.called = true
		captured.input = in
		return sampleBatch(correlation), nil
	}

	r := buildBatchTestServer(t, exec)
	body, err := json.Marshal(map[string]any{
		"notifications": []map[string]any{
			{"channel": "sms", "recipient": "+15555550000", "content": "hello"},
			{"channel": "sms", "recipient": "+15555550001", "content": "hello"},
			{"channel": "sms", "recipient": "+15555550002", "content": "hello"},
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/notifications/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Correlation-ID", correlation)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusAccepted, rr.Code, "body=%s", rr.Body.String())
	require.True(t, captured.called)
	require.Equal(t, correlation, captured.input.CorrelationID)
	require.Len(t, captured.input.Notifications, 3)
	require.Equal(t, "sms", captured.input.Notifications[0].Channel)
	require.Equal(t, "+15555550000", captured.input.Notifications[0].Recipient)

	var out api.Batch
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Equal(t, "01940000-0000-7000-8000-000000000bbb", out.Id.String())
	require.Equal(t, 3, out.Size)
	require.NotNil(t, out.CorrelationId)
	require.Equal(t, correlation, *out.CorrelationId)

	require.Equal(t, "/api/v1/notifications/batch/01940000-0000-7000-8000-000000000bbb",
		rr.Header().Get("Location"))
	require.Equal(t, correlation, rr.Header().Get("X-Correlation-ID"))
}

// TestCreateBatch_EmptyArray_DomainRejects: when the caller posts an
// empty notifications array, the use case is reached but the domain
// invariant (1 ≤ N) returns ErrInvalidBatchSize. The handler does no
// pre-validation — the domain is the authority.
func TestCreateBatch_EmptyArray_DomainRejects(t *testing.T) {
	exec := func(_ context.Context, _ application.CreateBatchInput) (*domain.Batch, error) {
		return nil, domain.ErrInvalidBatchSize
	}
	r := buildBatchTestServer(t, exec)
	body, _ := json.Marshal(map[string]any{
		"notifications": []map[string]any{},
	})
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/notifications/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusBadRequest, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, "application/problem+json", rr.Header().Get("Content-Type"))
	var p struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
	require.Equal(t, "/probs/invalid-batch-size", p.Type)
}

// TestCreateBatch_NotificationsAreOmittedInResponse: the spec says
// member notifications are omitted from POST 202 to keep the payload
// small. The handler must respect that — clients fetch members via
// GET /api/v1/notifications/batch/{id}.
func TestCreateBatch_NotificationsAreOmittedInResponse(t *testing.T) {
	exec := func(_ context.Context, _ application.CreateBatchInput) (*domain.Batch, error) {
		return sampleBatch("01HXYZ-no-members-on-create"), nil
	}
	r := buildBatchTestServer(t, exec)
	body, _ := json.Marshal(map[string]any{
		"notifications": []map[string]any{
			{"channel": "sms", "recipient": "+15555550000", "content": "hi"},
		},
	})
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/notifications/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusAccepted, rr.Code, "body=%s", rr.Body.String())
	var out api.Batch
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Equal(t, 3, out.Size, "size still reflects the actual count")
	require.Nil(t, out.Notifications,
		"notifications array is intentionally omitted on POST 202 — see openapi.yaml")
}

// TestCreateBatch_NilExecutor_NotImplemented: when the Server has not
// been wired with a CreateBatch executor the embedded
// unimplementedServer returns ErrNotImplemented and the response is
// 501. Same contract as every other not-yet-wired operation.
func TestCreateBatch_NilExecutor_NotImplemented(t *testing.T) {
	r := buildBatchTestServer(t, nil)
	body, _ := json.Marshal(map[string]any{
		"notifications": []map[string]any{
			{"channel": "sms", "recipient": "+15555550000", "content": "hi"},
		},
	})
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/notifications/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotImplemented, rr.Code, "body=%s", rr.Body.String())
}
