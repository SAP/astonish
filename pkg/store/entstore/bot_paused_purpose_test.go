package entstore

import (
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"reflect"
	"testing"
)

func TestPausedControlConstraintsExcludeAutonomousTools(t *testing.T) {
	allowed := []string{"propose_bot_control", "ask_user", "list_scheduled_jobs", RoutineControlResource(store.BotControlGoalCreate), RoutineControlResource(store.BotControlResumeTasks)}
	resources := append(append([]string{}, allowed...), "web_search", "web_fetch", "shell_command", "http_request", "read_file", "schedule_job", "update_scheduled_job", "remove_scheduled_job", "list_scheduled_jobs_future", "list_scheduled_jobs:*", RoutineControlResource(store.BotControlTaskOnceOperation))
	input := []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: resources}, {Action: execution.CapabilityChat, Resources: []string{"other"}}}
	got := pausedControlConstraints(input)
	want := []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: allowed}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paused constraints = %+v; want %+v", got, want)
	}
	if len(input[0].Resources) != len(resources) {
		t.Fatal("mutated input policy")
	}
}
