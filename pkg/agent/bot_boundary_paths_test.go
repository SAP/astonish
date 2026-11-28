package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

func TestBotChildCannotInheritConsequentialGrants(t *testing.T) {
	for _, bridge := range []bool{false, true} {
		t.Run(fmt.Sprint(bridge), func(t *testing.T) {
			var calls, guards, classifications atomic.Int32
			target := botCountingTool(t, &calls)
			base := botBoundaryContext(t)
			id, _ := execution.BotRunIdentityFromContext(base)
			action := execution.ActionDescriptor{Tool: "read_file", Effect: execution.EffectSend, Arguments: json.RawMessage(`{"path":"notes"}`)}
			digest, _ := action.Digest()
			base = botPolicyContext(t, base, []string{"read_file"}, []execution.ScopedGrant{{ID: uuid.New(), Identity: id, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)}})
			base = WithBotPolicyRuntime(base, BotPolicyRuntime{
				Classifier: botClassifierFunc(func(_ context.Context, name string, raw json.RawMessage) (execution.ActionDescriptor, error) {
					classifications.Add(1)
					return execution.ActionDescriptor{Tool: name, Effect: execution.EffectSend, Arguments: raw}, nil
				}),
				Guard: botGuardFunc(func(context.Context, execution.ActionDescriptor) (execution.PolicyDecision, error) {
					guards.Add(1)
					return execution.PolicyDecision{}, nil
				}),
			})
			name, args := "read_file", map[string]any{"path": "notes"}
			if bridge {
				name, args = "execute_tool", map[string]any{"name": name, "arguments": args}
			}
			mgr := NewSubAgentManager(SubAgentConfig{MaxRetries: 1})
			mgr.AppName, mgr.UserID = "test", "user"
			mgr.SessionService = session.InMemoryService()
			mgr.ToolGroups = map[string]*ToolGroup{"core": {Name: "core", Tools: []tool.Tool{target}}}
			mgr.CredentialStore = botForbiddenCredentialResolver{t}
			mgr.LLM = &memoryCaptureModel{respond: func(_ context.Context, i int, _ *model.LLMRequest) (*model.LLMResponse, error) {
				if i == 0 {
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "child-send", Name: name, Args: args}}}}, TurnComplete: true}, nil
				}
				return &model.LLMResponse{Content: genai.NewContentFromText("done", genai.RoleModel), TurnComplete: true}, nil
			}}
			_ = mgr.RunTask(base, SubAgentTask{Name: "reader", Description: "read", ParentID: "parent", ToolFilter: []string{"read_file"}})
			if calls.Load() != 0 || guards.Load() != 0 || classifications.Load() != 1 {
				t.Fatalf("calls=%d guards=%d classifications=%d", calls.Load(), guards.Load(), classifications.Load())
			}
		})
	}
}

type botStartupToolset struct{ calls atomic.Int32 }

func (*botStartupToolset) Name() string { return "eager" }
func (s *botStartupToolset) Tools(agent.ReadonlyContext) ([]tool.Tool, error) {
	s.calls.Add(1)
	return nil, nil
}

