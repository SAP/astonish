package launcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
)

// Real configuration persistence with a controlled live-run reader: the factory
// still supplies the real bound tools, tenant/principal checks and policy guard.
// Tests end each simulated run before issuing a separate owner configuration
// command; no production mutation bypass is installed.
type scheduleTestRepo struct {
	store.BotStore
	store.BotTriggerStore
	f *botFactoryFixture
}

func (s scheduleTestRepo) GetRun(context.Context, store.BotID, store.RunID) (*store.BotRun, error) {
	r := s.f.req.Run
	return &r, nil
}

func scheduleFixture(t *testing.T, scope store.BotScope) (*botFactoryFixture, store.BotStore) {
	t.Helper()
	f := newBotFactoryFixture(t)
	dir := t.TempDir()
	_, db, err := entstore.NewPlatformServices(f.ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := f.req.Principal
	org := &store.Organization{ID: uuid.NewString(), Slug: p.OrgSlug, Name: "Schedules", Status: "active"}
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
	team := &store.Team{ID: uuid.NewString(), Slug: p.TeamSlug, Name: "Schedules", SchemaName: "team_schedules"}
	if err := ods.Teams().CreateTeam(f.ctx, team); err != nil {
		t.Fatal(err)
	}
	if err := db.Users().Create(f.ctx, &store.User{ID: p.Subject, Email: "schedules@example.test", DisplayName: "Owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Organizations().AddMember(f.ctx, p.Subject, org.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if err := ods.Teams().AddMember(f.ctx, &store.TeamMembership{UserID: p.Subject, TeamID: team.ID, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	f.services, err = BotChatServicesForOwner(ods, p, scope)
	if err != nil {
		t.Fatal(err)
	}
	if f.services.Scheduler == nil {
		t.Fatal("owner-scoped services omitted the team scheduler")
	}
	if scope == store.BotScopePersonal && f.services.PersonalScheduler == nil {
		t.Fatal("owner-scoped services omitted the personal scheduler")
	}
	if scope == store.BotScopeTeam && f.services.PersonalScheduler != nil {
		t.Fatal("team services exposed a personal scheduler")
	}
	repo := f.services.Bots
	dbPath := filepath.Join(dir, "orgs", p.OrgSlug, "teams", p.TeamSlug+".db")
	if scope == store.BotScopePersonal {
		repo = f.services.PersonalBots
		dbPath = filepath.Join(dir, "orgs", p.OrgSlug, "personal", p.Subject+".db")
	}
	b, err := repo.CreateDraft(f.ctx, store.BotCreateRequest{Name: "Schedules", OwnerID: f.req.Bot.OwnerID, Version: f.req.Version, IdempotencyKey: "schedule"})
	if err != nil {
		t.Fatal(err)
	}
	// Activation infrastructure is covered independently; seed active lifecycle.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.ExecContext(f.ctx, "UPDATE bots SET status='active' WHERE id=?", b.ID.String())
	_ = raw.Close()
	if err != nil {
		t.Fatal(err)
	}
	f.req.Bot.ID = b.ID
	wrapped := scheduleTestRepo{BotStore: repo, BotTriggerStore: repo.(store.BotTriggerStore), f: f}
	if scope == store.BotScopePersonal {
		f.services.PersonalBots = wrapped
	} else {
		f.services.Bots = wrapped
	}
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, q bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
		r, err := prepare(ctx, q, s)
		if err != nil {
			return r, err
		}
		r.Tools, err = BotScheduleTools(q, s)
		return r, err
	}
	return f, repo
}

func scheduleFactory(t *testing.T, f *botFactoryFixture, repo store.BotStore) (*api.BotChatResources, basicToolContext) {
	t.Helper()
	b, err := repo.Get(f.ctx, f.req.Bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	v, err := repo.GetVersion(f.ctx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	f.req.Bot, f.req.Version = *b, *v
	f.req.Run.BotID, f.req.Run.Version, f.req.Run.Lease.BotID = b.ID, v.Version, b.ID
	id := f.req.Policy.Identity()
	id.BotID, id.Version = b.ID, v.Version
	f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: v.Autonomy.Level, AllowedTools: []string{"schedule_job", "list_scheduled_jobs", "update_scheduled_job", "remove_scheduled_job"}})
	if err != nil {
		t.Fatal(err)
	}
	return basicFactory(t, f, "schedule_job", "list_scheduled_jobs", "update_scheduled_job", "remove_scheduled_job")
}

func decodeScheduleReview(t *testing.T, out map[string]any) botScheduleChangeResult {
	t.Helper()
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var result botScheduleChangeResult
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != botScheduleReviewRequired || len(result.Review.Digest) != 64 {
		t.Fatalf("not exact review: %v", out)
	}
	// Legacy request IDs identify an unsaved definition, never an existing job.
	parts := strings.Split(strings.Split(result.Review.Path, "?")[0], "/")
	result.JobID = parts[6]
	return result
}

func applyScheduleReview(ctx context.Context, repo store.BotStore, id store.BotID, r botScheduleChangeResult) (*store.Bot, error) {
	triggerID, err := uuid.Parse(r.JobID)
	if err != nil {
		return nil, err
	}
	q := store.BotTriggerChange{BotID: id, TriggerID: triggerID, Action: r.Review.Action, ExpectedRevision: r.Review.Body.ExpectedRevision, Reason: r.Review.Body.Reason}
	if r.Review.Body.Definition != nil {
		q.Definition = *r.Review.Body.Definition
	}
	return repo.(store.BotTriggerStore).ChangeTrigger(ctx, q)
}

func TestBotScheduleToolsStatelessReviewHasNoJobReceipt(t *testing.T) {
	f, repo := scheduleFixture(t, store.BotScopePersonal)
	r, c := scheduleFactory(t, f, repo)
	out := basicCall(t, r, c, "schedule_job", map[string]any{"name": "Daily", "mode": "adaptive", "schedule": "0 9 * * *", "instructions": "Report"})
	if out["job_id"] != nil || out["operation_id"] != nil {
		t.Fatalf("stateless request claimed a durable identity: %v", out)
	}
}

func TestBotScheduleToolsReviewedLifecycle(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, repo := scheduleFixture(t, scope)
			r, c := scheduleFactory(t, f, repo)
			if out := basicCall(t, r, c, "list_scheduled_jobs", map[string]any{}); out["count"] != float64(0) {
				t.Fatalf("empty list: %v", out)
			}
			args := map[string]any{"name": "Daily check", "mode": "adaptive", "schedule": "0 9 * * *", "timezone": "UTC", "instructions": "Review status and report."}
			proposal := decodeScheduleReview(t, basicCall(t, r, c, "schedule_job", args))
			if proposal.Review.Method != "PUT" || !strings.HasSuffix(proposal.Review.Path, "?scope="+string(scope)) || proposal.Review.Body.Definition.Enabled {
				t.Fatalf("unsafe save: %+v", proposal)
			}
			if out := basicCall(t, r, c, "list_scheduled_jobs", map[string]any{}); out["count"] != float64(0) {
				t.Fatal("model persisted schedule")
			}
			if _, err := applyScheduleReview(c, repo, f.req.Bot.ID, proposal); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("run configuration bypass: %v", err)
			}
			r.Cleanup()
			if _, err := applyScheduleReview(f.ctx, repo, f.req.Bot.ID, proposal); err != nil {
				t.Fatal(err)
			}
			if _, err := applyScheduleReview(f.ctx, repo, f.req.Bot.ID, proposal); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale review replay: %v", err)
			}
			for _, enabled := range []bool{true, false} {
				r, c = scheduleFactory(t, f, repo)
				out := basicCall(t, r, c, "list_scheduled_jobs", map[string]any{})
				rows := out["jobs"].([]any)
				if len(rows) != 1 || rows[0].(map[string]any)["enabled"] != !enabled {
					t.Fatalf("list reviewed state: %v", out)
				}
				update := decodeScheduleReview(t, basicCall(t, r, c, "update_scheduled_job", map[string]any{"job_id": proposal.JobID, "enabled": enabled}))
				if update.Review.Body.Definition != nil {
					t.Fatal("enable bundled definition")
				}
				r.Cleanup()
				if _, err := applyScheduleReview(f.ctx, repo, f.req.Bot.ID, update); err != nil {
					t.Fatal(err)
				}
			}
			r, c = scheduleFactory(t, f, repo)
			for _, entry := range r.Agent.Tools {
				if entry.Name() == "update_scheduled_job" {
					_, err := entry.(botRunnableTool).Run(c, map[string]any{"job_id": proposal.JobID, "schedule": "0 10 * * *", "enabled": true})
					if !errors.Is(err, store.ErrBotInvalid) {
						t.Fatalf("bundled recurrence review accepted: %v", err)
					}
				}
			}
			changed := decodeScheduleReview(t, basicCall(t, r, c, "update_scheduled_job", map[string]any{"job_id": proposal.JobID, "schedule": "0 10 * * *"}))
			if changed.Review.Body.Definition.Enabled || changed.Review.Body.Definition.Fingerprint != "" || changed.Review.Body.Definition.ScheduleID != nil {
				t.Fatal("copied activation evidence")
			}
			r.Cleanup()
			if _, err := applyScheduleReview(f.ctx, repo, f.req.Bot.ID, changed); err != nil {
				t.Fatal(err)
			}
			r, c = scheduleFactory(t, f, repo)
			removed := decodeScheduleReview(t, basicCall(t, r, c, "remove_scheduled_job", map[string]any{"job_id": proposal.JobID}))
			if removed.Review.Method != "DELETE" {
				t.Fatal("wrong removal method")
			}
			r.Cleanup()
			if _, err := applyScheduleReview(f.ctx, repo, f.req.Bot.ID, removed); err != nil {
				t.Fatal(err)
			}
			r, c = scheduleFactory(t, f, repo)
			if out := basicCall(t, r, c, "list_scheduled_jobs", map[string]any{}); out["count"] != float64(0) {
				t.Fatalf("not removed: %v", out)
			}
		})
	}
}

