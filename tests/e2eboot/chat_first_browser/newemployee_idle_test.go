package main

import (
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func employeeIdleFixture(t *testing.T, status store.RunStatus) (employeeDisk, string) {
	t.Helper()
	bot, run := uuid.New(), uuid.New()
	data, err := json.Marshal(store.BotRun{ID: run, BotID: bot, Status: status})
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"bots":[{"id":"` + bot.String() + `","status":"paused","active_run_id":null,"lease_expires_at":null}],"runs":[{"id":"` + run.String() + `","bot_id":"` + bot.String() + `","status":"` + string(status) + `","lease_expires_at":null,"data":` + string(mustEmployeeJSON(t, string(data))) + `}],"jobs":[{"id":"job","status":"paused","next_run_at":null}]}`
	var disk employeeDisk
	if err := json.Unmarshal([]byte(raw), &disk); err != nil {
		t.Fatal(err)
	}
	return disk, bot.String()
}

func mustEmployeeJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEmployeeExecutionIdle(t *testing.T) {
	for _, status := range []store.RunStatus{store.RunInterrupted, store.RunSucceeded, store.RunPartial, store.RunFailed, store.RunSkipped, store.RunCancelled, store.RunQueued, store.RunRunning, store.RunWaitingApproval, "unknown"} {
		t.Run(string(status), func(t *testing.T) {
			disk, bot := employeeIdleFixture(t, status)
			want := status.Terminal() || status == store.RunInterrupted
			if got := employeeExecutionIdle(disk, bot); got != want {
				t.Fatalf("idle=%v want %v", got, want)
			}
		})
	}
	if store.RunInterrupted.Terminal() {
		t.Fatal("interrupted must stay resumable in production")
	}
}

func TestEmployeeExecutionIdleOwnership(t *testing.T) {
	for _, status := range []store.RunStatus{store.RunInterrupted, store.RunFailed, store.RunSucceeded} {
		for _, tc := range []struct {
			name   string
			change func(*employeeDisk)
		}{
			{"teardown-active-run", func(d *employeeDisk) { d.Bots[0].ActiveRunID = json.RawMessage(`"pending-teardown"`) }},
			{"bot-lease", func(d *employeeDisk) { d.Bots[0].LeaseExpiresAt = json.RawMessage(`"2026-11-09T05:00:00Z"`) }},
			{"run-lease-even-expired", func(d *employeeDisk) { d.Runs[0].LeaseExpiresAt = json.RawMessage(`"2020-01-01T00:00:00Z"`) }},
			{"missing-active-evidence", func(d *employeeDisk) { d.Bots[0].ActiveRunID = nil }},
			{"missing-bot-lease-evidence", func(d *employeeDisk) { d.Bots[0].LeaseExpiresAt = nil }},
			{"missing-run-lease-evidence", func(d *employeeDisk) { d.Runs[0].LeaseExpiresAt = nil }},
			{"no-bot", func(d *employeeDisk) { d.Bots = nil }},
			{"wrong-bot", func(d *employeeDisk) { d.Bots[0].ID = uuid.NewString() }},
			{"peer-run", func(d *employeeDisk) { d.Runs[0].BotID = uuid.NewString() }},
			{"running-sibling", func(d *employeeDisk) { r := d.Runs[0]; r.Status = string(store.RunRunning); d.Runs = append(d.Runs, r) }},
		} {
			t.Run(string(status)+"/"+tc.name, func(t *testing.T) {
				disk, bot := employeeIdleFixture(t, status)
				tc.change(&disk)
				if employeeExecutionIdle(disk, bot) {
					t.Fatal("ownership or incomplete evidence accepted as idle")
				}
			})
		}
	}
}

func TestEmployeePauseStopped(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*employeeDisk)
		want   bool
	}{
		{"interrupted-cleared", func(*employeeDisk) {}, true},
		{"indexed-state-over-historical-json", func(d *employeeDisk) {
			d.Runs[0].Data = `{"Run":{"status":"running","lease":{"expires_at":"2026-11-09T05:00:00Z"}}}`
		}, true},
		{"teardown-still-owned", func(d *employeeDisk) { d.Bots[0].ActiveRunID = json.RawMessage(`"teardown"`) }, false},
		{"run-lease-still-owned", func(d *employeeDisk) { d.Runs[0].LeaseExpiresAt = json.RawMessage(`"2020-01-01"`) }, false},
		{"waiting-run", func(d *employeeDisk) { d.Runs[0].Status = string(store.RunWaitingApproval) }, false},
		{"queued-run", func(d *employeeDisk) { d.Runs[0].Status = string(store.RunQueued) }, false},
		{"missing-run-status", func(d *employeeDisk) { d.Runs[0].Status = "" }, false},
		{"pending-question-independent", func(d *employeeDisk) { d.Approvals = []json.RawMessage{json.RawMessage(`{"status":"pending"}`)} }, true},
		{"active-bot", func(d *employeeDisk) { d.Bots[0].Status = string(store.BotActive) }, false},
		{"enabled-job", func(d *employeeDisk) { d.Jobs[0] = json.RawMessage(`{"status":"active","next_run_at":null}`) }, false},
		{"due-job", func(d *employeeDisk) {
			d.Jobs[0] = json.RawMessage(`{"status":"paused","next_run_at":"2026-11-09T05:00:00Z"}`)
		}, false},
		{"missing-next-run", func(d *employeeDisk) { d.Jobs[0] = json.RawMessage(`{"status":"paused"}`) }, false},
		{"failed-job", func(d *employeeDisk) { d.Jobs[0] = json.RawMessage(`{"status":"failed","next_run_at":null}`) }, false},
		{"missing-job", func(d *employeeDisk) { d.Jobs = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disk, bot := employeeIdleFixture(t, store.RunInterrupted)
			tc.change(&disk)
			if got := employeePauseStopped(disk, bot); got != tc.want {
				t.Fatalf("pause=%v want %v", got, tc.want)
			}
		})
	}
}
