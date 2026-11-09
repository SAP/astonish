package launcher

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// resumeMandateRuntime persists the operation built from the actual request;
// readback returns a separate copy rather than an unrelated pending fixture.
type resumeMandateRuntime struct {
	routineCaptureRuntime
	request store.BotAuthorizedOperationRequest
	blocked bool
	reads   int
}

func (r *resumeMandateRuntime) EnqueueAuthorizedOperation(ctx context.Context, q store.BotAuthorizedOperationRequest) (*store.BotOperation, error) {
	r.request = q
	if _, err := r.routineCaptureRuntime.EnqueueAuthorizedOperation(ctx, q); err != nil {
		return nil, err
	}
	if r.blocked {
		r.op.Status, r.op.Decision = store.BotOperationBlocked, store.BotOperationRequireAuthorization
	}
	captured, err := r.op.Clone()
	return &captured, err
}

func (r *resumeMandateRuntime) ReadAuthorizedOperation(ctx context.Context, ref store.BotMandateRef, id uuid.UUID) (*store.BotOperation, error) {
	if r.op == nil || r.op.ID != id || r.op.MandateRef != ref {
		return nil, store.ErrBotConflict
	}
	r.reads++
	return r.routineCaptureRuntime.ReadAuthorizedOperation(ctx, ref, id)
}

func TestMandatedResumeCannotFallBackToLegacyProposal(t *testing.T) {
	for _, mandated := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "mandated"}[mandated], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			repo := &proposalTestRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
			f.services.Bots, f.services.PersonalBots = repo, repo
			runtime := &resumeMandateRuntime{}
			if mandated {
				id := f.req.Policy.Identity()
				mandate := &factoryMandateRuntime{saved: execution.BotMandate{Ref: execution.BotMandateRef{ID: uuid.New(), BotID: id.BotID, OwnerID: id.ActorID, OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, Revision: 1}, Purpose: execution.BotMandatePurposeOperation, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig()), ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{"propose_bot_control", "bot_control:resume_tasks"}}}}}
				auth, err := execution.VerifyBotMandateAuthorization(context.Background(), mandate, mandate.saved.Ref, execution.BotMandatePurposeOperation)
				if err != nil {
					t.Fatal(err)
				}
				f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: mandate, Identity: id}
			}
			f.services.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
				return runtime, nil
			}
			owner := store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services)
			binding := &botFactoryBinding{owner: owner, req: f.req, verifyIsolation: func(context.Context) error { return nil }}
			raw, err := newBotProposalTool(binding)
			if err != nil {
				t.Fatal(err)
			}
			tool := raw.(*botProposalTool)
			ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: owner}, sid: f.req.Run.SessionID.String()}
			args := botProposalArgs{Payload: store.BotControlPayload{Operation: store.BotControlResume, Reason: "Owner explicitly requested resuming the recurring Orlando weather updates."}}
			result, err := tool.capture(ctx, args)
			if !mandated {
				if err != nil || result["proposal_id"] == nil || repo.calls != 1 {
					t.Fatalf("legacy review lost: %v %v", result, err)
				}
				return
			}
			if !errors.Is(err, store.ErrBotInvalid) || !strings.Contains(err.Error(), "resume_tasks") || repo.calls != 0 {
				t.Fatalf("broad resume created redundant review: result=%v err=%v proposals=%d", result, err, repo.calls)
			}
			wire := map[string]any{"payload": map[string]any{"operation": string(store.BotControlResume), "reason": "resume weather"}}
			if tool.validateInput(wire) == nil {
				t.Fatal("mandated schema still advertises broad resume")
			}
			target := uuid.New()
			args.Payload.Operation = store.BotControlResumeTasks
			args.Payload.ResumeTargets = &store.BotControlResumeTargets{ScheduleIDs: []uuid.UUID{target}}
			result, err = tool.capture(ctx, args)
			if err != nil || result["operation_id"] == nil || result["proposal_id"] != nil || repo.calls != 0 {
				t.Fatalf("targeted resume not routed: %v %v", result, err)
			}
			if runtime.request.Lease != f.req.Run.Lease || runtime.request.MandateRef != f.req.Mandate.Authorization.Ref() || runtime.request.Payload.ResumeTargets.ScheduleIDs[0] != target {
				t.Fatal("target or source provenance changed")
			}
			if runtime.reads != 1 || result["operation_id"] != runtime.op.ID.String() || result["status"] != store.BotOperationPending || result["decision"] != store.BotOperationExecute || !strings.Contains(result["message"].(string), "pending, not applied") {
				t.Fatalf("targeted resume lost independent pending readback: %v reads=%d", result, runtime.reads)
			}
			runtime.blocked = true
			result, err = tool.capture(ctx, args)
			if err != nil || result["decision"] != store.BotOperationRequireAuthorization || !strings.Contains(result["message"].(string), "bounded owner authorization") {
				t.Fatalf("out-of-ceiling authorization lost: %v %v", result, err)
			}
		})
	}
}
