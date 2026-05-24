package domain_test

import (
	"errors"
	"testing"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// TestNewPriority covers parsing and validation of priority input. Priority
// follows the same shape as Channel (ADR-0001): an underlying string with
// three canonical lowercase constants and a lenient parser. Comparison
// semantics (high > normal > low) live in the queue adapter where asynq
// translates them into queue weights — the domain itself only cares that
// the priority names a valid bucket.
func TestNewPriority(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    domain.Priority
		wantErr error
	}{
		{name: "low lowercase", input: "low", want: domain.PriorityLow},
		{name: "normal lowercase", input: "normal", want: domain.PriorityNormal},
		{name: "high lowercase", input: "high", want: domain.PriorityHigh},
		{name: "low uppercase normalised", input: "LOW", want: domain.PriorityLow},
		{name: "normal mixed case normalised", input: "Normal", want: domain.PriorityNormal},
		{name: "high with surrounding whitespace", input: "  high  ", want: domain.PriorityHigh},
		{name: "empty string", input: "", wantErr: domain.ErrInvalidPriority},
		{name: "whitespace only", input: "   ", wantErr: domain.ErrInvalidPriority},
		{name: "unknown priority", input: "critical", wantErr: domain.ErrInvalidPriority},
		{name: "near-miss", input: "lov", wantErr: domain.ErrInvalidPriority},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.NewPriority(tc.input)

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
				t.Errorf("priority = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPriority_String confirms the canonical lowercase representation; this
// is what gets persisted, embedded in queue payloads, and serialized in API
// responses.
func TestPriority_String(t *testing.T) {
	cases := map[domain.Priority]string{
		domain.PriorityLow:    "low",
		domain.PriorityNormal: "normal",
		domain.PriorityHigh:   "high",
	}
	for p, want := range cases {
		t.Run(want, func(t *testing.T) {
			if got := p.String(); got != want {
				t.Errorf("Priority.String() = %q, want %q", got, want)
			}
		})
	}
}

// TestPriority_IsValid is the strict predicate: only the package-level
// constants count as valid. Casting a raw string (e.g. Priority("HIGH"))
// does not pass — callers that want lenient handling must go through
// NewPriority.
func TestPriority_IsValid(t *testing.T) {
	valid := []domain.Priority{
		domain.PriorityLow,
		domain.PriorityNormal,
		domain.PriorityHigh,
	}
	for _, p := range valid {
		if !p.IsValid() {
			t.Errorf("Priority(%q).IsValid() = false, want true", p)
		}
	}

	invalid := []domain.Priority{
		domain.Priority(""),
		domain.Priority("critical"),
		domain.Priority("HIGH"), // not normalised
	}
	for _, p := range invalid {
		if p.IsValid() {
			t.Errorf("Priority(%q).IsValid() = true, want false", p)
		}
	}
}
