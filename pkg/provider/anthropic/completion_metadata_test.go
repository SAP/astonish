package anthropic

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestNativeTerminationMetadata(t *testing.T) {
	for _, tc := range []struct {
		reason   string
		finish   genai.FinishReason
		complete bool
	}{
		{"end_turn", genai.FinishReasonStop, true},
		{"stop_sequence", genai.FinishReasonStop, true},
		{"tool_use", genai.FinishReasonOther, false},
		{"pause_turn", genai.FinishReasonOther, false},
		{"max_tokens", genai.FinishReasonMaxTokens, false},
		{"model_context_window_exceeded", genai.FinishReasonMaxTokens, false},
		{"refusal", genai.FinishReasonSafety, false},
		{"", genai.FinishReasonOther, false},
		{"new_reason", genai.FinishReasonOther, false},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			var got *model.LLMResponse
			(&Provider{}).handleResponse(strings.NewReader(fmt.Sprintf(`{"content":[{"type":"text","text":"answer"}],"stop_reason":%q}`, tc.reason)), func(r *model.LLMResponse, err error) bool {
				if err != nil {
					t.Fatal(err)
				}
				got = r
				return true
			})
			if got == nil || got.FinishReason != tc.finish || got.TurnComplete != tc.complete {
				t.Fatalf("response=%+v want finish=%s complete=%v", got, tc.finish, tc.complete)
			}
		})
	}
}

func TestNativeStreamFailureMetadata(t *testing.T) {
	for _, tc := range []struct {
		reason string
		finish genai.FinishReason
	}{
		{"max_tokens", genai.FinishReasonMaxTokens},
		{"model_context_window_exceeded", genai.FinishReasonMaxTokens},
		{"refusal", genai.FinishReasonSafety},
		{"unknown_reason", genai.FinishReasonOther},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			input := safetySSE(
				`{"type":"message_start","message":{"usage":{"input_tokens":11}}}`,
				safetyToolStart, safetyToolStop,
				fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":7}}`, tc.reason),
				safetyStop,
			)
			failures := 0
			(&Provider{}).handleStream(strings.NewReader(input), func(r *model.LLMResponse, err error) bool {
				if err == nil {
					t.Fatal("failed termination published executable content")
				}
				failures++
				if r == nil || r.FinishReason != tc.finish || r.TurnComplete || r.Content != nil {
					t.Fatalf("failure response=%+v; want finish=%s, no completion or tools", r, tc.finish)
				}
				if r.UsageMetadata == nil || r.UsageMetadata.PromptTokenCount != 11 || r.UsageMetadata.CandidatesTokenCount != 7 {
					t.Fatalf("lost failure usage: %+v", r.UsageMetadata)
				}
				return true
			})
			if failures != 1 {
				t.Fatalf("failures=%d, want 1", failures)
			}
		})
	}
}
func TestNativeStreamIncompleteTerminationNotCertified(t *testing.T) {
	for _, suffix := range []string{"", safetySSE(`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`, safetyStop)} {
		input := safetySSE(safetyToolStart, safetyToolStop, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) + suffix
		failures := 0
		(&Provider{}).handleStream(strings.NewReader(input), func(r *model.LLMResponse, err error) bool {
			if err == nil || r == nil || r.TurnComplete || r.FinishReason == genai.FinishReasonStop || r.Content != nil {
				t.Fatalf("incomplete/conflicting termination certified: response=%+v err=%v", r, err)
			}
			failures++
			return true
		})
		if failures != 1 {
			t.Fatalf("failures=%d, want 1", failures)
		}
	}
}

func TestNativeToolHandoffMetadata(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var got *model.LLMResponse
			yield := func(r *model.LLMResponse, err error) bool {
				if err != nil {
					t.Fatal(err)
				}
				got = r
				return true
			}
			p := &Provider{}
			if stream {
				p.handleStream(strings.NewReader(safetySSE(safetyToolStart, safetyToolStop, `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`, safetyStop)), yield)
			} else {
				p.handleResponse(strings.NewReader(`{"content":[{"type":"tool_use","id":"call-1","name":"read_file","input":{"path":"/test"}}],"stop_reason":"tool_use"}`), yield)
			}
			if got == nil || got.TurnComplete || got.FinishReason != genai.FinishReasonOther || got.Content == nil || len(got.Content.Parts) != 1 || got.Content.Parts[0].FunctionCall == nil {
				t.Fatalf("handoff lost tools or certified final: %+v", got)
			}
		})
	}
}

func TestNativeStreamCompletionMetadata(t *testing.T) {
	for _, reason := range []string{"end_turn", "stop_sequence", "tool_use", "pause_turn", ""} {
		t.Run(reason, func(t *testing.T) {
			input := "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"answer\"}}\n\n" +
				"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
				fmt.Sprintf("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q}}\n\n", reason) +
				"data: {\"type\":\"message_stop\"}\n\n"
			var got *model.LLMResponse
			(&Provider{}).handleStream(strings.NewReader(input), func(r *model.LLMResponse, err error) bool {
				if err != nil {
					t.Fatal(err)
				}
				if !r.Partial {
					got = r
				}
				return true
			})
			complete := reason == "end_turn" || reason == "stop_sequence"
			if got == nil || got.TurnComplete != complete || (got.FinishReason == genai.FinishReasonStop) != complete {
				t.Fatalf("response=%+v complete=%v", got, complete)
			}
		})
	}
}
