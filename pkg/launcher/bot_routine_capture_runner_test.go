package launcher

import (
	"context"
	"encoding/json"
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
	"github.com/google/uuid"
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

// continuityAssessmentModel is a deterministic tool-free assessor used only by
// this context acceptance. Runtime quality semantics have separate acceptance.
type continuityAssessmentModel struct{ model.LLM }

func (m continuityAssessmentModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	system := providerSystem(req)
	if !strings.Contains(system, "BOT_WORK_PREPARE_V1") && !strings.Contains(system, "BOT_WORK_REVIEW_V1") && !strings.Contains(system, "BOT_WORK_PENDING_REVIEW_V1") {
		return m.LLM.GenerateContent(ctx, req, stream)
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		var input string
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				input += p.Text
			}
		}
		var out any
		if strings.Contains(system, "BOT_WORK_PREPARE_V1") {
			var contract store.BotWorkContract
			if err := json.Unmarshal([]byte(input), &contract); err != nil {
				yield(nil, err)
				return
			}
			sources := []string{"assignment", "charter"}
			for _, g := range contract.Goals {
				sources = append(sources, "goal:"+g.ID.String())
			}
			out = struct {
				Criteria []store.BotWorkCriterion `json:"criteria"`
			}{[]store.BotWorkCriterion{{ID: "continuity-context", SourceIDs: sources, Text: "Report current standards or truthful pending control receipt"}}}
		} else {
			var w store.BotWorkState
			if err := json.Unmarshal([]byte(input), &w); err != nil {
				yield(nil, err)
				return
			}
			if w.Candidate == nil {
				yield(nil, errors.New("missing candidate"))
				return
			}
			out = store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "continuity-context", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Controlled context-only assessment; no external effects claimed"}}}
			if strings.Contains(system, "BOT_WORK_PENDING_REVIEW_V1") {
				var pending struct {
					Scope    string `json:"scope"`
					Controls []struct {
						Operation      store.BotControlOperation `json:"operation"`
						Acknowledgment string                    `json:"acknowledgment"`
						Payload        store.BotControlPayload   `json:"payload"`
					} `json:"trusted_pending_controls"`
				}
				if err := json.Unmarshal([]byte(input), &pending); err != nil {
					yield(nil, err)
					return
				}
				current := out.(store.BotWorkReview)
				pass := pending.Scope == "current_turn_pending_acknowledgment" && len(pending.Controls) == 1
				for _, control := range pending.Controls {
					p := control.Payload
					pass = pass && p.Validate() == nil && p.Operation == control.Operation && p.Operation == store.BotControlGoalCreate && p.Goal != nil && p.Goal.Goal.Title == "Orlando weather clarity" && p.Goal.Goal.CompletionCriteria == "Each weather update is easy to understand." && strings.Contains(w.Contract.Instructions, "Orlando weather clarity") && strings.Contains(w.Contract.Instructions, "easy to understand") && strings.Contains(w.Candidate.Text, control.Acknowledgment) && strings.Contains(control.Acknowledgment, "pending")
				}
				if !pass {
					current.Criteria[0].Verdict = store.BotCriterionFailed
				}
				current.Criteria[0].Explanation = "Checked original clarity request against complete trusted goal payload and pending acknowledgment; application is deferred."
				deferred := store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "continuity-context", Verdict: store.BotCriterionUnverified, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Worker application is not observed in the source turn."}}}
				out = map[string]any{"current_turn": current, "deferred_application": deferred}
			}
		}
		raw, err := json.Marshal(out)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 3, TotalTokenCount: 10}}, nil)
	}
}

func TestRoutineGoalSourceRunnerThenWorkerApply(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint("mixed=", mixed), func(t *testing.T) {
			runRoutineGoalSource(t, store.BotScopePersonal, mixed)
		})
	}
}

