package launcher

import (
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/backend"
	"strings"
)

func prepareCodePlanExecution(runtime *agent.PlanRuntime, message string, opts backend.TurnOptions) error {
	if opts.PlanMode || opts.GraphPlanMode || opts.AskMode {
		return nil
	}
	if opts.ApprovedPlanExecution && opts.CodePlanExecutionMode != "" {
		return runtime.SetCodeExecutionMode(opts.CodePlanExecutionMode)
	}
	if runtime.CodeReviewPending() {
		switch strings.ToLower(strings.TrimSpace(message)) {
		case "continue", "continue.", "proceed", "next", "continue to the next phase":
			return runtime.ContinueCodeReview()
		}
	}
	return nil
}
