package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

func TestEmployeeWorkerRecoveredEvidenceMerge(t *testing.T) {
	for _, mode := range []string{"denied-browser", "read-feedback", "missing-denial-receipt", "uncertain-write", "missing-response", "mixed-native", "missing-final", "terminal-native", "cancelled", "pending-schedule-denial", "pending-schedule-mixed-native", "pending-schedule-confirmed-denial", "pending-schedule-foreign-denial", "pending-schedule-missing-receipt"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx = agent.WithBotPrivateWorkerExecution(ctx)
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectRead, verified: true}})
			ctx, _ = bot.WithToolOutcomes(ctx)
			journal := &botAgentReadJournal{}
			ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
			cr := newChatRunner("worker", "user", studioChatAppName, false)
			defer cr.cancel()
			cr.ctx = ctx
			observe := func(call *genai.FunctionCall, response map[string]any, receipt *bot.ToolOutcome) {
				journal.capture(ctx, call)
				cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}})
				if receipt != nil {
					bot.RecordToolOutcome(ctx, call.ID, *receipt)
				}
				cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: response}}}}}})
			}
			observe(&genai.FunctionCall{ID: "valid-read", Name: "read_file", Args: map[string]any{"path": "evidence"}}, map[string]any{"content": "Useful established audit evidence"}, nil)
			denied := &agent.BotPolicyError{Decision: execution.PolicyDecision{Kind: execution.PolicyDeny, Reason: "browser not permitted"}}
			call := &genai.FunctionCall{ID: "failure", Name: "execute_tool", Args: map[string]any{"name": "browser_navigate", "arguments": map[string]any{"url": "https://example.test"}}}
			response := map[string]any{"error": denied}
			outcome := bot.NewToolNonexecutionOutcome(ctx, call.ID, "browser_navigate", map[string]any{"url": "https://example.test"}, bot.ToolFeedbackTerminal)
			receipt := &outcome
			switch mode {
			case "read-feedback":
				call = &genai.FunctionCall{ID: "failure", Name: "read_file", Args: map[string]any{"path": "unavailable"}}
				response = map[string]any{"error": "source unavailable"}
				receipt = nil
			case "missing-denial-receipt":
				receipt = nil
			case "uncertain-write":
				call.Args["name"] = "write_file"
				response = map[string]any{"error": "write failed after dispatch"}
				receipt = nil
			case "terminal-native":
				response["error"] = errors.New("native boundary failure")
			case "cancelled":
				cr.ctx = cancelledWorkerContext(ctx)
			}
			observe(call, response, receipt)
			observe(&genai.FunctionCall{ID: "audit", Name: "read_file", Args: map[string]any{"path": "audit"}}, map[string]any{"content": "Additional verified audit"}, nil)
			cr.observeCompletion(&session.Event{ID: "worker-final", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Useful limited result; browser/source unavailable.", "model")}})
			c := cr.Completion()
			switch mode {
			case "missing-response":
				c.Usage.ToolCalls++
			case "mixed-native":
				c.Err = errors.Join(c.Err, errors.New("persistence failed"))
			case "missing-final":
				c.Final = false
			}
			if strings.HasPrefix(mode, "pending-schedule-") {
				payload := store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "owner request", Schedule: &store.BotControlSchedule{ExpectedRevision: 1, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "*/5 * * * *", Timezone: "UTC", Instructions: "Deliver a known test instruction", Enabled: true}}}
				raw, _ := json.Marshal(payload)
				c.pendingControls = []botWorkPendingControl{{OperationID: req.Run.ID.String(), Operation: payload.Operation, Acknowledgment: "pending", Payload: raw}}
				c.reportBodies[botEmployeePrivateScheduleSourceKey] = req.Run.ID.String()
				if mode == "pending-schedule-confirmed-denial" {
					c.Report.EffectState = store.BotReportEffectsConfirmed
				}
				if mode == "pending-schedule-foreign-denial" {
					c.reportBodies[botEmployeePrivateDeniedSourceKey] = "foreign"
				}
				if mode == "pending-schedule-missing-receipt" {
					delete(c.reportBodies, botEmployeePrivateScheduleSourceKey)
				}
				if mode == "pending-schedule-mixed-native" {
					c.Err = errors.Join(c.Err, errors.New("persistence failed"))
				}
			}
			parent := ChatCompletion{Final: true, FinalEventID: "employee-final", FinalText: "Employee's own useful limited answer", Report: store.BotReport{EffectState: store.BotReportEffectsReadOnly}}
			mergeBotEmployeeCompletion(&parent, c)
			wantSafe := mode == "denied-browser" || mode == "read-feedback" || mode == "pending-schedule-denial" || mode == "pending-schedule-confirmed-denial"
			if got := parent.Report.EffectState != store.BotReportEffectsUncertain; got != wantSafe {
				t.Fatalf("safe=%v want=%v worker=%+v merged=%+v", got, wantSafe, c, parent)
			}
			if parent.FinalText != "Employee's own useful limited answer" {
				t.Fatal("worker prose replaced Employee speech")
			}
			if wantSafe {
				// The second merge consumes aggregate evidence with no worker final.
				aggregate := ChatCompletion{}
				mergeBotEmployeeCompletion(&aggregate, c)
				final := ChatCompletion{Final: true, FinalEventID: "main-final", FinalText: "Employee final", Report: store.BotReport{EffectState: store.BotReportEffectsReadOnly}}
				mergeBotEmployeeCompletion(&final, aggregate)
				if final.Report.EffectState == store.BotReportEffectsUncertain {
					t.Fatalf("aggregate poisoned final: %+v", final)
				}
			}
		})
	}
}

