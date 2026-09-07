package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	planVerifyTimeout   = 10 * time.Minute
	planVerifyMaxOutput = 8 * 1024
)

const VerifyFailedFollowupContext = `The last plan phase verify command failed. Do not mark that phase complete and do not claim the plan is done. Investigate the failure using the verify output in the transcript. Research caps are lifted this turn.`

// DefaultPlanVerify runs command in workdir with a wall-clock cap. Output is
// truncated so PLAN.md and the tool result stay bounded.
func DefaultPlanVerify(workdir, command string) (exitCode int, output string, err error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return -1, "", fmt.Errorf("empty verify command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), planVerifyTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "bash", "-c", command)
	}
	if workdir != "" {
		cmd.Dir = workdir
	}
	out, runErr := cmd.CombinedOutput()
	trimmed := truncateVerifyOutput(out)
	if ctx.Err() == context.DeadlineExceeded {
		return -1, trimmed, fmt.Errorf("verify timed out after %s", planVerifyTimeout)
	}
	if runErr == nil {
		return 0, trimmed, nil
	}
	if ee, ok := runErr.(*exec.ExitError); ok {
		return ee.ExitCode(), trimmed, nil
	}
	return -1, trimmed, runErr
}

func truncateVerifyOutput(out []byte) string {
	if len(out) > planVerifyMaxOutput {
		out = out[:planVerifyMaxOutput]
	}
	out = bytes.TrimSpace(out)
	if !utf8.Valid(out) {
		return strings.ToValidUTF8(string(out), "\uFFFD")
	}
	return string(out)
}

func formatPlanEvidence(exitCode int, output string) string {
	line := fmt.Sprintf("exit %d", exitCode)
	if strings.TrimSpace(output) == "" {
		return line
	}
	// Single-line evidence so PLAN.md parser keeps it on the Evidence: line.
	compact := strings.Join(strings.Fields(output), " ")
	if len(compact) > 240 {
		compact = compact[:240] + "…"
	}
	return line + "; " + compact
}

// ApplyPlanStepUpdate is the engine behind update_plan. Completing a step runs
// that step's verify command; a non-zero exit marks the step failed.
func (c *ChatAgent) ApplyPlanStepUpdate(step, status string) PlanStepApplyResult {
	plan := c.GetActivePlan()
	if plan == nil {
		return PlanStepApplyResult{}
	}

	info, ok := plan.StepLookup(step)
	if !ok {
		return PlanStepApplyResult{}
	}

	want := normalizePlanStatus(status)
	switch want {
	case "running":
		if reason := plan.CanStart(info.Name); reason != "" {
			msg := "cannot start this phase yet"
			code := reason
			switch reason {
			case PlanStepSerialBlocked:
				msg = "cannot start " + info.Name + ": an earlier serial phase is not complete"
			case PlanStepDeleteBlocked:
				msg = "cannot start " + info.Name + ": deleting a running surface requires an earlier complete behavior phase"
			}
			return PlanStepApplyResult{Name: info.Name, Code: code, Message: msg}
		}
		name, applied := plan.SetStepStatus(info.Name, "running")
		return PlanStepApplyResult{Name: name, Applied: applied, Code: PlanStepOK}

	case "failed":
		name, applied := plan.SetStepStatus(info.Name, "failed")
		c.markPlanVerifyFailed()
		return PlanStepApplyResult{Name: name, Applied: applied, Code: PlanStepOK}

	case "complete":
		if !c.IsActivePlanApproved() {
			return PlanStepApplyResult{
				Name:    info.Name,
				Code:    PlanStepBlockedPlan,
				Message: "cannot mark a phase complete until the user has approved the plan for execution",
			}
		}
		cmd := strings.TrimSpace(info.Verify)
		if cmd == "" {
			return PlanStepApplyResult{
				Name:    info.Name,
				Code:    PlanStepNoVerify,
				Message: "this phase has no verify command; request changes and re-announce the plan — do not invent go test ./...",
			}
		}
		if info.Status != "running" {
			if reason := plan.CanStart(info.Name); reason == "" {
				plan.SetStepStatus(info.Name, "running")
			}
		}
		exit, output, err := c.runPlanVerify(cmd)
		evidence := formatPlanEvidence(exit, output)
		if err != nil {
			plan.RecordEvidence(info.Name, evidence+"; "+err.Error())
			plan.SetStepStatus(info.Name, "failed")
			c.markPlanVerifyFailed()
			return PlanStepApplyResult{
				Name:    info.Name,
				Applied: "failed",
				Code:    PlanStepVerifyFailed,
				Message: "verify command failed to run: " + err.Error(),
				Output:  output,
			}
		}
		if exit != 0 {
			plan.RecordEvidence(info.Name, evidence)
			plan.SetStepStatus(info.Name, "failed")
			c.markPlanVerifyFailed()
			return PlanStepApplyResult{
				Name:    info.Name,
				Applied: "failed",
				Code:    PlanStepVerifyFailed,
				Message: fmt.Sprintf("verify command exited %d — phase stays failed", exit),
				Output:  output,
			}
		}
		plan.RecordEvidence(info.Name, evidence)
		name, applied := plan.SetStepStatus(info.Name, "complete")
		c.clearPlanVerifyFailed()
		return PlanStepApplyResult{Name: name, Applied: applied, Code: PlanStepOK, Output: output}

	default:
		name, applied := plan.SetStepStatus(info.Name, want)
		return PlanStepApplyResult{Name: name, Applied: applied, Code: PlanStepOK}
	}
}

