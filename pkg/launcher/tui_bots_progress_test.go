package launcher

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"testing"
)

func TestBotProgressMapperPrivacyAndLaterConversation(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "restore"}[replay], func(t *testing.T) {
			m := botTranscriptMapper{}
			apply := func(row session.Event) []events.Event {
				raw, _ := json.Marshal(row)
				out, err := m.mapFrame(client.SSEEvent{Data: string(raw)})
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			s := store.BotRunState{SchemaVersion: 1, BotID: uuid.New(), RunID: uuid.New(), SessionID: uuid.New(), StateSequence: 1, Status: store.RunRunning}
			s.Progress = &store.BotProgressProjection{SchemaVersion: 1, BotID: s.BotID, RunID: s.RunID, SessionID: s.SessionID, Revision: 1, Phase: store.BotProgressWorking}
			if got := apply(session.Event{Actions: session.EventActions{StateDelta: map[string]any{store.BotRunStateKey: s}}}); len(got) != 1 || got[0].Kind != events.KindBotRunState {
				t.Fatal("missing state")
			}
			row := session.Event{Author: "model"}
			row.Content = genai.NewContentFromText("private working draft", "model")
			if len(apply(row)) != 0 {
				t.Fatal("private draft leaked")
			}
			row.Content = &genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "private_tool"}}}}
			if len(apply(row)) != 0 {
				t.Fatal("tool leaked")
			}
			row.Author = "user"
			row.Content = genai.NewContentFromText("owner input", "user")
			if len(apply(row)) == 0 {
				t.Fatal("owner hidden")
			}
			row.Author = "model"
			row.InvocationID = "bot-report:" + s.RunID.String()
			row.Content = genai.NewContentFromText("final report", "model")
			if len(apply(row)) == 0 {
				t.Fatal("committed report hidden")
			}
			s.RunID = uuid.New()
			s.Progress = nil
			s.Presentation = store.BotRunPresentationHomeInteractive
			apply(session.Event{Actions: session.EventActions{StateDelta: map[string]any{store.BotRunStateKey: s}}})
			row.InvocationID = "ordinary-conversation"
			row.Content = genai.NewContentFromText("useful owner conversation", "model")
			if len(apply(row)) == 0 {
				t.Fatal("progress hid subsequent ordinary conversation")
			}
		})
	}
}
