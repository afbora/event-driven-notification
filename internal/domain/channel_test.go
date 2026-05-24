package domain_test

import (
	"errors"
	"testing"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// TestNewChannel covers parsing and validation of channel input.
//
// Channel is a value object — three valid values (sms, email, push) and one
// sentinel error (ErrInvalidChannel). Input is case-insensitive but the
// normalised internal representation is always lowercase, so that callers can
// safely compare with ChannelSMS / ChannelEmail / ChannelPush constants.
func TestNewChannel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    domain.Channel
		wantErr error
	}{
		{name: "sms lowercase", input: "sms", want: domain.ChannelSMS},
		{name: "email lowercase", input: "email", want: domain.ChannelEmail},
		{name: "push lowercase", input: "push", want: domain.ChannelPush},
		{name: "sms uppercase normalised", input: "SMS", want: domain.ChannelSMS},
		{name: "email mixed case normalised", input: "Email", want: domain.ChannelEmail},
		{name: "push with surrounding whitespace", input: "  push  ", want: domain.ChannelPush},
		{name: "empty string", input: "", wantErr: domain.ErrInvalidChannel},
		{name: "whitespace only", input: "   ", wantErr: domain.ErrInvalidChannel},
		{name: "unknown channel", input: "whatsapp", wantErr: domain.ErrInvalidChannel},
		{name: "near-miss", input: "smss", wantErr: domain.ErrInvalidChannel},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.NewChannel(tc.input)

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
				t.Errorf("channel = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestChannel_String confirms the value object renders to its canonical
// lowercase form. This is the representation persisted to the database and
// emitted in API responses, so the contract is load-bearing.
func TestChannel_String(t *testing.T) {
	cases := map[domain.Channel]string{
		domain.ChannelSMS:   "sms",
		domain.ChannelEmail: "email",
		domain.ChannelPush:  "push",
	}
	for ch, want := range cases {
		t.Run(want, func(t *testing.T) {
			if got := ch.String(); got != want {
				t.Errorf("Channel.String() = %q, want %q", got, want)
			}
		})
	}
}

// TestChannel_IsValid verifies the predicate used by external callers (e.g.,
// HTTP request validators) that want to check a channel without parsing.
func TestChannel_IsValid(t *testing.T) {
	valid := []domain.Channel{
		domain.ChannelSMS,
		domain.ChannelEmail,
		domain.ChannelPush,
	}
	for _, ch := range valid {
		if !ch.IsValid() {
			t.Errorf("Channel(%q).IsValid() = false, want true", ch)
		}
	}

	invalid := []domain.Channel{
		domain.Channel(""),
		domain.Channel("whatsapp"),
		domain.Channel("SMS"), // not normalised
	}
	for _, ch := range invalid {
		if ch.IsValid() {
			t.Errorf("Channel(%q).IsValid() = true, want false", ch)
		}
	}
}
