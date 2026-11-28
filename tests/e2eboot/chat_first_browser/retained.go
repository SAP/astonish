package main

import (
	"encoding/json"
	"fmt"
	"github.com/go-rod/rod"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const retainedArtifactPath = "/root/chat-first-proof.txt"
const retainedArtifactBytes = "CHAT_FIRST_REAL_TOOL_PROOF_20261106\n"

// Separate checkpoints deliberately preserve every historical proposal and attempt.
func retainedArtifactReview(page *rod.Page, api func(string, string, any) string, state map[string]any, statePath, root, botPath string) {
	save := func() { b, _ := json.MarshalIndent(state, "", "  "); must(os.WriteFile(statePath, b, 0600)) }
	capture := func(name, path string) string {
		s := api("GET", path, nil)
		must(os.WriteFile(filepath.Join(root, "retained-"+name+".json"), []byte(s), 0600))
		return s
	}
	state["retained_expected_artifact_path"] = retainedArtifactPath
	save()
	type approval struct {
		ID, Status   string
		ActionDigest string `json:"action_digest"`
		RunID        string `json:"run_id"`
		Action       struct {
			Tool      string
			Arguments struct{ Path, Content string }
		}
	}
	approvals := func() []approval {
		var v struct{ Approvals []approval }
		must(json.Unmarshal([]byte(capture("approvals", botPath+"/approvals?scope=personal")), &v))
		return v.Approvals
	}
	click := func(id, label, key, digest string) {
		if state[key] != nil {
			if key != "retained_approval_attempted" || os.Getenv("CHAT_FIRST_RETAINED_APPROVAL_RETRY") != id || state[key+"_digest"] != digest {
				panic("retained review already attempted: " + key + "; inspect before retry, never replay")
			}
			state["retained_approval_deliberate_retry_digest"] = digest
		}
		page.MustReload()
		state[key] = id
		state[key+"_digest"] = digest
		save()
		page.MustElementR(`[data-review-id="`+id+`"] button`, "^"+label+"$").MustClick()
		page.MustWait(`id=>{const e=document.querySelector('[data-review-id="'+id+'"]');return !!(e && (!e.querySelector('button') || e.querySelector('[role="status"]') || e.querySelector('[role="alert"]')))}`, id)
		if e := page.MustEval(`id=>document.querySelector('[data-review-id="'+id+'"] [role="alert"]')?.textContent||''`, id).Str(); e != "" {
			panic(e)
		}
	}
	oldID, _ := state["permission_exact_approval_id"].(string)
	found := false
	for _, a := range approvals() {
		if a.ID == oldID {
			found = true
			if a.Action.Tool != "write_file" || a.Action.Arguments.Path != "/workspace/chat-first-proof.txt" || a.Action.Arguments.Content != retainedArtifactBytes {
				panic("old denial scope mismatch")
			}
			if a.Status == "pending" {
				click(a.ID, "Deny", "retained_old_denial_attempted", a.ActionDigest)
			} else if a.Status != "denied" {
				panic("old write was not safely denied")
			}
		}
	}
	if !found {
		panic("old approval missing")
	}
	capture("denied-approvals", botPath+"/approvals?scope=personal")
	type proposal struct {
		ID, Status, Digest string
		SourceRunID        string `json:"source_run_id"`
		Payload            struct {
			Operation     string
			Configuration struct {
				Autonomy struct {
					Level        string
					AllowedTools []string `json:"allowed_tools"`
					AllowedPaths []string `json:"allowed_paths"`
					AllowedHosts []string `json:"allowed_hosts"`
					CanPublish   bool     `json:"can_publish"`
					CanContact   bool     `json:"can_contact_people"`
				}
			}
			TaskOnce struct{ Instructions string } `json:"task_once"`
		}
	}
	proposals := func() []proposal {
		var v struct{ Proposals []proposal }
		must(json.Unmarshal([]byte(capture("proposals", botPath+"/proposals?scope=personal")), &v))
		return v.Proposals
	}
	terminal := func(id string) bool {
		var v struct{ Runs []struct{ ID, Status string } }
		must(json.Unmarshal([]byte(capture("runs", botPath+"/runs?scope=personal")), &v))
		for _, r := range v.Runs {
			if r.ID == id {
				return r.Status == "succeeded" || r.Status == "failed" || r.Status == "partial"
			}
		}
		return false
	}
	propose := func(key, op, message string) proposal {
		existing := map[string]bool{}
		for _, p := range proposals() {
			existing[p.ID] = true
		}
		if state[key+"_sent"] != true {
			state[key+"_before"] = existing
			save()
			page.MustElement(`[data-testid="chat-input"]`).MustInput(message)
			page.MustElement(`[data-testid="send-button"]`).MustClick()
			state[key+"_sent"] = true
			save()
		}
		before := map[string]bool{}
		raw, _ := json.Marshal(state[key+"_before"])
		must(json.Unmarshal(raw, &before))
		for end := time.Now().Add(100 * time.Second); time.Now().Before(end); time.Sleep(time.Second) {
			for _, p := range proposals() {
				if p.Payload.Operation == op && (state[key+"_id"] == p.ID || !before[p.ID]) {
					state[key+"_id"] = p.ID
					save()
					if !terminal(p.SourceRunID) {
						continue
					}
					return p
				}
			}
		}
		panic("no terminal source for retained " + op)
	}
	p := propose("retained_config_clarified", "configuration", "I genuinely choose /root/chat-first-proof.txt as the target. Actual reason: this Bot has retained Docker home storage astonish-bot-home-b967adae5036d5c3c2263aa5f6f1d6e777e9a1becf4a1f2989f626da4152d228, not an ordinary session workspace. We will verify the live HOME mount before approving any write. The prior approval 27969337-2bae-4498-bce5-61966b381801 was explicitly DENIED in its native review and the public API confirms denied; do not revive it. This is not routing around approval: the NEW task must obtain its own exact-action approval. Please propose cleanly the separate configuration I requested: allowed_paths exactly /root/chat-first-proof.txt, allowed_tools exactly write_file and load_file, supervised, no hosts/contact/external publishing, zero spend, max_runtime_seconds 900, notify on_change. Keep draft and schedules disabled. Propose only, no execution.")
	a := p.Payload.Configuration.Autonomy
	if a.Level != "supervised" || a.CanPublish || a.CanContact || len(a.AllowedHosts) != 0 || len(a.AllowedPaths) != 1 || a.AllowedPaths[0] != retainedArtifactPath || len(a.AllowedTools) != 2 {
		panic("retained config scope mismatch")
	}
	tools := strings.Join(a.AllowedTools, ",")
	if !strings.Contains(tools, "write_file") || !strings.Contains(tools, "load_file") {
		panic("retained tools mismatch")
	}
	if p.Status != "applied" {
		click(p.ID, "Confirm exact change", "retained_config_attempted", p.Digest)
	}
	capture("configured-bot", botPath+"?scope=personal")
	task := propose("retained_task", "task_once", "The corrected retained-home configuration review is applied. Create a NEW task_once review, never replay earlier tasks: use write_file exactly ONCE to write /root/chat-first-proof.txt with exactly CHAT_FIRST_REAL_TOOL_PROOF_20261106 followed by a newline. This single write_file provides the existing file artifact mechanism; do not call load_file or any second tool. Report only afterward. Keep draft and schedules disabled. Emit a short sentence and propose_bot_control, do not execute in this control conversation.")
	if !strings.Contains(task.Payload.TaskOnce.Instructions, retainedArtifactPath) {
		panic("retained task path mismatch")
	}
	if task.Status != "applied" {
		click(task.ID, "Confirm exact change", "retained_task_attempted", task.Digest)
	}
	var current approval
	for end := time.Now().Add(110 * time.Second); time.Now().Before(end); time.Sleep(time.Second) {
		for _, a := range approvals() {
			if a.ID != oldID && a.Action.Tool == "write_file" && a.Action.Arguments.Path == retainedArtifactPath && a.Action.Arguments.Content == retainedArtifactBytes {
				current = a
			}
		}
		if current.ID != "" {
			break
		}
	}
	if current.ID == "" {
		panic("retained exact approval missing")
	}
	state["retained_exact_approval_id"] = current.ID
	state["retained_run_id"] = current.RunID
	save()
	if os.Getenv("CHAT_FIRST_RETAINED_HOME_VERIFIED") != "/root" {
		panic("retained exact approval ready; inspect owned live HOME/mount before approval")
	}
	if current.Status == "pending" {
		click(current.ID, "Approve & continue", "retained_approval_attempted", current.ActionDigest)
	}
	for end := time.Now().Add(100 * time.Second); time.Now().Before(end) && !terminal(current.RunID); time.Sleep(time.Second) {
	}
	capture("after-approval-runs", botPath+"/runs?scope=personal")
	capture("after-approval-approvals", botPath+"/approvals?scope=personal")
	page.MustReload()
	must(os.WriteFile(filepath.Join(root, "retained-reloaded-ui.txt"), []byte(page.MustElement("body").MustText()), 0600))
	var bot struct {
		HomeSessionID string `json:"home_session_id"`
	}
	must(json.Unmarshal([]byte(capture("after-bot", botPath+"?scope=personal")), &bot))
	bytes := api("GET", "/api/studio/artifacts?session="+bot.HomeSessionID+"&path=%2Froot%2Fchat-first-proof.txt", nil)
	if bytes != retainedArtifactBytes {
		panic("retained artifact bytes mismatch")
	}
	must(os.WriteFile(filepath.Join(root, "retained-downloaded-proof.txt"), []byte(bytes), 0600))
	state["retained_download_bytes_verified"] = true
	save()
	fmt.Println("VERIFIED retained artifact authenticated HTTP bytes", len(bytes), "run", current.RunID)
	if os.Getenv("CHAT_FIRST_REMAINING_DISCUSSION") == "1" && state["retained_remaining_discussion_sent"] != true {
		page.MustElement(`[data-testid="chat-input"]`).MustInput("For the remaining smoke stages, discuss and propose only: propose a disabled every-minute local smoke schedule for explicit later review, never activate it or run prior tasks. Separately record this correction as a memory proposal for review: my smoke marker label is RETAINED_HOME_CHECK, not WORKSPACE_CHECK. Do not execute tools outside the control proposal mechanism; do not repeat any write or task.")
		page.MustElement(`[data-testid="send-button"]`).MustClick()
		state["retained_remaining_discussion_sent"] = true
		save()
		for end := time.Now().Add(35 * time.Second); time.Now().Before(end); time.Sleep(time.Second) {
			capture("remaining-proposals", botPath+"/proposals?scope=personal")
		}
		capture("remaining-runs", botPath+"/runs?scope=personal")
	}
	panic("retained bytes verified; full schedule/memory/paused-restart acceptance remains unverified")
}
