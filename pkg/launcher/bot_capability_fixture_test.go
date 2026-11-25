package launcher

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
)

func installSharedEmailApprovalGuard(f *botFactoryFixture) {
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, scope)
		if err == nil {
			safe.PolicyRuntime.Guard = &mcpOnceGuard{}
		}
		return safe, err
	}
}

func approveSharedEmailAction(t *testing.T, f *botFactoryFixture, target botRunnableTool, ctx tool.Context, name string, args map[string]any) tool.Context {
	t.Helper()
	bound, release, err := target.(factoryBridgeTarget).botRunnableTool.(*chatRuntimeTool).binding.bind(ctx)
	defer release()
	if err != nil {
		t.Fatal(err)
	}
	account, err := scopedEmailAccount(ctx, f.services, f.req.Bot.Scope == store.BotScopePersonal)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(args)
	digest, err := (execution.ActionDescriptor{Tool: name, Arguments: raw, Effect: execution.EffectUnknown, TargetFingerprint: account.Fingerprint()}).Digest()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := f.req.Policy.WithGrant(execution.ScopedGrant{ID: uuid.New(), Identity: f.req.Policy.Identity(), ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return basicToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: execution.WithBotRunIdentity(execution.WithRunPolicy(bound, policy), f.req.Policy.Identity()), session: ctx.SessionID(), functionCallID: ctx.FunctionCallID()}}
}

// factoryBridgeTarget retains the target declaration but always invokes the real
// progressive bridge. No authorization or result transformation lives here.
type factoryBridgeTarget struct {
	botRunnableTool
	bridge botRunnableTool
}

func (t factoryBridgeTarget) Run(ctx tool.Context, args any) (map[string]any, error) {
	return t.bridge.Run(ctx, map[string]any{"name": t.Name(), "arguments": args})
}

// factoryCatalogTools enumerates real catalog entries while preserving progressive
// dispatch. This is only a test view; it does not change the provider tool list.
func factoryCatalogTools(t *testing.T, r *api.BotChatResources) []tool.Tool {
	t.Helper()
	var bridge botRunnableTool
	for _, candidate := range r.Agent.Tools {
		if candidate.Name() == "execute_tool" {
			bridge = candidate.(botRunnableTool)
		}
	}
	if bridge == nil {
		t.Fatal("shared progressive bridge missing")
	}
	var result []tool.Tool
	for _, group := range r.Agent.ToolIndex.ListAll() {
		for _, match := range group {
			entry := r.Agent.ToolIndex.GetToolEntry(match.ToolName)
			if entry != nil && entry.Tool != nil {
				result = append(result, factoryBridgeTarget{botRunnableTool: entry.Tool.(botRunnableTool), bridge: bridge})
			}
		}
	}
	return result
}

func productionFactoryTool(t *testing.T, f *botFactoryFixture, name string, resources ChatCatalogResources, additionalAllowed ...string) (botRunnableTool, tool.Context) {
	t.Helper()
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: append([]string{name}, additionalAllowed...)})
	if err != nil {
		t.Fatal(err)
	}
	f.req.Policy = policy
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, scope)
		if err != nil {
			return nil, err
		}
		if resources.Pool == nil {
			resources.Pool = safe.Catalog.Pool
		}
		safe.Catalog = &resources
		return safe, nil
	}
	r, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Cleanup)
	entry := r.Agent.ToolIndex.GetToolEntry(name)
	if entry == nil {
		t.Fatalf("shared catalog missing %s", name)
	}
	for _, candidate := range r.Agent.Tools {
		if candidate.Name() == "execute_tool" {
			return factoryBridgeTarget{botRunnableTool: entry.Tool.(botRunnableTool), bridge: candidate.(botRunnableTool)}, basicToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: f.ctx, session: f.req.Run.SessionID.String(), functionCallID: "production-" + name}}
		}
	}
	t.Fatal("shared progressive bridge missing")
	return nil, nil
}

