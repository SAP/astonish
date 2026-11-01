package openai

import (
	"context"
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
func TestExpiredJWTRequestCanRecover(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			calls := 0
			cfg := openailib.DefaultConfig("test")
			cfg.HTTPClient = &http.Client{Transport: safetyTransport(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return &http.Response{StatusCode: 401, Status: "401 Unauthorized", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("Jwt is expired: Jwt is expired"))}, nil
				}
				body := `{"choices":[{"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}]}`
				contentType := "application/json"
				if stream {
					body = safetyChat(map[string]any{"content": "recovered"}, "stop") + "data: [DONE]\n\n"
					contentType = "text/event-stream"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			p := NewProvider(openailib.NewClientWithConfig(cfg), "test", false)
			req := &model.LLMRequest{}
			var firstErr error
			for resp, err := range p.GenerateContent(context.Background(), req, stream) {
				if resp != nil {
					t.Fatal("rejected request emitted output")
				}
				firstErr = err
			}
			if !llmerror.IsRetryable(firstErr) || llmerror.StatusCode(firstErr) != 401 {
				t.Fatalf("expired JWT must allow retry: %v", firstErr)
			}
			// The agent's retry loop uses this classification to issue the same request.
			var final *model.LLMResponse
			for resp, err := range p.GenerateContent(context.Background(), req, stream) {
				if err != nil {
					t.Fatal(err)
				}
				if resp != nil && !resp.Partial {
					final = resp
				}
			}
			if calls != 2 || final == nil || final.Content == nil || len(final.Content.Parts) != 1 || final.Content.Parts[0].Text != "recovered" {
				t.Fatalf("same request did not recover: calls=%d response=%v", calls, final)
			}
		})
	}
}
