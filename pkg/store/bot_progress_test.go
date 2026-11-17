package store

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
)

func progressContractFixture() (*BotProgressState, BotProgressMutation, time.Time) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	cost := int64(7)
	c := &BotProgressCheckpoint{
		Kind: BotProgressCheckpointV1, SchemaVersion: BotProgressSchemaVersion,
		Binding:  BotProgressBinding{BotID: uuid.New(), RunID: uuid.New(), SessionID: uuid.New(), Version: 2, WorkRevision: 3, Task: &BotTaskRef{TaskID: uuid.New(), Revision: 1, Digest: strings.Repeat("a", 64)}},
		Revision: 1, TurnID: uuid.New(), EventCursor: 2, TranscriptHighWater: 2,
		ContextRef: "context:private:1", ContextDigest: strings.Repeat("b", 64),
		LastEvaluation: &BotProgressEvaluation{At: now, Decision: BotProgressContinue, Reason: BotProgressReasonCadence, Summary: "Comparing the remaining evidence."},
		NextEligibleAt: now.Add(5 * time.Minute), Usage: BotUsage{RuntimeNanos: 10, ToolCalls: 1, InputTokens: 2, OutputTokens: 3, CostMicros: &cost}, DispatchLedgerCursor: 1,
	}
	m := BotProgressMutation{Binding: c.Binding, Lease: BotLease{BotID: c.Binding.BotID, RunID: c.Binding.RunID, WorkerID: "worker", Fence: 4, ExpiresAt: now.Add(time.Minute)}}
	return &BotProgressState{SchemaVersion: BotProgressSchemaVersion, Phase: BotProgressWorking, Checkpoint: c}, m, now
}

func TestBotProgressContractCheckpoint(t *testing.T) {
	s, m, now := progressContractFixture()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (BotProgressCheckpointRequest{Mutation: m, State: *s}).Validate(now); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*BotProgressState){
		"state version":           func(s *BotProgressState) { s.SchemaVersion++ },
		"checkpoint version":      func(s *BotProgressState) { s.Checkpoint.SchemaVersion++ },
		"approval format":         func(s *BotProgressState) { s.Checkpoint.Kind = "approval" },
		"missing checkpoint":      func(s *BotProgressState) { s.Checkpoint = nil },
		"unknown phase":           func(s *BotProgressState) { s.Phase = "completed" },
		"unknown reason":          func(s *BotProgressState) { s.Reason = "unknown" },
		"unknown decision":        func(s *BotProgressState) { s.Checkpoint.LastEvaluation.Decision = "stop" },
		"missing evaluation date": func(s *BotProgressState) { s.Checkpoint.LastEvaluation.At = time.Time{} },
		"oversized summary": func(s *BotProgressState) {
			s.Checkpoint.LastEvaluation.Summary = strings.Repeat("s", BotProgressMaxSummaryBytes+1)
		},
		"oversized ref": func(s *BotProgressState) {
			s.Checkpoint.ContextRef = strings.Repeat("x", BotProgressMaxContextRefBytes+1)
		},
		"digest":                        func(s *BotProgressState) { s.Checkpoint.ContextDigest = "bad" },
		"task digest":                   func(s *BotProgressState) { s.Checkpoint.Binding.Task.Digest = "bad" },
		"missing session":               func(s *BotProgressState) { s.Checkpoint.Binding.SessionID = uuid.Nil },
		"negative cursor":               func(s *BotProgressState) { s.Checkpoint.EventCursor = -1 },
		"negative usage":                func(s *BotProgressState) { s.Checkpoint.Usage.ToolCalls = -1 },
		"overflow tokens":               func(s *BotProgressState) { s.Checkpoint.Usage.InputTokens = 1<<63 - 1 },
		"negative cost":                 func(s *BotProgressState) { *s.Checkpoint.Usage.CostMicros = -1 },
		"eligibility before evaluation": func(s *BotProgressState) { s.Checkpoint.NextEligibleAt = now.Add(-time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			q := s.Clone()
			change(q)
			if q.Validate() == nil {
				t.Fatal("malformed progress accepted")
			}
		})
	}
	var approval BotProgressCheckpoint
	if err := json.Unmarshal([]byte(`{"schema_version":1,"kind":"approval","tool_name":"send"}`), &approval); err != nil {
		t.Fatal(err)
	}
	if approval.Validate() == nil {
		t.Fatal("approval became progress")
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var restored BotProgressState
	if err := json.Unmarshal(encoded, &restored); err != nil || restored.Validate() != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	clone := s.Clone()
	clone.Checkpoint.Binding.Task.Revision++
	clone.Checkpoint.LastEvaluation.Summary = "changed"
	*clone.Checkpoint.Usage.CostMicros = 999
	if s.Checkpoint.Binding.Task.Revision != 1 || s.Checkpoint.LastEvaluation.Summary == "changed" || *s.Checkpoint.Usage.CostMicros != 7 {
		t.Fatal("checkpoint aliases")
	}
}

