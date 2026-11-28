package launcher

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

func TestBotTranscriptSharedRendering(t *testing.T) {
	var got []events.Event
	mapper := botTranscriptMapper{}
	for i, wire := range []struct{ typ, payload string }{{"text", `{"text":"**report**"}`}, {"tool_call", `{"name":"read_file","id":"call1","args":{"path":"a"}}`}, {"tool_result", `{"name":"read_file","id":"call1","result":"ok"}`}, {"turn_complete", `{}`}} {
		out, err := mapper.mapFrame(runtimeTranscriptFrame(t, wire.typ, wire.payload, int64(i+1)))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, out...)
	}
	if len(got) != 5 || got[0].Kind != events.KindText || got[1].Kind != events.KindToolCall || got[2].Kind != events.KindStatus || got[3].ToolID != "call1" || got[4].Kind != events.KindDone {
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
