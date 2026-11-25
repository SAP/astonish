package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/google/uuid"
)

// PlanPreparedKind identifies an operation of the existing plan runtime.
type PlanPreparedKind string

const (
	PlanPreparedSnapshot PlanPreparedKind = "snapshot"
	PlanPreparedAnnounce PlanPreparedKind = "announce"
	PlanPreparedVerify   PlanPreparedKind = "verify"
)

// PlanPreparedStage describes which effects may already have happened. A restored
// verifying record is uncertain and must never automatically execute again.
type PlanPreparedStage string

const (
	PlanPreparedReady     PlanPreparedStage = "ready"
	PlanPreparedVerifying PlanPreparedStage = "verifying"
	PlanPreparedSettled   PlanPreparedStage = "settled"
)

const PlanRuntimeVerificationUncertain = "verification_uncertain"

// PlanSettledVerification is trusted verifier evidence, including failures. It is
// not model input and does not confer permission to publish a plan.
type PlanSettledVerification struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
}

// PlanPreparedOperation is a versioned, serializable retry record. Store it only
// in trusted session-scoped storage, never in editable PLAN.md or model input.
// Base is captured authority; Candidate already has its final identity/sequence.
// A nil Candidate means verification has not produced publishable evidence.
// Restore/resume callers must independently authorize the operation in their
// current scope. The engine deliberately does not implement that policy.
type PlanPreparedOperation struct {
	Version       int                      `json:"version"`
	ID            string                   `json:"id"`
	Kind          PlanPreparedKind         `json:"kind"`
	Stage         PlanPreparedStage        `json:"stage"`
	RequestDigest string                   `json:"request_digest"`
	Base          PlanSnapshot             `json:"base"`
	Candidate     *PlanSnapshot            `json:"candidate,omitempty"`
	Step          string                   `json:"step,omitempty"`
	Verification  *PlanSettledVerification `json:"verification,omitempty"`
	Result        PlanStepApplyResult      `json:"result"`
}

func (p PlanPreparedOperation) Clone() PlanPreparedOperation {
	p.Base = p.Base.Clone()
	if p.Candidate != nil {
		s := p.Candidate.Clone()
		p.Candidate = &s
	}
	if p.Verification != nil {
		v := *p.Verification
		p.Verification = &v
	}
	return p
}

// PreparePlanOperation durably records/admit an operation before its next effect.
// It runs under runtime/plan locks, receives a deep copy, and must not reenter
// either lock. Return an error to suspend. Ready precedes verifier dispatch;
// Verifying is the durable no-replay intent; Settled precedes snapshot mirroring.
// A failed settled save retains evidence in memory; after process loss a durable
// Verifying record remains uncertain, not permission to rerun the command.
type PreparePlanOperation func(PlanPreparedOperation) error

func preparedDigest(value any) string {
	b, _ := json.Marshal(value) // all record fields are concrete JSON-compatible values
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (r *PlanRuntime) preparedBaseLocked() PlanSnapshot {
	if r.plan == nil {
		return PlanSnapshot{}
	}
	return snapshotPlanLocked(r.plan)
}
func (r *PlanRuntime) retainPreparedLocked(op PlanPreparedOperation) {
	if r.prepared == nil {
		r.prepared = make(map[string]PlanPreparedOperation)
	}
	r.prepared[op.ID] = op.Clone()
}
func (r *PlanRuntime) notifyPreparedLocked(op PlanPreparedOperation) error {
	r.retainPreparedLocked(op)
	if r.options.PrepareOperation != nil {
		if err := r.options.PrepareOperation(op.Clone()); err != nil {
			return planRuntimeError(PlanRuntimePersistFailed, err.Error())
		}
	}
	return nil
}

// PreparedOperations returns independent retry records in stable ID order.
// Successful publications remove their pending record; durable owners may retain
// a receipt and resume it again (an identical current candidate is a no-op).
func (r *PlanRuntime) PreparedOperations() []PlanPreparedOperation {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]PlanPreparedOperation, 0, len(r.prepared))
	for _, op := range r.prepared {
		result = append(result, op.Clone())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *PlanRuntime) prepareSnapshotLocked(kind PlanPreparedKind, next PlanSnapshot) PlanPreparedOperation {
	base := r.preparedBaseLocked()
	digest := preparedDigest(next)
	for _, op := range r.prepared {
		if op.Kind == kind && op.RequestDigest == digest && preparedDigest(op.Base) == preparedDigest(base) {
			return op.Clone()
		}
	}
	if kind == PlanPreparedAnnounce {
		next.PlanID = uuid.NewString()
		next.Revision = 1
		next.Sequence = 1
	} else {
		if next.PlanID == "" {
			next.PlanID = uuid.NewString()
		}
		if next.Revision == 0 {
			next.Revision = 1
		}
		next.Sequence++
	}
	syncPlanSnapshotMetadata(&next)
	op := PlanPreparedOperation{Version: 1, ID: uuid.NewString(), Kind: kind, Stage: PlanPreparedSettled, RequestDigest: digest, Base: base, Candidate: &next}
	r.retainPreparedLocked(op)
	return op
}

