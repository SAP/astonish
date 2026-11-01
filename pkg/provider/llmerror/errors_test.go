package llmerror

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInterruptedStreamClassification(t *testing.T) {
	for _, code := range []int{408, 429, 500, 502, 503, 504, 400} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			cause := NewLLMError("test", code, "context length exceeded", "diagnostic body")
			cause.RetryAfter = time.Second
			if code == 400 && !IsContextOverflow(cause) || code != 400 && !IsRetryable(cause) {
				t.Fatal("unmarked cause must retain recovery classification")
			}
			interrupted := NewInterruptedStream(cause)
			for _, err := range []error{interrupted, fmt.Errorf("outer: %w", interrupted), errors.Join(cause, interrupted)} {
				if IsRetryable(err) || IsContextOverflow(err) {
					t.Fatalf("interrupted output must not replay: %v", err)
				}
				var marker *InterruptedStreamError
				var gotCause *LLMError
				if !errors.As(err, &marker) || !errors.As(err, &gotCause) || gotCause != cause || !errors.Is(err, cause) {
					t.Fatalf("lost marker or cause: %v", err)
				}
				if StatusCode(err) != code || GetRetryAfter(err) != time.Second || !strings.Contains(err.Error(), cause.Error()) {
					t.Fatalf("lost diagnostics: %v", err)
				}
			}
		})
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		err := NewInterruptedStream(cause)
		if !errors.Is(err, cause) || IsRetryable(err) || IsContextOverflow(err) {
			t.Fatalf("cancellation identity changed: %v", err)
		}
	}
}

func TestNewInterruptedStreamNil(t *testing.T) {
	if err := NewInterruptedStream(nil); err != nil {
		t.Fatalf("nil cause produced an error: %v", err)
	}
}

func TestNewLLMError(t *testing.T) {
	tests := []struct {
		name       string
		code       int
		retryable  bool
		wantStatus int
	}{
		{"rate_limit", 429, true, 429},
		{"bad_gateway", 502, true, 502},
		{"service_unavailable", 503, true, 503},
		{"gateway_timeout", 504, true, 504},
		{"request_timeout", 408, true, 408},
		{"bad_request", 400, false, 400},
		{"unauthorized", 401, false, 401},
		{"forbidden", 403, false, 403},
		{"not_found", 404, false, 404},
		{"internal_server_error", 500, true, 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewLLMError("test", tt.code, "test message", "")
			if e.StatusCode != tt.wantStatus {
				t.Errorf("StatusCode = %d, want %d", e.StatusCode, tt.wantStatus)
			}
			if e.Retryable != tt.retryable {
				t.Errorf("Retryable = %v, want %v", e.Retryable, tt.retryable)
			}
			if e.Provider != "test" {
				t.Errorf("Provider = %q, want %q", e.Provider, "test")
			}
		})
	}
}

func TestExpiredTokenRequiresCredentialRecovery(t *testing.T) {
	tests := []struct {
		name, message, body string
		code                int
	}{
		{"reported_plain_text", "invalid character 'J' looking for beginning of value", "Jwt is expired: Jwt is expired", 401},
		{"jwt_message", "JWT expired", "", 401},
		{"json_code", "Unauthorized", `{"error":{"code":"token_expired"}}`, 401},
		{"access_token", "The access token has expired", "", 401},
		{"invalid_key", "Invalid API key", "", 401},
		{"bare_unauthorized", "Unauthorized", "", 401},
		{"unrelated_expiry", "Subscription has expired", "", 401},
		{"forbidden", "JWT is expired", "", 403},
		{"bad_request", "JWT is expired", "", 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewLLMError("test", tt.code, tt.message, tt.body)
			wrapped := fmt.Errorf("provider: %w", err)
			if err.Retryable || IsRetryable(wrapped) {
				t.Errorf("must not retry without credential recovery: %v", err)
			}
			if IsAuthError(wrapped) != (tt.code == 401 || tt.code == 403) {
				t.Errorf("authentication classification changed: %v", err)
			}
			if StatusCode(wrapped) != tt.code || err.Message != tt.message || err.Body != tt.body || err.Provider != "test" {
				t.Errorf("lost diagnostics: %+v", err)
			}
			if IsRetryable(NewInterruptedStream(err)) {
				t.Fatal("must not replay after output")
			}
		})
	}
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized", Header: http.Header{"Retry-After": []string{"30"}}}
	const body = "Jwt is expired: Jwt is expired"
	err := NewFromResponse("test", resp, []byte(body))
	if IsRetryable(err) || !IsAuthError(err) {
		t.Errorf("HTTP expiry response requires credential recovery: %v", err)
	}
	if err.Body != body || !strings.Contains(err.Error(), body) || GetRetryAfter(err) != 30*time.Second {
		t.Fatalf("HTTP expiry response lost diagnostics: %+v", err)
	}
}

func TestLLMErrorString(t *testing.T) {
	e := NewLLMError("anthropic", 429, "rate limited", "")
	got := e.Error()
	want := "anthropic: 429 rate limited"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	// Empty message falls back to http.StatusText
	e2 := NewLLMError("openai", 500, "", "")
	got2 := e2.Error()
	want2 := "openai: 500 Internal Server Error"
	if got2 != want2 {
		t.Errorf("Error() = %q, want %q", got2, want2)
	}

	// NewFromResponse uses resp.Status ("400 Bad Request") — don't double the code.
	e3 := NewLLMError("sap-vertex", 400, "400 Bad Request", `{"error":{"message":"Unknown name \"additionalProperties\""}}`)
	got3 := e3.Error()
	if strings.Contains(got3, "400 400") {
		t.Errorf("Error() duplicated status code: %q", got3)
	}
	if !strings.Contains(got3, "additionalProperties") {
		t.Errorf("Error() should include body detail, got %q", got3)
	}
}