func TestBotEagerToolsetsBlockedBeforeEnumeration(t *testing.T) {
	for _, path := range []string{"direct", "native", "flow", "react", "child"} {
		t.Run(path, func(t *testing.T) {
			ts := &botStartupToolset{}
			base := botBoundaryContext(t)
			a := &AstonishAgent{AutoApprove: true, Toolsets: []tool.Toolset{ts}}
			node := &config.Node{Name: "bot_node", Tools: true, ToolsSelection: []string{"read_file"}, ToolsAutoApproval: true}
			state := NewMockState()
			var blocked error
			yield := func(_ *session.Event, err error) bool {
				if err != nil {
					blocked = err
				}
				return true
			}
			switch path {
			case "direct":
				a.handleToolNode(base, node, state, yield)
			case "native":
				_, blocked = botGuardToolsets(a.Toolsets)[0].Tools(&minimalReadonlyContext{Context: base})
			case "flow":
				a.SessionService = &MockSessionService{State: state}
				_, blocked = a.executeLLMNodeAttempt(&MockInvocationContext{Context: base, StateVal: state}, node, node.Name, state, yield)
			case "react":
				_, blocked = a.executeReActFallback(base, node, node.Name, state, yield, nil, "read", "read")
			case "child":
				mgr := NewSubAgentManager(SubAgentConfig{})
				mgr.ToolGroups = map[string]*ToolGroup{"mcp:eager": {Name: "mcp:eager", Toolsets: a.Toolsets}}
				_, _, blocked = mgr.childToolScope(base, SubAgentTask{ToolFilter: []string{"mcp:eager"}})
			}
			if blocked == nil || !strings.Contains(blocked.Error(), "eager toolset") || ts.calls.Load() != 0 {
				t.Fatalf("blocked=%v enumerations=%d", blocked, ts.calls.Load())
			}
		})
	}
}

