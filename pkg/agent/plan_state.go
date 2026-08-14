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
// it belongs to. A plan step is marked "running" when its first task starts,
// and "complete" only when ALL registered tasks for that step have completed.
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
	// update_plan (SetStepStatus). When true, the end-of-turn CompleteAll sweep
	// is suppressed so the plan reflects the model's real reported progress
	// instead of a bulk "everything complete" fabrication.
	manuallyTracked bool
}

type planStep struct {
	name          string
	description   string
	details       string           // optional richer per-phase content persisted to PLAN.md
	files         []PlanFileChange // optional affected files (path + new/modify/delete) persisted to PLAN.md
	verify        string           // optional command that proves the phase is done, persisted to PLAN.md
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
		ps.steps[i] = planStep{
			name:          s.Name,
			description:   s.Description,
			details:       s.Details,
			files:         s.Files,
			verify:        s.Verify,
			parallelGroup: s.ParallelGroup,
			status:        "pending",
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
			Files:         s.files,
			Verify:        s.verify,
			ParallelGroup: s.parallelGroup,
			Status:        s.status,
		}
	}
	return goal, info
}

// SnapshotDoc returns the document-level narrative sections stored in this plan.
func (ps *PlanState) SnapshotDoc() PlanDocumentInfo {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.doc
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
// via update_plan. Used to decide whether the end-of-turn CompleteAll sweep
// should run.
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

	// Mark the next pending step as running
	for i := range ps.steps {
		if ps.steps[i].status == "pending" {
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

	// Mark step running if pending
	if ps.steps[idx].status == "pending" {
		ps.steps[idx].status = "running"
		ps.notifyChangeLocked()
		return ps.steps[idx].name
	}
	return "" // already running or complete — no transition to emit
}

// CompleteTask marks a task as done within its plan step. If ALL registered
// tasks for that step are now complete, the step itself is marked "complete".
//
// Returns the step name if the step transitioned to "complete", or "" otherwise.
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
			ps.steps[idx].status = "complete"
			ps.notifyChangeLocked()
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

// CompleteAll marks all remaining running/pending steps as complete.
// Returns the names of steps that were transitioned.
func (ps *PlanState) CompleteAll() []string {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	var completed []string
	for i := range ps.steps {
		if ps.steps[i].status == "running" || ps.steps[i].status == "pending" {
			ps.steps[i].status = "complete"
			completed = append(completed, ps.steps[i].name)
		}
	}
	if len(completed) > 0 {
		ps.notifyChangeLocked()
	}
	return completed
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
