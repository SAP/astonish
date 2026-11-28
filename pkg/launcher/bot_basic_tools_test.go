package launcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"iter"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

type basicToolContext struct {
	botFactoryHandoffContext
	state session.State
}

func (c basicToolContext) AppName() string        { return "astonish" }
func (c basicToolContext) State() session.State   { return c.state }
func (c basicToolContext) FunctionCallID() string { return "controlled-call" }
func (c basicToolContext) AgentName() string      { return "bot" }
func (c basicToolContext) UserID() string {
	p, _ := execution.PrincipalFromContext(c)
	return p.Subject
}

func basicFactory(t *testing.T, f *botFactoryFixture, names ...string) (*api.BotChatResources, basicToolContext) {
	t.Helper()
	var err error
	f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: f.req.Version.Autonomy.Level, AllowedTools: names})
	if err != nil {
		t.Fatal(err)
	}
	resources, err := f.constructWorker(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resources.Cleanup)
	workerID := uuid.NewSHA1(f.req.Run.ID, []byte("private-fixture-worker")).String()
	s, err := f.sessions.Get(f.ctx, &session.GetRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: workerID})
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.WithBotPrivateWorkerExecution(execution.WithBotRunIdentity(f.ctx, f.req.Policy.Identity()))
	ctx = execution.WithRunPolicy(ctx, f.req.Policy)
	if resources.PolicyRuntime != nil {
		ctx = agent.WithBotPolicyRuntime(ctx, *resources.PolicyRuntime)
	}
	return resources, basicToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: ctx, session: workerID}, state: s.Session.State()}
}

// constructWorker exercises the production private execution role rather than
// giving the public Employee execution tools. Its conversation is isolated from
// the durable public run session, and is stable across fixture reconstruction.
func (f *botFactoryFixture) constructWorker(t *testing.T) (*api.BotChatResources, error) {
	t.Helper()
	employee, err := f.construct(t)
	if err != nil {
		return nil, err
	}
	id := uuid.NewSHA1(f.req.Run.ID, []byte("private-fixture-worker")).String()
	if _, err := f.sessions.Get(f.ctx, &session.GetRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: id}); err != nil {
		if _, err := f.sessions.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: id}); err != nil {
			employee.Cleanup()
			return nil, err
		}
	}
	worker, err := employee.WorkerFactory(f.ctx, api.BotEmployeeWorkerRequest{Request: f.req, ConversationID: id, Sessions: f.sessions})
	if err != nil {
		employee.Cleanup()
		return nil, err
	}
	cleanup := worker.Cleanup
	worker.Cleanup = func() { cleanup(); employee.Cleanup() }
	return worker, nil
}

func basicCall(t *testing.T, r *api.BotChatResources, c tool.Context, name string, args any) map[string]any {
	t.Helper()
	for _, entry := range r.Agent.Tools {
		if entry.Name() == "execute_tool" {
			out, err := entry.(botRunnableTool).Run(c, map[string]any{"name": name, "arguments": args})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return out
		}
	}
	t.Fatalf("missing tool %s", name)
	return nil
}

