package store

import (
	"github.com/google/uuid"
	"testing"
)

func TestRoutineLifecyclePayloadBounds(t *testing.T) {
	id := uuid.New()
	for _, tc := range []struct {
		name  string
		p     BotControlPayload
		valid bool
	}{
		{"pause", BotControlPayload{Operation: BotControlPriorityPause, Reason: "owner stop"}, true},
		{"empty resume", BotControlPayload{Operation: BotControlResumeTasks, Reason: "resume", ResumeTargets: &BotControlResumeTargets{}}, false},
		{"duplicate", BotControlPayload{Operation: BotControlResumeTasks, Reason: "resume", ResumeTargets: &BotControlResumeTargets{ScheduleIDs: []uuid.UUID{id, id}}}, false},
		{"exact", BotControlPayload{Operation: BotControlResumeTasks, Reason: "resume", ResumeTargets: &BotControlResumeTargets{ScheduleIDs: []uuid.UUID{id}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if (tc.p.Validate() == nil) != tc.valid {
				t.Fatal("unexpected validation")
			}
		})
	}
}
