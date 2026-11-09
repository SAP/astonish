package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/google/uuid"
)

const employeeLegacyMessage = "Create a new reminder here every Monday at midnight UTC saying preservation witness. Keep the historical June reminder disabled."

var employeeHistoryStages = []string{"fixture", "read", "schedule", "restart-active", "cadence", "pause", "restart-paused", "resume", "restart-resumed"}

type employeeLegacyFixture struct {
	BotID         uuid.UUID `json:"bot_id"`
	TriggerID     uuid.UUID `json:"trigger_id"`
	ScheduleID    uuid.UUID `json:"schedule_id"`
	HomeSessionID uuid.UUID `json:"home_session_id"`
}

// Absence is proven from the private owned record, not a public DTO which omits
// authority by design. Reads can change read cursors, but cannot add mandate heads.
func employeeLegacyUnmandated(d employeeDisk, f employeeLegacyFixture) error {
	if len(d.Bots) != 1 || d.Bots[0].ID != f.BotID.String() || len(d.Runs) != 0 || len(d.Operations) != 0 {
		return fmt.Errorf("legacy fixture has work or mismatched identity")
	}
	_, fields, err := employeeStoredDocument(d.Bots[0].Data)
	if err != nil {
		return err
	}
	var metadata map[string]json.RawMessage
	if raw, ok := fields["metadata"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return err
		}
	}
	for key := range metadata {
		if strings.Contains(strings.ToLower(key), "mandate") || strings.HasPrefix(key, "routine_task") {
			return fmt.Errorf("read minted legacy authority: %s", key)
		}
	}
	return nil
}

// Compare the complete immutable trigger, not just enabled=false; then verify
// the actual scheduler row and all historical runs using the exact old identity.
func employeeHistoricalDisabled(d employeeDisk, v store.BotVersion, f employeeLegacyFixture, original store.TriggerSpec) error {
	if f.BotID == uuid.Nil || f.TriggerID == uuid.Nil || f.ScheduleID == uuid.Nil || original.ID != f.TriggerID || original.ScheduleID == nil || *original.ScheduleID != f.ScheduleID || original.Enabled {
		return fmt.Errorf("invalid historical fixture identity")
	}
	found := 0
	for _, tr := range v.Triggers {
		if tr.ID == f.TriggerID {
			found++
			if !reflect.DeepEqual(tr, original) {
				return fmt.Errorf("historical trigger changed")
			}
		}
	}
	if found != 1 {
		return fmt.Errorf("historical trigger absent or duplicated")
	}
	found = 0
	for _, raw := range d.Jobs {
		var job struct {
			ID      uuid.UUID         `json:"id"`
			Status  employeeJobStatus `json:"status"`
			Next    json.RawMessage   `json:"next_run_at"`
			Payload string            `json:"payload"`
			Cron    string            `json:"schedule"`
		}
		if err := json.Unmarshal(raw, &job); err != nil {
			return err
		}
		if job.ID != f.ScheduleID {
			continue
		}
		found++
		var p store.JobPayload
		if json.Unmarshal([]byte(job.Payload), &p) != nil || p.BotID != f.BotID.String() || p.BotTriggerID != f.TriggerID.String() || job.Status != employeeJobPaused || job.Cron != original.Cron || !employeeNull(job.Next) {
			return fmt.Errorf("historical schedule enabled, scheduled, or rebound")
		}
	}
	if found != 1 {
		return fmt.Errorf("historical scheduler row absent or duplicated")
	}
	for _, row := range d.Runs {
		if r := employeeRun(row); r.TriggerID == f.TriggerID {
			return fmt.Errorf("historical disabled trigger executed")
		}
	}
	return nil
}

