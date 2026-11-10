// Real native-browser smoke against the isolated launcher-owned daemon only.
// No fake model, route interception, seeded transcript, or internal HTTP handlers.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	statePath := os.Getenv("CHAT_FIRST_STATE")
	raw, err := os.ReadFile(statePath)
	must(err)
	var state map[string]any
	must(json.Unmarshal(raw, &state))
	root := state["root"].(string)
	base := state["url"].(string)
	if !strings.HasPrefix(base, "http://127.0.0.1:") || !strings.HasPrefix(filepath.Base(root), "astonish-chat-first-") {
		panic("not owned loopback smoke")
	}
	raw, err = os.ReadFile(filepath.Join(root, "credentials.json"))
	must(err)
	var creds map[string]string
	must(json.Unmarshal(raw, &creds))
	evidence := map[string]any{"passed": []string{}, "url": base, "started": time.Now().UTC().Format(time.RFC3339), "model": "real configured daemon provider", "status": "running"}
	save := func() {
		b, _ := json.MarshalIndent(evidence, "", "  ")
		must(os.WriteFile(filepath.Join(root, "browser-evidence.json"), b, 0600))
	}
	pass := func(name string) {
		evidence["passed"] = append(evidence["passed"].([]string), name)
		save()
		fmt.Println("PASS", name)
		f, e := os.OpenFile("/tmp/chat-first-live-smoke.md", os.O_APPEND|os.O_WRONLY, 0600)
		if e == nil {
			fmt.Fprintf(f, "\n- PASS `%s` at %s.\n", name, time.Now().UTC().Format(time.RFC3339))
			f.Close()
		}
	}
	save()
	l := launcher.New().Bin(os.Getenv("ASTONISH_BOT_BROWSER_PATH")).Headless(true).UserDataDir(filepath.Join(root, "chrome-profile")).Set("no-first-run")
	control, err := l.Launch()
	must(err)
	defer func() {
		// Cleanup waits for process exit. Kill first even if CDP has disconnected.
		l.Kill()
		l.Cleanup()
	}()
	b := rod.New().ControlURL(control).MustConnect()
	defer func() { _ = b.Timeout(5 * time.Second).Close() }()
	basePage := b.MustPage()
	page := independentPageTimeout(basePage, 40*time.Second)
	go basePage.EachEvent(func(e *proto.RuntimeExceptionThrown) {
		message := e.ExceptionDetails.Text
		if e.ExceptionDetails.Exception != nil {
			message += ": " + e.ExceptionDetails.Exception.Description
		}
		if len(message) > 6000 {
			message = message[:6000]
		}
		f, err := os.OpenFile(filepath.Join(root, "browser-runtime-errors.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err == nil {
			fmt.Fprintln(f, time.Now().UTC().Format(time.RFC3339), message)
			f.Close()
		}
	})()
	stage := "browser-navigation"
	setStage := func(name string, timeout time.Duration) {
		stage = name
		page = independentPageTimeout(basePage, timeout)
		evidence["stage"] = name
		save()
		fmt.Println("STAGE", name, time.Now().UTC().Format(time.RFC3339))
		f, e := os.OpenFile("/tmp/chat-first-live-smoke.md", os.O_APPEND|os.O_WRONLY, 0600)
		if e == nil {
			fmt.Fprintf(f, "\n- Live recovery stage `%s` started %s (not a pass).\n", name, time.Now().UTC().Format(time.RFC3339))
			f.Close()
		}
	}
	defer func() {
		if failure := recover(); failure != nil {
			if current, ok := evidence["stage"].(string); ok {
				stage = current
			}
			evidence["status"] = "blocked"
			evidence["blocker"] = stage + ": " + fmt.Sprint(failure)
			if text, e := independentPageTimeout(basePage, 5*time.Second).Eval(`() => JSON.stringify({url:location.href,ready:document.readyState,text:document.body.innerText.slice(0,12000),html:document.documentElement.outerHTML.slice(0,4000)})`); e == nil {
				must(os.WriteFile(filepath.Join(root, "browser-blocked.txt"), []byte(text.Value.Str()), 0600))
			}
			if image, e := independentPageTimeout(basePage, 5*time.Second).Screenshot(true, nil); e == nil {
				_ = os.WriteFile(filepath.Join(root, "browser-blocked.png"), image, 0600)
			}
			save()
			fmt.Fprintln(os.Stderr, "BLOCKED", stage, failure)
			f, e := os.OpenFile("/tmp/chat-first-live-smoke.md", os.O_APPEND|os.O_WRONLY, 0600)
			if e == nil {
				fmt.Fprintf(f, "\n- BLOCKED `%s`: %v. Snapshot: `%s/browser-blocked.txt`.\n", stage, failure, root)
				f.Close()
			}
			_ = b.Timeout(5 * time.Second).Close()
			l.Kill()
			l.Cleanup()
			os.Exit(1)
		}
	}()
	api := func(method, path string, body any) string {
		if method != "GET" && !(method == "POST" && path == "/api/auth/login") {
			panic("product mutations require native UI")
		}
		operation := independentPageTimeout(basePage, 20*time.Second)
		defer operation.CancelTimeout()
		result := operation.MustEval(`async (method,path,body) => { const r=await fetch(path,{method,credentials:'include',headers:{'Content-Type':'application/json','X-Requested-With':'XMLHttpRequest'},body:body===null?undefined:JSON.stringify(body)}); const text=await r.text(); if(!r.ok) throw new Error(method+' '+path+' HTTP '+r.status+': '+text); return text }`, method, path, body)
		return result.Str()
	}
	setStage("browser-navigation", 30*time.Second)
	page.MustNavigate(base)
	setStage("login-and-render", 40*time.Second)
	authenticate := authenticateBrowser(func() error {
		return redactedAuthentication(func() {
			api("POST", "/api/auth/login", map[string]string{"email": creds["email"], "password": creds["password"]})
		})
	})
	must(authenticate())
	page.MustNavigate(base + "/#/chat").MustReload()
	page.MustElementR("button", "^New Bot$").MustVisible()
	if os.Getenv("BOT_EMPLOYEE_ACCEPTANCE") == "1" || os.Getenv("BOT_QUALITY_ACCEPTANCE") == "1" {
		setStage("employee-fresh", 30*time.Minute)
		newEmployeeWorkflow(basePage, api, state, statePath, root, base, evidence, save, pass)
		return
	}
	if os.Getenv("CHAT_MODEL_ACCEPTANCE") == "1" {
		setStage("title-first", 30*time.Minute)
		newChatModelWorkflow(basePage, api, state, statePath, root, base, evidence, save, pass)
		return
	}
	setStage("minimal-conversation-open", 40*time.Second)
	if id, ok := state["bot_id"].(string); ok && id != "" {
		page.MustNavigate(base + "/#/chat/bots/personal/" + id)
	} else {
		if state["bot_creation_attempted"] == true {
			panic("ambiguous Bot creation: inspect owned checkpoint; never create duplicate")
		}
		state["bot_creation_attempted"] = true
		raw, _ = json.MarshalIndent(state, "", "  ")
		must(os.WriteFile(statePath, raw, 0600))
		page.MustElementR("button", "^New Bot$").MustClick()
		page.MustElement(`[aria-label="Bot name"]`).MustInput("Clean real-daemon Bot")
		page.MustElementR("button", "^Start conversation$").MustClick()
	}
	page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	home := page.MustInfo().URL
	parts := strings.Split(home, "/")
	id := parts[len(parts)-1]
	state["bot_id"] = id
	raw, _ = json.MarshalIndent(state, "", "  ")
	must(os.WriteFile(statePath, raw, 0600))
	botPath := "/api/studio/bots/" + id
	if os.Getenv("CHAT_FIRST_REMAINING_STAGE") == "" && os.Getenv("CHAT_FIRST_RETAINED_REVIEW") != "1" && os.Getenv("CHAT_FIRST_REVIEW_PERMISSION") != "1" {
		setStage("coherent-fresh-workflow", 30*time.Minute)
		coherentWorkflow(page, api, state, statePath, root, botPath, home, evidence, save, pass, authenticate)
		return
	}
	if os.Getenv("CHAT_FIRST_REMAINING_STAGE") != "" {
		setStage("remaining-"+os.Getenv("CHAT_FIRST_REMAINING_STAGE"), 95*time.Second)
		remainingStage(page, api, state, statePath, root, botPath)
	}
	botJSON := api("GET", botPath+"?scope=personal", nil)
	var bot map[string]any
	must(json.Unmarshal([]byte(botJSON), &bot))
	if bot["status"] != "draft" {
		panic("minimal Bot not draft")
	}
	if page.MustEval(`() => !!document.querySelector('[aria-label="Bot details"]')`).Bool() {
		panic("dashboard opened at creation")
	}
	must(os.WriteFile(filepath.Join(root, "minimal-bot.json"), []byte(botJSON), 0600))
	page.MustScreenshot(filepath.Join(root, "minimal-conversation.png"))
	pass("minimal-conversation")
	setStage("details-no-reconnect", 20*time.Second)
	page.MustEval(`() => { window.__composer=document.querySelector('[data-testid="chat-input"]'); window.__streamCount=performance.getEntriesByType('resource').filter(e=>e.name.includes('/stream')).length }`)
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("button", "^Close$").MustClick()
	if !page.MustEval(`() => window.__composer===document.querySelector('[data-testid="chat-input"]') && window.__streamCount===performance.getEntriesByType('resource').filter(e=>e.name.includes('/stream')).length`).Bool() {
		panic("Details remounted/reconnected conversation")
	}
	pass("details-no-reconnect")
	setStage("responsibility-proposal", 100*time.Second)
	proposals := api("GET", botPath+"/proposals?scope=personal", nil)
	if !strings.Contains(proposals, `"responsibility"`) && state["responsibility_request_sent"] != true {
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput("Please propose, using propose_bot_control, this responsibility for my explicit review: Maintain a local smoke-test note only. Do not execute actions or enable schedules. Emit a short plain-text sentence before calling any tool, then call propose_bot_control with operation responsibility. I will confirm the review card myself.")
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="send-button"]`).MustClick()
		state["responsibility_request_sent"] = true
		raw, _ = json.MarshalIndent(state, "", "  ")
		must(os.WriteFile(statePath, raw, 0600))
	}
	page.MustElementR(`[aria-label="Bot workspace"] h3`, "^Review responsibility$").MustVisible()
	must(os.WriteFile(filepath.Join(root, "responsibility-proposals.json"), []byte(api("GET", botPath+"/proposals?scope=personal", nil)), 0600))
	page.MustScreenshot(filepath.Join(root, "responsibility-review.png"))
	setStage("responsibility-confirm", 25*time.Second)
	if !strings.Contains(proposals, `"status":"applied"`) {
		page.MustElementR(`[aria-label="Bot workspace"] button`, "^Confirm exact change$").MustClick()
	}
	page.MustElementR(`[data-review-id] p`, "^applied$").MustVisible()
	if page.MustInfo().URL != home {
		panic("confirmation left conversation")
	}
	pass("responsibility-confirmed")
	setStage("responsibility-reload", 25*time.Second)
	page.MustReload()
	page.MustElementR(`[data-review-id] p`, "^applied$").MustVisible()
	page.MustScreenshot(filepath.Join(root, "responsibility-reloaded.png"))
	if os.Getenv("CHAT_FIRST_RETAINED_REVIEW") == "1" {
		setStage("retained-artifact-review", 420*time.Second)
		retainedArtifactReview(page, api, state, statePath, root, botPath)
	}
	if os.Getenv("CHAT_FIRST_REVIEW_PERMISSION") == "1" {
		setStage("reviewed-permission-prerequisite", 330*time.Second)
		reviewedPermissionPrerequisite(page, api, state, statePath, root, botPath)
		panic("permission prerequisite returned without outcome evidence")
	}
	setStage("tool-artifact-request", 100*time.Second)
	if state["artifact_request_sent"] != true {
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput("Please now perform this explicitly authorized local-only action: use write_file to create /tmp/chat-first-proof.txt with exactly CHAT_FIRST_REAL_TOOL_PROOF_20261106 followed by a newline, then use load_file to publish that file for download in this conversation. First emit one plain-text sentence before any tool call. Do not enable schedules or perform external actions.")
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="send-button"]`).MustClick()
		state["artifact_request_sent"] = true
		raw, _ = json.MarshalIndent(state, "", "  ")
		must(os.WriteFile(statePath, raw, 0600))
	}
	setStage("tool-task-proposal", 85*time.Second)
	if state["artifact_task_request_sent"] != true {
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput("Understood. Please use propose_bot_control to propose operation task_once with task_once.instructions: Create /workspace/chat-first-proof.txt containing exactly CHAT_FIRST_REAL_TOOL_PROOF_20261106 followed by a newline using write_file, then publish it with load_file for download. This is a proposed local smoke-test note task only; do not run it in this control conversation. I will confirm the saved task review. Emit a short plain-text sentence before the tool call.")
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="send-button"]`).MustClick()
		state["artifact_task_request_sent"] = true
		raw, _ = json.MarshalIndent(state, "", "  ")
		must(os.WriteFile(statePath, raw, 0600))
	}
	page.MustElementR(`[aria-label="Bot workspace"] h3`, "^Review task once$").MustVisible()
	page.MustScreenshot(filepath.Join(root, "task-review.png"))
	setStage("tool-task-confirm", 30*time.Second)
	tasks := api("GET", botPath+"/proposals?scope=personal", nil)
	var taskList struct {
		Proposals []struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Payload struct {
				Operation string `json:"operation"`
			} `json:"payload"`
		} `json:"proposals"`
	}
	must(json.Unmarshal([]byte(tasks), &taskList))
	for _, task := range taskList.Proposals {
		if task.Payload.Operation == "task_once" && task.Status == "proposed" {
			if state["task_confirmation_attempted"] == task.ID {
				panic("previous task confirmation failed; inspect saved evidence before retrying")
			}
			state["task_confirmation_attempted"] = task.ID
			raw, _ = json.MarshalIndent(state, "", "  ")
			must(os.WriteFile(statePath, raw, 0600))
			page.MustElementR(`[data-review-id="`+task.ID+`"] button`, "^Confirm exact change$").MustClick()
		}
	}
	setStage("tool-task-execution", 100*time.Second)
	page.MustWait(`() => !!document.querySelector('[aria-label="Download file"]') || !!document.querySelector('[aria-label="Bot review"] [role="alert"]')`)
	if failure := page.MustEval(`() => document.querySelector('[aria-label="Bot review"] [role="alert"]')?.textContent || ''`).Str(); failure != "" {
		panic("task confirmation: " + failure)
	}
	page.MustElement(`[aria-label="Download file"]`).MustVisible()
	page.MustScreenshot(filepath.Join(root, "tool-artifact-card.png"))
	setStage("tool-artifact-reload", 25*time.Second)
	page.MustReload()
	page.MustElement(`[aria-label="Download file"]`).MustVisible()
	if page.MustInfo().URL != home {
		panic("artifact reload left conversation")
	}
	page.MustScreenshot(filepath.Join(root, "tool-artifact-reloaded.png"))
	evidence["artifact_card_reloaded"] = true
	evidence["status"] = "partial"
	evidence["blocker"] = "Artifact card reloaded but downloaded bytes and schedule/approval/memory/restart journey are not yet verified; partial UI evidence is never full acceptance"
	save()
	fmt.Fprintln(os.Stderr, evidence["blocker"])
	os.Exit(1)
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
