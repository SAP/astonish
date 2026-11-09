package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

type botClassifierFunc func(context.Context, string, json.RawMessage) (execution.ActionDescriptor, error)

func (f botClassifierFunc) DescribeAction(ctx context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	return f(ctx, name, args)
}

type botGuardFunc func(context.Context, execution.ActionDescriptor) (execution.PolicyDecision, error)

func (f botGuardFunc) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	return f(ctx, a)
}

type botApprovalFunc func(context.Context, execution.BotRunIdentity, execution.ActionDescriptor, string) (uuid.UUID, error)

func (f botApprovalFunc) RequestApproval(ctx context.Context, id execution.BotRunIdentity, a execution.ActionDescriptor, digest string) (uuid.UUID, error) {
	return f(ctx, id, a, digest)
}

func botPolicyContext(t *testing.T, ctx context.Context, names []string, grants []execution.ScopedGrant) context.Context {
	t.Helper()
	id, _ := execution.BotRunIdentityFromContext(ctx)
	p, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomyScoped, AllowedTools: names, Grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	return execution.WithRunPolicy(ctx, p)
}

func botVerifiedRead(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
}

func TestBotPolicyUnknownEffectsRequireReview(t *testing.T) {
	ctx := botPolicyContext(t, botBoundaryContext(t), []string{"read_file"}, nil)
	requests, guards := 0, 0
	runtime := BotPolicyRuntime{
		Guard: botGuardFunc(func(context.Context, execution.ActionDescriptor) (execution.PolicyDecision, error) {
			guards++
			return execution.PolicyDecision{Kind: execution.PolicyAllow}, nil
		}),
		Approvals: botApprovalFunc(func(_ context.Context, _ execution.BotRunIdentity, a execution.ActionDescriptor, digest string) (uuid.UUID, error) {
			requests++
			want, _ := a.Digest()
			if digest != want || a.Effect != execution.EffectUnknown {
				t.Fatalf("review not exact unknown action: %+v", a)
			}
			return uuid.New(), nil
		}),
	}
	err := AuthorizeBotTool(WithBotPolicyRuntime(ctx, runtime), "read_file", map[string]any{"effect": "read", "constraints_verified": true, "auto_approve": true})
	var blocked *BotPolicyError
	if !errors.As(err, &blocked) || !blocked.ApprovalRequired() || requests != 1 || guards != 0 {
		t.Fatalf("err=%v requests=%d guards=%d", err, requests, guards)
	}
}

func TestBotPolicyGuardMustAuthorizeExactAction(t *testing.T) {
	for _, mode := range []string{"missing", "failure", "not_applicable", "consume_only", "wrong_digest", "allow"} {
		t.Run(mode, func(t *testing.T) {
			ctx := botPolicyContext(t, botBoundaryContext(t), []string{"read_file"}, nil)
			runtime := BotPolicyRuntime{Classifier: botClassifierFunc(botVerifiedRead)}
			if mode != "missing" {
				runtime.Guard = botGuardFunc(func(_ context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
					digest, _ := a.Digest()
					d := execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest}
					switch mode {
					case "failure":
						return d, errors.New("lease expired")
					case "not_applicable":
						d.Kind = execution.PolicyNotApplicable
					case "consume_only":
						d.Kind = execution.PolicyConsumeGrant
					case "wrong_digest":
						d.ActionDigest = "other"
					}
					return d, nil
				})
			}
			err := AuthorizeBotTool(WithBotPolicyRuntime(ctx, runtime), "read_file", map[string]any{"path": "notes"})
			if (err == nil) != (mode == "allow") {
				t.Fatalf("%s err=%v", mode, err)
			}
		})
	}
}

func TestBotPolicyClassifierCannotRewriteAction(t *testing.T) {
	for _, mode := range []string{"tool", "arguments", "error", "invalid_effect"} {
		t.Run(mode, func(t *testing.T) {
			ctx := botPolicyContext(t, botBoundaryContext(t), []string{"read_file"}, nil)
			runtime := BotPolicyRuntime{Classifier: botClassifierFunc(func(ctx context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
				a, _ := botVerifiedRead(ctx, name, args)
				switch mode {
				case "tool":
					a.Tool = "write_file"
				case "arguments":
					a.Arguments = json.RawMessage(`{"path":"other"}`)
				case "error":
					return a, errors.New("unclassifiable")
				case "invalid_effect":
					a.Effect = "safe"
				}
				return a, nil
			})}
			if err := AuthorizeBotTool(WithBotPolicyRuntime(ctx, runtime), "read_file", map[string]any{"path": "notes"}); err == nil {
				t.Fatal("rewritten action allowed")
			}
		})
	}
}

