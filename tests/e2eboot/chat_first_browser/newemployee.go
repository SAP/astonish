package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/google/uuid"
)

const employeeContract = "bot-employee-v1"

// Fail closed: snapshot is the server projection, not model prose or a harness-created card.
func validateEmployeeSchedule(v store.BotOperationView, b store.Bot, pending map[uuid.UUID]bool) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if v.BotID != b.ID || v.HomeSessionID != b.HomeSessionID || v.SourceRunID == uuid.Nil || v.SourceUserEventID == "" {
		return fmt.Errorf("operation source/Home mismatch")
	}
	if v.Operation != store.BotControlScheduleCreate || v.Status != store.BotOperationApplied || v.Decision != store.BotOperationExecute || !pending[v.OperationID] {
		return fmt.Errorf("schedule lacks observed pending-to-applied transition")
	}
	s := v.VerifiedSchedule
	if s == nil || s.ScheduleID == uuid.Nil || s.TriggerID == uuid.Nil || s.Version < 1 || !s.Enabled || s.Removed || s.NextRun == nil || s.NextRun.IsZero() {
		return fmt.Errorf("schedule lacks enabled durable job/next-run readback")
	}
	return nil
}

// employeeSchedulePoll retains pending evidence and the caller's durable schedule
// validator; employeeOperationPoll only decides when observation is ready.
func employeeSchedulePoll(snapshot func() store.BotSnapshot, runs func() []store.BotRun, before, pending map[uuid.UUID]bool, target store.BotRun, capturePending func(), applied func(store.BotOperationView, store.Bot)) bool {
	return employeeOperationPoll(snapshot, runs, before, nil, target, store.BotControlScheduleCreate, false, func(op store.BotOperationView) {
		pending[op.OperationID] = true
		capturePending()
	}, applied)
}

func employeeChangePoll(snapshot func() store.BotSnapshot, runs func() []store.BotRun, prior, seen map[uuid.UUID]bool, target store.BotRun, operation store.BotControlOperation, exception bool, ready func(store.BotOperationView, store.Bot)) bool {
	return employeeOperationPoll(snapshot, runs, prior, seen, target, operation, exception, nil, ready)
}

