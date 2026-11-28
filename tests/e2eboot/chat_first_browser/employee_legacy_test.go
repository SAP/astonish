package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func legacyFixture(t *testing.T) (employeeDisk, store.BotVersion, employeeLegacyFixture, store.TriggerSpec) {
	t.Helper()
	f := employeeLegacyFixture{BotID: uuid.New(), TriggerID: uuid.New(), ScheduleID: uuid.New(), HomeSessionID: uuid.New()}
	tr := store.TriggerSpec{ID: f.TriggerID, ScheduleID: &f.ScheduleID, Kind: store.TriggerCron, Enabled: false, Cron: "0 0 1 6 *", Timezone: "UTC"}
	job := map[string]any{"id": f.ScheduleID, "status": "paused", "next_run_at": nil, "schedule": tr.Cron, "payload": employeeDiskJSON(t, store.JobPayload{BotID: f.BotID.String(), BotTriggerID: f.TriggerID.String()})}
	d := employeeDisk{Bots: []employeeDiskRow{{ID: f.BotID.String(), Data: `{"Metadata":{}}`}}, Jobs: []json.RawMessage{json.RawMessage(employeeDiskJSON(t, job))}}
	return d, store.BotVersion{Triggers: []store.TriggerSpec{tr}}, f, tr
}

func TestEmployeeLegacyReadsCannotMintAuthority(t *testing.T) {
	d, _, f, _ := legacyFixture(t)
	if err := employeeLegacyUnmandated(d, f); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mandate_head", "mandate_usage", "routine_task_head"} {
		d.Bots[0].Data = employeeDiskJSON(t, map[string]any{"Metadata": map[string]any{key: map[string]any{}}})
		if employeeLegacyUnmandated(d, f) == nil {
			t.Fatalf("accepted %s", key)
		}
	}
	d.Bots[0].Data = `{"Metadata":{}}`
	d.Runs = []employeeDiskRow{{ID: uuid.NewString()}}
	if employeeLegacyUnmandated(d, f) == nil {
		t.Fatal("accepted existing work")
	}
}

func TestEmployeeHistoricalDisabledRejectsCorruption(t *testing.T) {
	for _, name := range []string{"valid", "enabled", "cron", "missing-trigger", "duplicate-trigger", "missing-job", "active-job", "next-run", "job-cron", "foreign-payload", "historical-run"} {
		t.Run(name, func(t *testing.T) {
			d, v, f, tr := legacyFixture(t)
			var job map[string]any
			_ = json.Unmarshal(d.Jobs[0], &job)
			switch name {
			case "enabled":
				v.Triggers[0].Enabled = true
			case "cron":
				v.Triggers[0].Cron = "* * * * *"
			case "missing-trigger":
				v.Triggers = nil
			case "duplicate-trigger":
				v.Triggers = append(v.Triggers, tr)
			case "missing-job":
				d.Jobs = nil
			case "active-job":
				job["status"] = "active"
			case "next-run":
				job["next_run_at"] = time.Now()
			case "job-cron":
				job["schedule"] = "* * * * *"
			case "foreign-payload":
				job["payload"] = `{}`
			case "historical-run":
				r := store.BotRun{ID: uuid.New(), BotID: f.BotID, TriggerID: f.TriggerID, Trigger: store.TriggerCron}
				d.Runs = []employeeDiskRow{{ID: r.ID.String(), Data: employeeDiskJSON(t, r)}}
			}
			if len(d.Jobs) > 0 {
				d.Jobs[0] = json.RawMessage(employeeDiskJSON(t, job))
			}
			err := employeeHistoricalDisabled(d, v, f, tr)
			if (err == nil) != (name == "valid") {
				t.Fatalf("%s: %v", name, err)
			}
		})
	}
}

func TestEmployeeLegacyNewScheduleLifecycle(t *testing.T) {
	d, v, f, _ := legacyFixture(t)
	tid, sid := uuid.New(), uuid.New()
	cron := "0 0 * * 2"
	v.Triggers = append(v.Triggers, store.TriggerSpec{ID: tid, ScheduleID: &sid, Enabled: true, Cron: cron})
	for _, status := range []store.BotStatus{store.BotActive, store.BotPaused, store.BotActive} {
		job := map[string]any{"id": sid, "status": "active", "next_run_at": time.Now().Add(time.Hour), "schedule": cron, "payload": employeeDiskJSON(t, store.JobPayload{BotID: f.BotID.String(), BotTriggerID: tid.String()})}
		if status == store.BotPaused {
			job["status"] = "paused"
			job["next_run_at"] = nil
		}
		d.Jobs = append(d.Jobs[:1], json.RawMessage(employeeDiskJSON(t, job)))
		b := store.Bot{ID: f.BotID, Status: status}
		if err := employeeLegacyNewSchedule(d, v, b, f, tid, sid, cron); err != nil {
			t.Fatal(err)
		}
		if employeeLegacyNewSchedule(d, v, b, f, tid, sid, "* * * * *") == nil {
			t.Fatal("wrong cadence accepted")
		}
		if employeeLegacyNewSchedule(d, v, b, f, f.TriggerID, f.ScheduleID, cron) == nil {
			t.Fatal("historical identity reused")
		}
		dup := d
		dup.Jobs = append(append([]json.RawMessage{}, d.Jobs...), d.Jobs[1])
		if employeeLegacyNewSchedule(dup, v, b, f, tid, sid, cron) == nil {
			t.Fatal("duplicate job accepted")
		}
	}
}

func TestEmployeeLegacyMandateExactTurn(t *testing.T) {
	d, _, f, _ := legacyFixture(t)
	r := store.BotRun{ID: uuid.New(), BotID: f.BotID, UserEventID: "new-owner-event", Trigger: store.TriggerManual, Presentation: store.BotRunPresentationHomeInteractive, Status: store.RunSucceeded}
	ref := store.BotMandateRef{ID: uuid.New(), BotID: f.BotID, OrgSlug: "org", TeamSlug: "team", OwnerID: "owner", Revision: 1}
	op := store.BotOperationView{SchemaVersion: 1, StateSequence: 1, OperationID: uuid.New(), BotID: f.BotID, HomeSessionID: f.HomeSessionID, SourceRunID: r.ID, SourceUserEventID: r.UserEventID, Operation: store.BotControlScheduleCreate, Status: store.BotOperationApplied, Decision: store.BotOperationExecute, Revision: 1}
	d.Runs = []employeeDiskRow{{ID: r.ID.String(), Data: employeeDiskJSON(t, map[string]any{"Run": r, "mandate_ref": ref})}}
	if _, err := employeeLegacyMandate(d, f, r, op); err != nil {
		t.Fatal(err)
	}
	op.SourceUserEventID = "old-owner-event"
	if _, err := employeeLegacyMandate(d, f, r, op); err == nil {
		t.Fatal("stale event accepted")
	}
	op.SourceUserEventID = r.UserEventID
	d.Runs[0].Data = employeeDiskJSON(t, map[string]any{"Run": r})
	if _, err := employeeLegacyMandate(d, f, r, op); err == nil {
		t.Fatal("unmandated adoption accepted")
	}
}