func employeeLegacyMandate(d employeeDisk, f employeeLegacyFixture, target store.BotRun, op store.BotOperationView) (store.BotMandateRef, error) {
	var zero store.BotMandateRef
	if op.Validate() != nil || op.BotID != f.BotID || op.HomeSessionID != f.HomeSessionID || op.SourceRunID != target.ID || op.SourceUserEventID != target.UserEventID || op.Operation != store.BotControlScheduleCreate || op.Status != store.BotOperationApplied || op.Decision != store.BotOperationExecute {
		return zero, fmt.Errorf("adoption operation provenance mismatch")
	}
	for _, row := range d.Runs {
		if row.ID != target.ID.String() {
			continue
		}
		r := employeeRun(row)
		_, fields, err := employeeStoredDocument(row.Data)
		if err != nil {
			return zero, err
		}
		var ref store.BotMandateRef
		if json.Unmarshal(fields["mandate_ref"], &ref) != nil || ref.Validate() != nil || ref.BotID != f.BotID || target.BotID != f.BotID || r.BotID != f.BotID || r.UserEventID != target.UserEventID || r.ID != target.ID || r.Status != store.RunSucceeded || r.Presentation != store.BotRunPresentationHomeInteractive || r.Trigger != store.TriggerManual {
			return zero, fmt.Errorf("adoption lacks successful exact-turn persisted mandate")
		}
		return ref, nil
	}
	return zero, fmt.Errorf("adoption source absent from owned disk")
}

type employeeLegacyProbe struct {
	fixture                 employeeLegacyFixture
	original                store.TriggerSpec
	stages                  []string
	page                    func() *rod.Page
	home, path, root        string
	api                     func(string, string, any) string
	disk                    func(string) employeeDisk
	save                    func(string, any)
	snapshot                func() store.BotSnapshot
	runs                    func() []store.BotRun
	capture                 func(string, string) string
	mark                    func(string)
	newTrigger, newSchedule uuid.UUID
	cron                    string
	result                  map[string]any
}

func employeeStartLegacy(page func() *rod.Page, api func(string, string, any) string, state map[string]any, root string, save func(string, any), mark func(string)) *employeeLegacyProbe {
	p := &employeeLegacyProbe{page: page, api: api, root: root, save: save, mark: mark}
	raw, err := json.Marshal(state["employee_legacy_fixture"])
	must(err)
	must(json.Unmarshal(raw, &p.fixture))
	f := p.fixture
	if f.BotID == uuid.Nil || f.HomeSessionID == uuid.Nil || f.BotID.String() == state["bot_id"] {
		panic("missing distinct legacy fixture")
	}
	p.path = "/api/studio/bots/" + f.BotID.String()
	p.home = state["url"].(string) + "/#/chat/bots/personal/" + f.BotID.String()
	p.capture = func(label, path string) string {
		raw := api("GET", path, nil)
		must(os.WriteFile(filepath.Join(root, "employee-legacy-"+label+".json"), []byte(raw), 0600))
		return raw
	}
	p.disk = func(label string) employeeDisk {
		// Same strictly-owned, read-only observer as the peer probe. Never change the
		// launcher's canonical primary Bot/deletion cache to observe a second Bot.
		st := map[string]any{}
		for k, v := range state {
			st[k] = v
		}
		st["bot_id"] = f.BotID.String()
		raw, err := json.Marshal(st)
		must(err)
		out, err := exec.Command("python3", "-c", "import json,sys; sys.path.insert(0,'tests/e2eboot'); import local_bot_smoke_launcher as l; s=json.loads(sys.argv[1]); l.inspect_owned(s); d=l.employee_readonly(s); assert d['observation_complete'], d['blocker']; print(json.dumps(d))", string(raw)).Output()
		must(err)
		must(os.WriteFile(filepath.Join(root, "employee-legacy-"+label+"-disk.json"), out, 0600))
		var disk employeeDisk
		must(json.Unmarshal(out, &disk))
		return disk
	}
	p.snapshot = func() store.BotSnapshot {
		var s store.BotSnapshot
		must(json.Unmarshal([]byte(p.capture("snapshot", p.path+"/snapshot?scope=personal")), &s))
		if s.Bot.ID != f.BotID || s.Bot.HomeSessionID != f.HomeSessionID {
			panic("legacy Home changed")
		}
		return s
	}
	p.runs = func() []store.BotRun {
		var r struct{ Runs []store.BotRun }
		must(json.Unmarshal([]byte(p.capture("runs", p.path+"/runs?scope=personal")), &r))
		return r.Runs
	}
	var disk employeeDisk
	raw, err = os.ReadFile(filepath.Join(root, "employee-legacy-fixture-disk.json"))
	must(err)
	must(json.Unmarshal(raw, &disk))
	must(employeeLegacyUnmandated(disk, f))
	var v store.BotVersion
	raw, err = os.ReadFile(filepath.Join(root, "employee-legacy-fixture-version.json"))
	must(err)
	must(json.Unmarshal(raw, &v))
	for _, tr := range v.Triggers {
		if tr.ID == f.TriggerID {
			p.original = tr
		}
	}
	must(employeeHistoricalDisabled(disk, v, f, p.original))
	p.stages = append(p.stages, "fixture")
	// Opening Home, listing, snapshot, version and history are deliberately reads.
	page().MustNavigate(p.home).MustReload()
	page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	p.capture("list", "/api/studio/bots?scope=personal")
	p.capture("history", employeePeerHistoryPath(p.snapshot().Bot))
	p.observe("read")
	must(employeeLegacyUnmandated(p.disk("read-authority"), f))
	p.result = map[string]any{"LegacyBotID": f.BotID.String(), "LegacyHome": p.home, "HistoricalTriggerID": f.TriggerID.String(), "HistoricalScheduleID": f.ScheduleID.String()}
	return p
}

