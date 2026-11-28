package daemon

import (
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
)

// restrictBotRoutinePolicy partitions the current owner baseline, never extending it.
// Only exact resources actually present in that baseline can become exceptions.
func restrictBotRoutinePolicy(p store.BotMandatePolicy, c config.BotRoutinePolicyConfig) (store.BotMandatePolicy, error) {
	if err := c.Validate(); err != nil {
		return store.BotMandatePolicy{}, err
	}
	if len(c.RequireApproval) == 0 {
		return p, nil
	}
	restricted := map[string]bool{}
	for _, control := range c.RequireApproval {
		switch control {
		case config.BotRoutineMemoryForget:
			restricted[entstore.RoutineControlResource(store.BotControlMemoryForget)] = true
		}
	}
	constraints := make([]execution.BotActionResourceConstraint, 0, len(p.Constraints))
	exceptions := append([]execution.BotActionResourceConstraint(nil), p.ExceptionConstraints...)
	for _, constraint := range p.Constraints {
		kept := make([]string, 0, len(constraint.Resources))
		for _, resource := range constraint.Resources {
			if constraint.Action == execution.CapabilityToolExecute && restricted[resource] {
				exceptions = append(exceptions, execution.BotActionResourceConstraint{Action: constraint.Action, Resources: []string{resource}})
			} else {
				kept = append(kept, resource)
			}
		}
		if len(kept) != 0 {
			constraint.Resources = kept
			constraints = append(constraints, constraint)
		}
	}
	p.Constraints = constraints
	p.ExceptionConstraints = exceptions
	return p, nil
}
