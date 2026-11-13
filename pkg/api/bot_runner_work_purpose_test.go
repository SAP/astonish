package api

import (
	"context"
	"encoding/json"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type botAssessmentPurposeModel struct {
	t      *testing.T
	prompt string
	calls  int
}

func (*botAssessmentPurposeModel) Name() string { return "assessment-purpose" }
func (m *botAssessmentPurposeModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if bot.ModelCallPurposeFromContext(ctx) != bot.ModelCallPurposeAssessment {
			m.t.Error("assessment purpose absent at model boundary")
		}
		schema, err := json.Marshal(req.Config.ResponseJsonSchema)
		wantPrompt := m.prompt + "\nRESPONSE JSON SCHEMA (server-defined output contract; enum values are exact, not examples):\n" + string(schema)
		if err != nil || !stream || len(req.Config.Tools) != 0 || req.Config.SystemInstruction.Parts[0].Text != wantPrompt {
			m.t.Error("assessment request changed")
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(`{}`, "model")}, nil)
	}
}
func TestBotWorkJSONAssessmentPurpose(t *testing.T) {
	for _, prompt := range []string{botWorkPreparePrompt, botWorkReviewPrompt, botWorkPendingReviewPrompt} {
		ctx, req, _ := botChatTestRequest(t)
		llm := &botAssessmentPurposeModel{t: t, prompt: prompt}
		var output map[string]any
		var usage store.BotUsage
		if err := botWorkJSON(ctx, req, llm, prompt, map[string]string{"data": "untrusted"}, &output, &usage); err != nil {
			t.Fatal(err)
		}
		if llm.calls != 1 {
			t.Fatalf("calls = %d", llm.calls)
		}
		if bot.ModelCallPurposeFromContext(ctx) != bot.ModelCallPurposeProducer {
			t.Fatal("assessment purpose leaked to producer")
		}
	}
}
