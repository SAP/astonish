package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// TrustedPlanRevisionDecision is transport-owned, never inferred from user prose.
type TrustedPlanRevisionDecision struct {
	PlanID       string               `json:"plan_id"`
	ProposalID   string               `json:"proposal_id"`
	BaseRevision int64                `json:"base_revision"`
	Decision     PlanRevisionDecision `json:"decision"`
}

const PlanSnapshotStateKey = "astonish_plan_snapshot"

type sessionPlanRuntimeKey struct {
	agent     *ChatAgent
	sessionID string
}

var sessionPlanRuntimes sync.Map

// BindSessionPlanRuntime selects the session-local registry for legacy delegate
// progress events, which carry a session ID rather than a context.
func (c *ChatAgent) BindSessionPlanRuntime(sessionID string, runtime *PlanRuntime) func() {
	key := sessionPlanRuntimeKey{c, sessionID}
	binding := &planRuntimeBinding{runtime: runtime}
	sessionPlanRuntimes.Store(key, binding)
	return func() { sessionPlanRuntimes.CompareAndDelete(key, binding) }
}
func (c *ChatAgent) SessionPlanRuntime(sessionID string) (*PlanRuntime, bool) {
	value, ok := sessionPlanRuntimes.Load(sessionPlanRuntimeKey{c, sessionID})
	if !ok {
		return nil, false
	}
	return value.(*planRuntimeBinding).runtime, true
}

// PlanState exposes the session's existing delegate lifecycle registry.
func (r *PlanRuntime) PlanState() *PlanState {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.plan
}

// RevisionToolBlocked checks the logical operation, including execute_tool wrappers.
func RevisionToolBlocked(po *PromptOverrides, name string, args map[string]any) string {
	if name != "update_plan" || args["action"] != "revise" {
		return ""
	}
	if po != nil {
		if po.Bot != nil {
			return "unsupported_bot_revision"
		}
		if po.PlanMode || po.GraphPlanMode || po.AskMode {
			return "blocked_revision_mode"
		}
	}
	return ""
}

func IsPlanManagementTool(name string) bool {
	return name == "announce_plan" || name == "update_plan" || name == "announce_completion" || IsGraphPlanTransitionTool(name)
}

// PlanRuntimeError is a stable machine-readable failure, including stale evidence.
type PlanRuntimeError struct {
	Code    string
	Message string
}

func (e *PlanRuntimeError) Error() string { return e.Message }

const (
	PlanRuntimeNoPlan             = "no_active_plan"
	PlanRuntimeStale              = "stale_revision"
	PlanRuntimeBusy               = "plan_busy"
	PlanRuntimePending            = "revision_pending"
	PlanRuntimeRevisionObsolete   = "revision_decision_obsolete"
	PlanRuntimeCompleted          = "plan_completed"
	PlanRuntimeInvalid            = "invalid_revision"
	PlanRuntimePersistFailed      = "persistence_failed"
	PlanRuntimeAlreadyResolved    = "already_resolved"
	PlanRuntimeUnapproved         = "plan_not_approved"
	PlanRuntimeReplacementBlocked = "blocked_active_approved_plan"
)

func planRuntimeError(code, message string) error {
	return &PlanRuntimeError{Code: code, Message: message}
}
func PlanRuntimeErrorCode(err error) string {
	var e *PlanRuntimeError
	if errors.As(err, &e) {
		return e.Code
	}
	return PlanRuntimeInvalid
}

// CommitPlanSnapshot must durably save the complete snapshot or return an error.
// It runs under the plan lock and must not call back into the runtime/PlanState.
// A nil callback is an explicit in-memory session, not a best-effort disk save.
type CommitPlanSnapshot func(PlanSnapshot) error

type PlanRuntimeOptions struct {
	Workspace      string
	Acceptance     string
	Verify         func(string) (int, string, error)
	CommitSnapshot CommitPlanSnapshot
}

