package daemon

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type knowledgeUsageModel struct{ fail bool }

func (knowledgeUsageModel) Name() string { return "knowledge-usage" }
func (m knowledgeUsageModel) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		r := &model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, ThoughtsTokenCount: 4096}}
		if m.fail {
			yield(r, errors.New("failed"))
			return
		}
		if yield(r, nil) {
			yield(r, nil)
		}
	}
}
func TestBotKnowledgeReasoningUsage(t *testing.T) {
	for _, fail := range []bool{false, true} {
		out, err := (botKnowledgeModel{llm: knowledgeUsageModel{fail: fail}}).GenerateKnowledgeExtraction(context.Background(), bot.KnowledgeExtractionModelRequest{MaxOutputTokens: 8192})
		if err == nil {
			t.Fatal("empty response accepted")
		}
		if out.Usage.InputTokens != 10 || out.Usage.OutputTokens != 4096 {
			t.Errorf("fail=%v usage=%+v", fail, out.Usage)
		}
	}
}
