package bot

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

func TestKnowledgeExtractorRestoredAccountingIgnoresHistoricCaps(t *testing.T) {
	e, _, m := extractionRunner(t, `{"schema_version":1,"kind":"no_change","reason":"already retained","proposals":[]}`)
	zero := int64(0)
	usage := store.BotUsage{InputTokens: 100000, OutputTokens: 200000, ToolCalls: 1000, RuntimeNanos: int64(24 * time.Hour)}
	budget, err := RestoreRunAccounting(store.BudgetSpec{MaxRuntimeSeconds: 0, MaxTokens: &zero, MaxToolCalls: &zero, MaxModelCostMicros: &zero}, time.Now(), usage)
	if err != nil {
		t.Fatal(err)
	}
	e.budget = budget // Unknown historic and estimated price must not deny generation.
	result, err := e.Extract(context.Background(), extractionInput())
	if err != nil || result.State != store.BotKnowledgeExtractionNoChange || m.calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, m.calls)
	}
	actual := budget.Usage()
	if actual.InputTokens != usage.InputTokens+10 || actual.OutputTokens != usage.OutputTokens+20 || actual.ToolCalls != usage.ToolCalls || actual.RuntimeNanos < usage.RuntimeNanos || actual.CostMicros != nil || !budget.Deadline().IsZero() {
		t.Fatalf("historic accounting lost or execution capped: %+v", actual)
	}
}
