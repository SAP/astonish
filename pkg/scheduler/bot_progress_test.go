package scheduler

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestBotAdmissionCoalesced(t *testing.T) {
	for _, id := range []uuid.UUID{uuid.New(), uuid.Nil} {
		e := &Executor{BotTrigger: func(context.Context, BotTriggerRequest) (BotAdmission, error) {
			return BotAdmission{Status: BotAdmissionCoalesced, RunID: id, Acknowledgement: BotScheduleAcknowledged}, nil
		}}
		result, err := e.Execute(context.Background(), botTriggerTestJob())
		if id == uuid.Nil {
			if err == nil {
				t.Fatal("coalescing requires a durable run ID")
			}
		} else if err != nil || result != "" {
			t.Fatalf("coalesced receipt rejected: %q %v", result, err)
		}
	}
}