func TestBotProgressContractBindingsAndMonotonicity(t *testing.T) {
	s, m, now := progressContractFixture()
	for name, change := range map[string]func(*BotProgressBinding){
		"bot":     func(b *BotProgressBinding) { b.BotID = uuid.New() },
		"run":     func(b *BotProgressBinding) { b.RunID = uuid.New() },
		"session": func(b *BotProgressBinding) { b.SessionID = uuid.New() },
		"version": func(b *BotProgressBinding) { b.Version++ },
		"work":    func(b *BotProgressBinding) { b.WorkRevision++ },
		"task":    func(b *BotProgressBinding) { b.Task.Revision++ },
	} {
		t.Run(name, func(t *testing.T) {
			q := s.Clone()
			change(&q.Checkpoint.Binding)
			if q.Checkpoint.ValidateBinding(s.Checkpoint.Binding) == nil {
				t.Fatal("tampered binding accepted")
			}
			if (BotProgressCheckpointRequest{Mutation: m, State: *q}).Validate(now) == nil {
				t.Fatal("mutation binding mismatch accepted")
			}
		})
	}
	for name, change := range map[string]func(*BotProgressMutation){
		"foreign lease":  func(m *BotProgressMutation) { m.Lease.RunID = uuid.New() },
		"expired":        func(m *BotProgressMutation) { m.Lease.ExpiresAt = now },
		"missing fence":  func(m *BotProgressMutation) { m.Lease.Fence = 0 },
		"missing worker": func(m *BotProgressMutation) { m.Lease.WorkerID = "" },
		"stale revision": func(m *BotProgressMutation) { m.ExpectedRevision++ },
	} {
		t.Run(name, func(t *testing.T) {
			q := m
			change(&q)
			if (BotProgressCheckpointRequest{Mutation: q, State: *s}).Validate(now) == nil {
				t.Fatal("invalid lease/revision accepted")
			}
		})
	}
	next := s.Checkpoint.Clone()
	next.Revision++
	next.Binding.WorkRevision++
	next.Usage.ToolCalls++
	if err := next.ValidateSuccessor(s.Checkpoint); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*BotProgressCheckpoint){
		"revision reset":    func(c *BotProgressCheckpoint) { c.Revision = 1 },
		"revision gap":      func(c *BotProgressCheckpoint) { c.Revision = 3 },
		"turn replacement":  func(c *BotProgressCheckpoint) { c.TurnID = uuid.New() },
		"event reset":       func(c *BotProgressCheckpoint) { c.EventCursor = 0 },
		"transcript reset":  func(c *BotProgressCheckpoint) { c.TranscriptHighWater = 0 },
		"ledger reset":      func(c *BotProgressCheckpoint) { c.DispatchLedgerCursor = 0 },
		"usage reset":       func(c *BotProgressCheckpoint) { c.Usage.RuntimeNanos = 0 },
		"cost erased":       func(c *BotProgressCheckpoint) { c.Usage.CostMicros = nil },
		"evaluation erased": func(c *BotProgressCheckpoint) { c.LastEvaluation = nil },
	} {
		t.Run(name, func(t *testing.T) {
			q := next.Clone()
			change(q)
			if q.ValidateSuccessor(s.Checkpoint) == nil {
				t.Fatal("nonmonotonic successor accepted")
			}
		})
	}
}

