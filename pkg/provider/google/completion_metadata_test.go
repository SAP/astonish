package google

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/genai"
)

// Exercise the actual ADK Gemini decoder, not a mock that invents metadata.
func TestNativeGeminiTerminationMetadata(t *testing.T) {
	for _, reason := range []genai.FinishReason{genai.FinishReasonStop, genai.FinishReasonMaxTokens, genai.FinishReasonSafety} {
		t.Run(string(reason), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"answer"}]},"finishReason":%q}]}`, reason)
			}))
			defer server.Close()
			inner, err := gemini.NewModel(context.Background(), "gemini-test", &genai.ClientConfig{APIKey: "local-fixture", HTTPOptions: genai.HTTPOptions{BaseURL: server.URL}})
			if err != nil {
				t.Fatal(err)
			}
			p := &Provider{model: inner}
			count := 0
			for r, err := range p.GenerateContent(context.Background(), &model.LLMRequest{Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "hello"}}}}}, false) {
				if err != nil {
					t.Fatal(err)
				}
				count++
				if r.FinishReason != reason {
					t.Fatalf("finish=%s want=%s", r.FinishReason, reason)
				}
			}
			if count != 1 {
				t.Fatalf("responses=%d", count)
			}
		})
	}
}
