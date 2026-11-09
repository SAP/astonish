package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/anthropic"
	"github.com/SAP/astonish/pkg/provider/httpool"
	"github.com/SAP/astonish/pkg/provider/sap"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// Direct Anthropic has a fixed URL and no public client option. Route its shared
// transport's TLS dial to an owned plaintext loopback listener, preserving the
// actual adapter's request path, headers, serializer and response decoder. No
// provider internals, DNS, credentials, external endpoint or proxy are used.
// This test and its children must remain serial while the dial hook is installed.
func botNativeAnthropicLoopback(t *testing.T, server *httptest.Server) {
	t.Helper()
	transport := httpool.Transport().(*http.Transport)
	old := transport.DialTLSContext
	transport.CloseIdleConnections()
	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "api.anthropic.com:443" {
			return nil, fmt.Errorf("unexpected native fixture TLS destination: %s", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		transport.DialTLSContext = old
	})
}

// Composes production Bot runtime -> checkpoint capture -> budget -> swappable
// provider -> native SAP/Anthropic adapter -> owned HTTP wire, then runs actual
// ADK tool callbacks. The fixture returns one call per turn, or deliberately
// violates the serial hint. These are new controlled responses, not a replay.
func TestBotApprovalCaptureNativeSerialProviderWire(t *testing.T) {
	for _, adapterName := range []string{"sap-claude", "direct-anthropic"} {
		for _, scenario := range []string{"serial-two-then-text", "batch-first", "batch-after-read"} {
			t.Run(adapterName+"/"+scenario, func(t *testing.T) {
				ctx, req, ss := botChatTestRequest(t)
				ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				approvals := &resumeTestStore{}
				ctx = bot.WithApprovalStore(ctx, approvals)
				ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
				var cleaned, reads, sends, generations, authCalls, deployments atomic.Int64
				var ordinary atomic.Bool
				ordinary.Store(true)
				bodies := make(chan map[string]any, 16)
				const finalText = "Observed both native serial receipts."
				call := func(id, name, path string) map[string]any {
					return map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{"path": path}}
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/oauth/token":
						authCalls.Add(1)
						if err := r.ParseForm(); err != nil || r.Method != http.MethodPost || r.Form.Get("client_id") != "fixture-id" || r.Form.Get("client_secret") != "fixture-secret" || r.Form.Get("grant_type") != "client_credentials" {
							t.Error("invalid fixture auth exchange")
						}
						_, _ = w.Write([]byte(`{"access_token":"fixture-token","expires_in":3600}`))
						return
					case "/v2/lm/deployments":
						deployments.Add(1)
						if r.Method != http.MethodGet || r.URL.Query().Get("status") != "RUNNING" || r.URL.Query().Get("resourceGroup") != "fixture-group" || r.Header.Get("Authorization") != "Bearer fixture-token" {
							t.Error("invalid deployment discovery request")
						}
						_, _ = w.Write([]byte(`{"resources":[{"id":"native-fixture","status":"RUNNING","details":{"resources":{"backendDetails":{"model":{"name":"anthropic--claude-4.6-sonnet"}}}}}]}`))
						return
					}
					path := "/v1/messages"
					if adapterName == "sap-claude" {
						path = "/v2/inference/deployments/native-fixture/invoke"
						if r.Header.Get("AI-Resource-Group") != "fixture-group" || r.Header.Get("Authorization") != "Bearer fixture-token" {
							t.Error("lost SAP auth/resource group")
						}
					} else if r.Header.Get("x-api-key") != "fixture-key" || r.Header.Get("anthropic-version") != "2023-06-01" {
						t.Error("lost native Anthropic headers")
					}
					if r.Method != http.MethodPost || r.URL.Path != path {
						t.Errorf("unexpected generation route %s %s", r.Method, r.URL.Path)
						http.Error(w, "unexpected route", http.StatusBadRequest)
						return
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						http.Error(w, "invalid JSON", http.StatusBadRequest)
						return
					}
					bodies <- body
					if _, exists := body["parallel_tool_calls"]; exists {
						t.Error("OpenAI option leaked onto native wire")
					}
					content := []any{map[string]any{"type": "text", "text": finalText}}
					stop := "end_turn"
					if ordinary.Load() {
						if _, exists := body["tool_choice"]; exists {
							t.Error("checkpoint option leaked into ordinary Chat")
						}
					} else {
						n := generations.Add(1)
						if reads.Load() != n-1 || sends.Load() != 0 {
							t.Errorf("generation %d started before exactly %d completed reads: reads=%d sends=%d", n, n-1, reads.Load(), sends.Load())
						}
						want := map[string]any{"type": "auto", "disable_parallel_tool_use": true}
						if !reflect.DeepEqual(body["tool_choice"], want) || body["stream"] == true {
							t.Errorf("checkpoint native request: choice=%v stream=%v", body["tool_choice"], body["stream"])
						}
						if scenario == "batch-first" || (scenario == "batch-after-read" && n == 2) {
							content = []any{call("reject-read", "read_file", "rejected-secret"), call("reject-send", "send_email", "rejected-secret")}
							stop = "tool_use"
						} else if n <= 2 {
							content = []any{call(fmt.Sprintf("native-%d", n), "read_file", fmt.Sprintf("receipt-%d", n))}
							stop = "tool_use"
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "native-message", "type": "message", "role": "assistant", "content": content, "stop_reason": stop, "usage": map[string]any{"input_tokens": 4, "output_tokens": 2}})
				}))
				defer server.Close()
				var inner model.LLM
				if adapterName == "sap-claude" {
					var err error
					inner, err = sap.NewProviderWithConfig(ctx, "anthropic--claude-4.6-sonnet", "fixture-id", "fixture-secret", server.URL, server.URL, "fixture-group")
					if err != nil {
						t.Fatal(err)
					}
				} else {
					botNativeAnthropicLoopback(t, server)
					inner = anthropic.NewProvider("fixture-key", "claude-sonnet-4-6")
				}
				ordinaryReq := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("Observe receipt", genai.RoleUser)}, Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "read_file", ParametersJsonSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}}}}}
				before, _ := json.Marshal(ordinaryReq)
				ordinaryRun := func() {
					finals := 0
					for response, err := range inner.GenerateContent(ctx, ordinaryReq, false) {
						if err != nil {
							t.Fatal(err)
						}
						if response != nil && response.Content != nil && len(response.Content.Parts) == 1 && response.Content.Parts[0].Text == finalText {
							finals++
						}
					}
					if finals != 1 {
						t.Fatalf("ordinary Chat final responses=%d", finals)
					}
				}
				ordinaryRun()
				baseline := <-bodies
				ordinary.Store(false)
				read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read native fixture receipt"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
					n := reads.Add(1)
					if c.FunctionCallID() != fmt.Sprintf("native-%d", n) || !reflect.DeepEqual(args, map[string]any{"path": fmt.Sprintf("receipt-%d", n)}) {
						t.Errorf("native call changed: id=%s args=%v", c.FunctionCallID(), args)
					}
					bot.RecordToolOutcome(c, c.FunctionCallID(), bot.ToolOutcome{Verified: true, Unchanged: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: fmt.Sprintf("Native receipt %d", n), Source: "fixture://receipt", ObservedAt: time.Now(), ActorID: req.Bot.OwnerID}}})
					return map[string]any{"receipt": fmt.Sprintf("receipt-%d", n)}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				send, err := functiontool.New(functiontool.Config{Name: "send_email", Description: "Forbidden fixture effect"}, func(tool.Context, map[string]any) (map[string]any, error) {
					sends.Add(1)
					return map[string]any{"sent": true}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				budgeted, err := provider.NewBudgetLLM(provider.NewSwappableLLM(inner), budget, provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true})
				if err != nil {
					t.Fatal(err)
				}
				runtime, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, budgeted, []tool.Tool{read, send}, &cleaned))
				if err != nil {
					t.Fatal(err)
				}
				var events []bot.RuntimeEvent
				result, runErr := runtime.Start(ctx, req, func(_ context.Context, e bot.RuntimeEvent) error { events = append(events, e); return nil })
				wantReads, wantGenerations := int64(2), int64(3)
				if scenario == "serial-two-then-text" {
					if runErr != nil || result.Status != store.RunSucceeded || result.Report.Summary != finalText {
						t.Fatalf("serial result=%+v err=%v", result, runErr)
					}
				} else {
					wantReads, wantGenerations = 0, 1
					if scenario == "batch-after-read" {
						wantReads, wantGenerations = 1, 2
					}
					var native *bot.CheckpointResponseError
					if !errors.Is(runErr, store.ErrBotUnsupported) || !errors.As(runErr, &native) || native.Reason != bot.CheckpointResponseMultipleCalls || native.ToolCalls != 2 {
						t.Fatalf("lost native rejection identity: %v", runErr)
					}
					if result.Status != store.RunFailed || result.Failure == nil || result.Failure.Class != bot.ErrorPermanent || !result.Failure.TerminalForAttempt || result.Report.Failure == nil || result.Report.Failure.Kind != store.BotReportFailureExecutionFormat {
						t.Fatalf("lost terminal failure classification: %+v", result)
					}
					if !strings.Contains(result.Report.Summary, "not been automatically retried") || strings.Contains(result.Report.Summary+result.Report.Failure.TechnicalDetails, "rejected-secret") {
						t.Fatal("unsafe or incomplete failure explanation")
					}
				}
				if reads.Load() != wantReads || sends.Load() != 0 || generations.Load() != wantGenerations || cleaned.Load() != 1 {
					t.Fatalf("effects/retry: reads=%d sends=%d generations=%d cleanup=%d", reads.Load(), sends.Load(), generations.Load(), cleaned.Load())
				}
				if result.Usage.ToolCalls != wantReads || len(result.Report.Unchanged) != int(wantReads) || approvals.a != nil || len(result.Checkpoint) != 0 || len(result.Report.NeedsApproval) != 0 {
					t.Fatalf("evidence/approval mismatch: %+v", result)
				}
				if len(events) == 0 || events[len(events)-1].Type != "done" {
					t.Fatal("missing terminal done")
				}
				for _, event := range events {
					if event.Type == "error" {
						t.Fatal("raw native checkpoint error escaped")
					}
				}
				for i := int64(0); i < wantGenerations; i++ {
					body := <-bodies
					// Later generations must carry the previous tool result, proving
					// the actual agent loop, not three disconnected provider calls.
					if i > 0 {
						messages, _ := json.Marshal(body["messages"])
						if !strings.Contains(string(messages), `"tool_result"`) || !strings.Contains(string(messages), fmt.Sprintf("native-%d", i)) {
							t.Fatalf("missing prior tool result: %s", messages)
						}
					}
				}
				ordinary.Store(true)
				ordinaryRun()
				if !reflect.DeepEqual(baseline, <-bodies) {
					t.Fatal("reused provider changed ordinary Chat wire request")
				}
				after, _ := json.Marshal(ordinaryReq)
				if string(before) != string(after) || len(bodies) != 0 || generations.Load() != wantGenerations {
					t.Fatal("request mutation or unexpected generation")
				}
				if adapterName == "sap-claude" && (authCalls.Load() != 2 || deployments.Load() != 1) {
					t.Fatalf("SAP constructor exchange: auth=%d deployments=%d", authCalls.Load(), deployments.Load())
				}
			})
		}
	}
}
