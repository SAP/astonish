package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Exercise the real observe predicate, not a second implementation of its
// validators. Replacing the source object between calls catches captured reads.
func TestEmployeeLegacyObserverFreshEveryPredicate(t *testing.T) {
	for _, label := range []string{"schedule", "cadence", "pause", "restart-paused", "resume", "restart-resumed"} {
		t.Run(label, func(t *testing.T) {
			stale, v, f, old := legacyFixture(t)
			tid, sid := uuid.New(), uuid.New()
			cron := "0 0 * * 1"
			status, jobStatus := store.BotActive, employeeJobActive
			var next any = "2026-11-16 00:00:00 +0000 UTC"
			if label == "pause" || label == "restart-paused" {
				status, jobStatus, next = store.BotPaused, employeeJobPaused, nil
			}
			v.Triggers = append(v.Triggers, store.TriggerSpec{ID: tid, ScheduleID: &sid, Enabled: true, Cron: cron})
			current := stale
			current.Jobs = append(append([]json.RawMessage{}, stale.Jobs...), json.RawMessage(employeeDiskJSON(t, map[string]any{
				"id": sid, "status": jobStatus, "next_run_at": next, "schedule": cron,
				"payload": employeeDiskJSON(t, store.JobPayload{BotID: f.BotID.String(), BotTriggerID: tid.String()}),
			})))
			source := stale
			reads, snapshots, captures := 0, 0, 0
			p := employeeLegacyProbe{
				fixture: f, original: old, newTrigger: tid, newSchedule: sid, cron: cron,
				stages: []string{"fixture", "read"},
				snapshot: func() store.BotSnapshot {
					snapshots++
					return store.BotSnapshot{Bot: store.Bot{ID: f.BotID, Status: status}}
				},
				capture: func(string, string) string { captures++; return employeeDiskJSON(t, v) },
				disk: func(got string) employeeDisk {
					if got != label {
						t.Fatalf("wrong evidence stage: %s", got)
					}
					reads++
					return source
				},
			}
			p.observeWithWait(label, func(name string, budget time.Duration, check func() bool) {
				if name != "legacy scoped schedule storage convergence" || budget != 30*time.Second {
					t.Fatalf("changed wait contract: %q %v", name, budget)
				}
				if reads != 0 {
					t.Fatalf("read before predicate: %d", reads)
				}
				for i := 1; i <= 3; i++ {
					if i == 3 {
						source = current
					}
					if got := check(); got != (i == 3) {
						t.Fatalf("poll %d: ready=%v", i, got)
					}
					if reads != i {
						t.Fatalf("poll %d: reads=%d", i, reads)
					}
					if !reflect.DeepEqual(p.stages, []string{"fixture", "read"}) {
						t.Fatal("stage published before convergence")
					}
				}
			})
			if snapshots != 1 || captures != 1 {
				t.Fatalf("poll refreshed API: snapshot=%d version=%d", snapshots, captures)
			}
			if !reflect.DeepEqual(p.stages, []string{"fixture", "read", label}) {
				t.Fatalf("stages=%v", p.stages)
			}
		})
	}
}

func TestEmployeeLegacyObserverMissingScheduleTimesOut(t *testing.T) {
	d, v, f, old := legacyFixture(t)
	tid, sid := uuid.New(), uuid.New()
	v.Triggers = append(v.Triggers, store.TriggerSpec{ID: tid, ScheduleID: &sid, Enabled: true, Cron: "0 0 * * 1"})
	reads := 0
	p := employeeLegacyProbe{
		fixture: f, original: old, newTrigger: tid, newSchedule: sid, cron: "0 0 * * 1",
		stages: []string{"fixture", "read"},
		snapshot: func() store.BotSnapshot {
			return store.BotSnapshot{Bot: store.Bot{ID: f.BotID, Status: store.BotActive}}
		},
		capture: func(string, string) string { return employeeDiskJSON(t, v) },
		disk:    func(string) employeeDisk { reads++; return d },
	}
	const timeout = "timeout: legacy scoped schedule storage convergence"
	defer func() {
		if got := recover(); got != timeout {
			t.Fatalf("expected timeout, got %v", got)
		}
		if reads != 3 {
			t.Fatalf("missing evidence reads=%d", reads)
		}
		if !reflect.DeepEqual(p.stages, []string{"fixture", "read"}) {
			t.Fatal("timeout published passing stage")
		}
	}()
	p.observeWithWait("schedule", func(_ string, _ time.Duration, check func() bool) {
		for i := 0; i < 3; i++ {
			if check() {
				t.Fatal("missing scheduler row passed")
			}
		}
		panic(timeout)
	})
}
