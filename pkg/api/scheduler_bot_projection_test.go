package api

import (
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

func TestSchedulerJobBotManagedProjection(t *testing.T) {
	for _, id := range []string{"", "bot-id"} {
		job := storeJobToAPIJob(&store.ScheduledJob{Payload: store.JobPayload{BotID: id, BotTriggerID: "trigger"}})
		if job.BotManaged != (id != "") || job.Payload.BotID != id {
			t.Fatalf("incorrect managed navigation projection: %+v", job)
		}
	}
}
