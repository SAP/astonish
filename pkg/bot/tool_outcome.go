package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/SAP/astonish/pkg/execution"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// ToolOutcomeKey is reserved for server-generated tool metadata. Remote JSON,
// tool arguments and model prose must never be decoded into ToolOutcome.
const ToolOutcomeKey = "astonish_bot_outcome"

// ToolOutcome is an in-process attestation by a trusted tool implementation.
// Verified means the tool independently observed the stated outcome, not merely
// that dispatch returned without error. Unknown effects belong in Unverified.
type ToolOutcome struct {
	Verified   bool
	Changed    []store.BotEvidence
	Unchanged  []store.BotEvidence
	Unverified []string
	Artifacts  []ToolReportArtifact
	NextRunAt  *time.Time
	// SourceAccess records transport facts, never source usefulness or truth.
	SourceAccess []store.BotSourceAccessFact
	// SourceRead binds actual first-party read execution, never admission authority.
	SourceRead *ToolSourceReadReceipt
	// ScheduleCapture attests only durable enqueue, never a scheduler mutation.
	ScheduleCapture *ScheduleCapture
	RoutineCapture  *RoutineCapture
	// Nonexecution is recorded only at a concrete pre-execution rejection.
	// It never certifies a successful task, content, or external effect.
	Nonexecution *ToolNonexecutionReceipt
	// ReceiptConflict prevents duplicate ledger records from erasing uncertainty.
	ReceiptConflict bool
}

// ToolSourceReadReceipt is out-of-band and minted only by a pinned read adapter.
type ToolSourceReadReceipt struct {
	CallID, ToolName, ArgumentDigest, ResponseDigest string
	Identity                                         execution.BotRunIdentity
}

func NewToolSourceReadReceipt(ctx context.Context, callID, target string, args any, response map[string]any) *ToolSourceReadReceipt {
	binding := NewToolNonexecutionOutcome(ctx, callID, target, args, ToolFeedbackTerminal).Nonexecution
	raw, err := json.Marshal(response)
	if binding == nil || err != nil || response == nil {
		return nil
	}
	sum := sha256.Sum256(raw)
	return &ToolSourceReadReceipt{CallID: callID, ToolName: target, Identity: binding.Identity, ArgumentDigest: binding.ArgumentDigest, ResponseDigest: hex.EncodeToString(sum[:])}
}

func (r *ToolSourceReadReceipt) ValidFor(identity execution.BotRunIdentity, callID, target, digest string, response map[string]any) bool {
	if r == nil || callID == "" || target == "" || digest == "" || r.Identity != identity || r.CallID != callID || r.ToolName != target || r.ArgumentDigest != digest || response == nil {
		return false
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(raw)
	return r.ResponseDigest == hex.EncodeToString(sum[:])
}

// ToolNonexecutionReceipt binds a trusted rejection to its exact invocation.
// This type is server-only; provider JSON and tool arguments cannot mint it.
type ToolFeedbackDisposition string

const (
	ToolFeedbackTerminal         ToolFeedbackDisposition = "terminal"
	ToolFeedbackCorrectCall      ToolFeedbackDisposition = "correct_call"
	ToolFeedbackUnresolvedTarget ToolFeedbackDisposition = "unresolved_target"
)

type ToolNonexecutionReceipt struct {
	CallID         string
	ToolName       string
	Identity       execution.BotRunIdentity
	ArgumentDigest string
	Disposition    ToolFeedbackDisposition
}

// NewToolNonexecutionOutcome is only for a boundary which knows dispatch has not
// started. A repair permits a new corrected invocation, never replay of this one.
func NewToolNonexecutionOutcome(ctx context.Context, callID, target string, args any, disposition ToolFeedbackDisposition) ToolOutcome {
	identity, ok := execution.BotRunIdentityFromContext(ctx)
	if !ok || callID == "" || target == "" {
		return ToolOutcome{}
	}
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return ToolOutcome{}
	}
	digest, err := (execution.ActionDescriptor{Tool: target, Arguments: raw, Effect: execution.EffectUnknown}).Digest()
	if err != nil {
		return ToolOutcome{}
	}
	return ToolOutcome{Verified: true, Nonexecution: &ToolNonexecutionReceipt{CallID: callID, ToolName: target, Identity: identity, ArgumentDigest: digest, Disposition: disposition}}
}

func (r *ToolNonexecutionReceipt) ValidFor(identity execution.BotRunIdentity, callID, target, digest string) bool {
	validDisposition := r != nil && (r.Disposition == ToolFeedbackTerminal || r.Disposition == ToolFeedbackCorrectCall || r.Disposition == ToolFeedbackUnresolvedTarget)
	return validDisposition && r.Identity == identity && r.CallID == callID && callID != "" && r.ToolName == target && target != "" && r.ArgumentDigest == digest && digest != ""
}

