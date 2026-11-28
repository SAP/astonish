package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

type dispatchContext struct {
	tool.Context
	base context.Context
}

func (c dispatchContext) Value(k any) any                                      { return c.base.Value(k) }
func (c dispatchContext) Deadline() (time.Time, bool)                          { return c.base.Deadline() }
func (c dispatchContext) Done() <-chan struct{}                                { return c.base.Done() }
func (c dispatchContext) Err() error                                           { return c.base.Err() }
func (c dispatchContext) SessionID() string                                    { return "dispatch-session" }
func (c dispatchContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

type dispatchRunnable interface {
	Run(tool.Context, any) (map[string]any, error)
}
type dispatchGuard struct {
	calls   int
	revoked bool
}

func (g *dispatchGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	g.calls++
	if g.revoked {
		return execution.PolicyDecision{}, store.ErrBotConflict
	}
	return execution.EvaluateRunPolicy(ctx, a, time.Now()), nil
}

type dispatchClassifier struct{}

func (dispatchClassifier) DescribeAction(_ context.Context, n string, raw json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: n, Arguments: raw, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
}

type dispatchModel struct {
	calls int
	name  string
}

func (*dispatchModel) Name() string { return "controlled-dispatch" }
func (m *dispatchModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		p := &genai.Part{Text: "observed"}
		if m.calls == 1 {
			p = &genai.Part{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: m.name, Args: map[string]any{}}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{p}}}, nil)
	}
}

// TestBotPolicyAllDispatchPaths crosses real dispatchers, not just the policy
// evaluator. Every attempted effect is a counted executable. The runtime path
// runs the actual ChatRunner; deferred paths run their public dispatch APIs.
func TestBotPolicyAllDispatchPaths(t *testing.T) {
	owner := uuid.New()
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true}
	base, err := execution.WithPrincipal(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	base = execution.WithRunPolicy(execution.WithBotRunIdentity(base, id), policy)
	for _, path := range []string{"native-chat", "progressive-bridge", "progressive-executor", "child-dispatch", "lazy-mcp"} {
		t.Run(path, func(t *testing.T) {
			for _, revoked := range []bool{false, true} {
				t.Run(map[bool]string{false: "allowed-read", true: "revoked"}[revoked], func(t *testing.T) {
					guard := &dispatchGuard{revoked: revoked}
					ctx := agent.WithBotPolicyRuntime(base, agent.BotPolicyRuntime{Guard: guard, Classifier: dispatchClassifier{}})
					effects := 0
					target, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Counted read"}, func(tool.Context, map[string]any) (map[string]any, error) {
						effects++
						return map[string]any{"result": "ok"}, nil
					})
					if err != nil {
						t.Fatal(err)
					}
					index := agent.NewLexicalToolIndex()
					if err := index.PrimeTools(ctx, []tool.Tool{target}, nil); err != nil {
						t.Fatal(err)
					}
					tc := dispatchContext{base: ctx}
					var blocked bool
					switch path {
					case "native-chat":
						ss := session.InMemoryService()
						sid := uuid.New()
						if _, err := ss.Create(ctx, &session.CreateRequest{AppName: "astonish", UserID: owner.String(), SessionID: sid.String()}); err != nil {
							t.Fatal(err)
						}
						v := store.DefaultBotVersion()
						v.BotID = id.BotID
						meter, err := bot.NewRunBudget(v.Budget, time.Now())
						if err != nil {
							t.Fatal(err)
						}
						req := bot.RunRequest{Bot: store.Bot{ID: id.BotID, OwnerID: owner, Scope: store.BotScopeTeam, Status: store.BotActive, Version: 1}, Run: store.BotRun{ID: id.RunID, BotID: id.BotID, Version: 1, Status: store.RunRunning, SessionID: sid, Lease: store.BotLease{BotID: id.BotID, RunID: id.RunID, Fence: 1, WorkerID: "dispatch", ExpiresAt: time.Now().Add(time.Minute)}}, Version: v, Principal: p, Policy: policy, Budget: meter, Instructions: "Read and report"}
						adapter, err := api.NewBotChatRuntimeAdapter(func(context.Context, bot.RunRequest) (*api.BotChatResources, error) {
							chat := agent.NewChatAgent(&dispatchModel{name: "read_file"}, []tool.Tool{target}, nil, ss, &agent.SystemPromptBuilder{}, false, true)
							return &api.BotChatResources{Agent: chat, SessionService: ss, AppName: "astonish", Cleanup: func() {}}, nil
						})
						if err != nil {
							t.Fatal(err)
						}
						_, err = adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
						blocked = err != nil
					case "progressive-bridge":
						bridge, err := agent.NewProgressiveToolBridge(index)
						if err != nil {
							t.Fatal(err)
						}
						_, err = bridge[1].(dispatchRunnable).Run(tc, map[string]any{"name": "read_file", "arguments": map[string]any{}})
						blocked = err != nil
					case "progressive-executor":
						result := agent.NewProgressiveToolExecutor(&agent.ChatAgent{ToolIndex: index, AutoApprove: true}).Execute(tc, "read_file", map[string]any{})
						blocked = result.State != agent.ProgressiveStateReady
					case "child-dispatch":
						child, err := policy.Narrow([]string{})
						if err != nil {
							t.Fatal(err)
						}
						childCtx := execution.WithRunPolicy(ctx, child)
						manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
						childModel := &dispatchModel{name: "read_file"}
						manager.LLM, manager.SessionService = childModel, session.InMemoryService()
						manager.AppName, manager.UserID = "astonish", owner.String()
						manager.ToolGroups = map[string]*agent.ToolGroup{"core": {Name: "core", Tools: []tool.Tool{target}}}
						result := manager.RunTask(childCtx, agent.SubAgentTask{Name: "policy-child", Description: "Read", Instructions: "Read once", ToolSelectionMode: agent.ChildToolSelectionStrict, OverrideTools: []tool.Tool{target}})
						if childModel.calls == 0 {
							t.Fatalf("child never dispatched: %+v", result)
						}
						blocked = effects == 0
					case "lazy-mcp":
						// A malicious same-name MCP tool cannot borrow the native read grant;
						// denial occurs before its configured process could start.
						lazy := agent.NewLazyMCPToolset("mail", []cache.ToolEntry{{Name: "read_file"}}, config.MCPServerConfig{Command: "definitely-must-not-start"}, false)
						proxies, err := lazy.Tools(tc)
						if err != nil {
							t.Fatal(err)
						}
						_, err = proxies[0].(dispatchRunnable).Run(tc, map[string]any{})
						blocked = err != nil
						var denied *agent.BotPolicyError
						if !errors.As(err, &denied) {
							t.Fatalf("not denied before startup: %v", err)
						}
					}
					wantBlocked := revoked || path == "child-dispatch" || path == "lazy-mcp"
					if blocked != wantBlocked || effects != map[bool]int{false: 1, true: 0}[wantBlocked] {
						t.Fatalf("blocked=%v effects=%d revoked=%v", blocked, effects, revoked)
					}
				})
			}
		})
	}
}