func TestBotBasicPlanLifecycle(t *testing.T) {
	f := newBotFactoryFixture(t)
	verifies, writes := 0, 0
	exit := 1
	var markdown string
	guard := &planExactGrantGuard{grants: map[string]execution.ScopedGrant{}, calls: map[string]int{}}
	write := basicPlanWrite(t)
	node := &basicPlanNode{call: func(name string, a map[string]any) (json.RawMessage, error) {
		switch name {
		case "shell_command":
			verifies++
			if a["command"] != "printf verified" || a["working_dir"] != "/isolated" {
				t.Errorf("wrong verify args: %v", a)
			}
			return json.Marshal(map[string]any{"exit_code": exit, "stdout": "verified"})
		case "write_file":
			writes++
			markdown, _ = a["content"].(string)
			if a["path"] != "/isolated/PLAN.md" {
				t.Errorf("host path: %v", a)
			}
			return write(name, a)
		default:
			t.Fatalf("unexpected node dispatch: %s", name)
			return nil, nil
		}
	}}
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(c context.Context, q bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
		r, e := prepare(c, q, s)
		if e != nil {
			return nil, e
		}
		r.Catalog.Pool = &basicPlanPool{client: node}
		r.PolicyRuntime.Guard = basicPlanGuard{exact: guard}
		return r, e
	}
	allowed := []string{"announce_plan", "update_plan", "announce_completion", "shell_command", "write_file"}
	r, c := basicFactory(t, f, allowed...)
	plan := r.Agent.ToolIndex.GetToolEntry("write_file").Tool.(*chatRuntimeTool).binding.plan
	basicApprovePlanMirrors(t, plan, guard, nil, allowed)
	// Explicit fixture owner consent also approves the plan lifecycle; this does
	// not authorize its independently guarded file writes.
	r.Agent.AutoApprove = true
	args := map[string]any{"goal": "Controlled plan", "context": "Controlled context", "what_not_to_do": "No host calls", "verification": "Observe controlled verifier", "steps": []any{map[string]any{"name": "verify", "description": "Verify scope", "details": "Run controlled sandbox verifier", "summary": "Prove scoped verification", "outcome": "Verified", "files": []any{map[string]any{"path": "evidence.txt", "kind": "new"}}, "verify": "printf verified", "verify_kind": "behavior"}}}
	out := basicCall(t, r, c, "announce_plan", args)
	if out["status"] != "ok" {
		t.Fatalf("announce: %v", out)
	}
	out = basicCall(t, r, c, "announce_plan", args)
	if out["status"] != "blocked_active_approved_plan" {
		t.Fatalf("replacement: %v", out)
	}
	out = basicCall(t, r, c, "announce_completion", map[string]any{"outcome_observed": "premature", "unverified": ""})
	if out["status"] != agent.PlanCompletionIncomplete {
		t.Fatalf("premature: %v", out)
	}
	basicCall(t, r, c, "update_plan", map[string]any{"step": "verify", "status": "running"})
	out = basicCall(t, r, c, "update_plan", map[string]any{"step": "verify", "status": "complete"})
	if out["status"] != agent.PlanStepVerifyFailed {
		t.Fatalf("failed verify: %v", out)
	}
	exit = 0
	basicCall(t, r, c, "update_plan", map[string]any{"step": "verify", "status": "running"})
	out = basicCall(t, r, c, "update_plan", map[string]any{"step": "verify", "status": "complete"})
	if out["status"] != "ok" {
		t.Fatalf("verify: %v", out)
	}
	out = basicCall(t, r, c, "announce_completion", map[string]any{"outcome_observed": "Scoped plan verified", "unverified": "Live daemon"})
	if out["status"] != "ok" {
		t.Fatalf("complete: %v", out)
	}
	saved, err := sForBasicRestore(t, f).State().Get(agent.PlanSnapshotStateKey)
	raw, _ := json.Marshal(saved)
	var snapshot agent.PlanSnapshot
	if err != nil || json.Unmarshal(raw, &snapshot) != nil || agent.NewPlanState(snapshot.Goal, snapshot.Document, snapshot.Steps).RenderMarkdown() != markdown || !strings.Contains(markdown, "## Results") || verifies != 2 || writes == 0 || guard.calls["write_file"] != writes {
		t.Fatalf("persistence: %v %v verifies=%d writes=%d grants=%v", saved, err, verifies, writes, guard.calls)
	}
	restored, restoredContext := basicFactory(t, f, allowed...)
	out = basicCall(t, restored, restoredContext, "update_plan", map[string]any{"step": "verify", "status": "running"})
	if out["status"] != agent.PlanRuntimeCompleted {
		t.Fatalf("completed plan not restored: %v", out)
	}
	restoredPlan := restored.Agent.ToolIndex.GetToolEntry("write_file").Tool.(*chatRuntimeTool).binding.plan
	if restoredPlan.runtime == nil || !strings.Contains(restoredPlan.runtime.Snapshot().Document.Results, "Scoped plan verified") {
		t.Fatal("plan results not restored")
	}
}

