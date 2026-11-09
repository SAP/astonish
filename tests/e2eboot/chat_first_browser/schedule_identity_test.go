package main

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestResolveAppliedSchedule(t *testing.T) {
	p := store.BotControlProposal{ID: uuid.New(), BotID: uuid.New(), Version: 3, Payload: store.BotControlPayload{Operation: store.BotControlScheduleCreate, Schedule: &store.BotControlSchedule{Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "* * * * *", Timezone: "UTC", Enabled: true, Instructions: "Reply exactly RETAINED_HOME_TICK, no tools or actions."}}}}
	b := store.Bot{ID: p.BotID, Version: 4}
	tr := p.Payload.Schedule.Trigger
	tr.ID = uuid.NewSHA1(p.ID, []byte("trigger"))
	sid := uuid.NewSHA1(p.ID, []byte("schedule"))
	tr.ScheduleID = &sid
	tr.Fingerprint = "server-fingerprint"
	v := store.BotVersion{BotID: p.BotID, Version: 4, Triggers: []store.TriggerSpec{tr}}
	got, err := resolveAppliedSchedule(p, b, v)
	if err != nil || got.ID != tr.ID {
		t.Fatalf("omitted ID must resolve authoritative generated UUID: got %s err %v", got.ID, err)
	}
	tests := map[string]func(*store.BotVersion){
		"different instructions": func(v *store.BotVersion) { v.Triggers[0].Instructions += " extra" },
		"different timezone":     func(v *store.BotVersion) { v.Triggers[0].Timezone = "Europe/Berlin" },
		"silent catchup":         func(v *store.BotVersion) { v.Triggers[0].CatchUpOnce = true },
		"unrelated schedule":     func(v *store.BotVersion) { id := uuid.New(); v.Triggers[0].ScheduleID = &id },
		"unrelated trigger":      func(v *store.BotVersion) { v.Triggers[0].ID = uuid.New() },
		"duplicate":              func(v *store.BotVersion) { v.Triggers = append(v.Triggers, v.Triggers[0]) },
		"old version":            func(v *store.BotVersion) { v.Version-- },
		"wrong bot":              func(v *store.BotVersion) { v.BotID = uuid.New() },
		"missing":                func(v *store.BotVersion) { v.Triggers = nil },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			bad := v
			bad.Triggers = append([]store.TriggerSpec(nil), v.Triggers...)
			change(&bad)
			if _, err := resolveAppliedSchedule(p, b, bad); err == nil {
				t.Fatal("must reject changed or ambiguous publication")
			}
		})
	}
}
