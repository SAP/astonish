package launcher

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
	"testing"
)

func TestBotOperationWire(t *testing.T) {
	v := store.BotOperationView{SchemaVersion: 1, BotID: uuid.New(), HomeSessionID: uuid.New(), OperationID: uuid.New(), Revision: 1, StateSequence: 1, Status: store.BotOperationPending, Decision: store.BotOperationExecute}
	mapper := botTranscriptMapper{}
	for _, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationApplying, store.BotOperationApplied, store.BotOperationBlocked, store.BotOperationFailed, store.BotOperationCancelled} {
		v.Status = status
		row := adksession.Event{Actions: adksession.EventActions{StateDelta: map[string]any{store.BotOperationKey: v}}}
		raw, _ := json.Marshal(row)
		out, err := mapper.mapFrame(client.SSEEvent{Type: "transcript", Data: string(raw)})
		if err != nil || len(out) != 1 || out[0].Kind != events.KindBotOperation || out[0].BotOperation.Status != status {
			t.Fatalf("%s: %#v %v", status, out, err)
		}
	}
	v.SchemaVersion = 99
	row := adksession.Event{Actions: adksession.EventActions{StateDelta: map[string]any{store.BotOperationKey: v}}}
	raw, _ := json.Marshal(row)
	out, err := mapper.mapFrame(client.SSEEvent{Data: string(raw)})
	if err != nil || len(out) != 0 {
		t.Fatal("unknown version accepted")
	}
}