func TestBotPolicyApprovalGrantConsumedOnceAcrossProgressiveLazy(t *testing.T) {
	for _, boundary := range []string{"bridge", "executor", "lazy"} {
		t.Run(boundary, func(t *testing.T) {
			base := botBoundaryContext(t)
			id, _ := execution.BotRunIdentityFromContext(base)
			args := map[string]any{"recipient": "alice", "body": "hello", "id": json.Number("9007199254740993")}
			raw, _ := json.Marshal(args)
			action := execution.ActionDescriptor{Tool: "mcp:mail/send_email", Effect: execution.EffectUnknown, Arguments: raw}
			digest, _ := action.Digest()
			grant := execution.ScopedGrant{ID: uuid.New(), Identity: id, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)}
			base = botPolicyContext(t, base, []string{action.Tool}, []execution.ScopedGrant{grant})
			var used atomic.Bool
			guards, calls := 0, 0
			base = WithBotPolicyRuntime(base, BotPolicyRuntime{Guard: botGuardFunc(func(_ context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
				guards++
				got, _ := a.Digest()
				if got != digest {
					t.Fatalf("guard wrong digest %s", got)
				}
				if !used.CompareAndSwap(false, true) {
					return execution.PolicyDecision{}, errors.New("already consumed")
				}
				return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: got, GrantID: grant.ID}, nil
			})})
			inner, err := functiontool.New(functiontool.Config{Name: "send_email", Description: "Send"}, func(_ tool.Context, _ map[string]any) (map[string]any, error) {
				calls++
				return map[string]any{"sent": true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			ts := NewLazyMCPToolset("mail", []cache.ToolEntry{{Name: "send_email"}}, config.MCPServerConfig{}, false)
			ts.started, ts.liveTools = true, map[string]tool.Tool{"send_email": inner}
			ctx := &contextToolContext{minimalReadonlyContext{Context: base}}
			proxies, _ := ts.Tools(ctx)
			idx := syncTestToolIndex(t, &ToolGroup{Name: "mcp:mail", Tools: proxies})
			bridge, _ := NewProgressiveToolBridge(idx)
			invoke := func(arguments map[string]any) error {
				switch boundary {
				case "bridge":
					_, err := bridge[1].(runnableDeferredTool).Run(ctx, map[string]any{"name": "mcp:mail/send_email", "arguments": arguments})
					return err
				case "executor":
					result := NewProgressiveToolExecutor(&ChatAgent{ToolIndex: idx, AutoApprove: true}).Execute(ctx, "mcp:mail/send_email", arguments)
					if result.State != ProgressiveStateReady {
						return errors.New(string(result.State))
					}
					return nil
				default:
					_, err := proxies[0].(runnableDeferredTool).Run(ctx, arguments)
					return err
				}
			}
			changed := map[string]any{"recipient": "bob", "body": "hello", "id": json.Number("9007199254740993")}
			if err := invoke(changed); err == nil || calls != 0 || guards != 0 {
				t.Fatalf("changed recipient bypass: calls=%d guards=%d err=%v", calls, guards, err)
			}
			if err := invoke(args); err != nil || calls != 1 || guards != 1 {
				t.Fatalf("first exact call: calls=%d guards=%d err=%v", calls, guards, err)
			}
			if err := invoke(args); err == nil || calls != 1 || guards != 2 {
				t.Fatalf("replay: calls=%d guards=%d err=%v", calls, guards, err)
			}
		})
	}
}

func TestBotPolicyLazyDeniedBeforeServerStartup(t *testing.T) {
	ctx := &contextToolContext{minimalReadonlyContext{Context: botBoundaryContext(t)}}
	ts := NewLazyMCPToolset("mail", []cache.ToolEntry{{Name: "read_file"}}, config.MCPServerConfig{}, false)
	proxies, _ := ts.Tools(ctx)
	_, err := proxies[0].(runnableDeferredTool).Run(ctx, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "bot policy") || ts.started {
		t.Fatalf("startup occurred before denial: started=%v err=%v", ts.started, err)
	}
}

func TestBotPolicyLazyHandoffCannotWidenOrReplay(t *testing.T) {
	base := botPolicyContext(t, botBoundaryContext(t), []string{"mcp:mail/read_file"}, nil)
	base = WithBotPolicyRuntime(base, BotPolicyRuntime{Classifier: botClassifierFunc(botVerifiedRead), Guard: botGuardFunc(func(_ context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
		d, _ := a.Digest()
		return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: d}, nil
	})})
	for _, mode := range []string{"arguments", "identity", "policy", "replay"} {
		t.Run(mode, func(t *testing.T) {
			ctx := &contextToolContext{minimalReadonlyContext{Context: base}}
			args := map[string]any{"path": "notes"}
			authorized, err := authorizeBotInvocation(ctx, "mcp:mail/read_file", args)
			if err != nil {
				t.Fatal(err)
			}
			name := "mcp:mail/read_file"
			switch mode {
			case "arguments":
				args["path"] = "other"
			case "identity":
				name = "mcp:other/read_file"
			case "policy":
				child, _ := execution.RunPolicyFromContext(base).Narrow([]string{})
				authorized = &botPolicyToolContext{Context: authorized, context: execution.WithRunPolicy(authorized, child)}
			case "replay":
				if err := authorizeBotLazyInvocation(authorized, name, args); err != nil {
					t.Fatal(err)
				}
			}
			if err := authorizeBotLazyInvocation(authorized, name, args); err == nil {
				t.Fatal("handoff bypass")
			}
		})
	}
}

