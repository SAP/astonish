package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// The saved failure read was version 2 with one job; the later read-only
// SQLite evidence has both version-3 jobs. Neither is an all-Bot API response.
func TestEmployeeLegacySavedStorageSchedule(t *testing.T) {
	raw, err := os.ReadFile("testdata/employee_legacy_schedule.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Jobs      []json.RawMessage
		Bot       store.Bot
		Version   store.BotVersion
		StaleJobs []json.RawMessage `json:"stale_disk_jobs"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	old, current := fixture.Version.Triggers[0], fixture.Version.Triggers[1]
	f := employeeLegacyFixture{BotID: fixture.Bot.ID, HomeSessionID: fixture.Bot.HomeSessionID, TriggerID: old.ID, ScheduleID: *old.ScheduleID}
	validate := func(jobs []json.RawMessage) error {
		return employeeLegacyNewSchedule(employeeDisk{Jobs: jobs}, fixture.Version, fixture.Bot, f, current.ID, *current.ScheduleID, current.Cron)
	}
	if err := validate(fixture.Jobs); err != nil {
		t.Fatal(err)
	}
	reads := 0
	probe := employeeLegacyProbe{
		fixture: f, original: old, newTrigger: current.ID, newSchedule: *current.ScheduleID, cron: current.Cron,
		snapshot: func() store.BotSnapshot { return store.BotSnapshot{Bot: fixture.Bot} },
		capture:  func(string, string) string { return employeeDiskJSON(t, fixture.Version) },
		disk: func(string) employeeDisk {
			reads++
			if reads == 1 {
				return employeeDisk{Jobs: fixture.StaleJobs}
			}
			return employeeDisk{Jobs: fixture.Jobs}
		},
	}
	probe.observe("schedule")
	if reads != 2 || len(probe.stages) != 1 {
		t.Fatalf("did not reread stale storage: reads=%d stages=%v", reads, probe.stages)
	}
	if validate(fixture.StaleJobs) == nil {
		t.Fatal("stale missing schedule accepted")
	}
	for _, name := range []string{"duplicate-trigger", "missing-historical-trigger", "foreign-bot", "foreign-version"} {
		t.Run(name, func(t *testing.T) {
			v, b := fixture.Version, fixture.Bot
			v.Triggers = append([]store.TriggerSpec{}, v.Triggers...)
			switch name {
			case "duplicate-trigger":
				v.Triggers[0] = v.Triggers[1]
			case "missing-historical-trigger":
				v.Triggers = v.Triggers[1:]
			case "foreign-bot":
				b.ID = current.ID
			case "foreign-version":
				v.BotID = current.ID
			}
			if employeeLegacyNewSchedule(employeeDisk{Jobs: fixture.Jobs}, v, b, f, current.ID, *current.ScheduleID, current.Cron) == nil {
				t.Fatal("corrupt identity accepted")
			}
		})
	}
	for _, name := range []string{"duplicate-new", "duplicate-old", "missing-old", "foreign", "invalid-time", "empty-time", "zero-time"} {
		t.Run(name, func(t *testing.T) {
			jobs := append([]json.RawMessage{}, fixture.Jobs...)
			switch name {
			case "duplicate-new":
				jobs = append(jobs, jobs[0])
			case "duplicate-old":
				jobs = append(jobs, jobs[1])
			case "missing-old":
				jobs = jobs[:1]
			case "foreign":
				var row map[string]any
				_ = json.Unmarshal(jobs[1], &row)
				row["payload"] = `{"bot_id":"foreign"}`
				jobs[1] = json.RawMessage(employeeDiskJSON(t, row))
			default:
				var row map[string]any
				_ = json.Unmarshal(jobs[0], &row)
				row["next_run_at"] = map[string]string{"invalid-time": "tomorrow", "empty-time": "", "zero-time": "0001-01-01T00:00:00Z"}[name]
				jobs[0] = json.RawMessage(employeeDiskJSON(t, row))
			}
			if validate(jobs) == nil {
				t.Fatal("corrupt scoped evidence accepted")
			}
		})
	}
}
