package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// fixedNow is the synthetic clock used throughout the notification tests. The
// concrete value is irrelevant; what matters is that it is identical across
// every assertion, so timestamp comparisons are stable.
var fixedNow = time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)

// validInput returns a known-good NewNotificationInput. Each test mutates
// only the field it is exercising, keeping the table-driven cases short.
func validInput() domain.NewNotificationInput {
	return domain.NewNotificationInput{
		ID:             "01HXYZ7Z9N6V0V9N6V0V9N6V0V",
		CorrelationID:  "01HXYZ7Z9N6V0V9N6V0V9N6V0V",
		Channel:        domain.ChannelSMS,
		Priority:       domain.PriorityNormal,
		Recipient:      "+905551234567",
		Content:        "hello world",
		IdempotencyKey: "",
	}
}

// TestNewNotification exercises every validation branch in the constructor.
// On success it also confirms the initial state: Status=pending, Attempts=0,
// CreatedAt=UpdatedAt=now.
func TestNewNotification(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*domain.NewNotificationInput)
		wantErr error
	}{
		{name: "valid sms", mutate: func(in *domain.NewNotificationInput) {}},
		{
			name: "valid email",
			mutate: func(in *domain.NewNotificationInput) {
				in.Channel = domain.ChannelEmail
				in.Recipient = "user@example.com"
				in.Content = "Subject and body."
			},
		},
		{
			name: "valid push",
			mutate: func(in *domain.NewNotificationInput) {
				in.Channel = domain.ChannelPush
				in.Recipient = "fcm-token-abc123"
				in.Content = "Breaking news."
			},
		},

		// ID
		{name: "empty id", mutate: func(in *domain.NewNotificationInput) { in.ID = "" }, wantErr: domain.ErrInvalidNotificationID},

		// CorrelationID
		{name: "empty correlation id", mutate: func(in *domain.NewNotificationInput) { in.CorrelationID = "" }, wantErr: domain.ErrInvalidCorrelationID},

		// Channel
		{name: "invalid channel", mutate: func(in *domain.NewNotificationInput) { in.Channel = domain.Channel("fax") }, wantErr: domain.ErrInvalidChannel},

		// Priority
		{name: "invalid priority", mutate: func(in *domain.NewNotificationInput) { in.Priority = domain.Priority("urgent") }, wantErr: domain.ErrInvalidPriority},

		// SMS recipient — E.164 format
		{name: "sms missing +", mutate: func(in *domain.NewNotificationInput) { in.Recipient = "905551234567" }, wantErr: domain.ErrInvalidRecipient},
		{name: "sms with letters", mutate: func(in *domain.NewNotificationInput) { in.Recipient = "+abc551234567" }, wantErr: domain.ErrInvalidRecipient},
		{name: "sms too short", mutate: func(in *domain.NewNotificationInput) { in.Recipient = "+9" }, wantErr: domain.ErrInvalidRecipient},
		{name: "sms too long", mutate: func(in *domain.NewNotificationInput) { in.Recipient = "+" + strings.Repeat("9", 16) }, wantErr: domain.ErrInvalidRecipient},
		{name: "sms empty", mutate: func(in *domain.NewNotificationInput) { in.Recipient = "" }, wantErr: domain.ErrInvalidRecipient},

		// Email recipient
		{
			name: "email missing @",
			mutate: func(in *domain.NewNotificationInput) {
				in.Channel = domain.ChannelEmail
				in.Recipient = "userexample.com"
				in.Content = "ok"
			},
			wantErr: domain.ErrInvalidRecipient,
		},
		{
			name: "email missing local-part",
			mutate: func(in *domain.NewNotificationInput) {
				in.Channel = domain.ChannelEmail
				in.Recipient = "@example.com"
				in.Content = "ok"
			},
			wantErr: domain.ErrInvalidRecipient,
		},
		{
			name: "email missing domain",
			mutate: func(in *domain.NewNotificationInput) {
				in.Channel = domain.ChannelEmail
				in.Recipient = "user@"
				in.Content = "ok"
			},
			wantErr: domain.ErrInvalidRecipient,
		},

		// Push recipient — just non-empty token
		{
			name: "push empty token",
			mutate: func(in *domain.NewNotificationInput) {
				in.Channel = domain.ChannelPush
				in.Recipient = ""
				in.Content = "ok"
			},
			wantErr: domain.ErrInvalidRecipient,
		},

		// Content — channel-specific length limits
		{name: "sms content empty", mutate: func(in *domain.NewNotificationInput) { in.Content = "" }, wantErr: domain.ErrInvalidContent},
		{name: "sms content too long", mutate: func(in *domain.NewNotificationInput) { in.Content = strings.Repeat("x", 161) }, wantErr: domain.ErrInvalidContent},
		{name: "sms content exactly 160", mutate: func(in *domain.NewNotificationInput) { in.Content = strings.Repeat("x", 160) }},
		{
			name: "email content too long",
			mutate: func(in *domain.NewNotificationInput) {
				in.Channel = domain.ChannelEmail
				in.Recipient = "user@example.com"
				in.Content = strings.Repeat("x", 10001)
			},
			wantErr: domain.ErrInvalidContent,
		},
		{
			name: "email content exactly 10000",
			mutate: func(in *domain.NewNotificationInput) {
				in.Channel = domain.ChannelEmail
				in.Recipient = "user@example.com"
				in.Content = strings.Repeat("x", 10000)
			},
		},
		{
			name: "push content too long",
			mutate: func(in *domain.NewNotificationInput) {
				in.Channel = domain.ChannelPush
				in.Recipient = "fcm-token"
				in.Content = strings.Repeat("x", 501)
			},
			wantErr: domain.ErrInvalidContent,
		},
		{
			name: "push content exactly 500",
			mutate: func(in *domain.NewNotificationInput) {
				in.Channel = domain.ChannelPush
				in.Recipient = "fcm-token"
				in.Content = strings.Repeat("x", 500)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.mutate(&in)

			n, err := domain.NewNotification(in, fixedNow)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if n == nil {
				t.Fatal("notification is nil")
			}
			if n.Status != domain.StatusPending {
				t.Errorf("initial status = %q, want pending", n.Status)
			}
			if n.Attempts != 0 {
				t.Errorf("initial attempts = %d, want 0", n.Attempts)
			}
			if !n.CreatedAt.Equal(fixedNow) {
				t.Errorf("CreatedAt = %v, want %v", n.CreatedAt, fixedNow)
			}
			if !n.UpdatedAt.Equal(fixedNow) {
				t.Errorf("UpdatedAt = %v, want %v", n.UpdatedAt, fixedNow)
			}
		})
	}
}

