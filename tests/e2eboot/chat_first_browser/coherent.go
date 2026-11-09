package main

// The default workflow is deliberately fail-closed. Diagnostic recovery branches
// cannot contribute receipts to this workflow. Checkpoints record consent BEFORE
// clicking: ambiguous transport failures may be inspected, never replayed.
import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/google/uuid"
)

var coherentRequired = []string{"minimal-conversation", "responsibility-confirmed", "tool-artifact-reload", "scheduled-tick-return", "exact-approval-continued", "paused-approval-blocked", "memory-next-answer", "details-no-reconnect", "paused-restart-bytes"}

type smokeReceipt struct{ BotID, Candidate, Home, Detail string }
type smokeCheckpoint struct {
	Contract               int
	BotID, Candidate, Home string
	Sent                   map[string]bool
	Before                 map[string]map[string]bool
	IDs                    map[string]string
	Consents               map[string]string
	ConfirmAttempts        map[string][]confirmAttempt
	Receipts               map[string]smokeReceipt
	Restarted              bool
}

// Each successful independent action publishes its own chronological card.
// The write and subsequent load intentionally share a path, not an event.
const expectedArtifactCards = 2

func requireArtifactCards(count int) {
	if count != expectedArtifactCards {
		panic(fmt.Sprintf("expected one artifact card per completed write/load action, got %d", count))
	}
}

