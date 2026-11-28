package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Progress checkpoints are PRIVATE repository envelopes, never owner approval
// payloads, public transcripts, model-writable identity, or authority grants.
const (
	BotProgressSchemaVersion      = 1
	BotProgressMaxBytes           = 64 << 10
	BotProgressMaxSummaryBytes    = 4096
	BotProgressMaxContextRefBytes = 1024
)

type BotProgressPhase string

const (
	BotProgressWorking              BotProgressPhase = "working"
	BotProgressEvaluating           BotProgressPhase = "evaluating"
	BotProgressReplanning           BotProgressPhase = "replanning"
	BotProgressSynthesizing         BotProgressPhase = "synthesizing"
	BotProgressCheckpointing        BotProgressPhase = "checkpointing"
	BotProgressWaitingUser          BotProgressPhase = "waiting_user"
	BotProgressWaitingBudget        BotProgressPhase = "waiting_budget"
	BotProgressWaitingAuthorization BotProgressPhase = "waiting_authorization"
	BotProgressSuspended            BotProgressPhase = "suspended"
	BotProgressReconciling          BotProgressPhase = "reconciling"
)

func (p BotProgressPhase) Valid() bool {
	switch p {
	case BotProgressWorking, BotProgressEvaluating, BotProgressReplanning, BotProgressSynthesizing, BotProgressCheckpointing, BotProgressWaitingUser, BotProgressWaitingBudget, BotProgressWaitingAuthorization, BotProgressSuspended, BotProgressReconciling:
		return true
	}
	return false
}

type BotProgressDecision string

const (
	BotProgressContinue   BotProgressDecision = "continue"
	BotProgressReplan     BotProgressDecision = "replan"
	BotProgressSynthesize BotProgressDecision = "synthesize"
	BotProgressAskUser    BotProgressDecision = "ask_user"
)

func (d BotProgressDecision) Valid() bool {
	switch d {
	case BotProgressContinue, BotProgressReplan, BotProgressSynthesize, BotProgressAskUser:
		return true
	}
	return false
}

type BotProgressReason string

const (
	BotProgressReasonCadence           BotProgressReason = "cadence"
	BotProgressReasonRecovery          BotProgressReason = "recovery"
	BotProgressReasonToolFailure       BotProgressReason = "tool_failure"
	BotProgressReasonContextPressure   BotProgressReason = "context_pressure"
	BotProgressReasonReadyToSynthesize BotProgressReason = "ready_to_synthesize"
	BotProgressReasonOwnerInput        BotProgressReason = "owner_input"
	BotProgressReasonBudget            BotProgressReason = "budget"
	BotProgressReasonAuthorization     BotProgressReason = "authorization"
	BotProgressReasonPaused            BotProgressReason = "paused"
	BotProgressReasonUncertainEffect   BotProgressReason = "uncertain_effect"
)

func (r BotProgressReason) Valid() bool {
	switch r {
	case BotProgressReasonCadence, BotProgressReasonRecovery, BotProgressReasonToolFailure, BotProgressReasonContextPressure, BotProgressReasonReadyToSynthesize, BotProgressReasonOwnerInput, BotProgressReasonBudget, BotProgressReasonAuthorization, BotProgressReasonPaused, BotProgressReasonUncertainEffect:
		return true
	}
	return false
}

// Binding comes from authenticated scoped repository state, not evaluation output.
type BotProgressBinding struct {
	BotID        BotID       `json:"bot_id"`
	RunID        RunID       `json:"run_id"`
	SessionID    uuid.UUID   `json:"session_id"`
	Version      int64       `json:"version"`
	Task         *BotTaskRef `json:"task,omitempty"`
	WorkRevision int64       `json:"work_revision"`
}

