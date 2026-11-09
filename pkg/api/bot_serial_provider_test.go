package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/litellm"
	openaiadapter "github.com/SAP/astonish/pkg/provider/openai"
	"github.com/SAP/astonish/pkg/provider/openai_compat"
	"github.com/SAP/astonish/pkg/store"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// This is a controlled HTTP provider, not a replay of a discarded live response.
func TestBotApprovalCaptureSerialProviderWire(t *testing.T) {
	for _, adapter := range []string{"openai", "litellm", "compatible", "responses", "compatible-responses"} {
		for _, malicious := range []bool{false, true} {
			t.Run(adapter+map[bool]string{false: "/single", true: "/violating-batch"}[malicious], func(t *testing.T) {
				requests := 0
				checkpointed := true
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodGet && r.URL.Path == "/openai/v1/models" {
						_, _ = w.Write([]byte(`{"data":[{"id":"gpt-6-test"}]}`))
						return
					}
					requests++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					if checkpointed {
						if value, ok := body["parallel_tool_calls"]; !ok || value != false {
							t.Errorf("checkpoint request parallel_tool_calls = %v (present=%v), want explicit false", value, ok)
						}
						if body["stream"] == true {
							t.Error("checkpoint must request non-streaming output")
						}
					} else if _, ok := body["parallel_tool_calls"]; ok {
						t.Error("checkpoint setting leaked into ordinary Chat request")
					}
					if adapter == "responses" || adapter == "compatible-responses" {
						calls := []any{map[string]any{"type": "function_call", "call_id": "call-one", "name": "remember", "arguments": `{"unit":"Celsius"}`}}
						if malicious {
							calls = append(calls, map[string]any{"type": "function_call", "call_id": "call-two", "name": "remember", "arguments": `{"unit":"Fahrenheit"}`})
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"id": "response-one", "status": "completed", "output": calls})
						return
					}
					calls := []any{map[string]any{"id": "call-one", "type": "function", "function": map[string]any{"name": "remember", "arguments": `{"unit":"Celsius"}`}}}
					if malicious {
						calls = append(calls, map[string]any{"id": "call-two", "type": "function", "function": map[string]any{"name": "remember", "arguments": `{"unit":"Fahrenheit"}`}})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "chat-one", "choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": calls}}}})
				}))
				defer srv.Close()
				cfg := openai.DefaultConfig("test-key")
				cfg.BaseURL = srv.URL + "/v1"
				var inner model.LLM
				switch adapter {
				case "litellm":
					inner = litellm.NewProvider("test-key", srv.URL, "test-model")
				case "compatible":
					inner = openai_compat.NewProvider(t.Context(), "test-key", srv.URL, "test-model", false)
				case "compatible-responses":
					inner = openai_compat.NewProvider(t.Context(), "test-key", srv.URL+"/litellm/v1", "gpt-6-test", false)
				case "responses":
					inner = openaiadapter.NewOpenAIProvider(openai.NewClientWithConfig(cfg), "gpt-6-test", true)
				default:
					inner = openaiadapter.NewOpenAIProvider(openai.NewClientWithConfig(cfg), "test-model", true)
				}
				req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("Remember Celsius", genai.RoleUser)}, Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "remember", ParametersJsonSchema: map[string]any{"type": "object", "properties": map[string]any{"unit": map[string]any{"type": "string"}}}}}}}}}
				before, _ := json.Marshal(req)
				capture := &botApprovalCapture{}
				// Production composition includes a per-run budget and may use a
				// swappable shared provider. Both must preserve the request extension.
				budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				budgeted, err := provider.NewBudgetLLM(provider.NewSwappableLLM(inner), budget, provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true})
				if err != nil {
					t.Fatal(err)
				}
				wrapped := &botApprovalCaptureModel{LLM: budgeted, capture: capture}
				responses := 0
				var gotErr error
				for response, err := range wrapped.GenerateContent(context.Background(), req, true) {
					if err != nil {
						gotErr = err
						continue
					}
					responses++
					if malicious {
						t.Fatal("malicious batch escaped checkpoint validation")
					}
					call := response.Content.Parts[0].FunctionCall
					if call == nil || call.ID != "call-one" || call.Name != "remember" || !reflect.DeepEqual(call.Args, map[string]any{"unit": "Celsius"}) {
						t.Fatalf("exact single action changed: %#v", call)
					}
				}
				if malicious {
					if !errors.Is(gotErr, store.ErrBotUnsupported) || responses != 0 {
						t.Fatalf("batch result: responses=%d err=%v", responses, gotErr)
					}
				} else if gotErr != nil || responses != 1 {
					t.Fatalf("single result: responses=%d err=%v", responses, gotErr)
				}
				after, _ := json.Marshal(req)
				if string(before) != string(after) {
					t.Fatal("capture mutated caller request")
				}
				// Reuse the same provider and request without checkpoint capture.
				checkpointed = false
				for _, err := range inner.GenerateContent(t.Context(), req, false) {
					if err != nil {
						t.Fatal(err)
					}
				}
				if requests != 2 {
					t.Fatalf("generation requests=%d", requests)
				}
				// Once approval exists no further provider generation is allowed.
				capture.approval = &store.BotApproval{}
				for response, err := range wrapped.GenerateContent(t.Context(), req, false) {
					if response != nil || err == nil {
						t.Fatal("pending checkpoint did not block generation")
					}
				}
				if requests != 2 {
					t.Fatal("pending checkpoint contacted provider")
				}
			})
		}
	}
}