func TestIsContextOverflow(t *testing.T) {
	if IsContextOverflow(NewLLMError("sap-vertex", 400, "400 Bad Request", `{"error":"Unknown name additionalProperties"}`)) {
		t.Error("schema 400 should not be treated as context overflow")
	}
	if !IsContextOverflow(NewLLMError("openai", 400, "Bad Request", `{"error":{"code":"context_length_exceeded"}}`)) {
		t.Error("context_length_exceeded should be treated as overflow")
	}
	if IsContextOverflow(NewLLMError("openai", 429, "rate limited", "too many tokens")) {
		t.Error("non-400 should not be overflow even with keywords")
	}
}

func TestErrorsAsChain(t *testing.T) {
	original := NewLLMError("test", 429, "rate limited", "")
	wrapped := fmt.Errorf("llm call failed: %w", original)
	doubleWrapped := fmt.Errorf("agent error: %w", wrapped)

	// Should work through any depth of wrapping
	if !IsRetryable(doubleWrapped) {
		t.Error("IsRetryable should be true through wrapped errors")
	}
	if !IsRateLimited(doubleWrapped) {
		t.Error("IsRateLimited should be true through wrapped errors")
	}
	if StatusCode(doubleWrapped) != 429 {
		t.Errorf("StatusCode = %d, want 429", StatusCode(doubleWrapped))
	}
}

func TestClassificationHelpers(t *testing.T) {
	tests := []struct {
		name        string
		code        int
		isRetryable bool
		isRateLimit bool
		isAuth      bool
		isServer    bool
	}{
		{"429", 429, true, true, false, false},
		{"502", 502, true, false, false, true},
		{"503", 503, true, false, false, true},
		{"504", 504, true, false, false, true},
		{"401", 401, false, false, true, false},
		{"403", 403, false, false, true, false},
		{"500", 500, true, false, false, true},
		{"400", 400, false, false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewLLMError("test", tt.code, "msg", "")
			if got := IsRetryable(err); got != tt.isRetryable {
				t.Errorf("IsRetryable = %v, want %v", got, tt.isRetryable)
			}
			if got := IsRateLimited(err); got != tt.isRateLimit {
				t.Errorf("IsRateLimited = %v, want %v", got, tt.isRateLimit)
			}
			if got := IsAuthError(err); got != tt.isAuth {
				t.Errorf("IsAuthError = %v, want %v", got, tt.isAuth)
			}
			if got := IsServerError(err); got != tt.isServer {
				t.Errorf("IsServerError = %v, want %v", got, tt.isServer)
			}
		})
	}
}

func TestNonLLMErrorReturnsDefaults(t *testing.T) {
	err := errors.New("some random error")

	if IsRetryable(err) {
		t.Error("IsRetryable should be false for non-LLMError")
	}
	if IsRateLimited(err) {
		t.Error("IsRateLimited should be false for non-LLMError")
	}
	if IsAuthError(err) {
		t.Error("IsAuthError should be false for non-LLMError")
	}
	if IsServerError(err) {
		t.Error("IsServerError should be false for non-LLMError")
	}
	if StatusCode(err) != 0 {
		t.Errorf("StatusCode = %d, want 0 for non-LLMError", StatusCode(err))
	}
	if GetRetryAfter(err) != 0 {
		t.Error("GetRetryAfter should be 0 for non-LLMError")
	}
}

func TestNilErrorReturnsDefaults(t *testing.T) {
	if IsRetryable(nil) {
		t.Error("IsRetryable(nil) should be false")
	}
	if StatusCode(nil) != 0 {
		t.Error("StatusCode(nil) should be 0")
	}
}

func TestNewFromResponse(t *testing.T) {
	// Create a fake HTTP response with Retry-After header
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body := []byte(`{"error":"rate limited"}`)
	llmErr := NewFromResponse("anthropic", resp, body)

	if llmErr.StatusCode != 429 {
		t.Errorf("StatusCode = %d, want 429", llmErr.StatusCode)
	}
	if !llmErr.Retryable {
		t.Error("should be retryable")
	}
	if llmErr.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", llmErr.RetryAfter)
	}
	if llmErr.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", llmErr.Provider, "anthropic")
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"empty", "", 0},
		{"seconds", "30", 30 * time.Second},
		{"zero_seconds", "0", 0},
		{"negative_seconds", "-5", 0},
		{"invalid", "not-a-number", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetryAfter(tt.value)
			if got != tt.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestGetRetryAfter(t *testing.T) {
	e := &LLMError{
		StatusCode: 429,
		RetryAfter: 15 * time.Second,
	}
	if got := GetRetryAfter(e); got != 15*time.Second {
		t.Errorf("GetRetryAfter = %v, want 15s", got)
	}

	// Through wrapping
	wrapped := fmt.Errorf("wrapped: %w", e)
	if got := GetRetryAfter(wrapped); got != 15*time.Second {
		t.Errorf("GetRetryAfter(wrapped) = %v, want 15s", got)
	}
}