func boundedWait(label string, timeout time.Duration, check func() bool) {
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(time.Second) {
		if check() {
			return
		}
	}
	panic("timeout: " + label)
}
func exactGrant(p store.BotControlProposal) bool {
	c := p.Payload.Configuration
	if c == nil {
		return false
	}
	a := c.Autonomy
	tools := map[string]bool{}
	for _, s := range a.AllowedTools {
		tools[s] = true
	}
	return string(a.Level) == "supervised" && len(a.AllowedTools) == 2 && tools["write_file"] && tools["load_file"] && len(a.AllowedPaths) == 1 && a.AllowedPaths[0] == retainedArtifactPath && len(a.AllowedHosts) == 0 && !a.CanContactPeople && !a.CanPublish && a.MaxSpendPerRunMinor == 0 && a.MaxSpendPerMonthMinor == 0
}
func coherentWorkflow(page *rod.Page, api func(string, string, any) string, state map[string]any, statePath, root, botPath, home string, evidence map[string]any, saveEvidence func(), pass func(string), authenticate authenticateBrowser) {
	cp := smokeCheckpoint{Contract: coherentContract, BotID: state["bot_id"].(string), Candidate: state["binary_sha256"].(string), Home: home, Sent: map[string]bool{}, Before: map[string]map[string]bool{}, IDs: map[string]string{}, Consents: map[string]string{}, Receipts: map[string]smokeReceipt{}}
	checkpoint := filepath.Join(root, "coherent-checkpoint.json")
	if raw, err := os.ReadFile(checkpoint); err == nil {
		cp = smokeCheckpoint{}
		must(json.Unmarshal(raw, &cp))
		must(validateCheckpoint(cp))
		if cp.BotID != state["bot_id"] || cp.Candidate != state["binary_sha256"] || cp.Home != home {
			panic("checkpoint identity changed")
		}
	}
	persist := func() {
		raw, err := json.MarshalIndent(cp, "", "  ")
		must(err)
		must(os.WriteFile(checkpoint, raw, 0600))
	}
	get := func(suffix string, v any) {
		path := botPath + suffix
		if strings.Contains(path, "?") {
			path += "&scope=personal"
		} else {
			path += "?scope=personal"
		}
		raw := api("GET", path, nil)
		must(json.Unmarshal([]byte(raw), v))
		must(os.WriteFile(filepath.Join(root, "coherent"+strings.ReplaceAll(strings.Split(suffix, "?")[0], "/", "-")+".json"), []byte(raw), 0600))
	}
	bot := func() store.Bot { var v store.Bot; get("", &v); return v }
	runs := func() []store.BotRun { var v struct{ Runs []store.BotRun }; get("/runs", &v); return v.Runs }
	proposals := func() []store.BotControlProposal {
		var v struct{ Proposals []store.BotControlProposal }
		get("/proposals", &v)
		return v.Proposals
	}
	approvals := func() []store.BotApproval {
		var v struct{ Approvals []store.BotApproval }
		get("/approvals", &v)
		return v.Approvals
	}
	run := func(id string) store.BotRun {
		for _, r := range runs() {
			if r.ID.String() == id {
				return r
			}
		}
		return store.BotRun{}
	}
	sourceDone := func(id string) {
		boundedWait("terminal source "+id, 150*time.Second, func() bool { r := run(id); return r.ID != uuid.Nil && r.Status.Terminal() })
		// Terminal report alone is not cleanup completion: await destruction of
		// the exact generating physical session before presenting consent.
		r := run(id)
		if r.ControlOnly() {
			return
		} // Control-only turns need no physical sandbox.
		if r.SessionID == uuid.Nil {
			panic("source physical session missing")
		}
		boundedWait("source cleanup "+id, 90*time.Second, func() bool {
			out, err := exec.Command("docker", "ps", "-aq", "--filter", "name=^/"+smokeContainerName(r.SessionID)+"$").CombinedOutput()
			must(err)
			return strings.TrimSpace(string(out)) == ""
		})
	}
	send := func(key, message string) {
		if cp.Sent[key] {
			return
		}
		cp.Sent[key] = true
		persist()
		page.Timeout(30 * time.Second).MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput(message)
		page.Timeout(30 * time.Second).MustElement(`[aria-label="Bot workspace"] [data-testid="send-button"]`).MustClick()
	}
	click := func(id, label, digest string) {
		if digest == "" {
			panic("empty digest")
		}
		if cp.Consents[id] != "" {
			panic("consent already attempted; no automatic replay: " + id)
		}
		cp.Consents[id] = digest
		persist()
		page.Timeout(30*time.Second).MustElementR(`[aria-label="Bot workspace"] [data-review-id="`+id+`"] button`, "^"+label+"$").MustClick()
	}
	propose := func(key string, op store.BotControlOperation, message string) store.BotControlProposal {
		if cp.Before[key] == nil {
			cp.Before[key] = map[string]bool{}
			for _, p := range proposals() {
				cp.Before[key][p.ID.String()] = true
			}
			persist()
		}
		send(key, "Emit a short sentence, then use propose_bot_control to propose ONLY this change for my explicit review. Never execute in this control conversation. "+message)
		var found store.BotControlProposal
		boundedWait("proposal "+key, 150*time.Second, func() bool {
			for _, p := range proposals() {
				id := p.ID.String()
				if p.Payload.Operation == op && (cp.IDs[key] == id || (!cp.Before[key][id] && cp.IDs[key] == "")) {
					found = p
					cp.IDs[key] = id
					persist()
					return true
				}
			}
			return false
		})
		sourceDone(found.SourceRunID.String())
		return found
	}
	confirm := func(p store.BotControlProposal) {
		if p.Status == store.BotControlApplied {
			if cp.Consents[p.ID.String()] != p.Digest {
				panic("applied proposal lacks this checkpoint's exact consent")
			}
			if p.ReconciliationPending {
				panic("applied proposal reconciliation pending")
			}
			return
		}
		if p.Status != store.BotControlProposed {
			panic("proposal not current")
		}
		if bot().Revision != p.ExpectedRevision {
			panic("stale proposal revision")
		}
		if p.Payload.Operation == store.BotControlSchedulePause {
			fresh := func() (store.BotControlProposal, store.Bot) {
				for _, v := range proposals() {
					if v.ID == p.ID {
						return v, bot()
					}
				}
				panic("saved proposal disappeared")
			}
			idle := func() {
				boundedWait("idle before fresh schedule confirmation", 90*time.Second, func() bool {
					for _, r := range runs() {
						if !r.Status.Terminal() {
							return false
						}
					}
					return true
				})
				time.Sleep(time.Second) // Cleanup remains authoritatively fenced by server.
			}
			must(confirmWithBusyRetry(&cp, p, persist, fresh, idle, func() (int, string) {
				return captureConfirmClick(page, botPath+"/proposals/"+p.ID.String()+"/decision", p.ID.String(), p.Digest)
			}))
		} else {
			click(p.ID.String(), "Confirm exact change", p.Digest)
		}
		boundedWait("applied "+p.ID.String(), 45*time.Second, func() bool {
			for _, v := range proposals() {
				if v.ID == p.ID {
					return v.Status == store.BotControlApplied && !v.ReconciliationPending
				}
			}
			return false
		})
	}
	stage := func(name string, work func() string) {
		evidence["stage"] = name
		saveEvidence()
		if _, ok := cp.Receipts[name]; !ok {
			detail := work()
			if detail == "" {
				panic("empty stage assertion")
			}
			cp.Receipts[name] = smokeReceipt{cp.BotID, cp.Candidate, home, detail}
			persist()
		}
		r := cp.Receipts[name]
		if r.BotID != cp.BotID || r.Candidate != cp.Candidate || r.Home != home || r.Detail == "" {
			panic("invalid stage receipt")
		}
		evidence["receipts"] = cp.Receipts
		pass(name)
	}
	assertScheduler := func(triggerID string, enabled bool) {
		boundedWait("public scheduler next_run", 30*time.Second, func() bool {
			raw := api("GET", "/api/scheduler/jobs?scope=personal", nil)
			var response struct {
				Jobs []smokeSchedulerJob `json:"jobs"`
			}
			must(json.Unmarshal([]byte(raw), &response))
			if response.Jobs == nil {
				panic("scheduler response missing jobs array")
			}
			must(os.WriteFile(filepath.Join(root, fmt.Sprintf("scheduler-enabled-%t.json", enabled)), []byte(raw), 0600))
			return schedulerState(response.Jobs, cp.BotID, triggerID, enabled)
		})
	}
	history := func() string {
		raw := api("GET", botPath+"/sessions/"+bot().HomeSessionID.String()+"/stream?scope=personal", nil)
		if !strings.Contains(raw, `"persisted":true`) {
			panic("incomplete history snapshot")
		}
		return raw
	}
	artifactCount := func() int {
		return page.MustEval(`() => document.querySelectorAll('[aria-label="Bot workspace"] [aria-label="Download file"]').length`).Int()
	}
	assertDefaultHome := func() {
		if page.MustInfo().URL != home || !page.MustEval(`() => { const w=document.querySelector('[aria-label="Bot workspace"]'); return !!w && !document.querySelector('[aria-label="Bot details"]') && !w.querySelector('[role="tab"],[role="tablist"]') && !Array.from(w.querySelectorAll('button')).some(e=>['Overview','Runs','Memory','Settings','Configuration'].includes(e.textContent.trim())) }`).Bool() {
			panic("default Home exposed details/tabs or changed URL")
		}
	}
	captureModes := func(prefix string) {
		for _, theme := range []string{"light", "dark"} {
			for _, mode := range []struct {
				name          string
				width, height int
			}{{"desktop", 1440, 1000}, {"mobile", 390, 844}} {
				page.MustSetViewport(mode.width, mode.height, 1, false)
				page.MustEval(`theme => localStorage.setItem('astonish-theme', theme)`, theme)
				page.Timeout(30 * time.Second).MustReload()
				page.Timeout(30 * time.Second).MustElement(`[aria-label="Bot workspace"]`).MustVisible()
				if page.MustEval(`() => document.documentElement.classList.contains('dark')`).Bool() != (theme == "dark") {
					panic("actual theme mismatch")
				}
				assertDefaultHome()
				if prefix == "artifact-reloaded" {
					page.Timeout(30 * time.Second).MustElement(`[aria-label="Download file"]`).MustVisible()
					requireArtifactCards(artifactCount())
				}
				page.MustScreenshot(filepath.Join(root, prefix+"-"+mode.name+"-"+theme+".png"))
			}
		}
		page.MustSetViewport(1440, 1000, 1, false)
	}
	artifact := func() string {
		v := bot()
		if v.HomeSessionID == uuid.Nil {
			panic("missing home")
		}
		s := api("GET", "/api/studio/artifacts?session="+v.HomeSessionID.String()+"&path="+url.QueryEscape(retainedArtifactPath), nil)
		if s != retainedArtifactBytes {
			panic("artifact bytes mismatch")
		}
		return s
	}
	taskRun := func(p store.BotControlProposal) store.BotRun {
		var found store.BotRun
		boundedWait("task admission", 90*time.Second, func() bool {
			for _, r := range runs() {
				if r.IdempotencyKey == "control-proposal:"+p.ID.String() {
					found = r
					return true
				}
			}
			return false
		})
		return found
	}
	lifecycle := func(label, target string) {
		if string(bot().Status) == target {
			return
		}
		key := "lifecycle-" + target
		if cp.Consents[key] != "" {
			panic("lifecycle attempted, refusing replay")
		}
		cp.Consents[key] = target
		persist()
		page.Timeout(30*time.Second).MustElementR("button", "^Details$").MustClick()
		page.Timeout(30*time.Second).MustElementR("button", "^"+label+"$").MustClick()
		page.Timeout(30 * time.Second).MustElement(`[aria-label="Lifecycle reason"]`).MustInput("Owned smoke: pause and retain Home, disable schedules, no catch-up or replay")
		page.Timeout(30*time.Second).MustElementR("button", "^Confirm pause$").MustClick()
		boundedWait("paused", 45*time.Second, func() bool { return string(bot().Status) == target })
		page.Timeout(30*time.Second).MustElementR("button", "^Close$").MustClick()
	}
	stage("minimal-conversation", func() string {
		v := bot()
		if v.Status != store.BotDraft || len(runs()) != 0 {
			panic("not fresh minimal draft")
		}
		if page.MustEval(`()=>!!document.querySelector('[aria-label="Bot details"]')`).Bool() {
			panic("details opened automatically")
		}
		captureModes("minimal-conversation")
		return "draft; zero runs; four actual-theme viewport screenshots; no default tabs; conversation Home " + v.HomeSessionID.String()
	})
	stage("details-no-reconnect", func() string {
		page.MustEval(`()=>{window.__composer=document.querySelector('[data-testid="chat-input"]');window.__streams=0;window.__oldFetch=window.fetch;window.fetch=function(...args){if(String(args[0]).includes('/stream'))window.__streams++;return window.__oldFetch.apply(this,args)}}`)
		page.Timeout(30*time.Second).MustElementR("button", "^Details$").MustClick()
		page.Timeout(30*time.Second).MustElementR("button", "^Close$").MustClick()
		time.Sleep(time.Second)
		if !page.MustEval(`()=>window.__composer===document.querySelector('[data-testid="chat-input"]') && window.__streams===0`).Bool() {
			panic("Details reconnect/remount")
		}
		return "same composer; zero new stream fetches"
	})
	stage("responsibility-confirmed", func() string {
		p := propose("responsibility", store.BotControlResponsibility, "Responsibility exactly: Maintain a local smoke-test note only. No network, external publishing or contacting people. Do not activate.")
		if p.Payload.Responsibility == nil || !strings.Contains(*p.Payload.Responsibility, "local smoke-test note") {
			panic("wrong responsibility")
		}
		confirm(p)
		return p.ID.String() + ":" + p.Digest
	})
	// Configuration is a prerequisite, never disguised as one of the nine outcomes.
	if cp.IDs["configured"] == "" {
		p := propose("configuration", store.BotControlConfigurationOperation, "Configure supervised autonomy; allowed_tools exactly write_file and load_file; allowed_paths exactly /root/chat-first-proof.txt (the retained Docker Home is /root, not /workspace). No allowed_hosts, contact or external publishing; zero spend; budget max_runtime_seconds 900; delivery notify on_change. Preserve Draft and disabled schedules.")
		if !exactGrant(p) {
			panic("configuration scope mismatch")
		}
		confirm(p)
		cp.IDs["configured"] = p.ID.String()
		persist()
	}
	stage("exact-approval-continued", func() string {
		var runIDs, approvalIDs []string
		for _, action := range []struct{ key, tool, instructions string }{
			{"artifact-write", "write_file", "Propose task_once.instructions: use write_file exactly once on /root/chat-first-proof.txt with exactly CHAT_FIRST_REAL_TOOL_PROOF_20261106 followed by a newline. This task contains only that one action. No load_file or other tools."},
			{"artifact-load", "load_file", privateArtifactInstructions},
		} {
			if action.key == "artifact-load" && cp.Sent[action.key] && cp.IDs[action.key] == "" {
				if !cp.Sent[artifactLoadClarification] {
					ps, rs := proposals(), runs()
					refusal, err := loadRefusal(cp, rs, ps)
					must(err)
					raw, err := json.MarshalIndent(map[string]any{"reason": "one read-only exact-path private attachment clarification; no external publication", "original": cp, "refusal": refusal, "proposals": ps, "runs": rs}, "", "  ")
					must(err)
					must(os.WriteFile(filepath.Join(root, "artifact-load-absence.json"), raw, 0600))
				}
				action.key = artifactLoadClarification
			}
			// A saved run is never proposed or consented again. Recheck its literal
			// terminal outcome and consumed exact approval before proceeding.
			if saved := cp.IDs[action.key+"-run"]; saved != "" {
				r := run(saved)
				id, err := validateSingleAction(r, approvals(), cp.Consents, action.tool)
				must(err)
				runIDs = append(runIDs, saved)
				approvalIDs = append(approvalIDs, id)
				sourceDone(saved)
				continue
			}
			p := propose(action.key, store.BotControlTaskOnceOperation, action.instructions)
			if p.Payload.TaskOnce == nil || !strings.Contains(p.Payload.TaskOnce.Instructions, retainedArtifactPath) || !strings.Contains(p.Payload.TaskOnce.Instructions, action.tool) {
				panic("wrong single-action task")
			}
			confirm(p)
			r := taskRun(p)
			cp.IDs[action.key+"-run"] = r.ID.String()
			persist()
			boundedWait("single exact "+action.tool+" continuation", 240*time.Second, func() bool {
				r = run(r.ID.String())
				if r.Status.Terminal() {
					return true
				}
				currentApprovals := approvals()
				matching := 0
				for _, a := range currentApprovals {
					if a.RunID == r.ID {
						matching++
					}
				}
				if matching > 1 {
					panic("second approval on single-action task; refusing repeat action")
				}
				for _, a := range currentApprovals {
					if a.RunID != r.ID {
						continue
					}
					must(validateExactAction(a, action.tool))
					if a.Status == store.ApprovalPending && r.Status == store.RunWaitingApproval && cp.Consents[a.ID.String()] == "" {
						click(a.ID.String(), "Approve & continue", a.ActionDigest)
					}
				}
				return false
			})
			id, err := validateSingleAction(r, approvals(), cp.Consents, action.tool)
			must(err) // Partial always blocks, including an already-consumed write.
			runIDs = append(runIDs, r.ID.String())
			approvalIDs = append(approvalIDs, id)
			sourceDone(r.ID.String())
		}
		if runIDs[0] == runIDs[1] || approvalIDs[0] == approvalIDs[1] {
			panic("single actions must have distinct runs and approvals")
		}
		cp.IDs["artifact-run"] = runIDs[1]
		persist()
		return fmt.Sprintf("runs=%v unique approvals=%v; each succeeded tools=1 consumed=1; total tools=2", runIDs, approvalIDs)
	})
	stage("tool-artifact-reload", func() string {
		artifact()
		r := run(cp.IDs["artifact-run"])
		if r.SessionID == uuid.Nil {
			panic("artifact physical session missing")
		}
		boundedWait("artifact compute destroyed", 90*time.Second, func() bool {
			out, err := exec.Command("docker", "ps", "-aq", "--filter", "name=^/"+smokeContainerName(r.SessionID)+"$").CombinedOutput()
			must(err)
			return strings.TrimSpace(string(out)) == ""
		})
		page.Timeout(30 * time.Second).MustElement(`[aria-label="Download file"]`).MustVisible()
		beforeHistory, beforeCards := history(), artifactCount()
		requireArtifactCards(beforeCards)
		must(os.WriteFile(filepath.Join(root, "artifact-history-before.sse"), []byte(beforeHistory), 0600))
		page.Timeout(30 * time.Second).MustReload()
		page.Timeout(30 * time.Second).MustElement(`[aria-label="Download file"]`).MustVisible()
		afterHistory := history()
		must(os.WriteFile(filepath.Join(root, "artifact-history-after.sse"), []byte(afterHistory), 0600))
		if beforeHistory != afterHistory || artifactCount() != beforeCards {
			panic("reload changed history or duplicated artifact")
		}
		captureModes("artifact-reloaded")
		page.Timeout(30 * time.Second).MustElement(`[aria-label="Download file"]`).MustVisible()
		if artifactCount() != expectedArtifactCards || history() != beforeHistory {
			panic("mode reload duplicated artifact/history")
		}
		if page.MustInfo().URL != home {
			panic("Home changed")
		}
		s := artifact()
		return fmt.Sprintf("run=%s bytes=%d url=%s", cp.IDs["artifact-run"], len(s), home)
	})
	stage("memory-next-answer", func() string {
		p := propose("memory-original", store.BotControlMemoryRemember, "Remember this fact for review: The smoke marker label is WORKSPACE_CHECK.")
		confirm(p)
		p = propose("memory-correction", store.BotControlMemoryCorrect, "Correct the existing smoke marker fact to: The smoke marker label is RETAINED_HOME_CHECK, not WORKSPACE_CHECK. Use its exact current memory_id and expected revision/fingerprint; propose only.")
		if p.Payload.Memory == nil || !strings.Contains(p.Payload.Memory.Content, "RETAINED_HOME_CHECK") || p.Payload.Memory.MemoryID == "" {
			panic("memory correction missing exact target: current production control prompt did not supply/resolve saved memory identity; blocked without spoon-feeding memory_id or bypassing review")
		}
		confirm(p)
		var saved any
		get("/memory", &saved)
		raw, _ := json.Marshal(saved)
		if !strings.Contains(string(raw), "RETAINED_HOME_CHECK") {
			panic("corrected memory not saved")
		}
		if cp.Before["answer"] == nil {
			cp.Before["answer"] = map[string]bool{}
			for _, r := range runs() {
				cp.Before["answer"][r.ID.String()] = true
			}
			persist()
		}
		send("answer", "What is my current smoke marker label? Use the saved fact in one substantive answer. Do not propose changes or call tools.")
		var answer store.BotRun
		boundedWait("new memory answer", 150*time.Second, func() bool {
			for _, r := range runs() {
				if !cp.Before["answer"][r.ID.String()] && r.Status.Terminal() {
					answer = r
					return true
				}
			}
			return false
		})
		if answer.Status != store.RunSucceeded || !strings.Contains(answer.Report.Summary, "RETAINED_HOME_CHECK") {
			panic("next answer did not use corrected fact")
		}
		return "correction=" + p.ID.String() + " answer=" + answer.ID.String()
	})
	stage("scheduled-tick-return", func() string {
		p := propose("schedule", store.BotControlScheduleCreate, "Create one reviewed cron schedule: * * * * * UTC, enabled true, catch_up_once false; instructions: Reply exactly RETAINED_HOME_TICK, no tools or actions. Keep Bot Draft until a separate activation review.")
		if p.Payload.Schedule == nil {
			panic("missing schedule")
		}
		t := p.Payload.Schedule.Trigger
		if t.Kind != store.TriggerCron || t.Cron != "* * * * *" || t.Timezone != "UTC" || !t.Enabled || t.CatchUpOnce || !strings.Contains(t.Instructions, "RETAINED_HOME_TICK") {
			panic("schedule scope changed")
		}
		confirm(p)
		current := bot()
		var appliedVersion store.BotVersion
		get(fmt.Sprintf("/versions/%d", current.Version), &appliedVersion)
		var resolveErr error
		t, resolveErr = resolveAppliedSchedule(p, current, appliedVersion)
		must(resolveErr)
		cp.IDs["trigger"] = t.ID.String()
		persist()
		a := propose("activation", store.BotControlActivate, "Propose activation of this Draft Bot and its sole reviewed every-minute schedule. No immediate task and never replay prior tools.")
		confirm(a)
		assertScheduler(t.ID.String(), true)
		var tick store.BotRun
		boundedWait("real scheduler tick", 180*time.Second, func() bool {
			for _, r := range runs() {
				if r.Trigger == store.TriggerCron && r.TriggerID == t.ID && r.Status.Terminal() {
					tick = r
					return true
				}
			}
			return false
		})
		if tick.Status != store.RunSucceeded || tick.Usage.ToolCalls != 0 || !strings.Contains(tick.Report.Summary, "RETAINED_HOME_TICK") {
			panic("tick did not return real provider report")
		}
		page.Timeout(30 * time.Second).MustReload()
		page.Timeout(30*time.Second).MustElementR(`[aria-label="Bot workspace"]`, "RETAINED_HOME_TICK").MustVisible()
		pause := propose("schedule-disable", store.BotControlSchedulePause, "Pause the existing exact schedule "+t.ID.String()+" using its current revision; do not create another schedule or run anything.")
		if pause.Payload.Schedule == nil || pause.Payload.Schedule.Trigger.ID != t.ID {
			panic("wrong schedule pause target")
		}
		confirm(pause)
		assertScheduler(t.ID.String(), false)
		var version store.BotVersion
		get(fmt.Sprintf("/versions/%d", bot().Version), &version)
		if len(version.Triggers) != 1 || version.Triggers[0].ID != t.ID || version.Triggers[0].Enabled || version.Triggers[0].CatchUpOnce {
			panic("schedule did not disable exactly")
		}
		return "trigger=" + t.ID.String() + " tick=" + tick.ID.String()
	})
	stage("paused-approval-blocked", func() string {
		if cp.IDs["pause-task"] != "" {
			rs, as := runs(), approvals()
			original, err := validatePauseOriginal(cp, rs, as)
			must(err)
			if !cp.Sent[pauseReadonlyTask] {
				raw, err := json.MarshalIndent(map[string]any{"reason": "separate reviewed read-only replacement; original task never replayed", "original": cp, "refused_run": original, "runs": rs, "approvals": as, "proposals": proposals()}, "", "  ")
				must(err)
				must(os.WriteFile(filepath.Join(root, "pause-original-refusal.json"), raw, 0600))
			}
		}
		beforeBytes := artifact()
		beforeCards := artifactCount()
		requireArtifactCards(beforeCards)
		p := propose(pauseReadonlyTask, store.BotControlTaskOnceOperation, pauseTaskInstructions(cp))
		confirm(p)
		r := taskRun(p)
		var pending store.BotApproval
		boundedWait("actual waiting_approval before pause", 150*time.Second, func() bool {
			r = run(r.ID.String())
			if r.Status.Terminal() {
				panic("read-only pause task ended without pending approval; no replay")
			}
			if r.Usage.ToolCalls != 0 {
				panic("read-only pause task executed before pause")
			}
			if r.Status != store.RunWaitingApproval {
				return false
			}
			for _, a := range approvals() {
				if a.RunID == r.ID && a.Status == store.ApprovalPending {
					must(validatePauseAction(a))
					pending = a
					return true
				}
			}
			return false
		})
		lifecycle("Pause", "paused")
		count := len(runs())
		tools := r.Usage.ToolCalls
		click(pending.ID.String(), "Approve & continue", pending.ActionDigest)
		page.Timeout(30*time.Second).MustElementR(`[data-review-id="`+pending.ID.String()+`"]`, "continuation blocked by pause").MustVisible()
		time.Sleep(3 * time.Second)
		if bot().Status != store.BotPaused || len(runs()) != count || run(r.ID.String()).Usage.ToolCalls != tools {
			panic("paused approval executed")
		}
		for _, a := range approvals() {
			if a.ID == pending.ID && a.Status != store.ApprovalApproved {
				panic("paused decision not retained approved")
			}
		}
		if artifact() != beforeBytes || artifactCount() != beforeCards || run(r.ID.String()).Status != store.RunWaitingApproval {
			panic("paused read-only action changed bytes/cards or continued")
		}
		cp.IDs["paused-run"] = r.ID.String()
		persist()
		return fmt.Sprintf("approval=%s digest=%s runs=%d tools=%d unchanged bytes", pending.ID, pending.ActionDigest, count, tools)
	})
	stage("paused-restart-bytes", func() string {
		if bot().Status != store.BotPaused {
			panic("restart requires paused Bot")
		}
		before := len(runs())
		beforeHistory := history()
		if cp.Restarted {
			// Resume verification only: preserve the actual pre-restart snapshot.
			saved, err := os.ReadFile(filepath.Join(root, "restart-history-before.sse"))
			must(err)
			beforeHistory = string(saved)
			var approval, digest string
			var tools int
			n, err := fmt.Sscanf(cp.Receipts["paused-approval-blocked"].Detail, "approval=%s digest=%s runs=%d tools=%d unchanged bytes", &approval, &digest, &before, &tools)
			if err != nil || n != 4 || tools != 0 {
				panic("missing pre-restart run count")
			}
		} else {
			must(os.WriteFile(filepath.Join(root, "restart-history-before.sse"), []byte(beforeHistory), 0600))
		}
		bytes := artifact()
		must(restartAndAuthenticate(&cp, func() error {
			cmd := exec.Command("python3", "tests/e2eboot/local_bot_smoke_launcher.py", "--state", statePath, "--restart-owned")
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("owned restart: %s: %w", out, err)
			}
			return nil
		}, persist, authenticate))
		page.Timeout(45 * time.Second).MustNavigate(home).MustReload()
		page.Timeout(30 * time.Second).MustElement(`[aria-label="Bot workspace"]`).MustVisible()
		afterHistory := history()
		must(os.WriteFile(filepath.Join(root, "restart-history-after.sse"), []byte(afterHistory), 0600))
		page.Timeout(30 * time.Second).MustElement(`[aria-label="Download file"]`).MustVisible()
		if afterHistory != beforeHistory || artifactCount() != expectedArtifactCards {
			panic("restart changed actual history or artifact count")
		}
		assertScheduler(cp.IDs["trigger"], false)
		assertDefaultHome()
		if bot().Status != store.BotPaused || page.MustInfo().URL != home || artifact() != bytes || len(runs()) != before {
			panic("paused restart changed state/history/bytes")
		}
		time.Sleep(65 * time.Second)
		if len(runs()) != before || run(cp.IDs["paused-run"]).Usage.ToolCalls != 0 {
			panic("paused restart admitted catch-up work")
		}
		pausedApprovals := 0
		for _, a := range approvals() {
			if a.RunID.String() == cp.IDs["paused-run"] {
				pausedApprovals++
				must(validatePauseAction(a))
				if a.Status != store.ApprovalApproved {
					panic("paused restart consumed approval")
				}
			}
		}
		if pausedApprovals != 1 {
			panic("paused restart approval count changed")
		}
		return fmt.Sprintf("owned restart; paused; runs=%d unchanged; bytes=%d; Home=%s", before, len(bytes), home)
	})
	for _, name := range coherentRequired {
		if cp.Receipts[name].Detail == "" {
			panic("missing evidence " + name)
		}
	}
	evidence["status"] = "verified"
	evidence["candidate"] = cp.Candidate
	evidence["bot_id"] = cp.BotID
	saveEvidence()
}