func botCountingTool(t *testing.T, calls *atomic.Int32) tool.Tool {
	t.Helper()
	candidate, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "fixture"}, func(_ tool.Context, _ map[string]any) (map[string]any, error) {
		calls.Add(1)
		return map[string]any{"ok": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

// Exercise the actual dispatchers, not only the policy helper. Successful cases
// ensure a broken fixture cannot make the denied cases pass vacuously.
func TestBotDispatchPolicyBeforeCredentials(t *testing.T) {
	for _, path := range []string{"direct", "native", "native_bridge", "flow", "react", "child", "child_bridge"} {
		for _, mode := range []string{"missing_policy", "qualified_denied", "allowed", "qualified_allowed"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				var calls, guards, classifications atomic.Int32
				allowed := mode == "allowed" || mode == "qualified_allowed"
				target, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "fixture"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
					calls.Add(1)
					if args["path"] != "bot-fixture-secret" {
						t.Errorf("credentials not substituted: %v", args)
					}
					return map[string]any{"ok": true}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				var executable tool.Tool = target
				base := botBoundaryContext(t)
				qualified := strings.HasPrefix(mode, "qualified_")
				var sets []tool.Toolset
				if qualified {
					ts := NewLazyMCPToolset("mail", []cache.ToolEntry{{Name: "read_file"}}, config.MCPServerConfig{}, false)
					sets = []tool.Toolset{ts}
					proxies, err := ts.Tools(&minimalReadonlyContext{Context: base})
					if err != nil {
						t.Fatal(err)
					}
					executable = proxies[0]
					if allowed {
						ts.started, ts.liveTools = true, map[string]tool.Tool{"read_file": target}
					}
					defer func() {
						if ts.started && !allowed {
							t.Error("denied tool started MCP server")
						}
					}()
				}
				names := []string{"read_file"}
				if mode == "qualified_allowed" {
					names = []string{"mcp:mail/read_file"}
				}
				if mode != "missing_policy" {
					base = botPolicyContext(t, base, names, nil)
				}
				base = WithBotPolicyRuntime(base, BotPolicyRuntime{
					Classifier: botClassifierFunc(func(ctx context.Context, name string, raw json.RawMessage) (execution.ActionDescriptor, error) {
						classifications.Add(1)
						if strings.Contains(string(raw), "bot-fixture-secret") || !strings.Contains(string(raw), "{{CREDENTIAL:private:token}}") {
							t.Errorf("classifier did not receive credential reference: %s", raw)
						}
						return botVerifiedRead(ctx, name, raw)
					}),
					Guard: botGuardFunc(func(_ context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
						guards.Add(1)
						digest, err := action.Digest()
						return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest}, err
					}),
				})
				args := map[string]any{"path": "{{CREDENTIAL:private:token}}"}
				name := "read_file"
				group := "core"
				if qualified {
					group = "mcp:mail"
				}
				bridge := strings.HasSuffix(path, "_bridge")
				if bridge {
					name, args = "execute_tool", map[string]any{"name": BotToolIdentity(group, "read_file"), "arguments": args}
				}
				llm := &memoryCaptureModel{respond: func(_ context.Context, i int, _ *model.LLMRequest) (*model.LLMResponse, error) {
					if i == 0 {
						if path == "react" {
							return &model.LLMResponse{Content: genai.NewContentFromText("Thought: inspect\nAction: read_file\nAction Input: {\"path\":\"{{CREDENTIAL:private:token}}\"}", genai.RoleModel), TurnComplete: true}, nil
						}
						return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "bot-call", Name: name, Args: args}}}}, TurnComplete: true}, nil
					}
					if i == 1 {
						text := "done"
						if path == "react" {
							text = "Final Answer: done"
						}
						return &model.LLMResponse{Content: genai.NewContentFromText(text, genai.RoleModel), TurnComplete: true}, nil
					}
					return nil, fmt.Errorf("unexpected round")
				}}
				a := &AstonishAgent{LLM: llm, Tools: []tool.Tool{executable}, AutoApprove: true}
				if !allowed {
					a.CredentialStore = botForbiddenCredentialResolver{t}
				} else {
					cs, err := credentials.Open(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					if err := cs.Set("private", &credentials.Credential{Type: credentials.CredBearer, Token: "bot-fixture-secret"}); err != nil {
						t.Fatal(err)
					}
					a.CredentialStore = cs
				}
				node := &config.Node{Name: "bot_node", Type: "llm", Prompt: "read", Tools: true, ToolsSelection: []string{"read_file"}, ToolsAutoApproval: true, Args: args}
				state := NewMockState()
				yield := func(_ *session.Event, _ error) bool { return true }
				switch path {
				case "direct":
					a.handleToolNode(base, node, state, yield)
				case "flow":
					a.SessionService = &MockSessionService{State: state}
					_, _ = a.executeLLMNodeAttempt(&MockInvocationContext{Context: base, StateVal: state}, node, node.Name, state, yield)
				case "react":
					_, _ = a.executeReActFallback(base, node, node.Name, state, yield, a.Tools, "read", "read")
				case "native", "native_bridge":
					svc := session.InMemoryService()
					nativeTools := []tool.Tool{executable}
					idx := syncTestToolIndex(t, &ToolGroup{Name: group, Tools: nativeTools})
					if bridge {
						var err error
						nativeTools, err = NewProgressiveToolBridge(idx)
						if err != nil {
							t.Fatal(err)
						}
					}
					chat := NewChatAgent(llm, nativeTools, nil, svc, &SystemPromptBuilder{CustomPrompt: "CORE"}, false, false)
					chat.AutoApprove, chat.ToolIndex, chat.CredentialStore = true, idx, a.CredentialStore
					runner := memoryTestRunner(t, chat, svc)
					for range runner.Run(base, "u", "bot-native", genai.NewContentFromText("read", genai.RoleUser), agent.RunConfig{}) {
					}
				case "child", "child_bridge":
					mgr := NewSubAgentManager(SubAgentConfig{MaxRetries: 1})
					mgr.AppName, mgr.UserID, mgr.LLM = "test", "user", llm
					mgr.SessionService, mgr.CredentialStore = session.InMemoryService(), a.CredentialStore
					mgr.ToolGroups = map[string]*ToolGroup{group: {Name: group, Tools: []tool.Tool{executable}, Toolsets: sets}}
					_ = mgr.RunTask(base, SubAgentTask{Name: "reader", Description: "read", ParentID: "parent", ToolFilter: []string{BotToolIdentity(group, "read_file")}})
				}
				want := int32(0)
				if allowed {
					want = 1
				}
				wantClassifications := want
				if qualified {
					// Lazy MCP revalidates the full classified action at its terminal
					// boundary without consuming the approval a second time.
					wantClassifications += want
				}
				if calls.Load() != want || guards.Load() != want || classifications.Load() != wantClassifications {
					t.Fatalf("calls=%d guards=%d classifications=%d; want %d", calls.Load(), guards.Load(), classifications.Load(), want)
				}
			})
		}
	}
}
