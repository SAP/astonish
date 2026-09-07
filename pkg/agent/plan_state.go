package agent

import (
	"strings"
	"sync"
)

// PlanState tracks the ordered steps from an announce_plan call, enabling
// automatic progression driven by sub-task lifecycle events (task_start,
// task_complete) with explicit plan_step binding between delegate tasks
// and plan steps.
//
// Each delegate task carries a plan_step field identifying which plan step
// it belongs to. A plan step is marked "running" when its first task starts.
// Completing a step requires a passing verify command — finishing sub-tasks
// is not enough.
type PlanState struct {
	mu    sync.Mutex
	goal  string
	doc   PlanDocumentInfo
	steps []planStep

	// taskRegistry tracks which tasks belong to each plan step.
	// Key: step name (lowercase), Value: set of task names (lowercase).
	taskRegistry map[string]map[string]bool

	// completedTasks tracks which tasks have finished.
	// Key: step name (lowercase), Value: set of completed task names (lowercase).
	completedTasks map[string]map[string]bool

	// onChange, if set, is invoked (with ps.mu held) whenever a step's status
	// transitions. It is used to persist the plan to PLAN.md so the plan
	// survives context compaction. Kept minimal and non-blocking by callers.
	onChange func()

	// manuallyTracked is set once the model explicitly drives the plan via
	// update_plan (SetStepStatus).
	manuallyTracked bool
}

type planStep struct {
	name          string
	description   string
	details       string           // optional richer per-phase content persisted to PLAN.md
	summary       string           // optional plain-English explanation for the human approving the plan
	files         []PlanFileChange // optional affected files (path + new/modify/delete) persisted to PLAN.md
	outcome       string           // testable user-visible contract, persisted to PLAN.md
	verify        string           // command that proves the phase is done, persisted to PLAN.md
	verifyKind    string           // "unit" or "behavior", persisted to PLAN.md
	evidence      string           // last verify result, persisted to PLAN.md
	parallelGroup string           // optional concurrency group label
	status        string           // "pending", "running", "complete", "failed"
}

// NewPlanState creates a PlanState from an announce_plan call's step list.
func NewPlanState(goal string, doc PlanDocumentInfo, steps []PlanStepInfo) *PlanState {
	ps := &PlanState{
		goal:           goal,
		doc:            doc,
		steps:          make([]planStep, len(steps)),
		taskRegistry:   make(map[string]map[string]bool),
		completedTasks: make(map[string]map[string]bool),
	}
	for i, s := range steps {
		status := normalizePlanStatus(s.Status)
		if strings.TrimSpace(s.Status) == "" {
			status = "pending"
		}
		ps.steps[i] = planStep{
			name:          s.Name,
			description:   s.Description,
			details:       s.Details,
			summary:       s.Summary,
			files:         s.Files,
			outcome:       s.Outcome,
			verify:        s.Verify,
			verifyKind:    NormalizeVerifyKind(s.VerifyKind),
			evidence:      s.Evidence,
			parallelGroup: s.ParallelGroup,
			status:        status,
		}
	}
	return ps
}

// SetOnChange registers a callback invoked whenever a step's status transitions.
// The callback runs with the internal mutex held, so it must not call back into
// PlanState. Used to persist the plan to PLAN.md.
func (ps *PlanState) SetOnChange(fn func()) {
	ps.mu.Lock()
	ps.onChange = fn
	ps.mu.Unlock()
}

// notifyChangeLocked invokes the onChange hook. Must be called with ps.mu held.
func (ps *PlanState) notifyChangeLocked() {
	if ps.onChange != nil {
		ps.onChange()
	}
}

// Snapshot returns the plan goal and a copy of its steps. Thread-safe.
func (ps *PlanState) Snapshot() (string, []planStep) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.snapshotLocked()
}

