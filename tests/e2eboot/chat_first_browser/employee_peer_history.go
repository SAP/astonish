package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

const (
	employeePeerTranscript = "transcript"
	employeePeerDone       = "done"
)

// This finite, authorized read contains persisted event IDs. The generic session
// JSON summary has only display messages and cannot prove admission provenance.
func employeePeerHistoryPath(peer store.Bot) string {
	return "/api/studio/bots/" + peer.ID.String() + "/sessions/" + peer.HomeSessionID.String() + "/stream?scope=personal"
}

// Zero target and nil error means observation is pending, never admission. A
// complete snapshot may precede the title event; no assistant response is needed.
func employeePeerTitleRun(raw string, peer store.Bot, runs []store.BotRun) (store.BotRun, error) {
	var zero store.BotRun
	if len(runs) > 1 {
		return zero, fmt.Errorf("duplicate preservation peer title run")
	}
	if len(runs) == 1 {
		r := runs[0]
		if r.ID == uuid.Nil || r.BotID != peer.ID || r.Trigger != store.TriggerManual || r.Presentation != store.BotRunPresentationHomeInteractive || r.UserEventID == "" || r.HistoryCursor <= 0 {
			return zero, fmt.Errorf("preservation peer title source mismatch")
		}
	}
	events := map[string]string{}
	complete := false
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	for _, frame := range strings.Split(normalized, "\n\n") {
		if strings.TrimSpace(frame) == "" {
			continue
		}
		var kind string
		var data []string
		for _, line := range strings.Split(frame, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				kind = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			case strings.HasPrefix(line, "id:"), strings.HasPrefix(line, ":"), strings.TrimSpace(line) == "":
			default:
				return zero, fmt.Errorf("malformed preservation peer history: expected persisted SSE, not session summary")
			}
		}
		if len(data) == 0 && kind == "" {
			continue
		}
		if complete {
			return zero, fmt.Errorf("malformed preservation peer history: data after done")
		}
		payload := []byte(strings.Join(data, "\n"))
		switch kind {
		case employeePeerDone:
			var done struct {
				Persisted bool `json:"persisted"`
				Count     *int `json:"count"`
			}
			if json.Unmarshal(payload, &done) != nil || !done.Persisted || (done.Count != nil && *done.Count != len(events)) {
				return zero, fmt.Errorf("malformed preservation peer history: invalid persisted completion")
			}
			complete = true
		case employeePeerTranscript:
			var event struct {
				ID      string `json:"id"`
				Author  string `json:"author"`
				Content *struct {
					Role  string `json:"role"`
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			}
			if json.Unmarshal(payload, &event) != nil || event.ID == "" || event.Author == "" {
				return zero, fmt.Errorf("malformed preservation peer history: invalid transcript event")
			}
			if _, exists := events[event.ID]; exists {
				return zero, fmt.Errorf("duplicate preservation peer history event")
			}
			text := ""
			if event.Author == "user" && event.Content != nil && event.Content.Role == "user" {
				for _, part := range event.Content.Parts {
					text += part.Text
				}
			}
			events[event.ID] = text
		default:
			return zero, fmt.Errorf("malformed preservation peer history: unexpected event %q", kind)
		}
	}
	if !complete {
		return zero, nil
	}
	titles := 0
	for _, text := range events {
		if text == employeePeerTitle {
			titles++
		}
	}
	if titles > 1 {
		return zero, fmt.Errorf("duplicate preservation peer title event")
	}
	if len(runs) == 0 {
		return zero, nil
	}
	r := runs[0]
	text, exists := events[r.UserEventID]
	if !exists {
		return zero, nil
	}
	if text != employeePeerTitle || titles != 1 {
		return zero, fmt.Errorf("preservation peer title source mismatch")
	}
	return r, nil
}
