package bot

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotGenerationHealthBudgetSnapshot(t *testing.T) {
	spec := store.BudgetSpec{MaxRuntimeSeconds: 300, RuntimePolicy: execution.NewBotProgressRuntimePolicy()}
	b, err := NewRunBudget(spec, time.Now().Add(-6*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if b.ModelCallTimeout() != 0 || b.ToolCallTimeout() != 120*time.Second || !b.Deadline().IsZero() || b.Check(context.Background()) != nil {
		t.Fatal("health mode age-kills healthy work or changes tool limits")
	}
	spec.RuntimePolicy.GenerationHealth.IdleTimeoutSeconds = 1
	snapshot := b.GenerationHealth()
	if snapshot == nil || snapshot.IdleTimeoutSeconds != 120 {
		t.Fatal("budget did not snapshot health")
	}
	snapshot.CheckIntervalSeconds = 1
	if b.GenerationHealth().CheckIntervalSeconds != 10 {
		t.Fatal("budget health aliases consumer")
	}
	spec.RuntimePolicy.GenerationHealth = nil
	spec.RuntimePolicy.ModelCallTimeoutSeconds = 120
	old, err := NewRunBudget(spec, time.Now())
	if err != nil || old.GenerationHealth() != nil || old.ModelCallTimeout() != 120*time.Second {
		t.Fatalf("saved progress timeout lost: %v", err)
	}
}