// SnapshotInfo returns the plan goal and steps as exported PlanStepInfo values.
// Used by the TUI to render the plan without depending on the unexported planStep type.
func (ps *PlanState) SnapshotInfo() (string, []PlanStepInfo) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	goal, steps := ps.snapshotLocked()
	info := make([]PlanStepInfo, len(steps))
	for i, s := range steps {
		info[i] = PlanStepInfo{
			Name:          s.name,
			Description:   s.description,
			Details:       s.details,
			Summary:       s.summary,
			Files:         s.files,
			Outcome:       s.outcome,
			Verify:        s.verify,
			VerifyKind:    s.verifyKind,
			Evidence:      s.evidence,
			ParallelGroup: s.parallelGroup,
			Status:        s.status,
		}
	}
	return goal, info
}

// StepNames returns the exact announce_plan step identifiers, in order.
func (ps *PlanState) StepNames() []string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	names := make([]string, len(ps.steps))
	for i, s := range ps.steps {
		names[i] = s.name
	}
	return names
}

// SnapshotDoc returns the document-level narrative sections stored in this plan.
func (ps *PlanState) SnapshotDoc() PlanDocumentInfo {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.doc
}

// SetResults stores the completion report and persists PLAN.md.
func (ps *PlanState) SetResults(results string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.doc.Results = results
	ps.notifyChangeLocked()
}

// AllStepsComplete reports whether every phase is complete (not failed/pending).
func (ps *PlanState) AllStepsComplete() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.steps) == 0 {
		return false
	}
	for _, s := range ps.steps {
		if s.status != "complete" {
			return false
		}
	}
	return true
}

// IsFullyAccepted is true only when every phase is complete and a Results
// section exists. Checkboxes alone are not enough to leave execution mode.
func (ps *PlanState) IsFullyAccepted() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if strings.TrimSpace(ps.doc.Results) == "" {
		return false
	}
	if len(ps.steps) == 0 {
		return false
	}
	for _, s := range ps.steps {
		if s.status != "complete" {
			return false
		}
	}
	return true
}

// snapshotLocked returns the plan goal and a copy of its steps.
// Must be called with ps.mu held (e.g. from within the onChange hook).
func (ps *PlanState) snapshotLocked() (string, []planStep) {
	steps := make([]planStep, len(ps.steps))
	copy(steps, ps.steps)
	return ps.goal, steps
}

// normalizePlanStatus maps caller-provided status strings to the canonical set.
// Unknown values fall back to "pending".
func normalizePlanStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "in_progress", "in-progress", "started":
		return "running"
	case "complete", "completed", "done", "finished":
		return "complete"
	case "failed", "error", "blocked":
		return "failed"
	default:
		return "pending"
	}
}

// SetStepStatus explicitly transitions a named step to the given status. This
// is the engine behind the update_plan tool: it lets the model drive plan
// progress for main-thread (non-delegated) work. It marks the plan as manually
// tracked (suppressing the end-of-turn bulk sweep), fires the onChange hook so
// PLAN.md is rewritten, and returns the canonical step name + status if a
// transition occurred, or ("", "") when the step was not found.
func (ps *PlanState) SetStepStatus(stepName, status string) (string, string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	idx := ps.findStepLocked(stepName)
	if idx < 0 {
		return "", ""
	}
	ps.manuallyTracked = true
	newStatus := normalizePlanStatus(status)
	if ps.steps[idx].status == newStatus {
		return ps.steps[idx].name, newStatus // idempotent, no rewrite needed
	}
	ps.steps[idx].status = newStatus
	ps.notifyChangeLocked()
	return ps.steps[idx].name, newStatus
}

// IsManuallyTracked reports whether the model has explicitly driven this plan
// via update_plan.
func (ps *PlanState) IsManuallyTracked() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.manuallyTracked
}

// AdvanceOnToolStart is called when a non-delegate tool begins executing.
// If no step is currently running, it marks the next pending step as running
// and returns the step name (for SSE emission). Returns "" if no step to advance.
func (ps *PlanState) AdvanceOnToolStart() string {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// Check if any step is already running
	for _, s := range ps.steps {
		if s.status == "running" {
			return "" // a step is already in progress
		}
	}

	for i := range ps.steps {
		if ps.steps[i].status == "pending" {
			if reason := ps.canStartLocked(i); reason != "" {
				return ""
			}
			ps.steps[i].status = "running"
			ps.notifyChangeLocked()
			return ps.steps[i].name
		}
	}
	return ""
}

