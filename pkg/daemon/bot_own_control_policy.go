package daemon

import (
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
)

// addBotOwnControlBaseline is composition-only authority for canonical owner
// turns. The operation runtime still checks direct-owner provenance and exact
// grants; these resources grant no external, budget, or configuration access.
func addBotOwnControlBaseline(p store.BotMandatePolicy) store.BotMandatePolicy {
	resources := []string{"ask_user", "propose_bot_control", "schedule_job", "update_scheduled_job", "remove_scheduled_job", "list_scheduled_jobs"}
	for _, op := range []store.BotControlOperation{store.BotControlPriorityPause, store.BotControlResumeTasks, store.BotControlTaskOnceOperation, store.BotControlResponsibility, store.BotControlPersona, store.BotControlGoalCreate, store.BotControlGoalEdit, store.BotControlMemoryRemember, store.BotControlMemoryCorrect, store.BotControlMemoryForget} {
		resources = append(resources, entstore.RoutineControlResource(op))
	}
	p.Constraints = append(append([]execution.BotActionResourceConstraint(nil), p.Constraints...), execution.BotActionResourceConstraint{Action: execution.CapabilityToolExecute, Resources: resources})
	return p
}
