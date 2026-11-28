package api

import (
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestCollectUsageMatchesLiveReasoning(t *testing.T) {
	for _, tc := range []struct {
		name          string
		metadata      *genai.GenerateContentResponseUsageMetadata
		input, output int64
	}{
		{"reasoning", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 20, ThoughtsTokenCount: 80, TotalTokenCount: 110}, 10, 100},
		{"tool_prompt", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, ToolUsePromptTokenCount: 5, CandidatesTokenCount: 20, ThoughtsTokenCount: 80, TotalTokenCount: 115}, 15, 100},
		{"missing_total", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, ToolUsePromptTokenCount: 5, ThoughtsTokenCount: 100}, 15, 100},
		{"total_fallback", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 20, TotalTokenCount: 110}, 10, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := newChatRunner("usage", "user", "app", false)
			defer cr.cancel()
			transcript := testEvents{}
			for range 2 {
				partial := &session.Event{LLMResponse: model.LLMResponse{Partial: true, UsageMetadata: tc.metadata}}
				final := &session.Event{LLMResponse: model.LLMResponse{UsageMetadata: tc.metadata}}
				transcript = append(transcript, partial, partial, final)
				cr.emitUsage(partial)
				cr.emitUsage(partial)
				cr.emitUsage(final)
			}
			restored := collectUsage(transcript)
			if restored == nil || int64(restored.InputTokens) != 2*tc.input || int64(restored.OutputTokens) != 2*tc.output || int64(restored.TotalTokens) != 2*(tc.input+tc.output) {
				t.Fatalf("restored usage=%+v, want input=%d output=%d", restored, 2*tc.input, 2*tc.output)
			}
			if len(cr.events) != 2 {
				t.Fatalf("live usage events=%d", len(cr.events))
			}
			for _, e := range cr.events {
				if e.Data["input_tokens"] != tc.input || e.Data["output_tokens"] != tc.output || e.Data["total_tokens"] != tc.input+tc.output {
					t.Fatalf("live usage=%v", e.Data)
				}
			}
		})
	}
}

func TestCollectUsageNoCompletedUsage(t *testing.T) {
	for _, events := range []testEvents{
		{},
		{&session.Event{}},
		{&session.Event{LLMResponse: model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{}}}},
		{&session.Event{LLMResponse: model.LLMResponse{Partial: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{TotalTokenCount: 110}}}},
	} {
		if got := collectUsage(events); got != nil {
			t.Fatalf("usage=%+v, want nil", got)
		}
	}
}