// StartStep registers a task under a plan step and marks the step as "running"
// if it is currently "pending". The stepName is resolved either from the
// explicit plan_step field or via fallback prefix matching on taskName.
//
// Returns the matched step name (for SSE emission), or "" if no match or
// the step is already running/complete.
func (ps *PlanState) StartStep(stepName, taskName string) string {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	idx := ps.findStepLocked(stepName)
	if idx < 0 {
		return ""
	}

	sn := strings.ToLower(ps.steps[idx].name)
	tn := strings.ToLower(taskName)

	// Register this task under the step
	if ps.taskRegistry[sn] == nil {
		ps.taskRegistry[sn] = make(map[string]bool)
	}
	ps.taskRegistry[sn][tn] = true

	if ps.steps[idx].status == "pending" {
		if reason := ps.canStartLocked(idx); reason != "" {
			return ""
		}
		ps.steps[idx].status = "running"
		ps.notifyChangeLocked()
		return ps.steps[idx].name
	}
	return "" // already running or complete — no transition to emit
}

// CompleteTask marks a task as done within its plan step. If ALL registered
// tasks for that step are now finished, the step name is returned so the
// caller can run verify. The step stays "running" — sub-agent finish is not
// completion.
func (ps *PlanState) CompleteTask(stepName, taskName string) string {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	idx := ps.findStepLocked(stepName)
	if idx < 0 {
		return ""
	}
	if ps.steps[idx].status != "running" {
		return "" // not running — nothing to complete
	}

	sn := strings.ToLower(ps.steps[idx].name)
	tn := strings.ToLower(taskName)

	// Record this task as completed
	if ps.completedTasks[sn] == nil {
		ps.completedTasks[sn] = make(map[string]bool)
	}
	ps.completedTasks[sn][tn] = true

	// Check if ALL registered tasks for this step are done
	registered := ps.taskRegistry[sn]
	completed := ps.completedTasks[sn]
	if len(registered) > 0 && len(completed) >= len(registered) {
		allDone := true
		for task := range registered {
			if !completed[task] {
				allDone = false
				break
			}
		}
		if allDone {
			return ps.steps[idx].name
		}
	}
	return "" // not all tasks done yet
}

// ResolveStepName returns the plan step name for a given explicit plan_step
// value and task name. If planStep is non-empty, it uses exact matching.
// If planStep is empty, it falls back to prefix matching on taskName.
// Returns "" if no match found.
func (ps *PlanState) ResolveStepName(planStep, taskName string) string {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if planStep != "" {
		// Exact match on plan_step
		idx := ps.findStepExactLocked(planStep)
		if idx >= 0 {
			return ps.steps[idx].name
		}
		return ""
	}

	// Fallback: prefix matching on taskName
	idx := ps.matchStepByPrefixLocked(taskName)
	if idx >= 0 {
		return ps.steps[idx].name
	}
	return ""
}

// findStepLocked finds a step by exact case-insensitive name match.
// Must be called with ps.mu held.
func (ps *PlanState) findStepExactLocked(name string) int {
	target := strings.ToLower(name)
	for i, s := range ps.steps {
		if strings.ToLower(s.name) == target {
			return i
		}
	}
	return -1
}

// findStepLocked tries exact match first, then prefix match.
// Must be called with ps.mu held.
func (ps *PlanState) findStepLocked(name string) int {
	// Try exact match first
	idx := ps.findStepExactLocked(name)
	if idx >= 0 {
		return idx
	}
	// Fall back to prefix match
	return ps.matchStepByPrefixLocked(name)
}

// matchStepByPrefixLocked finds the best matching plan step index using
// prefix matching on task name. Returns -1 if no match.
// Must be called with ps.mu held.
func (ps *PlanState) matchStepByPrefixLocked(taskName string) int {
	bestIdx := -1
	bestLen := 0

	tn := strings.ToLower(taskName)
	for i, s := range ps.steps {
		sn := strings.ToLower(s.name)
		if strings.HasPrefix(tn, sn) || strings.HasPrefix(sn, tn) {
			if len(sn) > bestLen {
				bestIdx = i
				bestLen = len(sn)
			}
		}
	}
	return bestIdx
}

