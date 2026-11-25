package daemon

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// The daemon must borrow the exact bundle captured by the ordinary Chat factory,
// not reconstruct an incomplete capability list from its pool.
func TestProductionBotTemplateAssemblySource(t *testing.T) {
	tree, err := parser.ParseFile(token.NewFileSet(), "run.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(tree, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "productionBotChatResources" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("production Run reconstructs Bot dependencies instead of borrowing Chat's complete template bundle")
	}
}

// Controlled backend transport: no container, user Bot, or host commands are
// executed. All tool implementations/catalog/dispatch are production Chat code.
type templateWiringBackend struct {
	sandbox.Backend
	destroyed []string
}

func (b *templateWiringBackend) Kind() sandbox.BackendKind { return sandbox.BackendKindDocker }

func (b *templateWiringBackend) DestroySession(_ context.Context, sid string) error {
	b.destroyed = append(b.destroyed, sid)
	return nil
}

type templateWiringPool struct {
	intentHTTPPool
	backend                  sandbox.Backend
	selected, sid, org, team string
	closed                   bool
}

func (p *templateWiringPool) GetBackend() sandbox.Backend { return p.backend }
func (p *templateWiringPool) GetOrCreateWithTemplate(sid, name string) sandbox.ToolNodeClient {
	p.sid, p.selected = sid, name
	return p
}
func (p *templateWiringPool) SetSessionScope(sid, org, team string) {
	p.sid, p.org, p.team = sid, org, team
}
func (p *templateWiringPool) Remove(string) {}
func (p *templateWiringPool) Cleanup()      { p.closed = true }

// This is a test-owned exact-action authorization hook, not a new production
// policy. Shared dispatch must run it before the same concrete template tool.
type templateWiringAuthorization struct{ expected map[string]string }

func (a templateWiringAuthorization) Authorize(ctx context.Context, name string, args any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	if a.expected[name] != string(raw) {
		return store.ErrBotForbidden
	}
	return agent.AuthorizeBotTool(ctx, name, args)
}

type templateWiringGrantGuard struct{ consumed map[uuid.UUID]bool }

func (g *templateWiringGrantGuard) Authorize(ctx context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	decision := execution.EvaluateRunPolicy(ctx, action, time.Now())
	if decision.Kind != execution.PolicyConsumeGrant || g.consumed[decision.GrantID] {
		return execution.PolicyDecision{}, store.ErrBotForbidden
	}
	g.consumed[decision.GrantID] = true
	decision.Kind = execution.PolicyAllow
	return decision, nil
}

