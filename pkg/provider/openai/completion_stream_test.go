package openai

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/provider/llmerror"
	openailib "github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestChatCompletionStreamTerminationEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, finish         string
		delta                map[string]any
		recoverable, wantErr bool
		wantFinish           genai.FinishReason
	}{
		{"length_empty", "length", nil, true, true, genai.FinishReasonMaxTokens},
		{"stop_empty", "stop", nil, true, true, genai.FinishReasonStop},
		{"length_visible", "length", map[string]any{"content": "partial"}, false, true, genai.FinishReasonMaxTokens},
		{"reasoning_only", "length", map[string]any{"reasoning_content": "secret thoughts"}, false, true, genai.FinishReasonMaxTokens},
		{"refusal", "stop", map[string]any{"refusal": "secret refusal"}, false, true, genai.FinishReasonSafety},
		{"refusal_missing_finish", "", map[string]any{"refusal": "secret refusal"}, false, true, genai.FinishReasonSafety},
		{"filter", "content_filter", nil, false, true, genai.FinishReasonSafety},
		{"unknown", "new_reason", nil, false, true, genai.FinishReasonOther},
		{"text", "stop", map[string]any{"content": "answer"}, false, false, genai.FinishReasonStop},
		{"tool", "tool_calls", map[string]any{"tool_calls": []any{safetyTool(0, "call", "lookup", "{}")}}, false, false, genai.FinishReasonStop},
		{"tool_length", "length", map[string]any{"tool_calls": []any{safetyTool(0, "call", "lookup", "{}")}}, false, true, genai.FinishReasonMaxTokens},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := safetyChat(tt.delta, tt.finish)
			body += safetyEvent(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4096, "total_tokens": 4106, "completion_tokens_details": map[string]any{"reasoning_tokens": 4000}}}) + "data: [DONE]\n\n"
			p, b := safetyProvider(body, false)
			var final *model.LLMResponse
			var finalErr error
			for response, err := range p.GenerateContent(context.Background(), &model.LLMRequest{}, true) {
				if response != nil && response.Content != nil {
					for _, part := range response.Content.Parts {
						if strings.Contains(part.Text, "secret") && !part.Thought {
							t.Fatal("private payload exposed as visible text")
						}
						if tt.wantErr && part.FunctionCall != nil {
							t.Fatal("failed completion exposed executable tool")
						}
					}
				}
				if response != nil && !response.Partial {
					final = response
					finalErr = err
				}
			}
			if !b.closed {
				t.Error("stream body not closed")
			}
			if (finalErr != nil) != tt.wantErr || llmerror.IsRecoverableEmptyCompletion(finalErr) != tt.recoverable {
				t.Fatalf("error=%v recoverable=%v", finalErr, llmerror.IsRecoverableEmptyCompletion(finalErr))
			}
			if final == nil || final.FinishReason != tt.wantFinish {
				t.Fatalf("final=%+v", final)
			}
			u := final.UsageMetadata
			if u == nil || u.CandidatesTokenCount != 96 || u.ThoughtsTokenCount != 4000 || u.TotalTokenCount != 4106 {
				t.Errorf("lost/double counted trailer usage: %+v", u)
			}
			if finalErr != nil && strings.Contains(finalErr.Error(), "secret") {
				t.Fatal("error leaked payload")
			}
		})
	}
}

func TestCompletionUsageReasoningSubset(t *testing.T) {
	for _, reasoning := range []int{-1, 0, 7, 50} {
		u := usageMetadata(openailib.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30, CompletionTokensDetails: &openailib.CompletionTokensDetails{ReasoningTokens: reasoning}})
		if u.ThoughtsTokenCount < 0 || u.CandidatesTokenCount < 0 || u.ThoughtsTokenCount+u.CandidatesTokenCount != 20 || u.TotalTokenCount != 30 {
			t.Errorf("reasoning=%d usage=%+v", reasoning, u)
		}
	}
}
