package launcher

import (
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/client"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestLocalCodeReasoningUsage(t *testing.T) {
	for _, tc := range []struct {
		name          string
		metadata      *genai.GenerateContentResponseUsageMetadata
		input, output int64
	}{
		{"reasoning", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 20, ThoughtsTokenCount: 80, TotalTokenCount: 110}, 10, 100},
		{"tool_prompt", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, ToolUsePromptTokenCount: 5, CandidatesTokenCount: 20, ThoughtsTokenCount: 80, TotalTokenCount: 115}, 15, 100},
		{"reasoning_only", &genai.GenerateContentResponseUsageMetadata{ThoughtsTokenCount: 100}, 0, 100},
		{"tool_prompt_only", &genai.GenerateContentResponseUsageMetadata{ToolUsePromptTokenCount: 5}, 5, 0},
		{"total_fallback", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 20, TotalTokenCount: 110}, 10, 100},
		{"empty", &genai.GenerateContentResponseUsageMetadata{}, 0, 0},
		{"absent", nil, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &localAgentBackend{}
			calls := 0
			emit := func(kind string, data map[string]any) {
				calls++
				if kind != "usage" || data["input_tokens"] != tc.input || data["output_tokens"] != tc.output || data["total_tokens"] != tc.input+tc.output {
					t.Fatalf("%s payload=%v, want input=%d output=%d", kind, data, tc.input, tc.output)
				}
				raw, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				mapped := mapSSEToEvents(&client.SSEEvent{Type: kind, Data: string(raw)}, false)
				if len(mapped) != 1 || mapped[0].Usage == nil {
					t.Fatalf("TUI events=%+v", mapped)
				}
				usage := mapped[0].Usage
				if usage.Input != tc.input || usage.Output != tc.output || usage.Total != tc.input+tc.output || usage.Estimated {
					t.Fatalf("TUI usage=%+v", usage)
				}
			}
			for range 2 {
				if b.emitUsage(&session.Event{LLMResponse: model.LLMResponse{Partial: true, UsageMetadata: tc.metadata}}, emit) {
					t.Fatal("partial counted")
				}
			}
			if calls != 0 {
				t.Fatal("partial emitted usage")
			}
			want := tc.input+tc.output > 0
			for n := 1; n <= 2; n++ {
				if got := b.emitUsage(&session.Event{LLMResponse: model.LLMResponse{UsageMetadata: tc.metadata}}, emit); got != want {
					t.Fatalf("emitUsage=%v, want %v", got, want)
				}
				if want && calls != n {
					t.Fatalf("calls=%d, want %d", calls, n)
				}
				if !want && calls != 0 {
					t.Fatal("empty metadata emitted usage")
				}
			}
		})
	}
}
