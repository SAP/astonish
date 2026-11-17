package launcher

import (
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/tui/events"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// The shared Home HTTP projection sends released narration as complete content,
// and historical telemetry as identity-only rows. Exercise the real terminal
// mapper/reducer before any final report arrives, then replay from scratch.
func TestBotHomeNarrativeTerminalBeforeFinal(t *testing.T) {
	const ack = "I'll prepare a concise weekly AI roundup."
	rows := []session.Event{
		{ID: "answer", Author: "user", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Weekly roundup", "user")}},
		{ID: "ack", Author: "bot_runtime", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(ack, "model")}},
		{ID: "historical-private-tool", Author: "bot_runtime"},
	}
	for _, mode := range []string{"live", "reload"} {
		t.Run(mode, func(t *testing.T) {
			mapper := botTranscriptMapper{}
			transcript := &events.Transcript{}
			for _, row := range rows {
				raw, _ := json.Marshal(row)
				mapped, err := mapper.mapFrame(client.SSEEvent{Type: client.BotTranscriptEvent, Data: string(raw)})
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range mapped {
					transcript.Apply(e)
				}
			}
			if len(transcript.Items) != 2 || transcript.Items[1].Kind != events.ItemAgent || transcript.Items[1].Content != ack || transcript.Items[1].Provisional {
				t.Fatalf("acknowledgement hidden or tool leaked: %+v", transcript.Items)
			}
		})
	}
}