func (p *employeeLegacyProbe) observe(label string) {
	p.observeWithWait(label, boundedWait)
}

// Keep the wait budget injectable for offline polling-contract tests. The live
// path always uses boundedWait; every predicate must obtain fresh disk evidence.
func (p *employeeLegacyProbe) observeWithWait(label string, wait func(string, time.Duration, func() bool)) {
	s := p.snapshot()
	switch label {
	case "pause", "restart-paused":
		if s.Bot.Status != store.BotPaused {
			panic("legacy paused state did not survive")
		}
	case "schedule", "restart-active", "cadence", "resume", "restart-resumed":
		if s.Bot.Status != store.BotActive {
			panic("legacy active state did not survive")
		}
	}
	var v store.BotVersion
	must(json.Unmarshal([]byte(p.capture(label+"-version", fmt.Sprintf("%s/versions/%d?scope=personal", p.path, s.Bot.Version))), &v))
	if p.newTrigger != uuid.Nil {
		// API application is not proof of disk convergence. Read exactly once per
		// predicate and retain that read; never replay the mutation or refresh API
		// state to make the frozen expected version match stale disk evidence.
		wait("legacy scoped schedule storage convergence", 30*time.Second, func() bool {
			fresh := p.disk(label)
			return employeeHistoricalDisabled(fresh, v, p.fixture, p.original) == nil && employeeLegacyNewSchedule(fresh, v, s.Bot, p.fixture, p.newTrigger, p.newSchedule, p.cron) == nil
		})
	} else {
		must(employeeHistoricalDisabled(p.disk(label), v, p.fixture, p.original))
	}
	p.stages = append(p.stages, label)
}

// SQLite stores Go time strings; API fixtures use RFC3339. Preserve the raw
// observer evidence and decode both canonical forms only at this proof boundary.
type employeeLegacyStorageTime struct{ time.Time }

func (t *employeeLegacyStorageTime) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -0700 MST"} {
		parsed, err := time.Parse(layout, value)
		if err == nil && !parsed.IsZero() {
			t.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("invalid legacy scheduler next_run_at")
}

const employeeJobActive employeeJobStatus = "active"

