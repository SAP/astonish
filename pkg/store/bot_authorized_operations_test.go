package store

import (
	"encoding/json"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotOperationScheduleReceiptClone(t *testing.T) {
	now := time.Now().UTC()
	r := BotOperationReceipt{Status: BotOperationApplied, ObservedAt: now, Schedule: &BotOperationScheduleReceipt{TriggerID: uuid.New(), ScheduleID: uuid.New(), Version: 2, Enabled: true, NextRun: &now, SourceRunID: uuid.New(), UserEventID: "user-event", SourceDigest: "digest"}, Postconditions: []BotOperationPostcondition{{Resource: "schedule", Revision: 2, Verified: true}}}
	copy := r.Clone()
	*copy.Schedule.NextRun = now.Add(time.Hour)
	copy.Schedule.Enabled = false
	copy.Postconditions[0].Resource = "other"
	if !r.Schedule.NextRun.Equal(now) || !r.Schedule.Enabled || r.Postconditions[0].Resource != "schedule" {
		t.Fatal("clone aliases source")
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded BotOperationReceipt
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Schedule.SourceRunID != r.Schedule.SourceRunID || decoded.Schedule.UserEventID != "user-event" {
		t.Fatal("lost provenance")
	}
	var legacy BotOperationReceipt
	if err = json.Unmarshal([]byte(`{"status":"pending"}`), &legacy); err != nil || legacy.Schedule != nil || legacy.Validate() != nil {
		t.Fatal("legacy receipt incompatible")
	}
}
