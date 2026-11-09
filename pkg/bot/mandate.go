package bot

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"math"
	"sync"
	"time"
)

// MandateExecution is constructed by trusted composition for one persisted run pin.
// Every use rechecks the durable revision, membership and current operator policy.
type MandateExecution struct {
	Authorization execution.VerifiedBotMandateAuthorization
	Runtime       store.BotMandateRuntime
	Identity      execution.BotRunIdentity
}

func (m *MandateExecution) Check(ctx context.Context) error {
	if m == nil || m.Runtime == nil {
		return store.ErrBotForbidden
	}
	ref := m.Authorization.Ref()
	if ref.BotID != m.Identity.BotID || ref.OwnerID != m.Identity.ActorID || ref.OrgSlug != m.Identity.OrgSlug || ref.TeamSlug != m.Identity.TeamSlug {
		return store.ErrBotForbidden
	}
	_, err := execution.VerifyBotMandateAuthorization(ctx, m.Runtime, ref, m.Authorization.Snapshot().Purpose)
	return err
}
func (m *MandateExecution) AuthorizeAction(ctx context.Context, a execution.ActionDescriptor) (execution.ScopedGrant, error) {
	if err := m.Check(ctx); err != nil {
		return execution.ScopedGrant{}, err
	}
	// Resource-independent, mechanically verified reads/reversible writes only.
	// Consequential or unknown effects require a separate exact approval adapter.
	if !a.ConstraintsVerified || (a.Effect != execution.EffectRead && a.Effect != execution.EffectReversibleWrite) || a.SpendMinor != 0 {
		return execution.ScopedGrant{}, store.ErrBotForbidden
	}
	if err := m.Authorization.Authorize(execution.CapabilityToolExecute, a.Tool); err != nil {
		return execution.ScopedGrant{}, err
	}
	digest, err := a.Digest()
	if err != nil {
		return execution.ScopedGrant{}, err
	}
	return execution.ScopedGrant{ID: m.Authorization.Ref().ID, Identity: m.Identity, ActionDigest: digest, ExpiresAt: m.Authorization.Snapshot().ExpiresAt}, nil
}

// MandateBudget reserves durable capacity before dispatch and reconciles only
// final model usage. Parent and children share it; actual usage is never refunded.
type MandateBudget struct {
	*SharedRunBudget
	modelMu      sync.Mutex
	modelSettled map[uuid.UUID]bool
	Mandate      *MandateExecution
	once         sync.Once
	releaseErr   error
	// OnReleaseFailure queues only the exact trusted reservation for retry.
	OnReleaseFailure func(*MandateExecution)
}

func (b *MandateBudget) Check(ctx context.Context) error {
	if err := b.Mandate.Check(ctx); err != nil {
		return err
	}
	return b.SharedRunBudget.Check(ctx)
}
func (b *MandateBudget) ReserveTool(ctx context.Context, name string) error {
	if err := b.Check(ctx); err != nil {
		return err
	}
	if err := b.Mandate.Authorization.Authorize(execution.CapabilityToolExecute, name); err != nil {
		return err
	}
	if err := b.Mandate.Runtime.ConsumeBotMandateQuota(ctx, b.Mandate.Authorization.Ref(), b.Mandate.Identity.RunID, 1, 0, 0); err != nil {
		return err
	}
	return b.SharedRunBudget.ReserveTool(ctx, name)
}
func (b *MandateBudget) ReserveModel(ctx context.Context, r execution.BudgetReservation) (execution.BudgetReservation, error) {
	if err := b.Check(ctx); err != nil {
		return r, err
	}
	if r.InputTokens < 0 || r.OutputTokens < 0 || r.InputTokens > math.MaxInt64-r.OutputTokens {
		return r, store.ErrBotInvalid
	}
	// Model operating cost is not external action spend. The local model meter
	// enforces an explicitly requested monetary ceiling and preserves unknown cost;
	// the durable mandate ledger enforces finite per-run and aggregate tokens.
	reserved, err := b.SharedRunBudget.ReserveModel(ctx, r)
	if err != nil {
		return r, err
	}
	if err := b.Mandate.Runtime.ReserveBotMandateModel(ctx, b.Mandate.Authorization.Ref(), b.Mandate.Identity.RunID, reserved.ID, reserved.InputTokens+reserved.OutputTokens); err != nil {
		zero := int64(0)
		_ = b.SharedRunBudget.SettleModel(context.WithoutCancel(ctx), reserved.ID, execution.BudgetUsage{CostMicros: &zero})
		return r, err
	}
	return reserved, nil
}
func (b *MandateBudget) SettleModel(ctx context.Context, id uuid.UUID, usage execution.BudgetUsage) error {
	b.modelMu.Lock()
	defer b.modelMu.Unlock()
	if b.modelSettled[id] {
		return nil
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.InputTokens > math.MaxInt64-usage.OutputTokens || usage.CostMicros != nil && *usage.CostMicros < 0 {
		return ErrBudgetReservation
	}
	if err := b.Mandate.Runtime.SettleBotMandateModel(context.WithoutCancel(ctx), b.Mandate.Authorization.Ref(), b.Mandate.Identity.RunID, id, usage.InputTokens+usage.OutputTokens); err != nil {
		return err
	}
	if b.modelSettled == nil {
		b.modelSettled = map[uuid.UUID]bool{}
	}
	b.modelSettled[id] = true
	return b.SharedRunBudget.SettleModel(ctx, id, usage)
}
func (b *MandateBudget) Release(ctx context.Context) error {
	b.once.Do(func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		b.releaseErr = b.Mandate.Runtime.ReleaseBotMandateRun(c, b.Mandate.Authorization.Ref(), b.Mandate.Identity.RunID)
		if b.releaseErr != nil && b.OnReleaseFailure != nil {
			b.OnReleaseFailure(b.Mandate)
		}
	})
	return b.releaseErr
}

var _ execution.RunBudget = (*MandateBudget)(nil)
