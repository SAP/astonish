package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// Exercise the dedicated extractor against the real scoped SQLite adapter, not
// an extraction fake. This adapter deliberately has no tools or paid provider.
type knowledgeStorageModel struct {
	t     *testing.T
	r     *botRepository
	input store.BotKnowledgeExtractionInput
	calls int
	raw   string
	fail  error
}

func (m *knowledgeStorageModel) GenerateKnowledgeExtraction(c context.Context, q bot.KnowledgeExtractionModelRequest) (bot.KnowledgeExtractionModelResponse, error) {
	m.calls++
	persisted, err := m.r.ReadBotKnowledgeExtraction(c, m.input.Source)
	if err != nil || persisted == nil || persisted.State != store.BotKnowledgeExtractionExtracting || persisted.AttemptID == uuid.Nil || persisted.AttemptID != q.AttemptID {
		m.t.Fatalf("model began before durable attempt: %+v %v", persisted, err)
	}
	zero := int64(0)
	return bot.KnowledgeExtractionModelResponse{JSON: m.raw, Usage: execution.BudgetUsage{InputTokens: 10, OutputTokens: 20, CostMicros: &zero}}, m.fail
}

func TestBotKnowledgeExtractionDedicatedWorkerSQLite(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, kind := range []string{"no_change", "prepared", "uncertain"} {
			t.Run(string(scope)+"/"+kind, func(t *testing.T) {
				r, c, input, _ := knowledgeStorageFixture(t, scope)
				model := &knowledgeStorageModel{t: t, r: r, input: input, raw: `{"schema_version":1,"kind":"no_change","reason":"Already retained","proposals":[]}`}
				expected := store.BotKnowledgeExtractionNoChange
				if kind == "prepared" {
					model.raw = `{"schema_version":1,"kind":"changes","reason":"Retain honest limitation","proposals":[{"kind":"failure","path":"failures/source.md","content":"# Source\nUnavailable."}]}`
					expected = store.BotKnowledgeExtractionPrepared
				}
				if kind == "uncertain" {
					model.fail = errors.New("provider acknowledgement lost")
					expected = store.BotKnowledgeExtractionHeld
				}
				budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 120}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				worker, err := bot.NewKnowledgeExtractor(bot.KnowledgeExtractorConfig{Store: r, Model: model, Budget: budget, WorkerID: "dedicated-extractor", ValidateSafety: func(context.Context, store.BotKnowledgeExtractionInput, store.BotKnowledgeExtractionDecision) error {
					return nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				result, err := worker.Extract(c, input)
				if kind == "uncertain" {
					if !errors.Is(err, model.fail) {
						t.Fatalf("uncertain cause %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if result == nil || result.State != expected || model.calls != 1 {
					t.Fatalf("result %+v calls %d", result, model.calls)
				}
				again, err := worker.Extract(c, input)
				if err != nil || again.State != expected || again.ID != result.ID || again.AttemptID != result.AttemptID || model.calls != 1 {
					t.Fatalf("duplicate regenerated: %+v %v calls %d", again, err, model.calls)
				}
				if kind != "uncertain" {
					usage := budget.Usage()
					if usage.InputTokens != 10 || usage.OutputTokens != 20 || usage.ToolCalls != 0 {
						t.Fatalf("unshared model accounting %+v", usage)
					}
				}
			})
		}
	}
}
