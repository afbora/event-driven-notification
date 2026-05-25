package http

import (
	"bufio"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// The custom ResponseWriter wrappers used by MetricsMiddleware
// (statusTracker) and IdempotencyMiddleware (capturingWriter) must
// expose http.Hijacker and http.Flusher when the underlying writer
// supports them. Without those methods the WebSocket upgrade in
// coder/websocket fails with "http.ResponseWriter does not implement
// http.Hijacker", and any streaming response (SSE, chunked transfer)
// silently breaks because handlers cannot flush.
//
// Go does NOT promote methods from the concrete type behind an
// embedded interface — so embedding nethttp.ResponseWriter does not
// give the wrapper Hijack/Flush automatically. The wrapper must
// declare them explicitly and forward to the underlying writer.
//
// These tests document that contract.

// fakeHijackerWriter implements both http.ResponseWriter and
// http.Hijacker so we can verify the wrapper forwards Hijack to the
// underlying writer. It tracks whether Hijack was called.
type fakeHijackerWriter struct {
	nethttp.ResponseWriter
	hijackCalled bool
}

func (f *fakeHijackerWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	f.hijackCalled = true
	return nil, nil, nil
}

// fakeFlusherWriter implements both http.ResponseWriter and
// http.Flusher so we can verify the wrapper forwards Flush.
type fakeFlusherWriter struct {
	nethttp.ResponseWriter
	flushCalled bool
}

func (f *fakeFlusherWriter) Flush() {
	f.flushCalled = true
}

// --- statusTracker -------------------------------------------------------

// TestStatusTracker_ImplementsHijacker: the wrapper must satisfy
// http.Hijacker so coder/websocket's type assertion during Accept
// succeeds. Failing this test means WebSocket upgrades will fail
// at runtime through MetricsMiddleware.
func TestStatusTracker_ImplementsHijacker(t *testing.T) {
	var w nethttp.ResponseWriter = &statusTracker{ResponseWriter: httptest.NewRecorder()}
	_, ok := w.(nethttp.Hijacker)
	require.True(t, ok, "statusTracker must implement http.Hijacker so WebSocket upgrades succeed")
}

// TestStatusTracker_HijackForwardsToUnderlying: when the underlying
// writer supports Hijacker, statusTracker.Hijack must delegate to it.
func TestStatusTracker_HijackForwardsToUnderlying(t *testing.T) {
	underlying := &fakeHijackerWriter{ResponseWriter: httptest.NewRecorder()}
	tracker := &statusTracker{ResponseWriter: underlying}

	_, _, err := nethttp.ResponseWriter(tracker).(nethttp.Hijacker).Hijack()
	require.NoError(t, err)
	require.True(t, underlying.hijackCalled, "Hijack must reach the underlying writer")
}

// TestStatusTracker_HijackErrorsWhenUnderlyingUnsupported: when the
// underlying writer does not support Hijacker (e.g. httptest.NewRecorder
// in this test), statusTracker.Hijack must return a clear error
// rather than panicking on a nil dereference or silently swallowing
// the call.
func TestStatusTracker_HijackErrorsWhenUnderlyingUnsupported(t *testing.T) {
	tracker := &statusTracker{ResponseWriter: httptest.NewRecorder()}

	_, _, err := nethttp.ResponseWriter(tracker).(nethttp.Hijacker).Hijack()
	require.Error(t, err, "Hijack on a non-hijackable underlying writer must surface an error")
}

// TestStatusTracker_ImplementsFlusher: the wrapper must satisfy
// http.Flusher so streaming response handlers (SSE, chunked transfer)
// can still flush through MetricsMiddleware.
func TestStatusTracker_ImplementsFlusher(t *testing.T) {
	var w nethttp.ResponseWriter = &statusTracker{ResponseWriter: httptest.NewRecorder()}
	_, ok := w.(nethttp.Flusher)
	require.True(t, ok, "statusTracker must implement http.Flusher for streaming bodies")
}

// TestStatusTracker_FlushForwardsToUnderlying: when the underlying
// writer supports Flusher, statusTracker.Flush must delegate.
func TestStatusTracker_FlushForwardsToUnderlying(t *testing.T) {
	underlying := &fakeFlusherWriter{ResponseWriter: httptest.NewRecorder()}
	tracker := &statusTracker{ResponseWriter: underlying}

	nethttp.ResponseWriter(tracker).(nethttp.Flusher).Flush()
	require.True(t, underlying.flushCalled, "Flush must reach the underlying writer")
}

// --- capturingWriter -----------------------------------------------------

// TestCapturingWriter_ImplementsHijacker: same contract as
// statusTracker, this time for IdempotencyMiddleware. Without it any
// upgradeable endpoint (WebSocket) that flows through capturingWriter
// — which is every write request when the client supplies an
// Idempotency-Key header — fails to upgrade.
func TestCapturingWriter_ImplementsHijacker(t *testing.T) {
	var w nethttp.ResponseWriter = &capturingWriter{ResponseWriter: httptest.NewRecorder()}
	_, ok := w.(nethttp.Hijacker)
	require.True(t, ok, "capturingWriter must implement http.Hijacker so WebSocket upgrades succeed")
}

// TestCapturingWriter_HijackForwardsToUnderlying: when the underlying
// writer supports Hijacker, capturingWriter.Hijack must delegate.
func TestCapturingWriter_HijackForwardsToUnderlying(t *testing.T) {
	underlying := &fakeHijackerWriter{ResponseWriter: httptest.NewRecorder()}
	cw := &capturingWriter{ResponseWriter: underlying}

	_, _, err := nethttp.ResponseWriter(cw).(nethttp.Hijacker).Hijack()
	require.NoError(t, err)
	require.True(t, underlying.hijackCalled, "Hijack must reach the underlying writer")
}

// TestCapturingWriter_HijackErrorsWhenUnderlyingUnsupported: when the
// underlying writer does not support Hijacker, capturingWriter.Hijack
// must return a clear error rather than panicking.
func TestCapturingWriter_HijackErrorsWhenUnderlyingUnsupported(t *testing.T) {
	cw := &capturingWriter{ResponseWriter: httptest.NewRecorder()}

	_, _, err := nethttp.ResponseWriter(cw).(nethttp.Hijacker).Hijack()
	require.Error(t, err, "Hijack on a non-hijackable underlying writer must surface an error")
}

// TestCapturingWriter_ImplementsFlusher: the wrapper must satisfy
// http.Flusher for streaming-body handlers.
func TestCapturingWriter_ImplementsFlusher(t *testing.T) {
	var w nethttp.ResponseWriter = &capturingWriter{ResponseWriter: httptest.NewRecorder()}
	_, ok := w.(nethttp.Flusher)
	require.True(t, ok, "capturingWriter must implement http.Flusher for streaming bodies")
}

// TestCapturingWriter_FlushForwardsToUnderlying: when the underlying
// writer supports Flusher, capturingWriter.Flush must delegate.
func TestCapturingWriter_FlushForwardsToUnderlying(t *testing.T) {
	underlying := &fakeFlusherWriter{ResponseWriter: httptest.NewRecorder()}
	cw := &capturingWriter{ResponseWriter: underlying}

	nethttp.ResponseWriter(cw).(nethttp.Flusher).Flush()
	require.True(t, underlying.flushCalled, "Flush must reach the underlying writer")
}
