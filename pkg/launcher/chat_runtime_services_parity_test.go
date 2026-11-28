package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/email"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
)

// This is local service/dispatcher evidence, not physical sandbox evidence. Only
// transport and repositories are fixtures: each factory builds its real catalog,
// drill engine, plan runtime and email implementation. Node calls execute the
// production dispatcher, bounded to a temporary directory and a local SMTP peer.
type servicesParityNode struct {
	sandbox.ToolNodeClient
	backend       *productionCapabilityBackend
	root, session string
	calls         []string
	outputs       []map[string]any
	// Lease clock bubbles dispatch shell probes in a test-owned node process.
	shellDispatch func(string, map[string]any) (json.RawMessage, error)
}

func (*servicesParityNode) BindSession(string)       {}
func (*servicesParityNode) EnsureReady(string) error { return nil }
func (n *servicesParityNode) Call(id, name string, args map[string]any) (json.RawMessage, error) {
	if id != n.session {
		return nil, fmt.Errorf("foreign node session %s", id)
	}
	switch name {
	case "write_file":
		if args["path"] != filepath.Join(n.root, "PLAN.md") {
			return nil, fmt.Errorf("unexpected write %v", args["path"])
		}
	case "shell_command":
		if args["command"] != "printf services-parity-ok" || args["working_dir"] != n.root {
			return nil, fmt.Errorf("unexpected command %v", args)
		}
	case email.NodeOperation:
		if n.backend.owner != id {
			return nil, fmt.Errorf("unbound vault")
		}
	default:
		return nil, fmt.Errorf("unexpected node operation %s", name)
	}
	n.calls = append(n.calls, name)
	cs := sessioncreds.NewStore(nil)
	if len(n.backend.vault) > 0 {
		var err error
		cs, err = sessioncreds.Parse(n.backend.vault)
		if err != nil {
			return nil, err
		}
	}
	var raw json.RawMessage
	var err error
	if name == "shell_command" && n.shellDispatch != nil {
		raw, err = n.shellDispatch(name, args)
	} else {
		var result any
		result, err = tools.ExecuteTool(store.WithCredentialStore(context.Background(), cs), name, args, id)
		if err == nil {
			raw, err = json.Marshal(result)
		}
	}
	if err != nil {
		return nil, err
	}
	var output map[string]any
	if err := json.Unmarshal(raw, &output); err != nil {
		return nil, err
	}
	n.outputs = append(n.outputs, output)
	return raw, nil
}

type servicesParityContext struct {
	tool.Context
	owner, sid string
}

func (c servicesParityContext) UserID() string    { return c.owner }
func (c servicesParityContext) SessionID() string { return c.sid }
func (servicesParityContext) AppName() string     { return "astonish" }
func (servicesParityContext) AgentName() string   { return "chat" }

type servicesParityFlows struct{ drillFlowStore }

func (servicesParityFlows) ListFlowsByType(context.Context, []string) []store.FlowSummary {
	return []store.FlowSummary{{Name: "first", Type: "drill", Suite: "suite"}}
}

type servicesParityReports struct {
	store.DrillReportStore
	owner   string
	reports []store.DrillReport
}

func (r *servicesParityReports) SaveReport(ctx context.Context, report *store.DrillReport) error {
	if store.UserIDFromContext(ctx) != r.owner || store.TeamSlugFromContext(ctx) != "owner-team" {
		return store.ErrBotForbidden
	}
	r.reports = append(r.reports, *report)
	return nil
}

type servicesParityRuntime struct {
	f        *botFactoryFixture
	chat     *agent.ChatAgent
	node     *servicesParityNode
	reports  *servicesParityReports
	cleanup  func()
	guard    *drillGuard
	messages <-chan string
}

