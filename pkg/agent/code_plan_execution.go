package agent

import (
	"fmt"
	"strings"
)

// CodePlanExecutionMode is trusted local-Code approval state, never a model revision field.
type CodePlanExecutionMode string

const (
	CodePlanRunToCompletion CodePlanExecutionMode = "run_to_completion"
	CodePlanReviewEachPhase CodePlanExecutionMode = "review_each_phase"
	CodePlanReviewRequired                        = "code_plan_review_required"
)

func (m CodePlanExecutionMode) Valid() bool {
	return m == CodePlanRunToCompletion || m == CodePlanReviewEachPhase
}

// SetCodeExecutionMode is called only by the local approval transport.
func (r *PlanRuntime) SetCodeExecutionMode(mode CodePlanExecutionMode) error {
	if !mode.Valid() {
		return fmt.Errorf("invalid Code plan execution mode %q", mode)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	r.plan.mu.Lock()
	defer r.plan.mu.Unlock()
	s := snapshotPlanLocked(r.plan)
	s.Document.CodeExecutionMode = mode
	s.Document.CodeReviewPending = ""
	return r.commitLocked(s)
}

func (r *PlanRuntime) CodeExecutionEnabled() bool {
	return r != nil && r.options.CodeExecution && r.Snapshot().Document.CodeExecutionMode.Valid()
}

func codeReviewPending(s PlanSnapshot) bool {
	return s.Document.CodeExecutionMode == CodePlanReviewEachPhase && s.Document.CodeReviewPending != "" && s.Document.Results == ""
}

// codePhaseBlock enforces review ownership independently of parallel groups.
// Callers apply this only to local Code execution. The pending phase remains
// editable for corrections; only explicit continuation releases its checkpoint.
func codePhaseBlock(s PlanSnapshot, step string) error {
	if s.Document.CodeExecutionMode != CodePlanReviewEachPhase {
		return nil
	}
	if codeReviewPending(s) && !strings.EqualFold(s.Document.CodeReviewPending, step) {
		return planRuntimeError(CodePlanReviewRequired, fmt.Sprintf("phase %q awaits user review before phase %q can execute", s.Document.CodeReviewPending, step))
	}
	for _, prior := range s.Steps {
		if strings.EqualFold(prior.Name, step) {
			break
		}
		if prior.Status != "complete" || !passingPlanEvidence(prior.Evidence) {
			return planRuntimeError(CodePlanReviewRequired, fmt.Sprintf("phase %q must verify and receive user review before phase %q can execute", prior.Name, step))
		}
	}
	return nil
}

func (r *PlanRuntime) CodeReviewPending() bool {
	return r.CodeExecutionEnabled() && codeReviewPending(r.Snapshot())
}

// ContinueCodeReview clears only the trusted checkpoint, never evidence or mode.
func (r *PlanRuntime) ContinueCodeReview() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return nil
	}
	r.plan.mu.Lock()
	defer r.plan.mu.Unlock()
	s := snapshotPlanLocked(r.plan)
	if s.Document.CodeReviewPending == "" {
		return nil
	}
	s.Document.CodeReviewPending = ""
	return r.commitLocked(s)
}

// CodePlanRecovery is normal model feedback, not an execution/transport error.
type CodePlanRecovery struct {
	Code     string       `json:"code"`
	Message  string       `json:"message"`
	Snapshot PlanSnapshot `json:"snapshot"`
	Blockers []string     `json:"blockers,omitempty"`
	Recovery string       `json:"recovery"`
}

func (r *PlanRuntime) CodeRecovery(code, message string) *CodePlanRecovery {
	s := r.Snapshot()
	blockers := []string{}
	for _, step := range s.Steps {
		if step.Status != "complete" {
			blockers = append(blockers, step.Name+": "+step.Status)
		}
	}
	return &CodePlanRecovery{Code: code, Message: message, Snapshot: s, Blockers: blockers, Recovery: CodePlanRecoveryGuidance}
}

const CodePlanRecoveryGuidance = `Continue the approved plan, not routine progress narration. Incomplete prerequisites: finish and verify them before retrying the blocked phase. Invalid/impossible acceptance or ordering: use evidence-based update_plan action=revise with the current plan_id/base_revision and full definitions, then continue; do not weaken valid verification to hide a real failure. Real implementation/test failures: fix the code and rerun verification. If a revision is blocked by a running phase, mark that phase failed after its delegates finish, then revise. Delegate finish is not phase completion: explicitly update_plan complete only after all phase work is ready. Call announce_completion after every phase has passing evidence. Stop for actual authorization, cancellation, provider failures or required user input. In review_each_phase mode stop at the runtime review checkpoint until the user continues.`

// AdmitCodeDelegates validates the entire batch before committing any phase start.
// IDs, not display names, own registry entries; callbacks are ignored on this path.
func (r *PlanRuntime) AdmitCodeDelegates(tasks []SubAgentTask) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	p := r.plan
	p.mu.Lock()
	defer p.mu.Unlock()
	s := snapshotPlanLocked(p)
	if err := runtimeEditable(s); err != nil {
		return err
	}
	if s.Document.Lifecycle != PlanLifecycleApproved {
		return planRuntimeError(PlanRuntimeUnapproved, "the plan is not approved for execution")
	}
	for _, task := range tasks {
		idx := p.findStepExactLocked(task.PlanStep)
		if idx < 0 {
			return planRuntimeError(PlanStepNotFound, "each Code execution delegate requires an exact plan_step")
		}
		if r.options.CodeExecution {
			if err := codePhaseBlock(s, s.Steps[idx].Name); err != nil {
				return err
			}
		}
		if s.Steps[idx].Status == "complete" {
			return planRuntimeError(PlanRuntimeCompleted, "phase already complete: "+task.PlanStep)
		}
		if r.inFlight[s.Steps[idx].Name] || p.verifying[s.Steps[idx].Name] {
			return planRuntimeError(PlanRuntimeBusy, "phase verification running: "+task.PlanStep)
		}
		if reason := p.canStartLocked(idx); reason != "" {
			return planRuntimeError(reason, "finish earlier prerequisites before delegating "+task.PlanStep)
		}
		if s.Steps[idx].Status != "running" {
			s.Steps[idx].Evidence = ""
		}
		s.Steps[idx].Status = "running"
	}
	if err := r.commitLocked(s); err != nil {
		return err
	}
	for _, task := range tasks {
		key := strings.ToLower(task.PlanStep)
		if p.taskRegistry[key] == nil {
			p.taskRegistry[key] = map[string]bool{}
		}
		p.taskRegistry[key][task.TaskID] = true
	}
	return nil
}

// FinishCodeDelegates releases all registry entries, including error/cancel paths.
// Successful subtasks leave the phase running until the parent explicitly verifies.
func (r *PlanRuntime) FinishCodeDelegates(tasks []SubAgentTask) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return
	}
	p := r.plan
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, task := range tasks {
		key := strings.ToLower(task.PlanStep)
		if p.taskRegistry[key][task.TaskID] {
			if p.completedTasks[key] == nil {
				p.completedTasks[key] = map[string]bool{}
			}
			p.completedTasks[key][task.TaskID] = true
		}
	}
}
