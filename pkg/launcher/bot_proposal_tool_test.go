package launcher

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"testing"
	"time"
)

type proposalTestRepo struct {
	botFactoryTestRepo
	store.BotControlProposalStore
	calls   int
	request store.BotControlProposalRequest
	actor   string
}

func (r *proposalTestRepo) ProposeControl(ctx context.Context, q store.BotControlProposalRequest) (*store.BotControlProposal, error) {
	r.calls++
	r.request = q
	p, _ := execution.PrincipalFromContext(ctx)
	r.actor = p.Subject
	return &store.BotControlProposal{ID: uuid.New(), BotID: q.Lease.BotID, Digest: "saved", Status: store.BotControlProposed}, nil
}

func (r *proposalTestRepo) ReadControlProposal(_ context.Context, id store.BotID, pid uuid.UUID) (*store.BotControlProposal, error) {
	return &store.BotControlProposal{ID: pid, BotID: id, Digest: "saved", Status: store.BotControlProposed}, nil
}

type proposalTestContext struct {
	botFactoryToolContext
	sid string
}

func (c proposalTestContext) SessionID() string      { return c.sid }
func (c proposalTestContext) FunctionCallID() string { return "call-id" }

type scheduleProposalCallContext struct {
	proposalTestContext
	callID string
}

func (c scheduleProposalCallContext) FunctionCallID() string { return c.callID }

