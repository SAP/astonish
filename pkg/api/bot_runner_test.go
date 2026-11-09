package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type botChatTestBudget struct{ checks atomic.Int64 }

func (b *botChatTestBudget) Check(ctx context.Context) error                 { b.checks.Add(1); return ctx.Err() }
func (b *botChatTestBudget) ReserveTool(ctx context.Context, _ string) error { return ctx.Err() }
func (b *botChatTestBudget) ReserveModel(ctx context.Context, r execution.BudgetReservation) (execution.BudgetReservation, error) {
	return r, ctx.Err()
}
func (b *botChatTestBudget) SettleModel(ctx context.Context, _ uuid.UUID, _ execution.BudgetUsage) error {
	return ctx.Err()
}

func botChatTestRequest(t *testing.T) (context.Context, bot.RunRequest, session.Service) {
	t.Helper()
	owner := uuid.New()
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true}
	ctx, err := execution.WithPrincipal(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: owner.String(), OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	version := store.DefaultBotVersion()
	version.BotID, version.Charter = id.BotID, "Read the assigned information and report."
	req := bot.RunRequest{
		Bot:     store.Bot{ID: id.BotID, OwnerID: owner, Status: store.BotActive, Version: 1, Scope: store.BotScopePersonal},
		Run:     store.BotRun{ID: id.RunID, BotID: id.BotID, Version: 1, SessionID: uuid.New(), Status: store.RunRunning, Lease: store.BotLease{BotID: id.BotID, RunID: id.RunID, WorkerID: "test", Fence: 1, ExpiresAt: time.Now().Add(time.Minute)}},
		Version: version, Principal: p, Policy: policy, Budget: &botChatTestBudget{}, Instructions: "Report the observation.",
	}
	ss := session.InMemoryService()
	_, err = ss.Create(ctx, &session.CreateRequest{AppName: studioChatAppName, UserID: owner.String(), SessionID: req.Run.SessionID.String(), State: map[string]any{"execution_owner": "bot", "preserved": "yes"}})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, req, ss
}

func botChatTestFactory(t *testing.T, ss session.Service, llm model.LLM, tools []tool.Tool, cleaned *atomic.Int64) BotChatAgentFactory {
	t.Helper()
	return func(ctx context.Context, req bot.RunRequest) (*BotChatResources, error) {
		id, ok := execution.BotRunIdentityFromContext(ctx)
		if !ok || id != req.Policy.Identity() || execution.RunPolicyFromContext(ctx) != req.Policy || BotChatRunBudgetFromContext(ctx) != req.Budget {
			t.Error("factory lost trusted policy/budget context")
		}
		chat := agent.NewChatAgent(llm, tools, nil, ss, &agent.SystemPromptBuilder{}, false, false)
		return &BotChatResources{Agent: chat, SessionService: ss, AppName: studioChatAppName, Cleanup: func() { cleaned.Add(1) }}, nil
	}
}

// This uses a real temporary HTTP listener, not a recorder or fake ChatRunner.
// HTTP only drives the worker here; it is deliberately not a production route.
func TestBotChatRuntimeAdapterHTTPToolless(t *testing.T) {
	ctx, req, ss := botChatTestRequest(t)
	var cleaned atomic.Int64
	llm := NewMockLLM(TextTurn("Observed: all checks green."))
	adapter, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, llm, nil, &cleaned))
	if err != nil {
		t.Fatal(err)
	}
	var events []bot.RuntimeEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := adapter.Start(ctx, req, func(_ context.Context, event bot.RuntimeEvent) error { events = append(events, event); return nil })
		if err != nil {
			t.Errorf("Start: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	response, err := server.Client().Post(server.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result bot.RunResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || result.Status != store.RunSucceeded || result.Report.Summary != "Observed: all checks green." || len(result.Report.Unverified) != 0 {
		t.Fatalf("result: %+v, HTTP %d", result, response.StatusCode)
	}
	if cleaned.Load() != 1 || len(llm.Calls) != 1 {
		t.Fatalf("cleanup=%d model calls=%d", cleaned.Load(), len(llm.Calls))
	}
	for i, event := range events {
		if event.Sequence != int64(i+1) || event.RunID != req.Run.ID || event.SessionID != req.Run.SessionID || event.TurnID != events[0].TurnID {
			t.Fatalf("event identity/sequence: %+v", event)
		}
	}
	if len(events) < 3 || events[len(events)-1].Type != "done" {
		t.Fatalf("missing final transport event: %+v", events)
	}
	stored, err := ss.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := stored.Session.State().Get("execution_owner")
	if owner != "bot" {
		t.Fatalf("execution owner changed: %v", owner)
	}
}

// A trusted, mechanically constrained classifier and live guard are installed
// for this one test tool. This does not claim native production paths are ready.
type botChatTestClassifier struct{}

func (botChatTestClassifier) DescribeAction(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
}

type botChatTestGuard struct{}

func (botChatTestGuard) Authorize(ctx context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	return execution.EvaluateRunPolicy(ctx, action, time.Now()), nil
}