func TestBotBasicMemoryFactorySQLite(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newBotFactoryFixture(t)
			dir := t.TempDir()
			_, db, err := entstore.NewPlatformServices(f.ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			p := f.req.Principal
			org := &store.Organization{ID: uuid.NewString(), Slug: p.OrgSlug, Name: "Basic tools", Status: "active"}
			if err := db.Organizations().Create(f.ctx, org); err != nil {
				t.Fatal(err)
			}
			if err := db.ProvisionOrg(f.ctx, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			ods, err := db.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: p.TeamSlug, Name: "Tools", SchemaName: "team_tools"}
			if err := ods.Teams().CreateTeam(f.ctx, team); err != nil {
				t.Fatal(err)
			}
			if err := db.Users().Create(f.ctx, &store.User{ID: p.Subject, Email: "tools@example.test", DisplayName: "Tools", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := db.Organizations().AddMember(f.ctx, p.Subject, org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err := ods.Teams().AddMember(f.ctx, &store.TeamMembership{UserID: p.Subject, TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			fileFactory := f.services.BotFilesFactory
			f.services, err = BotChatServicesForOwner(ods, p, scope)
			if err != nil {
				t.Fatal(err)
			}
			// Retain live authority dependencies for the real SQLite memberships.
			f.services.Platform, f.services.TenantRouter = db, db
			// Owner-scoped stores do not provision a filesystem. Keep the explicit
			// confined file fixture required by the production factory.
			f.services.BotFilesFactory = fileFactory
			repo := f.services.Bots
			dbPath := filepath.Join(dir, "orgs", p.OrgSlug, "teams", p.TeamSlug+".db")
			if scope == store.BotScopePersonal {
				repo = f.services.PersonalBots
				dbPath = filepath.Join(dir, "orgs", p.OrgSlug, "personal", p.Subject+".db")
			}
			b, err := repo.Create(f.ctx, store.BotCreateRequest{Name: "Memory", OwnerID: f.req.Bot.OwnerID, Version: f.req.Version, IdempotencyKey: "memory"})
			if err != nil {
				t.Fatal(err)
			}
			// Seed reviewed active status only; activation infrastructure is tested separately.
			raw, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = raw.ExecContext(f.ctx, "UPDATE bots SET status='active' WHERE id=?", b.ID.String())
			raw.Close()
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(f.ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			run, err := repo.RequestRun(f.ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, IdempotencyKey: "memory", Instructions: "Remember"})
			if err != nil {
				t.Fatal(err)
			}
			identity := execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
			worker := execution.WithBotRunIdentity(f.ctx, identity)
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "basic", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			run, err = repo.GetRun(worker, b.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			run.Lease = lease
			f.req.Bot, f.req.Run = *b, *run
			f.req.Version.BotID = b.ID
			f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: identity, Level: execution.AutonomyReversible})
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.sessions.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: p.Subject, SessionID: run.SessionID.String()})
			if err != nil {
				t.Fatal(err)
			}
			prepare := f.cfg.Prepare
			f.cfg.Prepare = func(c context.Context, q bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
				r, e := prepare(c, q, s)
				if e != nil {
					return r, e
				}
				memory, e := BotMemoryTools(q, s)
				if e != nil {
					return r, e
				}
				r.PolicyRuntime.Classifier = BotCatalogueClassifier{}
				r.Tools, e = BotCatalogueTools(&catalogueClient{}, q.Run.SessionID.String(), memory...)
				return r, e
			}
			f.req.Version.Autonomy.Level = execution.AutonomyReversible
			r, c := basicFactory(t, f, "memory_save", "memory_search", "memory_get")
			saved := basicCall(t, r, c, "memory_save", map[string]any{"category": "facts", "content": "quasar source evidence"})
			if saved["saved"] != true {
				t.Fatalf("save: %v", saved)
			}
			found := basicCall(t, r, c, "memory_search", map[string]any{"query": "quasar", "max_results": 3})
			data, _ := json.Marshal(found["result"])
			var results tools.PlatformMemorySearchResult
			if err := json.Unmarshal(data, &results); err != nil {
				t.Fatal(err)
			}
			if results.Count != 1 || results.Results[0].SessionID != run.SessionID.String() {
				t.Fatalf("search: %s", data)
			}
			got := basicCall(t, r, c, "memory_get", map[string]any{"path": results.Results[0].Path})
			if got["content"] != "quasar source evidence" {
				t.Fatalf("get: %v", got)
			}
			attribution := results.Results[0].Attribution
			if attribution == nil || attribution.BotID != b.ID || attribution.RunID != run.ID || attribution.Status != store.MemoryKnowledgeInferred || attribution.Evidence != nil || attribution.Source != "agent-tool:controlled-call" || attribution.ObservedAt.IsZero() {
				t.Fatalf("agent save lost server-bound inferred attribution: %+v", attribution)
			}
			// Attributed text cannot inherit the old claim's provenance through overwrite.
			entry := r.Agent.ToolIndex.GetToolEntry("memory_save")
			if entry == nil {
				t.Fatal("memory_save missing from shared catalog")
			}
			out, e := entry.Tool.(botRunnableTool).Run(c, map[string]any{"category": "facts", "content": "quasar replacement", "overwrite": true})
			if !errors.Is(e, store.ErrBotConflict) || !strings.Contains(e.Error(), "owner correction") || out["saved"] == true {
				t.Fatalf("attributed overwrite needs actionable correction, not false success: %v %v", out, e)
			}
			memory, err := repo.(store.BotMemoryStoreProvider).BotMemoryStore(b.ID)
			if err != nil {
				t.Fatal(err)
			}
			unchanged, err := memory.Get(f.ctx, results.Results[0].ID)
			if err != nil || unchanged == nil || unchanged.Snippet != "quasar source evidence" || !reflect.DeepEqual(unchanged.Attribution, attribution) {
				t.Fatalf("rejected overwrite altered historical provenance: %+v %v", unchanged, err)
			}
			// Generic memory is not a Bot content store after cutover.
			generic, err := f.services.Memory.ListBySession(f.ctx, run.SessionID.String())
			if err != nil || len(generic) != 0 {
				t.Fatalf("Bot facts leaked into generic SQL memory: %+v %v", generic, err)
			}
			testBotForgottenMemoryProvider(t, f, r, c, run.SessionID.String())
		})
	}
}

