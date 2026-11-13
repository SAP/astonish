package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/google/uuid"
)

// invokeAgentLearning is deliberately separate from factual/adapter proof. A
// failed journey retains its partial receipts and never clears an obligation.
func invokeAgentLearning(root, home string, state, evidence map[string]any, hooks agentSkillsJourneyHooks, run func(string, string, map[string]any, map[string]any, agentSkillsJourneyHooks)) {
	defer func() {
		if failure := recover(); failure != nil {
			evidence["learning_blocker"] = fmt.Sprint(failure)
		}
		hooks.Save()
	}()
	run(root, home, state, evidence, hooks)
	learning, ok := evidence["learning"].(map[string]any)
	if !ok || learning["status"] != "verified" || learning["contract"] != agentSkillsContract || learning["nonce"] != state["agent_nonce"] {
		return
	}
	journeys, ok := evidence["journeys"].(map[string]any)
	if !ok {
		journeys = map[string]any{}
		evidence["journeys"] = journeys
	}
	for _, name := range []string{"feedback-restart", "skill-three-scheduler-occurrences", "schedule-delete-manual-reuse"} {
		journeys[name+"-outcome"] = map[string]any{"status": "verified", "source": "learning"}
	}
}

// agentLearningTerminalSend observes only the existing final manual reuse.
func agentLearningTerminalSend(send func(string, string) store.BotRun, wait func(store.BotRun) store.BotRun, observe func(func() error) (map[string]string, error), evidence map[string]any, persist, save func()) func(string, string) store.BotRun {
	attempted := false
	return func(key, message string) (result store.BotRun) {
		if key != "skills-after-removal" {
			return send(key, message)
		}
		if attempted {
			panic("duplicate terminal-observed manual reuse")
		}
		attempted = true
		defer func() {
			failure := recover()
			if failure != nil && evidence["terminal_blocker"] == nil {
				evidence["terminal_blocker"] = "candidate terminal observer or manual reuse failed"
			}
			persist() // retain the independently read CLI digest even on failure
			save()
			if failure != nil {
				panic(failure)
			}
		}()
		started, settled := false, false
		receipt, err := observe(func() error {
			if started {
				return fmt.Errorf("duplicate terminal work callback")
			}
			started = true // never retry an uncertain Send
			result = send(key, message)
			result = wait(result)
			settled = true
			return nil
		})
		if receipt != nil {
			evidence["terminal_manifest"] = receipt
		}
		if err != nil {
			evidence["terminal_blocker"] = err.Error()
		}
		if !settled {
			panic("terminal observer did not complete existing manual reuse")
		}
		// A capture failure must not replay a paid request or discard its run.
		return result
	}
}