// employeeOperationPoll observes only. Callers own bounded waits, durable
// validators and receipts. No terminal source status substitutes for an applied
// operation, and no captured sibling hides a missing or failed operation.
func employeeOperationPoll(snapshot func() store.BotSnapshot, runs func() []store.BotRun, before, seen map[uuid.UUID]bool, target store.BotRun, operation store.BotControlOperation, exception bool, pending func(store.BotOperationView), complete func(store.BotOperationView, store.Bot)) bool {
	// Capture is persisted before source completion, but applied asynchronously.
	// Read runs first so a newly terminal source cannot race an older snapshot.
	rr := runs()
	s := snapshot()
	if target.ID == uuid.Nil { // UI submission is not yet durably admitted.
		return false
	}
	if target.BotID != s.Bot.ID || target.UserEventID == "" || before[target.ID] {
		panic("employee target admission mismatch")
	}
	matchesTarget := func(r store.BotRun) bool {
		return r.ID == target.ID && r.BotID == target.BotID && r.UserEventID == target.UserEventID && r.Trigger == store.TriggerManual && r.Presentation == store.BotRunPresentationHomeInteractive
	}
	for _, proposal := range s.PendingProposals {
		if proposal.SourceRunID == target.ID {
			panic("employee operation incorrectly requires review/configuration")
		}
	}
	for _, approval := range s.PendingApprovals {
		if approval.RunID == target.ID {
			panic("employee operation incorrectly requires review/configuration")
		}
	}
	captured := map[uuid.UUID]bool{}
	var ready []store.BotOperationView
	for _, op := range s.Operations {
		if seen[op.OperationID] || op.Operation != operation || op.BotID != s.Bot.ID || op.HomeSessionID != s.Bot.HomeSessionID || op.SourceRunID == uuid.Nil || op.SourceUserEventID == "" {
			continue
		}
		matched := false
		for _, r := range rr {
			if matchesTarget(r) && r.ID == op.SourceRunID && r.UserEventID == op.SourceUserEventID {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		must(op.Validate())
		captured[op.SourceRunID] = true
		switch op.Status {
		case store.BotOperationPending:
			if pending != nil {
				pending(op)
			}
		case store.BotOperationApplying:
			// Wait, but never synthesize pending evidence.
		case store.BotOperationBlocked:
			// Only the explicit exception gate expects an authorization request.
			if !exception || op.Decision != store.BotOperationRequireAuthorization {
				panic("employee operation terminal/blocked: " + string(op.Status))
			}
			ready = append(ready, op)
		case store.BotOperationFailed, store.BotOperationCancelled:
			panic("employee operation terminal/blocked: " + string(op.Status))
		case store.BotOperationApplied:
			if exception || op.Decision != store.BotOperationExecute {
				panic("unexpected employee operation decision: " + string(op.Decision))
			}
			ready = append(ready, op)
		}
	}
	for _, r := range rr {
		if r.ID == target.ID && !matchesTarget(r) {
			panic("employee target run provenance changed")
		}
		if matchesTarget(r) && r.Status.Terminal() && !captured[r.ID] {
			panic("employee turn terminal without expected durable operation: " + string(r.Status))
		}
	}
	if len(ready) > 0 {
		complete(ready[0], s.Bot)
		return true
	}
	return false
}

func newEmployeeWorkflow(basePage *rod.Page, api func(string, string, any) string, state map[string]any, statePath, root, base string, evidence map[string]any, save func(), pass func(string)) {
	evidence["contract"] = employeeContract
	evidence["candidate"] = state["binary_sha256"]
	evidence["browser_candidate"] = state["browser_sha256"]
	receipts := map[string]map[string]any{}
	actions := map[string]bool{}
	evidence["receipts"] = receipts
	evidence["unverified"] = true // Cleared only after every required receipt validates.
	checkpoint := func() {
		raw, e := json.MarshalIndent(map[string]any{"contract": employeeContract, "candidate": state["binary_sha256"], "browser_candidate": state["browser_sha256"], "actions": actions, "bot_id": state["bot_id"], "receipts": receipts}, "", "  ")
		must(e)
		must(os.WriteFile(filepath.Join(root, "employee-checkpoint.json"), raw, 0600))
		save()
	}
	stage := func(name string) {
		evidence["stage"] = name
		checkpoint()
		fmt.Println("STAGE", name, time.Now().UTC().Format(time.RFC3339))
	}
	page := func() *rod.Page { return independentPageTimeout(basePage, 40*time.Second) }
	capture := func(name, path string) string {
		raw := api("GET", path, nil)
		must(os.WriteFile(filepath.Join(root, "employee-"+name+".json"), []byte(raw), 0600))
		return raw
	}
	stage("employee-create-weather")
	if state["bot_id"] != nil {
		panic("employee workflow requires fresh root")
	}
	page().MustElementR("button", "^New Bot$").MustClick()
	page().MustElement(`[aria-label="Bot name"]`).MustInput("Weather in Orlando, FL today")
	actions["create-attempted"] = true
	checkpoint()
	page().MustElementR("button", "^Start conversation$").MustClick()
	page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	home := page().MustInfo().URL
	if !strings.HasPrefix(home, base+"/#/chat/bots/personal/") {
		panic("creation left owned Home")
	}
	id := strings.TrimPrefix(home, base+"/#/chat/bots/personal/")
	state["bot_id"] = id
	raw, e := json.MarshalIndent(state, "", "  ")
	must(e)
	must(os.WriteFile(statePath, raw, 0600))
	checkpoint()
	receipt := func(name, detail, source string) {
		receipts[name] = map[string]any{"Candidate": state["binary_sha256"], "BotID": id, "Home": home, "Detail": detail, "Source": source, "SchemaVersion": 1}
		checkpoint()
		pass(name)
	}
	botPath := "/api/studio/bots/" + id
	snapshot := func() store.BotSnapshot {
		var s store.BotSnapshot
		must(json.Unmarshal([]byte(capture("snapshot", botPath+"/snapshot?scope=personal")), &s))
		if s.Bot.ID.String() != id {
			panic("snapshot Bot mismatch")
		}
		return s
	}
	runs := func() []store.BotRun {
		var r struct{ Runs []store.BotRun }
		must(json.Unmarshal([]byte(capture("runs", botPath+"/runs?scope=personal")), &r))
		return r.Runs
	}
	stage("employee-title-weather")
	boundedWait("employee real weather", 8*time.Minute, func() bool {
		rr := runs()
		if len(rr) > 1 {
			panic("duplicate title run")
		}
		if len(rr) == 0 {
			return false
		}
		r := rr[0]
		if receipts["candidate-node"] == nil && r.SessionID != uuid.Nil {
			name := smokeContainerName(r.SessionID)
			out, err := exec.Command("docker", "exec", name, "sha256sum", "/sandbox/rootfs/usr/local/bin/astonish").CombinedOutput()
			if err == nil && strings.HasPrefix(string(out), state["binary_sha256"].(string)+" ") {
				must(os.WriteFile(filepath.Join(root, "employee-node-sha256.txt"), out, 0600))
				receipt("candidate-node", "Physical owned session node matches daemon candidate", "employee-node-sha256.txt")
			}
		}
		if !r.Status.Terminal() {
			return false
		}
		if r.Status != store.RunSucceeded {
			panic("title terminal without success: " + string(r.Status))
		}
		s := snapshot()
		history := capture("title-history", "/api/studio/sessions/"+s.Bot.HomeSessionID.String())
		stream := capture("title-events", botPath+"/sessions/"+r.SessionID.String()+"/stream?scope=personal")
		must(employeeTitleEvidence(history, stream, s.Bot, r))
		// Commit/readback can precede the viewer's next SSE poll. Wait for the
		// exact DOM postcondition under the existing deadline; do not replay work.
		if employeeReportExactlyOnce(history, s.Bot.HomeSessionID.String(), r, employeeReportBubbles(page())) != nil {
			return false
		}
		if len(s.PendingProposals) > 0 || len(s.PendingApprovals) > 0 {
			panic("title unexpectedly requires configuration/review")
		}
		text := page().MustElement(`[aria-label="Bot workspace"]`).MustText()
		if !strings.Contains(strings.ToLower(text), "orlando") {
			panic("weather answer not visible in Home")
		}
		receipt("title-weather", "Real-provider weather result visible in same Home with correlated web call/result", "employee-title-history.json + employee-title-events.json")
		return true
	})
	if receipts["candidate-node"] == nil {
		panic("candidate node not verified")
	}
	readOwned := func(label, command string) employeeDisk {
		out, err := exec.Command("python3", "tests/e2eboot/local_bot_smoke_launcher.py", "--state", statePath, "--employee-command", command).Output()
		must(err)
		must(os.WriteFile(filepath.Join(root, "employee-"+label+".json"), out, 0600))
		var disk employeeDisk
		must(json.Unmarshal(out, &disk))
		return disk
	}
	saveEvidence := func(name string, value any) {
		raw, err := json.MarshalIndent(value, "", "  ")
		must(err)
		must(os.WriteFile(filepath.Join(root, "employee-"+name+".json"), raw, 0600))
	}
	stage("employee-existing-bot-read")
	legacy := employeeStartLegacy(page, api, state, root, saveEvidence, func(key string) {
		if actions[key] {
			panic("duplicate legacy action")
		}
		actions[key] = true
		checkpoint()
	})
	legacyReceipt := func(name, detail, source string) {
		receipts[name] = map[string]any{"Candidate": state["binary_sha256"], "BotID": id, "Home": home, "Detail": detail, "Source": source, "SchemaVersion": 1}
		for k, v := range legacy.result {
			receipts[name][k] = v
		}
		checkpoint()
		pass(name)
	}
	legacyReceipt("existing-bot-read-no-authority", "Owned inert existing Bot remained unmandated after Home/list/snapshot/history reads", "employee-legacy-fixture-disk.json + employee-legacy-read-authority-disk.json")
	legacyTarget, legacyOperation := legacy.change("schedule", employeeLegacyMessage, store.BotControlScheduleCreate)
	var legacyRef store.BotMandateRef
	boundedWait("legacy successful mandated admission", 4*time.Minute, func() bool {
		for _, r := range legacy.runs() {
			if r.ID == legacyTarget.ID {
				if !r.Status.Terminal() {
					return false
				}
				if r.Status != store.RunSucceeded {
					panic("legacy adoption failed")
				}
			}
		}
		var err error
		legacyRef, err = employeeLegacyMandate(legacy.disk("adoption"), legacy.fixture, legacyTarget, legacyOperation)
		must(err)
		return true
	})
	legacy.result["SourceRunID"] = legacyTarget.ID.String()
	legacy.result["SourceUserEventID"] = legacyTarget.UserEventID
	legacy.result["MandateID"] = legacyRef.ID.String()
	legacy.result["OperationID"] = legacyOperation.OperationID.String()
	legacyReceipt("existing-bot-adoption", "New natural owner message admitted exact successful Home turn with persisted mandate and applied schedule", "employee-legacy-adoption-disk.json + employee-legacy-schedule-operation.json")
	page().MustNavigate(home)
	page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	quality := startQuality(root, home, state, func(key, message string) store.BotRun {
		boundedWait("quality idle", 6*time.Minute, func() bool { return employeeExecutionIdle(readOwned("quality-idle", "read"), id) })
		return employeeSend(page, home, key, message, snapshot, runs, capture, func() { actions[key] = true; checkpoint() }, saveEvidence)
	}, readOwned, runs)
	stage("employee-natural-schedule")
	before := map[uuid.UUID]bool{}
	for _, r := range runs() {
		before[r.ID] = true
	}
	scheduleTarget := employeeSend(page, home, "schedule", "send this here every minute", snapshot, runs, capture, func() {
		actions["schedule-message-attempted"] = true
		checkpoint()
	}, saveEvidence)
	pending := map[uuid.UUID]bool{}
	boundedWait("natural schedule operation", 4*time.Minute, func() bool {
		return employeeSchedulePoll(snapshot, runs, before, pending, scheduleTarget, func() {
			capture("schedule-pending", botPath+"/snapshot?scope=personal")
		}, func(op store.BotOperationView, b store.Bot) {
			for _, persisted := range readOwned("schedule-transitions", "read").Operations {
				if persisted.OperationID == op.OperationID && persisted.Status == store.BotOperationPending && persisted.BotID == b.ID && persisted.HomeSessionID == b.HomeSessionID && persisted.Revision < op.Revision {
					pending[op.OperationID] = true
				}
			}
			must(validateEmployeeSchedule(op, b, pending))
			var v store.BotVersion
			must(json.Unmarshal([]byte(capture("scheduled-version", fmt.Sprintf("%s/versions/%d?scope=personal", botPath, b.Version))), &v))
			found := false
			for _, t := range v.Triggers {
				if t.ID == op.VerifiedSchedule.TriggerID && t.Enabled && t.Cron == "* * * * *" {
					found = true
				}
			}
			if !found {
				panic("actual trigger differs from every-minute request")
			}
			if page().MustInfo().URL != home {
				panic("schedule changed Home")
			}
			receipt("schedule-natural", "Observed pending-to-applied operation, enabled durable schedule with next_run and every-minute trigger", "employee-schedule-pending.json + employee-snapshot.json + employee-scheduled-version.json")
		})
	})
	// No browser, login, or protected HTTP request is allowed between restart
	// and the durable scheduled-run observation below.
	browser := basePage.Browser()
	// Main owns the browser pointer and closes the current connection on exit.
	// Keep reconnects on that pointer so existing fail-closed cleanup remains wired.
	reopen := func() {
		l := launcher.New().Bin(os.Getenv("ASTONISH_BOT_BROWSER_PATH")).Headless(true).UserDataDir(filepath.Join(root, "chrome-profile")).Set("no-first-run")
		control, err := l.Launch()
		must(err)
		next := rod.New().ControlURL(control).MustConnect()
		*browser = *next
		*basePage = *browser.MustPage()
		page().MustNavigate(base)
		must(redactedAuthentication(func() {
			var credentials map[string]string
			data, err := os.ReadFile(filepath.Join(root, "credentials.json"))
			must(err)
			must(json.Unmarshal(data, &credentials))
			api("POST", "/api/auth/login", map[string]string{"email": credentials["email"], "password": credentials["password"]})
		}))
		page().MustNavigate(home).MustReload()
		page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
		if page().MustInfo().URL != home {
			panic("restart changed Home")
		}
	}
	restart := func(key string) employeeDisk {
		actions[key] = true
		checkpoint()
		must(browser.Close())
		// Chrome shutdown is asynchronous. The launcher checks the actual profile process.
		time.Sleep(2 * time.Second)
		return readOwned(key, key)
	}
	stage("employee-offline-restart-tick")
	pre := readOwned("before-offline", "read")
	var expectedTrigger uuid.UUID
	for _, op := range snapshot().Operations {
		if op.Operation == store.BotControlScheduleCreate && op.VerifiedSchedule != nil {
			expectedTrigger = op.VerifiedSchedule.TriggerID
		}
	}
	if expectedTrigger == uuid.Nil {
		panic("missing expected trigger")
	}
	restartEvidence := restart("restart-tick")
	restartedAt := restartEvidence.RestartedAt
	if restartedAt.IsZero() {
		panic("owned restart lacks process timestamp")
	}
	var offline employeeDisk
	var tick store.BotRun
	boundedWait("offline durable scheduled result", 7*time.Minute, func() bool {
		offline = readOwned("offline-restart-tick", "read")
		for _, row := range offline.Runs {
			if employeeHasRow(pre.Runs, row.ID) {
				continue
			}
			r := employeeRun(row)
			if !employeePostRestartRun(r, uuid.MustParse(id), expectedTrigger, restartedAt) {
				continue
			}
			if r.Status.Terminal() && r.Status != store.RunSucceeded {
				panic("offline scheduled run failed: " + string(r.Status))
			}
			if r.Status == store.RunSucceeded {
				tick = r
				return quality.offline(offline, uuid.MustParse(id), expectedTrigger, restartedAt)
			}
		}
		return false
	})
	receipt("offline-restart-tick", "Native browser closed; owned process restarted; read-only SQLite observed a new successful schedule run before login", "employee-before-offline.json + employee-restart-tick.log + employee-offline-restart-tick.json")
	reopen()
	legacy.observe("restart-active")
	stage("employee-reauth-same-home")
	history := capture("reauth-history", "/api/studio/sessions/"+snapshot().Bot.HomeSessionID.String())
	// Verify one publication in the persisted Home and one formatted assistant
	// bubble with that publication's run identity; raw Markdown is not UI text.
	reportBubbles := employeeReportBubbles(page())
	reportEvidence, err := json.MarshalIndent(reportBubbles, "", "  ")
	must(err)
	must(os.WriteFile(filepath.Join(root, "employee-reauth-report-bubbles.json"), reportEvidence, 0600))
	must(employeeReportExactlyOnce(history, snapshot().Bot.HomeSessionID.String(), tick, reportBubbles))
	quality.restored(page(), history, snapshot().Bot.HomeSessionID.String())
	receipt("reauth-same-home", "One login after offline evidence; scheduled result appears once in unchanged Home", "employee-reauth-history.json + employee-reauth-report-bubbles.json")
	idle := func() {
		boundedWait("employee idle (execution ownership cleared)", 6*time.Minute, func() bool {
			return employeeExecutionIdle(readOwned("idle", "read"), id)
		})
	}
	send := func(key, message string) store.BotRun {
		// Owner messages use the native priority queue even while recurring work
		// is queued/running. Global idle is a teardown barrier, not admission.
		if actions[key] {
			panic("refusing duplicate employee action: " + key)
		}
		return employeeSend(page, home, key, message, snapshot, runs, capture, func() {
			actions[key] = true
			checkpoint()
		}, saveEvidence)
	}
	change := func(key, message string, operation store.BotControlOperation, exception bool) store.BotOperationView {
		stage("employee-" + key)
		seen := map[uuid.UUID]bool{}
		for _, op := range snapshot().Operations {
			seen[op.OperationID] = true
		}
		prior := map[uuid.UUID]bool{}
		for _, r := range runs() {
			prior[r.ID] = true
		}
		target := send(key, message)
		var result store.BotOperationView
		boundedWait(key, 4*time.Minute, func() bool {
			return employeeChangePoll(snapshot, runs, prior, seen, target, operation, exception, func(op store.BotOperationView, _ store.Bot) { result = op })
		})
		capture(key, botPath+"/snapshot?scope=personal")
		return result
	}
	change("routine-memory", "Remember that I prefer weather temperatures in Celsius.", store.BotControlMemoryRemember, false)
	memory := capture("memory", botPath+"/memory?scope=personal")
	if !strings.Contains(strings.ToLower(memory), "celsius") {
		panic("memory write not persisted")
	}
	receipt("routine-memory", "Natural memory request applied without review; Celsius preference persisted", "employee-routine-memory.json + employee-memory.json")
	cadence := change("cadence-change", "Change these weather updates to every two minutes instead.", store.BotControlScheduleEdit, false)
	if cadence.VerifiedSchedule == nil || !cadence.VerifiedSchedule.Enabled || cadence.VerifiedSchedule.NextRun == nil {
		panic("cadence lacks durable job readback")
	}
	var version store.BotVersion
	must(json.Unmarshal([]byte(capture("cadence-version", fmt.Sprintf("%s/versions/%d?scope=personal", botPath, snapshot().Bot.Version))), &version))
	found := false
	for _, tr := range version.Triggers {
		if tr.ID == cadence.VerifiedSchedule.TriggerID && tr.Cron == "*/2 * * * *" && tr.Enabled {
			found = true
		}
	}
	if !found {
		panic("two-minute cadence not persisted")
	}
	receipt("cadence-change", "Natural cadence edit persisted enabled two-minute trigger and next run", "employee-cadence-change.json + employee-cadence-version.json")
	if quality != nil {
		quality.cadence(readOwned("quality-cadence", "read"))
		quality.ownerUpdate(send, readOwned, runs)
	}
	legacy.change("cadence", "Change only the new weekly preservation witness reminder to every Tuesday at midnight UTC. Leave the historical June reminder disabled.", store.BotControlScheduleEdit)
	legacy.change("pause", "Pause all background work until I ask you to resume.", store.BotControlPriorityPause)
	boundedWait("legacy pause stopped", 4*time.Minute, func() bool { return employeePauseStopped(legacy.disk("paused-stopped"), legacy.fixture.BotID.String()) })
	page().MustNavigate(home)
	page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	change("pause", "Pause all background work until I explicitly ask you to resume.", store.BotControlPriorityPause, false)
	if snapshot().Bot.Status != store.BotPaused {
		panic("Bot not paused")
	}
	var paused employeeDisk
	boundedWait("employee pause (ownership cleared and schedules disabled)", 6*time.Minute, func() bool {
		paused = readOwned("paused-before", "read")
		return employeePauseStopped(paused, id)
	})
	receipt("pause", "Explicit pause applied; active-run/teardown barrier and leases cleared, schedules paused with no next run", "employee-pause.json + employee-paused-before.json")
	restart("restart-paused")
	for end := time.Now().Add(130 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Second) {
		disk := readOwned("paused-after", "read")
		if len(disk.Runs) != len(paused.Runs) {
			panic("paused work restarted at due boundary")
		}
	}
	reopen()
	legacy.observe("restart-paused")
	pausedRuns := runs()
	send("paused-conversation", "Which temperature units do I prefer?")
	boundedWait("paused conversation answer", 4*time.Minute, func() bool {
		for _, r := range runs() {
			old := false
			for _, prior := range pausedRuns {
				old = old || prior.ID == r.ID
			}
			if old {
				continue
			}
			if r.Trigger == store.TriggerCron {
				panic("paused conversation resumed background work")
			}
			if !r.Status.Terminal() {
				return false
			}
			if r.Status != store.RunSucceeded {
				panic("paused conversation unsuccessful")
			}
			transcript := capture("paused-answer", botPath+"/sessions/"+r.SessionID.String()+"/stream?scope=personal")
			messages, err := chatModelMessages(transcript)
			must(err)
			for _, m := range messages {
				if m.Type == "assistant" && strings.Contains(strings.ToLower(m.Content), "celsius") {
					return true
				}
			}
			// The finite SSE parser projects tools only; use this run's durable report, never old Home text.
			return strings.Contains(strings.ToLower(r.Report.Summary), "celsius")
		}
		return false
	})
	change("paused-goal", "Keep a goal named Orlando weather clarity: make each weather update easy to understand. Stay paused.", store.BotControlGoalCreate, false)
	if snapshot().Bot.Status != store.BotPaused {
		panic("conversation or goal resumed paused Bot")
	}
	capture("paused-history", "/api/studio/sessions/"+snapshot().Bot.HomeSessionID.String())
	receipt("while-paused", "No runs over due boundary and restart; conversation recalls memory and goal edit works without resuming", "employee-paused-before.json + employee-paused-after.json + employee-paused-goal.json + employee-paused-history.json")
	legacy.change("resume", "Resume the weekly preservation witness reminder. Keep the historical June reminder disabled.", store.BotControlResumeTasks)
	page().MustNavigate(home)
	page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	change("resume", "Resume the weather updates every two minutes now.", store.BotControlResumeTasks, false)
	if snapshot().Bot.Status != store.BotActive {
		panic("explicit resume did not activate")
	}
	waitTick := func(label string, since time.Time) employeeDisk {
		var observed employeeDisk
		boundedWait(label+" successful scheduled execution", 7*time.Minute, func() bool {
			observed = readOwned(label, "read")
			for _, row := range observed.Runs {
				r := employeeRun(row)
				if !employeePostRestartRun(r, uuid.MustParse(id), expectedTrigger, since) {
					continue
				}
				if r.Status.Terminal() && r.Status != store.RunSucceeded {
					panic(label + ": scheduled execution failed")
				}
				if r.Status == store.RunSucceeded {
					return true
				}
			}
			return false
		})
		return observed
	}
	resumedAt := time.Now()
	waitTick("resume-tick", resumedAt)
	receipt("resume", "Explicit resume re-enabled background work and a new exact-trigger execution succeeded", "employee-resume.json + employee-resume-tick.json")
	// The operator fixture excludes memory_forget from the routine grant while
	// retaining it in the finite operator ceiling. Never simulate an external effect.
	beforeDeny := capture("denied-memory-before", botPath+"/memory?scope=personal")
	denied := change("exception-denied", "Forget my saved temperature preference.", store.BotControlMemoryForget, true)
	must(employeeException(denied))
	card := `[data-operation-id="` + denied.OperationID.String() + `"]`
	cardDOM, cardErr := employeeReadExceptionCard(page(), denied)
	must(cardErr)
	must(os.WriteFile(filepath.Join(root, "employee-denied-card.txt"), []byte(cardDOM.Text), 0600))
	must(employeeExceptionCard(denied, cardDOM))
	actions["deny-"+denied.OperationID.String()] = true
	checkpoint()
	page().MustElementR(card+" button", "^Deny$").MustClick()
	boundedWait("denied exception cancelled", time.Minute, func() bool {
		for _, op := range snapshot().Operations {
			if op.OperationID == denied.OperationID {
				return op.Status == store.BotOperationCancelled
			}
		}
		return false
	})
	if after := capture("denied-memory-immediate", botPath+"/memory?scope=personal"); after != beforeDeny {
		panic("denied action changed memory before restart")
	}
	deniedRestart := restart("restart-denied")
	disk := waitTick("denied-after-restart", deniedRestart.RestartedAt)
	must(employeeForgetLedger(disk.Operations, denied.OperationID, uuid.Nil))
	reopen()
	legacy.observe("restart-resumed")
	if !reflect.DeepEqual(legacy.stages, employeeHistoryStages) {
		panic("missing historical schedule lifecycle")
	}
	legacy.result["Stages"] = legacy.stages
	legacyReceipt("historical-disabled-schedule", "Exact historical disabled trigger and paused job survived adoption, schedule/cadence/pause/resume and owned restarts without execution", "employee-legacy-{fixture,read,schedule,restart-active,cadence,pause,restart-paused,resume,restart-resumed}-{disk,version}.json")
	if after := capture("denied-memory-after", botPath+"/memory?scope=personal"); after != beforeDeny {
		panic("denied memory action changed persisted memory")
	}
	for _, op := range snapshot().Operations {
		if op.Operation == store.BotControlMemoryForget && op.OperationID != denied.OperationID {
			panic("denied request repeated without fresh user intent")
		}
	}
	receipt("exception-denied-no-repeat", "Exact memory-forget exception denied; no effect or repeated request after due boundary and restart", "employee-exception-denied.json + employee-denied-memory-before.json + employee-denied-memory-after.json + employee-denied-after-restart.json")
	approved := change("exception-approved", "I have changed my mind. Please forget my saved temperature preference now.", store.BotControlMemoryForget, true)
	must(employeeException(approved))
	if approved.OperationID == denied.OperationID || approved.SourceUserEventID == denied.SourceUserEventID || approved.SourceRunID == denied.SourceRunID || approved.Exception.PayloadDigest == "" {
		panic("approval reused denied operation")
	}
	card = `[data-operation-id="` + approved.OperationID.String() + `"]`
	cardDOM, cardErr = employeeReadExceptionCard(page(), approved)
	must(cardErr)
	must(os.WriteFile(filepath.Join(root, "employee-approved-card.txt"), []byte(cardDOM.Text), 0600))
	must(employeeExceptionCard(approved, cardDOM))
	actions["approve-"+approved.OperationID.String()] = true
	checkpoint()
	page().MustElementR(card+" button", "^Approve ").MustClick()
	boundedWait("approved exception applied", time.Minute, func() bool {
		for _, op := range snapshot().Operations {
			if op.OperationID == approved.OperationID {
				return op.Status == store.BotOperationApplied
			}
		}
		return false
	})
	must(employeeForgottenMemory(beforeDeny, capture("approved-memory", botPath+"/memory?scope=personal"), approved))
	page().MustReload()
	finalOps := waitTick("approved-after-tick", time.Now()).Operations
	must(employeeForgetLedger(finalOps, denied.OperationID, approved.OperationID))
	must(employeeForgottenMemory(beforeDeny, capture("approved-memory-after-tick", botPath+"/memory?scope=personal"), approved))
	capture("approved-final", botPath+"/snapshot?scope=personal")
	receipt("exception-approved-once", "Distinct exact exception approved once; preference removed and no replay after reload/tick", "employee-exception-approved.json + employee-approved-memory.json")
	stage("employee-delete-preserves-peer")
	peerCapture := func(label, path string, format employeePeerFormat) employeePeerPayload {
		p, err := employeeCapturePeer(page(), root, label, path, format)
		must(err)
		return p
	}
	peerJSON := func(label, path string) employeePeerPayload { return peerCapture(label, path, employeePeerJSON) }
	// Create once through UI. The original workspace remains mounted while the
	// asynchronous New Bot request is pending, so visibility is not navigation.
	originalBot := snapshot().Bot
	var peerInventory struct{ Bots []store.Bot }
	must(peerJSON("peer-list-before", "/api/studio/bots?scope=personal").decode(&peerInventory))
	peerBeforeIDs := map[uuid.UUID]bool{}
	for _, b := range peerInventory.Bots {
		peerBeforeIDs[b.ID] = true
	}
	if actions["peer-create"] {
		panic("preservation peer creation already attempted; refusing duplicate")
	}
	page().MustElementR("button", "^New Bot$").MustClick()
	page().MustElement(`[aria-label="Bot name"]`).MustInput(employeePeerTitle)
	actions["peer-create"] = true
	checkpoint()
	page().MustElementR(`[role="dialog"] button`, "^Start conversation$").MustClick()
	peerID, peerErr := employeeWaitPeerHome(page(), base, id)
	must(peerErr)
	peerBot, peerErr := employeeCreatedPeer(peerJSON("peer-list-created", "/api/studio/bots?scope=personal").Body, peerID, originalBot, peerBeforeIDs)
	must(peerErr)
	saveEvidence("peer-identity", map[string]any{"bot": peerBot, "home_url": page().MustInfo().URL})
	peerTitleStarted := time.Now()
	var admittedPeerTitle store.BotRun
	boundedWait("preservation peer terminal", 6*time.Minute, func() bool {
		var rr struct{ Runs []store.BotRun }
		must(peerJSON("peer-title-runs", "/api/studio/bots/"+peerID+"/runs?scope=personal").decode(&rr))
		historyPath := employeePeerHistoryPath(peerBot)
		// api retains the exact GET path/status on HTTP failure; do not treat
		// authorization/not-found errors as transient evidence or admission.
		saveEvidence("peer-title-observation", map[string]any{"path": historyPath, "elapsed_ms": time.Since(peerTitleStarted).Milliseconds(), "runs": rr.Runs, "verified": false})
		titleRun, err := peerCapture("peer-title-history", historyPath, employeePeerSSE).titleRun(peerBot, rr.Runs)
		if err != nil {
			panic(fmt.Sprintf("preservation peer history GET %s HTTP 200 after %s: %v", historyPath, time.Since(peerTitleStarted).Round(time.Millisecond), err))
		}
		if titleRun.ID == uuid.Nil {
			return false // Incomplete/empty persisted snapshot is not a source.
		}
		if admittedPeerTitle.ID != uuid.Nil && (admittedPeerTitle.ID != titleRun.ID || admittedPeerTitle.UserEventID != titleRun.UserEventID || admittedPeerTitle.HistoryCursor != titleRun.HistoryCursor) {
			panic("preservation peer title admission changed during observation")
		}
		admittedPeerTitle = titleRun
		for _, r := range rr.Runs {
			if !r.Status.Terminal() {
				return false
			}
			if r.Status != store.RunSucceeded {
				panic("preservation peer title run failed")
			}
		}
		saveEvidence("peer-title-run", titleRun)
		return true
	})
	peerPath := "/api/studio/bots/" + peerID
	page().MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput("Remember that my preservation witness code is violet-cairn-731.")
	page().MustElement(`[aria-label="Bot workspace"] [data-testid="send-button"]`).MustClick()
	boundedWait("peer memory persisted", 4*time.Minute, func() bool {
		return strings.Contains(peerJSON("peer-memory-seeding", peerPath+"/memory?scope=personal").Body, "violet-cairn-731")
	})
	boundedWait("peer memory turn terminal", 4*time.Minute, func() bool {
		var rr struct{ Runs []store.BotRun }
		must(peerJSON("peer-memory-runs", peerPath+"/runs?scope=personal").decode(&rr))
		for _, r := range rr.Runs {
			if !r.Status.Terminal() {
				return false
			}
			if r.Status != store.RunSucceeded {
				panic("peer witness run failed")
			}
		}
		return len(rr.Runs) >= 2
	})
	var peerSnapshot store.BotSnapshot
	must(peerJSON("peer-snapshot-before", peerPath+"/snapshot?scope=personal").decode(&peerSnapshot))
	peerHome := peerSnapshot.Bot.HomeSessionID.String()
	peerPaths := map[string]string{"bot": peerPath + "?scope=personal", "history": "/api/studio/sessions/" + peerHome, "memory": peerPath + "/memory?scope=personal", "runs": peerPath + "/runs?scope=personal"}
	peerBefore := map[string]string{}
	for label, path := range peerPaths {
		peerBefore[label] = peerJSON("peer-"+label+"-before", path).Body
	}
	if !strings.Contains(peerBefore["history"], "violet-cairn-731") {
		panic("peer history witness absent")
	}
	// Volume identity is independently read from the owned database, not guessed.
	peerState := map[string]any{}
	for key, value := range state {
		peerState[key] = value
	}
	peerState["bot_id"] = peerID
	peerStateRaw, err := json.Marshal(peerState)
	must(err)
	peerPhysical, err := exec.Command("python3", "-c", "import json,sys; sys.path.insert(0,'tests/e2eboot'); import local_bot_smoke_launcher as l; print(json.dumps(l.employee_readonly(json.loads(sys.argv[1]))))", string(peerStateRaw)).Output()
	must(err)
	must(os.WriteFile(filepath.Join(root, "employee-peer-disk-before.json"), peerPhysical, 0600))
	peerVolume, err := employeeDiskHomeVolume(peerPhysical, "employee-peer-disk-before.json", peerID)
	must(err)
	peerVolumeBefore, err := exec.Command("docker", "volume", "inspect", peerVolume).Output()
	must(err)
	must(os.WriteFile(filepath.Join(root, "employee-peer-volume-before.json"), peerVolumeBefore, 0600))
	page().MustNavigate(home)
	idle()
	deletion := readOwned("delete-before", "read")
	// Recheck this exact capture: the earlier idle observation may be stale.
	// Keep it for the final gate; deleted rows cannot prove stopped execution.
	_, err = employeeFinalStoppedEvidence(deletion, id)
	must(err)
	if len(deletion.Bots) != 1 {
		panic("missing physical Home evidence")
	}
	volumeName, err := employeeHomeVolume(deletion.Bots[0].HomeBinding, id)
	must(err)
	physicalBefore, err := exec.Command("docker", "volume", "inspect", volumeName).Output()
	must(err)
	must(os.WriteFile(filepath.Join(root, "employee-delete-volume-before.json"), physicalBefore, 0600))
	page().MustElementR("button", "^Details$").MustClick()
	page().MustElementR("button", "^Delete Bot$").MustClick()
	page().MustElementR(`[role="dialog"]`, "Permanently delete").MustVisible()
	actions["hard-delete"] = true
	checkpoint()
	page().MustElementR(`[role="dialog"] button`, "^Delete$").MustClick()
	boundedWait("permanent deletion durable cleanup", 4*time.Minute, func() bool {
		d := readOwned("delete-after", "read")
		return len(d.Bots) == 0 && len(d.Runs) == 0 && len(d.Events) == 0 && len(d.Jobs) == 0 && len(d.Goals) == 0 && len(d.Versions) == 0 && len(d.Approvals) == 0 && len(d.Sessions) == 0
	})
	for label, path := range peerPaths {
		if peerJSON("peer-"+label+"-after", path).Body != peerBefore[label] {
			panic("deletion altered peer " + label)
		}
	}
	peerVolumeAfter, err := exec.Command("docker", "volume", "inspect", peerVolume).Output()
	must(err)
	must(os.WriteFile(filepath.Join(root, "employee-peer-volume-after.json"), peerVolumeAfter, 0600))
	if string(peerVolumeAfter) != string(peerVolumeBefore) {
		panic("deletion altered peer physical Home")
	}
	volumes, err := exec.Command("docker", "volume", "ls", "--format", "{{.Name}}").Output()
	must(err)
	for _, name := range strings.Fields(string(volumes)) {
		if name == volumeName {
			panic("deleted physical Home volume remains")
		}
	}
	must(os.WriteFile(filepath.Join(root, "employee-delete-volumes.txt"), volumes, 0600))
	receipt("hard-delete-preserves-peer", "Native permanent deletion removed Bot, jobs, runs, history and physical Home; peer unchanged", "employee-delete-before.json + employee-delete-after.json + employee-delete-volumes.txt + employee-peer-{bot,history,memory,runs,volume}-{before,after}.json")
	stage("employee-terminal-evidence")
	detail, err := employeeFinalStoppedEvidence(deletion, id)
	must(err)
	receipt("terminal-evidence", detail, "employee-delete-before.json")
	for _, required := range employeeRequired {
		if receipts[required] == nil {
			panic("missing required employee receipt: " + required)
		}
	}
	if len(receipts) != len(employeeRequired) {
		panic("unknown employee receipt")
	}
	evidence["unverified"] = false
	evidence["status"] = "verified"
	checkpoint()
	quality.finish(evidence)
	save()
}

var employeeRequired = []string{"title-weather", "candidate-node", "schedule-natural", "offline-restart-tick", "reauth-same-home", "routine-memory", "cadence-change", "pause", "while-paused", "resume", "exception-denied-no-repeat", "exception-approved-once", "hard-delete-preserves-peer", "terminal-evidence", "existing-bot-read-no-authority", "existing-bot-adoption", "historical-disabled-schedule"}

type employeeDiskRow struct {
	Status         string          `json:"status"`
	ActiveRunID    json.RawMessage `json:"active_run_id"`
	LeaseExpiresAt json.RawMessage `json:"lease_expires_at"`
	ID             string          `json:"id"`
	BotID          string          `json:"bot_id"`
	Data           string          `json:"data"`
	HomeBinding    string          `json:"home_binding"`
}
type employeeDisk struct {
	RestartedAt                                        time.Time `json:"restarted_at"`
	Bots, Runs                                         []employeeDiskRow
	Events, Jobs, Goals, Versions, Approvals, Sessions []json.RawMessage
	Operations                                         []store.BotOperationView
}

func employeeHomeVolume(raw, bot string) (string, error) {
	raw, _, err := employeeStoredDocument(raw)
	if err != nil {
		return "", fmt.Errorf("home_binding document: %w", err)
	}
	var binding store.BotHomeBinding
	if err := json.Unmarshal([]byte(raw), &binding); err != nil {
		return "", fmt.Errorf("home_binding store.BotHomeBinding: %w", err)
	}
	// StorageKey is the opaque backend ref, not serialized sandbox.BotHomeBinding.
	if binding.ID == uuid.Nil || binding.BotID == uuid.Nil || binding.BotID.String() != bot || binding.Scope != store.BotScopePersonal || binding.Revision < 1 || binding.Backend != string(sandbox.BackendKindDocker) {
		return "", fmt.Errorf("home_binding physical Home identity mismatch")
	}
	if len(binding.StorageKey) < 2 {
		return "", fmt.Errorf("home_binding.storage_key: invalid Docker volume name")
	}
	for i, c := range binding.StorageKey {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || (i > 0 && (c == '_' || c == '.' || c == '-')) {
			continue
		}
		return "", fmt.Errorf("home_binding.storage_key: invalid Docker volume name")
	}
	return binding.StorageKey, nil
}

func employeePostRestartRun(r store.BotRun, bot, trigger uuid.UUID, restarted time.Time) bool {
	return bot != uuid.Nil && trigger != uuid.Nil && !restarted.IsZero() && r.BotID == bot && r.Trigger == store.TriggerCron && r.TriggerID == trigger && r.CreatedAt.After(restarted) && r.StartedAt != nil && r.StartedAt.After(restarted)
}

func employeeHasRow(rows []employeeDiskRow, id string) bool {
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}

// employeeDiskObject rejects duplicate/case-aliased members before Go's JSON
// decoder can silently choose or merge them. Keep values raw (not float64 maps)
// so persisted counters and run evidence retain their exact precision.
func employeeDiskObject(raw string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("invalid disk document object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid disk document object")
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("invalid disk document member")
		}
		key = strings.ToLower(key)
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("ambiguous disk document member")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("invalid disk document value")
		}
		fields[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, fmt.Errorf("invalid disk document object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing disk document data")
	}
	return fields, nil
}

