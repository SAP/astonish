package launcher

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type pausedProposalRuntime struct {
	store.BotAuthorizedOperationStore
	op      *store.BotOperation
	request store.BotAuthorizedOperationRequest
}

func (r *pausedProposalRuntime) EnqueueAuthorizedOperation(_ context.Context, q store.BotAuthorizedOperationRequest) (*store.BotOperation, error) {
	r.request = q
	return r.op, nil
}
func (r *pausedProposalRuntime) ReadAuthorizedOperation(context.Context, store.BotMandateRef, uuid.UUID) (*store.BotOperation, error) {
	return r.op, nil
}

func TestBotPriorityPauseToolReceiptDoesNotClaimConversationFenced(t *testing.T) {
	f := newBotFactoryFixture(t)
	repo := &proposalTestRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
	f.services.Bots, f.services.PersonalBots = repo, repo
	id := f.req.Policy.Identity()
	mandate := &factoryMandateRuntime{saved: execution.BotMandate{Ref: execution.BotMandateRef{ID: uuid.New(), BotID: id.BotID, OwnerID: id.ActorID, OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, Revision: 1}, Purpose: execution.BotMandatePurposeOperation, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig()), ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{"propose_bot_control"}}}}}
	auth, err := execution.VerifyBotMandateAuthorization(context.Background(), mandate, mandate.saved.Ref, execution.BotMandatePurposeOperation)
	if err != nil {
		t.Fatal(err)
	}
	f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: mandate, Identity: id}
	runtime := &pausedProposalRuntime{op: &store.BotOperation{ID: uuid.New(), Status: store.BotOperationApplied, Decision: store.BotOperationExecute, Provenance: &store.BotOperationProvenance{PayloadDigest: "digest"}}}
	f.services.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
		return runtime, nil
	}
	owner := store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services)
	binding := &botFactoryBinding{owner: owner, req: f.req, verifyIsolation: func(context.Context) error { return nil }}
	raw, err := newBotProposalTool(binding)
	if err != nil {
		t.Fatal(err)
	}
	ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: owner}, sid: f.req.Run.SessionID.String()}
	result, err := raw.(*botProposalTool).capture(ctx, botProposalArgs{Payload: store.BotControlPayload{Operation: store.BotControlPriorityPause, Reason: "Already paused"}})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := result["message"].(string)
	if strings.Contains(text, "source lease") || !strings.Contains(text, "leaves explicit conversations running") || !strings.Contains(text, "schedules are disabled") {
		t.Fatalf("misleading receipt: %s", text)
	}
	if runtime.request.Lease != f.req.Run.Lease || runtime.request.IdempotencyKey != ctx.FunctionCallID() {
		t.Fatal("tool provenance changed")
	}
}
