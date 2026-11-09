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
	Update     func(string, string) agent.PlanStepApplyResult
	KnownSteps func() []string
	Complete   func(string, string) agent.PlanCompletionResult
	Acceptance func() string
}
type planCallbacksKey struct{}

func WithPlanCallbacks(ctx context.Context, callbacks PlanCallbacks) context.Context {
	return context.WithValue(ctx, planCallbacksKey{}, callbacks)
}
func scopedPlanCallbacks(ctx tool.Context) PlanCallbacks {
	if ctx != nil {
		if c, ok := ctx.Value(planCallbacksKey{}).(PlanCallbacks); ok {
			return c
		}
	}
	return PlanCallbacks{Progress: planProgressCallback, State: planStateCallback, Update: planStepUpdateCallback, KnownSteps: planKnownStepsCallback, Complete: planCompletionCallback, Acceptance: graphPlanAcceptanceLookup}
}
