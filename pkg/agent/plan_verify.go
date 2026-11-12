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
	if runtime := c.GetPlanRuntime(); runtime != nil {
		result := runtime.UpdateStep(step, status)
		if result.Code == PlanStepVerifyFailed {
			c.markPlanVerifyFailed()
		} else if result.Code == PlanStepOK && result.Applied == "complete" {
			c.clearPlanVerifyFailed()
		}
		return result
	}
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
		return c.applyLegacyPlanVerification(plan, info)

	default:
		name, applied := plan.SetStepStatus(info.Name, want)
		return PlanStepApplyResult{Name: name, Applied: applied, Code: PlanStepOK}
	}
}

func (c *ChatAgent) applyLegacyPlanVerification(plan *PlanState, info PlanStepInfo) PlanStepApplyResult {
	fail := func(code, message string) PlanStepApplyResult {
		return PlanStepApplyResult{Name: info.Name, Code: code, Message: message}
	}
	plan.mu.Lock()
	idx := plan.findStepExactLocked(info.Name)
	if idx < 0 {
		plan.mu.Unlock()
		return fail(PlanStepNotFound, "phase not found")
	}
	info = planStepInfo(plan.steps[idx])
	if plan.verifying[info.Name] {
		plan.mu.Unlock()
		return fail(PlanRuntimeBusy, "phase verification is already running")
	}
	if strings.TrimSpace(info.Verify) == "" {
		plan.mu.Unlock()
		return fail(PlanStepNoVerify, "this phase has no verify command; revise the plan")
	}
	if reason := plan.canStartLocked(idx); reason != "" {
		plan.mu.Unlock()
		return fail(reason, "an earlier phase must complete first")
	}
	if plan.verifying == nil {
		plan.verifying = map[string]bool{}
	}
	plan.verifying[info.Name] = true
	plan.steps[idx].status = "running"
	plan.notifyChangeLocked()
	captured := plan.steps[idx].epoch
	plan.mu.Unlock()
	exit, output, err := c.runPlanVerify(info.Verify)
	plan.mu.Lock()
	defer plan.mu.Unlock()
	delete(plan.verifying, info.Name)
	// Lock ordering matches the legacy onChange callback (plan -> agent).
	c.activePlanMu.Lock()
	if c.activePlan != plan || idx >= len(plan.steps) || plan.steps[idx].epoch != captured || !implementationEqual(planStepInfo(plan.steps[idx]).Definition(), info.Definition()) {
		c.activePlanMu.Unlock()
		return fail(PlanRuntimeStale, "phase changed during verification; evidence discarded")
	}
	evidence := formatPlanEvidence(exit, output)
	applied := "complete"
	code := PlanStepOK
	message := ""
	if err != nil || exit != 0 {
		applied = "failed"
		code = PlanStepVerifyFailed
		message = fmt.Sprintf("verify command exited %d — phase stays failed", exit)
		if err != nil {
			message = "verify command failed to run: " + err.Error()
			evidence += "; " + err.Error()
		}
	}
	plan.steps[idx].evidence = evidence
	plan.steps[idx].status = applied
	c.activePlanMu.Unlock()
	plan.notifyChangeLocked()
	if code == PlanStepVerifyFailed {
		c.markPlanVerifyFailed()
	} else {
		c.clearPlanVerifyFailed()
	}
	return PlanStepApplyResult{Name: info.Name, Applied: applied, Code: code, Message: message, Output: output}
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
	PlanCompletionOK         = "ok"
	PlanCompletionIncomplete = "incomplete"
	PlanCompletionNoPlan     = "no_active_plan"
)

// AnnounceCompletion records that an approved plan is done and writes the
// Results section. Completion is structural: every phase must already be
// complete, and each phase's verify command ran (and was recorded) when it was
// marked complete. This does NOT execute any command — Results aggregates the
// evidence already stored on each phase. The plan does not leave execution mode
// without this.
func (c *ChatAgent) AnnounceCompletion(outcomeObserved, unverified string) PlanCompletionResult {
	if runtime := c.GetPlanRuntime(); runtime != nil {
		return runtime.Complete(outcomeObserved, unverified)
	}
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
	// Aggregate the verify evidence each phase already recorded. No command
	// is executed here: re-running them would duplicate work the phases
	// already proved, and a failure would strand a sealed plan.
	_, steps := plan.SnapshotInfo()
	var logLines []string
	for _, step := range steps {
		cmd := strings.TrimSpace(step.Verify)
		if cmd == "" {
			continue
		}
		evidence := strings.TrimSpace(step.Evidence)
		if evidence == "" {
			evidence = "(no evidence recorded)"
		}
		logLines = append(logLines, fmt.Sprintf("%s\n$ %s\n%s", step.Name, cmd, evidence))
	}
	unverified = strings.TrimSpace(unverified)
	if unverified == "" {
		unverified = "(none stated)"
	}
	results := fmt.Sprintf("Outcome observed: %s\n\nPhase evidence:\n%s\n\nUnverified: %s",
		strings.TrimSpace(outcomeObserved),
		strings.Join(logLines, "\n"),
		unverified)
	plan.SetResults(results)
	c.clearPlanVerifyFailed()
	return PlanCompletionResult{Code: PlanCompletionOK, Log: strings.Join(logLines, "\n")}
}
