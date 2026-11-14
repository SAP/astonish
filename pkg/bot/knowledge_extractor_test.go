package bot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type extractionFakeStore struct {
	record              store.BotKnowledgeExtraction
	lease               store.BotKnowledgeExtractionLease
	begins, transitions int
	failBegin           bool
}

func (s *extractionFakeStore) EnqueueBotKnowledgeExtraction(_ context.Context, i store.BotKnowledgeExtractionInput) (*store.BotKnowledgeExtraction, error) {
	if s.record.ID == uuid.Nil {
		s.record = store.BotKnowledgeExtraction{ID: uuid.New(), Input: i.Clone(), State: store.BotKnowledgeExtractionPending, Revision: 1}
	}
	r := s.record.Clone()
	return &r, nil
}
func (s *extractionFakeStore) ReadBotKnowledgeExtraction(context.Context, store.BotKnowledgeExtractionSource) (*store.BotKnowledgeExtraction, error) {
	r := s.record.Clone()
	return &r, nil
}
func (s *extractionFakeStore) ClaimBotKnowledgeExtraction(_ context.Context, src store.BotKnowledgeExtractionSource, w string, dur time.Duration) (store.BotKnowledgeExtractionLease, error) {
	s.lease = store.BotKnowledgeExtractionLease{BotID: src.BotID, ExtractionID: s.record.ID, WorkerID: w, Fence: s.lease.Fence + 1, ExpiresAt: time.Now().Add(dur)}
	return s.lease, nil
}
func (s *extractionFakeStore) BeginBotKnowledgeExtraction(_ context.Context, l store.BotKnowledgeExtractionLease, rev int64) (*store.BotKnowledgeExtraction, bool, error) {
	s.begins++
	if s.failBegin {
		return nil, false, errors.New("uncertain begin")
	}
	if s.record.State != store.BotKnowledgeExtractionPending {
		r := s.record.Clone()
		return &r, false, nil
	}
	if rev != s.record.Revision || l != s.lease {
		return nil, false, store.ErrBotConflict
	}
	s.record.State = store.BotKnowledgeExtractionExtracting
	s.record.AttemptID = uuid.New()
	s.record.Revision++
	r := s.record.Clone()
	return &r, true, nil
}
func (s *extractionFakeStore) TransitionBotKnowledgeExtraction(_ context.Context, q store.BotKnowledgeExtractionTransition) (*store.BotKnowledgeExtraction, error) {
	s.transitions++
	if q.Lease != s.lease || q.ExpectedRevision != s.record.Revision || q.From != s.record.State {
		return nil, store.ErrBotConflict
	}
	if err := q.Validate(s.record, time.Now()); err != nil {
		return nil, err
	}
	if err := store.ValidateBotKnowledgeExtractionTransition(q.From, q.To, s.record.Decision != nil); err != nil {
		return nil, err
	}
	s.record.State = q.To
	s.record.Revision++
	if q.Decision != nil {
		d := q.Decision.Clone()
		s.record.Decision = &d
		s.record.DecisionDigest = q.DecisionDigest
	}
	s.record.HoldReason = q.Reason
	r := s.record.Clone()
	return &r, nil
}

type extractionFakeModel struct {
	calls   int
	raw     string
	err     error
	request KnowledgeExtractionModelRequest
	store   *extractionFakeStore
	t       *testing.T
}

