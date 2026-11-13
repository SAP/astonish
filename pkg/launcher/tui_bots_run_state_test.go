package launcher

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"testing"
)

func TestBotRunStateDoesNotEnableRuntimeProjection(t *testing.T) {
	m := botTranscriptMapper{}
	row := session.Event{Actions: session.EventActions{StateDelta: map[string]any{store.BotRunStateKey: store.BotRunState{SchemaVersion: 1, Work: &store.BotWorkProjection{SchemaVersion: 1, Protocol: store.BotWorkProtocolAgentLedV1, WorkRevision: 3, Disposition: store.AgentWorkPartial}}}}}
	raw, _ := json.Marshal(row)
	got, err := m.mapFrame(client.SSEEvent{Data: string(raw)})
	if err != nil || len(got) != 1 || got[0].Kind != events.KindBotRunState || len(m.runtimeTurns) != 0 {
		t.Fatalf("state %+v %v runtime=%v", got, err, m.runtimeTurns)
	}
	if got[0].BotRunState.Work == nil || got[0].BotRunState.Work.WorkRevision != 3 || got[0].BotRunState.Work.Disposition != store.AgentWorkPartial {
		t.Fatalf("work projection lost in SSE mapping: %+v", got)
	}
	row = session.Event{Author: "model"}
	row.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "Useful draft"}}}
	raw, _ = json.Marshal(row)
	got, err = m.mapFrame(client.SSEEvent{Data: string(raw)})
	if err != nil || len(got) == 0 || got[0].Text != "Useful draft" {
		t.Fatalf("ADK suppressed %+v %v", got, err)
	}
}
