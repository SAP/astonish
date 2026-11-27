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
				if view.Presentation != BotOperationPresentationInternal {
					t.Fatalf("operation became conversation: %+v", view)
				}
				assertBotOperationNeutralSummary(t, view)
				if status == BotOperationApplied {
					s := view.VerifiedSchedule
					if s == nil || s.TriggerID != op.Receipt.Schedule.TriggerID || s.ScheduleID != op.Receipt.Schedule.ScheduleID || s.Version != 2 || s.Enabled != op.Receipt.Schedule.Enabled || s.Removed != op.Receipt.Schedule.Removed {
						t.Fatalf("verified schedule lost: %+v", s)
					}
					if s.Enabled && (s.NextRun == nil || !s.NextRun.Equal(next)) {
						t.Fatalf("verified next run lost: %+v", s)
					}
					for _, change := range []func(*BotOperationScheduleReceipt){
						func(s *BotOperationScheduleReceipt) { s.SourceRunID = uuid.New() },
						func(s *BotOperationScheduleReceipt) { s.UserEventID = uuid.NewString() },
						func(s *BotOperationScheduleReceipt) { s.SourceDigest = "mismatched-digest" },
					} {
						invalid := op
						receipt := *op.Receipt
						schedule := *op.Receipt.Schedule
						change(&schedule)
						receipt.Schedule = &schedule
						invalid.Receipt = &receipt
						if _, err := ProjectBotOperation(b, invalid, 11); err == nil {
							t.Fatal("mismatched source receipt accepted")
						}
					}
				}
				raw, _ := json.Marshal(view)
				if strings.Contains(string(raw), "private") {
					t.Fatalf("private fields leaked: %s", raw)
				}
				first, err := NewBotOperationEvent(view, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				old := view
				old.Presentation = ""
				prior, err := NewBotOperationEvent(old, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if first.ID != prior.ID || first.Content != nil || first.Author != "bot_runtime" {
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

func assertBotOperationNeutralSummary(t *testing.T, view BotOperationView) {
	t.Helper()
	want := map[BotOperationStatus]string{
		BotOperationPending:   "Change queued.",
		BotOperationApplying:  "Applying change.",
		BotOperationApplied:   "Change verified and applied.",
		BotOperationFailed:    "Change could not be completed.",
		BotOperationBlocked:   "Change needs attention.",
		BotOperationCancelled: "Change cancelled.",
	}[view.Status]
	if want == "" || view.Summary != want {
		t.Fatalf("expected neutral product summary %q, got %q", want, view.Summary)
	}
}

func TestBotOperationPresentationValidation(t *testing.T) {
	v := BotOperationView{SchemaVersion: 1, BotID: uuid.New(), HomeSessionID: uuid.New(), OperationID: uuid.New(), Revision: 1, StateSequence: 1, Status: BotOperationPending, Decision: BotOperationExecute, Operation: BotControlScheduleCreate, Presentation: BotOperationPresentationInternal}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, status := range []BotOperationStatus{BotOperationApplied, BotOperationFailed, BotOperationBlocked, BotOperationCancelled} {
		t.Run("valid_terminal_internal/"+string(status), func(t *testing.T) {
			terminal := v
			terminal.Status = status
			if err := terminal.Validate(); err != nil {
				t.Fatalf("neutral terminal state rejected: %+v %v", terminal, err)
			}
		})
	}
	for _, change := range []func(*BotOperationView){
		func(v *BotOperationView) { v.Presentation = "unknown" },
		func(v *BotOperationView) { v.Operation = BotControlResumeTasks },
		func(v *BotOperationView) { v.Operation = "unknown" },
		func(v *BotOperationView) { v.Status = "unknown" },
		func(v *BotOperationView) { v.Decision = "unknown" },
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
