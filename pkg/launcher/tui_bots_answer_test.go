package launcher

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/tui/events"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"testing"
)

func TestBotOwnerAnswerHumanReadableOnce(t *testing.T) {
	rawAnswer := `{"type":"bot_owner_answer","approval_id":"saved","response":{"question_id":"q","answer":"no"}}`
	row := session.Event{Author: "user"}
	row.Content = &genai.Content{Role: "user", Parts: []*genai.Part{{Text: rawAnswer}}}
	raw, _ := json.Marshal(row)
	mapper := botTranscriptMapper{runtimeTurns: map[string]bool{"": true}}
	got, err := mapper.mapFrame(client.SSEEvent{Data: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range got {
		if e.Kind == events.KindUser {
			count++
			if e.Text != "Answer: no" {
				t.Fatalf("raw answer: %+v", e)
			}
		}
	}
	if count != 1 {
		t.Fatalf("answer count %d: %+v", count, got)
	}
	if botOwnerAnswerText("ordinary text") != "ordinary text" {
		t.Fatal("ordinary text changed")
	}
}
