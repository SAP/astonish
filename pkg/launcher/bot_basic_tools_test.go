package launcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"iter"
	"path/filepath"
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
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type basicToolContext struct {
	botFactoryHandoffContext
	state session.State
}

func (c basicToolContext) State() session.State   { return c.state }
func (c basicToolContext) FunctionCallID() string { return "controlled-call" }
func (c basicToolContext) AgentName() string      { return "bot" }
func (c basicToolContext) UserID() string         { return "owner" }

func basicFactory(t *testing.T, f *botFactoryFixture, names ...string) (*api.BotChatResources, basicToolContext) {
	t.Helper()
	var err error
	f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: f.req.Version.Autonomy.Level, AllowedTools: names})
	if err != nil {
		t.Fatal(err)
	}
	resources, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resources.Cleanup)
	s, err := f.sessions.Get(f.ctx, &session.GetRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: f.req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := execution.WithBotRunIdentity(f.ctx, f.req.Policy.Identity())
	ctx = execution.WithRunPolicy(ctx, f.req.Policy)
	return resources, basicToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: ctx, session: f.req.Run.SessionID.String()}, state: s.Session.State()}
}
func basicCall(t *testing.T, r *api.BotChatResources, c tool.Context, name string, args any) map[string]any {
	t.Helper()
	for _, entry := range r.Agent.Tools {
		if entry.Name() == name {
			out, err := entry.(botRunnableTool).Run(c, args)
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
	shell, _ := functiontool.New(functiontool.Config{Name: "shell_command", Description: "Controlled sandbox verifier"}, func(_ tool.Context, a map[string]any) (map[string]any, error) {
		verifies++
		if a["command"] != "printf verified" || a["working_dir"] != "/isolated" {
			t.Errorf("wrong verify args: %v", a)
		}
		return map[string]any{"exit_code": exit, "stdout": "verified"}, nil
	})
	write, _ := functiontool.New(functiontool.Config{Name: "write_file", Description: "Controlled sandbox file writer"}, func(_ tool.Context, a map[string]any) (map[string]any, error) {
		writes++
		markdown, _ = a["content"].(string)
		if a["path"] != "/isolated/PLAN.md" {
			t.Errorf("host path: %v", a)
		}
		return map[string]any{"ok": true}, nil
	})
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(c context.Context, q bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
		r, e := prepare(c, q, s)
		r.Tools = []tool.Tool{shell, write}
		return r, e
	}
	r, c := basicFactory(t, f, "announce_plan", "update_plan", "announce_completion", "shell_command", "write_file")
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
	saved, err := c.State().Get(botPlanStateKey)
	if err != nil || saved != markdown || !strings.Contains(markdown, "## Results") || verifies != 2 || writes == 0 {
		t.Fatalf("persistence: %v %v verifies=%d writes=%d", saved, err, verifies, writes)
	}
	if err := f.sessions.AppendEvent(c, sForBasicRestore(t, f), &session.Event{LLMResponse: model.LLMResponse{}, Actions: session.EventActions{StateDelta: map[string]any{botPlanStateKey: markdown}}}); err != nil {
		t.Fatal(err)
	}
	restored := agent.NewChatAgent(r.Agent.LLM, nil, nil, f.sessions, nil, false, false)
	if err := restoreBotPlan(c, restored, &BotChatScope{Sessions: f.sessions}, f.req.Run.SessionID.String(), f.req.Bot.OwnerID.String()); err != nil {
		t.Fatal(err)
	}
	if restored.GetActivePlan() == nil || !strings.Contains(restored.GetActivePlan().RenderMarkdown(), "Scoped plan verified") {
		t.Fatal("plan not restored")
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
			f.services, err = BotChatServicesForOwner(ods, p, scope)
			if err != nil {
				t.Fatal(err)
			}
			repo := f.services.Bots
			dbPath := filepath.Join(dir, "orgs", p.OrgSlug, "teams", p.TeamSlug+".db")
			if scope == store.BotScopePersonal {
				repo = f.services.PersonalBots
				dbPath = filepath.Join(dir, "orgs", p.OrgSlug, "personal", p.Subject+".db")
			}
			b, err := repo.CreateDraft(f.ctx, store.BotCreateRequest{Name: "Memory", OwnerID: f.req.Bot.OwnerID, Version: f.req.Version, IdempotencyKey: "memory"})
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
			data, _ := json.Marshal(found)
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
			basicCall(t, r, c, "memory_save", map[string]any{"category": "facts", "content": "quasar replacement", "overwrite": true})
			got = basicCall(t, r, c, "memory_get", map[string]any{"path": results.Results[0].Path})
			if got["content"] != "quasar replacement" {
				t.Fatalf("overwrite: %v", got)
			}
			testBotForgottenMemoryProvider(t, f, r, c, run.SessionID.String())
		})
	}
}

func TestBotBasicPlanVerifierCannotBypassPolicy(t *testing.T) {
	f := newBotFactoryFixture(t)
	calls := 0
	shell, _ := functiontool.New(functiontool.Config{Name: "shell_command", Description: "Must not execute"}, func(tool.Context, map[string]any) (map[string]any, error) {
		calls++
		return map[string]any{"exit_code": 0}, nil
	})
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(c context.Context, q bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
		r, e := prepare(c, q, s)
		r.Tools = []tool.Tool{shell}
		return r, e
	}
	r, c := basicFactory(t, f, "update_plan")
	r.Agent.SetActivePlan(agent.NewPlanState("Guarded", agent.PlanDocumentInfo{}, []agent.PlanStepInfo{{Name: "verify", Verify: "printf prohibited", VerifyKind: "behavior"}}))
	r.Agent.MarkActivePlanApproved()
	out := basicCall(t, r, c, "update_plan", map[string]any{"step": "verify", "status": "complete"})
	if out["status"] != agent.PlanStepVerifyFailed || calls != 0 {
		t.Fatalf("verify bypass: %v calls=%d", out, calls)
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
