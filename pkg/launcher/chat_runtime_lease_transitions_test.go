package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
)

// This suite uses persisted SQLite admission/claim/renewal and real shared Chat
// dispatch. synctest advances the clock seen by BOTH the repository and runtime;
// no expired request snapshot is fabricated and no wall-clock TTL is slept.
// The node transport is controlled, but executes the production shell dispatcher
// in a test-owned directory. It is not physical Docker/Kubernetes evidence.
func TestChatRuntimePersistedLeaseTransitions(t *testing.T) {
	// The shared process manager is process-lifetime infrastructure, not owned
	// by an individual virtual-time bubble.
	_ = tools.GetProcessManager()
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"interactive-bot", "autonomous-bot"} {
			for _, transition := range []string{"renewed", "expired", "replaced"} {
				t.Run(string(scope)+"/"+mode+"/"+transition, func(t *testing.T) {
					// PTY completion channels live outside the virtual clock bubble.
					// Outer cleanup closes helper input and joins the owned process.
					before := len(tools.GetProcessManager().List())
					root := t.TempDir()
					node := newProcessParityNode(t, "lease-probe", root)
					synctest.Test(t, func(t *testing.T) {
						leaseTransitionCase(t, scope, mode, transition, root, node)
					})
					if len(node.calls) != 1 || node.calls[0] != "shell_command" {
						t.Fatalf("owned dispatcher calls=%v, want one shell probe", node.calls)
					}
					if len(tools.GetProcessManager().List()) != before {
						t.Fatal("lease clock bubble leaked a process into the parent manager")
					}
				})
			}
		}
	}
}

