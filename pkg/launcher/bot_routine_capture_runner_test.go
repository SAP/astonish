package launcher

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Controlled model, real factory/tool/runner and temporary SQLite runtime.
// Not a replay or a live-provider acceptance receipt.
type routineGoalModel struct {
	calls    int
	badFirst bool
}

func (*routineGoalModel) Name() string { return "routine-goal-fixture" }
func (m *routineGoalModel) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls > 1 && (!m.badFirst || m.calls > 2) {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Done, goal applied.", "model"), TurnComplete: true}, nil)
			return
		}
		goal := map[string]any{"title": "Orlando weather clarity", "completion_criteria": "Each weather update is easy to understand."}
		var branch any = map[string]any{"goal": goal}
		if m.badFirst && m.calls == 1 {
			branch = goal
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("goal-%d", m.calls), Name: botProposalToolName, Args: map[string]any{"payload": map[string]any{"operation": "goal_create", "reason": "Owner requested clarity", "goal": branch}}}}}}, TurnComplete: true}, nil)
	}
}

func TestRoutineGoalSourceRunnerThenWorkerApply(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint("mixed=", mixed), func(t *testing.T) {
			f, repo := scheduleFixture(t, store.BotScopePersonal)
			b, err := repo.Get(f.ctx, f.req.Bot.ID)
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Pause(f.ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Stay paused"})
			if err != nil {
				t.Fatal(err)
			}
			policy := func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
				return store.BotMandatePolicy{TTL: time.Hour, Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{"propose_bot_control", entstore.RoutineControlResource(store.BotControlGoalCreate)}}}, Limits: store.BotMandateLimits(config.DefaultBotMandateConfig())}, nil
			}
			owner, err := execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: f.req.Principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			owner = store.WithBotMessageAdmission(store.WithBotMandateAdmission(owner, policy), execution.NewBotTurnAuthorizationRegistry(2))
			admission, err := repo.(store.BotMessageStore).AdmitMessage(owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, Instructions: "Keep a goal named Orlando weather clarity: make each weather update easy to understand. Stay paused.", IdempotencyKey: "goal-source"})
			if err != nil {
				t.Fatal(err)
			}
			id := execution.BotRunIdentity{BotID: b.ID, RunID: admission.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}
			worker := execution.WithBotRunIdentity(owner, id)
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: admission.Run.ID, WorkerID: "goal-test", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			rt, err := entstore.NewBotAuthorizedOperationRuntime(repo, policy, func(context.Context, store.BotScheduleAuthorization) error { return nil }, func(context.Context, store.Bot, store.BotVersion) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			auth, err := execution.VerifyBotMandateAuthorization(worker, rt.(store.BotMandateRuntime), *admission.Run.MandateRef, execution.BotMandatePurposeOperation)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Principal = auth.Principal(execution.SurfaceStudio)
			f.ctx, err = execution.WithPrincipal(context.Background(), f.req.Principal)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Bot = *b
			v, err := repo.GetVersion(owner, b.ID, b.Version)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Version = *v
			f.req.Run = admission.Run
			f.req.Run.Status, f.req.Run.Lease = store.RunRunning, lease
			f.req.Instructions = "Keep a goal named Orlando weather clarity: make each weather update easy to understand. Stay paused."
			f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: rt.(store.BotMandateRuntime), Identity: id}
			f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"propose_bot_control"}})
			if err != nil {
				t.Fatal(err)
			}
			f.req.Budget, err = bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			f.services.PersonalBots = repo
			f.services.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
				return rt, nil
			}
			_, err = f.sessions.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: admission.Run.SessionID.String(), State: map[string]any{"execution_owner": "bot"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.sessions.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: b.HomeSessionID.String(), State: map[string]any{"execution_owner": "bot"}})
			if err != nil {
				t.Fatal(err)
			}
			llm := &routineGoalModel{badFirst: mixed}
			f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
			factory, err := NewBotChatAgentFactory(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := api.NewBotChatRuntimeAdapter(factory)
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if !mixed && (runErr != nil || result.Status != store.RunSucceeded || len(result.Report.Unverified) != 0) {
				t.Fatalf("source: %+v %v", result, runErr)
			}
			if mixed && (result.Status == store.RunSucceeded || !strings.Contains(strings.Join(result.Report.Unverified, " "), "proposal input")) {
				t.Fatalf("mixed failure hidden: %+v %v", result, runErr)
			}
			if len(result.Report.Changed) != 0 || len(result.Report.Unchanged) != 1 || !strings.Contains(result.Report.Unchanged[0].Summary, "pending, not applied") {
				t.Fatalf("source falsely applied: %+v", result.Report)
			}
			ops, err := rt.ListAuthorizedOperations(worker, *admission.Run.MandateRef)
			if err != nil || len(ops) != 1 || ops[0].Status != store.BotOperationPending || ops[0].Receipt != nil {
				t.Fatalf("pending: %+v %v", ops, err)
			}
			if _, err = rt.ApplyAuthorizedOperation(worker, *admission.Run.MandateRef, ops[0].ID); !errors.Is(err, store.ErrBotBusy) {
				t.Fatalf("applied before source commit: %v", err)
			}
			if err = repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: result.Status, Report: result.Report}); err != nil {
				t.Fatal(err)
			}
			applied, err := rt.ApplyAuthorizedOperation(owner, *admission.Run.MandateRef, ops[0].ID)
			if err != nil || applied.Status != store.BotOperationApplied || applied.Receipt == nil {
				t.Fatalf("worker: %+v %v", applied, err)
			}
			after, err := repo.Get(owner, b.ID)
			if err != nil || after.Status != store.BotPaused {
				t.Fatalf("resumed background work: %+v %v", after, err)
			}
			source, err := repo.GetRun(owner, b.ID, admission.Run.ID)
			if err != nil || source.Status != result.Status || len(source.Report.Changed) != 0 {
				t.Fatalf("worker rewrote source: %+v %v", source, err)
			}
		})
	}
}
