package tools

import (
	"context"
	"github.com/SAP/astonish/pkg/agent"
	"google.golang.org/adk/tool"
)

// PlanCallbacks binds the existing Chat plan tools to one agent. A present value
// is authoritative, including nil fields; it never falls back to global state.
type PlanCallbacks struct {
	Progress   func(agent.SubTaskProgressEvent)
	State      func(string, agent.PlanDocumentInfo, []agent.PlanStepInfo) bool
	Announce   func(string, agent.PlanDocumentInfo, []agent.PlanStepInfo) error
	Update     func(string, string) agent.PlanStepApplyResult
	KnownSteps func() []string
	Complete   func(string, string) agent.PlanCompletionResult
	Acceptance func() string
	Snapshot   func() agent.PlanSnapshot
	Revise     func(agent.PlanRevisionRequest) (agent.PlanRevisionResult, error)
}
type planCallbacksKey struct{}

func WithPlanCallbacks(ctx context.Context, callbacks PlanCallbacks) context.Context {
	return context.WithValue(ctx, planCallbacksKey{}, callbacks)
}
func scopedPlanCallbacks(ctx tool.Context) PlanCallbacks {
	if runtime, bound := agent.PlanRuntimeFromContext(ctx); bound {
		c := PlanCallbacks{}
		if ctx != nil {
			c, _ = ctx.Value(planCallbacksKey{}).(PlanCallbacks)
		}
		// A bound nil runtime explicitly disables all lifecycle operations.
		if runtime == nil {
			return PlanCallbacks{Progress: c.Progress, Acceptance: c.Acceptance}
		}
		c.State = nil
		c.Announce = runtime.Announce
		c.Update, c.Complete, c.Snapshot, c.Revise = runtime.UpdateStep, runtime.Complete, runtime.Snapshot, runtime.Revise
		c.KnownSteps = func() []string {
			var names []string
			for _, s := range runtime.Snapshot().Steps {
				names = append(names, s.Name)
			}
			return names
		}
		return c
	}
	if ctx != nil {
		if c, ok := ctx.Value(planCallbacksKey{}).(PlanCallbacks); ok {
			return c
		}
	}
	return PlanCallbacks{Progress: planProgressCallback, State: planStateCallback, Update: planStepUpdateCallback, KnownSteps: planKnownStepsCallback, Complete: planCompletionCallback, Acceptance: graphPlanAcceptanceLookup}
}
