package daemon

import (
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"reflect"
	"testing"
)

func TestRestrictBotRoutinePolicy(t *testing.T) {
	forget := entstore.RoutineControlResource(store.BotControlMemoryForget)
	remember := entstore.RoutineControlResource(store.BotControlMemoryRemember)
	baseline := store.BotMandatePolicy{Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{"propose_bot_control", forget, remember}}}}
	p, err := restrictBotRoutinePolicy(baseline, config.BotRoutinePolicyConfig{})
	if err != nil || !reflect.DeepEqual(p, baseline) {
		t.Fatal("defaults changed", err)
	}
	restriction := config.BotRoutinePolicyConfig{RequireApproval: []config.BotRoutineControl{config.BotRoutineMemoryForget}}
	p, err = restrictBotRoutinePolicy(baseline, restriction)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Constraints[0].Resources, []string{"propose_bot_control", remember}) {
		t.Fatal(p)
	}
	want := []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{forget}}}
	if !reflect.DeepEqual(p.ExceptionConstraints, want) {
		t.Fatal(p.ExceptionConstraints)
	}
	if len(baseline.Constraints[0].Resources) != 3 || baseline.Constraints[0].Resources[1] != forget {
		t.Fatal("mutated caller baseline")
	}
	p, err = restrictBotRoutinePolicy(store.BotMandatePolicy{}, restriction)
	if err != nil || len(p.Constraints) != 0 || len(p.ExceptionConstraints) != 0 {
		t.Fatal("restriction granted missing authority", p, err)
	}
	_, err = restrictBotRoutinePolicy(baseline, config.BotRoutinePolicyConfig{RequireApproval: []config.BotRoutineControl{"shell_command"}})
	if err == nil {
		t.Fatal("accepted extra capability")
	}
}
