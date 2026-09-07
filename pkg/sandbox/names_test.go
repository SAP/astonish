package sandbox

import (
	"testing"
)

func TestSanitizeInstanceName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple alphanumeric", "abc123", "abc123"},
		{"uppercase converted", "ABC123", "abc123"},
		{"uuid passthrough", "a2a5b479-b03a-4662", "a2a5b479-b03a-4662"},
		{"colons replaced", "email:direct:user", "email-direct-user"},
		{"email address", "user@example.com", "user-example-com"},
		{"consecutive special chars collapsed", "a::b@@c", "a-b-c"},
		{"leading special chars stripped", "::abc", "abc"},
		{"trailing special chars stripped", "abc::", "abc"},
		{"mixed special chars", "email:direct:user@domain.com", "email-direct-user-domain-com"},
		{"empty string", "", ""},
		{"only special chars", ":::@@@...", ""},
		{"hyphens preserved", "my-session-id", "my-session-id"},
		{"spaces replaced", "hello world", "hello-world"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeInstanceName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeInstanceName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