func runRoutineGoalSource(t *testing.T, scope store.BotScope, mixed bool) (*botFactoryFixture, store.BotStore, context.Context) {
	f, repo := scheduleFixture(t, scope)
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
	if scope == store.BotScopePersonal {
		f.services.PersonalBots = repo
	} else {
		f.services.Bots = repo
	}
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
	f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) {
		return continuityAssessmentModel{llm}, nil
	}
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
	return f, repo, owner
}
func TestBotWorkResponsibilityContinuityRunner(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, repo, owner := runRoutineGoalSource(t, scope, false)
			goals, err := repo.ListGoals(owner, f.req.Bot.ID)
			if err != nil || len(goals) != 1 {
				t.Fatalf("committed goals: %+v %v", goals, err)
			}
			original := goals[0]
			if original.CompletionCriteria != "Each weather update is easy to understand." {
				t.Fatal("requirement was not applied")
			}
			// Reconstruct from the durable input, with no repeated owner standards.
			fresh := func(key string) *store.BotWorkState {
				b, err := repo.Get(owner, f.req.Bot.ID)
				if err != nil {
					t.Fatal(err)
				}
				admissionOwner := store.WithBotMessageAdmission(owner, execution.NewBotTurnAuthorizationRegistry(2))
				admission, err := repo.(store.BotMessageStore).AdmitMessage(admissionOwner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, Instructions: "What are your current reporting standards? Stay paused.", IdempotencyKey: key})
				if err != nil {
					t.Fatal(err)
				}
				id := execution.BotRunIdentity{BotID: b.ID, RunID: admission.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}
				worker := execution.WithBotRunIdentity(owner, id)
				input, err := repo.(store.BotRuntimeStore).LoadRunInput(worker, b.ID, admission.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: admission.Run.ID, WorkerID: "continuity", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				input.Run.Status, input.Run.Lease = store.RunRunning, lease
				input.Work, err = repo.(store.BotWorkStore).LoadWork(worker, lease)
				if err != nil {
					t.Fatal(err)
				}
				if input.Work == nil || len(input.Work.Contract.Goals) != 1 {
					t.Fatal("fresh claim omitted saved responsibility")
				}
				auth, err := execution.VerifyBotMandateAuthorization(worker, f.req.Mandate.Runtime, *admission.Run.MandateRef, execution.BotMandatePurposeOperation)
				if err != nil {
					t.Fatal(err)
				}
				f.req.Bot, f.req.Version, f.req.Run, f.req.Goals, f.req.Instructions = input.Bot, input.Version, input.Run, input.Goals, input.Instructions
				f.req.Principal = auth.Principal(execution.SurfaceStudio)
				f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: f.req.Mandate.Runtime, Identity: id}
				f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"propose_bot_control"}})
				if err != nil {
					t.Fatal(err)
				}
				f.req.Budget, err = bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				f.ctx, err = execution.WithPrincipal(context.Background(), f.req.Principal)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.sessions.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: input.Run.SessionID.String(), State: map[string]any{"execution_owner": "bot"}})
				if err != nil {
					t.Fatal(err)
				}
				llm := &botFactoryTestModel{}
				f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) {
					return continuityAssessmentModel{llm}, nil
				}
				factory, err := NewBotChatAgentFactory(f.cfg)
				if err != nil {
					t.Fatal(err)
				}
				adapter, err := api.NewBotChatRuntimeAdapter(factory)
				if err != nil {
					t.Fatal(err)
				}
				// The controlled assessor keeps this a continuity acceptance, not
				// a claim about live-model judgment. Use the real pinned snapshot.
				f.req.Work = input.Work.Clone()
				_, err = adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				if len(llm.requests) == 0 {
					t.Fatal("real runner never called provider")
				}
				system := providerSystem(llm.requests[0])
				want := input.Work.Contract.Goals[0].CompletionCriteria
				if !strings.Contains(system, want) || !strings.Contains(system, original.ID.String()) || !strings.Contains(system, `"current_goals"`) {
					t.Fatalf("provider missed committed criteria: %s", system)
				}
				for _, c := range llm.requests[0].Contents {
					for _, p := range c.Parts {
						if c.Role == "user" && strings.Contains(p.Text, want) {
							t.Fatal("criteria required owner restatement")
						}
					}
				}
				// No claimed success for this context-only occurrence.
				if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: store.BotReport{Summary: "Continuity context exercised", Unverified: []string{"Context-only harness; deliverable quality not assessed"}}}); err != nil {
					t.Fatal(err)
				}
				return input.Work.Clone()
			}
			old := fresh("continuity-before")
			b, err := repo.Get(owner, f.req.Bot.ID)
			if err != nil {
				t.Fatal(err)
			}
			triggerID := uuid.New()
			triggers := repo.(store.BotTriggerStore)
			definition := store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 9 * * *", Timezone: "UTC", Instructions: "Report weather", Enabled: false}
			if _, err := triggers.ChangeTrigger(owner, store.BotTriggerChange{BotID: b.ID, TriggerID: triggerID, ExpectedRevision: b.Revision, Action: store.BotTriggerSave, Reason: "Owner cadence", Definition: definition}); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(owner, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			definition.Cron = "0 10 * * *"
			if _, err := triggers.ChangeTrigger(owner, store.BotTriggerChange{BotID: b.ID, TriggerID: triggerID, ExpectedRevision: b.Revision, Action: store.BotTriggerSave, Reason: "Cadence only", Definition: definition}); err != nil {
				t.Fatal(err)
			}
			afterCadence := fresh("continuity-cadence")
			if afterCadence.Contract.Goals[0].Revision != original.Revision || afterCadence.Contract.Goals[0].CompletionCriteria != original.CompletionCriteria {
				t.Fatal("cadence reset standards")
			}
			changed := original
			changed.CompletionCriteria = "Each weather update cites its source and observation time."
			if _, err := repo.SaveGoal(owner, store.BotGoalUpdate{BotID: b.ID, Goal: changed, ExpectedRevision: original.Revision}); err != nil {
				t.Fatal(err)
			}
			afterChange := fresh("continuity-requirement")
			if afterChange.Contract.Goals[0].CompletionCriteria != changed.CompletionCriteria || afterChange.Contract.Goals[0].Revision <= old.Contract.Goals[0].Revision || old.Contract.Goals[0].CompletionCriteria != original.CompletionCriteria {
				t.Fatal("current/pinned criteria were conflated")
			}
			t.Log("real factory/ChatRunner: captured and applied goal; fresh provider context retained criteria; cadence preserved standards; requirement revision affected later run only")
		})
	}
}
