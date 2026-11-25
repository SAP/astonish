package launcher

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/tui/events"
	"testing"
)

func TestBotOwnerAnswerHumanReadableOnce(t *testing.T) {
	m := botTranscriptMapper{}
	frame := runtimeTranscriptFrame(t, "user_message", `{"text":"Answer: no"}`, 1)
	got, err := m.mapFrame(frame)
	if err != nil || len(got) != 1 || got[0].Kind != events.KindUser || got[0].Text != "Answer: no" {
		t.Fatalf("answer: %+v %v", got, err)
	}
	got, err = m.mapFrame(frame)
	if err != nil || len(got) != 0 {
		t.Fatalf("replayed answer: %+v %v", got, err)
	}
}

func TestBotInitialTitleOwnerAnswerOptionAndCustomProjection(t *testing.T) {
	for _, answer := range []string{"forecast", "Compare rain forecasts for my private weekend trip 🙂"} {
		data, _ := json.Marshal(map[string]string{"text": "Answer: " + answer})
		frame := runtimeTranscriptFrame(t, "user_message", string(data), 1)
		for i := 0; i < 2; i++ {
			m := botTranscriptMapper{}
			got, err := m.mapFrame(frame)
			if err != nil || len(got) != 1 || got[0].Kind != events.KindUser || got[0].Text != "Answer: "+answer {
				t.Fatalf("answer: %+v %v", got, err)
			}
		}
	}
}