// NonexecutionFor validates the complete exclusive attestation, not only its ID.
// Mixed effect/source records and duplicate receipts never establish nonexecution.
func (o ToolOutcome) NonexecutionFor(ctx context.Context, callID, target string, args any) bool {
	want := NewToolNonexecutionOutcome(ctx, callID, target, args, ToolFeedbackTerminal).Nonexecution
	return want != nil && o.Nonexecution.ValidFor(want.Identity, callID, target, want.ArgumentDigest) && o.Verified && !o.ReceiptConflict && len(o.Changed) == 0 && len(o.Unchanged) == 0 && len(o.Unverified) == 0 && len(o.Artifacts) == 0 && len(o.SourceAccess) == 0 && o.SourceRead == nil && o.RoutineCapture == nil && o.ScheduleCapture == nil && o.NextRunAt == nil
}

// ToolArgumentError is minted only by trusted pre-dispatch argument validation.
// Authorization, budget, cancellation and unknown execution errors do not use it.
type ToolArgumentError struct{ Cause error }

func (e *ToolArgumentError) Error() string {
	return "Invalid tool arguments; correct the call using its described schema."
}
func (e *ToolArgumentError) Unwrap() error { return e.Cause }

// ToolReportArtifact binds captured report bytes to a server-observed home file.
// Its digest and home ownership are checked again by the runtime adapter.
type ToolReportArtifact struct {
	Artifact store.BotArtifact
	Body     string
}

// ValidObservation deliberately excludes model inference and owner confirmation:
// this boundary accepts only independently observed server tool records.
func ValidObservation(e store.BotEvidence) bool {
	return e.Kind == store.BotEvidenceObservation && e.ValidateCompletion() == nil && strings.TrimSpace(e.Source) != ""
}

// ToolOutcomes carries attestations out-of-band because ADK functiontool JSON
// round-trips its return value. The model cannot create entries in this ledger.
type ToolOutcomes struct {
	mu      sync.Mutex
	records map[string]ToolOutcome
}
type toolOutcomesKey struct{}

// ToolOutcomesEnabled allows boundaries to avoid touching invocation-only fields
// when no per-attempt ledger was installed (for example metadata-only callers).
func ToolOutcomesEnabled(ctx context.Context) bool {
	ledger, _ := ctx.Value(toolOutcomesKey{}).(*ToolOutcomes)
	return ledger != nil
}

func WithToolOutcomes(ctx context.Context) (context.Context, *ToolOutcomes) {
	ledger := &ToolOutcomes{records: make(map[string]ToolOutcome)}
	return context.WithValue(ctx, toolOutcomesKey{}, ledger), ledger
}

// WithToolOutcomesFromContext carries only the trusted per-attempt ledger across
// an owner-context isolation boundary; no other source values are inherited.
func WithToolOutcomesFromContext(dst, src context.Context) context.Context {
	ledger, _ := src.Value(toolOutcomesKey{}).(*ToolOutcomes)
	if ledger == nil {
		return dst
	}
	return context.WithValue(dst, toolOutcomesKey{}, ledger)
}

// RecordToolOutcome is called by trusted server tool code after observing its
// result. Never call this with model-supplied or remotely deserialized metadata.
// callID must be the active ToolContext.FunctionCallID().
func RecordToolOutcome(ctx context.Context, callID string, outcome ToolOutcome) {
	ledger, _ := ctx.Value(toolOutcomesKey{}).(*ToolOutcomes)
	if ledger == nil || callID == "" {
		return
	}
	outcome.SourceAccess = append([]store.BotSourceAccessFact(nil), outcome.SourceAccess...)
	if outcome.ScheduleCapture != nil {
		capture := *outcome.ScheduleCapture
		outcome.ScheduleCapture = &capture
	}
	if outcome.RoutineCapture != nil {
		capture := *outcome.RoutineCapture
		outcome.RoutineCapture = &capture
	}
	if outcome.SourceRead != nil {
		receipt := *outcome.SourceRead
		outcome.SourceRead = &receipt
	}
	if outcome.Nonexecution != nil {
		receipt := *outcome.Nonexecution
		outcome.Nonexecution = &receipt
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if prior, exists := ledger.records[callID]; exists {
		// A second record cannot overwrite a failed effect with nonexecution.
		prior.ReceiptConflict = true
		ledger.records[callID] = prior
		return
	}
	ledger.records[callID] = outcome
}

// TakeToolOutcome consumes an attestation only when its tool response arrives.
func TakeToolOutcome(ctx context.Context, callID string) (ToolOutcome, bool) {
	ledger, _ := ctx.Value(toolOutcomesKey{}).(*ToolOutcomes)
	if ledger == nil || callID == "" {
		return ToolOutcome{}, false
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	outcome, ok := ledger.records[callID]
	delete(ledger.records, callID)
	return outcome, ok
}