func TestBotChatRuntimeAdapterHTTPToolSequence(t *testing.T) {
	ctx, req, ss := botChatTestRequest(t)
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
	var cleaned, executed atomic.Int64
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read controlled fixture"}, func(ctx tool.Context, args map[string]any) (map[string]any, error) {
		if err := agent.AuthorizeBotTool(ctx, "read_file", args); err != nil {
			return nil, err
		}
		executed.Add(1)
		return map[string]any{"content": "fixture value"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	llm := NewMockLLM(ToolCallTurn("read_file", map[string]any{"path": "fixture"}), TextTurn("Fixture value observed."))
	adapter, _ := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, llm, []tool.Tool{read}, &cleaned))
	var events []bot.RuntimeEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		result, err := adapter.Start(ctx, req, func(_ context.Context, e bot.RuntimeEvent) error { events = append(events, e); return nil })
		if err != nil {
			t.Errorf("Start: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	response, err := server.Client().Post(server.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result bot.RunResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Status != store.RunPartial || len(result.Report.Unverified) == 0 || executed.Load() != 1 || len(llm.Calls) != 2 || cleaned.Load() != 1 {
		t.Fatalf("result=%+v executed=%d calls=%d cleanup=%d", result, executed.Load(), len(llm.Calls), cleaned.Load())
	}
	calls, results := 0, 0
	for _, event := range events {
		if event.Type == "tool_call" {
			calls++
		}
		if event.Type == "tool_result" {
			results++
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("calls=%d results=%d events=%+v", calls, results, events)
	}
}

func TestBotChatRuntimeAdapterRejectsMissingAuthority(t *testing.T) {
	for _, name := range []string{"budget", "policy", "principal", "owner", "session", "lease", "checkpoint"} {
		t.Run(name, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			switch name {
			case "budget":
				req.Budget = nil
			case "policy":
				req.Policy = nil
			case "principal":
				ctx = context.Background()
			case "owner":
				req.Bot.OwnerID = uuid.New()
			case "session":
				req.Run.SessionID = uuid.Nil
			case "lease":
				req.Run.Lease.Fence = 0
			case "checkpoint":
				req.Run.Checkpoint = json.RawMessage(`{}`)
			}
			adapter, _ := NewBotChatRuntimeAdapter(func(context.Context, bot.RunRequest) (*BotChatResources, error) {
				t.Error("factory called without authority")
				return nil, nil
			})
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err == nil || result.Status == store.RunSucceeded || result.Failure == nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestBotChatRuntimeAdapterSinkFailureAndModelFailure(t *testing.T) {
	for _, failure := range []string{"sink", "model", "empty"} {
		t.Run(failure, func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			var cleaned atomic.Int64
			turns := []*MockTurn{TextTurn("success must not escape")}
			if failure == "model" {
				turns = []*MockTurn{ErrorTurn("failed", "controlled failure")}
			}
			if failure == "empty" {
				turns = []*MockTurn{TextTurn("")}
			}
			adapter, _ := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, NewMockLLM(turns...), nil, &cleaned))
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error {
				if failure == "sink" {
					return errors.New("persistence unavailable")
				}
				return nil
			})
			if err == nil || result.Status == store.RunSucceeded || result.Failure == nil || cleaned.Load() != 1 {
				t.Fatalf("result=%+v err=%v cleaned=%d", result, err, cleaned.Load())
			}
		})
	}
}

func TestBotChatRuntimeAdapterTypedCompletionNotDone(t *testing.T) {
	for _, eventType := range []string{"done", "error", "error_info", "approval", "network_denial_hint", "chat_question"} {
		t.Run(eventType, func(t *testing.T) {
			cr := newChatRunner("session", "user", studioChatAppName, false)
			defer cr.cancel()
			cr.emitEvent(eventType, map[string]any{"error": "failure"})
			completion := cr.Completion()
			if completion.Final {
				t.Fatal("wire event became successful completion")
			}
			if eventType == "error" || eventType == "error_info" {
				if completion.Err == nil {
					t.Fatal("lost failure")
				}
			}
			if eventType == "approval" || eventType == "network_denial_hint" || eventType == "chat_question" {
				if !completion.Blocked {
					t.Fatal("lost block")
				}
			}
		})
	}
	cr := newChatRunner("session", "user", studioChatAppName, false)
	defer cr.cancel()
	cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("claim", "model")}})
	cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "execute_tool", Response: map[string]any{"state": string(agent.ProgressiveStateAuthorizationDenied)}}}}}}})
	cr.emitEvent("done", map[string]any{"done": true})
	if !cr.Completion().Blocked {
		t.Fatal("done erased blocked result")
	}
}

func TestBotChatRuntimeAdapterSinkBeforePublish(t *testing.T) {
	cr := newChatRunner("session", "user", studioChatAppName, false)
	defer cr.cancel()
	var mu sync.Mutex
	persisted := 0
	cr.SetEventSink(func(context.Context, ChatEvent) error {
		mu.Lock()
		defer mu.Unlock()
		if len(cr.GetHistory()) != persisted {
			t.Error("event published before persistence")
		}
		persisted++
		return nil
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { cr.emitEvent("text", map[string]any{"text": "event"}) })
	}
	wg.Wait()
	if len(cr.GetHistory()) != 20 || persisted != 20 {
		t.Fatal("event loss")
	}
	cr.SetEventSink(func(context.Context, ChatEvent) error { return errors.New("no persistence") })
	cr.emitEvent("text", map[string]any{"text": "must not publish"})
	if len(cr.GetHistory()) != 20 || cr.Completion().Err == nil || cr.ctx.Err() == nil {
		t.Fatal("sink failure did not fail closed")
	}
}
