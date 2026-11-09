package launcher

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotTranscriptSharedRendering(t *testing.T) {
	e := adksession.Event{Author: "agent"}
	e.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "**report**"}, {FunctionCall: &genai.FunctionCall{Name: "read_file", ID: "call1", Args: map[string]any{"path": "a"}}}, {FunctionResponse: &genai.FunctionResponse{Name: "read_file", ID: "call1", Response: map[string]any{"result": "ok"}}}}}
	raw, _ := json.Marshal(e)
	got, err := botTranscriptEvents(client.SSEEvent{Type: client.BotTranscriptEvent, Data: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Kind != events.KindText || got[1].Kind != events.KindToolCall || got[2].ToolID != "call1" || got[3].Kind != events.KindDone {
		t.Fatalf("%+v", got)
	}
	tr := events.NewTranscript()
	for _, e := range got {
		tr.Apply(e)
	}
	if len(tr.Items) == 0 {
		t.Fatal("shared reducer empty")
	}
}
func TestBotBackendRemoteReadOnlyAndExactDecision(t *testing.T) {
	b := newBotPlatformBackend(&platformBackend{}, "personal")
	id := uuid.New()
	b.selected = &store.Bot{ID: id, Name: "Monitor", Status: store.BotActive}
	if b.Info().Mode != "platform" {
		t.Fatal("third mode added")
	}
	if _, err := b.RunTurn(context.Background(), "yes", backend.TurnOptions{}); err == nil {
		t.Fatal("watch allowed chat execution")
	}
	if err := b.DecideBotApproval(context.Background(), store.BotApproval{BotID: uuid.New()}, store.ApprovalApproved); err == nil {
		t.Fatal("cross bot decision allowed")
	}
	if _, _, err := b.SetModelPin(context.Background(), "p", "m"); err == nil {
		t.Fatal("Bot model mutation allowed")
	}
	b.DetachBot()
	if b.SelectedBot() != nil {
		t.Fatal("detach failed")
	}
	if _, ok := any(&localAgentBackend{}).(backend.BotBackend); ok {
		t.Fatal("local code exposes remote Bots")
	}
}