func TestBotBasicPlanVerifierCannotBypassPolicy(t *testing.T) {
	f := newBotFactoryFixture(t)
	writes := 0
	write := basicPlanWrite(t)
	client := &basicPlanNode{call: func(name string, args map[string]any) (json.RawMessage, error) {
		writes++
		return write(name, args)
	}}
	guard := &planExactGrantGuard{grants: map[string]execution.ScopedGrant{}, calls: map[string]int{}}
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(c context.Context, q bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
		r, e := prepare(c, q, s)
		r.Catalog.Pool = &basicPlanPool{client: client}
		r.PolicyRuntime.Guard = basicPlanGuard{exact: guard}
		return r, e
	}
	allowed := []string{"update_plan", "write_file"}
	r, c := basicFactory(t, f, allowed...)
	// Shared Chat restores the authoritative snapshot, not the old mutable
	// Agent.ActivePlan pointer. Seed an approved running phase to exercise its verifier.
	snapshot := agent.PlanSnapshot{PlanID: uuid.NewString(), Revision: 1, Sequence: 1, Goal: "Guarded", Document: agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleApproved}, Steps: []agent.PlanStepInfo{{Name: "verify", Status: "running", Verify: "printf prohibited", VerifyKind: "behavior"}}}
	if err := f.sessions.AppendEvent(c, sForBasicRestore(t, f), &session.Event{Actions: session.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: snapshot}}}); err != nil {
		t.Fatal(err)
	}
	plan := r.Agent.ToolIndex.GetToolEntry("write_file").Tool.(*chatRuntimeTool).binding.plan
	basicApprovePlanMirrors(t, plan, guard, &snapshot, allowed)
	out := basicCall(t, r, c, "update_plan", map[string]any{"step": "verify", "status": "complete"})
	if out["status"] != agent.PlanStepVerifyFailed || writes != guard.calls["write_file"] || writes == 0 {
		t.Fatalf("verify bypass: %v calls=%d", out, writes)
	}
}

func TestBotBasicMemoryClassification(t *testing.T) {
	for name, effect := range map[string]execution.ActionEffect{"memory_save": execution.EffectReversibleWrite, "memory_delete": execution.EffectDelete, "memory_search": execution.EffectRead, "memory_get": execution.EffectRead} {
		a, err := (BotCatalogueClassifier{}).DescribeAction(context.Background(), name, json.RawMessage(`{}`))
		if err != nil || a.Effect != effect || !a.ConstraintsVerified {
			t.Fatalf("%s: %+v %v", name, a, err)
		}
	}
}

func sForBasicRestore(t *testing.T, f *botFactoryFixture) session.Session {
	t.Helper()
	// Plan snapshots belong to the physical run, not the private conversation.
	s, err := f.sessions.Get(f.ctx, &session.GetRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: f.req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	return s.Session
}

type basicChildModel struct{ text string }

func (b basicChildModel) Name() string { return "controlled-child" }
func (b basicChildModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText(b.text, "model"), TurnComplete: true}, nil)
	}
}
func TestBotBasicDelegateResultIsolation(t *testing.T) {
	f := newBotFactoryFixture(t)
	full := strings.Repeat("controlled child evidence\n", 1000)
	f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) {
		return basicChildModel{text: full}, nil
	}
	r, c := basicFactory(t, f, "delegate_tasks", "read_task_result")
	out := basicCall(t, r, c, "delegate_tasks", map[string]any{"tasks": []any{map[string]any{"name": "research", "task": "Return controlled evidence", "tools": []any{}}}})
	data, _ := json.Marshal(out)
	var result tools.DelegateTasksResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || len(result.Results) != 1 || result.Results[0].FullResultID == "" {
		t.Fatalf("delegate: %s", data)
	}
	id := result.Results[0].FullResultID
	got := basicCall(t, r, c, "read_task_result", map[string]any{"result_id": id})
	if strings.TrimSpace(got["content"].(string)) != strings.TrimSpace(full) {
		t.Fatal("full child result lost")
	}
	if _, ok := tools.GetTaskResultStore().Get(id); ok {
		t.Fatal("Bot output leaked to global store")
	}
	other, oc := basicFactory(t, newBotFactoryFixture(t), "read_task_result")
	denied := basicCall(t, other, oc, "read_task_result", map[string]any{"result_id": id})
	if denied["error"] == nil {
		t.Fatalf("cross-run retrieval: %v", denied)
	}
}