func (c *ChatAgent) runPlanVerify(command string) (int, string, error) {
	if c != nil && c.PlanVerify != nil {
		return c.PlanVerify(command)
	}
	workdir := ""
	if c != nil {
		workdir = c.WorkingDir
	}
	return DefaultPlanVerify(workdir, command)
}

func (c *ChatAgent) markPlanVerifyFailed() {
	if c == nil {
		return
	}
	c.planVerifyMu.Lock()
	c.planVerifyFailed = true
	c.planVerifyMu.Unlock()
}

func (c *ChatAgent) clearPlanVerifyFailed() {
	if c == nil {
		return
	}
	c.planVerifyMu.Lock()
	c.planVerifyFailed = false
	c.planVerifyMu.Unlock()
}

// PlanVerifyFailed reports whether the last verify command failed and research
// caps should be lifted.
func (c *ChatAgent) PlanVerifyFailed() bool {
	if c == nil {
		return false
	}
	c.planVerifyMu.Lock()
	defer c.planVerifyMu.Unlock()
	return c.planVerifyFailed
}

// PlanCompletionResult is the outcome of announce_completion.
type PlanCompletionResult struct {
	Code    string
	Message string
	Log     string
}

const (
	PlanCompletionOK           = "ok"
	PlanCompletionIncomplete   = "incomplete"
	PlanCompletionVerifyFailed = "verify_failed"
	PlanCompletionNoPlan       = "no_active_plan"
)

// AnnounceCompletion runs plan-level verification and writes the Results
// section. The plan does not leave execution mode without this.
func (c *ChatAgent) AnnounceCompletion(outcomeObserved, unverified string) PlanCompletionResult {
	plan := c.GetActivePlan()
	if plan == nil {
		return PlanCompletionResult{Code: PlanCompletionNoPlan, Message: "no active plan"}
	}
	if !c.IsActivePlanApproved() {
		return PlanCompletionResult{Code: PlanCompletionIncomplete, Message: "the plan is not approved for execution"}
	}
	if !plan.AllStepsComplete() {
		return PlanCompletionResult{Code: PlanCompletionIncomplete, Message: "not every phase is complete with a passing verify — cannot celebrate yet"}
	}
	doc := plan.SnapshotDoc()
	cmds := verificationCommands(doc.Verification)
	if len(cmds) == 0 {
		return PlanCompletionResult{Code: PlanCompletionIncomplete, Message: "plan-level verification is empty; request changes and re-announce"}
	}
	var logLines []string
	for _, cmd := range cmds {
		exit, output, err := c.runPlanVerify(cmd)
		line := formatPlanEvidence(exit, output)
		if err != nil {
			line += "; " + err.Error()
		}
		logLines = append(logLines, "$ "+cmd+"\n"+line)
		if err != nil || exit != 0 {
			c.markPlanVerifyFailed()
			return PlanCompletionResult{
				Code:    PlanCompletionVerifyFailed,
				Message: "end-to-end verification failed; stay in execution mode",
				Log:     strings.Join(logLines, "\n"),
			}
		}
	}
	unverified = strings.TrimSpace(unverified)
	if unverified == "" {
		unverified = "(none stated)"
	}
	results := fmt.Sprintf("Outcome observed: %s\n\nVerification:\n%s\n\nUnverified: %s",
		strings.TrimSpace(outcomeObserved),
		strings.Join(logLines, "\n"),
		unverified)
	plan.SetResults(results)
	c.clearPlanVerifyFailed()
	return PlanCompletionResult{Code: PlanCompletionOK, Log: strings.Join(logLines, "\n")}
}

func verificationCommands(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")
		out = append(out, line)
	}
	return out
}