func (r *PlanRuntime) publishPreparedLocked(op PlanPreparedOperation) error {
	if op.Stage != PlanPreparedSettled || op.Candidate == nil {
		return planRuntimeError(PlanRuntimeInvalid, "operation has no settled candidate")
	}
	current := r.preparedBaseLocked()
	if preparedDigest(current) == preparedDigest(*op.Candidate) {
		delete(r.prepared, op.ID)
		return nil
	}
	if preparedDigest(current) != preparedDigest(op.Base) {
		return planRuntimeError(PlanRuntimeStale, "prepared operation base changed")
	}
	if err := r.notifyPreparedLocked(op); err != nil {
		return err
	}
	next := op.Candidate.Clone()
	if r.options.CommitSnapshot != nil {
		if err := r.options.CommitSnapshot(next.Clone()); err != nil {
			return planRuntimeError(PlanRuntimePersistFailed, err.Error())
		}
	}
	if r.plan == nil || op.Kind == PlanPreparedAnnounce {
		r.plan = NewPlanState(next.Goal, next.Document, next.Steps)
		r.inFlight = make(map[string]bool)
		r.allowInitialReplacement = false
	} else {
		r.applySnapshotLocked(next)
	}
	if op.Kind == PlanPreparedVerify {
		r.plan.manuallyTracked = true
	}
	delete(r.prepared, op.ID)
	return nil
}

func preparedStageOrder(stage PlanPreparedStage) int {
	switch stage {
	case PlanPreparedReady:
		return 1
	case PlanPreparedVerifying:
		return 2
	case PlanPreparedSettled:
		return 3
	}
	return 0
}

func validatePreparedOperation(op PlanPreparedOperation) error {
	if op.Version != 1 || op.ID == "" || op.RequestDigest == "" {
		return planRuntimeError(PlanRuntimeInvalid, "invalid prepared operation identity/version")
	}
	switch op.Kind {
	case PlanPreparedSnapshot, PlanPreparedAnnounce:
		if op.Stage != PlanPreparedSettled || op.Candidate == nil || op.Verification != nil {
			return planRuntimeError(PlanRuntimeInvalid, "invalid prepared snapshot")
		}
	case PlanPreparedVerify:
		if op.Step == "" {
			return planRuntimeError(PlanRuntimeInvalid, "missing prepared phase")
		}
		switch op.Stage {
		case PlanPreparedReady, PlanPreparedVerifying:
			if op.Candidate != nil || op.Verification != nil {
				return planRuntimeError(PlanRuntimeInvalid, "unsettled verification has evidence")
			}
		case PlanPreparedSettled:
			if op.Candidate == nil || op.Verification == nil {
				return planRuntimeError(PlanRuntimeInvalid, "settled verification lacks candidate/evidence")
			}
		default:
			return planRuntimeError(PlanRuntimeInvalid, "invalid prepared stage")
		}
	default:
		return planRuntimeError(PlanRuntimeInvalid, "invalid prepared operation kind")
	}
	return nil
}

// ResumePreparedOperation accepts only a trusted record previously produced by
// this runtime. It publishes settled evidence without invoking Verify, dispatches
// a Ready verification through UpdateStep, and refuses uncertain Verifying work.
// The exact Base must still be current; an already-current identical Candidate
// is idempotent and does not invoke either persistence callback again.
func (r *PlanRuntime) ResumePreparedOperation(op PlanPreparedOperation) (PlanStepApplyResult, error) {
	if r == nil {
		return PlanStepApplyResult{}, planRuntimeError(PlanRuntimeNoPlan, "no active plan")
	}
	if err := validatePreparedOperation(op); err != nil {
		return PlanStepApplyResult{}, err
	}
	op = op.Clone()
	r.mu.Lock()
	p := r.plan
	if p != nil {
		p.mu.Lock()
	}
	unlock := func() {
		if p != nil {
			p.mu.Unlock()
		}
		r.mu.Unlock()
	}
	if existing, ok := r.prepared[op.ID]; ok {
		// Never downgrade a locally settled result using an older durable intent,
		// but accept a newer trusted record when recovery previously loaded intent.
		if existing.Kind != op.Kind || existing.RequestDigest != op.RequestDigest {
			unlock()
			return PlanStepApplyResult{}, planRuntimeError(PlanRuntimeInvalid, "conflicting prepared identity")
		}
		if preparedStageOrder(existing.Stage) >= preparedStageOrder(op.Stage) {
			if existing.Stage == op.Stage && preparedDigest(existing) != preparedDigest(op) {
				unlock()
				return PlanStepApplyResult{}, planRuntimeError(PlanRuntimeInvalid, "conflicting prepared record")
			}
			op = existing.Clone()
		}
	}
	if op.Kind == PlanPreparedVerify && r.inFlight[op.Step] {
		unlock()
		return PlanStepApplyResult{}, planRuntimeError(PlanRuntimeBusy, "phase verification is already running")
	}
	if op.Stage == PlanPreparedSettled {
		err := r.publishPreparedLocked(op)
		unlock()
		return op.Result, err
	}
	if preparedDigest(r.preparedBaseLocked()) != preparedDigest(op.Base) {
		unlock()
		return PlanStepApplyResult{}, planRuntimeError(PlanRuntimeStale, "prepared operation base changed")
	}
	r.retainPreparedLocked(op)
	if op.Stage == PlanPreparedVerifying {
		unlock()
		return PlanStepApplyResult{}, planRuntimeError(PlanRuntimeVerificationUncertain, "verification outcome is unknown; reconcile trusted evidence, do not replay")
	}
	unlock()
	result := r.UpdateStep(op.Step, "complete")
	if result.Code != PlanStepOK && result.Code != PlanStepVerifyFailed {
		return result, planRuntimeError(result.Code, result.Message)
	}
	return result, nil
}
