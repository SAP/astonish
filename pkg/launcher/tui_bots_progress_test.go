package launcher

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	"testing"
)

func TestBotProgressMapperPrivacyAndLaterConversation(t *testing.T) {
	for _, mode := range []string{"live", "restore"} {
		t.Run(mode, func(t *testing.T) {
			m := botTranscriptMapper{}
			var sequence int64
			apply := func(typ, payload string) []events.Event {
				sequence++
				out, err := m.mapFrame(runtimeTranscriptFrame(t, typ, payload, sequence))
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			s := store.BotRunState{SchemaVersion: 1, BotID: uuid.New(), RunID: uuid.New(), SessionID: uuid.New(), StateSequence: 1, Status: store.RunRunning}
			s.Progress = &store.BotProgressProjection{SchemaVersion: 1, BotID: s.BotID, RunID: s.RunID, SessionID: s.SessionID, Revision: 1, Phase: store.BotProgressWorking}
			raw, _ := json.Marshal(s)
			if got := apply(store.BotRunStateKey, string(raw)); len(got) != 1 || got[0].Kind != events.KindBotRunState {
				t.Fatal("missing state")
			}
			// Privacy is enforced by the server projection. Hidden working rows arrive
			// as transport-only cursor frames, never as raw ADK text/tool payloads.
			for i := 0; i < 2; i++ {
				if got, err := m.mapFrame(client.SSEEvent{Type: "cursor", Data: `{}`}); err != nil || len(got) != 0 {
					t.Fatal("hidden row rendered")
				}
			}
			if got := apply("user_message", `{"text":"owner input"}`); len(got) != 1 || got[0].Kind != events.KindUser {
				t.Fatal("owner hidden")
			}
			report, _ := json.Marshal(map[string]any{"text": "final report", "invocationId": "bot-report:" + s.RunID.String()})
			if len(apply("text", string(report))) == 0 {
				t.Fatal("committed report hidden")
			}
			s.RunID = uuid.New()
			s.Progress = nil
			s.Presentation = store.BotRunPresentationHomeInteractive
			raw, _ = json.Marshal(s)
			apply(store.BotRunStateKey, string(raw))
			if got := apply("text", `{"text":"useful owner conversation","invocationId":"ordinary-conversation"}`); len(got) != 1 || got[0].Kind != events.KindText {
				t.Fatal("progress hid subsequent ordinary conversation")
			}
		})
	}
}
