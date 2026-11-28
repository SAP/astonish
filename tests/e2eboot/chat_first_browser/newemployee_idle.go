package main

import (
	"bytes"
	"encoding/json"

	"github.com/SAP/astonish/pkg/store"
)

// employeeNull requires explicit observed SQL NULL, not an omitted column from
// an old/incomplete observer. Even expired leases must have been cleared.
func employeeNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// employeeExecutionIdle checks physical ownership, not resumability or success.
// active_run_id is also the teardown barrier: production clears it only after
// verified cleanup. cleanup_complete is home-retirement state, not run teardown.
// Indexed status/lease columns are authoritative; stored Run JSON can retain the
// old running status/lease after fencing, just as historical worker/fence IDs do.
func employeeExecutionIdle(d employeeDisk, bot string) bool {
	if bot == "" || len(d.Bots) != 1 || d.Bots[0].ID != bot {
		return false
	}
	b := d.Bots[0]
	if !employeeNull(b.ActiveRunID) || !employeeNull(b.LeaseExpiresAt) {
		return false
	}
	for _, row := range d.Runs {
		if row.ID == "" || row.BotID != bot || !employeeNull(row.LeaseExpiresAt) {
			return false
		}
		switch store.RunStatus(row.Status) {
		case store.RunInterrupted, store.RunSucceeded, store.RunPartial, store.RunFailed, store.RunSkipped, store.RunCancelled:
			// Interrupted is resumable history, not active compute. A failed run is
			// physically idle only with cleared ownership; it never proves success.
		default: // queued, running, waiting_approval, and unknown/incomplete status
			return false
		}
	}
	return true
}

type employeeJobStatus string

const employeeJobPaused employeeJobStatus = "paused"

// employeePauseStopped supplements the applied-operation gate. Questions may
// remain pending independently; runnable/waiting runs may not. This scenario
// already created a recurrence, so missing jobs cannot prove schedule disablement.
func employeePauseStopped(d employeeDisk, bot string) bool {
	if !employeeExecutionIdle(d, bot) || store.BotStatus(d.Bots[0].Status) != store.BotPaused || len(d.Jobs) == 0 {
		return false
	}
	for _, raw := range d.Jobs {
		var job struct {
			Status  employeeJobStatus `json:"status"`
			NextRun json.RawMessage   `json:"next_run_at"`
		}
		if json.Unmarshal(raw, &job) != nil || job.Status != employeeJobPaused || !employeeNull(job.NextRun) {
			return false
		}
	}
	return true
}
