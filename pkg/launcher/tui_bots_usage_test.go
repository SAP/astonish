package launcher

import (
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/tui/events"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotTranscriptReasoningUsage(t *testing.T) {
	for _, metadata := range []*genai.GenerateContentResponseUsageMetadata{
		{PromptTokenCount: 10, ThoughtsTokenCount: 4096},
		{PromptTokenCount: 10, ThoughtsTokenCount: 4096, TotalTokenCount: 4106},
		{PromptTokenCount: 10, TotalTokenCount: 4106},
	} {
		e := adksession.Event{Author: "agent"}
		e.UsageMetadata = metadata
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		got, err := botTranscriptEvents(client.SSEEvent{Type: client.BotTranscriptEvent, Data: string(raw)})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Kind != events.KindUsage || got[0].Usage.Input != 10 || got[0].Usage.Output != 4096 || got[0].Usage.Total != 4106 {
			t.Fatalf("metadata=%+v events=%+v", metadata, got)
		}
	}
}