// Exercise the real durable issuer, not a pending-shaped enqueue mock.
func TestMandatedScheduleControlUsesAuthorizedCapture(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, operation := range []store.BotControlOperation{store.BotControlScheduleCreate, store.BotControlScheduleEdit, store.BotControlScheduleRemove} {
			t.Run(string(scope)+"/"+string(operation), func(t *testing.T) {
				f, repo := scheduleFixture(t, scope)
				trigger := store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 9 * * *", Timezone: "UTC", Instructions: "report status", Enabled: true}
				if operation != store.BotControlScheduleCreate {
					current, err := repo.Get(f.ctx, f.req.Bot.ID)
					if err != nil {
						t.Fatal(err)
					}
					trigger.ID = uuid.New()
					trigger.Enabled = false
					_, err = repo.(store.BotTriggerStore).ChangeTrigger(f.ctx, store.BotTriggerChange{BotID: current.ID, TriggerID: trigger.ID, Action: store.BotTriggerSave, ExpectedRevision: current.Revision, Reason: "Fixture schedule", Definition: trigger})
					if err != nil {
						t.Fatal(err)
					}
					current, err = repo.Get(f.ctx, current.ID)
					if err != nil {
						t.Fatal(err)
					}
					version, err := repo.GetVersion(f.ctx, current.ID, current.Version)
					if err != nil {
						t.Fatal(err)
					}
					trigger = version.Triggers[0]
					f.req.Version = *version
				}
				policy := func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
					return store.BotMandatePolicy{TTL: time.Hour, Scopes: []string{string(execution.CapabilityToolExecute)}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{"propose_bot_control"}}}, Limits: store.BotMandateLimits{MinScheduleIntervalSeconds: 60, MaxSchedules: 4, MaxDailyRuns: 2, MaxConcurrentRuns: 1, MaxRuntimeSeconds: 60, MaxToolCalls: 20, MaxTokens: 100, MaxTotalToolCalls: 30, MaxTotalTokens: 150}}, nil
				}
				owner, err := execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: f.req.Principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				owner = store.WithBotMessageAdmission(owner, execution.NewBotTurnAuthorizationRegistry(2))
				owner = store.WithBotMandateAdmission(owner, policy)
				admission, err := repo.(store.BotMessageStore).AdmitMessage(owner, store.BotMessageRequest{BotID: f.req.Bot.ID, Version: f.req.Version.Version, Instructions: "Schedule this briefing every day at 9am", IdempotencyKey: "proposal-schedule-source"})
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
				lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: admission.Run.ID, WorkerID: "proposal-schedule-test", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				f.req.Run.Lease = lease
				deny, sourceChecks := false, 0
				runtime, err := entstore.NewBotAuthorizedOperationRuntime(repo, policy, func(_ context.Context, a store.BotScheduleAuthorization) error {
					sourceChecks++
					if deny || a.Instructions != "Schedule this briefing every day at 9am" {
						return store.ErrBotForbidden
					}
					return nil
				}, func(context.Context, store.Bot, store.BotVersion) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				auth, err := execution.VerifyBotMandateAuthorization(worker, runtime.(store.BotMandateRuntime), *admission.Run.MandateRef, execution.BotMandatePurposeTask)
				if err != nil {
					t.Fatal(err)
				}
				f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: runtime.(store.BotMandateRuntime), Identity: id}
				f.services.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
					return runtime, nil
				}
				proposals := &proposalTestRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
				f.req.Run.Status = store.RunRunning
				binding := &botFactoryBinding{owner: store.WithServices(worker, f.services), req: f.req, verifyIsolation: func(context.Context) error { return nil }}
				resolved, err := botMandatedProposalInputSchema().Resolve(nil)
				if err != nil {
					t.Fatal(err)
				}
				captureTool := &botProposalTool{binding: binding, repo: proposals, validateInput: resolved.Validate}
				ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: binding.owner}, sid: admission.Run.SessionID.String()}
				payload := store.BotControlPayload{Operation: operation, Reason: "Owner requested schedule", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: trigger}}
				out, err := captureTool.Run(ctx, botProposalArgs{Payload: payload})
				if err != nil {
					t.Fatal(err)
				}
				outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
				if proposals.calls != 0 || sourceChecks == 0 || out["proposal_id"] != nil || out["operation_id"] == nil || out["status"] != string(store.BotOperationPending) || !strings.Contains(out["message"].(string), "pending") || !ok || !outcome.Verified || outcome.ScheduleCapture == nil || !outcome.ScheduleCapture.ValidFor(id) || outcome.RoutineCapture != nil || len(outcome.Changed) != 0 || outcome.NextRunAt != nil {
					t.Fatalf("not verified authorized pending capture: out=%+v outcome=%+v proposals=%d sourceChecks=%d", out, outcome, proposals.calls, sourceChecks)
				}
				if again, err := captureTool.Run(ctx, botProposalArgs{Payload: payload}); err != nil || again["operation_id"] != out["operation_id"] {
					t.Fatalf("exact replay changed capture: %v %v", again, err)
				}
				bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
				stale := payload
				stale.Reason = "Stale revision request"
				staleSchedule := *payload.Schedule
				staleSchedule.ExpectedRevision++
				stale.Schedule = &staleSchedule
				staleCtx := scheduleProposalCallContext{proposalTestContext: ctx, callID: "stale-revision"}
				if result, err := captureTool.Run(staleCtx, botProposalArgs{Payload: stale}); !errors.Is(err, store.ErrBotConflict) || result != nil {
					t.Fatalf("stale supplied revision was inferred or proposed: %v %v", result, err)
				}
				if _, ok := bot.TakeToolOutcome(staleCtx, staleCtx.FunctionCallID()); ok {
					t.Fatal("stale revision minted capture")
				}
				ops, err := runtime.ListAuthorizedOperations(worker, *admission.Run.MandateRef)
				if err != nil || len(ops) != 1 || !reflect.DeepEqual(ops[0].Payload, payload) || ops[0].IdempotencyKey != ctx.FunctionCallID() || ops[0].Receipt != nil {
					t.Fatalf("lost exact request: %+v %v", ops, err)
				}
				if _, err := runtime.ApplyAuthorizedOperation(worker, *admission.Run.MandateRef, ops[0].ID); !errors.Is(err, store.ErrBotBusy) {
					t.Fatalf("applied before source termination: %v", err)
				}
				for _, scenario := range []string{"absent", "read-error", "wrong-id", "wrong-run", "wrong-digest", "wrong-payload", "applied", "missing-provenance"} {
					t.Run(scenario, func(t *testing.T) {
						f.services.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
							return scheduleCaptureReadback{BotAuthorizedOperationStore: runtime, scenario: scenario}, nil
						}
						captureTool.attempts = 0 // isolate read-back refusal from the existing attempt cap
						if out, err := captureTool.Run(ctx, botProposalArgs{Payload: payload}); err == nil || out != nil {
							t.Fatalf("untrusted readback: %v %v", out, err)
						}
						if _, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID()); ok {
							t.Fatal("failed read minted outcome")
						}
					})
				}
				f.services.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
					return runtime, nil
				}
				deny = true
				denied := payload
				denied.Reason = "Denied distinct request"
				if out, err := captureTool.Run(ctx, botProposalArgs{Payload: denied}); !errors.Is(err, store.ErrBotForbidden) || out != nil {
					t.Fatalf("denial became review: %v %v", out, err)
				}
				if _, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID()); ok || proposals.calls != 0 {
					t.Fatal("denied request captured or proposed")
				}
			})
		}
	}
}
func TestBotProposalCapabilityStrictBoundAndCapped(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, intent := range []store.BotExecutionIntent{store.BotIntentRegular, store.BotIntentConversation} {
			t.Run(string(scope)+string(intent), func(t *testing.T) {
				f := newBotFactoryFixture(t)
				f.req.Bot.Scope = scope
				f.req.Run.Intent = intent
				if intent == store.BotIntentConversation {
					f.req.Bot.Status = store.BotPaused
				}
				repo := &proposalTestRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
				f.services.Bots, f.services.PersonalBots = repo, repo
				owner := store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services)
				binding := &botFactoryBinding{owner: owner, req: f.req, verifyIsolation: func(context.Context) error { return nil }}
				raw, err := newBotProposalTool(binding)
				if err != nil {
					t.Fatal(err)
				}
				tool := raw.(*botProposalTool)
				ledgerCtx, _ := bot.WithToolOutcomes(context.Background())
				binding.owner = bot.WithToolOutcomesFromContext(binding.owner, ledgerCtx)
				ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: ledgerCtx}, sid: f.req.Run.SessionID.String()}
				req := &model.LLMRequest{}
				if err = tool.ProcessRequest(ctx, req); err != nil {
					t.Fatal(err)
				}
				if req.Tools[botProposalToolName] != tool {
					t.Fatal("unguarded dispatch")
				}
				valid := map[string]any{"payload": map[string]any{"operation": "responsibility", "reason": "review", "responsibility": "new"}}
				bad := map[string]any{"payload": map[string]any{"operation": "responsibility", "reason": "review", "responsibility": "new", "actor_id": "forged"}}
				if _, err = tool.Run(ctx, bad); err == nil || repo.calls != 0 {
					t.Fatal("unknown payload accepted")
				}
				_, _ = bot.TakeToolOutcome(ledgerCtx, ctx.FunctionCallID())
				for i := 0; i < 3; i++ {
					out, e := tool.Run(ctx, valid)
					outcome, ok := bot.TakeToolOutcome(ledgerCtx, ctx.FunctionCallID())
					if !ok || !outcome.Verified || len(outcome.Changed) != 1 || outcome.Changed[0].Summary != "Saved control proposal for review; not applied" {
						t.Fatalf("capture evidence: %+v", outcome)
					}
					if e != nil || out["proposal_id"] == nil || out["payload"] != nil {
						t.Fatalf("reference: %+v %v", out, e)
					}
				}
				if _, err = tool.Run(ctx, valid); err == nil || repo.calls != 3 {
					t.Fatal("proposal spam not capped")
				}
				if repo.actor != f.req.Bot.OwnerID.String() || repo.request.Lease != f.req.Run.Lease || time.Until(repo.request.ExpiresAt) < 23*time.Hour {
					t.Fatal("unbound provenance or expiry")
				}
				tool.attempts = 0 // isolate live-lease refusal from the already exhausted cap
				f.req.Run.Lease.Fence++
				if _, err = tool.Run(ctx, valid); err == nil || repo.calls != 3 {
					t.Fatal("stale lease accepted")
				}
			})
		}
	}
}
