package api

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestBotOperationViewWireIsolation(t *testing.T) {
	v := store.BotOperationView{SchemaVersion: 1, BotID: uuid.New(), HomeSessionID: uuid.New(), OperationID: uuid.New(), Revision: 1, StateSequence: 2, Status: store.BotOperationPending, Decision: store.BotOperationExecute, Summary: "Change queued."}
	e, err := store.NewBotOperationEvent(v, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["mandate_ref"] = "secret"
	e.Actions.StateDelta[store.BotOperationKey] = m
	data, err := marshalBotTranscriptEvent(e, v.BotID, v.HomeSessionID)
	if err != nil || strings.Contains(string(data), "secret") {
		t.Fatalf("unsafe wire: %s %v", data, err)
	}
	if _, err = marshalBotTranscriptEvent(e, uuid.New(), v.HomeSessionID); err == nil {
		t.Fatal("cross bot accepted")
	}
	if _, err = marshalBotTranscriptEvent(e, v.BotID, uuid.New()); err == nil {
		t.Fatal("cross home accepted")
	}
	m["schema_version"] = 2
	if _, err = marshalBotTranscriptEvent(e, v.BotID, v.HomeSessionID); err == nil {
		t.Fatal("unknown version accepted")
	}
}
