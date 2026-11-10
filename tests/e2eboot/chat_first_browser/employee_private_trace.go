package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type employeeWebResponse struct {
	URL        string          `json:"url"`
	FinalURL   string          `json:"final_url"`
	Status     json.RawMessage `json:"status"`
	StatusCode int             `json:"status_code"`
	Body       string          `json:"body"`
	Content    string          `json:"content"`
}

// Decode exactly one ADK result wrapper; never search arbitrary nested data.
func employeeDecodeWebResponse(raw json.RawMessage) (employeeWebResponse, error) {
	var response employeeWebResponse
	var envelope map[string]json.RawMessage
	if !successfulWebResult(raw) || json.Unmarshal(raw, &envelope) != nil || envelope == nil {
		return response, fmt.Errorf("invalid web envelope")
	}
	payload := raw
	if nested, ok := envelope["result"]; ok {
		payload = nested
	}
	if !successfulWebResult(payload) || json.Unmarshal(payload, &response) != nil {
		return response, fmt.Errorf("invalid web result")
	}
	if response.StatusCode == 0 {
		_ = json.Unmarshal(response.Status, &response.StatusCode)
	}
	if response.Body == "" {
		response.Body = response.Content
	} else if response.Content != "" && response.Content != response.Body {
		return response, fmt.Errorf("ambiguous web body")
	}
	return response, nil
}

// Home is a publication surface, never a substitute for private tool evidence.
func employeeFinalOnlyHistory(history string) error {
	messages, err := chatModelMessages(history)
	if err != nil {
		return err
	}
	for _, m := range messages {
		if m.Type == "tool_call" || m.Type == "tool_result" || (m.Type == "agent" && !strings.HasPrefix(m.InvocationID, "bot-report:")) {
			return fmt.Errorf("private draft/tool trace leaked into Home")
		}
	}
	return nil
}

// Bind real ADK call/response evidence to the persisted run/session/source before
// accepting its canonical Home publication. Runtime summaries/model assessments
// cannot supply a tool result. Source invocation prevents cross-turn ID pairing.
func employeeTitleEvidence(history, stream string, b store.Bot, run store.BotRun) error {
	if run.ID == uuid.Nil || run.BotID != b.ID || b.ID == uuid.Nil || b.HomeSessionID == uuid.Nil || run.SessionID == uuid.Nil || run.SessionID == b.HomeSessionID || run.UserEventID == "" || run.Status != store.RunSucceeded {
		return fmt.Errorf("title run identity/status mismatch")
	}
	if err := employeeFinalOnlyHistory(history); err != nil {
		return err
	}
	var home struct {
		ID       string             `json:"id"`
		Messages []chatModelMessage `json:"messages"`
	}
	if json.Unmarshal([]byte(history), &home) != nil || home.ID != b.HomeSessionID.String() {
		return fmt.Errorf("wrong Home")
	}
	reports := 0
	users := map[string]bool{}
	for _, m := range home.Messages {
		if m.Type == "user" {
			users[m.Content] = true
		}
		if m.InvocationID == "bot-report:"+run.ID.String() {
			if m.Type != "agent" || m.Content != bot.RenderReport(run) {
				return fmt.Errorf("noncanonical title report")
			}
			reports++
		}
	}
	if reports != 1 {
		return fmt.Errorf("title report count %d", reports)
	}
	type function struct {
		ID, Name string
		Args     struct {
			URL string `json:"url"`
		} `json:"args"`
		Response json.RawMessage `json:"response"`
	}
	type event struct {
		ID, InvocationID, Author string
		Persisted                bool `json:"persisted"`
		Content                  *struct {
			Parts []struct {
				Text   string    `json:"text"`
				Call   *function `json:"functionCall"`
				Result *function `json:"functionResponse"`
			} `json:"parts"`
		} `json:"content"`
		Actions struct {
			StateDelta struct {
				State *struct {
					BotID       uuid.UUID `json:"bot_id"`
					RunID       uuid.UUID `json:"run_id"`
					SessionID   uuid.UUID `json:"session_id"`
					HomeID      uuid.UUID `json:"home_session_id"`
					UserEventID string    `json:"user_event_id"`
				} `json:"bot_run_state"`
				Runtime *struct {
					RunID     uuid.UUID `json:"run_id"`
					SessionID uuid.UUID `json:"session_id"`
				} `json:"bot_runtime_event"`
			}
		}
	}
	var events []event
	persisted, bound := false, false
	sourceInvocation := ""
	for _, frame := range strings.Split(strings.ReplaceAll(stream, "\r\n", "\n"), "\n\n") {
		var data []string
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if len(data) == 0 {
			continue
		}
		var e event
		if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &e); err != nil {
			return err
		}
		persisted = persisted || e.Persisted
		if s := e.Actions.StateDelta.State; s != nil {
			if e.Author != "bot_runtime" || s.BotID != b.ID || s.RunID != run.ID || s.SessionID != run.SessionID || s.HomeID != b.HomeSessionID || s.UserEventID != run.UserEventID {
				return fmt.Errorf("private run state provenance mismatch")
			}
			bound = true
		}
		if r := e.Actions.StateDelta.Runtime; r != nil && (e.Author != "bot_runtime" || r.RunID != run.ID || r.SessionID != run.SessionID) {
			return fmt.Errorf("private runtime provenance mismatch")
		}
		if e.ID == run.UserEventID {
			if !bound || e.Author != "user" || e.InvocationID == "" || e.Content == nil || sourceInvocation != "" {
				return fmt.Errorf("invalid private source event")
			}
			text := ""
			for _, p := range e.Content.Parts {
				text += p.Text
			}
			if !users[text] {
				return fmt.Errorf("private source not in Home")
			}
			sourceInvocation = e.InvocationID
		}
		events = append(events, e)
	}
	if !persisted || !bound || sourceInvocation == "" {
		return fmt.Errorf("incomplete private run/source evidence")
	}
	calls := map[string]function{}
	matched := false
	for _, e := range events {
		if e.InvocationID != sourceInvocation || e.Content == nil {
			continue
		}
		for _, p := range e.Content.Parts {
			if c := p.Call; c != nil && c.ID != "" && (c.Name == "http_request" || c.Name == "web_fetch") {
				if _, exists := calls[c.ID]; exists {
					return fmt.Errorf("duplicate private tool identity")
				}
				calls[c.ID] = *c
			}
			if result := p.Result; result != nil {
				call, ok := calls[result.ID]
				if !ok || call.Name != result.Name || !successfulWebResult(result.Response) {
					continue
				}
				if !strings.HasPrefix(call.Args.URL, "https://") && !strings.HasPrefix(call.Args.URL, "http://") {
					continue
				}
				response, err := employeeDecodeWebResponse(result.Response)
				if err != nil {
					continue
				}
				status := response.StatusCode
				if (response.URL != "" && response.URL != call.Args.URL) || (response.FinalURL != "" && response.FinalURL != call.Args.URL) || (call.Name == "http_request" && (status < 200 || status >= 400)) || (status != 0 && (status < 200 || status >= 400)) || strings.TrimSpace(response.Body+response.Content) == "" {
					continue
				}
				// Report observations are server-captured, but still require the raw paired
				// successful response above; a model-written source string alone cannot pass.
				for _, observation := range run.Report.Unchanged {
					if observation.Source == call.Args.URL {
						matched = true
					}
				}
				for _, observation := range run.Report.Changed {
					if observation.Source == call.Args.URL {
						matched = true
					}
				}
			}
		}
	}
	if !matched {
		return fmt.Errorf("title lacks real correlated private web result")
	}
	return nil
}