func TestProductionBotTemplateWiringScopedDispatch(t *testing.T) {
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"ordinary", "interactive", "autonomous"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				b := &templateWiringBackend{}
				p := &templateWiringPool{backend: b}
				registry := sandbox.NewInMemoryRegistry(t.TempDir())
				if err := registry.Add(&sandbox.TemplateMeta{Name: "selected-fixture", CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
				sessions, err := sandbox.NewLocalSessionStore(filepath.Join(t.TempDir(), "sessions"))
				if err != nil {
					t.Fatal(err)
				}
				sessRegistry := sandbox.NewSessionRegistryFromStore(sessions)
				bundle := &launcher.ChatCatalogResources{Backend: b, Pool: p, TemplateRegistry: registry, SessionRegistry: sessRegistry}
				result := &launcher.ChatFactoryResult{SharedResources: bundle, SandboxPool: p}
				shared := productionBotChatResources(result)
				if shared == nil || shared.TemplateRegistry != registry || shared.SessionRegistry != sessRegistry || shared.Backend != b || shared.Pool != p {
					t.Fatal("production assembly lost shared template dependencies")
				}
				shared.Prepared = true
				if bundle.Prepared {
					t.Fatal("attempt mutation changed bootstrap bundle")
				}
				shared.Prepared = false
				principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: uuid.NewString(), OrgSlug: "fixture-org", TeamSlug: "fixture-team", Authenticated: true}
				ctx, err := execution.WithPrincipal(t.Context(), principal)
				if err != nil {
					t.Fatal(err)
				}
				var revoke func()
				var grantGuard *templateWiringGrantGuard
				id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: principal.Subject, OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug}
				if mode == "interactive" {
					ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
					if err != nil {
						t.Fatal(err)
					}
					turns := execution.NewBotTurnAuthorizationRegistry(2)
					defer turns.Close()
					admission, err := turns.Register(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					auth, err := turns.Authorizer(id, admission.CallerCeiling(), func(_ context.Context, p execution.Principal) (execution.Principal, error) { return p, nil })
					if err != nil {
						t.Fatal(err)
					}
					ctx = execution.WithBotTurnAuthorizer(execution.WithBotRunIdentity(ctx, id), auth)
					revoke = func() { turns.Cancel(id) }
				} else if mode == "autonomous" {
					grants := []execution.ScopedGrant{}
					for name, args := range map[string]string{"list_sandbox_templates": "{}", "use_sandbox_template": `{"template_name":"selected-fixture"}`} {
						digest, err := (execution.ActionDescriptor{Tool: name, Effect: execution.EffectUnknown, Arguments: json.RawMessage(args)}).Digest()
						if err != nil {
							t.Fatal(err)
						}
						grants = append(grants, execution.ScopedGrant{ID: uuid.New(), Identity: id, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
					}
					policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"list_sandbox_templates", "use_sandbox_template"}, Grants: grants})
					if err != nil {
						t.Fatal(err)
					}
					grantGuard = &templateWiringGrantGuard{consumed: map[uuid.UUID]bool{}}
					ctx = agent.WithBotPolicyRuntime(execution.WithRunPolicy(execution.WithBotRunIdentity(ctx, id), policy), agent.BotPolicyRuntime{Guard: grantGuard})
				}
				cfg := &config.AppConfig{}
				inputs, err := chatruntime.Build(ctx, chatruntime.BuildOptions{Principal: principal, Services: &store.Services{}, ResourceScope: scope, AppConfig: cfg, Pool: p, FallbackPolicy: chatruntime.FallbackForbidden})
				if err != nil {
					t.Fatal(err)
				}
				defer inputs.Close()
				hooks := launcher.ChatRuntimeHooks{}
				if mode != "ordinary" {
					hooks.Authorization = templateWiringAuthorization{expected: map[string]string{"list_sandbox_templates": "{}", "use_sandbox_template": `{"template_name":"selected-fixture"}`}}
				}
				sid := uuid.NewString()
				runtime, err := launcher.NewScopedChatRuntime(ctx, launcher.ChatRuntimeDependencies{Inputs: inputs, Config: &launcher.ChatFactoryConfig{AppConfig: cfg}, LLM: provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(), SessionID: sid, Resources: shared}, hooks)
				if err != nil {
					t.Fatal(err)
				}
				defer runtime.Cleanup()
				if len(b.destroyed) != 0 || p.selected != "" {
					t.Fatal("construction triggered template work")
				}
				invoke := func(base context.Context, name string, args map[string]any) (map[string]any, error) {
					entry := runtime.ChatAgent.ToolIndex.GetToolEntry(name)
					if entry == nil {
						t.Fatalf("production template capability not discoverable: %s", name)
					}
					return entry.Tool.(interface {
						Run(tool.Context, any) (map[string]any, error)
					}).Run(workerAuthorityToolContext{base: base, sid: sid, uid: principal.Subject}, args)
				}
				for _, name := range []string{"list_sandbox_templates", "use_sandbox_template", "save_sandbox_template"} {
					if runtime.ChatAgent.ToolIndex.GetToolEntry(name) == nil {
						t.Fatalf("missing %s", name)
					}
				}
				if mode == "autonomous" {
					noGrant, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"list_sandbox_templates", "use_sandbox_template"}})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := invoke(execution.WithRunPolicy(ctx, noGrant), "use_sandbox_template", map[string]any{"template_name": "selected-fixture"}); err == nil || len(b.destroyed) != 0 || len(grantGuard.consumed) != 0 {
						t.Fatal("allowlist without exact grant executed template switch")
					}
				}
				out, err := invoke(ctx, "list_sandbox_templates", map[string]any{})
				raw, _ := json.Marshal(out)
				if err != nil || !strings.Contains(string(raw), "selected-fixture") {
					t.Fatalf("list: %s %v", raw, err)
				}
				if mode != "ordinary" {
					if _, err := invoke(ctx, "use_sandbox_template", map[string]any{"template_name": "unreviewed"}); err == nil || len(b.destroyed) != 0 {
						t.Fatal("changed template bypassed exact authorization")
					}
				}
				foreign := principal
				foreign.TeamSlug = "foreign-team"
				foreignCtx, err := execution.WithPrincipal(t.Context(), foreign)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := invoke(foreignCtx, "use_sandbox_template", map[string]any{"template_name": "selected-fixture"}); err == nil || len(b.destroyed) != 0 {
					t.Fatal("foreign scope reached template backend")
				}
				out, err = invoke(ctx, "use_sandbox_template", map[string]any{"template_name": "selected-fixture"})
				if err != nil || out["status"] != "ok" {
					t.Fatalf("selected dispatch: %v %v", out, err)
				}
				if len(b.destroyed) != 1 || b.destroyed[0] != sid || p.selected != "selected-fixture" || p.sid != sid || p.org != principal.OrgSlug || p.team != principal.TeamSlug {
					t.Fatalf("wrong template/session/scope: backend=%v pool=%+v", b.destroyed, p)
				}
				if revoke != nil {
					revoke()
					if _, err := invoke(ctx, "use_sandbox_template", map[string]any{"template_name": "selected-fixture"}); err == nil {
						t.Fatal("revoked interactive authority executed")
					}
				}
				if grantGuard != nil {
					if len(grantGuard.consumed) != 2 {
						t.Fatal("exact grants not consumed")
					}
					if _, err := invoke(ctx, "use_sandbox_template", map[string]any{"template_name": "selected-fixture"}); err == nil {
						t.Fatal("single-use template grant replayed")
					}
				}
				if len(b.destroyed) != 1 {
					t.Fatal("denied replay touched backend")
				}
				runtime.Cleanup()
				if p.closed {
					t.Fatal("attempt closed borrowed pool")
				}
			})
		}
	}
}

