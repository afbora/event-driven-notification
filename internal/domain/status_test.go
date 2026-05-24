package domain_test

import (
	"errors"
	"testing"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// TestNewStatus covers parsing and validation of status input. Status follows
// the same lenient-parse / strict-predicate shape as Channel and Priority,
// but adds a state machine on top (see TestStatus_CanTransitionTo below).
func TestNewStatus(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    domain.Status
		wantErr error
	}{
		{name: "pending", input: "pending", want: domain.StatusPending},
		{name: "queued", input: "queued", want: domain.StatusQueued},
		{name: "processing", input: "processing", want: domain.StatusProcessing},
		{name: "delivered", input: "delivered", want: domain.StatusDelivered},
		{name: "failed", input: "failed", want: domain.StatusFailed},
		{name: "retrying", input: "retrying", want: domain.StatusRetrying},
		{name: "cancelled", input: "cancelled", want: domain.StatusCancelled},
		{name: "uppercase normalised", input: "PENDING", want: domain.StatusPending},
		{name: "mixed case normalised", input: "Delivered", want: domain.StatusDelivered},
		{name: "whitespace trimmed", input: "  failed  ", want: domain.StatusFailed},
		{name: "empty", input: "", wantErr: domain.ErrInvalidStatus},
		{name: "whitespace only", input: "   ", wantErr: domain.ErrInvalidStatus},
		{name: "unknown status", input: "sent", wantErr: domain.ErrInvalidStatus},
		{name: "american spelling not accepted", input: "canceled", wantErr: domain.ErrInvalidStatus},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.NewStatus(tc.input)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStatus_String(t *testing.T) {
	cases := map[domain.Status]string{
		domain.StatusPending:    "pending",
		domain.StatusQueued:     "queued",
		domain.StatusProcessing: "processing",
		domain.StatusDelivered:  "delivered",
		domain.StatusFailed:     "failed",
		domain.StatusRetrying:   "retrying",
		domain.StatusCancelled:  "cancelled",
	}
	for s, want := range cases {
		t.Run(want, func(t *testing.T) {
			if got := s.String(); got != want {
				t.Errorf("Status.String() = %q, want %q", got, want)
			}
		})
	}
}

func TestStatus_IsValid(t *testing.T) {
	valid := []domain.Status{
		domain.StatusPending,
		domain.StatusQueued,
		domain.StatusProcessing,
		domain.StatusDelivered,
		domain.StatusFailed,
		domain.StatusRetrying,
		domain.StatusCancelled,
	}
	for _, s := range valid {
		if !s.IsValid() {
			t.Errorf("Status(%q).IsValid() = false, want true", s)
		}
	}

	invalid := []domain.Status{
		domain.Status(""),
		domain.Status("sent"),
		domain.Status("PENDING"), // not normalised
	}
	for _, s := range invalid {
		if s.IsValid() {
			t.Errorf("Status(%q).IsValid() = true, want false", s)
		}
	}
}

// TestStatus_IsTerminal records the project's policy: once a notification
// reaches delivered / failed / cancelled it never moves again. This is the
// hinge of the state machine — terminal states reject all outgoing
// transitions in CanTransitionTo.
func TestStatus_IsTerminal(t *testing.T) {
	terminal := []domain.Status{
		domain.StatusDelivered,
		domain.StatusFailed,
		domain.StatusCancelled,
	}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("Status(%q).IsTerminal() = false, want true", s)
		}
	}

	notTerminal := []domain.Status{
		domain.StatusPending,
		domain.StatusQueued,
		domain.StatusProcessing,
		domain.StatusRetrying,
	}
	for _, s := range notTerminal {
		if s.IsTerminal() {
			t.Errorf("Status(%q).IsTerminal() = true, want false", s)
		}
	}
}

// TestStatus_CanTransitionTo is the load-bearing test for the state machine.
// The PLAN.md contract:
//
//	pending → queued → processing → delivered | failed | retrying
//	retrying → processing
//	pending | queued | retrying → cancelled
//	delivered | failed | cancelled → (terminal)
//
// Anything else is invalid: skips, backward moves, same-state idempotent
// transitions, and processing → cancelled (you can't cancel while the
// provider is mid-call).
func TestStatus_CanTransitionTo(t *testing.T) {
	tests := []struct {
		name string
		from domain.Status
		to   domain.Status
		want bool
	}{
		// --- pending ------------------------------------------------------
		{name: "pending → queued", from: domain.StatusPending, to: domain.StatusQueued, want: true},
		{name: "pending → cancelled", from: domain.StatusPending, to: domain.StatusCancelled, want: true},
		{name: "pending → processing (skip queued)", from: domain.StatusPending, to: domain.StatusProcessing, want: false},
		{name: "pending → delivered (skip)", from: domain.StatusPending, to: domain.StatusDelivered, want: false},
		{name: "pending → pending (no-op)", from: domain.StatusPending, to: domain.StatusPending, want: false},

		// --- queued -------------------------------------------------------
		{name: "queued → processing", from: domain.StatusQueued, to: domain.StatusProcessing, want: true},
		{name: "queued → cancelled", from: domain.StatusQueued, to: domain.StatusCancelled, want: true},
		{name: "queued → pending (backward)", from: domain.StatusQueued, to: domain.StatusPending, want: false},
		{name: "queued → delivered (skip processing)", from: domain.StatusQueued, to: domain.StatusDelivered, want: false},

		// --- processing ---------------------------------------------------
		{name: "processing → delivered", from: domain.StatusProcessing, to: domain.StatusDelivered, want: true},
		{name: "processing → failed", from: domain.StatusProcessing, to: domain.StatusFailed, want: true},
		{name: "processing → retrying", from: domain.StatusProcessing, to: domain.StatusRetrying, want: true},
		{name: "processing → cancelled (mid-flight forbidden)", from: domain.StatusProcessing, to: domain.StatusCancelled, want: false},
		{name: "processing → queued (backward)", from: domain.StatusProcessing, to: domain.StatusQueued, want: false},
		{name: "processing → processing (no-op)", from: domain.StatusProcessing, to: domain.StatusProcessing, want: false},

		// --- retrying -----------------------------------------------------
		{name: "retrying → processing", from: domain.StatusRetrying, to: domain.StatusProcessing, want: true},
		{name: "retrying → cancelled", from: domain.StatusRetrying, to: domain.StatusCancelled, want: true},
		{name: "retrying → delivered (skip processing)", from: domain.StatusRetrying, to: domain.StatusDelivered, want: false},
		{name: "retrying → failed (skip processing)", from: domain.StatusRetrying, to: domain.StatusFailed, want: false},

		// --- terminal states reject every outgoing edge -------------------
		{name: "delivered → queued", from: domain.StatusDelivered, to: domain.StatusQueued, want: false},
		{name: "failed → retrying", from: domain.StatusFailed, to: domain.StatusRetrying, want: false},
		{name: "cancelled → pending", from: domain.StatusCancelled, to: domain.StatusPending, want: false},

		// --- invalid target -----------------------------------------------
		{name: "pending → bogus", from: domain.StatusPending, to: domain.Status("bogus"), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.from.CanTransitionTo(tc.to); got != tc.want {
				t.Errorf("%s.CanTransitionTo(%s) = %v, want %v", tc.from, tc.to, got, tc.want)
			}
		})
	}
}