func (b BotProgressBinding) Validate() error {
	if b.BotID == uuid.Nil || b.RunID == uuid.Nil || b.SessionID == uuid.Nil || b.Version < 1 || b.WorkRevision < 1 {
		return ErrBotInvalid
	}
	if b.Task != nil {
		return b.Task.Validate()
	}
	return nil
}
func (b BotProgressBinding) Matches(other BotProgressBinding) bool {
	return reflect.DeepEqual(b, other)
}

type BotProgressEvaluation struct {
	At       time.Time           `json:"at"`
	Decision BotProgressDecision `json:"decision"`
	Reason   BotProgressReason   `json:"reason"`
	Summary  string              `json:"summary"`
}

func (e *BotProgressEvaluation) Validate() error {
	if e == nil {
		return nil
	}
	if e.At.IsZero() || !e.Decision.Valid() || !e.Reason.Valid() || strings.TrimSpace(e.Summary) == "" || len(e.Summary) > BotProgressMaxSummaryBytes {
		return ErrBotInvalid
	}
	return nil
}

type BotProgressCheckpointKind string

const BotProgressCheckpointV1 BotProgressCheckpointKind = "bot_progress_v1"

// ContextRef addresses bounded private persisted context; Digest authenticates
// those bytes on restore. It is never an arbitrary filesystem path to open.
type BotProgressCheckpoint struct {
	Kind                 BotProgressCheckpointKind `json:"kind"`
	SchemaVersion        int                       `json:"schema_version"`
	Binding              BotProgressBinding        `json:"binding"`
	Revision             int64                     `json:"revision"`
	TurnID               uuid.UUID                 `json:"turn_id"`
	EventCursor          int64                     `json:"event_cursor"`
	TranscriptHighWater  int64                     `json:"transcript_high_water"`
	ContextRef           string                    `json:"context_ref"`
	ContextDigest        string                    `json:"context_digest"`
	LastEvaluation       *BotProgressEvaluation    `json:"last_evaluation,omitempty"`
	NextEligibleAt       time.Time                 `json:"next_eligible_at"`
	Usage                BotUsage                  `json:"usage"`
	DispatchLedgerCursor int64                     `json:"dispatch_ledger_cursor"`
}

func (c *BotProgressCheckpoint) Clone() *BotProgressCheckpoint {
	if c == nil {
		return nil
	}
	out := *c
	out.Binding.Task = c.Binding.Task.Clone()
	if c.LastEvaluation != nil {
		e := *c.LastEvaluation
		out.LastEvaluation = &e
	}
	if c.Usage.CostMicros != nil {
		v := *c.Usage.CostMicros
		out.Usage.CostMicros = &v
	}
	return &out
}
func (c *BotProgressCheckpoint) Validate() error {
	if c == nil || c.Kind != BotProgressCheckpointV1 || c.SchemaVersion != BotProgressSchemaVersion {
		return ErrBotUnsupported
	}
	if c.Binding.Validate() != nil || c.Revision < 1 || c.TurnID == uuid.Nil || c.EventCursor < 0 || c.TranscriptHighWater < 0 || c.DispatchLedgerCursor < 0 || strings.TrimSpace(c.ContextRef) == "" || len(c.ContextRef) > BotProgressMaxContextRefBytes || !skillDigestValid(c.ContextDigest) || c.NextEligibleAt.IsZero() || c.LastEvaluation.Validate() != nil || !botProgressUsageValid(c.Usage) {
		return ErrBotInvalid
	}
	if c.LastEvaluation != nil && c.NextEligibleAt.Before(c.LastEvaluation.At) {
		return ErrBotInvalid
	}
	return botProgressBounded(c)
}
func botProgressBounded(v any) error {
	data, err := json.Marshal(v)
	if err != nil || len(data) > BotProgressMaxBytes {
		return ErrBotInvalid
	}
	return nil
}
func botProgressUsageValid(u BotUsage) bool {
	return u.RuntimeNanos >= 0 && u.ToolCalls >= 0 && u.InputTokens >= 0 && u.OutputTokens >= 0 && u.InputTokens <= (1<<63-1)-u.OutputTokens && (u.CostMicros == nil || *u.CostMicros >= 0)
}
func botProgressUsageMonotonic(next, prev BotUsage) bool {
	return botProgressUsageValid(next) && next.RuntimeNanos >= prev.RuntimeNanos && next.ToolCalls >= prev.ToolCalls && next.InputTokens >= prev.InputTokens && next.OutputTokens >= prev.OutputTokens && (prev.CostMicros == nil || next.CostMicros != nil && *next.CostMicros >= *prev.CostMicros)
}

