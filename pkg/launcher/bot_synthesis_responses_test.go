package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	openaiadapter "github.com/SAP/astonish/pkg/provider/openai"
	"github.com/SAP/astonish/pkg/store"
	openai "github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Exercise the actual Responses wire decoder, budget wrapper and synthesis gate.
// A scripted LLM with hand-written completion flags would hide this regression.
func TestBotSynthesisResponsesCompletedWire(t *testing.T) {
	for _, health := range []bool{false, true} {
		t.Run(fmt.Sprintf("health=%v", health), func(t *testing.T) {
			answer := strings.Repeat("Design from retained evidence. ", 1300)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if r.URL.Path != "/responses" || (request["stream"] == true) != health {
					t.Errorf("unexpected transport: %s stream=%v", r.URL.Path, request["stream"])
				}
				if tools, ok := request["tools"].([]any); ok && len(tools) > 0 {
					t.Error("synthesis exposed tools")
				}
				response := map[string]any{"id": "resp_final", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": answer}}}}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 5, "total_tokens": 25}}
				if health {
					w.Header().Set("Content-Type", "text/event-stream")
					raw, _ := json.Marshal(map[string]any{"type": "response.completed", "response": response})
					fmt.Fprintf(w, "data: %s\n\n", raw)
					w.(http.Flusher).Flush()
				} else {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(response)
				}
			}))
			defer server.Close()
			f := newBotFactoryFixture(t)
			spec := store.BudgetSpec{MaxRuntimeSeconds: 300}
			if health {
				spec.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
			}
			budget, err := bot.NewRunBudget(spec, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			f.req.Budget = budget
			cfg := openai.DefaultConfig("fixture")
			cfg.BaseURL = server.URL
			native := openaiadapter.NewProviderWithResponses(openai.NewClientWithConfig(cfg), "gpt-6-astra", true, 8192)
			f.cfg.WrapModel = func(_ context.Context, _ model.LLM, b execution.RunBudget) (model.LLM, error) {
				return provider.NewBudgetLLM(native, b, provider.BudgetLLMOptions{MaxOutputTokens: 8192, SupportsOutputLimit: true})
			}
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			llm := botFactoryModelForTest(t, resources.Agent.LLM)
			llm.binding.synthesisReserve.final = true
			count := 0
			for response, err := range llm.GenerateContent(f.ctx, &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("Deliver the design.", "user")}}, false) {
				if err != nil {
					t.Fatalf("completed upstream answer rejected: %v", err)
				}
				count++
				if response == nil || !response.TurnComplete || response.FinishReason != genai.FinishReasonStop || response.Content.Parts[0].Text != answer {
					t.Fatal("complete answer/metadata lost")
				}
			}
			if count != 1 || calls.Load() != 1 {
				t.Fatalf("published=%d calls=%d; no correction necessary", count, calls.Load())
			}
			if u := budget.Usage(); u.InputTokens != 20 || u.OutputTokens != 5 {
				t.Fatalf("usage lost: %+v", u)
			}
		})
	}
}