func agentLearningLifecycle(basePage *rod.Page, api func(string, string, any) string, state map[string]any, statePath, root, base string, evidence map[string]any, persist, save func()) {
	page := func() *rod.Page { return independentPageTimeout(basePage, 40*time.Second) }
	// Never reuse one of the stock/document Bots, even if its preceding case failed.
	page().MustNavigate(base + "/#/chat")
	page().MustElementR("button", "^New Bot$").MustClick()
	page().MustElement(`[aria-label="Bot name"]`).MustInput("Weather in Orlando, FL today")
	state["agent_learning_creation_attempted"] = true
	persist()
	save()
	page().MustElementR("button", "^Start conversation$").MustClick()
	page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	home := page().MustInfo().URL
	prefix := base + "/#/chat/bots/personal/"
	if !strings.HasPrefix(home, prefix) {
		panic("agent learning left owned Home")
	}
	id, err := uuid.Parse(strings.TrimPrefix(home, prefix))
	must(err)
	state["bot_id"] = id.String()
	state["agent_learning_bot_id"] = id.String()
	persist()
	save()
	actions := map[string]bool{}
	receipts := map[string]any{}
	checkpoint := func() {
		persist()
		raw, err := json.MarshalIndent(map[string]any{"contract": agentLiveContract, "nonce": state["agent_nonce"], "candidate": state["binary_sha256"], "browser_candidate": state["browser_sha256"], "bot_id": id.String(), "actions": actions, "receipts": receipts}, "", "  ")
		must(err)
		must(os.WriteFile(filepath.Join(root, "agent-learning-checkpoint.json"), raw, 0600))
		save()
	}
	checkpoint()
	capture := func(name, path string) string {
		raw := api("GET", path, nil)
		must(os.WriteFile(filepath.Join(root, "agent-learning-"+name+".json"), []byte(raw), 0600))
		return raw
	}
	record := func(name string, value any) {
		raw, err := json.MarshalIndent(value, "", "  ")
		must(err)
		must(os.WriteFile(filepath.Join(root, "agent-learning-"+name+".json"), raw, 0600))
		checkpoint()
	}
	botPath := "/api/studio/bots/" + id.String()
	snapshot := func() store.BotSnapshot {
		var s store.BotSnapshot
		must(json.Unmarshal([]byte(capture("snapshot", botPath+"/snapshot?scope=personal")), &s))
		if s.Bot.ID != id {
			panic("agent learning snapshot identity mismatch")
		}
		return s
	}
	runs := func() []store.BotRun {
		var rows struct{ Runs []store.BotRun }
		must(json.Unmarshal([]byte(capture("runs", botPath+"/runs?scope=personal")), &rows))
		return rows.Runs
	}
	read := func(label, command string) employeeDisk {
		out, err := exec.Command("python3", "tests/e2eboot/local_bot_smoke_launcher.py", "--state", statePath, "--employee-command", command).Output()
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				panic(fmt.Sprintf("owned learning observer: %s", e.Stderr))
			}
			must(err)
		}
		must(os.WriteFile(filepath.Join(root, "agent-learning-"+label+".json"), out, 0600))
		// The launcher records restart attempts and observer identities in state.
		// Preserve those on the next Go checkpoint; never erase replay barriers.
		updated, err := os.ReadFile(statePath)
		must(err)
		var latest map[string]any
		must(json.Unmarshal(updated, &latest))
		for key, value := range latest {
			state[key] = value
		}
		var disk employeeDisk
		must(json.Unmarshal(out, &disk))
		return disk
	}
	send := func(key, message string) store.BotRun {
		if actions[key] {
			panic("duplicate agent learning action: " + key)
		}
		boundedWait("agent learning idle", 8*time.Minute, func() bool { return employeeExecutionIdle(read("idle", "read"), id.String()) })
		return employeeSend(page, home, key, message, snapshot, runs, capture, func() { actions[key] = true; checkpoint() }, record)
	}
	change := func(key, message string, operation store.BotControlOperation, exception bool) store.BotOperationView {
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
		record(key, result)
		return result
	}
	schedule := func(skill uuid.UUID) (store.BotVersion, uuid.UUID) {
		op := change("agent-learning-schedule", "Send my Orlando weather report here every minute using my saved skill "+skill.String()+" and its updated requirements.", store.BotControlScheduleCreate, false)
		pending := map[uuid.UUID]bool{}
		for _, p := range read("schedule-transitions", "read").Operations {
			if p.OperationID == op.OperationID && p.Status == store.BotOperationPending && p.Revision < op.Revision {
				pending[p.OperationID] = true
			}
		}
		b := snapshot().Bot
		must(validateEmployeeSchedule(op, b, pending))
		var v store.BotVersion
		must(json.Unmarshal([]byte(capture("scheduled-version", fmt.Sprintf("%s/versions/%d?scope=personal", botPath, b.Version))), &v))
		found := false
		for _, t := range v.Triggers {
			if t.ID == op.VerifiedSchedule.TriggerID && t.Enabled && t.Cron == "* * * * *" && t.SkillID != nil && *t.SkillID == skill {
				found = true
			}
		}
		if !found {
			panic("agent learning schedule lacks requested skill/every-minute trigger")
		}
		receipts["schedule-natural"] = op
		checkpoint()
		return v, op.VerifiedSchedule.TriggerID
	}
	browser := basePage.Browser()
	restart := func() time.Time {
		actions["restart-tick"] = true
		checkpoint()
		must(browser.Close())
		time.Sleep(2 * time.Second)
		return read("restart-tick", "restart-tick").RestartedAt
	}
	reconnect := func() {
		control, err := launcher.New().Bin(os.Getenv("ASTONISH_BOT_BROWSER_PATH")).Headless(true).UserDataDir(filepath.Join(root, "chrome-profile")).Set("no-first-run").Launch()
		must(err)
		next := rod.New().ControlURL(control).MustConnect()
		*browser = *next
		*basePage = *browser.MustPage()
		page().MustNavigate(base)
		must(redactedAuthentication(func() {
			var credentials map[string]string
			raw, err := os.ReadFile(filepath.Join(root, "credentials.json"))
			must(err)
			must(json.Unmarshal(raw, &credentials))
			api("POST", "/api/auth/login", map[string]string{"email": credentials["email"], "password": credentials["password"]})
		}))
		page().MustNavigate(home).MustReload()
		page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
		if page().MustInfo().URL != home {
			panic("agent learning reconnect changed Home")
		}
	}
	// removeAndInspect invokes this existing key only after restart, reconnect,
	// schedule removal and drained work. Observe that same request, never create
	// an additional request just to produce terminal evidence.
	observedSend := agentLearningTerminalSend(send, func(target store.BotRun) store.BotRun {
		var result store.BotRun
		boundedWait("terminal-observed manual reuse result", 8*time.Minute, func() bool {
			for _, run := range runs() {
				if run.ID == target.ID && run.Status.Terminal() {
					result = run
					return true
				}
			}
			return false
		})
		return result
	}, func(startWork func() error) (map[string]string, error) {
		return agentObserveTerminalDuring(root, base, id.String(), state, startWork)
	}, evidence, persist, save)
	hooks := agentSkillsJourneyHooks{API: api, Send: observedSend, Read: read, Runs: runs, Page: page, Change: change, Schedule: schedule, Restart: restart, Reconnect: reconnect, Save: checkpoint}
	invokeAgentLearning(root, home, state, evidence, hooks, runAgentSkillsJourney)
}
