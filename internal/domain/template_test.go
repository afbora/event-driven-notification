package domain_test

import (
	"errors"
	"testing"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// validTemplateInput returns a known-good NewTemplateInput.
func validTemplateInput() domain.NewTemplateInput {
	return domain.NewTemplateInput{
		ID:      "01HXYZTEMPLATE0000000000000",
		Name:    "welcome-sms",
		Channel: domain.ChannelSMS,
		Body:    "Hello {{.Name}}, your code is {{.Code}}.",
	}
}

// TestNewTemplate covers constructor validation. Body syntax checking is
// part of the contract — an unparseable template body returns
// ErrInvalidTemplateBody so the failure surfaces at template-creation time,
// not at render time when a real notification depends on it.
func TestNewTemplate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*domain.NewTemplateInput)
		wantErr error
	}{
		{name: "valid sms template", mutate: func(_ *domain.NewTemplateInput) {}},
		{
			name: "valid email template",
			mutate: func(in *domain.NewTemplateInput) {
				in.Channel = domain.ChannelEmail
				in.Body = "Hi {{.Name}}, please confirm at {{.Link}}."
			},
		},
		{
			name: "valid push template",
			mutate: func(in *domain.NewTemplateInput) {
				in.Channel = domain.ChannelPush
				in.Body = "Breaking: {{.Headline}}"
			},
		},
		{
			name: "body without variables is valid",
			mutate: func(in *domain.NewTemplateInput) {
				in.Body = "Service is back online."
			},
		},

		// Required fields
		{name: "empty id", mutate: func(in *domain.NewTemplateInput) { in.ID = "" }, wantErr: domain.ErrInvalidTemplateID},
		{name: "empty name", mutate: func(in *domain.NewTemplateInput) { in.Name = "" }, wantErr: domain.ErrInvalidTemplateName},
		{name: "invalid channel", mutate: func(in *domain.NewTemplateInput) { in.Channel = domain.Channel("fax") }, wantErr: domain.ErrInvalidChannel},
		{name: "empty body", mutate: func(in *domain.NewTemplateInput) { in.Body = "" }, wantErr: domain.ErrInvalidTemplateBody},

		// Body syntax — Go text/template parser must accept it
		{name: "unclosed action", mutate: func(in *domain.NewTemplateInput) { in.Body = "Hello {{.Name" }, wantErr: domain.ErrInvalidTemplateBody},
		{name: "unknown function", mutate: func(in *domain.NewTemplateInput) { in.Body = "Hello {{badFn .Name}}" }, wantErr: domain.ErrInvalidTemplateBody},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := validTemplateInput()
			tc.mutate(&in)

			tmpl, err := domain.NewTemplate(in, fixedNow)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if tmpl == nil {
				t.Fatal("template is nil")
			}
			if tmpl.ID != in.ID {
				t.Errorf("ID = %q, want %q", tmpl.ID, in.ID)
			}
			if !tmpl.CreatedAt.Equal(fixedNow) {
				t.Errorf("CreatedAt = %v, want %v", tmpl.CreatedAt, fixedNow)
			}
		})
	}
}

// TestTemplate_Render exercises the substitution behavior, including the
// strict missing-variable policy. Templates that reference a variable not
// in the provided map must fail loudly — silent <no value> output would
// ship a half-baked notification to a real user.
func TestTemplate_Render(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		vars    map[string]any
		want    string
		wantErr error
	}{
		{
			name: "single substitution",
			body: "Hello {{.Name}}!",
			vars: map[string]any{"Name": "Ahmet"},
			want: "Hello Ahmet!",
		},
		{
			name: "multiple substitutions",
			body: "{{.Greeting}}, {{.Name}}!",
			vars: map[string]any{"Greeting": "Hi", "Name": "Bora"},
			want: "Hi, Bora!",
		},
		{
			name: "non-string variable types",
			body: "You have {{.Count}} new messages.",
			vars: map[string]any{"Count": 5},
			want: "You have 5 new messages.",
		},
		{
			name: "no variables in body",
			body: "Service is back online.",
			vars: nil,
			want: "Service is back online.",
		},
		{
			name: "extra variables are ignored",
			body: "Hello {{.Name}}!",
			vars: map[string]any{"Name": "X", "Unused": "ignored"},
			want: "Hello X!",
		},

		// Missing variable must error, not produce "<no value>".
		{
			name:    "missing variable",
			body:    "Hello {{.Name}}!",
			vars:    map[string]any{},
			wantErr: domain.ErrTemplateRenderFailed,
		},
		{
			name:    "nil vars when body needs them",
			body:    "Hello {{.Name}}!",
			vars:    nil,
			wantErr: domain.ErrTemplateRenderFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := domain.NewTemplate(domain.NewTemplateInput{
				ID:      "01HXYZTMPL00000000000000000",
				Name:    "render-test",
				Channel: domain.ChannelSMS,
				Body:    tc.body,
			}, fixedNow)
			if err != nil {
				t.Fatalf("setup template: %v", err)
			}

			got, err := tmpl.Render(tc.vars)

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
				t.Errorf("Render() = %q, want %q", got, tc.want)
			}
		})
	}
}
