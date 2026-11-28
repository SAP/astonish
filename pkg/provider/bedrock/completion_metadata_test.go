package bedrock

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestTerminationMetadata(t *testing.T) {
	for _, tc := range []struct {
		reason   string
		finish   genai.FinishReason
		complete bool
	}{
		{"end_turn", genai.FinishReasonStop, true}, {"stop_sequence", genai.FinishReasonStop, true},
		{"tool_use", genai.FinishReasonOther, false}, {"pause_turn", genai.FinishReasonOther, false},
		{"max_tokens", genai.FinishReasonMaxTokens, false}, {"model_context_window_exceeded", genai.FinishReasonMaxTokens, false},
		{"refusal", genai.FinishReasonSafety, false}, {"", genai.FinishReasonOther, false}, {"unknown", genai.FinishReasonOther, false},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			r, err := ParseResponse([]byte(fmt.Sprintf(`{"content":[{"type":"text","text":"answer"}],"stop_reason":%q}`, tc.reason)))
			if err != nil {
				t.Fatal(err)
			}
			if r.FinishReason != tc.finish || r.TurnComplete != tc.complete {
				t.Fatalf("response=%+v", r)
			}
			input := "data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"text\",\"text\":\"answer\"}}\n\n" +
				fmt.Sprintf("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q}}\n\n", tc.reason) + "data: {\"type\":\"message_stop\"}\n\n"
			wantError := tc.reason != "" && tc.reason != "end_turn" && tc.reason != "stop_sequence" && tc.reason != "tool_use"
			final, failed := false, false
			for r, err := range ParseStream(strings.NewReader(input)) {
				if err != nil {
					failed = true
					continue
				}
				if r.Partial {
					if r.TurnComplete {
						t.Fatal("partial marked complete")
					}
					continue
				}
				final = true
				if r.FinishReason != tc.finish || r.TurnComplete != tc.complete {
					t.Fatalf("stream response=%+v", r)
				}
			}
			if failed != wantError || final == wantError {
				t.Fatalf("final=%v failed=%v wantError=%v", final, failed, wantError)
			}
		})
	}
}
