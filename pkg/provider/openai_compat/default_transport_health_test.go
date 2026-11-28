package openai_compat_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/provider/litellm"
	"github.com/SAP/astonish/pkg/provider/openai_compat"
	"github.com/SAP/astonish/pkg/provider/streaming"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Exercise public constructors, not SDK clients with a test-injected transport.
func TestDefaultClientsGenerationHealth(t *testing.T) {
	for _, variant := range []struct {
		name                   string
		responses, debug, lite bool
	}{
		{name: "compatible-chat"},
		{name: "compatible-chat-debug", debug: true},
		{name: "compatible-responses", responses: true},
		{name: "compatible-responses-debug", responses: true, debug: true},
		{name: "litellm-chat", lite: true},
	} {
		for _, scenario := range []string{"keepalive", "idle", "cancel"} {
			t.Run(variant.name+"/"+scenario, func(t *testing.T) {
				started := make(chan struct{})
				disconnected := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer fixture" {
						t.Errorf("authorization lost: %q", r.Header.Get("Authorization"))
					}
					if r.Method == http.MethodGet && r.URL.Path == "/openai/v1/models" {
						fmt.Fprint(w, `{"data":[{"id":"gpt-6"}]}`)
						return
					}
					defer close(disconnected)
					wantPath := "/litellm/v1/chat/completions"
					if variant.responses {
						wantPath = "/openai/v1/responses"
					}
					if r.URL.Path != wantPath {
						t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
					}
					var request map[string]any
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					if request["stream"] != true {
						t.Error("generation was not streaming")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					w.(http.Flusher).Flush()
					close(started)
					if scenario != "keepalive" {
						<-r.Context().Done()
						return
					}
					// Only real SSE comments arrive for longer than the idle budget.
					for i := 0; i < 20; i++ {
						fmt.Fprint(w, ": upstream keepalive\n\n")
						w.(http.Flusher).Flush()
						select {
						case <-r.Context().Done():
							return
						case <-time.After(20 * time.Millisecond):
						}
					}
					if variant.responses {
						fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}}`+"\n\n")
					} else {
						fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
					}
				}))
				defer server.Close()
				var provider model.LLM
				if variant.lite {
					provider = litellm.NewProvider("fixture", server.URL+"/litellm", "test-model")
				} else {
					modelName := "test-model"
					if variant.responses {
						modelName = "gpt-6"
					}
					provider = openai_compat.NewProvider(context.Background(), "fixture", server.URL+"/litellm", modelName, variant.debug)
				}
				parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				ctx, stop := streaming.StartHealthMonitor(parent, streaming.HealthOptions{IdleTimeout: 200 * time.Millisecond, CheckInterval: 5 * time.Millisecond})
				defer stop()
				if scenario == "cancel" {
					go func() {
						select {
						case <-started:
							cancel()
						case <-parent.Done():
						}
					}()
				}
				var got error
				finals, partials := 0, 0
				for response, err := range provider.GenerateContent(ctx, &model.LLMRequest{Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "test"}}}}}, !variant.responses) {
					if err != nil {
						got = err
						continue
					}
					if response.Partial {
						partials++
					} else {
						finals++
					}
					if response.Content == nil || len(response.Content.Parts) != 1 || response.Content.Parts[0].Text != "done" {
						t.Errorf("fabricated or missing response content: %+v", response)
					}
				}
				cause := stop()
				switch scenario {
				case "keepalive":
					wantPartials := 1
					if variant.responses {
						wantPartials = 0
					}
					if got != nil || cause != nil || finals != 1 || partials != wantPartials {
						t.Fatalf("keepalive: err=%v cause=%v finals=%d partials=%d", got, cause, finals, partials)
					}
				case "idle":
					var stalled *streaming.GenerationStalledError
					if got == nil || !errors.As(cause, &stalled) || finals+partials != 0 {
						t.Fatalf("idle: err=%v cause=%v responses=%d", got, cause, finals+partials)
					}
				case "cancel":
					if got == nil || !errors.Is(cause, context.Canceled) || finals+partials != 0 {
						t.Fatalf("cancel: err=%v cause=%v responses=%d", got, cause, finals+partials)
					}
				}
				select {
				case <-disconnected:
				case <-time.After(time.Second):
					t.Fatal("upstream handler not released")
				}
			})
		}
	}
}