func employeeLegacyNewSchedule(d employeeDisk, v store.BotVersion, b store.Bot, f employeeLegacyFixture, trigger, schedule uuid.UUID, cron string) error {
	if len(v.Triggers) != 2 || len(d.Jobs) != 2 || trigger == uuid.Nil || schedule == uuid.Nil || trigger == f.TriggerID || schedule == f.ScheduleID {
		return fmt.Errorf("legacy duplicate or missing schedule: triggers=%d scoped_jobs=%d", len(v.Triggers), len(d.Jobs))
	}
	if b.ID != f.BotID || (v.BotID != uuid.Nil && v.BotID != f.BotID) {
		return fmt.Errorf("legacy schedule Bot identity mismatch")
	}
	// Jobs is already scoped by employee_readonly. Do not hide foreign rows.
	seenJobs := map[uuid.UUID]bool{}
	for _, raw := range d.Jobs {
		var row struct {
			ID      uuid.UUID `json:"id"`
			Payload string    `json:"payload"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		if _, err := employeeDiskObject(row.Payload); err != nil {
			return err
		}
		var payload store.JobPayload
		if json.Unmarshal([]byte(row.Payload), &payload) != nil || payload.BotID != f.BotID.String() || row.ID == uuid.Nil || seenJobs[row.ID] || (row.ID != schedule && row.ID != f.ScheduleID) {
			return fmt.Errorf("legacy scheduler scope or identity mismatch")
		}
		seenJobs[row.ID] = true
	}
	seenTriggers := map[uuid.UUID]bool{}
	for _, tr := range v.Triggers {
		if seenTriggers[tr.ID] || (tr.ID != trigger && tr.ID != f.TriggerID) {
			return fmt.Errorf("legacy trigger identity absent or duplicated")
		}
		seenTriggers[tr.ID] = true
	}
	enabled := b.Status == store.BotActive
	if !enabled && b.Status != store.BotPaused {
		return fmt.Errorf("legacy unexpected lifecycle status")
	}
	found := false
	for _, tr := range v.Triggers {
		if tr.ID == trigger {
			found = true
			if tr.ScheduleID == nil || *tr.ScheduleID != schedule || tr.Cron != cron || !tr.Enabled {
				return fmt.Errorf("legacy new trigger lifecycle mismatch")
			}
		}
	}
	if !found {
		return fmt.Errorf("new trigger missing")
	}
	found = false
	for _, raw := range d.Jobs {
		var job struct {
			ID      uuid.UUID                  `json:"id"`
			Status  employeeJobStatus          `json:"status"`
			Next    *employeeLegacyStorageTime `json:"next_run_at"`
			Cron    string                     `json:"schedule"`
			Payload string                     `json:"payload"`
		}
		if err := json.Unmarshal(raw, &job); err != nil {
			return err
		}
		if job.ID != schedule {
			continue
		}
		found = true
		var payload store.JobPayload
		if json.Unmarshal([]byte(job.Payload), &payload) != nil || payload.BotID != f.BotID.String() || payload.BotTriggerID != trigger.String() || job.Cron != cron {
			return fmt.Errorf("new scheduler binding or cadence mismatch")
		}
		if enabled {
			if job.Status != employeeJobActive || job.Next == nil || job.Next.IsZero() {
				return fmt.Errorf("new schedule not enabled")
			}
		} else if job.Status != employeeJobPaused || job.Next != nil {
			return fmt.Errorf("new schedule not paused")
		}
	}
	if !found {
		return fmt.Errorf("new scheduler row missing")
	}
	return nil
}

func (p *employeeLegacyProbe) change(label, message string, kind store.BotControlOperation) (store.BotRun, store.BotOperationView) {
	p.page().MustNavigate(p.home)
	p.page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	boundedWait("legacy idle", 4*time.Minute, func() bool { return employeeExecutionIdle(p.disk("idle"), p.fixture.BotID.String()) })
	before, seen := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	for _, r := range p.runs() {
		before[r.ID] = true
	}
	for _, op := range p.snapshot().Operations {
		seen[op.OperationID] = true
	}
	target := employeeSend(p.page, p.home, "legacy-"+label, message, p.snapshot, p.runs, p.capture, func() { p.mark("legacy-" + label) }, p.save)
	var result store.BotOperationView
	boundedWait("legacy "+label, 4*time.Minute, func() bool {
		return employeeChangePoll(p.snapshot, p.runs, before, seen, target, kind, false, func(op store.BotOperationView, _ store.Bot) { result = op })
	})
	if kind == store.BotControlScheduleCreate || kind == store.BotControlScheduleEdit {
		s := result.VerifiedSchedule
		if s == nil || !s.Enabled || s.NextRun == nil || s.ScheduleID == p.fixture.ScheduleID || s.TriggerID == p.fixture.TriggerID {
			panic("legacy schedule readback missing or reused historical schedule")
		}
		if kind == store.BotControlScheduleCreate {
			p.newTrigger, p.newSchedule, p.cron = s.TriggerID, s.ScheduleID, "0 0 * * 1"
		} else {
			if s.TriggerID != p.newTrigger || s.ScheduleID != p.newSchedule {
				panic("legacy cadence duplicated schedule")
			}
			p.cron = "0 0 * * 2"
		}
	}
	if kind == store.BotControlPriorityPause && p.snapshot().Bot.Status != store.BotPaused {
		panic("legacy pause did not pause Bot")
	}
	if kind == store.BotControlResumeTasks && p.snapshot().Bot.Status != store.BotActive {
		panic("legacy resume did not activate Bot")
	}
	p.save("legacy-"+label+"-operation", result)
	p.observe(label)
	return target, result
}
