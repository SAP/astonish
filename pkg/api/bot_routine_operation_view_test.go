package api

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotRoutineOperationPresentationWire(t *testing.T) {
	for _, kind := range []store.BotControlOperation{store.BotControlResponsibility, store.BotControlPersona, store.BotControlGoalCreate, store.BotControlGoalEdit, store.BotControlMemoryRemember, store.BotControlMemoryCorrect, store.BotControlMemoryForget} {
		for _, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationApplying, store.BotOperationApplied, store.BotOperationFailed, store.BotOperationBlocked, store.BotOperationCancelled} {
			t.Run(string(kind)+"/"+string(status), func(t *testing.T) {
				presentation := store.BotOperationPresentationConversation
				if status == store.BotOperationPending || status == store.BotOperationApplying {
					presentation = store.BotOperationPresentationInternal
				}
				v := store.BotOperationView{SchemaVersion: 1, BotID: uuid.New(), HomeSessionID: uuid.New(), OperationID: uuid.New(), SourceRunID: uuid.New(), SourceUserEventID: uuid.NewString(), Revision: 2, StateSequence: 3, Operation: kind, Status: status, Decision: store.BotOperationExecute, Presentation: presentation, Summary: "I've updated the requested information."}
				event, err := store.NewBotOperationEvent(v, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				data, err := marshalBotTranscriptEvent(event, v.BotID, v.HomeSessionID)
				if err != nil {
					t.Fatal(err)
				}
				var wire map[string]any
				if err := json.Unmarshal(data, &wire); err != nil {
					t.Fatal(err)
				}
				var find func(any) map[string]any
				find = func(value any) map[string]any {
					m, ok := value.(map[string]any)
					if !ok {
						return nil
					}
					if m["operation_id"] == v.OperationID.String() && m["schema_version"] != nil {
						return m
					}
					for _, nested := range m {
						if found := find(nested); found != nil {
							return found
						}
					}
					return nil
				}
				got := find(wire)
				if got == nil || got["presentation"] != string(presentation) || got["summary"] != v.Summary || got["source_run_id"] != v.SourceRunID.String() || got["source_user_event_id"] != v.SourceUserEventID {
					t.Fatalf("wire lost scoped presentation: %s", data)
				}
			})
		}
	}
}