// The legacy adapter tests above cannot establish that ordinary autonomous
// capabilities survived catalog consolidation. Exercise discovery independently
// with a configured, owner-scoped transport and no provider/network activity.
func TestBotSharedEmailCatalogConfiguredTransport(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.services.PersonalSettings = &emailPersonalSettings{settings: emailTestSettings()}
	f.services.PersonalCredentials = &workflowCredStore{entries: map[string]*store.Credential{"mail": {Type: store.CredBasic, Username: "owner@example.com", Password: "fixture-email-password"}}}
	backend := &productionCapabilityBackend{}
	client := &sourceAccessCatalogueClient{response: json.RawMessage(`{}`)}
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, scope)
		if err != nil {
			return nil, err
		}
		safe.Catalog.Backend = backend
		safe.Catalog.Pool = productionCapabilityPool{backend: backend, client: client}
		return safe, nil
	}
	r, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Cleanup()
	for _, name := range []string{"email_list", "email_read", "email_send", "email_reply"} {
		if r.Agent.ToolIndex.GetToolEntry(name) == nil {
			t.Errorf("configured shared Bot catalog missing %s", name)
		}
	}
	if client.calls != 0 || len(backend.vault) != 0 {
		t.Fatal("catalog discovery performed email IO")
	}
}

// Discovery alone is not acceptance: an autonomous call must reach the same
// scoped transport via the real progressive bridge, not a test-injected adapter.
func TestBotSharedEmailAutonomousDispatch(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.services.PersonalSettings = &emailPersonalSettings{settings: emailTestSettings()}
	f.services.PersonalCredentials = &workflowCredStore{entries: map[string]*store.Credential{"mail": {Type: store.CredBasic, Username: "owner@example.com", Password: "fixture-email-password"}}}
	backend := &productionCapabilityBackend{}
	client := &sourceAccessCatalogueClient{response: json.RawMessage(`{"messages":[]}`)}
	installSharedEmailApprovalGuard(f)
	target, ctx := productionFactoryTool(t, f, "email_list", ChatCatalogResources{Backend: backend, Pool: productionCapabilityPool{backend: backend, client: client}})
	if _, err := target.Run(ctx, map[string]any{}); err == nil || client.calls != 0 || len(backend.vault) != 0 {
		t.Fatalf("email read bypassed exact approval: %v", err)
	}
	ctx = approveSharedEmailAction(t, f, target, ctx, "email_list", map[string]any{})
	out, err := target.Run(ctx, map[string]any{})
	if err != nil || out["error"] != nil || client.calls != 1 {
		t.Fatalf("autonomous shared email dispatch: result=%v err=%v node_calls=%d", out, err, client.calls)
	}
}

// Missing transport must discard even an explicitly supplied bootstrap catalog.
func TestBotSharedEmailMissingTransportDoesNotInherit(t *testing.T) {
	f := newBotFactoryFixture(t)
	bootstrap, err := tools.GetEmailToolsForClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, scope)
		if err == nil {
			safe.Catalog.EmailTools = bootstrap
		}
		return safe, err
	}
	r, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Cleanup()
	if r.Agent.ToolIndex.GetToolEntry("email_list") != nil {
		t.Fatal("inherited bootstrap email without scoped transport")
	}
}

