package main

import (
	"encoding/json"
	"fmt"
	"github.com/go-rod/rod"
	"os"
	"path/filepath"
	"time"
)

func remainingLifecycle(label string) (string, string, string) {
	switch label {
	case "Pause":
		return "Confirm pause", "paused", "Explicit smoke pause; retain transcript and artifact; do not resume execution"
	case "Review activation":
		return "Confirm activate", "active", "Explicit isolated read-only approval acceptance; schedule must remain disabled, no recurring work or prior writes"
	default:
		panic("unsupported remaining lifecycle action")
	}
}

// remainingStage runs only explicitly requested remaining UI actions; it never
// replays prerequisite reviews. Every exit saves live API and DOM evidence.
func remainingStage(page *rod.Page, api func(string, string, any) string, state map[string]any, statePath, root, botPath string) {
	key := os.Getenv("CHAT_FIRST_REMAINING_STAGE")
	save := func() { b, _ := json.MarshalIndent(state, "", "  "); must(os.WriteFile(statePath, b, 0600)) }
	capture := func() {
		for _, suffix := range []string{"proposals", "runs", "approvals"} {
			must(os.WriteFile(filepath.Join(root, "remaining-"+key+"-"+suffix+".json"), []byte(api("GET", botPath+"/"+suffix+"?scope=personal", nil)), 0600))
		}
		must(os.WriteFile(filepath.Join(root, "remaining-"+key+"-bot.json"), []byte(api("GET", botPath+"?scope=personal", nil)), 0600))
		must(os.WriteFile(filepath.Join(root, "remaining-"+key+"-ui.txt"), []byte(page.MustElement("body").MustText()), 0600))
		if session := os.Getenv("CHAT_FIRST_INSPECT_SESSION"); session != "" {
			history := api("GET", "/api/studio/sessions/"+session, nil)
			var parsed any
			must(json.Unmarshal([]byte(history), &parsed))
			pretty, _ := json.MarshalIndent(parsed, "", "  ")
			must(os.WriteFile(filepath.Join(root, "remaining-"+key+"-history.json"), pretty, 0600))
		}
	}
	capture()
	defer capture()
	if message := os.Getenv("CHAT_FIRST_REMAINING_MESSAGE"); message != "" {
		if state["remaining_"+key+"_sent"] != nil {
			panic("remaining message already attempted; inspect evidence")
		}
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput(message)
		state["remaining_"+key+"_sent"] = message
		save()
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="send-button"]`).MustClick()
	}
	if id := os.Getenv("CHAT_FIRST_REMAINING_CONFIRM"); id != "" {
		if state["remaining_"+key+"_confirmed"] != nil {
			panic("remaining confirmation already attempted")
		}
		state["remaining_"+key+"_confirmed"] = id
		save()
		page.MustElementR(`[aria-label="Bot workspace"] [data-review-id="`+id+`"] button`, "^Confirm exact change$").MustClick()
		page.MustWait(`id=>{const e=document.querySelector('[aria-label="Bot workspace"] [data-review-id="'+id+'"]');return !!e && (e.innerText.includes('applied') || !!e.querySelector('[role="alert"]'))}`, id)
	}
	if label := os.Getenv("CHAT_FIRST_REMAINING_LIFECYCLE"); label != "" {
		page.MustElementR(`[aria-label="Bot workspace"] button`, "^Details$").MustClick()
		page.MustElementR(`button`, "^"+label+"$").MustClick()
		confirm, status, reason := remainingLifecycle(label)
		page.MustElement(`[aria-label="Lifecycle reason"]`).MustInput(reason)
		page.MustElementR(`button`, "^"+confirm+"$").MustClick()
		page.MustWait(`status=>document.querySelector('[aria-label="Bot workspace"] header')?.innerText.includes(status)`, status)
		page.MustElementR(`button`, "^Close$").MustClick()
	}
	if id := os.Getenv("CHAT_FIRST_REMAINING_APPROVE"); id != "" {
		if state["remaining_"+key+"_approved"] != nil {
			panic("remaining approval already attempted")
		}
		state["remaining_"+key+"_approved"] = id
		save()
		page.MustElementR(`[aria-label="Bot workspace"] [data-review-id="`+id+`"] button`, "^Approve & continue$").MustClick()
		page.MustWait(`id=>{const e=document.querySelector('[data-review-id="'+id+'"]');return !!e && (!!e.querySelector('[role="alert"]') || e.innerText.includes('approved'))}`, id)
	}
	for end := time.Now().Add(65 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Second) {
		capture()
		fmt.Println("OBSERVE", key, time.Now().UTC().Format(time.RFC3339))
		if os.Getenv("CHAT_FIRST_REMAINING_MESSAGE") == "" {
			break
		}
		var runs struct{ Runs []struct{ Status string } }
		must(json.Unmarshal([]byte(api("GET", botPath+"/runs?scope=personal", nil)), &runs))
		busy := false
		for _, r := range runs.Runs {
			if r.Status == "running" || r.Status == "queued" {
				busy = true
			}
		}
		if !busy {
			break
		}
	}
	fmt.Println("REMAINING evidence saved", key)
	panic("remaining stage evidence captured; acceptance requires inspection")
}
