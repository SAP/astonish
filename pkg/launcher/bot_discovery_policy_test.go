package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

func discoveryMandateFixture(t *testing.T) (*botFactoryFixture, *factoryMandateRuntime, *int) {
	t.Helper()
	f := newBotFactoryFixture(t)
	id := f.req.Policy.Identity()
	runtime := &factoryMandateRuntime{saved: execution.BotMandate{Ref: execution.BotMandateRef{ID: uuid.New(), BotID: id.BotID, OwnerID: id.ActorID, OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, Revision: 1}, Purpose: execution.BotMandatePurposeTask, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{"task"}}, {Action: execution.CapabilityToolExecute, Resources: []string{"ask_user"}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig())}}
	auth, err := execution.VerifyBotMandateAuthorization(context.Background(), runtime, runtime.saved.Ref, execution.BotMandatePurposeTask)
	if err != nil {
		t.Fatal(err)
	}
	f.req.Principal = auth.Principal(execution.SurfaceStudio)
	f.ctx, err = execution.WithPrincipal(context.Background(), f.req.Principal)
	if err != nil {
		t.Fatal(err)
	}
	f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"ask_user"}})
	if err != nil {
		t.Fatal(err)
	}
	f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: runtime, Identity: id}
	f.req.Run.MandateRef = &runtime.saved.Ref
	f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
	f.req.Instructions = "send this here every minute"
	calls := new(int)
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, scope)
		if err != nil {
			return nil, err
		}
		for _, name := range []string{"ask_user", "shell_command"} {
			target, err := functiontool.New(functiontool.Config{Name: name, Description: "ask user a clarifying question with choices"}, func(c tool.Context, _ map[string]any) (map[string]any, error) {
				if name != "ask_user" {
					t.Error("excluded target executed")
				}
				*calls++
				bot.RecordToolOutcome(c, c.FunctionCallID(), bot.ToolOutcome{Verified: true, Unchanged: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: "Controlled permitted action observed", Source: "fixture://permitted-action", ActorID: f.req.Bot.OwnerID, ObservedAt: time.Now().UTC()}}})
				return map[string]any{"fixture_question": true}, nil
			})
			if err != nil {
				return nil, err
			}
			safe.Tools = append(safe.Tools, target)
		}
		return safe, nil
	}
	return f, runtime, calls
}

type discoveryContractModel struct {
	t                 *testing.T
	calls             int
	finishAfterAction bool
}

func (*discoveryContractModel) Name() string { return "discovery-contract" }
func (m *discoveryContractModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls > 1 {
			names := []string{"search_tools", "describe_tools", "execute_tool", "execute_tool"}
			want := names[m.calls-2]
			var response map[string]any
			for _, c := range req.Contents {
				for _, p := range c.Parts {
					if p.FunctionResponse != nil && p.FunctionResponse.Name == want {
						response = p.FunctionResponse.Response
					}
				}
			}
			raw, _ := json.Marshal(response)
			switch m.calls {
			case 2:
				if !strings.Contains(string(raw), `"tool_name":"ask_user"`) || strings.Contains(string(raw), "shell_command") {
					m.t.Errorf("search did not return only scoped clarification capability: %s", raw)
				}
			case 3:
				if !strings.Contains(string(raw), `"name":"ask_user"`) || strings.Contains(string(raw), `"name":"shell_command"`) {
					m.t.Errorf("describe lost scoped capability: %s", raw)
				}
			case 4:
				if !strings.Contains(string(raw), `"fixture_question":true`) {
					m.t.Errorf("allowed target did not execute: %s", raw)
				}
			case 5:
				if !strings.Contains(string(raw), "not found") {
					m.t.Errorf("excluded target not rejected: %s", raw)
				}
			}
		}
		if m.calls == 4 && m.finishAfterAction {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Discovery checked; no schedule created.", "model"), TurnComplete: true}, nil)
			return
		}
		var name string
		var args map[string]any
		switch m.calls {
		case 1:
			name, args = "search_tools", map[string]any{"query": "ask user a clarifying question with choices"}
		case 2:
			name, args = "describe_tools", map[string]any{"names": []any{"ask_user", "shell_command"}}
		case 3:
			name, args = "execute_tool", map[string]any{"name": "ask_user", "arguments": map[string]any{}}
		case 4:
			name, args = "execute_tool", map[string]any{"name": "shell_command", "arguments": map[string]any{}}
		default:
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Discovery checked; no schedule created.", "model"), TurnComplete: true}, nil)
			return
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("discovery-%d", m.calls), Name: name, Args: args}}}}, TurnComplete: true}, nil)
	}
}

