package launcher

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	"testing"
)

func TestBotOperationWire(t *testing.T) {
	v := store.BotOperationView{SchemaVersion: 1, BotID: uuid.New(), HomeSessionID: uuid.New(), OperationID: uuid.New(), Revision: 1, StateSequence: 1, Status: store.BotOperationPending, Decision: store.BotOperationExecute}
	mapper := botTranscriptMapper{}
	for i, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationApplying, store.BotOperationApplied, store.BotOperationBlocked, store.BotOperationFailed, store.BotOperationCancelled} {
		v.Status = status
		raw, _ := json.Marshal(v)
		out, err := mapper.mapFrame(runtimeTranscriptFrame(t, store.BotOperationKey, string(raw), int64(i+1)))
		if err != nil || len(out) != 1 || out[0].Kind != events.KindBotOperation || out[0].BotOperation.Status != status {
			t.Fatalf("%s: %#v %v", status, out, err)
		}
	}
	v.SchemaVersion = 99
	raw, _ := json.Marshal(v)
	out, err := mapper.mapFrame(runtimeTranscriptFrame(t, store.BotOperationKey, string(raw), 7))
	if err == nil || len(out) != 0 {
		t.Fatal("unknown version accepted")
	}
}
