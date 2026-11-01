package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/provider/llmerror"
	openailib "github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
)

// Exercise the SDK's non-JSON error path, not just a hand-built LLMError.
func TestExpiredJWTWithoutRenewalIsTerminal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			calls := 0
			const body = "Jwt is expired: Jwt is expired"
			cfg := openailib.DefaultConfig("test")
			cfg.HTTPClient = &http.Client{Transport: safetyTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized", Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			p := NewProvider(openailib.NewClientWithConfig(cfg), "test", false)
			var firstErr error
			for resp, err := range p.GenerateContent(context.Background(), &model.LLMRequest{}, stream) {
				if resp != nil {
					t.Error("rejected request emitted output")
				}
				firstErr = err
			}
			if llmerror.IsRetryable(firstErr) || llmerror.StatusCode(firstErr) != http.StatusUnauthorized || !llmerror.IsAuthError(firstErr) {
				t.Errorf("expired JWT without renewal must be terminal authentication failure: %v", firstErr)
			}
			if calls != 1 {
				t.Errorf("rejected request was automatically replayed: calls=%d", calls)
			}
			var llmErr *llmerror.LLMError
			if !errors.As(firstErr, &llmErr) || llmErr.Body != body || !strings.Contains(firstErr.Error(), body) {
				t.Fatalf("expired JWT lost response body diagnostics: %v", firstErr)
			}
		})
	}
}