func TestEmployeeWorkerSendRecoversLimitedFinal(t *testing.T) {
	for _, mode := range []string{"denied-browser", "read-feedback", "uncertain-write"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx = execution.WithRunPolicy(ctx, req.Policy)
			ctx, _ = bot.WithToolOutcomes(ctx)
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectRead, verified: true}, Guard: botChatTestGuard{}})
			trace := &employeeWorkerTranscript{}
			resources := &BotChatResources{AppName: studioChatAppName, Transcript: trace}
			resources.WorkerFactory = func(c context.Context, w BotEmployeeWorkerRequest) (*BotChatResources, error) {
				read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Controlled read"}, func(tc tool.Context, args map[string]any) (map[string]any, error) {
					if args["path"] == "unavailable" {
						return map[string]any{"error": "source unavailable"}, nil
					}
					return map[string]any{"content": "Useful audit evidence"}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				deniedCalls := 0
				browser, err := functiontool.New(functiontool.Config{Name: "browser_navigate", Description: "Denied browser"}, func(tc tool.Context, args map[string]any) (map[string]any, error) { deniedCalls++; return nil, nil })
				if err != nil {
					t.Fatal(err)
				}
				// Normal ChatAgent authorization must reject before the executable.
				tools := []tool.Tool{read, browser}
				failure := ToolCallTurn("browser_navigate", map[string]any{"url": "https://example.test"})
				if mode == "read-feedback" {
					failure = ToolCallTurn("read_file", map[string]any{"path": "unavailable"})
				}
				if mode == "uncertain-write" {
					write, e := functiontool.New(functiontool.Config{Name: "read_file", Description: "Conflicting started result"}, func(tc tool.Context, args map[string]any) (map[string]any, error) {
						bot.RecordToolOutcome(tc, tc.FunctionCallID(), bot.ToolOutcome{ReceiptConflict: true, Unverified: []string{"started write uncertain"}})
						return map[string]any{"error": "write failed"}, nil
					})
					if e != nil {
						t.Fatal(e)
					}
					tools = []tool.Tool{write}
					failure = ToolCallTurn("read_file", map[string]any{"path": "uncertain"})
				}
				llm := NewMockLLM(ToolCallTurn("read_file", map[string]any{"path": "valid"}), failure, ToolCallTurn("read_file", map[string]any{"path": "audit"}), TextTurn("Useful limited result; unavailable source/action was not used."), TextTurn("Private followup"))
				t.Cleanup(func() {
					if deniedCalls != 0 {
						t.Fatal("browser permission broadened")
					}
				})
				chat := agent.NewChatAgent(llm, tools, nil, w.Sessions, &agent.SystemPromptBuilder{}, false, false)
				chat.ToolIndex = agent.NewLexicalToolIndex()
				if err := chat.ToolIndex.PrimeTools(c, tools, nil); err != nil {
					t.Fatal(err)
				}
				return &BotChatResources{Agent: chat, AppName: studioChatAppName, Cleanup: func() {}}, nil
			}
			s := &botEmployeeChatService{req: req, resources: resources, workers: map[string]*botEmployeeWorker{}}
			defer s.close()
			result, err := s.Send(ctx, bot.EmployeeChatRequest{Message: "Inspect sources"})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "uncertain-write" {
				if result.Status != bot.EmployeeChatFailed || s.workers[result.WorkerID].pending == "" || s.completion.Report.EffectState != store.BotReportEffectsUncertain {
					t.Fatalf("uncertain dispatch reopened: %+v %+v", result, s.completion)
				}
				return
			}
			if result.Status != bot.EmployeeChatPartial || result.PrivateText == "" || len(result.Limitations) == 0 || s.workers[result.WorkerID].pending != "" || s.completion.Report.EffectState != store.BotReportEffectsReadOnly {
				t.Fatalf("limited result lost: %+v completion=%+v", result, s.completion)
			}
			parent := ChatCompletion{Final: true, FinalEventID: "employee-final", FinalText: "Employee final", Report: store.BotReport{EffectState: store.BotReportEffectsReadOnly}}
			mergeBotEmployeeCompletion(&parent, s.completion)
			if parent.Report.EffectState != store.BotReportEffectsReadOnly || parent.FinalText != "Employee final" {
				t.Fatalf("aggregate poisoned Employee: %+v", parent)
			}
			followup, err := s.Send(ctx, bot.EmployeeChatRequest{WorkerID: result.WorkerID, Message: "Follow up without unavailable action"})
			if err != nil || followup.Status != bot.EmployeeChatCompleted {
				t.Fatalf("safe dispatch remained locked: %+v %v", followup, err)
			}
		})
	}
}

func cancelledWorkerContext(ctx context.Context) context.Context {
	c, cancel := context.WithCancel(ctx)
	cancel()
	return c
}
