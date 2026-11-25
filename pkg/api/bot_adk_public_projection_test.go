package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestSessionEventProjectorHomeOrdinaryADKActivity(t *testing.T) {
	for _, name := range []string{"write_file", "edit_file", "load_file"} {
		t.Run(name, func(t *testing.T) {
			for _, succeeded := range []bool{true, false} {
				t.Run(map[bool]string{true: "success", false: "failure"}[succeeded], func(t *testing.T) {
					bid, home := uuid.New(), uuid.New()
					response := map[string]any{"status": "success"}
					if !succeeded {
						response = map[string]any{"error": "recoverable tool failure"}
					}
					rows := []*session.Event{
						{ID: "call", InvocationID: "turn", Author: "assistant", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "write", Name: name, Args: map[string]any{"path": "/workspace/shared.txt", "content": "public"}}}}}}},
						{ID: "result", InvocationID: "turn", Author: "assistant", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "write", Name: name, Response: response}}}}}, Actions: session.EventActions{StateDelta: map[string]any{"private_checkpoint": "PRIVATE_ADK"}}},
					}
					project := func(p *SessionEventProjector, e *session.Event, cursor int64) SessionEventProjection {
						t.Helper()
						before, _ := json.Marshal(e)
						out, err := p.ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: cursor, Event: e}, nil)
						if err != nil {
							t.Fatal(err)
						}
						if strings.Contains(string(out.Transcript), "PRIVATE_ADK") {
							t.Fatal("private checkpoint leaked")
						}
						after, _ := json.Marshal(e)
						if string(before) != string(after) {
							t.Fatal("durable row mutated")
						}
						return out
					}
					live := &SessionEventProjector{BotID: bid, HomeSessionID: home, SessionID: home}
					restored := &SessionEventProjector{BotID: bid, HomeSessionID: home, SessionID: home}
					reconnect := &SessionEventProjector{BotID: bid, HomeSessionID: home, SessionID: home}
					var types []string
					// Feed the already-delivered call to rebuild presentation state only. Its
					// stable identity is not a new delivery; the HTTP reconnect tests own emission.
					project(reconnect, rows[0], 1)
					for i, e := range rows {
						out := project(live, e, int64(i+1))
						replay := project(restored, e, int64(i+1))
						if !reflect.DeepEqual(out.Events, replay.Events) {
							t.Fatal("live/restore diverged")
						}
						if i == 1 && !reflect.DeepEqual(out.Events, project(reconnect, e, 2).Events) {
							t.Fatal("reconnect lost matched file response")
						}
						for _, ev := range out.Events {
							types = append(types, ev.Type)
							if ev.Type == "artifact" && (ev.Data["isReport"] == true || ev.Data["path"] != "/workspace/shared.txt") {
								t.Fatalf("unreviewed write promoted: %+v", ev)
							}
						}
					}
					want := []string{"tool_call", "tool_result"}
					if succeeded {
						want = append(want, "artifact")
					}
					if !reflect.DeepEqual(types, want) {
						t.Fatalf("Home dropped ordinary ADK activity: %v want %v", types, want)
					}
				})
			}
		})
	}
}

func TestSessionEventProjectorHomeOrdinaryADKDelegation(t *testing.T) {
	bid, home := uuid.New(), uuid.New()
	p := &SessionEventProjector{BotID: bid, HomeSessionID: home, SessionID: home}
	call := &session.Event{ID: "call", Author: "assistant", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "batch", Name: "delegate_tasks", Args: map[string]any{"tasks": []any{map[string]any{"name": "Public research", "task": "Compare evidence"}}}}}}}}}
	result := &session.Event{ID: "response", Author: "assistant", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "batch", Name: "delegate_tasks", Response: map[string]any{"status": "complete", "results": []any{map[string]any{"name": "Public research", "status": "success", "result": "Public comparison", "private_grant": "PRIVATE_AUTHORITY"}}}}}}}}}
	for i, e := range []*session.Event{call, result} {
		out, err := p.ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: int64(i + 1), Event: e}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range out.Events {
			if ev.Type != "subtask_progress" {
				t.Fatalf("delegation rendered as opaque tool: %+v", ev)
			}
			raw, _ := json.Marshal(ev.Data)
			if strings.Contains(string(raw), "PRIVATE_AUTHORITY") {
				t.Fatal("delegation authority leaked")
			}
		}
		if i == 1 {
			if len(out.Events) != 5 {
				t.Fatalf("missing delegation lifecycle: %+v", out.Events)
			}
		}
	}
}