func TestBotProgressContractDispatchAndQuota(t *testing.T) {
	s, m, now := progressContractFixture()
	m.ExpectedRevision = s.Checkpoint.Revision
	begin := BotProgressBeginDispatchRequest{Mutation: m, DispatchID: uuid.New(), Kind: BotProgressDispatchTool, OperationDigest: strings.Repeat("a", 64)}
	if err := begin.Validate(now); err != nil {
		t.Fatal(err)
	}
	bad := begin
	bad.Kind = "unknown"
	if bad.Validate(now) == nil {
		t.Fatal("unknown dispatch kind")
	}
	settle := BotProgressSettleDispatchRequest{Mutation: m, DispatchID: begin.DispatchID, Outcome: BotProgressDispatchUncertain}
	if err := settle.Validate(now); err != nil {
		t.Fatal(err)
	}
	settle.Outcome = BotProgressDispatchNotExecuted
	if settle.Validate(now) == nil {
		t.Fatal("unproven nonexecution")
	}
	settle.EvidenceRef = "trusted:receipt:1"
	if settle.Validate(now) != nil {
		t.Fatal("trusted ref rejected")
	}
	continuation := BotProgressContinueRequest{Mutation: m, CheckpointRevision: m.ExpectedRevision, Reason: BotProgressReasonRecovery}
	if err := continuation.Validate(now); err != nil {
		t.Fatal(err)
	}
	continuation.CheckpointRevision++
	if continuation.Validate(now) == nil {
		t.Fatal("stale continuation accepted")
	}
	q := BotMandateProgressQuotaRequest{Mutation: m, MandateRef: BotMandateRef{ID: uuid.New(), BotID: m.Binding.BotID, OrgSlug: "o", TeamSlug: "t", OwnerID: "u", Revision: 1}}
	if err := q.Validate(now); err != nil {
		t.Fatal(err)
	}
	q.MandateRef.BotID = uuid.New()
	if q.Validate(now) == nil {
		t.Fatal("foreign quota binding")
	}
	if reflect.TypeOf((*BotProgressStore)(nil)).Elem().NumMethod() != 4 || reflect.TypeOf((*BotMandateProgressQuotaStore)(nil)).Elem().NumMethod() != 2 {
		t.Fatal("optional contract signatures changed")
	}
}

func TestBotRuntimePolicyBudgetAndOperatorClone(t *testing.T) {
	var legacy BudgetSpec
	if err := json.Unmarshal([]byte(`{"max_runtime_seconds":900}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.RuntimePolicy != nil || legacy.MaxRuntimeSeconds != 900 {
		t.Fatal("legacy upgraded")
	}
	data, _ := json.Marshal(legacy)
	if strings.Contains(string(data), "runtime_policy") {
		t.Fatal("nil serialized")
	}
	n := int64(100)
	b := BudgetSpec{RuntimePolicy: execution.NewBotProgressRuntimePolicy(), MaxTokens: &n, MaxToolCalls: &n, MaxRunsPerDay: &n, MaxModelCostMicros: &n}
	b.RuntimePolicy.MaxActiveSeconds = &n
	clone := b.Clone()
	*clone.RuntimePolicy.MaxActiveSeconds = 1
	*clone.MaxTokens = 2
	*clone.MaxToolCalls = 3
	*clone.MaxRunsPerDay = 4
	*clone.MaxModelCostMicros = 5
	if n != 100 {
		t.Fatal("budget clone aliases")
	}
	p := BotMandatePolicy{RuntimePolicy: b.RuntimePolicy, Scopes: []string{"chat"}, Constraints: []execution.BotActionResourceConstraint{{Resources: []string{"one"}}}, ExceptionConstraints: []execution.BotActionResourceConstraint{{Resources: []string{"two"}}}}
	cp := p.Clone()
	*cp.RuntimePolicy.MaxActiveSeconds = 9
	cp.Scopes[0] = "changed"
	cp.Constraints[0].Resources[0] = "changed"
	cp.ExceptionConstraints[0].Resources[0] = "changed"
	if n != 100 || p.Scopes[0] != "chat" || p.Constraints[0].Resources[0] != "one" || p.ExceptionConstraints[0].Resources[0] != "two" {
		t.Fatal("policy clone aliases")
	}
	input := BotRunInput{Progress: sForInput(t)}
	if input.Progress.Validate() != nil {
		t.Fatal("input progress contract")
	}
}
func sForInput(t *testing.T) *BotProgressState {
	t.Helper()
	s, _, _ := progressContractFixture()
	return s
}
