package bot

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"strings"
	"sync"
	"time"
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
	// ScheduleCapture attests only durable enqueue, never a scheduler mutation.
	ScheduleCapture *ScheduleCapture
	RoutineCapture  *RoutineCapture
}

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
	if outcome.ScheduleCapture != nil {
		capture := *outcome.ScheduleCapture
		outcome.ScheduleCapture = &capture
	}
	if outcome.RoutineCapture != nil {
		capture := *outcome.RoutineCapture
		outcome.RoutineCapture = &capture
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
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