// servicesParityAction reviews the same immutable suite and child descriptors as
// scheduled production dispatch, using the fixture's explicitly scoped flow store.
// The captured parent approval does not replace separate exact child approvals.
func servicesParityAction(t *testing.T, f *botFactoryFixture, name string, args map[string]any) execution.ActionDescriptor {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	owner := store.WithTeamFlowStore(store.WithTeamSlug(store.WithUserID(f.ctx, f.req.Principal.Subject), f.req.Principal.TeamSlug), f.services.Flows)
	classifier := botCapturedDrillClassifier{owner: owner, fallback: BotCatalogueClassifier{}}
	action, err := classifier.DescribeAction(f.ctx, name, raw)
	if err != nil {
		t.Fatal(err)
	}
	if name == "run_drill" {
		if action.Effect != execution.EffectUnknown || action.ConstraintsVerified || action.TargetFingerprint == "" || len(action.TargetSnapshot) == 0 {
			t.Fatalf("drill review lost exact snapshot/conservative effect: %+v", action)
		}
		if _, err := tools.DecodeCapturedDrill(action); err != nil {
			t.Fatal(err)
		}
	}
	return action
}

func newServicesParityRuntime(t *testing.T, scope chatruntime.ResourceScope, mode string) *servicesParityRuntime {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	f := newBotFactoryFixture(t)
	if scope == chatruntime.ResourceScopeTeam {
		f.req.Bot.Scope = store.BotScopeTeam
	}
	endpoint, messages := emailSMTPFixture(t, "owner", productionTestSecret)
	settings := emailTestSettings()
	settings.SMTPServer = endpoint
	own := sessioncreds.NewStore(map[string]*store.Credential{"mail": {Type: store.CredPassword, Username: "owner", Password: productionTestSecret}})
	other := sessioncreds.NewStore(map[string]*store.Credential{"mail": {Type: store.CredPassword, Username: "foreign", Password: "foreign-services-secret"}})
	f.services.PersonalSettings = &emailPersonalSettings{settings: settings}
	f.services.Settings = &emailTeamSettings{settings: settings}
	if scope == chatruntime.ResourceScopePersonal {
		f.services.PersonalCredentials, f.services.Credentials = own, other
	} else {
		f.services.PersonalCredentials, f.services.Credentials = other, own
	}
	// Both stores are explicit. This is the existing drill suite store contract,
	// not an implicit host flow directory or a replacement run_drill tool.
	data := map[string]string{
		"suite": "name: suite\ntype: drill_suite\nsuite_config: {}\n",
		"first": fmt.Sprintf("name: first\ntype: drill\nsuite: suite\nnodes:\n  - name: execute\n    type: tool\n    args: {tool: shell_command, command: 'printf services-parity-ok', working_dir: %q}\n", root),
	}
	fs := servicesParityFlows{drillFlowStore{data: data}}
	f.services.Flows, f.services.PersonalFlows = fs, fs
	reports := &servicesParityReports{owner: f.req.Principal.Subject}
	f.services.DrillReports = reports
	backend := &productionCapabilityBackend{}
	node := &servicesParityNode{backend: backend, root: root, session: f.req.Run.SessionID.String()}
	pool := productionCapabilityPool{backend: backend, client: node}
	resources := ChatCatalogResources{Pool: pool, Backend: backend}
	cfg := &config.AppConfig{}
	result := &servicesParityRuntime{f: f, node: node, reports: reports, guard: &drillGuard{consumed: map[uuid.UUID]bool{}}, messages: messages}
	if mode == "ordinary-chat" {
		inputs, err := chatruntime.Build(f.ctx, chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: scope, AppConfig: cfg, Pool: pool, FallbackPolicy: chatruntime.FallbackForbidden})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = inputs.Close() })
		var chat *agent.ChatAgent
		var cleanup func()
		if scope == chatruntime.ResourceScopePersonal {
			factory := newStudioChatRuntimeFactory(ChatFactoryConfig{AppConfig: cfg, WorkspaceDir: root}, resources, nil)
			runtime, err := factory(f.ctx, api.StudioChatRuntimeRequest{Inputs: inputs, AppConfig: cfg, LLM: provider.NewPlaceholderLLM(), SessionService: f.sessions, SessionID: node.session, UserID: f.req.Principal.Subject, AppName: "astonish"})
			if err != nil {
				t.Fatal(err)
			}
			chat, cleanup = runtime.ChatAgent, runtime.Cleanup
		} else {
			// Studio admission is personal-only. Exercise ordinary team Chat via
			// the same shared constructor with explicit team mailbox composition;
			// this does not claim a team Studio HTTP admission path exists.
			resources.EmailTools, err = ScopedChatEmailTools(ScopedChatEmailOptions{Inputs: inputs, Scope: scope, Backend: backend, Client: node, SessionID: node.session})
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := NewScopedChatRuntime(f.ctx, ChatRuntimeDependencies{Inputs: inputs, Config: &ChatFactoryConfig{AppConfig: cfg, WorkspaceDir: root}, Resources: &resources, LLM: provider.NewPlaceholderLLM(), Sessions: f.sessions, SessionID: node.session, UseScopedEngines: true}, ChatRuntimeHooks{})
			if err != nil {
				t.Fatal(err)
			}
			chat, cleanup = runtime.ChatAgent, runtime.Cleanup
		}
		result.chat, result.cleanup = chat, cleanup
	} else {
		if mode == "interactive-bot" {
			setBotSharedInteractive(f)
		} else {
			actions := map[string][]map[string]any{"run_drill": {{"suite_name": "suite"}}, "shell_command": {{"command": "printf services-parity-ok", "working_dir": root}}, "announce_plan": nil, "update_plan": nil, "announce_completion": nil, "write_file": nil}
			drillGrants(t, f, actions)
			spec := execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{"run_drill", "shell_command", "email_send", "announce_plan", "update_plan", "announce_completion", "write_file"}}
			// Re-create each grant using its real classifier; email additionally binds
			// the configured account fingerprint, not merely the native tool name.
			for name, calls := range actions {
				for _, args := range calls {
					action := servicesParityAction(t, f, name, args)
					digest, err := action.Digest()
					if err != nil {
						t.Fatal(err)
					}
					spec.Grants = append(spec.Grants, execution.ScopedGrant{ID: uuid.New(), Identity: spec.Identity, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
				}
			}
			account, err := scopedEmailAccount(f.ctx, f.services, scope == chatruntime.ResourceScopePersonal)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(emailSendArguments())
			action := execution.ActionDescriptor{Tool: "email_send", Arguments: raw, Effect: execution.EffectUnknown, TargetFingerprint: account.Fingerprint()}
			digest, err := action.Digest()
			if err != nil {
				t.Fatal(err)
			}
			spec.Grants = append(spec.Grants, execution.ScopedGrant{ID: uuid.New(), Identity: spec.Identity, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
			f.req.Policy, err = execution.NewRunPolicy(spec)
			if err != nil {
				t.Fatal(err)
			}
		}
		prepare := f.cfg.Prepare
		f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
			safe, err := prepare(ctx, req, scope)
			if err == nil {
				safe.ChatConfig, safe.Catalog, safe.WorkspaceDir = cfg, &resources, root
				safe.PolicyRuntime = agent.BotPolicyRuntime{Classifier: BotCatalogueClassifier{}, Guard: result.guard}
			}
			return safe, err
		}
		runtime, err := f.construct(t)
		if err != nil {
			t.Fatal(err)
		}
		result.chat, result.cleanup = runtime.Agent, runtime.Cleanup
	}
	t.Cleanup(result.cleanup)
	if len(node.calls) != 0 || len(backend.vault) != 0 {
		t.Fatal("constructor eagerly executed service")
	}
	return result
}

func (r *servicesParityRuntime) invoke(t *testing.T, ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	t.Helper()
	entry := r.chat.ToolIndex.GetToolEntry(name)
	if entry == nil {
		t.Fatalf("missing shared tool %s", name)
	}
	if _, ok := entry.Tool.(*chatRuntimeTool); !ok {
		t.Fatalf("%s bypassed shared wrapper: %T", name, entry.Tool)
	}
	base := runtimeCapabilityToolContext{sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: ctx}, sid: r.node.session}}}
	return entry.Tool.(chatRuntimeRunnable).Run(servicesParityContext{Context: base, owner: r.f.req.Principal.Subject, sid: r.node.session}, args)
}