// employeeStoredDocument is the shared owned-storage boundary for run data and
// Home bindings. Unwrap at most one precision-preserving bot_document string.
func employeeStoredDocument(raw string) (string, map[string]json.RawMessage, error) {
	fields, err := employeeDiskObject(raw)
	if err != nil {
		return "", nil, err
	}
	if document, wrapped := fields["bot_document"]; wrapped {
		// Never fall back to a sibling document if the wrapper is malformed.
		if len(fields) != 1 || string(document) == "null" || json.Unmarshal(document, &raw) != nil {
			return "", nil, fmt.Errorf("invalid disk document wrapper")
		}
		fields, err = employeeDiskObject(raw)
		if err != nil {
			return "", nil, err
		}
	}
	if _, wrapped := fields["bot_document"]; wrapped {
		return "", nil, fmt.Errorf("recursive disk document wrapper")
	}
	return raw, fields, nil
}

func employeeRun(row employeeDiskRow) store.BotRun {
	raw, fields, err := employeeStoredDocument(row.Data)
	must(err)
	if run, nested := fields["run"]; nested {
		// Private repository metadata may accompany Run, but public run
		// fields alongside it would provide a second, ambiguous identity/state.
		runType := reflect.TypeFor[store.BotRun]()
		for i := 0; i < runType.NumField(); i++ {
			key := strings.Split(runType.Field(i).Tag.Get("json"), ",")[0]
			if key != "-" {
				if _, exists := fields[key]; exists {
					panic("ambiguous flat and nested disk run")
				}
			}
		}
		raw = string(run)
		fields, err = employeeDiskObject(raw)
		must(err)
		if _, exists := fields["run"]; exists {
			panic("recursive disk run envelope")
		}
		if _, exists := fields["bot_document"]; exists {
			panic("recursive disk run wrapper")
		}
	}
	var run store.BotRun
	if err := json.Unmarshal([]byte(raw), &run); err != nil {
		// Do not leak raw private repository metadata in diagnostics.
		panic("invalid disk run data")
	}
	if run.ID == uuid.Nil || run.ID.String() != row.ID {
		panic("disk run identity mismatch")
	}
	if row.BotID != "" && run.BotID.String() != row.BotID {
		panic("disk run Bot identity mismatch")
	}
	return run
}
func employeeWebEvidence(raw string) bool {
	messages, err := chatModelMessages(raw)
	if err != nil {
		return false
	}
	calls := map[string]string{}
	for _, m := range messages {
		if m.Type == "tool_call" && m.ToolID != "" && (m.ToolName == "http_request" || m.ToolName == "web_fetch" || m.ToolName == "web_search" || m.ToolName == "browser_navigate") {
			calls[m.ToolID] = m.ToolName
		}
		if m.Type == "tool_result" && m.ToolID != "" && m.ToolName != "" && calls[m.ToolID] == m.ToolName && successfulWebResult(m.ToolResult) {
			return true
		}
	}
	return false
}

