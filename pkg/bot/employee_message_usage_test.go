package bot

import (
	"context"
	"errors"
	"iter"
	"math"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type employeeUsageModel struct {
	responses []*model.LLMResponse
	err       error
}

func (employeeUsageModel) Name() string { return "usage-test" }
func (m employeeUsageModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for i, r := range m.responses {
			var err error
			if i == len(m.responses)-1 {
				err = m.err
			}
			if !yield(r, err) {
				return
			}
		}
	}
}

func TestEmployeeModelJSONReasoningUsage(t *testing.T) {
	cases := []struct {
		name          string
		usage         genai.GenerateContentResponseUsageMetadata
		input, output int64
	}{
		{"reasoning_only", genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, ThoughtsTokenCount: 4096, TotalTokenCount: 4106}, 10, 4096},
		{"mixed_exact_once", genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 96, ThoughtsTokenCount: 4000, TotalTokenCount: 4106}, 10, 4096},
		{"total_fallback", genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, TotalTokenCount: 4106}, 10, 4096},
		{"tool_prompt", genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, ToolUsePromptTokenCount: 5, ThoughtsTokenCount: 100, TotalTokenCount: 115}, 15, 100},
		{"invalid_negative", genai.GenerateContentResponseUsageMetadata{PromptTokenCount: -10, CandidatesTokenCount: -20, ThoughtsTokenCount: -30, TotalTokenCount: -60}, 0, 0},
		{"invalid_total_below_input", genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 3, ThoughtsTokenCount: 4, TotalTokenCount: 1}, 10, 7},
		{"no_int32_overflow", genai.GenerateContentResponseUsageMetadata{PromptTokenCount: math.MaxInt32, ToolUsePromptTokenCount: math.MaxInt32, CandidatesTokenCount: math.MaxInt32, ThoughtsTokenCount: math.MaxInt32, TotalTokenCount: math.MaxInt32}, 2 * int64(math.MaxInt32), 2 * int64(math.MaxInt32)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, failed := range []bool{false, true} {
				r := &model.LLMResponse{Content: genai.NewContentFromText(`{"text":"incomplete"}`, "model"), UsageMetadata: &tc.usage}
				m := employeeUsageModel{responses: []*model.LLMResponse{r, r, {UsageMetadata: &genai.GenerateContentResponseUsageMetadata{}}}}
				if failed {
					m.err = errors.New("provider failed")
				}
				usage := store.BotUsage{InputTokens: 7, OutputTokens: 11, ToolCalls: 2}
				_, ok := employeeModelJSON(context.Background(), EmployeeMessageOptions{LLM: m, Usage: &usage, Boundary: func(context.Context) error { return nil }}, "instruction", "facts")
				if ok == failed {
					t.Fatalf("completion=%v, failed=%v", ok, failed)
				}
				if usage.InputTokens != 7+tc.input || usage.OutputTokens != 11+tc.output || usage.ToolCalls != 2 {
					t.Fatalf("usage=%+v; want added input=%d output=%d", usage, tc.input, tc.output)
				}
			}
		})
	}
}