// newPending returns a fresh notification for transition tests, advancing it
// to the requested starting status. It abstracts the mark-chain so each test
// reads as "given a notification in status X, when we call Y, then ...".
func newPending(t *testing.T) *domain.Notification {
	t.Helper()
	n, err := domain.NewNotification(validInput(), fixedNow)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return n
}

func newQueued(t *testing.T) *domain.Notification {
	t.Helper()
	n := newPending(t)
	if err := n.MarkQueued(fixedNow); err != nil {
		t.Fatalf("setup queued: %v", err)
	}
	return n
}

func newProcessing(t *testing.T) *domain.Notification {
	t.Helper()
	n := newQueued(t)
	if err := n.MarkProcessing(fixedNow); err != nil {
		t.Fatalf("setup processing: %v", err)
	}
	return n
}

func TestNotification_MarkQueued(t *testing.T) {
	t.Run("pending → queued", func(t *testing.T) {
		n := newPending(t)
		later := fixedNow.Add(time.Second)
		if err := n.MarkQueued(later); err != nil {
			t.Fatalf("MarkQueued: %v", err)
		}
		if n.Status != domain.StatusQueued {
			t.Errorf("status = %q, want queued", n.Status)
		}
		if !n.UpdatedAt.Equal(later) {
			t.Errorf("UpdatedAt = %v, want %v", n.UpdatedAt, later)
		}
	})

	t.Run("processing → queued is rejected", func(t *testing.T) {
		n := newProcessing(t)
		err := n.MarkQueued(fixedNow)
		if !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("err = %v, want ErrInvalidTransition", err)
		}
		if n.Status != domain.StatusProcessing {
			t.Errorf("status mutated on failed transition: %q", n.Status)
		}
	})
}

