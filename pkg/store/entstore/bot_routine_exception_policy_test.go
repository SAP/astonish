package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func routineExceptionTestPolicy(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
	p, e := authorizedSchedulePolicy(ctx, ref)
	p.ExceptionConstraints = []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{RoutineControlResource(store.BotControlResponsibility)}}}
	return p, e
}