// ValidateBinding does not authenticate. The caller supplies a freshly loaded
// trusted binding and still checks membership, lease, policy and revocation.
func (c *BotProgressCheckpoint) ValidateBinding(expected BotProgressBinding) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !c.Binding.Matches(expected) {
		return ErrBotConflict
	}
	return nil
}
func (c *BotProgressCheckpoint) ValidateSuccessor(previous *BotProgressCheckpoint) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if previous == nil {
		if c.Revision != 1 {
			return ErrBotConflict
		}
		return nil
	}
	if previous.Validate() != nil {
		return ErrBotInvalid
	}
	binding := c.Binding
	binding.WorkRevision = previous.Binding.WorkRevision
	if !binding.Matches(previous.Binding) || c.Binding.WorkRevision < previous.Binding.WorkRevision || c.Revision <= previous.Revision || c.Revision-previous.Revision != 1 || c.TurnID != previous.TurnID || c.EventCursor < previous.EventCursor || c.TranscriptHighWater < previous.TranscriptHighWater || c.DispatchLedgerCursor < previous.DispatchLedgerCursor || !botProgressUsageMonotonic(c.Usage, previous.Usage) {
		return ErrBotConflict
	}
	if previous.LastEvaluation != nil && (c.LastEvaluation == nil || c.LastEvaluation.At.Before(previous.LastEvaluation.At)) {
		return ErrBotConflict
	}
	return nil
}

type BotProgressState struct {
	SchemaVersion int                    `json:"schema_version"`
	Phase         BotProgressPhase       `json:"phase"`
	Reason        BotProgressReason      `json:"reason,omitempty"`
	Checkpoint    *BotProgressCheckpoint `json:"checkpoint"`
}

func (s *BotProgressState) Clone() *BotProgressState {
	if s == nil {
		return nil
	}
	out := *s
	out.Checkpoint = s.Checkpoint.Clone()
	return &out
}
func (s *BotProgressState) Validate() error {
	if s == nil || s.SchemaVersion != BotProgressSchemaVersion {
		return ErrBotUnsupported
	}
	if !s.Phase.Valid() || s.Reason != "" && !s.Reason.Valid() {
		return ErrBotInvalid
	}
	if err := s.Checkpoint.Validate(); err != nil {
		return err
	}
	return botProgressBounded(s)
}

// Every request is constructed by a trusted worker. Validate checks shape only;
// implementations MUST compare the live scoped lease/fence/revision atomically.
type BotProgressMutation struct {
	Lease            BotLease
	Binding          BotProgressBinding
	ExpectedRevision int64
}

func (m BotProgressMutation) Validate(now time.Time) error {
	if m.Binding.Validate() != nil || m.ExpectedRevision < 0 || m.Lease.BotID != m.Binding.BotID || m.Lease.RunID != m.Binding.RunID || strings.TrimSpace(m.Lease.WorkerID) == "" || len(m.Lease.WorkerID) > 256 || m.Lease.Fence < 1 || !m.Lease.ExpiresAt.After(now) {
		return ErrBotInvalid
	}
	return nil
}

type BotProgressCheckpointRequest struct {
	Mutation BotProgressMutation
	State    BotProgressState
}