func leaseTransitionCase(t *testing.T, scope chatruntime.ResourceScope, mode, transition, root string, node *processParityNode) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	f := newBotFactoryFixture(t)
	f.req.Bot.Scope = store.BotScope(scope)
	r := &servicesParityRuntime{f: f, guard: &drillGuard{consumed: map[uuid.UUID]bool{}}}
	m := newLiveMembershipFixture(t, r, scope)
	// Discard the membership fixture's snapshot adapter: all runtime Bot/run reads
	// below go directly to its actual scoped SQLite repository.
	repo := m.repo.BotStore
	f.services.Bots, f.services.PersonalBots = repo, repo
	db := f.services.Platform.(*entstore.Store)
	ods, err := db.ForOrg(f.req.Principal.OrgSlug)
	must(err)
	t.Cleanup(func() { _ = ods.Close() })
	// Physical readiness is explicitly fixture-owned, not a claim that activation
	// provisioned a container. Admission, claim and all lease CAS remain real.
	db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
	b, err := repo.Get(f.ctx, m.repo.probe)
	must(err)
	b, err = repo.Get(f.ctx, b.ID)
	must(err)
	f.req.Bot = *b
	v, err := repo.GetVersion(f.ctx, b.ID, b.Version)
	must(err)
	f.req.Version = *v
	r.node = &servicesParityNode{backend: &productionCapabilityBackend{}, root: root,
		shellDispatch: func(name string, args map[string]any) (json.RawMessage, error) {
			return node.Call(node.session, name, args)
		},
	}
	pool := productionCapabilityPool{backend: r.node.backend, client: r.node}
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, scope)
		if err == nil {
			safe.ChatConfig = &config.AppConfig{}
			safe.Catalog = &ChatCatalogResources{Pool: pool, Backend: r.node.backend}
			safe.WorkspaceDir = root
			safe.PolicyRuntime = agent.BotPolicyRuntime{Classifier: BotCatalogueClassifier{}, Guard: r.guard}
		}
		return safe, err
	}
	registry := execution.NewBotTurnAuthorizationRegistry(4)
	defer registry.Close()
	f.req.Principal.Scopes = []string{"chat", "tool:execute"}
	f.ctx, err = execution.WithPrincipal(context.Background(), f.req.Principal)
	must(err)
	f.ctx, err = execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: f.req.Principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	must(err)
	args := map[string]any{"command": "printf services-parity-ok", "working_dir": root}
	admit := func(key, worker string) uuid.UUID {
		t.Helper()
		var run *store.BotRun
		if mode == "interactive-bot" {
			receipt, err := repo.(store.BotMessageStore).AdmitMessage(store.WithBotMessageAdmission(f.ctx, registry), store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: key, Instructions: "Execute the controlled shell probe"})
			must(err)
			run = &receipt.Run
		} else {
			var err error
			run, err = repo.RequestRun(f.ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, IdempotencyKey: key, Instructions: "Execute the controlled shell probe"})
			must(err)
		}
		id := execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: f.req.Principal.Subject, OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}
		workerCtx := execution.WithBotRunIdentity(f.ctx, id)
		lease, err := repo.ClaimRun(workerCtx, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: worker, ExpiresAt: time.Now().Add(time.Minute)})
		must(err)
		run, err = repo.GetRun(workerCtx, b.ID, run.ID)
		must(err)
		if run.Lease != lease {
			t.Fatal("claim did not persist exact lease")
		}
		f.req.Run = *run
		f.req.Bot = *b
		r.node.session = run.SessionID.String()
		_, err = f.sessions.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: f.req.Principal.Subject, SessionID: run.SessionID.String(), State: map[string]any{"execution_owner": "bot"}})
		must(err)
		grantID := uuid.Nil
		if mode == "interactive-bot" {
			f.req.Policy = nil
			f.req.Authorization, err = registry.Authorizer(id, *run.CallerCeiling, m.current)
			must(err)
		} else {
			raw, err := json.Marshal(args)
			must(err)
			action, err := (BotCatalogueClassifier{}).DescribeAction(f.ctx, "shell_command", raw)
			must(err)
			digest, err := action.Digest()
			must(err)
			grantID = uuid.New()
			f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"shell_command"}, Grants: []execution.ScopedGrant{{ID: grantID, Identity: id, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)}}})
			must(err)
		}
		return grantID
	}
	construct := func() {
		t.Helper()
		runtime, err := f.construct(t)
		if err != nil {
			t.Fatalf("construct current lease: %v", err)
		}
		r.chat = runtime.Agent
		t.Cleanup(runtime.Cleanup)
		entry := r.chat.ToolIndex.GetToolEntry("shell_command")
		if entry == nil {
			t.Fatal("missing shared shell tool")
		}
		shared, ok := entry.Tool.(*chatRuntimeTool)
		if !ok {
			t.Fatalf("not shared Chat binding: %T", entry.Tool)
		}
		if _, ok := shared.chatRuntimeRunnable.(*sandbox.NodeTool); !ok {
			t.Fatalf("not actual node tool: %T", shared.chatRuntimeRunnable)
		}
	}
	grant := admit("original", "original-worker")
	original := f.req.Run.Lease
	identity := execution.BotRunIdentity{BotID: b.ID, RunID: f.req.Run.ID, Version: b.Version, ActorID: f.req.Principal.Subject, OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}
	workerCtx := execution.WithBotRunIdentity(f.ctx, identity)
	construct()
	oldTool := r.chat.ToolIndex.GetToolEntry("shell_command").Tool.(chatRuntimeRunnable)
	oldBase := runtimeCapabilityToolContext{sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: f.ctx}, sid: r.node.session}}}
	oldContext := servicesParityContext{Context: oldBase, owner: f.req.Principal.Subject, sid: r.node.session}
	if len(r.node.calls) != 0 || len(r.guard.consumed) != 0 {
		t.Fatal("construction executed action")
	}
	if transition == "renewed" {
		renewed, err := repo.RenewLease(workerCtx, original, time.Now().Add(3*time.Minute))
		must(err)
		if renewed.WorkerID != original.WorkerID || renewed.Fence != original.Fence {
			t.Fatal("renewal replaced authority")
		}
	}
	{
		// synctest's virtual clock, shared by store and runtime; not wall sleep.
		time.Sleep(time.Minute + time.Second)
		if time.Now().Before(original.ExpiresAt) || f.req.Run.Lease != original {
			t.Fatal("initial lease snapshot was not retained beyond TTL")
		}
	}
	positive := func(wantGrant uuid.UUID) {
		t.Helper()
		before := len(r.node.calls)
		out, err := r.invoke(t, f.ctx, "shell_command", args)
		must(err)
		if out["stdout"] != "services-parity-ok" || len(r.node.calls) != before+1 {
			t.Fatalf("shared shell did not execute: %v calls=%v", out, r.node.calls)
		}
		if mode == "autonomous-bot" && !r.guard.consumed[wantGrant] {
			t.Fatal("exact fresh grant not consumed")
		}
	}
	if transition == "renewed" {
		positive(grant)
		return
	}
	denied := func() {
		t.Helper()
		before := len(r.node.calls)
		if _, err := oldTool.Run(oldContext, args); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("old lease denial=%v", err)
		}
		if len(r.node.calls) != before || len(r.guard.consumed) != 0 || len(r.node.backend.vault) != 0 || f.resolver.model.calls != 0 {
			t.Fatal("stale authority executed or consumed grant")
		}
	}
	if transition == "expired" {
		if _, err := repo.RenewLease(workerCtx, original, time.Now().Add(time.Minute)); !errors.Is(err, store.ErrBotExpired) && !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("store did not observe expiry: %v", err)
		}
		denied()
	}
	// Recovery fences the expired attempt without replaying it. Admit and claim
	// NEW owner work with a new worker and monotonically increasing fence.
	err = repo.(interface {
		RecoverRun(context.Context, store.BotLease) error
	}).RecoverRun(f.ctx, original)
	if err != nil {
		t.Fatalf("recover expired attempt: %v", err)
	}
	b, err = repo.Get(f.ctx, b.ID)
	must(err)
	fresh := admit("replacement", "replacement-worker")
	if f.req.Run.Lease.Fence <= original.Fence || f.req.Run.Lease.WorkerID == original.WorkerID {
		t.Fatal("replacement did not change lease owner/fence")
	}
	denied() // retained old executable, while replacement authority is current
	if fresh == grant && mode == "autonomous-bot" {
		t.Fatal("replacement reused old grant")
	}
	construct()
	positive(fresh)
	if mode == "autonomous-bot" && (r.guard.consumed[grant] || len(r.guard.consumed) != 1) {
		t.Fatal("denied original grant was consumed")
	}
}