func TestBotSharedEmailMissingScopeNeverInherits(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, missing := range []string{"account", "credential"} {
			t.Run(string(scope)+"/"+missing, func(t *testing.T) {
				f := newBotFactoryFixture(t)
				f.req.Bot.Scope = scope
				settings := emailTestSettings()
				f.services.PersonalSettings = &emailPersonalSettings{settings: settings}
				f.services.Settings = &emailTeamSettings{settings: settings}
				own := sessioncreds.NewStore(map[string]*store.Credential{"mail": {Type: store.CredPassword, Password: productionTestSecret}})
				f.services.PersonalCredentials, f.services.Credentials = own, own
				if scope == store.BotScopePersonal {
					if missing == "account" {
						f.services.PersonalSettings = nil
					} else {
						f.services.PersonalCredentials = nil
					}
				} else if missing == "account" {
					f.services.Settings = nil
				} else {
					f.services.Credentials = nil
				}
				backend := &productionCapabilityBackend{}
				client := &sourceAccessCatalogueClient{response: json.RawMessage(`{"messages":[]}`)}
				installSharedEmailApprovalGuard(f)
				target, ctx := productionFactoryTool(t, f, "email_list", ChatCatalogResources{Backend: backend, Pool: productionCapabilityPool{backend: backend, client: client}})
				if missing == "credential" {
					ctx = approveSharedEmailAction(t, f, target, ctx, "email_list", map[string]any{})
				}
				if _, err := target.Run(ctx, map[string]any{}); err == nil || client.calls != 0 || len(backend.vault) != 0 {
					t.Fatalf("missing owner %s inherited opposite scope: %v calls=%d", missing, err, client.calls)
				}
			})
		}
	}
}

func TestBotSharedEmailSMTPExactApproval(t *testing.T) {
	tools.SetAllowPrivateNetworks(true)
	defer tools.SetAllowPrivateNetworks(false)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []string{"approved-once", "unapproved", "recipient-changed", "body-changed", "account-changed", "revoked"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				endpoint, messages := emailSMTPFixture(t, "owner", productionTestSecret)
				f := newBotFactoryFixture(t)
				f.req.Bot.Scope = scope
				settings := emailTestSettings()
				settings.SMTPServer = endpoint
				own := sessioncreds.NewStore(map[string]*store.Credential{"mail": {Type: store.CredPassword, Username: "owner", Password: productionTestSecret}})
				other := sessioncreds.NewStore(map[string]*store.Credential{"mail": {Type: store.CredPassword, Password: "wrong-scope-secret"}})
				if scope == store.BotScopePersonal {
					f.services.PersonalSettings = &emailPersonalSettings{settings: settings}
					f.services.PersonalCredentials, f.services.Credentials = own, other
				} else {
					f.services.Settings = &emailTeamSettings{settings: settings}
					f.services.Credentials, f.services.PersonalCredentials = own, other
				}
				backend := &productionCapabilityBackend{}
				client := &productionCapabilityClient{backend: backend}
				installSharedEmailApprovalGuard(f)
				target, ctx := productionFactoryTool(t, f, "email_send", ChatCatalogResources{Backend: backend, Pool: productionCapabilityPool{backend: backend, client: client}})
				args := emailSendArguments()
				if mode != "unapproved" {
					ctx = approveSharedEmailAction(t, f, target, ctx, "email_send", args)
				}
				switch mode {
				case "recipient-changed":
					args["to"] = []string{"unapproved@example.com"}
				case "body-changed":
					args["body"] = "changed body"
				case "account-changed":
					settings.Address = "unapproved@example.com"
				case "revoked":
					f.req.Bot.Status = store.BotPaused
				}
				out, err := target.Run(ctx, args)
				if mode != "approved-once" {
					if err == nil || client.calls != 0 || len(backend.vault) != 0 {
						t.Fatalf("denied send had effects: %v %v calls=%d", out, err, client.calls)
					}
					return
				}
				if err != nil || out["success"] != true || client.calls != 1 || backend.owner != f.req.Run.SessionID.String() {
					t.Fatalf("approved scoped send: %v %v calls=%d", out, err, client.calls)
				}
				select {
				case msg := <-messages:
					if !strings.Contains(msg, "Reviewed body") || !strings.Contains(msg, "Reviewed subject") {
						t.Fatalf("wrong SMTP bytes: %s", msg)
					}
				case <-time.After(time.Second):
					t.Fatal("SMTP message missing")
				}
				if _, err := target.Run(ctx, args); err == nil || client.calls != 1 {
					t.Fatalf("approval replay: %v calls=%d", err, client.calls)
				}
			})
		}
	}
}
