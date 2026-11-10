package launcher

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// The executable-tool assertions below remain direct. Settle their source run
// through the real quality controller rather than forging a successful commit.
type resumeReceiptModel struct{ model.LLM }

func (resumeReceiptModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText("The selected control was captured and remains pending; no schedule has been applied.", "model"), TurnComplete: true}, nil)
	}
}

func settleResumeSource(t *testing.T, f *botFactoryFixture, repo store.BotStore, worker context.Context) {
	t.Helper()
	f.req.Authorization = interactiveFactoryAuthorization{}
	f.req.Principal = f.req.Mandate.Authorization.Principal(execution.SurfaceStudio)
	worker, err := execution.WithPrincipal(worker, f.req.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sessions.Get(worker, &session.GetRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: f.req.Bot.HomeSessionID.String()}); err != nil {
		if _, err := f.sessions.Create(worker, &session.CreateRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: f.req.Bot.HomeSessionID.String()}); err != nil {
			t.Fatal(err)
		}
	}
	f.cfg.WrapModel = func(_ context.Context, m model.LLM, _ execution.RunBudget) (model.LLM, error) {
		return continuityAssessmentModel{resumeReceiptModel{m}}, nil
	}
	factory, err := NewBotChatAgentFactory(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := api.NewBotChatRuntimeAdapter(factory)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(worker, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil || result.Status != store.RunSucceeded || result.Report.Quality == nil || result.Report.Quality.Outcome != store.BotWorkAccepted {
		t.Fatalf("source receipt did not pass quality: %+v %v", result, err)
	}
	if err := repo.CommitRun(worker, store.BotRunCommit{Lease: f.req.Run.Lease, Status: result.Status, Report: result.Report, Usage: result.Usage}); err != nil {
		t.Fatal(err)
	}
}

func TestResumeTargetSchemaIdentity(t *testing.T) {
	for _, schema := range []bool{false, true} {
		s := botProposalInputSchema()
		if schema {
			s = botMandatedProposalInputSchema()
		}
		ids := s.Properties["payload"].Properties["resume_targets"].Properties["schedule_ids"]
		for _, text := range []string{ids.Description, ids.Items.Description} {
			for _, word := range []string{"scheduler job", "schedule_id", "trigger"} {
				if !strings.Contains(text, word) {
					t.Fatalf("missing %q in %s", word, text)
				}
			}
		}
	}
}

// Actual factory executables and router-backed SQLite capture; compute/model
// are controlled. This does not exercise a live provider or scheduler daemon.
func TestResumeFactoryListsCanonicalTargetThroughStore(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, repo := scheduleFixture(t, scope)
			if scope == store.BotScopePersonal {
				f.services.PersonalBots = repo
			} else {
				f.services.Bots = repo
			}
			policy := func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
				return store.BotMandatePolicy{TTL: time.Hour, Scopes: []string{string(execution.CapabilityChat), string(execution.CapabilityToolExecute)}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{"read_file", "schedule_job", "list_scheduled_jobs", "propose_bot_control", entstore.RoutineControlResource(store.BotControlResumeTasks)}}}, Limits: store.BotMandateLimits{MinScheduleIntervalSeconds: 60, MaxSchedules: 4, MaxDailyRuns: 4, MaxConcurrentRuns: 1, MaxRuntimeSeconds: 60, MaxToolCalls: 20, MaxTokens: 100, MaxTotalToolCalls: 30, MaxTotalTokens: 150}}, nil
			}
			owner, err := execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: f.req.Principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			registry := execution.NewBotTurnAuthorizationRegistry(4)
			defer registry.Close()
			owner = store.WithBotMessageAdmission(store.WithBotMandateAdmission(owner, policy), registry)
			rt, err := entstore.NewBotAuthorizedOperationRuntime(repo, policy, func(context.Context, store.BotScheduleAuthorization) error { return nil }, func(context.Context, store.Bot, store.BotVersion) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			f.services.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
				return rt, nil
			}
			admit := func(key string) (context.Context, store.BotMandateRef) {
				t.Helper()
				b, e := repo.Get(owner, f.req.Bot.ID)
				if e != nil {
					t.Fatal(e)
				}
				a, e := repo.(store.BotMessageStore).AdmitMessage(owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: key, Instructions: key})
				if e != nil {
					t.Fatal(e)
				}
				id := execution.BotRunIdentity{BotID: b.ID, RunID: a.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}
				worker := execution.WithBotRunIdentity(owner, id)
				lease, e := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: a.Run.ID, WorkerID: key, ExpiresAt: time.Now().Add(time.Minute)})
				if e != nil {
					t.Fatal(e)
				}
				run, e := repo.GetRun(worker, b.ID, a.Run.ID)
				if e != nil {
					t.Fatal(e)
				}
				v, e := repo.GetVersion(owner, b.ID, b.Version)
				if e != nil {
					t.Fatal(e)
				}
				f.req.Bot, f.req.Version, f.req.Run = *b, *v, *run
				f.req.Run.Lease = lease
				f.req.Policy, e = execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: v.Autonomy.Level, AllowedTools: []string{"list_scheduled_jobs", "propose_bot_control"}})
				if e != nil {
					t.Fatal(e)
				}
				purpose := execution.BotMandatePurposeTask
				if b.Status == store.BotPaused {
					purpose = execution.BotMandatePurposeOperation
				}
				auth, e := execution.VerifyBotMandateAuthorization(worker, rt.(store.BotMandateRuntime), *a.Run.MandateRef, purpose)
				if e != nil {
					t.Fatal(e)
				}
				f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: rt.(store.BotMandateRuntime), Identity: id}
				f.req.Instructions = key
				_, e = f.sessions.Create(owner, &session.CreateRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: run.SessionID.String(), State: map[string]any{"execution_owner": "bot"}})
				if e != nil {
					t.Fatal(e)
				}
				return worker, *a.Run.MandateRef
			}
			worker, ref := admit("Schedule a daily report")
			op, err := rt.EnqueueAuthorizedOperation(worker, store.BotAuthorizedOperationRequest{Lease: f.req.Run.Lease, MandateRef: ref, IdempotencyKey: "seed", Payload: store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "daily report", Schedule: &store.BotControlSchedule{ExpectedRevision: f.req.Bot.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 9 * * *", Timezone: "UTC", Instructions: "report", Enabled: true}}}})
			if err != nil {
				t.Fatal(err)
			}
			settleResumeSource(t, f, repo, worker)
			op, err = rt.ApplyAuthorizedOperation(owner, ref, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			scheduleID := op.Receipt.Schedule.ScheduleID
			b, err := repo.Get(owner, f.req.Bot.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.Pause(owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "pause"}); err != nil {
				t.Fatal(err)
			}
			worker, ref = admit("Resume only the daily report")
			f.req.Authorization = interactiveFactoryAuthorization{}
			f.req.Principal = f.req.Mandate.Authorization.Principal(f.req.Principal.Surface)
			f.ctx, err = execution.WithPrincipal(owner, f.req.Principal)
			if err != nil {
				t.Fatal(err)
			}
			resources, ctx := basicFactory(t, f, "list_scheduled_jobs", "propose_bot_control")
			listed := basicCall(t, resources, ctx, "list_scheduled_jobs", map[string]any{})
			row := listed["jobs"].([]any)[0].(map[string]any)
			if row["schedule_id"] != scheduleID.String() || row["job_id"] != row["schedule_id"] || row["id"] == row["schedule_id"] {
				t.Fatalf("ambiguous list: %v", row)
			}
			if row["enabled"] != false {
				t.Fatal("list did not observe pause")
			}
			var proposal botRunnableTool
			for _, entry := range resources.Agent.Tools {
				if entry.Name() == botProposalToolName {
					proposal = entry.(botRunnableTool)
				}
			}
			if proposal == nil {
				t.Fatal("factory omitted resume capture")
			}
			request := &model.LLMRequest{}
			if err := proposal.ProcessRequest(ctx, request); err != nil {
				t.Fatal(err)
			}
			executable := request.Tools[botProposalToolName].(botRunnableTool)
			call := func(target any) map[string]any {
				t.Helper()
				result, e := executable.Run(ctx, map[string]any{"payload": map[string]any{"operation": string(store.BotControlResumeTasks), "reason": "resume selected report", "resume_targets": map[string]any{"schedule_ids": []any{target}}}})
				if e != nil {
					t.Fatal(e)
				}
				return result
			}
			bad := call(row["id"])
			if bad["error_code"] != store.BotResumeInvalidTargetCode || bad["operation_id"] != nil || bad["invalid_target"] != row["id"] {
				t.Fatalf("wrong ID accepted: %v", bad)
			}
			ops, err := rt.ListAuthorizedOperations(owner, ref)
			if err != nil || len(ops) != 0 {
				t.Fatalf("bad target persisted: %+v %v", ops, err)
			}
			out := call(row["schedule_id"])
			if out["status"] != store.BotOperationPending || out["operation_id"] == nil {
				t.Fatalf("canonical ID not captured: %v", out)
			}
			saved, err := rt.ReadAuthorizedOperation(owner, ref, uuid.MustParse(out["operation_id"].(string)))
			if err != nil || saved.Payload.ResumeTargets.ScheduleIDs[0] != scheduleID {
				t.Fatalf("wrong durable target: %+v %v", saved, err)
			}
			// Preserve exact finite task authority, not merely the scheduler ID.
			jobs := f.services.Scheduler
			if scope == store.BotScopePersonal {
				jobs = f.services.PersonalScheduler
			}
			before, _ := json.Marshal(jobs.Get(owner, scheduleID.String()).Payload)
			settleResumeSource(t, f, repo, worker)
			saved, err = rt.ApplyAuthorizedOperation(owner, ref, saved.ID)
			if err != nil || saved.Status != store.BotOperationApplied {
				t.Fatalf("resume not applied: %+v %v", saved, err)
			}
			job := jobs.Get(owner, scheduleID.String())
			after, _ := json.Marshal(job.Payload)
			if !job.Enabled || job.NextRun == nil || string(before) != string(after) {
				t.Fatal("resume lost original task authority")
			}
		})
	}
}