func (r BotProgressCheckpointRequest) Validate(now time.Time) error {
	if err := r.Mutation.Validate(now); err != nil {
		return err
	}
	if err := r.State.Validate(); err != nil {
		return err
	}
	if !r.State.Checkpoint.Binding.Matches(r.Mutation.Binding) || r.State.Checkpoint.Revision <= r.Mutation.ExpectedRevision || r.State.Checkpoint.Revision-r.Mutation.ExpectedRevision != 1 {
		return ErrBotConflict
	}
	return nil
}

type BotProgressDispatchKind string

const (
	BotProgressDispatchModel BotProgressDispatchKind = "model"
	BotProgressDispatchTool  BotProgressDispatchKind = "tool"
)

type BotProgressDispatchOutcome string

const (
	BotProgressDispatchSettled     BotProgressDispatchOutcome = "settled"
	BotProgressDispatchNotExecuted BotProgressDispatchOutcome = "not_executed"
	BotProgressDispatchUncertain   BotProgressDispatchOutcome = "uncertain"
)

type BotProgressBeginDispatchRequest struct {
	Mutation   BotProgressMutation
	DispatchID uuid.UUID
	Kind       BotProgressDispatchKind
	// Digest pins the exact private operation, not model-provided authority.
	OperationDigest string
	ReservedUsage   BotUsage
}

func (r BotProgressBeginDispatchRequest) Validate(now time.Time) error {
	if err := r.Mutation.Validate(now); err != nil {
		return err
	}
	if r.DispatchID == uuid.Nil || r.Kind != BotProgressDispatchModel && r.Kind != BotProgressDispatchTool || !skillDigestValid(r.OperationDigest) || !botProgressUsageValid(r.ReservedUsage) {
		return ErrBotInvalid
	}
	return nil
}

type BotProgressSettleDispatchRequest struct {
	Mutation   BotProgressMutation
	DispatchID uuid.UUID
	Outcome    BotProgressDispatchOutcome
	Usage      BotUsage
	// EvidenceRef points to trusted receipt/nonexecution evidence, never tool text.
	EvidenceRef string
}

func (r BotProgressSettleDispatchRequest) Validate(now time.Time) error {
	if err := r.Mutation.Validate(now); err != nil {
		return err
	}
	if r.DispatchID == uuid.Nil || r.Outcome != BotProgressDispatchSettled && r.Outcome != BotProgressDispatchNotExecuted && r.Outcome != BotProgressDispatchUncertain || !botProgressUsageValid(r.Usage) || len(r.EvidenceRef) > BotProgressMaxContextRefBytes || r.Outcome == BotProgressDispatchNotExecuted && strings.TrimSpace(r.EvidenceRef) == "" {
		return ErrBotInvalid
	}
	return nil
}

type BotProgressDispatchReceipt struct {
	DispatchID   uuid.UUID
	LedgerCursor int64
	Revision     int64
}
type BotProgressContinueRequest struct {
	Mutation           BotProgressMutation
	CheckpointRevision int64
	Reason             BotProgressReason
}

func (r BotProgressContinueRequest) Validate(now time.Time) error {
	if err := r.Mutation.Validate(now); err != nil {
		return err
	}
	if r.CheckpointRevision < 1 || r.CheckpointRevision != r.Mutation.ExpectedRevision || !r.Reason.Valid() {
		return ErrBotInvalid
	}
	return nil
}

// Optional, separate from approval continuation and BotRuntimeStore. Continuation
// preserves run/turn/usage and refuses uncertain dispatch replay. No method grants
// authority or resets quota. Store transactions must enforce those invariants.
type BotProgressStore interface {
	CheckpointProgress(context.Context, BotProgressCheckpointRequest) (*BotProgressState, error)
	BeginProgressDispatch(context.Context, BotProgressBeginDispatchRequest) (*BotProgressDispatchReceipt, error)
	SettleProgressDispatch(context.Context, BotProgressSettleDispatchRequest) (*BotProgressDispatchReceipt, error)
	ContinueProgress(context.Context, BotProgressContinueRequest) (*BotProgressState, error)
}