func TestBotDiscoveryLiveBinding(t *testing.T) {
	for _, mode := range []string{"mandate", "interactive"} {
		for _, failure := range []string{"revoked", "disabled", "cancelled", "lease", "caller_scope"} {
			if mode == "mandate" && failure == "caller_scope" {
				continue // This restriction belongs to ephemeral caller authority.
			}
			t.Run(mode+"/"+failure, func(t *testing.T) {
				f, runtime, _ := discoveryMandateFixture(t)
				owner := botFactoryOwnerContext(f.ctx, f.req)
				var registry *execution.BotTurnAuthorizationRegistry
				if mode == "interactive" {
					f.req.Mandate = nil
					f.req.Run.MandateRef = nil
					f.req.Policy = nil
					p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceStudio, Subject: f.req.Bot.OwnerID.String(), OrgSlug: "owner-org", TeamSlug: "owner-team", Authenticated: true, Scopes: []string{"chat", "tool:execute"}}
					if failure == "caller_scope" {
						p.Scopes = []string{"chat"}
					}
					f.req.Principal = p
					f.ctx, _ = execution.WithPrincipal(context.Background(), p)
					f.ctx, _ = execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
					id := f.req.Mandate // no ephemeral mandate may be retained
					_ = id
					identity := execution.BotRunIdentity{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Version.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
					registry = execution.NewBotTurnAuthorizationRegistry(2)
					admission, err := registry.Register(f.ctx, identity)
					if err != nil {
						t.Fatal(err)
					}
					ceiling := admission.CallerCeiling()
					f.req.Run.CallerCeiling = &ceiling
					f.req.Authorization, err = registry.Authorizer(identity, ceiling, func(_ context.Context, p execution.Principal) (execution.Principal, error) { return p, nil })
					if err != nil {
						t.Fatal(err)
					}
					owner = botFactoryOwnerContext(f.ctx, f.req)
				}
				owner, _ = bot.WithToolOutcomes(store.WithServices(owner, f.services))
				binding := &botFactoryBinding{owner: owner, req: f.req, verifyIsolation: func(context.Context) error { return nil }}
				bridge, err := botProviderTools(agent.NewLexicalToolIndex(), binding)
				if err != nil {
					t.Fatal(err)
				}
				ledgerContext := bot.WithToolOutcomesFromContext(context.Background(), owner)
				ctx := botFactoryHandoffContext{base: ledgerContext, session: f.req.Run.SessionID.String(), functionCallID: "metadata-call"}
				for _, helper := range bridge[:2] {
					args := map[string]any{"query": "*"}
					if helper.Name() == "describe_tools" {
						args = map[string]any{"names": []any{"shell_command"}}
					}
					_, err := helper.(botRunnableTool).Run(ctx, args)
					outcome, observed := bot.TakeToolOutcome(ledgerContext, ctx.FunctionCallID())
					if observed != (err == nil) || (observed && (!outcome.Verified || len(outcome.Unchanged) != 1 || len(outcome.Changed) != 0)) {
						t.Fatalf("metadata ledger: observed=%v outcome=%+v err=%v", observed, outcome, err)
					}
					if mode == "interactive" && failure == "caller_scope" {
						if err == nil {
							t.Fatal("chat-only caller discovered tools")
						}
						continue
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				switch failure {
				case "revoked":
					if registry != nil {
						identity, _ := execution.BotRunIdentityFromContext(owner)
						registry.Cancel(identity)
					} else {
						runtime.revoked = true
					}
				case "disabled":
					binding.owner = store.WithDisabledTools(owner, []string{"search_tools", "describe_tools"})
				case "cancelled":
					c, cancel := context.WithCancel(ledgerContext)
					cancel()
					ctx.base = c
				case "lease":
					f.req.Run.Lease.Fence++
				}
				for _, helper := range bridge[:2] {
					if _, err := helper.(botRunnableTool).Run(ctx, map[string]any{"query": "*"}); err == nil {
						t.Fatalf("%s ignored %s", helper.Name(), failure)
					}
					if outcome, ok := bot.TakeToolOutcome(ledgerContext, ctx.FunctionCallID()); ok {
						t.Fatalf("failed discovery minted success: %+v", outcome)
					}
				}
			})
		}
	}
}

// Reproduce the live Home schedule turn through the actual factory and native
// callback chain, not just membership in Agent.Tools. No provider or daemon runs.
func TestBotMandateDiscoveryNativeContract(t *testing.T) {
	f, _, calls := discoveryMandateFixture(t)
	llm := &discoveryContractModel{t: t}
	f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
	factory, err := NewBotChatAgentFactory(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := api.NewBotChatRuntimeAdapter(factory)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err == nil || result.Status == store.RunSucceeded {
		t.Fatalf("excluded action was not rejected: result=%+v err=%v", result, err)
	}
	for _, unverified := range result.Report.Unverified {
		if strings.Contains(unverified, "search_tools") || strings.Contains(unverified, "describe_tools") {
			t.Fatalf("successful metadata was unverified: %s", unverified)
		}
	}
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			if line != `tool execution failed: tool "shell_command" was not found in the available catalog` {
				t.Fatalf("unexpected runtime error: %s", line)
			}
		}
	}
	if llm.calls != 5 || *calls != 1 {
		t.Fatalf("model rounds=%d target calls=%d", llm.calls, *calls)
	}
}
