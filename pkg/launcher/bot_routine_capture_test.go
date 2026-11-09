package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
)

type routineCaptureRuntime struct {
	store.BotAuthorizedOperationStore
	op *store.BotOperation
}

func (r *routineCaptureRuntime) EnqueueAuthorizedOperation(_ context.Context, q store.BotAuthorizedOperationRequest) (*store.BotOperation, error) {
	data, _ := json.Marshal(q.Payload)
	digest := sha256.Sum256(data)
	r.op = &store.BotOperation{ID: uuid.New(), MandateRef: q.MandateRef, IdempotencyKey: q.IdempotencyKey, Payload: q.Payload, Status: store.BotOperationPending, Decision: store.BotOperationExecute, Revision: 1, Provenance: &store.BotOperationProvenance{SourceRunID: q.Lease.RunID, SourceUserID: q.MandateRef.OwnerID, UserEventID: "user-event", SourceDigest: "source", PayloadDigest: hex.EncodeToString(digest[:]), Purpose: execution.BotMandatePurposeOperation, ExpectedRevision: 1}}
	return r.op, nil
}
func (r *routineCaptureRuntime) ReadAuthorizedOperation(context.Context, store.BotMandateRef, uuid.UUID) (*store.BotOperation, error) {
	c, err := r.op.Clone()
	return &c, err
}

func TestRoutineCaptureReportsPendingNotChanged(t *testing.T) {
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
	runtime := &routineCaptureRuntime{}
	f.services.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
		return runtime, nil
	}
	owner, _ := bot.WithToolOutcomes(store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services))
	binding := &botFactoryBinding{owner: owner, req: f.req, verifyIsolation: func(context.Context) error { return nil }}
	raw, err := newBotProposalTool(binding)
	if err != nil {
		t.Fatal(err)
	}
	ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: owner}, sid: f.req.Run.SessionID.String()}
	args := map[string]any{"payload": map[string]any{"operation": "goal_create", "reason": "Owner requested clarity", "goal": map[string]any{"goal": map[string]any{"title": "Weather clarity", "completion_criteria": "Each update is easy to understand."}}}}
	out, err := raw.(*botProposalTool).Run(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	outcome, ok := bot.TakeToolOutcome(owner, ctx.FunctionCallID())
	if !ok || !outcome.Verified || outcome.RoutineCapture == nil || !outcome.RoutineCapture.ValidFor(id) || len(outcome.Changed) != 0 || !strings.Contains(out["message"].(string), "pending") {
		t.Fatalf("not capture-only: out=%+v outcome=%+v", out, outcome)
	}
	for _, scenario := range []string{"absent", "read-error", "wrong-id", "wrong-run", "wrong-digest", "applied", "missing-provenance"} {
		t.Run(scenario, func(t *testing.T) {
			f.services.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
				return scheduleCaptureReadback{BotAuthorizedOperationStore: runtime, scenario: scenario}, nil
			}
			raw.(*botProposalTool).attempts = 0
			if result, err := raw.(*botProposalTool).Run(ctx, args); err == nil || result != nil {
				t.Fatalf("untrusted read accepted: %+v %v", result, err)
			}
			if _, ok := bot.TakeToolOutcome(owner, ctx.FunctionCallID()); ok {
				t.Fatal("minted receipt on failed read")
			}
		})
	}
}

func TestRoutineGoalSchemaExplainsNesting(t *testing.T) {
	for _, schema := range []*jsonschema.Schema{botProposalInputSchema(), botMandatedProposalInputSchema()} {
		if !strings.Contains(schema.Properties["payload"].Properties["goal"].Description, "payload.goal.goal") {
			t.Fatal("nested wire shape not explained")
		}
	}
}
