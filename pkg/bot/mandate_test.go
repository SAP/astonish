package bot

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

type presentationBudgetStoreFake struct {
	store.BotEmployeePresentationStore
	p        store.BotEmployeePresentation
	reserved store.BotEmployeePresentationModelReservation
	settled  store.BotEmployeePresentationModelSettlement
	deny     bool
}

func (f *presentationBudgetStoreFake) ReadEmployeePresentation(context.Context, store.BotEmployeePresentationRequest) (*store.BotEmployeePresentation, error) {
	return &f.p, nil
}
func (f *presentationBudgetStoreFake) ReserveEmployeePresentationModel(_ context.Context, q store.BotEmployeePresentationModelReservation) (*store.BotEmployeePresentation, error) {
	f.reserved = q
	if f.deny {
		return nil, ErrBudgetExceeded
	}
	return &f.p, nil
}
func (f *presentationBudgetStoreFake) SettleEmployeePresentationModel(ctx context.Context, q store.BotEmployeePresentationModelSettlement) (*store.BotEmployeePresentation, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	f.settled = q
	return &f.p, nil
}
func TestPresentationBudgetOriginalClaimAccounting(t *testing.T) {
	q := store.BotEmployeePresentationRequest{BotID: uuid.New(), RunID: uuid.New(), ClaimToken: "claim"}
	f := &presentationBudgetStoreFake{p: store.BotEmployeePresentation{BotID: q.BotID, RunID: q.RunID, ClaimToken: q.ClaimToken, State: store.BotEmployeePresentationClaimed, Deadline: time.Now().Add(time.Minute), AuthorityExpiresAt: time.Now().Add(time.Minute)}}
	b := &PresentationBudget{Store: f, Request: q, DispatchToken: "original-dispatch"}
	if !errors.Is(b.ReserveTool(context.Background(), "anything"), store.ErrBotForbidden) {
		t.Fatal("tools allowed")
	}
	cost := int64(11)
	r, err := b.ReserveModel(context.Background(), execution.BudgetReservation{InputTokens: 3, OutputTokens: 7, CostMicros: &cost})
	if err != nil || r.ID == uuid.Nil || f.reserved.RunID != q.RunID || f.reserved.DispatchToken != b.DispatchToken || f.reserved.Reserved.InputTokens != 3 {
		t.Fatalf("reservation=%+v err=%v", f.reserved, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.SettleModel(ctx, r.ID, execution.BudgetUsage{InputTokens: 2, OutputTokens: 4, CostMicros: &cost}); err != nil || f.settled.TurnID != r.ID.String() || f.settled.Usage.OutputTokens != 4 {
		t.Fatalf("settlement=%+v err=%v", f.settled, err)
	}
	f.deny = true
	if _, err := b.ReserveModel(context.Background(), execution.BudgetReservation{}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal("store denial bypassed")
	}
	f.p.Deadline = time.Now().Add(-time.Second)
	if err := b.Check(context.Background()); err != nil {
		t.Fatal("historical runtime cap blocked presentation", err)
	}
	f.p.AuthorityExpiresAt = time.Now().Add(-time.Second)
	if b.Check(context.Background()) == nil {
		t.Fatal("expired claim allowed")
	}
}

type mandateRuntimeFake struct {
	store.BotMandateRuntime
	m                              execution.BotMandate
	revoked                        bool
	tools, tokens, spend, releases int64
}

func (f *mandateRuntimeFake) VerifyBotMandate(context.Context, execution.BotMandateRef, execution.BotMandatePurpose) (execution.BotMandate, error) {
	if f.revoked {
		return execution.BotMandate{}, store.ErrBotForbidden
	}
	return f.m, nil
}
func (f *mandateRuntimeFake) ConsumeBotMandateQuota(_ context.Context, _ store.BotMandateRef, _ uuid.UUID, tools, tokens, spend int64) error {
	if f.revoked {
		return store.ErrBotForbidden
	}
	f.tools += tools
	f.tokens += tokens
	f.spend += spend
	return nil
}
func (f *mandateRuntimeFake) ReserveBotMandateModel(ctx context.Context, ref store.BotMandateRef, run, id uuid.UUID, tokens int64) error {
	return f.ConsumeBotMandateQuota(ctx, ref, run, 0, tokens, 0)
}
func (f *mandateRuntimeFake) SettleBotMandateModel(context.Context, store.BotMandateRef, uuid.UUID, uuid.UUID, int64) error {
	return nil
}
func (f *mandateRuntimeFake) ReleaseBotMandateRun(context.Context, store.BotMandateRef, uuid.UUID) error {
	f.releases++
	return nil
}
func mandateTestExecution(t *testing.T) (*MandateExecution, *mandateRuntimeFake) {
	t.Helper()
	ref := execution.BotMandateRef{ID: uuid.New(), BotID: uuid.New(), OwnerID: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Revision: 1}
	f := &mandateRuntimeFake{m: execution.BotMandate{Ref: ref, Purpose: execution.BotMandatePurposeTask, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{string(execution.CapabilityToolExecute)}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{"filter_json"}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig())}}
	a, err := execution.VerifyBotMandateAuthorization(context.Background(), f, ref, execution.BotMandatePurposeTask)
	if err != nil {
		t.Fatal(err)
	}
	return &MandateExecution{Authorization: a, Runtime: f, Identity: execution.BotRunIdentity{BotID: ref.BotID, RunID: uuid.New(), Version: 1, ActorID: ref.OwnerID, OrgSlug: ref.OrgSlug, TeamSlug: ref.TeamSlug}}, f
}
func TestMandateExecutionFreshExactScope(t *testing.T) {
	m, f := mandateTestExecution(t)
	ctx := context.Background()
	a := execution.ActionDescriptor{Tool: "filter_json", Effect: execution.EffectRead, Arguments: []byte(`{}`), ConstraintsVerified: true}
	grant, err := m.AuthorizeAction(ctx, a)
	if err != nil || grant.Identity != m.Identity {
		t.Fatalf("grant: %+v %v", grant, err)
	}
	a.Tool = "shell_command"
	if _, err = m.AuthorizeAction(ctx, a); err == nil {
		t.Fatal("scope widened")
	}
	a.Tool = "filter_json"
	a.Effect = execution.EffectSend
	if _, err = m.AuthorizeAction(ctx, a); err == nil {
		t.Fatal("send authorized")
	}
	f.revoked = true
	if err = m.Check(ctx); err == nil {
		t.Fatal("revocation ignored")
	}
	p := m.Authorization.Principal(execution.Surface("daemon"))
	if p.Authentication != execution.AuthMethodBotMandate || len(p.Roles) != 0 {
		t.Fatal("owner authority leaked")
	}
}
func TestMandateBudgetPrechargeAndReleaseOnce(t *testing.T) {
	m, f := mandateTestExecution(t)
	local, err := NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b := &MandateBudget{SharedRunBudget: local, Mandate: m}
	ctx := context.Background()
	if err = b.ReserveTool(ctx, "filter_json"); err != nil || f.tools != 1 {
		t.Fatalf("precharge: %v %d", err, f.tools)
	}
	if _, err = b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 1, OutputTokens: 2}); err != nil || f.tokens != 3 || f.spend != 0 {
		t.Fatalf("token-budgeted model blocked by external spend cap: %v", err)
	}
	if b.Usage().CostMicros != nil {
		t.Fatal("unknown model cost reported as zero")
	}
	cap := int64(100)
	capped, _ := NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxModelCostMicros: &cap}, time.Now())
	cappedBudget := &MandateBudget{SharedRunBudget: capped, Mandate: m}
	if _, err = cappedBudget.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 1, OutputTokens: 2}); !errors.Is(err, ErrBudgetUnknownCost) {
		t.Fatalf("monetary ceiling allowed unknown cost: %v", err)
	}
	f.revoked = true
	if err = b.ReserveTool(ctx, "filter_json"); err == nil || f.tools != 1 {
		t.Fatal("revoked quota charged")
	}
	if err = b.Release(ctx); err != nil {
		t.Fatal(err)
	}
	_ = b.Release(ctx)
	if f.releases != 1 {
		t.Fatal("release duplicated")
	}
}
