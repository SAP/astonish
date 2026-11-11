package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBotScheduleOperationPresentation(t *testing.T) {
	b := Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	ref := BotMandateRef{ID: uuid.New(), BotID: b.ID, OrgSlug: "org", TeamSlug: "team", OwnerID: uuid.NewString(), Revision: 1}
	next := time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC)
	for _, operation := range []BotControlOperation{BotControlScheduleCreate, BotControlScheduleEdit, BotControlSchedulePause, BotControlScheduleRemove} {
		for _, status := range []BotOperationStatus{BotOperationPending, BotOperationApplying, BotOperationApplied, BotOperationFailed, BotOperationBlocked, BotOperationCancelled} {
			t.Run(string(operation)+"/"+string(status), func(t *testing.T) {
				op := BotOperation{ID: uuid.New(), MandateRef: ref, IdempotencyKey: "private-key", Revision: 1, Status: status, Decision: BotOperationExecute,
					Payload:    BotControlPayload{Operation: operation, Reason: "private-reason", Schedule: &BotControlSchedule{ExpectedRevision: 1, Trigger: TriggerSpec{ID: uuid.New(), Kind: TriggerCron, Cron: "* * * * *", Timezone: "UTC", Instructions: "private instructions", Enabled: true}}},
					Provenance: &BotOperationProvenance{SourceRunID: uuid.New(), UserEventID: uuid.NewString(), SourceDigest: "private-digest"}}
				if status == BotOperationApplied {
					op.Receipt = &BotOperationReceipt{Status: status, ObservedAt: time.Now(), Postconditions: []BotOperationPostcondition{{Resource: "private-resource", Revision: 1, Verified: true}}, Schedule: &BotOperationScheduleReceipt{TriggerID: op.Payload.Schedule.Trigger.ID, ScheduleID: uuid.New(), Version: 2, Enabled: operation != BotControlSchedulePause && operation != BotControlScheduleRemove, Removed: operation == BotControlScheduleRemove, SourceRunID: op.Provenance.SourceRunID, UserEventID: op.Provenance.UserEventID, SourceDigest: op.Provenance.SourceDigest}}
					if op.Receipt.Schedule.Enabled {
						op.Receipt.Schedule.NextRun = &next
					}
				}
				view, err := ProjectBotOperation(b, op, 10)
				if err != nil {
					t.Fatal(err)
				}
				want := BotOperationPresentationConversation
				if status == BotOperationPending || status == BotOperationApplying {
					want = BotOperationPresentationInternal
				}
				if view.Presentation != want {
					t.Fatalf("presentation %q want %q", view.Presentation, want)
				}
				if status == BotOperationApplied {
					expected := map[BotControlOperation]string{BotControlScheduleCreate: "I've created the schedule. It runs every minute.", BotControlScheduleEdit: "I've updated the schedule. It runs every minute.", BotControlSchedulePause: "I've paused the schedule.", BotControlScheduleRemove: "I've removed the schedule."}[operation]
					if !strings.HasPrefix(view.Summary, expected) {
						t.Fatalf("summary: %s", view.Summary)
					}
				}
				raw, _ := json.Marshal(view)
				if strings.Contains(string(raw), "private") {
					t.Fatalf("private fields leaked: %s", raw)
				}
				first, _ := NewBotOperationEvent(view, time.Now())
				old := view
				old.Presentation = ""
				prior, _ := NewBotOperationEvent(old, time.Now())
				if first.ID != prior.ID || first.Content != nil {
					t.Fatal("changed canonical event identity or duplicated prose content")
				}
				if status == BotOperationBlocked {
					op.Decision = BotOperationRequireAuthorization
					actionable, err := ProjectBotOperation(b, op, 11)
					if err != nil || actionable.Presentation != "" || actionable.Blocker == "" {
						t.Fatalf("authorization hidden: %+v %v", actionable, err)
					}
				}
			})
		}
	}
}

func TestBotOperationPresentationValidation(t *testing.T) {
	v := BotOperationView{SchemaVersion: 1, BotID: uuid.New(), HomeSessionID: uuid.New(), OperationID: uuid.New(), Revision: 1, StateSequence: 1, Status: BotOperationPending, Decision: BotOperationExecute, Operation: BotControlScheduleCreate, Presentation: BotOperationPresentationInternal}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*BotOperationView){
		func(v *BotOperationView) { v.Presentation = "unknown" },
		func(v *BotOperationView) { v.Operation = BotControlResumeTasks },
		func(v *BotOperationView) { v.Status = BotOperationFailed },
		func(v *BotOperationView) { v.Decision = BotOperationRequireAuthorization },
		func(v *BotOperationView) { v.Presentation = BotOperationPresentationConversation },
		func(v *BotOperationView) {
			v.Presentation = BotOperationPresentationConversation
			v.Status = BotOperationApplied
		},
	} {
		invalid := v
		change(&invalid)
		if invalid.Validate() == nil {
			t.Fatalf("invalid presentation accepted: %+v", invalid)
		}
	}
	v.Presentation = ""
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "presentation") {
		t.Fatal("legacy field not optional")
	}
}