// CompleteAll is a no-op. Pending and running steps stay as they are.
// Completion requires a passing verify command via update_plan.
func (ps *PlanState) CompleteAll() []string {
	return nil
}

// PlanStepApplyResult is the outcome of applying an update_plan status change.
type PlanStepApplyResult struct {
	Name    string
	Applied string
	Code    string
	Message string
	Output  string
}

const (
	PlanStepOK            = "ok"
	PlanStepNotFound      = "step_not_found"
	PlanStepVerifyFailed  = "verify_failed"
	PlanStepNoVerify      = "no_verify"
	PlanStepBlockedPlan   = "blocked_plan_mode"
	PlanStepSerialBlocked = "serial_blocked"
	PlanStepDeleteBlocked = "delete_blocked"
)

// StepLookup returns a copy of the named step, or false if missing.
func (ps *PlanState) StepLookup(stepName string) (PlanStepInfo, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	idx := ps.findStepLocked(stepName)
	if idx < 0 {
		return PlanStepInfo{}, false
	}
	s := ps.steps[idx]
	return PlanStepInfo{
		Name:          s.name,
		Description:   s.description,
		Details:       s.details,
		Summary:       s.summary,
		Files:         s.files,
		Outcome:       s.outcome,
		Verify:        s.verify,
		VerifyKind:    s.verifyKind,
		Evidence:      s.evidence,
		ParallelGroup: s.parallelGroup,
		Status:        s.status,
	}, true
}

// RecordEvidence stores the last verify result on the named step.
func (ps *PlanState) RecordEvidence(stepName, evidence string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	idx := ps.findStepLocked(stepName)
	if idx < 0 {
		return
	}
	ps.steps[idx].evidence = evidence
	ps.notifyChangeLocked()
}

// CanStart reports whether the named step may be marked running given serial
// order and delete-after-behavior rules. Empty reason means yes.
func (ps *PlanState) CanStart(stepName string) string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	idx := ps.findStepLocked(stepName)
	if idx < 0 {
		return "step not found"
	}
	return ps.canStartLocked(idx)
}

func (ps *PlanState) canStartLocked(idx int) string {
	group := strings.TrimSpace(ps.steps[idx].parallelGroup)
	for i := 0; i < idx; i++ {
		eg := strings.TrimSpace(ps.steps[i].parallelGroup)
		if group != "" && group == eg {
			continue
		}
		if ps.steps[i].status != "complete" {
			return PlanStepSerialBlocked
		}
	}
	if stepDeletesRunningSurface(ps.steps[idx]) {
		prior := false
		for i := 0; i < idx; i++ {
			if ps.steps[i].verifyKind == VerifyKindBehavior && ps.steps[i].status == "complete" {
				prior = true
				break
			}
		}
		if !prior {
			return PlanStepDeleteBlocked
		}
	}
	return ""
}

func stepDeletesRunningSurface(s planStep) bool {
	for _, f := range s.files {
		if strings.TrimSpace(f.Path) == "" {
			continue
		}
		if fileKindIsDelete(f.Kind) && PathTouchesRunningSurface(f.Path) {
			return true
		}
	}
	return false
}

// HasPendingSteps returns true if any steps are still pending or running.
func (ps *PlanState) HasPendingSteps() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	for _, s := range ps.steps {
		if s.status == "pending" || s.status == "running" {
			return true
		}
	}
	return false
}

// IsAllComplete returns true when every step has reached a terminal state
// (complete or failed) — i.e. there is nothing left to execute. This is
// used to transition the session from execution mode to normal conversation
// mode so the model stops treating every user message as an action request.
func (ps *PlanState) IsAllComplete() bool {
	return !ps.HasPendingSteps()
}

// HasStartedSteps reports whether any step has left the "pending" state — i.e.
// whether execution actually began (a step is running/complete/failed).
//
// This guards the end-of-turn CompleteAll sweep: if the plan was merely
// announced this turn and no work started (every step still pending), the sweep
// must NOT fire — otherwise a freshly announced plan is immediately recorded as
// fully complete before any work is done.
func (ps *PlanState) HasStartedSteps() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	for _, s := range ps.steps {
		if s.status != "pending" {
			return true
		}
	}
	return false
}