// PlanRuntime belongs to one session. The PlanState lock also serializes legacy
// delegate lifecycle callbacks; no model or ChatAgent-global state is consulted.
type PlanRuntime struct {
	mu                      sync.Mutex
	plan                    *PlanState
	options                 PlanRuntimeOptions
	inFlight                map[string]bool
	allowInitialReplacement bool
}

func NewPlanRuntime(plan *PlanState, options PlanRuntimeOptions) *PlanRuntime {
	return &PlanRuntime{plan: plan, options: options, inFlight: make(map[string]bool)}
}
func NewPlanRuntimeFromSnapshot(snapshot PlanSnapshot, options PlanRuntimeOptions) *PlanRuntime {
	if snapshot.PendingRevision == nil {
		snapshot.PendingRevision = snapshot.Document.PendingRevision.Clone()
	}
	syncPlanSnapshotMetadata(&snapshot)
	return NewPlanRuntime(NewPlanState(snapshot.Goal, snapshot.Document, snapshot.Steps), options)
}

// PlanRuntimeFromContext distinguishes an unbound context from a bound nil
// runtime. The latter is authoritative and must never fall back to shared state.
type planRuntimeContextKey struct{}
type planRuntimeBinding struct{ runtime *PlanRuntime }

func WithPlanRuntime(ctx context.Context, runtime *PlanRuntime) context.Context {
	return context.WithValue(ctx, planRuntimeContextKey{}, planRuntimeBinding{runtime})
}
func PlanRuntimeFromContext(ctx context.Context) (*PlanRuntime, bool) {
	if ctx == nil {
		return nil, false
	}
	binding, ok := ctx.Value(planRuntimeContextKey{}).(planRuntimeBinding)
	return binding.runtime, ok
}

func snapshotPlanLocked(p *PlanState) PlanSnapshot {
	steps := make([]PlanStepInfo, len(p.steps))
	for i, s := range p.steps {
		steps[i] = planStepInfo(s)
	}
	return PlanSnapshot{PlanID: p.doc.PlanID, Revision: p.doc.Revision, Sequence: p.doc.Sequence, Goal: p.goal, Document: p.doc.Clone(), Steps: steps, PendingRevision: p.doc.PendingRevision.Clone()}
}
func planStepInfo(s planStep) PlanStepInfo {
	return PlanStepInfo{Name: s.name, Description: s.description, Details: s.details, Summary: s.summary, Files: append([]PlanFileChange(nil), s.files...), Outcome: s.outcome, Verify: s.verify, VerifyKind: s.verifyKind, Evidence: s.evidence, ParallelGroup: s.parallelGroup, Status: s.status}
}
func syncPlanSnapshotMetadata(s *PlanSnapshot) {
	s.Document.PlanID = s.PlanID
	s.Document.Revision = s.Revision
	s.Document.Sequence = s.Sequence
	s.Document.PendingRevision = s.PendingRevision.Clone()
}
func (r *PlanRuntime) Snapshot() PlanSnapshot {
	if r == nil {
		return PlanSnapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return PlanSnapshot{}
	}
	r.plan.mu.Lock()
	defer r.plan.mu.Unlock()
	return snapshotPlanLocked(r.plan)
}