func TestBotScheduleToolsForeignAndUnsafeRequests(t *testing.T) {
	f, repo := scheduleFixture(t, store.BotScopePersonal)
	foreign, err := repo.CreateDraft(f.ctx, store.BotCreateRequest{Name: "Other Bot", OwnerID: f.req.Bot.OwnerID, Version: f.req.Version, IdempotencyKey: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	foreignID := uuid.New()
	_, err = repo.(store.BotTriggerStore).ChangeTrigger(f.ctx, store.BotTriggerChange{BotID: foreign.ID, TriggerID: foreignID, ExpectedRevision: foreign.Revision, Action: store.BotTriggerSave, Reason: "Other Bot", Definition: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 9 * * *", Timezone: "UTC", Instructions: "Other task"}})
	if err != nil {
		t.Fatal(err)
	}
	r, c := scheduleFactory(t, f, repo)
	for _, tc := range []struct {
		name string
		args map[string]any
		want error
	}{
		{"update_scheduled_job", map[string]any{"job_id": foreignID.String(), "enabled": true}, store.ErrBotNotFound},
		{"remove_scheduled_job", map[string]any{"job_id": foreignID.String()}, store.ErrBotNotFound},
		{"remove_scheduled_job", map[string]any{"job_name": "Other Bot"}, store.ErrBotNotFound},
		{"schedule_job", map[string]any{"name": "bad", "mode": "adaptive", "schedule": "0 9 * * *", "instructions": "test", "scope": "team"}, store.ErrBotForbidden},
		{"schedule_job", map[string]any{"name": "bad", "mode": "routine", "schedule": "0 9 * * *", "flow": "other"}, store.ErrBotUnsupported},
		{"schedule_job", map[string]any{"name": "bad", "mode": "adaptive", "schedule": "0 9 * * *", "instructions": "test", "test_first": true}, store.ErrBotUnsupported},
		{"schedule_job", map[string]any{"name": "bad", "mode": "adaptive", "schedule": "@daily", "instructions": "test"}, store.ErrBotInvalid},
		{"schedule_job", map[string]any{"name": "bad", "mode": "adaptive", "schedule": "0 9 * * *", "instructions": "test", "delivery_mode": "team"}, store.ErrBotUnsupported},
	} {
		t.Run(tc.name+"/"+tc.want.Error(), func(t *testing.T) {
			for _, entry := range r.Agent.Tools {
				if entry.Name() == tc.name {
					_, err := entry.(botRunnableTool).Run(c, tc.args)
					if !errors.Is(err, tc.want) && !(tc.want == store.ErrBotUnsupported && err != nil && strings.Contains(err.Error(), "validating")) {
						t.Fatalf("got %v want %v", err, tc.want)
					}
					return
				}
			}
			t.Fatal("missing tool")
		})
	}
	if out := basicCall(t, r, c, "list_scheduled_jobs", map[string]any{}); out["count"] != float64(0) {
		t.Fatalf("foreign disclosure: %v", out)
	}
}

func TestBotScheduleToolsSchemaParity(t *testing.T) {
	f, repo := scheduleFixture(t, store.BotScopePersonal)
	r, _ := scheduleFactory(t, f, repo)
	ordinary, err := tools.GetSchedulerTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range ordinary {
		for _, got := range r.Agent.Tools {
			if want.Name() != got.Name() {
				continue
			}
			wd := want.(botRunnableTool).Declaration()
			gd := got.(botRunnableTool).Declaration()
			w, _ := json.Marshal([]any{wd.ParametersJsonSchema, wd.Parameters})
			g, _ := json.Marshal([]any{gd.ParametersJsonSchema, gd.Parameters})
			if want.Name() == "schedule_job" {
				if strings.Contains(string(g), "delivery_mode") || !strings.Contains(string(g), "home chat") {
					t.Fatalf("Bot create schema inherited external delivery: %s", g)
				}
				continue
			}
			if want.Name() == "update_scheduled_job" {
				expected, _ := json.Marshal([]any{botScheduleUpdateInputSchema(), gd.Parameters})
				if string(expected) != string(g) {
					t.Fatalf("Bot update schema drift: %s", g)
				}
				continue
			}
			if string(w) != string(g) {
				t.Fatalf("schema drift %s: %s != %s", want.Name(), w, g)
			}
		}
	}
}

func TestBotScheduleToolsEffectiveBinding(t *testing.T) {
	f, repo := scheduleFixture(t, store.BotScopePersonal)
	r, c := scheduleFactory(t, f, repo)
	proposal := decodeScheduleReview(t, basicCall(t, r, c, "schedule_job", map[string]any{"name": "Daily", "mode": "adaptive", "schedule": "0 9 * * *", "instructions": "Report"}))
	r.Cleanup()
	if _, err := applyScheduleReview(f.ctx, repo, f.req.Bot.ID, proposal); err != nil {
		t.Fatal(err)
	}
	r, c = scheduleFactory(t, f, repo)
	enable := decodeScheduleReview(t, basicCall(t, r, c, "update_scheduled_job", map[string]any{"job_id": proposal.JobID, "enabled": true}))
	r.Cleanup()
	if _, err := applyScheduleReview(f.ctx, repo, f.req.Bot.ID, enable); err != nil {
		t.Fatal(err)
	}
	b, _ := repo.Get(f.ctx, f.req.Bot.ID)
	v, _ := repo.GetVersion(f.ctx, b.ID, b.Version)
	tr := v.Triggers[0]
	job := f.services.PersonalScheduler.Get(f.ctx, tr.ScheduleID.String())
	job.Enabled = false
	next := time.Now().Add(time.Hour).Truncate(time.Second)
	job.NextRun = &next
	job.LastStatus = "paused"
	job.Name = "Effective daily"
	if err := f.services.PersonalScheduler.Update(f.ctx, job); err != nil {
		t.Fatal(err)
	}
	r, c = scheduleFactory(t, f, repo)
	out := basicCall(t, r, c, "list_scheduled_jobs", map[string]any{"filter": "Effective"})
	rows := out["jobs"].([]any)
	if len(rows) != 1 {
		t.Fatalf("name filter %v", out)
	}
	row := rows[0].(map[string]any)
	if row["enabled"] != false || row["next_run"] != next.Format(time.RFC3339) || row["last_status"] != "paused" {
		t.Fatalf("not effective state %v", out)
	}
	job.Payload.BotID = uuid.NewString()
	if err := f.services.PersonalScheduler.Update(f.ctx, job); err != nil {
		t.Fatal(err)
	}
	for _, entry := range r.Agent.Tools {
		if entry.Name() == "list_scheduled_jobs" {
			if _, err := entry.(botRunnableTool).Run(c, map[string]any{}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("foreign binding exposed %v", err)
			}
		}
	}
}

type scheduleAppliedCapture struct {
	store.BotAuthorizedOperationStore
}

func (s scheduleAppliedCapture) EnqueueAuthorizedOperation(context.Context, store.BotAuthorizedOperationRequest) (*store.BotOperation, error) {
	return &store.BotOperation{ID: uuid.New(), Status: store.BotOperationApplied}, nil
}

func TestBotScheduleToolsProviderDurablePending(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, repo := scheduleFixture(t, scope)
			policy := func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
				return store.BotMandatePolicy{TTL: time.Hour, Scopes: []string{string(execution.CapabilityToolExecute)}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{"schedule_job"}}}, Limits: store.BotMandateLimits{MinScheduleIntervalSeconds: 60, MaxSchedules: 4, MaxDailyRuns: 2, MaxConcurrentRuns: 1, MaxRuntimeSeconds: 60, MaxToolCalls: 20, MaxTokens: 100, MaxTotalToolCalls: 30, MaxTotalTokens: 150}}, nil
			}
			owner, err := execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: f.req.Principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			owner = store.WithBotMessageAdmission(owner, execution.NewBotTurnAuthorizationRegistry(2))
			owner = store.WithBotMandateAdmission(owner, policy)
			admission, err := repo.(store.BotMessageStore).AdmitMessage(owner, store.BotMessageRequest{BotID: f.req.Bot.ID, Version: 1, Instructions: "Every day at nine report status", IdempotencyKey: "schedule-source"})
			if err != nil {
				t.Fatal(err)
			}
			f.req.Run = admission.Run
			b, err := repo.Get(owner, f.req.Bot.ID)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Bot = *b
			id := execution.BotRunIdentity{BotID: b.ID, RunID: admission.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}
			worker := execution.WithBotRunIdentity(owner, id)
			worker, _ = bot.WithToolOutcomes(worker)
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: admission.Run.ID, WorkerID: "provider-test", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			f.req.Run.Lease = lease
			rt, err := entstore.NewBotAuthorizedOperationRuntime(repo, policy, func(_ context.Context, a store.BotScheduleAuthorization) error {
				if a.Instructions != "Every day at nine report status" {
					return store.ErrBotForbidden
				}
				return nil
			}, func(context.Context, store.Bot, store.BotVersion) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			auth, err := execution.VerifyBotMandateAuthorization(worker, rt.(store.BotMandateRuntime), *admission.Run.MandateRef, execution.BotMandatePurposeTask)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: rt.(store.BotMandateRuntime), Identity: id}
			f.services.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
				return rt, nil
			}
			ts, err := BotScheduleTools(f.req, &BotChatScope{Services: f.services})
			if err != nil {
				t.Fatal(err)
			}
			c := basicToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: worker, session: admission.Run.SessionID.String()}}
			var chosen botRunnableTool
			for _, entry := range ts {
				if entry.Name() == "schedule_job" {
					chosen = entry.(botRunnableTool)
				}
			}
			request := &model.LLMRequest{}
			if err := chosen.ProcessRequest(c, request); err != nil {
				t.Fatal(err)
			}
			provider, ok := request.Tools["schedule_job"].(interface {
				Run(tool.Context, any) (map[string]any, error)
			})
			if !ok {
				t.Fatal("provider executable missing")
			}
			args := map[string]any{"name": "Daily", "mode": "adaptive", "schedule": "0 9 * * *", "timezone": "UTC", "instructions": "report status"}
			out, err := provider.Run(c, args)
			if err != nil {
				t.Fatal(err)
			}
			if out["status"] != "pending" || out["operation_id"] == nil || out["job_id"] != nil {
				t.Fatalf("not pending: %v", out)
			}
			outcome, observed := bot.TakeToolOutcome(c, c.FunctionCallID())
			if !observed || !outcome.Verified || len(outcome.Changed) != 0 || outcome.NextRunAt != nil {
				t.Fatalf("durable enqueue lacks capture-only attestation: %+v observed=%v", outcome, observed)
			}
			again, err := provider.Run(c, args)
			if err != nil || again["operation_id"] != out["operation_id"] {
				t.Fatalf("replay %v %v", again, err)
			}
			ops, err := rt.ListAuthorizedOperations(worker, *admission.Run.MandateRef)
			if err != nil || len(ops) != 1 || ops[0].Status != store.BotOperationPending || !ops[0].Payload.Schedule.Trigger.Enabled || ops[0].Receipt != nil {
				t.Fatalf("not durably pending: %+v %v", ops, err)
			}
			if _, err = rt.ApplyAuthorizedOperation(worker, *admission.Run.MandateRef, ops[0].ID); !errors.Is(err, store.ErrBotBusy) {
				t.Fatalf("applied before turn ended: %v", err)
			}
			for _, scenario := range []string{"absent", "read-error", "wrong-id", "wrong-run", "wrong-digest", "wrong-payload", "applied", "missing-provenance"} {
				t.Run(scenario, func(t *testing.T) {
					bot.TakeToolOutcome(c, c.FunctionCallID())
					f.services.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
						return scheduleCaptureReadback{BotAuthorizedOperationStore: rt, scenario: scenario}, nil
					}
					if out, err := provider.Run(c, args); err == nil || out != nil {
						t.Fatalf("unverified readback accepted: %v %v", out, err)
					}
					if _, ok := bot.TakeToolOutcome(c, c.FunctionCallID()); ok {
						t.Fatal("unverified readback minted an attestation")
					}
				})
			}
			f.services.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
				return rt, nil
			}
			args["instructions"] = "different task"
			if _, err = provider.Run(c, args); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("changed replay: %v", err)
			}
			f.services.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
				return scheduleAppliedCapture{}, nil
			}
			if out, err := provider.Run(c, args); !errors.Is(err, store.ErrBotConflict) || out != nil {
				t.Fatalf("capture claimed application: %v %v", out, err)
			}
		})
	}
}

func TestBotScheduleToolsRequireBackendAndExactApproval(t *testing.T) {
	f := newBotFactoryFixture(t)
	if _, err := BotScheduleTools(f.req, nil); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
	if _, err := BotScheduleTools(f.req, &BotChatScope{Services: f.services}); err != nil {
		t.Fatalf("read-only review adapter unnecessarily requires mutation interface: %v", err)
	}
	f, repo := scheduleFixture(t, store.BotScopePersonal)
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, q bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
		r, err := prepare(ctx, q, scope)
		r.PolicyRuntime.Classifier = BotCatalogueClassifier{}
		return r, err
	}
	r, c := scheduleFactory(t, f, repo)
	for _, entry := range r.Agent.Tools {
		if entry.Name() != "schedule_job" {
			continue
		}
		_, err := entry.(botRunnableTool).Run(c, map[string]any{"name": "Daily", "mode": "adaptive", "schedule": "0 9 * * *", "instructions": "Report"})
		if err == nil {
			t.Fatal("unknown scheduling action bypassed exact policy approval")
		}
		return
	}
	t.Fatal("missing tool")
}
