package main

import (
	"testing"
	"time"

	"github.com/sony/gobreaker"
	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/infrastructure/metrics"
)

// TestBreakerStateToMetric_MapsAllStates pins the gobreaker.State →
// metrics.CircuitBreakerState mapping the OnStateChange callback relies
// on. The mapping is fixed by ADR convention (0=closed, 1=open,
// 2=half-open) so Grafana panels can pivot on numeric thresholds; an
// accidental swap (e.g. Open→Closed) would silently misclassify every
// breaker transition and live sweeps that only happen to observe one
// state would not catch it.
//
// The unknown-state case is in the table on purpose: gobreaker may grow
// new states in a future release, and the safe default (closed) keeps
// the gauge from spiking to a bogus value during a dependency upgrade.
func TestBreakerStateToMetric_MapsAllStates(t *testing.T) {
	cases := map[gobreaker.State]metrics.CircuitBreakerState{
		gobreaker.StateClosed:   metrics.CircuitClosed,
		gobreaker.StateOpen:     metrics.CircuitOpen,
		gobreaker.StateHalfOpen: metrics.CircuitHalfOpen,
		gobreaker.State(99):     metrics.CircuitClosed, // unknown future state → safe default
	}
	for in, want := range cases {
		if got := breakerStateToMetric(in); got != want {
			t.Errorf("breakerStateToMetric(%v) = %v, want %v", in, got, want)
		}
	}
}

// TestBreakerSettings_TripsAtConfiguredThreshold pins the gobreaker.Settings
// the worker builds from config to CLAUDE.md §5's documented behavior: open
// after maxFailures within the window (Interval), fail fast for openTimeout
// (Timeout), and allow a single half-open probe (MaxRequests=1). Without
// these explicit settings gobreaker silently falls back to its own defaults
// (trip at >5 *consecutive* failures, 60s open) which contradict both the
// README failure-path diagram and the constitution (ADR-0016). A regression
// that dropped any field would let the breaker drift back to those defaults
// without any test noticing — this test is the guard.
func TestBreakerSettings_TripsAtConfiguredThreshold(t *testing.T) {
	s := breakerSettings("provider-registry", 5, 10*time.Second, 30*time.Second, nil)

	require.Equal(t, "provider-registry", s.Name)
	require.EqualValues(t, 1, s.MaxRequests, "exactly one half-open probe")
	require.Equal(t, 10*time.Second, s.Interval, "10s count-clear window")
	require.Equal(t, 30*time.Second, s.Timeout, "30s open before half-open")
	require.NotNil(t, s.ReadyToTrip)

	require.False(t, s.ReadyToTrip(gobreaker.Counts{TotalFailures: 4}),
		"4 failures within the window: breaker stays closed")
	require.True(t, s.ReadyToTrip(gobreaker.Counts{TotalFailures: 5}),
		"5 failures within the window: breaker trips")
}
