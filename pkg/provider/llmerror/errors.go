package llmerror

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// InterruptedStreamError marks a failure after model output has been delivered.
// Replaying the request is unsafe, even when the underlying cause is retryable.
// The cause remains available through errors.Is and errors.As for diagnostics.
type InterruptedStreamError struct {
	cause error
}

func (e *InterruptedStreamError) Error() string {
	return fmt.Sprintf("interrupted stream after model output: %v", e.cause)
}

func (e *InterruptedStreamError) Unwrap() error { return e.cause }

// NewInterruptedStream marks err as non-replayable after visible model output.
// A nil cause returns nil.
func NewInterruptedStream(err error) error {
	if err == nil {
		return nil
	}
	return &InterruptedStreamError{cause: err}
}

// LLMError is a structured error returned by LLM providers when an API call
// fails. It preserves the HTTP status code and provider name so callers can
// make retry/display decisions without parsing error strings.
type LLMError struct {
	StatusCode int           // HTTP status code (429, 400, 500, etc.)
	Provider   string        // Provider name ("anthropic", "openai", etc.)
	Message    string        // Human-readable error message
	Body       string        // Raw response body (for debugging)
	Retryable  bool          // Whether this error is worth retrying
	RetryAfter time.Duration // Suggested wait before retry (from Retry-After header)
}

func (e *LLMError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	// resp.Status is often "400 Bad Request" — avoid "400 400 Bad Request".
	msg = strings.TrimPrefix(msg, strconv.Itoa(e.StatusCode)+" ")
	base := fmt.Sprintf("%s: %d %s", e.Provider, e.StatusCode, msg)
	if detail := truncateErrorBody(e.Body); detail != "" {
		return base + ": " + detail
	}
	return base
}

// truncateErrorBody returns a short, single-line snippet of the provider
// response body so callers see the real rejection reason (e.g. Gemini
// INVALID_ARGUMENT details) instead of only the HTTP status text.
func truncateErrorBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" || strings.HasPrefix(body, "<unreadable:") {
		return ""
	}
	body = strings.ReplaceAll(body, "\n", " ")
	body = strings.Join(strings.Fields(body), " ")
	const maxLen = 400
	if len(body) > maxLen {
		return body[:maxLen] + "..."
	}
	return body
}

// NewLLMError creates an LLMError with automatic retryable classification.
func NewLLMError(provider string, statusCode int, message, body string) *LLMError {
	return &LLMError{
		StatusCode: statusCode,
		Provider:   provider,
		Message:    message,
		Body:       body,
		Retryable:  isRetryableStatus(statusCode) || isExpiredTokenResponse(statusCode, message, body),
	}
}

// NewFromResponse creates an LLMError from an HTTP response, parsing
// the Retry-After header if present.
func NewFromResponse(provider string, resp *http.Response, body []byte) *LLMError {
	e := NewLLMError(provider, resp.StatusCode, resp.Status, string(body))
	e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	return e
}

// IsRetryable returns true if the error is an LLMError that should be retried.
// This includes 429 (rate limit), 500 (server error / proxy failure),
// 502/503 (server overload), 504 (timeout), and 408 (request timeout).
// Explicit token-expiry 401s also allow recovery within the caller's retry budget.
// An interrupted stream is never replayable, regardless of its underlying cause.
func IsRetryable(err error) bool {
	var interrupted *InterruptedStreamError
	if errors.As(err, &interrupted) {
		return false
	}
	var llmErr *LLMError
	if errors.As(err, &llmErr) {
		return llmErr.Retryable
	}
	return false
}

// IsRateLimited returns true if the error is specifically a 429 rate limit.
func IsRateLimited(err error) bool {
	var llmErr *LLMError
	if errors.As(err, &llmErr) {
		return llmErr.StatusCode == http.StatusTooManyRequests
	}
	return false
}

// IsAuthError returns true if the error is a 401 or 403 authentication failure.
func IsAuthError(err error) bool {
	var llmErr *LLMError
	if errors.As(err, &llmErr) {
		return llmErr.StatusCode == http.StatusUnauthorized || llmErr.StatusCode == http.StatusForbidden
	}
	return false
}

// IsServerError returns true if the error is a 5xx server error.
func IsServerError(err error) bool {
	var llmErr *LLMError
	if errors.As(err, &llmErr) {
		return llmErr.StatusCode >= 500
	}
	return false
}

// IsContextOverflow returns true if the error looks like a context-window
// overflow. Providers return 400 for many reasons (invalid schema, bad
// arguments, etc.), so we require overflow-related keywords in the body or
// message — a bare 400 is not enough. Interrupted streams return false because
// context-overflow recovery must not replay output already delivered.
func IsContextOverflow(err error) bool {
	var interrupted *InterruptedStreamError
	if errors.As(err, &interrupted) {
		return false
	}
	var llmErr *LLMError
	if !errors.As(err, &llmErr) || llmErr.StatusCode != http.StatusBadRequest {
		return false
	}
	haystack := strings.ToLower(llmErr.Message + " " + llmErr.Body)
	for _, kw := range []string{
		"context length",
		"context window",
		"maximum context",
		"too many tokens",
		"token limit",
		"prompt is too long",
		"input is too long",
		"exceeds the maximum",
		"max_tokens",
		"context_length_exceeded",
	} {
		if strings.Contains(haystack, kw) {
			return true
		}
	}
	return false
}

// GetRetryAfter extracts the RetryAfter duration from an LLMError, if present.
func GetRetryAfter(err error) time.Duration {
	var llmErr *LLMError
	if errors.As(err, &llmErr) {
		return llmErr.RetryAfter
	}
	return 0
}

// StatusCode extracts the HTTP status code from an LLMError. Returns 0 if
// the error is not an LLMError.
func StatusCode(err error) int {
	var llmErr *LLMError
	if errors.As(err, &llmErr) {
		return llmErr.StatusCode
	}
	return 0
}

// isExpiredTokenResponse permits bounded recovery when a provider or proxy
// rejects a stale token. It does not refresh credentials itself; permanent
// expiry will still exhaust the caller's retry budget. Other 401s and all 403s
// remain non-retryable. Match explicit expiry phrases, not unrelated expiry.
func isExpiredTokenResponse(code int, message, body string) bool {
	if code != http.StatusUnauthorized {
		return false
	}
	text := strings.ToLower(strings.Join(strings.Fields(message+" "+body), " "))
	for _, phrase := range []string{
		"jwt is expired", "jwt expired", "jwt has expired",
		"token is expired", "token expired", "token has expired",
		"expired jwt", "expired token", "token_expired", "expired_token",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// isRetryableStatus classifies HTTP status codes as retryable or not.
// 500 is included because LLM provider proxies (e.g., Bifrost) commonly
// return 500 for transient upstream failures (timeouts, connection resets)
// that succeed on retry. The retry budget (3 attempts with backoff) limits
// the cost of retrying genuine server bugs.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout,      // 504
		http.StatusRequestTimeout:      // 408
		return true
	default:
		return false
	}
}

// parseRetryAfter parses the Retry-After header value. It supports both
// delay-seconds (e.g., "30") and HTTP-date formats.
func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}

	// Try as seconds first (most common for rate limiting)
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	// Try as HTTP-date
	if t, err := http.ParseTime(value); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}

	return 0
}
