package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/google/uuid"
)

// employeeAdmittedRun reads the actual persisted Home event. Neither the newest
// run nor a matching operation is evidence that the intended message was sent.
func employeeAdmittedRun(raw, message string, bot store.Bot, before map[uuid.UUID]bool, runs []store.BotRun) (store.BotRun, error) {
	events := map[string]string{}
	complete := false
	for _, frame := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n\n") {
		var data []string
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if len(data) == 0 {
			continue
		}
		var event struct {
			ID        string `json:"id"`
			Author    string `json:"author"`
			Persisted bool   `json:"persisted"`
			Content   *struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		}
		if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &event); err != nil {
			return store.BotRun{}, err
		}
		complete = complete || event.Persisted
		if event.Author == "user" && event.Content != nil && event.Content.Role == "user" {
			var text strings.Builder
			for _, part := range event.Content.Parts {
				text.WriteString(part.Text)
			}
			if event.ID != "" {
				events[event.ID] = text.String()
			}
		}
	}
	if !complete {
		return store.BotRun{}, fmt.Errorf("employee admission history incomplete")
	}
	var found store.BotRun
	for _, run := range runs {
		if before[run.ID] || run.BotID != bot.ID || run.Trigger != store.TriggerManual || run.Presentation != store.BotRunPresentationHomeInteractive || run.ID == uuid.Nil || run.UserEventID == "" || run.HistoryCursor <= 0 {
			continue
		}
		if events[run.UserEventID] != message {
			continue
		}
		if found.ID != uuid.Nil {
			return store.BotRun{}, fmt.Errorf("duplicate employee message admission")
		}
		found = run
	}
	return found, nil
}

type employeeComposerState struct {
	URL      string `json:"url"`
	Input    string `json:"input"`
	Present  bool   `json:"present"`
	Disabled bool   `json:"disabled"`
	Alert    string `json:"alert"`
}

func employeeComposer(page *rod.Page) employeeComposerState {
	raw := page.MustEval(`() => {
  const w = document.querySelector('[aria-label="Bot workspace"]');
  const input = w?.querySelector('[data-testid="chat-input"]');
  const button = w?.querySelector('[data-testid="send-button"]');
  return JSON.stringify({url:location.href, input:input?.value || '', present:!!input && !!button,
   disabled:!input || !button || input.disabled || button.disabled,
   alert:[...(w?.querySelectorAll('[role="alert"]') || [])].filter(e=>e.getClientRects().length).map(e=>e.innerText).join('\n')});
 }`).String()
	var state employeeComposerState
	must(json.Unmarshal([]byte(raw), &state))
	return state
}

func validateEmployeeComposer(state employeeComposerState, home, message string) error {
	if state.URL != home || !state.Present || state.Disabled || state.Input != message || state.Alert != "" {
		return fmt.Errorf("employee send not ready: %+v", state)
	}
	return nil
}

// UI-only send; read-only APIs observe admission. Never inject a message, bypass
// a disabled button, retry a rejected POST, or infer admission from click return.
func employeeSend(page func() *rod.Page, home, key, message string, snapshot func() store.BotSnapshot, runs func() []store.BotRun, capture func(string, string) string, attempted func(), saveEvidence func(string, any)) store.BotRun {
	// Read-only observation identifies the owned Home; the production conversation
	// reconciles committed operation updates before sending. Do not refresh via
	// Details here: that would hide stale composer admission authority.
	b := snapshot().Bot
	input := page().MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`)
	if employeeComposer(page()).Input != "" {
		panic("employee composer contains unsent text")
	}
	input.MustInput(message)
	boundedWait("employee Send enabled", time.Minute, func() bool {
		state := employeeComposer(page())
		if state.Alert != "" {
			panic("employee send UI error: " + state.Alert)
		}
		return state.Present && !state.Disabled
	})
	state := employeeComposer(page())
	saveEvidence(key+"-send", state)
	must(validateEmployeeComposer(state, home, message))
	before := map[uuid.UUID]bool{}
	for _, run := range runs() {
		before[run.ID] = true
	}
	attempted()
	page().MustElement(`[aria-label="Bot workspace"] [data-testid="send-button"]`).MustClick()
	var target store.BotRun
	boundedWait(key+" message admission", time.Minute, func() bool {
		state := employeeComposer(page())
		saveEvidence(key+"-admission-ui", state)
		if state.URL != home || !state.Present {
			panic("employee admission left owned Home")
		}
		if state.Alert != "" {
			panic("employee message admission rejected: " + state.Alert)
		}
		rr := runs()
		raw := capture(key+"-admission-history", "/api/studio/bots/"+b.ID.String()+"/sessions/"+b.HomeSessionID.String()+"/stream?scope=personal")
		var err error
		target, err = employeeAdmittedRun(raw, message, b, before, rr)
		must(err)
		return target.ID != uuid.Nil && state.Input == ""
	})
	saveEvidence(key+"-admission", target)
	return target
}