func TestNotification_MarkProcessing(t *testing.T) {
	t.Run("queued → processing increments attempts", func(t *testing.T) {
		n := newQueued(t)
		if err := n.MarkProcessing(fixedNow); err != nil {
			t.Fatalf("MarkProcessing: %v", err)
		}
		if n.Status != domain.StatusProcessing {
			t.Errorf("status = %q, want processing", n.Status)
		}
		if n.Attempts != 1 {
			t.Errorf("attempts = %d, want 1", n.Attempts)
		}
	})

	t.Run("retrying → processing increments attempts again", func(t *testing.T) {
		n := newProcessing(t)
		if err := n.MarkRetrying(fixedNow, "5xx", fixedNow.Add(30*time.Second)); err != nil {
			t.Fatalf("MarkRetrying: %v", err)
		}
		if err := n.MarkProcessing(fixedNow); err != nil {
			t.Fatalf("MarkProcessing retry: %v", err)
		}
		if n.Attempts != 2 {
			t.Errorf("attempts after retry = %d, want 2", n.Attempts)
		}
	})

	t.Run("pending → processing skip is rejected", func(t *testing.T) {
		n := newPending(t)
		err := n.MarkProcessing(fixedNow)
		if !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("err = %v, want ErrInvalidTransition", err)
		}
	})
}

func TestNotification_MarkDelivered(t *testing.T) {
	t.Run("processing → delivered", func(t *testing.T) {
		n := newProcessing(t)
		if err := n.MarkDelivered(fixedNow); err != nil {
			t.Fatalf("MarkDelivered: %v", err)
		}
		if n.Status != domain.StatusDelivered {
			t.Errorf("status = %q, want delivered", n.Status)
		}
		if n.LastError != "" {
			t.Errorf("LastError = %q, want empty", n.LastError)
		}
	})

	t.Run("queued → delivered is rejected", func(t *testing.T) {
		n := newQueued(t)
		err := n.MarkDelivered(fixedNow)
		if !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("err = %v, want ErrInvalidTransition", err)
		}
	})
}

func TestNotification_MarkFailed(t *testing.T) {
	t.Run("processing → failed records reason", func(t *testing.T) {
		n := newProcessing(t)
		if err := n.MarkFailed(fixedNow, "provider returned 400"); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}
		if n.Status != domain.StatusFailed {
			t.Errorf("status = %q, want failed", n.Status)
		}
		if n.LastError != "provider returned 400" {
			t.Errorf("LastError = %q, want %q", n.LastError, "provider returned 400")
		}
	})

	t.Run("pending → failed is rejected", func(t *testing.T) {
		n := newPending(t)
		err := n.MarkFailed(fixedNow, "boom")
		if !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("err = %v, want ErrInvalidTransition", err)
		}
	})
}

func TestNotification_MarkRetrying(t *testing.T) {
	t.Run("processing → retrying records reason and next retry", func(t *testing.T) {
		n := newProcessing(t)
		nextAt := fixedNow.Add(30 * time.Second)
		if err := n.MarkRetrying(fixedNow, "5xx", nextAt); err != nil {
			t.Fatalf("MarkRetrying: %v", err)
		}
		if n.Status != domain.StatusRetrying {
			t.Errorf("status = %q, want retrying", n.Status)
		}
		if n.LastError != "5xx" {
			t.Errorf("LastError = %q, want %q", n.LastError, "5xx")
		}
		if n.NextRetryAt == nil || !n.NextRetryAt.Equal(nextAt) {
			t.Errorf("NextRetryAt = %v, want %v", n.NextRetryAt, nextAt)
		}
	})

	t.Run("delivered → retrying is rejected", func(t *testing.T) {
		n := newProcessing(t)
		_ = n.MarkDelivered(fixedNow)
		err := n.MarkRetrying(fixedNow, "5xx", fixedNow)
		if !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("err = %v, want ErrInvalidTransition", err)
		}
	})
}

func TestNotification_Cancel(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) *domain.Notification
		want  error
	}{
		{name: "pending cancellable", setup: newPending},
		{name: "queued cancellable", setup: newQueued},
		{
			name: "retrying cancellable",
			setup: func(t *testing.T) *domain.Notification {
				t.Helper()
				n := newProcessing(t)
				if err := n.MarkRetrying(fixedNow, "5xx", fixedNow); err != nil {
					t.Fatalf("setup: %v", err)
				}
				return n
			},
		},
		{
			name:  "processing rejected (mid-flight)",
			setup: newProcessing,
			want:  domain.ErrInvalidTransition,
		},
		{
			name: "delivered rejected (terminal)",
			setup: func(t *testing.T) *domain.Notification {
				t.Helper()
				n := newProcessing(t)
				if err := n.MarkDelivered(fixedNow); err != nil {
					t.Fatalf("setup: %v", err)
				}
				return n
			},
			want: domain.ErrInvalidTransition,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := tc.setup(t)
			err := n.Cancel(fixedNow)
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Errorf("err = %v, want %v", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected err: %v", err)
			}
			if n.Status != domain.StatusCancelled {
				t.Errorf("status = %q, want cancelled", n.Status)
			}
		})
	}
}
