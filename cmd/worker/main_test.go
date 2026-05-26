package main

import (
	"testing"

	"github.com/sony/gobreaker"

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