type botForbiddenCredentialResolver struct{ t *testing.T }

func (r botForbiddenCredentialResolver) Get(string) *credentials.Credential {
	r.t.Fatal("credentials resolved before Bot authorization")
	return nil
}
func (r botForbiddenCredentialResolver) Resolve(string) (string, string, error) {
	r.t.Fatal("credentials resolved before Bot authorization")
	return "", "", nil
}
func (r botForbiddenCredentialResolver) Reload() error {
	r.t.Fatal("credentials reloaded before Bot authorization")
	return nil
}

func TestBotPolicyMCPAliasCannotBorrowNativeAuthority(t *testing.T) {
	for _, boundary := range []string{"bridge", "executor", "lazy"} {
		t.Run(boundary, func(t *testing.T) {
			base := botPolicyContext(t, botBoundaryContext(t), []string{"read_file"}, nil)
			base = WithBotPolicyRuntime(base, BotPolicyRuntime{Classifier: botClassifierFunc(func(context.Context, string, json.RawMessage) (execution.ActionDescriptor, error) {
				t.Fatal("MCP identity passed native tool ceiling")
				return execution.ActionDescriptor{}, nil
			})})
			ctx := &contextToolContext{minimalReadonlyContext{Context: base}}
			ts := NewLazyMCPToolset("mail", []cache.ToolEntry{{Name: "read_file"}}, config.MCPServerConfig{}, false)
			proxies, _ := ts.Tools(ctx)
			idx := syncTestToolIndex(t, &ToolGroup{Name: "mcp:mail", Tools: proxies})
			args := map[string]any{"path": "{{CREDENTIAL:private:token}}"}
			switch boundary {
			case "bridge":
				bridge, _ := NewProgressiveToolBridge(idx)
				if _, err := bridge[1].(runnableDeferredTool).Run(ctx, map[string]any{"name": "read_file", "arguments": args}); err == nil {
					t.Fatal("unqualified MCP alias bypassed native ceiling")
				}
			case "executor":
				result := NewProgressiveToolExecutor(&ChatAgent{ToolIndex: idx, AutoApprove: true, CredentialStore: botForbiddenCredentialResolver{t}}).Execute(ctx, "read_file", args)
				if result.State != ProgressiveStateAuthorizationDenied {
					t.Fatalf("state=%s", result.State)
				}
			default:
				if _, err := proxies[0].(runnableDeferredTool).Run(ctx, args); err == nil {
					t.Fatal("direct MCP invocation borrowed native ceiling")
				}
			}
			if ts.started {
				t.Fatal("denied invocation started MCP server")
			}
		})
	}
}

func botBoundaryContext(t *testing.T) context.Context {
	t.Helper()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: "owner", OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	return execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: "owner", OrgSlug: "org", TeamSlug: "team"})
}

func TestBotPolicyBoundariesMissingPolicyCannotExecute(t *testing.T) {
	for _, boundary := range []string{"bridge", "executor", "lazy"} {
		t.Run(boundary, func(t *testing.T) {
			calls := 0
			inner, err := functiontool.New(functiontool.Config{Name: "send_email", Description: "Read-only harmless tool"}, func(_ tool.Context, _ map[string]any) (map[string]any, error) {
				calls++
				return map[string]any{"sent": true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := &contextToolContext{minimalReadonlyContext{Context: botBoundaryContext(t)}}
			idx := syncTestToolIndex(t, &ToolGroup{Name: "core", Tools: []tool.Tool{inner}})
			switch boundary {
			case "bridge":
				bridge, err := NewProgressiveToolBridge(idx)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = bridge[1].(runnableDeferredTool).Run(ctx, map[string]any{"name": "send_email", "arguments": map[string]any{"auto_approve": true, "effect": "read"}})
			case "executor":
				result := NewProgressiveToolExecutor(&ChatAgent{ToolIndex: idx, AutoApprove: true}).Execute(ctx, "send_email", map[string]any{"auto_approve": true})
				if result.State != ProgressiveStateAuthorizationDenied {
					t.Errorf("state=%s, want authorization_denied", result.State)
				}
			case "lazy":
				ts := NewLazyMCPToolset("mail", []cache.ToolEntry{{Name: "send_email"}}, config.MCPServerConfig{}, false)
				ts.started, ts.liveTools = true, map[string]tool.Tool{"send_email": inner}
				proxies, _ := ts.Tools(ctx)
				_, _ = proxies[0].(runnableDeferredTool).Run(ctx, map[string]any{"auto_approve": true})
			}
			if calls != 0 {
				t.Fatalf("Bot bypass executed %d action(s) without a policy", calls)
			}
		})
	}
}
