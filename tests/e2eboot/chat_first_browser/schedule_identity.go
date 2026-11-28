package main

import (
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"reflect"
)

// Read identity from the applied immutable version, never the pre-apply payload.
// Schedule binding is derived from the exact reviewed proposal by the server's
// applyControlScheduleTx contract; similar cron text is not identity evidence.
func resolveAppliedSchedule(p store.BotControlProposal, current store.Bot, version store.BotVersion) (store.TriggerSpec, error) {
	fail := func() (store.TriggerSpec, error) {
		return store.TriggerSpec{}, fmt.Errorf("applied schedule identity/scope mismatch or ambiguous current publication")
	}
	if p.ID == uuid.Nil || p.Payload.Operation != store.BotControlScheduleCreate || p.Payload.Schedule == nil || current.ID != p.BotID || version.BotID != p.BotID || current.Version != p.Version+1 || version.Version != current.Version {
		return fail()
	}
	expected := p.Payload.Schedule.Trigger
	if expected.ID == uuid.Nil {
		expected.ID = uuid.NewSHA1(p.ID, []byte("trigger"))
	}
	sid := uuid.NewSHA1(p.ID, []byte("schedule"))
	if expected.ScheduleID != nil {
		return fail()
	}
	expected.ScheduleID = &sid
	// This workflow reviewed precisely one schedule. Extra/duplicate schedules
	// cannot silently broaden activation, even if one candidate matches.
	if len(version.Triggers) != 1 {
		return fail()
	}
	applied := version.Triggers[0]
	// Fingerprint is server-computed metadata, not a reviewed mutable field.
	expected.Fingerprint = applied.Fingerprint
	if applied.ID == uuid.Nil || applied.Fingerprint == "" || !reflect.DeepEqual(expected, applied) {
		return fail()
	}
	return applied, nil
}
