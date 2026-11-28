package agent

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type botAuxProbe struct{ calls int }

func (*botAuxProbe) Name() string { return "probe" }
func (p *botAuxProbe) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		p.calls++
		yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"should_retry":false,"reason":"stop"}`, genai.RoleModel), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 2, CandidatesTokenCount: 3}}, nil)
	}
}
func TestBotAuxiliaryFailClosedAndMetered(t *testing.T) {
	identity := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: uuid.NewString(), OrgSlug: "org", TeamSlug: "team"}
	ctx := execution.WithBotRunIdentity(context.Background(), identity)
	unmetered := &botAuxProbe{}
	ctx = WithLLM(ctx, unmetered)
	distiller := NewFlowDistiller(unmetered, nil, nil, nil, nil, nil)
	if _, err := distiller.LLM(ctx, "test"); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("unbound distill: %v", err)
	}
	if _, err := NewErrorRecoveryNode(unmetered, false).Decide(ctx, ErrorContext{}); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("unbound recovery: %v", err)
	}
	for _, err := range (&PlatformReflector{LLM: unmetered}).effectiveLLM(ctx).GenerateContent(ctx, &model.LLMRequest{}, false) {
		if !errors.Is(err, store.ErrBotUnsupported) {
			t.Fatal(err)
		}
	}
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: identity, Level: execution.AutonomySupervised})
	if err != nil {
		t.Fatal(err)
	}
	policyOnly := execution.WithRunPolicy(WithLLM(context.Background(), unmetered), policy)
	if _, err := distiller.LLM(policyOnly, "policy without identity"); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("policy-only auxiliary escaped binding: %v", err)
	}
	if unmetered.calls != 0 {
		t.Fatal("unmetered provider called")
	}
	cap := int64(100)
	budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &cap}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	probe := &botAuxProbe{}
	metered, err := provider.NewBudgetLLM(probe, budget, provider.BudgetLLMOptions{MaxOutputTokens: 10, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 2, nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithBotAuxiliaryLLM(ctx, metered)
	if _, err := distiller.LLM(ctx, "distill"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewErrorRecoveryNode(unmetered, false).Decide(ctx, ErrorContext{}); err != nil {
		t.Fatal(err)
	}
	for _, err := range (&PlatformReflector{LLM: unmetered}).effectiveLLM(ctx).GenerateContent(ctx, &model.LLMRequest{}, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	usage := budget.Usage()
	if probe.calls != 3 || usage.InputTokens != 6 || usage.OutputTokens != 9 || unmetered.calls != 0 {
		t.Fatalf("calls=%d usage=%+v unmetered=%d", probe.calls, usage, unmetered.calls)
	}
	identity.RunID = uuid.New()
	wrong := execution.WithBotRunIdentity(ctx, identity)
	if _, err := distiller.LLM(wrong, "wrong run"); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("cross-run capability: %v", err)
	}
	zero := int64(0)
	empty, _ := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &zero}, time.Now())
	denied, _ := provider.NewBudgetLLM(probe, empty, provider.BudgetLLMOptions{MaxOutputTokens: 10})
	if _, err := distiller.LLM(WithBotAuxiliaryLLM(ctx, denied), "exhausted"); !errors.Is(err, bot.ErrBudgetExceeded) {
		t.Fatalf("budget denial: %v", err)
	}
	if probe.calls != 3 {
		t.Fatal("dispatch after exhaustion")
	}
}
func TestBotAuxiliaryOrdinaryChatUnchanged(t *testing.T) {
	fallback, override := &botAuxProbe{}, &botAuxProbe{}
	if auxiliaryLLM(context.Background(), fallback) != fallback {
		t.Fatal("fallback changed")
	}
	if auxiliaryLLM(WithLLM(context.Background(), override), fallback) != override {
		t.Fatal("override changed")
	}
}