// Examine all persisted revisions: a new blocked request is a replay too.
func employeeForgetLedger(ops []store.BotOperationView, denied, approved uuid.UUID) error {
	cancelled, applied := false, false
	for _, op := range ops {
		if op.Operation != store.BotControlMemoryForget {
			continue
		}
		if op.OperationID != denied && op.OperationID != approved {
			return fmt.Errorf("unrequested memory-forget operation")
		}
		if op.OperationID == denied {
			if op.Status == store.BotOperationApplied {
				return fmt.Errorf("denied action applied")
			}
			cancelled = cancelled || op.Status == store.BotOperationCancelled
		} else {
			applied = applied || op.Status == store.BotOperationApplied
		}
	}
	if !cancelled || (approved != uuid.Nil && !applied) {
		return fmt.Errorf("missing terminal exception evidence")
	}
	return nil
}

func employeeException(op store.BotOperationView) error {
	if op.Validate() != nil || op.Status != store.BotOperationBlocked || op.Decision != store.BotOperationRequireAuthorization || op.Exception == nil || op.Exception.OperationID != op.OperationID || op.Exception.PayloadDigest == "" || !op.Exception.ExpiresAt.After(time.Now()) || op.Operation != store.BotControlMemoryForget {
		return fmt.Errorf("not a bounded memory-forget exception")
	}
	for _, field := range op.Exception.Fields {
		if field.Caption == "Memory" && field.Value != "" {
			return nil
		}
	}
	return fmt.Errorf("exception lacks exact memory identity")
}
