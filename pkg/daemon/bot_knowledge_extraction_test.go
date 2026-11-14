package daemon

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type knowledgeAdapterModel struct {
	response *model.LLMResponse
	request  *model.LLMRequest
	calls    int
}

func (*knowledgeAdapterModel) Name() string { return "controlled" }
func (m *knowledgeAdapterModel) GenerateContent(c context.Context, q *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.request = q
	m.calls++
	return func(yield func(*model.LLMResponse, error) bool) {
		if e := c.Err(); e != nil {
			yield(nil, e)
			return
		}
		yield(m.response, nil)
	}
}
func TestBotKnowledgeProductionSafety(t *testing.T) {
	input := store.BotKnowledgeExtractionInput{SchemaVersion: 1, Complete: true, Source: store.BotKnowledgeExtractionSource{BotID: uuid.New(), RunID: uuid.New(), Sequence: 1, Version: 1}, OwnerMessage: "Use clear headings", Outcome: store.BotKnowledgeOutcome{Status: "succeeded", Answer: "Acknowledged"}}
	input.Source.OutcomeDigest = store.BotKnowledgeDigest(input.Outcome)
	r := credentials.NewRedactor()
	r.AddSecret("controlled", "never-retain-this-secret")
	ctx := credentials.WithRedactor(context.Background(), r)
	for _, text := range []string{"# Preference\nUse clear headings.", "# Preference\nnever-retain-this-secret", "# Procedure\n```sh\necho execute\n```", "<script>alert(1)</script>", "{{CREDENTIAL:hidden:token}}"} {
		d := store.BotKnowledgeExtractionDecision{SchemaVersion: 1, Kind: store.BotKnowledgeDecisionChanges, Reason: "owner correction", Proposals: []store.BotKnowledgeProposal{{Kind: store.BotKnowledgeProposalPreference, Path: "preferences/format.md", Content: text}}}
		err := botKnowledgeSafety(ctx, input, d)
		if text == "# Preference\nUse clear headings." {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("unsafe %q accepted: %v", text, err)
		}
	}
}
func TestBotKnowledgeWorkerRejectsLatePoll(t *testing.T) {
	w := &BotWorker{}
	w.Stop()
	if err := w.Poll(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("late poll: %v", err)
	}
	w.Start(context.Background()) // cannot resurrect a stopped worker
}

func TestBotKnowledgeDedicatedAdapterToolFree(t *testing.T) {
	m := &knowledgeAdapterModel{response: &model.LLMResponse{Content: genai.NewContentFromText(`{"schema_version":1,"kind":"no_change","reason":"nothing new","proposals":[]}`, "model")}}
	out, e := (botKnowledgeModel{llm: m}).GenerateKnowledgeExtraction(context.Background(), bot.KnowledgeExtractionModelRequest{InputJSON: `{"owner_message":"short correction"}`, SystemInstruction: "extract", MaxOutputTokens: 123})
	if e != nil || out.JSON == "" || m.calls != 1 || len(m.request.Tools) != 0 || len(m.request.Config.Tools) != 0 || m.request.Config.MaxOutputTokens != 123 {
		t.Fatalf("adapter %+v %v", out, e)
	}
	m.response.Content = &genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "shell_command"}}}}
	if _, e = (botKnowledgeModel{llm: m}).GenerateKnowledgeExtraction(context.Background(), bot.KnowledgeExtractionModelRequest{}); !errors.Is(e, store.ErrBotForbidden) {
		t.Fatalf("tool call %v", e)
	}
	c, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = (botKnowledgeModel{llm: m}).GenerateKnowledgeExtraction(c, bot.KnowledgeExtractionModelRequest{}); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel %v", e)
	}
}
func TestBotKnowledgeDedicatedAdapterPartialIsNotCompletion(t *testing.T) {
	m := &knowledgeAdapterModel{response: &model.LLMResponse{Partial: true, Content: genai.NewContentFromText(`{}`, "model")}}
	if _, e := (botKnowledgeModel{llm: m}).GenerateKnowledgeExtraction(context.Background(), bot.KnowledgeExtractionModelRequest{}); !errors.Is(e, store.ErrBotConflict) {
		t.Fatalf("partial %v", e)
	}
}