// AllowInitialPlanReplacement authorizes a new announcement from Plan mode.
// Any prior lifecycle is replaceable; authorization is consumed on durable success.
func (r *PlanRuntime) AllowInitialPlanReplacement() error {
	if r == nil {
		return planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	r.allowInitialReplacement = true
	return nil
}

// Announce accepts a definition, never model-supplied lifecycle/identity metadata.
// Existing plans require a trusted authorization consumed only on durable success.
func (r *PlanRuntime) Announce(goal string, doc PlanDocumentInfo, steps []PlanStepInfo) error {
	if r == nil {
		return planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	doc = PlanDocumentInfo{Context: doc.Context, WhatNotToDo: doc.WhatNotToDo, Verification: doc.Verification, GraphAcceptance: doc.GraphAcceptance}
	next := PlanSnapshot{Goal: goal, Document: doc, Steps: clonePlanStepInfos(steps)}
	if strings.TrimSpace(goal) == "" {
		return planRuntimeError(PlanRuntimeInvalid, "goal is required")
	}
	if msg := ValidateAnnouncedPlan(doc, steps); msg != "" {
		return planRuntimeError(PlanRuntimeInvalid, msg)
	}
	if r.plan != nil && !r.allowInitialReplacement {
		return planRuntimeError(PlanRuntimeReplacementBlocked, "a new plan must be announced in Plan mode")
	}
	acceptance := next.Document.GraphAcceptance
	if acceptance == "" {
		acceptance = r.options.Acceptance
		next.Document.GraphAcceptance = acceptance
	}
	if msg := GraphPlanAnnounceMessage(acceptance != "", next.Document.Verification, acceptance); msg != "" {
		return planRuntimeError(PlanRuntimeInvalid, msg)
	}
	for i := range next.Steps {
		next.Steps[i].Status = "pending"
		next.Steps[i].Evidence = ""
	}
	next.PlanID = uuid.NewString()
	next.Revision = 1
	next.Sequence = 1
	syncPlanSnapshotMetadata(&next)
	if r.options.CommitSnapshot != nil {
		if err := r.options.CommitSnapshot(next.Clone()); err != nil {
			return planRuntimeError(PlanRuntimePersistFailed, err.Error())
		}
	}
	// Detach the previous state so late verification cannot mutate this plan.
	r.plan = NewPlanState(next.Goal, next.Document, next.Steps)
	r.inFlight = make(map[string]bool)
	r.allowInitialReplacement = false
	return nil
}

// CommitSnapshot is a compare-and-swap for trusted bootstrap/restore callers.
// Definition revisions must use Revise, not this persistence primitive.
func (r *PlanRuntime) CommitSnapshot(next PlanSnapshot) error {
	if r == nil {
		return planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		if strings.TrimSpace(next.Goal) == "" {
			return planRuntimeError(PlanRuntimeInvalid, "goal is required")
		}
		if msg := ValidateAnnouncedPlan(next.Document, next.Steps); msg != "" {
			return planRuntimeError(PlanRuntimeInvalid, msg)
		}
		if next.PendingRevision != nil || next.Document.PendingRevision != nil || next.Document.Results != "" {
			return planRuntimeError(PlanRuntimeInvalid, "initial announcement must be definition-only")
		}
		for i := range next.Steps {
			next.Steps[i].Status = "pending"
			next.Steps[i].Evidence = ""
		}
		if next.PlanID == "" {
			next.PlanID = uuid.NewString()
		}
		if next.Revision == 0 {
			next.Revision = 1
		}
		next.Sequence++
		syncPlanSnapshotMetadata(&next)
		if r.options.CommitSnapshot != nil {
			if err := r.options.CommitSnapshot(next.Clone()); err != nil {
				return planRuntimeError(PlanRuntimePersistFailed, err.Error())
			}
		}
		r.plan = NewPlanState(next.Goal, next.Document, next.Steps)
		return nil
	}
	r.plan.mu.Lock()
	defer r.plan.mu.Unlock()
	current := snapshotPlanLocked(r.plan)
	if next.PlanID != current.PlanID || next.Revision != current.Revision || next.Sequence != current.Sequence {
		return planRuntimeError(PlanRuntimeStale, "snapshot changed before commit")
	}
	return r.commitLocked(next)
}

// commitLocked publishes only after durable persistence succeeds. Both locks
// are held; the callback receives its own deep copy and cannot mutate next.
func (r *PlanRuntime) commitLocked(next PlanSnapshot) error {
	if next.PlanID == "" {
		next.PlanID = uuid.NewString()
	}
	if next.Revision == 0 {
		next.Revision = 1
	}
	next.Sequence++
	syncPlanSnapshotMetadata(&next)
	if r.options.CommitSnapshot != nil {
		if err := r.options.CommitSnapshot(next.Clone()); err != nil {
			return planRuntimeError(PlanRuntimePersistFailed, err.Error())
		}
	}
	replacement := NewPlanState(next.Goal, next.Document, next.Steps)
	oldSteps := map[string]planStep{}
	for _, step := range r.plan.steps {
		oldSteps[step.name] = step
	}
	for i := range replacement.steps {
		step := &replacement.steps[i]
		if old, ok := oldSteps[step.name]; ok {
			step.epoch = old.epoch
			if old.status != step.status || old.evidence != step.evidence || !implementationEqual(planStepInfo(old).Definition(), planStepInfo(*step).Definition()) {
				step.epoch++
			}
		}
	}
	r.plan.goal = replacement.goal
	r.plan.doc = replacement.doc
	r.plan.steps = replacement.steps
	r.plan.generation++
	return nil
}

// Bootstrap persists identity for a legacy plan once, without changing its
// definition. Initial announcements may instead CommitSnapshot into an empty
// runtime, then SetApproved after the trusted user approval.
func (r *PlanRuntime) Bootstrap() error {
	if r == nil {
		return planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	r.plan.mu.Lock()
	defer r.plan.mu.Unlock()
	s := snapshotPlanLocked(r.plan)
	if s.PlanID != "" && s.Revision > 0 && s.PendingRevision == nil {
		return nil
	}
	// Pending candidates from the retired revision-approval workflow are obsolete.
	// Discard them durably without applying their definitions or granting approval.
	if s.PendingRevision != nil {
		s.PendingRevision = nil
		s.Document.RevisionNote = "Obsolete pending revision discarded; submit a new revision if still needed."
	}
	return r.commitLocked(s)
}
func (r *PlanRuntime) SetApproved() error {
	if r == nil {
		return planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	r.plan.mu.Lock()
	defer r.plan.mu.Unlock()
	s := snapshotPlanLocked(r.plan)
	if s.Document.Lifecycle == PlanLifecycleApproved {
		return nil
	}
	s.Document.Lifecycle = PlanLifecycleApproved
	return r.commitLocked(s)
}
func runtimeEditable(s PlanSnapshot) error {
	if len(s.Steps) == 0 {
		return planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	if strings.TrimSpace(s.Document.Results) != "" {
		return planRuntimeError(PlanRuntimeCompleted, "completed plans cannot be revised")
	}
	return nil
}

func (r *PlanRuntime) Revise(req PlanRevisionRequest) (PlanRevisionResult, error) {
	if r == nil {
		return PlanRevisionResult{}, planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return PlanRevisionResult{}, planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	r.plan.mu.Lock()
	defer r.plan.mu.Unlock()
	s := snapshotPlanLocked(r.plan)
	if err := runtimeEditable(s); err != nil {
		return PlanRevisionResult{}, err
	}
	if req.PlanID != s.PlanID || req.PlanID == "" || req.BaseRevision != s.Revision {
		return PlanRevisionResult{}, planRuntimeError(PlanRuntimeStale, "plan identity or base revision is stale")
	}
	candidate, err := BuildPlanRevisionCandidate(s, req, r.options.Workspace, r.options.Acceptance)
	if err != nil {
		return PlanRevisionResult{}, planRuntimeError(PlanRuntimeInvalid, err.Error())
	}
	if candidate.Classification == PlanRevisionNoop {
		return PlanRevisionResult{Kind: PlanRevisionResultNoop, Snapshot: s}, nil
	}
	affected := revisionAffectedSteps(s.Definition(), candidate.Definition)
	if err := r.checkBusyLocked(affected); err != nil {
		return PlanRevisionResult{}, err
	}
	s = applyRevisionDefinition(s, candidate.Definition, affected)
	s.Document.RevisionNote = req.Reason
	if err := r.commitLocked(s); err != nil {
		return PlanRevisionResult{}, err
	}
	return PlanRevisionResult{Kind: PlanRevisionResultApplied, Snapshot: snapshotPlanLocked(r.plan)}, nil
}

// ResolveRevision retains wire compatibility but never applies obsolete candidates.
func (r *PlanRuntime) ResolveRevision(planID, proposalID string, baseRevision int64, decision PlanRevisionDecision) (PlanRevisionResult, error) {
	if r == nil || len(r.Snapshot().Steps) == 0 {
		return PlanRevisionResult{}, planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	return PlanRevisionResult{}, planRuntimeError(PlanRuntimeRevisionObsolete, "revision decisions are obsolete; valid Normal-mode revisions apply immediately")
}

func implementationEqual(a, b PlanStepDefinition) bool {
	a.Description = ""
	b.Description = ""
	a.Summary = ""
	b.Summary = ""
	return reflect.DeepEqual(a, b)
}

// Conservative dependency closure: edits invalidate later waves, not independent
// siblings in the same wave. A structure edit invalidates from its first changed
// position; document/goal edits invalidate every phase.
func revisionAffectedSteps(old, next PlanDefinition) map[string]bool {
	affected := map[string]bool{}
	if old.Goal != next.Goal || old.Document != next.Document {
		for _, s := range old.Steps {
			affected[s.Name] = true
		}
		for _, s := range next.Steps {
			affected[s.Name] = true
		}
		return affected
	}
	oldByName := map[string]PlanStepDefinition{}
	for _, s := range old.Steps {
		oldByName[s.Name] = s
	}
	structuralAt := -1
	n := len(old.Steps)
	if len(next.Steps) > n {
		n = len(next.Steps)
	}
	for i := 0; i < n; i++ {
		if i >= len(old.Steps) || i >= len(next.Steps) || old.Steps[i].Name != next.Steps[i].Name || old.Steps[i].ParallelGroup != next.Steps[i].ParallelGroup {
			structuralAt = i
			break
		}
	}
	for i, s := range next.Steps {
		previous, ok := oldByName[s.Name]
		if !ok || !implementationEqual(previous, s) {
			affected[s.Name] = true
			for _, later := range next.Steps[i+1:] {
				if s.ParallelGroup == "" || s.ParallelGroup != later.ParallelGroup {
					affected[later.Name] = true
				}
			}
		}
	}
	if structuralAt >= 0 {
		for _, s := range old.Steps[minimum(structuralAt, len(old.Steps)):] {
			affected[s.Name] = true
		}
		for _, s := range next.Steps[minimum(structuralAt, len(next.Steps)):] {
			affected[s.Name] = true
		}
	}
	return affected
}
func minimum(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func applyRevisionDefinition(s PlanSnapshot, d PlanDefinition, affected map[string]bool) PlanSnapshot {
	old := map[string]PlanStepInfo{}
	for _, step := range s.Steps {
		old[step.Name] = step
	}
	s.Goal = d.Goal
	s.Document.Context = d.Document.Context
	s.Document.WhatNotToDo = d.Document.WhatNotToDo
	if s.Document.GraphAcceptance != "" && s.Document.Verification != d.Document.Verification {
		s.Document.GraphAcceptance = d.Document.Verification
	}
	s.Document.Verification = d.Document.Verification
	s.Document.Results = ""
	s.Steps = make([]PlanStepInfo, len(d.Steps))
	for i, definition := range d.Steps {
		step := definition.StepInfo()
		step.Status = "pending"
		if previous, ok := old[step.Name]; ok && !affected[step.Name] {
			step.Status = previous.Status
			step.Evidence = previous.Evidence
		}
		s.Steps[i] = step
	}
	s.Revision++
	s.PendingRevision = nil
	return s
}
func (r *PlanRuntime) checkBusyLocked(affected map[string]bool) error {
	for _, s := range r.plan.steps {
		if !affected[s.name] {
			continue
		}
		if s.status == "running" || r.inFlight[s.name] || r.plan.verifying[s.name] {
			return planRuntimeError(PlanRuntimeBusy, "affected phase is running: "+s.name)
		}
		key := strings.ToLower(s.name)
		for task := range r.plan.taskRegistry[key] {
			if !r.plan.completedTasks[key][task] {
				return planRuntimeError(PlanRuntimeBusy, "affected delegate is running: "+s.name)
			}
		}
	}
	return nil
}

// AdvanceOnToolStart starts the next eligible phase only after initial approval.
// Research calls against an unapproved plan are intentionally a no-op.
func (r *PlanRuntime) AdvanceOnToolStart() (string, error) {
	if r == nil {
		return "", nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return "", nil
	}
	r.plan.mu.Lock()
	defer r.plan.mu.Unlock()
	s := snapshotPlanLocked(r.plan)
	if s.Document.Lifecycle != PlanLifecycleApproved || s.Document.Results != "" {
		return "", nil
	}
	for _, step := range s.Steps {
		if step.Status == "running" {
			return "", nil
		}
	}
	for i, step := range s.Steps {
		if step.Status != "pending" {
			continue
		}
		if r.plan.canStartLocked(i) != "" {
			return "", nil
		}
		s.Steps[i].Status = "running"
		if err := r.commitLocked(s); err != nil {
			return "", err
		}
		return step.Name, nil
	}
	return "", nil
}

// RegisterDelegate registers one delegate and durably publishes its phase start.
// It returns a step name only for a new running transition (legacy event semantics).
func (r *PlanRuntime) RegisterDelegate(stepName, taskName string) (string, error) {
	if r == nil {
		return "", planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return "", planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	p := r.plan
	p.mu.Lock()
	defer p.mu.Unlock()
	s := snapshotPlanLocked(p)
	if err := runtimeEditable(s); err != nil {
		return "", err
	}
	if s.Document.Lifecycle != PlanLifecycleApproved {
		return "", planRuntimeError(PlanRuntimeUnapproved, "the plan is not approved for execution")
	}
	idx := p.findStepLocked(stepName)
	if idx < 0 {
		return "", nil
	}
	sn, tn := strings.ToLower(p.steps[idx].name), strings.ToLower(taskName)
	if p.taskRegistry[sn][tn] {
		return "", nil
	}
	started := ""
	if p.steps[idx].status == "pending" {
		if reason := p.canStartLocked(idx); reason != "" {
			return "", planRuntimeError(reason, "an earlier phase must complete before this phase")
		}
		s.Steps[idx].Status = "running"
		started = p.steps[idx].name
	}
	if err := r.commitLocked(s); err != nil {
		return "", err
	}
	if p.taskRegistry[sn] == nil {
		p.taskRegistry[sn] = make(map[string]bool)
	}
	p.taskRegistry[sn][tn] = true
	return started, nil
}

// FinishDelegate records task finish without completing its phase. A returned name
// means all registered tasks are done; the caller must still run UpdateStep verify.
// Failed persistence never publishes a task finish or changes delegate counters.
func (r *PlanRuntime) FinishDelegate(stepName, taskName string) (string, error) {
	if r == nil {
		return "", planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return "", planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	p := r.plan
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.doc.Lifecycle != PlanLifecycleApproved {
		return "", planRuntimeError(PlanRuntimeUnapproved, "the plan is not approved for execution")
	}
	idx := p.findStepLocked(stepName)
	if idx < 0 || p.steps[idx].status != "running" {
		return "", nil
	}
	sn, tn := strings.ToLower(p.steps[idx].name), strings.ToLower(taskName)
	if !p.taskRegistry[sn][tn] {
		return "", nil // Late completion from a replaced plan is not registered here.
	}
	if p.completedTasks[sn][tn] {
		return "", nil
	}
	if err := r.commitLocked(snapshotPlanLocked(p)); err != nil {
		return "", err
	}
	if p.completedTasks[sn] == nil {
		p.completedTasks[sn] = make(map[string]bool)
	}
	p.completedTasks[sn][tn] = true
	if len(p.taskRegistry[sn]) == 0 {
		return "", nil
	}
	for task := range p.taskRegistry[sn] {
		if !p.completedTasks[sn][task] {
			return "", nil
		}
	}
	return p.steps[idx].name, nil
}

// UpdateStep verifies outside both locks, then checks the captured implementation
// and step state before publishing evidence. Independent-wave commits do not invalidate work already in flight.
func (r *PlanRuntime) UpdateStep(step, status string) PlanStepApplyResult {
	fail := func(code, message string) PlanStepApplyResult {
		return PlanStepApplyResult{Name: step, Code: code, Message: message}
	}
	if r == nil {
		return fail(PlanRuntimeNoPlan, "no active plan")
	}
	r.mu.Lock()
	if r.plan == nil {
		r.mu.Unlock()
		return fail(PlanRuntimeNoPlan, "no active plan")
	}
	p := r.plan
	p.mu.Lock()
	unlock := func() { p.mu.Unlock(); r.mu.Unlock() }
	s := snapshotPlanLocked(p)
	if err := runtimeEditable(s); err != nil {
		unlock()
		return fail(PlanRuntimeErrorCode(err), err.Error())
	}
	idx := p.findStepExactLocked(step)
	if idx < 0 {
		unlock()
		return fail(PlanStepNotFound, "step not found")
	}
	info := s.Steps[idx]
	step = info.Name
	if r.inFlight[step] || p.verifying[step] {
		unlock()
		return fail(PlanRuntimeBusy, "phase verification is already running")
	}
	want := normalizePlanStatus(status)
	if want == "running" || want == "complete" {
		if s.Document.Lifecycle != PlanLifecycleApproved {
			unlock()
			return fail(PlanRuntimeUnapproved, "the plan is not approved for execution")
		}
		if reason := p.canStartLocked(idx); reason != "" {
			unlock()
			return fail(reason, "an earlier phase must complete before this phase")
		}
	}
	if want != "complete" {
		if info.Status == want {
			unlock()
			return PlanStepApplyResult{Name: step, Applied: want, Code: PlanStepOK}
		}
		s.Steps[idx].Status = want
		s.Steps[idx].Evidence = ""
		err := r.commitLocked(s)
		if err == nil {
			p.manuallyTracked = true
		}
		unlock()
		if err != nil {
			return fail(PlanRuntimeErrorCode(err), err.Error())
		}
		return PlanStepApplyResult{Name: step, Applied: want, Code: PlanStepOK}
	}
	if info.Status == "complete" && passingPlanEvidence(info.Evidence) {
		unlock()
		return PlanStepApplyResult{Name: step, Applied: "complete", Code: PlanStepOK}
	}
	if strings.TrimSpace(info.Verify) == "" {
		unlock()
		return fail(PlanStepNoVerify, "this phase has no verify command; revise the plan")
	}
	r.inFlight[step] = true
	capturedID := s.PlanID
	capturedEpoch := p.steps[idx].epoch
	verify := r.options.Verify
	workspace := r.options.Workspace
	unlock()
	var exit int
	var output string
	var runErr error
	if verify != nil {
		exit, output, runErr = verify(info.Verify)
	} else {
		exit, output, runErr = DefaultPlanVerify(workspace, info.Verify)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.plan == p {
		delete(r.inFlight, step)
	}
	current := snapshotPlanLocked(p)
	idx = p.findStepExactLocked(step)
	if r.plan != p || current.PlanID != capturedID || idx < 0 || p.steps[idx].epoch != capturedEpoch || !implementationEqual(current.Steps[idx].Definition(), info.Definition()) || current.Steps[idx].Status != info.Status || current.Steps[idx].Evidence != info.Evidence || current.Document.Results != "" {
		return fail(PlanRuntimeStale, "phase changed while verification was running; evidence discarded")
	}
	evidence := formatPlanEvidence(exit, output)
	applied := "complete"
	code := PlanStepOK
	message := ""
	if runErr != nil || exit != 0 {
		applied = "failed"
		code = PlanStepVerifyFailed
		message = fmt.Sprintf("verify command exited %d", exit)
		if runErr != nil {
			evidence += "; " + runErr.Error()
			message = runErr.Error()
		}
	}
	current.Steps[idx].Evidence = evidence
	current.Steps[idx].Status = applied
	if err := r.commitLocked(current); err != nil {
		return fail(PlanRuntimeErrorCode(err), err.Error())
	}
	p.manuallyTracked = true
	return PlanStepApplyResult{Name: step, Applied: applied, Code: code, Message: message, Output: output}
}
func passingPlanEvidence(evidence string) bool {
	return evidence == "exit 0" || strings.HasPrefix(evidence, "exit 0;")
}

func (r *PlanRuntime) Complete(outcomeObserved, unverified string) PlanCompletionResult {
	fail := func(code, message string) PlanCompletionResult {
		return PlanCompletionResult{Code: code, Message: message}
	}
	if r == nil {
		return fail(PlanCompletionNoPlan, "no active plan")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return fail(PlanCompletionNoPlan, "no active plan")
	}
	r.plan.mu.Lock()
	defer r.plan.mu.Unlock()
	s := snapshotPlanLocked(r.plan)
	if s.Document.Lifecycle != PlanLifecycleApproved {
		return fail(PlanCompletionIncomplete, "the plan is not approved for execution")
	}
	if strings.TrimSpace(s.Document.Results) != "" {
		return fail(PlanRuntimeCompleted, "completion was already recorded")
	}
	if len(s.Steps) == 0 || len(r.inFlight) > 0 {
		return fail(PlanCompletionIncomplete, "not every phase has finished verification")
	}
	var lines []string
	for _, step := range s.Steps {
		key := strings.ToLower(step.Name)
		for task := range r.plan.taskRegistry[key] {
			if !r.plan.completedTasks[key][task] {
				return fail(PlanRuntimeBusy, "a phase delegate is still running")
			}
		}
		if step.Status != "complete" || strings.TrimSpace(step.Verify) == "" || !passingPlanEvidence(step.Evidence) {
			return fail(PlanCompletionIncomplete, "every phase requires current passing evidence")
		}
		lines = append(lines, fmt.Sprintf("%s\n$ %s\n%s", step.Name, step.Verify, step.Evidence))
	}
	if strings.TrimSpace(unverified) == "" {
		unverified = "(none stated)"
	}
	log := strings.Join(lines, "\n")
	s.Document.Results = fmt.Sprintf("Outcome observed: %s\n\nPhase evidence:\n%s\n\nUnverified: %s", strings.TrimSpace(outcomeObserved), log, strings.TrimSpace(unverified))
	if err := r.commitLocked(s); err != nil {
		return fail(PlanRuntimeErrorCode(err), err.Error())
	}
	return PlanCompletionResult{Code: PlanCompletionOK, Log: log}
}

// AtomicPlanSnapshotFile writes a same-directory temporary file and renames it;
// failure leaves the previous authoritative document untouched.
func AtomicPlanSnapshotFile(path string) CommitPlanSnapshot {
	return func(snapshot PlanSnapshot) error {
		if path == "" {
			return fmt.Errorf("empty plan persistence path")
		}
		syncPlanSnapshotMetadata(&snapshot)
		content := NewPlanState(snapshot.Goal, snapshot.Document, snapshot.Steps).RenderMarkdown()
		f, err := os.CreateTemp(filepath.Dir(path), ".plan-*.tmp")
		if err != nil {
			return err
		}
		temp := f.Name()
		defer os.Remove(temp)
		if err = f.Chmod(0644); err == nil {
			_, err = f.WriteString(content)
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return os.Rename(temp, path)
	}
}
