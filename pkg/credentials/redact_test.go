package credentials

import "testing"

func TestRedactNonUser_UserAuthored(t *testing.T) {
	r := NewRedactor()
	// Register a credential value
	r.AddSecret("my-api-key", "super-secret-value-12345")

	// User-authored text should pass through unchanged, even if it contains
	// a known credential value. This prevents revealing credential names
	// (e.g., [REDACTED]) to users who coincidentally type a
	// string matching a stored secret.
	input := "Here is the value: super-secret-value-12345"
	got := r.RedactNonUser(input, true)
	if got != input {
		t.Errorf("RedactNonUser(isUserAuthored=true) should return text unchanged\ngot:  %q\nwant: %q", got, input)
	}
}

func TestRedactNonUser_NonUserAuthored(t *testing.T) {
	r := NewRedactor()
	r.AddSecret("my-api-key", "super-secret-value-12345")

	// Non-user text (LLM responses, tool outputs) should be redacted normally.
	input := "The key is super-secret-value-12345"
	got := r.RedactNonUser(input, false)
	want := "The key is [REDACTED]"
	if got != want {
		t.Errorf("RedactNonUser(isUserAuthored=false) should redact\ngot:  %q\nwant: %q", got, want)
	}
}

func TestRedactNonUser_NilRedactor(t *testing.T) {
	// Verify the method is safe to call on a properly initialized but empty Redactor.
	r := NewRedactor()
	input := "some text with no secrets"
	got := r.RedactNonUser(input, false)
	if got != input {
		t.Errorf("RedactNonUser with no signatures should return text unchanged\ngot:  %q\nwant: %q", got, input)
	}
}
