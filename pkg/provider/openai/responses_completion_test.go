package openai

import (
	"encoding/json"
	"testing"

	goopenai "github.com/sashabaranov/go-openai"
	"google.golang.org/genai"
)

func TestResponsesCompletionStatusPreserved(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed", "in_progress", "queued", "cancelled", ""} {
		t.Run(status, func(t *testing.T) {
			var upstream goopenai.CreateResponseResponse
			raw := `{"status":"` + status + `","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"visible answer"}]}]}`
			if err := json.Unmarshal([]byte(raw), &upstream); err != nil {
				t.Fatal(err)
			}
			got, err := responseToLLMResponse(upstream)
			if status != "completed" {
				if err == nil || got != nil {
					t.Fatalf("unconfirmed status certified: %q %+v %v", status, got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !got.TurnComplete || got.FinishReason != genai.FinishReasonStop || got.Partial || got.Content.Parts[0].Text != "visible answer" {
				t.Fatalf("completion metadata lost: %+v", got)
			}
		})
	}
}
