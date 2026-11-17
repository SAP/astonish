package bot

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

type progressExtractionBound struct {
	inner KnowledgeExtractionModel
	t     *testing.T
}

func (m progressExtractionBound) GenerateKnowledgeExtraction(ctx context.Context, r KnowledgeExtractionModelRequest) (KnowledgeExtractionModelResponse, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Second {
		m.t.Fatal("extraction ignored the narrower model operation timeout")
	}
	return m.inner.GenerateKnowledgeExtraction(ctx, r)
}

func TestBotProgressBudgetKnowledgeExtractionZeroDeadline(t *testing.T) {
	e, _, m := extractionRunner(t, `{"schema_version":1,"kind":"no_change","reason":"already retained","proposals":[]}`)
	policy := execution.NewBotProgressRuntimePolicy()
	policy.GenerationHealth = nil // Explicit historical elapsed-time policy.
	policy.ModelCallTimeoutSeconds = 1
	budget, err := NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300, RuntimePolicy: policy}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.budget = budget
	e.model = progressExtractionBound{inner: m, t: t}
	result, err := e.Extract(context.Background(), extractionInput())
	if err != nil || result == nil || result.State != store.BotKnowledgeExtractionNoChange || m.calls != 1 {
		t.Fatalf("zero hard deadline must permit bounded extraction: result=%+v calls=%d err=%v", result, m.calls, err)
	}
	if !budget.Deadline().IsZero() {
		t.Fatal("extraction installed a whole-task deadline")
	}
}
