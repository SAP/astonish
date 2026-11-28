package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotScheduleOperationPresentationWire(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       store.BotOperationStatus
		presentation store.BotOperationPresentation
	}{
		{"pending", store.BotOperationPending, store.BotOperationPresentationInternal},
		{"applying", store.BotOperationApplying, store.BotOperationPresentationInternal},
		{"applied", store.BotOperationApplied, store.BotOperationPresentationConversation},
		{"failed", store.BotOperationFailed, store.BotOperationPresentationConversation},
		{"blocked", store.BotOperationBlocked, store.BotOperationPresentationConversation},
		{"cancelled", store.BotOperationCancelled, store.BotOperationPresentationConversation},
		{"authorization", store.BotOperationBlocked, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			presentation := tc.presentation
			v := store.BotOperationView{SchemaVersion: 1, BotID: uuid.New(), HomeSessionID: uuid.New(), OperationID: uuid.New(), Revision: 1, StateSequence: 2, Operation: store.BotControlScheduleCreate, Status: tc.status, Decision: store.BotOperationExecute, Summary: "Change queued.", Presentation: presentation}
			if presentation == store.BotOperationPresentationConversation {
				v.Summary = "I couldn't complete the schedule change."
			}
			if tc.status == store.BotOperationApplied {
				next := time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC)
				v.VerifiedSchedule = &store.BotOperationScheduleView{TriggerID: uuid.New(), ScheduleID: uuid.New(), Version: 2, Enabled: true, NextRun: &next}
				v.Summary = "I've created the schedule. It runs every minute. Next run: 2026-11-10 23:00 UTC."
			}
			if presentation == "" {
				v.Status = store.BotOperationBlocked
				v.Decision = store.BotOperationRequireAuthorization
				v.Blocker = "Authorization required."
				v.Exception = &store.BotOperationExceptionView{OperationID: v.OperationID, PayloadDigest: "digest", ExpiresAt: time.Now().Add(time.Minute)}
			}
			event, err := store.NewBotOperationEvent(v, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			data, err := marshalBotTranscriptEvent(event, v.BotID, v.HomeSessionID)
			if err != nil {
				t.Fatal(err)
			}
			// The same canonical event goes through replay/SSE marshaling with the
			// optional field intact; authorization continues exposing its action.
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
			if got == nil {
				t.Fatalf("operation missing: %s", data)
			}
			if got["status"] != string(v.Status) || got["summary"] != v.Summary {
				t.Fatalf("operation outcome lost: %s", data)
			}
			if tc.status == store.BotOperationApplied {
				schedule, ok := got["verified_schedule"].(map[string]any)
				if !ok || schedule["schedule_id"] != v.VerifiedSchedule.ScheduleID.String() || schedule["next_run"] != v.VerifiedSchedule.NextRun.Format(time.RFC3339) {
					t.Fatalf("verified schedule lost: %s", data)
				}
			}
			if presentation == "" {
				if _, exists := got["presentation"]; exists || got["exception"] == nil {
					t.Fatalf("authorization presentation changed: %s", data)
				}
			} else if got["presentation"] != string(presentation) {
				t.Fatalf("presentation lost: %s", data)
			}
		})
	}
}