func (m *extractionFakeModel) GenerateKnowledgeExtraction(ctx context.Context, r KnowledgeExtractionModelRequest) (KnowledgeExtractionModelResponse, error) {
	m.calls++
	m.request = r
	if m.store.record.State != store.BotKnowledgeExtractionExtracting || m.store.record.AttemptID == uuid.Nil {
		m.t.Fatal("model called before durable attempt")
	}
	dl, ok := ctx.Deadline()
	if !ok || time.Until(dl) > KnowledgeExtractionTimeout {
		m.t.Fatal("missing extraction bound")
	}
	zero := int64(0)
	return KnowledgeExtractionModelResponse{JSON: m.raw, Usage: execution.BudgetUsage{InputTokens: 10, OutputTokens: 20, CostMicros: &zero}}, m.err
}
func extractionInput() store.BotKnowledgeExtractionInput {
	o := store.BotKnowledgeOutcome{Status: "partial", Answer: "Could not fetch source", Failure: "source unavailable", Tools: []store.BotKnowledgeToolEvidence{{ID: "call1", Name: "search", Arguments: "query", Result: "", Failure: "timeout"}}, Review: "Source failure confirmed"}
	return store.BotKnowledgeExtractionInput{SchemaVersion: 1, Source: store.BotKnowledgeExtractionSource{BotID: uuid.New(), RunID: uuid.New(), OutcomeDigest: store.BotKnowledgeDigest(o), Sequence: 1, Version: 1}, Complete: true, OwnerMessage: "Use EUR.", Conversation: "Owner corrected USD to EUR.", Outcome: o}
}
func extractionRunner(t *testing.T, raw string) (*KnowledgeExtractor, *extractionFakeStore, *extractionFakeModel) {
	t.Helper()
	s := &extractionFakeStore{}
	m := &extractionFakeModel{raw: raw, store: s, t: t}
	b, err := NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 120}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewKnowledgeExtractor(KnowledgeExtractorConfig{Store: s, Model: m, Budget: b, WorkerID: "extractor", ValidateSafety: func(context.Context, store.BotKnowledgeExtractionInput, store.BotKnowledgeExtractionDecision) error {
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return e, s, m
}
func TestKnowledgeExtractorEveryTurnNoChangeAndDuplicate(t *testing.T) {
	e, s, m := extractionRunner(t, `{"schema_version":1,"kind":"no_change","reason":"already retained","proposals":[]}`)
	i := extractionInput()
	r, err := e.Extract(context.Background(), i)
	if err != nil || r.State != store.BotKnowledgeExtractionNoChange {
		t.Fatalf("%+v %v", r, err)
	}
	_, err = e.Extract(context.Background(), i)
	if err != nil || m.calls != 1 {
		t.Fatalf("replayed: %d %v", m.calls, err)
	}
	if !strings.Contains(m.request.InputJSON, "Use EUR.") || !strings.Contains(m.request.InputJSON, "timeout") || !strings.Contains(m.request.InputJSON, "Source failure confirmed") {
		t.Fatal("correction/failure/review omitted")
	}
	r.Input.Outcome.Tools[0].Failure = "changed"
	if s.record.Input.Outcome.Tools[0].Failure != "timeout" {
		t.Fatal("alias")
	}
}
func TestKnowledgeExtractorPrepared(t *testing.T) {
	d := store.BotKnowledgeExtractionDecision{SchemaVersion: 1, Kind: store.BotKnowledgeDecisionChanges, Reason: "owner corrected currency", Proposals: []store.BotKnowledgeProposal{{Kind: store.BotKnowledgeProposalPreference, Path: "preferences/currency.md", Content: "Use EUR.", EvidenceIDs: []string{"call1"}}}}
	raw, _ := json.Marshal(d)
	e, s, m := extractionRunner(t, string(raw))
	r, err := e.Extract(context.Background(), extractionInput())
	if err != nil || r.State != store.BotKnowledgeExtractionPrepared || r.DecisionDigest != store.BotKnowledgeDigest(d) {
		t.Fatalf("%+v %v", r, err)
	}
	_, err = e.Extract(context.Background(), r.Input)
	if err != nil || m.calls != 1 || s.record.State != store.BotKnowledgeExtractionPrepared {
		t.Fatal("prepared must not replay model")
	}
}
func TestKnowledgeExtractorUncertainHeld(t *testing.T) {
	for _, raw := range []string{`{"schema_version":2,"kind":"no_change","reason":"x"}`, `{"schema_version":1,"kind":"no_change","kind":"changes","reason":"x"}`, `{"Schema_version":1,"kind":"no_change","reason":"x"}`, `{"schema_version":1,"kind":"no_change","reason":"x","unexpected":true}`, `{}`, strings.Repeat("x", store.BotKnowledgeExtractionMaxOutputBytes+1), `{"schema_version":1,"kind":"no_change","reason":"x"} {}`} {
		t.Run(raw[:min(len(raw), 40)], func(t *testing.T) {
			e, s, m := extractionRunner(t, raw)
			i := extractionInput()
			_, err := e.Extract(context.Background(), i)
			if err == nil || s.record.State != store.BotKnowledgeExtractionHeld {
				t.Fatal("invalid schema must hold")
			}
			_, _ = e.Extract(context.Background(), i)
			if m.calls != 1 {
				t.Fatal("held replay")
			}
		})
	}
	e, s, m := extractionRunner(t, "")
	m.err = errors.New("provider uncertain")
	_, err := e.Extract(context.Background(), extractionInput())
	if err == nil || s.record.State != store.BotKnowledgeExtractionHeld {
		t.Fatal("model error not held")
	}
}
func TestKnowledgeExtractorBoundsFailBeforeModel(t *testing.T) {
	for _, mutate := range []func(*store.BotKnowledgeExtractionInput){func(i *store.BotKnowledgeExtractionInput) { i.Complete = false }, func(i *store.BotKnowledgeExtractionInput) {
		i.Conversation = strings.Repeat("x", store.BotKnowledgeExtractionMaxInputBytes)
	}, func(i *store.BotKnowledgeExtractionInput) {
		i.Outcome.Tools = append(i.Outcome.Tools, i.Outcome.Tools[0])
		i.Source.OutcomeDigest = store.BotKnowledgeDigest(i.Outcome)
	}} {
		e, _, m := extractionRunner(t, "")
		i := extractionInput()
		mutate(&i)
		if _, err := e.Extract(context.Background(), i); err == nil || m.calls != 0 {
			t.Fatal("invalid source reached model")
		}
	}
	e, s, m := extractionRunner(t, "")
	s.failBegin = true
	if _, err := e.Extract(context.Background(), extractionInput()); err == nil || m.calls != 0 {
		t.Fatal("uncertain begin allowed model")
	}
}
func TestKnowledgeExtractorRejectsInventedEvidenceAndStaleFile(t *testing.T) {
	for _, proposal := range []store.BotKnowledgeProposal{
		{Kind: store.BotKnowledgeProposalKnowledge, Path: "knowledge/source.md", Content: "source unavailable", EvidenceIDs: []string{"invented"}},
		{Kind: store.BotKnowledgeProposalPreference, Path: "preferences/currency.md", ExpectedDigest: store.BotKnowledgeDigest("stale"), Content: "EUR"},
	} {
		d := store.BotKnowledgeExtractionDecision{SchemaVersion: 1, Kind: store.BotKnowledgeDecisionChanges, Reason: "new fact", Proposals: []store.BotKnowledgeProposal{proposal}}
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		e, s, m := extractionRunner(t, string(raw))
		if _, err := e.Extract(context.Background(), extractionInput()); err == nil || s.record.State != store.BotKnowledgeExtractionHeld || m.calls != 1 {
			t.Fatal("unbound evidence/file accepted")
		}
	}
}
func TestKnowledgeExtractorSharedBudgetAndMissingDI(t *testing.T) {
	if _, err := NewKnowledgeExtractor(KnowledgeExtractorConfig{}); err == nil {
		t.Fatal("missing DI accepted")
	}
	e, s, m := extractionRunner(t, `{"schema_version":1,"kind":"no_change","reason":"already saved"}`)
	zero := int64(0)
	budget, err := NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 120, MaxTokens: &zero}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.budget = budget
	if _, err := e.Extract(context.Background(), extractionInput()); err == nil || m.calls != 0 || s.record.State != store.BotKnowledgeExtractionHeld {
		t.Fatal("budget bypass")
	}
	e, _, m = extractionRunner(t, `{"schema_version":1,"kind":"no_change","reason":"already saved"}`)
	if _, err := e.Extract(context.Background(), extractionInput()); err != nil {
		t.Fatal(err)
	}
	usage := e.budget.Usage()
	if usage.InputTokens != 10 || usage.OutputTokens != 20 || usage.ToolCalls != 0 || m.calls != 1 {
		t.Fatalf("shared usage: %+v", usage)
	}
}

func TestKnowledgeExtractorSafetyHeld(t *testing.T) {
	e, s, _ := extractionRunner(t, `{"schema_version":1,"kind":"no_change","reason":"x"}`)
	e.safety = func(context.Context, store.BotKnowledgeExtractionInput, store.BotKnowledgeExtractionDecision) error {
		return errors.New("unsafe")
	}
	if _, err := e.Extract(context.Background(), extractionInput()); err == nil || s.record.State != store.BotKnowledgeExtractionHeld {
		t.Fatal("safety failure not held")
	}
}
