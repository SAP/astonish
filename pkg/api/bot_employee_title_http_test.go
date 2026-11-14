package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// A real loopback HTTP invocation of the production producer and event sink.
// The model is controlled, not paid: this proves transport ordering, not that a
// provider follows the prompt or that title admission supplies production tools.
// The reviewed final publication is intentionally outside this producer test.
func TestBotEmployeeTitleAcknowledgementHTTPToolBoundary(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		for _, toolBoundary := range []bool{false, true} {
			name := "historical"
			if guarded {
				name = "evidence-bound"
			}
			if toolBoundary {
				name += "/work-tool"
			} else {
				name += "/toolless"
			}
			t.Run(name, func(t *testing.T) {
				ctx, req, ss := botChatTestRequest(t)
				req.Bot.Name, req.Instructions, req.Interactive = "AI News", "AI News", true
				req.Run.Intent = store.BotIntentRegular
				if guarded {
					req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: req.Instructions})
					req.Work.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
				}
				ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
				ctx = execution.WithRunPolicy(ctx, req.Policy)
				ctx = context.WithValue(ctx, botChatBudgetKey{}, req.Budget)
				ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
				const acknowledgement = "I'll put together an AI news briefing and check the original reporting."
				const candidate = "PRIVATE_FINAL_CANDIDATE"
				var cleaned, executed atomic.Int64
				read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Inspect original controlled reporting"}, func(ctx tool.Context, args map[string]any) (map[string]any, error) {
					if err := agent.AuthorizeBotTool(ctx, "read_file", args); err != nil {
						return nil, err
					}
					executed.Add(1)
					return map[string]any{"content": "Controlled original reporting: a new model was announced."}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				turns := []*MockTurn{TextTurn(candidate)}
				if toolBoundary {
					turns = []*MockTurn{{Parts: []*genai.Part{{Text: acknowledgement}, {FunctionCall: &genai.FunctionCall{Name: "read_file", Args: map[string]any{"path": "original-reporting"}}}}, TurnComplete: true}, TextTurn(candidate)}
				}
				llm := NewMockLLM(turns...)
				resources, err := botChatTestFactory(t, ss, llm, []tool.Tool{read}, &cleaned)(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				defer resources.Cleanup()
				var events []bot.RuntimeEvent
				server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
					var sequence int64
					var completion ChatCompletion
					result, runErr := runBotChatAttempt(ctx, req, resources, func(_ context.Context, event bot.RuntimeEvent) error {
						events = append(events, event)
						return nil
					}, nil, nil, nil, false, &sequence, &completion)
					if runErr != nil {
						t.Errorf("producer: %v", runErr)
						out.WriteHeader(http.StatusInternalServerError)
					}
					if !completion.Final || completion.FinalText != candidate || result.Report.Summary != candidate {
						t.Errorf("producer lost its final candidate: %+v", completion)
					}
					_ = json.NewEncoder(out).Encode(result)
				}))
				defer server.Close()
				response, err := server.Client().Post(server.URL, "application/json", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("HTTP %d", response.StatusCode)
				}
				ackIndex, toolIndex, resultIndex := -1, -1, -1
				var live strings.Builder
				for i, event := range events {
					if event.Sequence != int64(i+1) || event.RunID != req.Run.ID || event.SessionID != req.Run.SessionID || event.TurnID != events[0].TurnID {
						t.Fatalf("wrong event identity/order: %+v", event)
					}
					if event.Type == "text" {
						var text struct {
							Text string `json:"text"`
						}
						if err := json.Unmarshal(event.Payload, &text); err != nil {
							t.Fatal(err)
						}
						live.WriteString(text.Text)
						if text.Text == acknowledgement {
							ackIndex = i
						}
					}
					if event.Type == "tool_call" {
						toolIndex = i
					}
					if event.Type == "tool_result" {
						resultIndex = i
					}
					if guarded && event.Type == "done" {
						t.Fatal("unreviewed producer closed the public stream")
					}
				}
				if toolBoundary && (ackIndex < 0 || toolIndex <= ackIndex || resultIndex <= toolIndex || executed.Load() != 1) {
					t.Fatalf("acknowledgement did not precede useful work: ack=%d call=%d result=%d executed=%d events=%+v", ackIndex, toolIndex, resultIndex, executed.Load(), events)
				}
				if guarded && toolBoundary && live.String() != acknowledgement {
					t.Fatalf("guarded active text must be exactly the model acknowledgement, got %q", live.String())
				}
				if guarded && !toolBoundary && live.Len() != 0 {
					t.Fatalf("toolless guarded candidate escaped: %q", live.String())
				}
				if guarded && strings.Contains(live.String(), candidate) {
					t.Fatal("unreviewed final candidate leaked")
				}
				if !guarded && !strings.Contains(live.String(), candidate) {
					t.Fatal("historical producer publication changed")
				}
				if !toolBoundary && (ackIndex >= 0 || executed.Load() != 0) {
					t.Fatal("server fabricated acknowledgement or work")
				}
				if len(llm.Calls) != len(turns) || !strings.Contains(llm.Calls[0].Config.SystemInstruction.Parts[0].Text, "begin with a brief AI-authored acknowledgement") {
					t.Fatal("producer prompt lost acknowledgement guidance or added a model call")
				}
			})
		}
	}
}
