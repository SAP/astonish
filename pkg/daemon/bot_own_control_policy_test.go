package daemon

import (
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"testing"
)

func TestBotOwnControlBaselineRestriction(t *testing.T) {
	base := addBotOwnControlBaseline(store.BotMandatePolicy{})
	has := func(cs []execution.BotActionResourceConstraint, resource string) bool {
		for _, c := range cs {
			for _, r := range c.Resources {
				if c.Action == execution.CapabilityToolExecute && r == resource {
					return true
				}
			}
		}
		return false
	}
	for _, r := range []string{"schedule_job", "ask_user", entstore.RoutineControlResource(store.BotControlMemoryRemember), entstore.RoutineControlResource(store.BotControlMemoryForget)} {
		if !has(base.Constraints, r) {
			t.Fatal("missing", r)
		}
	}
	for _, r := range []string{"*", "shell_command", "http_request", "bot_control:configuration", "bot_control:resume", "bot_control:budget"} {
		if has(base.Constraints, r) {
			t.Fatal("unexpected grant", r)
		}
	}
	restricted, err := restrictBotRoutinePolicy(base, config.BotRoutinePolicyConfig{RequireApproval: []config.BotRoutineControl{config.BotRoutineMemoryForget}})
	if err != nil {
		t.Fatal(err)
	}
	forget := entstore.RoutineControlResource(store.BotControlMemoryForget)
	if has(restricted.Constraints, forget) || !has(restricted.ExceptionConstraints, forget) || !has(base.Constraints, forget) {
		t.Fatal("restriction did not partition immutable baseline")
	}
	if !has(restricted.Constraints, "schedule_job") || !has(restricted.Constraints, entstore.RoutineControlResource(store.BotControlMemoryRemember)) {
		t.Fatal("restriction exceeded forget")
	}
}
