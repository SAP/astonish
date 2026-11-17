package openai

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/provider/llmerror"
	"google.golang.org/adk/model"
)

func TestChatCompletionReasoningAndInvalidTools(t *testing.T) {
	for _, tt := range []struct {
		name, finish string
		message      map[string]any
		recoverable  bool
	}{
		{"reasoning_length", "length", map[string]any{"reasoning_content": "private thought"}, true},
		{"reasoning_stop", "stop", map[string]any{"reasoning_content": "private thought"}, true},
		{"bad_tool_arguments", "tool_calls", map[string]any{"tool_calls": []any{map[string]any{"id": "a", "function": map[string]any{"name": "lookup", "arguments": "null"}}}}, false},
		{"legacy_tool", "stop", map[string]any{"function_call": map[string]any{"name": "lookup", "arguments": "{}"}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": tt.finish, "message": tt.message}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}})
			p, _ := safetyProvider(string(raw), false)
			count := 0
			for response, err := range p.GenerateContent(context.Background(), &model.LLMRequest{}, false) {
				count++
				if err == nil || llmerror.IsRecoverableEmptyCompletion(err) != tt.recoverable {
					t.Errorf("error=%v recovery=%v", err, llmerror.IsRecoverableEmptyCompletion(err))
				}
				if response == nil || response.UsageMetadata == nil || response.UsageMetadata.TotalTokenCount != 30 {
					t.Fatalf("response=%+v", response)
				}
				if response.Content != nil {
					for _, part := range response.Content.Parts {
						if part.FunctionCall != nil {
							t.Error("invalid tool batch published")
						}
						if part.Text != "" && !part.Thought {
							t.Error("thought promoted to visible text")
						}
					}
				}
			}
			if count != 1 {
				t.Errorf("yields=%d", count)
			}
		})
	}
}
