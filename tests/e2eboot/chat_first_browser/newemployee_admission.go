package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
)

// employeeSubmittedMessage mirrors StudioChat's text.trim() before onSend.
// Normalize only the expected draft, never the persisted evidence or its interior.
// ECMAScript includes BOM but excludes NEL, unlike Go's strings.TrimSpace.
func employeeSubmittedMessage(draft string) string {
	return strings.Trim(draft, "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
}

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
	message = employeeSubmittedMessage(message)
	var found store.BotRun
	if message == "" {
		return found, nil
	}
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

type employeeConnection string

const (
	employeeConnectionLoading      employeeConnection = "loading"
	employeeConnectionLive         employeeConnection = "live"
	employeeConnectionReconnecting employeeConnection = "reconnecting"
	employeeConnectionError        employeeConnection = "error"
	// Exact local transport message, not an arbitrary server/action failure.
	employeeUnavailableAlert = "I can’t confirm the latest outcome right now. Please refresh before trying again; your work may still be running."
)

type employeeComposerState struct {
	URL           string             `json:"url"`
	Input         string             `json:"input"`
	Present       bool               `json:"present"`
	Disabled      bool               `json:"disabled"`
	InputDisabled bool               `json:"input_disabled"`
	Alert         string             `json:"alert"`
	Connection    employeeConnection `json:"connection"`
}

type employeePreSendPhase string

const (
	employeeBeforeFill employeePreSendPhase = "before-fill"
	employeeDraftHeld  employeePreSendPhase = "draft-held"
)

type employeePreSendObservation struct {
	Phase    employeePreSendPhase  `json:"phase"`
	Elapsed  time.Duration         `json:"elapsed_ns"`
	Reopened bool                  `json:"reopened"`
	State    employeeComposerState `json:"state"`
}
type employeePreSendHooks struct {
	read    func() employeeComposerState
	fill    func(string)
	reopen  func()
	now     func() time.Time
	wait    func()
	observe func(employeePreSendObservation)
}

// The only retryable operation is an empty, owned Home reload. Once filled,
// transport recovery is observation-only. No branch can clear or retype a draft.
func employeePrepareSend(home, message string, timeout time.Duration, h employeePreSendHooks) (employeeComposerState, error) {
	start := h.now()
	phase := employeeBeforeFill
	reopened := false
	for {
		state := h.read()
		elapsed := h.now().Sub(start)
		h.observe(employeePreSendObservation{Phase: phase, Elapsed: elapsed, Reopened: reopened, State: state})
		if state.URL != home {
			return state, fmt.Errorf("employee pre-send left owned Home: %s", state.URL)
		}
		if phase == employeeBeforeFill && state.Input != "" {
			return state, fmt.Errorf("employee composer contains unsent text; preserved")
		}
		if phase == employeeDraftHeld && (!state.Present || state.Input != message) {
			return state, fmt.Errorf("employee pre-send draft changed or disappeared; refusing to retype")
		}
		if state.Alert != "" && state.Alert != employeeUnavailableAlert {
			return state, fmt.Errorf("employee pre-send non-transport alert: %s", state.Alert)
		}
		if elapsed >= timeout {
			return state, fmt.Errorf("employee pre-send timeout in %s; draft preserved, Send not attempted", phase)
		}
		if state.Present && !state.InputDisabled && state.Connection == employeeConnectionLive && state.Alert == "" {
			if phase == employeeBeforeFill {
				// Empty Send is normally disabled; transcript live means replay_complete
				// has arrived (web/src/api/botEvents.ts), not merely an open socket.
				phase = employeeDraftHeld
				h.fill(message)
				continue
			}
			if !state.Disabled {
				return state, nil
			}
		}
		if phase == employeeBeforeFill && state.Present && !reopened && elapsed >= 3*time.Second && (state.Connection == employeeConnectionLoading || state.Connection == employeeConnectionReconnecting || state.Connection == employeeConnectionError || state.Alert == employeeUnavailableAlert) {
			reopened = true
			h.reopen()
			continue
		}
		h.wait()
	}
}

// All observations/recovery precede attempted. Even an uncertain click error
// exits immediately: the caller must inspect admission, never replay the action.
func employeeSubmitOnce(home, message string, timeout time.Duration, h employeePreSendHooks, before, attempted func(), click func() error) error {
	before()
	state, err := employeePrepareSend(home, message, timeout, h)
	if err != nil {
		return err
	}
	if err := validateEmployeeComposer(state, home, message); err != nil {
		return err
	}
	attempted()
	return click()
}

func employeeComposer(page *rod.Page) employeeComposerState {
	raw := page.Timeout(5 * time.Second).MustEval(`() => {
  const w = document.querySelector('[aria-label="Bot workspace"]');
  const input = w?.querySelector('[data-testid="chat-input"]');
  const button = w?.querySelector('[data-testid="send-button"]');
  const transcript = [...(w?.querySelectorAll('[role="status"]') || [])].find(e => e.textContent?.startsWith('Bot transcript:'))?.textContent || '';
  return JSON.stringify({url:location.href, input:input?.value || '', present:!!input && !!button,
   input_disabled:!input || input.disabled, connection:transcript.match(/^Bot transcript:\s+(\w+)/)?.[1] || '',
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
	before := map[uuid.UUID]bool{}
	trace := []employeePreSendObservation{}
	// Persist every observed transition before any possible timeout/panic. The
	// existing capture API also retains server history without injecting a send.
	defer func() {
		if failure := recover(); failure != nil {
			saveEvidence(key+"-send-failure", fmt.Sprint(failure))
			saveEvidence(key+"-presend-trace", trace)
			func() {
				defer func() {
					if diagnosticFailure := recover(); diagnosticFailure != nil {
						saveEvidence(key+"-diagnostic-error", fmt.Sprint(diagnosticFailure))
					}
				}()
				saveEvidence(key+"-failure-ui", employeeComposer(page()))
				capture(key+"-failure-history", "/api/studio/bots/"+b.ID.String()+"/sessions/"+b.HomeSessionID.String()+"/stream?scope=personal")
			}()
			panic(failure)
		}
	}()
	hooks := employeePreSendHooks{
		read: func() employeeComposerState { return employeeComposer(page()) },
		fill: func(text string) {
			current := employeeComposer(page())
			if current.URL != home || !current.Present || current.Input != "" || current.InputDisabled || current.Connection != employeeConnectionLive || current.Alert != "" {
				panic("employee pre-fill readiness changed; refusing to overwrite or type")
			}
			page().Timeout(5 * time.Second).MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput(text)
		},
		reopen: func() {
			state := employeeComposer(page())
			if state.URL != home || !state.Present || state.Input != "" {
				panic("employee unsafe Home reload refused; draft preserved")
			}
			page().Timeout(10 * time.Second).MustReload()
		},
		now: time.Now, wait: func() { time.Sleep(250 * time.Millisecond) },
		observe: func(o employeePreSendObservation) {
			trace = append(trace, o)
			saveEvidence(key+"-presend-trace", trace)
			saveEvidence(key+"-send", o.State)
		},
	}
	must(employeeSubmitOnce(home, message, time.Minute, hooks, func() {
		for _, run := range runs() {
			before[run.ID] = true
		}
	}, attempted, func() error {
		button, err := page().Timeout(5 * time.Second).Element(`[aria-label="Bot workspace"] [data-testid="send-button"]`)
		if err != nil {
			return err
		}
		return button.Click(proto.InputMouseButtonLeft, 1)
	}))
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
