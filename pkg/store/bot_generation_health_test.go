package store

import (
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
)

func TestBotBudgetGenerationHealthCloneAndJSON(t *testing.T) {
	spec := BudgetSpec{MaxRuntimeSeconds: 300, RuntimePolicy: execution.NewBotProgressRuntimePolicy()}
	copy := spec.Clone()
	copy.RuntimePolicy.GenerationHealth.IdleTimeoutSeconds = 1
	if spec.RuntimePolicy.GenerationHealth.IdleTimeoutSeconds != 120 {
		t.Fatal("budget clone aliases health policy")
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var restored BudgetSpec
	if err := json.Unmarshal(raw, &restored); err != nil || restored.RuntimePolicy.GenerationHealth == nil || restored.RuntimePolicy.Validate() != nil {
		t.Fatalf("health policy roundtrip: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"max_runtime_seconds":300}`), &restored); err != nil {
		t.Fatal(err)
	}
	var old BudgetSpec
	if err := json.Unmarshal([]byte(`{"max_runtime_seconds":300}`), &old); err != nil || old.RuntimePolicy != nil {
		t.Fatal("old budget silently upgraded")
	}
}
