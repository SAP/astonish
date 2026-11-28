package daemon

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

type cleanupMandateRuntime struct {
	store.BotMandateRuntime
	ref   execution.BotMandateRef
	run   uuid.UUID
	fail  bool
	calls int
}

func (r *cleanupMandateRuntime) VerifyBotMandate(context.Context, execution.BotMandateRef, execution.BotMandatePurpose) (execution.BotMandate, error) {
	return execution.BotMandate{Ref: r.ref, Purpose: execution.BotMandatePurposeTask, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"chat"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{r.ref.BotID.String()}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig())}, nil
}
func (r *cleanupMandateRuntime) ReleaseBotMandateRun(ctx context.Context, ref store.BotMandateRef, run uuid.UUID) error {
	if ref != r.ref || run != r.run {
		return store.ErrBotForbidden
	}
	if _, ok := execution.PrincipalFromContext(ctx); ok {
		return errors.New("cleanup inherited user authority")
	}
	r.calls++
	if r.fail {
		return store.ErrBotConflict
	}
	return nil
}
func TestBotMandateCleanupRetry(t *testing.T) {
	rt := &cleanupMandateRuntime{ref: execution.BotMandateRef{ID: uuid.New(), BotID: uuid.New(), OwnerID: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Revision: 1}, run: uuid.New(), fail: true}
	auth, err := execution.VerifyBotMandateAuthorization(context.Background(), rt, rt.ref, execution.BotMandatePurposeTask)
	if err != nil {
		t.Fatal(err)
	}
	m := &bot.MandateExecution{Runtime: rt, Authorization: auth, Identity: execution.BotRunIdentity{BotID: rt.ref.BotID, RunID: rt.run, ActorID: rt.ref.OwnerID, OrgSlug: "org", TeamSlug: "team", Version: 1}}
	w := &BotWorker{}
	b := &bot.MandateBudget{Mandate: m, OnReleaseFailure: w.queueMandateCleanup}
	if !errors.Is(b.Release(context.Background()), store.ErrBotConflict) || len(w.mandateCleanup) != 1 {
		t.Fatal("failed release not surfaced/queued")
	}
	if !errors.Is(w.retryMandateCleanup(context.Background()), store.ErrBotConflict) || len(w.mandateCleanup) != 1 {
		t.Fatal("failed retry discarded")
	}
	rt.fail = false
	if err = w.retryMandateCleanup(context.Background()); err != nil || len(w.mandateCleanup) != 0 {
		t.Fatalf("retry: %v", err)
	}
	if err = w.retryMandateCleanup(context.Background()); err != nil || rt.calls != 3 {
		t.Fatal("successful cleanup repeated")
	}
}
