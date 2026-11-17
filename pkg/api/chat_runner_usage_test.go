package api

import (
	"fmt"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestChatReasoningUsageHTTP(t *testing.T) {
	metadata := &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 20, ThoughtsTokenCount: 80, TotalTokenCount: 110}
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(
		&MockTurn{Parts: []*genai.Part{{Text: "Thinking", Thought: true}}, Partial: true, UsageMetadata: metadata},
		&MockTurn{Parts: []*genai.Part{{Text: "Answer"}}, TurnComplete: true, UsageMetadata: metadata},
	), nil)
	events := env.Turn(t, "", "Report usage", false)
	count := 0
	for _, event := range events {
		if event.Type != "usage" {
			continue
		}
		count++
		if fmt.Sprint(event.Data["input_tokens"]) != "10" || fmt.Sprint(event.Data["output_tokens"]) != "100" || fmt.Sprint(event.Data["total_tokens"]) != "110" {
			t.Fatalf("SSE usage = %+v", event.Data)
		}
	}
	if count != 1 {
		t.Fatalf("usage events=%d, want 1", count)
	}
	assertHasEvent(t, events, "done")
}

func TestChatCompletionReasoningUsage(t *testing.T) {
	cases := []struct {
		name          string
		metadata      *genai.GenerateContentResponseUsageMetadata
		input, output int64
	}{
		{"split_completion", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 20, ThoughtsTokenCount: 80, TotalTokenCount: 110}, 10, 100},
		{"pure_reasoning", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, ThoughtsTokenCount: 100, TotalTokenCount: 110}, 10, 100},
		{"tool_prompt_and_total_fallback", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, ToolUsePromptTokenCount: 5, CandidatesTokenCount: 20, ThoughtsTokenCount: 60, TotalTokenCount: 115}, 15, 100},
		{"missing_total", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, ToolUsePromptTokenCount: 5, ThoughtsTokenCount: 100}, 15, 100},
		{"no_usage", nil, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := newChatRunner("usage", "user", "app", false)
			defer cr.cancel()
			// Streaming usage snapshots are cumulative, not independent calls.
			for range 2 {
				event := &session.Event{LLMResponse: model.LLMResponse{Partial: true, UsageMetadata: tc.metadata}}
				cr.observeCompletion(event)
				cr.emitUsage(event)
			}
			if usage := cr.Completion().Usage; usage.InputTokens != 0 || usage.OutputTokens != 0 {
				t.Fatalf("partial snapshots counted: %+v", usage)
			}
			if len(cr.events) != 0 {
				t.Fatalf("partial usage emitted: %+v", cr.events)
			}
			for n := int64(1); n <= 2; n++ {
				event := &session.Event{LLMResponse: model.LLMResponse{UsageMetadata: tc.metadata}}
				cr.observeCompletion(event)
				cr.emitUsage(event)
				if tc.metadata != nil {
					if len(cr.events) != int(n) {
						t.Fatalf("usage events=%d, want %d", len(cr.events), n)
					}
					data := cr.events[n-1].Data
					if data["input_tokens"] != tc.input || data["output_tokens"] != tc.output || data["total_tokens"] != tc.input+tc.output {
						t.Fatalf("live usage=%v, want input=%d output=%d total=%d", data, tc.input, tc.output, tc.input+tc.output)
					}
				} else if len(cr.events) != 0 {
					t.Fatal("missing metadata emitted usage")
				}
				usage := cr.Completion().Usage
				if usage.InputTokens != n*tc.input || usage.OutputTokens != n*tc.output {
					t.Fatalf("call %d usage=(%d,%d), want=(%d,%d)", n, usage.InputTokens, usage.OutputTokens, n*tc.input, n*tc.output)
				}
			}
		})
	}
}
