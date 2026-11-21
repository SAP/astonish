package launcher

import (
	"fmt"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/backend"
	"strings"
)

func prepareCodePlanExecution(runtime *agent.PlanRuntime, message string, opts backend.TurnOptions) error {
	if opts.CodePlanPermissionMode != "" {
		if !opts.CodePlanPermissionMode.Valid() || !opts.ApprovedPlanExecution || opts.PlanMode || opts.GraphPlanMode || opts.AskMode {
			return fmt.Errorf("invalid Code plan permission approval")
		}
		return runtime.ApproveCodeExecution(opts.CodePlanApprovalID, opts.CodePlanApprovalRevision, opts.CodePlanExecutionMode)
	}
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
