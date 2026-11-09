package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-rod/rod"
)

// reviewedPermissionPrerequisite never mutates configuration through an API and
// never re-confirms the failed applied task. Every change has its own UI review.
func reviewedPermissionPrerequisite(page *rod.Page, api func(string, string, any) string, state map[string]any, statePath, root, botPath string) {
	save := func() { raw, _ := json.MarshalIndent(state, "", "  "); must(os.WriteFile(statePath, raw, 0600)) }
	capture := func(name, path string) string {
		raw := api("GET", path, nil)
		must(os.WriteFile(filepath.Join(root, name), []byte(raw), 0600))
		return raw
	}
	type proposal struct {
		ID               string `json:"id"`
		Digest           string `json:"digest"`
		SourceRunID      string `json:"source_run_id"`
		ExpectedRevision int64  `json:"expected_revision"`
		Status           string `json:"status"`
		Payload          struct {
			Operation     string `json:"operation"`
			Configuration struct {
				Autonomy struct {
					Level        string   `json:"level"`
					AllowedTools []string `json:"allowed_tools"`
					AllowedPaths []string `json:"allowed_paths"`
					CanPublish   bool     `json:"can_publish"`
					CanContact   bool     `json:"can_contact_people"`
				} `json:"autonomy"`
			} `json:"configuration"`
		} `json:"payload"`
	}
	list := func() []proposal {
		var v struct {
			Proposals []proposal `json:"proposals"`
		}
		must(json.Unmarshal([]byte(capture("permission-proposals.json", botPath+"/proposals?scope=personal")), &v))
		return v.Proposals
	}
	// Establish the immutable failed predecessor before any new message.
	var runs struct {
		Runs []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Key    string `json:"idempotency_key"`
		} `json:"runs"`
	}
	must(json.Unmarshal([]byte(capture("permission-before-runs.json", botPath+"/runs?scope=personal")), &runs))
	old, _ := state["task_confirmation_attempted"].(string)
	failed := false
	for _, r := range runs.Runs {
		if r.Key == "control-proposal:"+old && r.Status == "failed" {
			failed = true
			state["permission_failed_predecessor"] = r.ID
		}
	}
	if !failed {
		panic("permission prerequisite requires a recorded failed predecessor; refusing replay")
	}
	save()
	send := func(key, message string) {
		if state[key] == true {
			return
		}
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput(message)
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="send-button"]`).MustClick()
		state[key] = true
		save()
	}
	before := map[string]bool{}
	for _, p := range list() {
		before[p.ID] = true
	}
	send("permission_request_sent", "The previous task failed because write_file was outside allowed_tools; no approval was created. Do not replay or reconfirm that applied task. Please propose a separate configuration change with propose_bot_control for my explicit review: supervised autonomy, allowed_tools exactly write_file and load_file, allowed_paths exactly /workspace/chat-first-proof.txt, no hosts, no contacting people, no publishing externally, zero spend. Preserve budget max_runtime_seconds 900 and delivery notify on_change. Keep the Bot draft and schedules disabled. Explain the missing permission prerequisite and emit a short sentence before proposing. This is only a reviewed permission proposal, not execution or activation.")
	waitProposal := func(operation, key string) proposal {
		deadline := time.Now().Add(100 * time.Second)
		for time.Now().Before(deadline) {
			for _, p := range list() {
				if p.Payload.Operation == operation && (state[key] == p.ID || !before[p.ID]) {
					state[key] = p.ID
					save()
					return p
				}
			}
			time.Sleep(time.Second)
		}
		panic("missing real model " + operation + " proposal")
	}
	p := waitProposal("configuration", "permission_proposal_id")
	a := p.Payload.Configuration.Autonomy
	if a.Level != "supervised" || a.CanPublish || a.CanContact || len(a.AllowedTools) != 2 || len(a.AllowedPaths) != 1 || a.AllowedPaths[0] != "/workspace/chat-first-proof.txt" {
		panic("permission review scope differs; not confirming")
	}
	tools := map[string]bool{}
	for _, t := range a.AllowedTools {
		tools[t] = true
	}
	if !tools["write_file"] || !tools["load_file"] {
		panic("unexpected tool grant")
	}
	confirm := func(p proposal, key string) {
		if p.Status == "applied" {
			return
		}
		// A proposal is reviewable only after its generating conversation terminates.
		deadline := time.Now().Add(80 * time.Second)
		terminal := false
		for time.Now().Before(deadline) {
			must(json.Unmarshal([]byte(capture("permission-review-runs.json", botPath+"/runs?scope=personal")), &runs))
			for _, r := range runs.Runs {
				if r.ID == p.SourceRunID && (r.Status == "partial" || r.Status == "succeeded" || r.Status == "failed") {
					terminal = true
				}
			}
			if terminal {
				break
			}
			time.Sleep(time.Second)
		}
		if !terminal {
			panic("source conversation still running; do not confirm")
		}
		if p.Status != "proposed" {
			panic("review not proposed")
		}
		if state[key] == p.ID {
			if prior, ok := state[key+"_digest"].(string); !ok || prior != p.Digest {
				panic("review digest not proven unchanged; request a fresh proposal")
			}
			if os.Getenv("CHAT_FIRST_REVIEW_PERMISSION_RETRY") != p.ID || p.Payload.Operation != "configuration" {
				panic("review already attempted; inspect checkpoint")
			}
			var current struct {
				Revision int64 `json:"revision"`
			}
			must(json.Unmarshal([]byte(capture("permission-retry-bot.json", botPath+"?scope=personal")), &current))
			if current.Revision != p.ExpectedRevision {
				panic("configuration revision changed; request a fresh proposal")
			}
			state["permission_deliberate_retry_digest"] = p.Digest
		}
		page.MustReload()
		state[key] = p.ID
		state[key+"_digest"] = p.Digest
		save()
		page.MustElementR(`[data-review-id="`+p.ID+`"] button`, "^Confirm exact change$").MustClick()
		page.MustWait(`(id) => { const e=document.querySelector('[data-review-id="'+id+'"]'); return !!e && ([...e.querySelectorAll('p')].some(p=>p.textContent==='applied') || !!e.querySelector('[role="alert"]')) }`, p.ID)
		if failure := page.MustEval(`id=>document.querySelector('[data-review-id="'+id+'"] [role="alert"]')?.textContent||''`, p.ID).Str(); failure != "" {
			panic("review confirmation: " + failure)
		}
		page.MustElementR(`[data-review-id="`+p.ID+`"] p`, "^applied$").MustVisible()
	}
	confirm(p, "permission_confirmation_attempted")
	capture("permission-after-bot.json", botPath+"?scope=personal")
	fmt.Println("REVIEWED configuration permission applied", p.ID)
	for _, p := range list() {
		before[p.ID] = true
	}
	send("permission_new_task_request_sent", "The separate configuration review is now applied. Please create a NEW task_once proposal for my review, never retry the old applied proposal: use write_file to create /workspace/chat-first-proof.txt containing exactly CHAT_FIRST_REAL_TOOL_PROOF_20261106 followed by a newline, then load_file that path for a private conversation download. Keep schedules disabled and do not activate. No execution in this control conversation; emit a short sentence then save the new task proposal.")
	task := waitProposal("task_once", "permission_new_task_proposal_id")
	if task.ID == old {
		panic("refusing applied predecessor replay")
	}
	confirm(task, "permission_new_task_confirmation_attempted")
	fmt.Println("REVIEWED new task applied", task.ID)
	deadline := time.Now().Add(100 * time.Second)
	for time.Now().Before(deadline) {
		approvals := capture("permission-after-approvals.json", botPath+"/approvals?scope=personal")
		var pending struct {
			Approvals []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"approvals"`
		}
		must(json.Unmarshal([]byte(approvals), &pending))
		for _, a := range pending.Approvals {
			if a.Status == "pending" {
				state["permission_exact_approval_id"] = a.ID
				save()
				panic("permission prerequisite reviewed; exact-action approval pending " + a.ID + "; not yet executed")
			}
		}
		must(json.Unmarshal([]byte(capture("permission-after-runs.json", botPath+"/runs?scope=personal")), &runs))
		for _, r := range runs.Runs {
			if r.Key == "control-proposal:"+task.ID && (r.Status == "awaiting_approval" || r.Status == "needs_approval" || r.Status == "failed" || r.Status == "succeeded" || r.Status == "partial") {
				state["permission_new_task_run_status"] = r.Status
				save()
				panic("permission prerequisite reviewed; new task outcome " + r.Status + "; remaining acceptance requires separate verification")
			}
		}
		time.Sleep(time.Second)
	}
	panic("permission prerequisite reviewed; new task pending; inspect saved runs and approvals, do not replay")
}