func TestProductionBotTemplateBootstrapCapture(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	enabled, disabled := true, false
	cfg := &config.AppConfig{Sandbox: config.SandboxConfig{Enabled: &enabled, Backend: "mock"}}
	cfg.Skills.Enabled = &disabled
	result, err := launcher.NewWiredChatAgent(t.Context(), &launcher.ChatFactoryConfig{AppConfig: cfg, PlatformMode: true, AllowMissingProvider: true})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Cleanup()
	shared := productionBotChatResources(result)
	if shared == nil || shared.TemplateRegistry == nil || shared.SessionRegistry == nil || result.RuntimeFactory == nil {
		t.Fatal("ordinary bootstrap did not expose its complete runtime infrastructure")
	}
	if shared.TemplateRegistry != result.SharedResources.TemplateRegistry {
		t.Fatal("Bot rebuilt template registry")
	}
}

func TestProductionBotTemplateMissingBundleFailsClosed(t *testing.T) {
	p := &templateWiringPool{}
	for _, result := range []*launcher.ChatFactoryResult{nil, {SandboxPool: p}, {SandboxPool: p, SharedResources: &launcher.ChatCatalogResources{Pool: p}}} {
		if productionBotChatResources(result) != nil {
			t.Fatalf("missing concrete bundle accepted: %+v", result)
		}
	}
}
