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

func TestBotInitialTitleOwnerAnswerOptionAndCustomProjection(t *testing.T) {
	for _, answer := range []string{"forecast", "Compare rain forecasts for my private weekend trip 🙂"} {
		t.Run(answer, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"type": "bot_owner_answer", "approval_id": "title-approval", "response": map[string]string{"question_id": "title-question", "answer": answer}})
			if err != nil {
				t.Fatal(err)
			}
			row := session.Event{Author: "user"}
			row.Content = &genai.Content{Role: "user", Parts: []*genai.Part{{Text: string(body)}}}
			raw, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			// Both a live client and a fresh history mapper project one human answer.
			for _, restored := range []bool{false, true} {
				mapper := botTranscriptMapper{runtimeTurns: map[string]bool{"": true}}
				got, err := mapper.mapFrame(client.SSEEvent{Data: string(raw)})
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, e := range got {
					if e.Kind == events.KindUser {
						count++
						if e.Text != "Answer: "+answer {
							t.Fatalf("restored=%t: raw or altered answer: %+v", restored, e)
						}
					}
				}
				if count != 1 {
					t.Fatalf("restored=%t: answer count=%d", restored, count)
				}
			}
			if row.Content.Parts[0].Text != string(body) {
				t.Fatal("presentation rewrote durable answer")
			}
		})
	}
}
