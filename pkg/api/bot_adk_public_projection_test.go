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

// Private Chat actions remain available in run diagnostics, not Home narration.
func TestSessionEventProjectorHomeOrdinaryADKActivity(t *testing.T) {
	for _, name := range []string{"write_file", "edit_file", "load_file"} {
		for _, succeeded := range []bool{true, false} {
			t.Run(name+map[bool]string{true: "/success", false: "/failure"}[succeeded], func(t *testing.T) {
				bid, home, sid := uuid.New(), uuid.New(), uuid.New()
				response := map[string]any{"status": "success"}
				if !succeeded {
					response = map[string]any{"error": "recoverable tool failure"}
				}
				rows := []*session.Event{
					{ID: "call", InvocationID: "turn", Author: "assistant", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "write", Name: name, Args: map[string]any{"path": "/workspace/shared.txt", "content": "private"}}}}}}},
					{ID: "result", InvocationID: "turn", Author: "assistant", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "write", Name: name, Response: response}}}}}, Actions: session.EventActions{StateDelta: map[string]any{"private_checkpoint": "PRIVATE_ADK"}}},
				}
				live := &SessionEventProjector{BotID: bid, HomeSessionID: home, SessionID: home}
				restored := &SessionEventProjector{BotID: bid, HomeSessionID: home, SessionID: home}
				diagnostic := &SessionEventProjector{BotID: bid, HomeSessionID: home, SessionID: sid}
				var types []string
				for i, e := range rows {
					before, _ := json.Marshal(e)
					a, err := live.ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: int64(i + 1), Event: e}, nil)
					if err != nil {
						t.Fatal(err)
					}
					b, err := restored.ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: int64(i + 1), Event: e}, nil)
					if err != nil || !reflect.DeepEqual(a.Events, b.Events) || len(a.Events) != 0 || strings.Contains(string(a.Transcript), "PRIVATE_ADK") {
						t.Fatalf("private Home projection: %+v %+v %v", a, b, err)
					}
					d, err := diagnostic.ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: int64(i + 1), Event: e}, nil)
					if err != nil {
						t.Fatal(err)
					}
					for _, ev := range d.Events {
						types = append(types, ev.Type)
						if ev.Type == "artifact" && (ev.Data["isReport"] == true || ev.Data["path"] != "/workspace/shared.txt") {
							t.Fatalf("unreviewed write promoted: %+v", ev)
						}
					}
					after, _ := json.Marshal(e)
					if string(before) != string(after) {
						t.Fatal("durable row mutated")
					}
				}
				want := []string{"tool_call", "tool_result"}
				if succeeded {
					want = append(want, "artifact")
				}
				if !reflect.DeepEqual(types, want) {
					t.Fatalf("private diagnostics lost: %v want %v", types, want)
				}
			})
		}
	}
}

func TestSessionEventProjectorHomeOrdinaryADKDelegation(t *testing.T) {
	bid, home := uuid.New(), uuid.New()
	rows := []*session.Event{
		{ID: "call", Author: "assistant", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "batch", Name: "delegate_tasks", Args: map[string]any{"tasks": []any{map[string]any{"name": "Private research", "task": "Compare evidence"}}}}}}}}},
		{ID: "response", Author: "assistant", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "batch", Name: "delegate_tasks", Response: map[string]any{"status": "complete", "results": []any{map[string]any{"name": "Private research", "status": "success", "result": "Private comparison", "private_grant": "PRIVATE_AUTHORITY"}}}}}}}}},
	}
	live := &SessionEventProjector{BotID: bid, HomeSessionID: home, SessionID: home}
	replay := &SessionEventProjector{BotID: bid, HomeSessionID: home, SessionID: home}
	for i, e := range rows {
		a, err := live.ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: int64(i + 1), Event: e}, nil)
		if err != nil {
			t.Fatal(err)
		}
		b, err := replay.ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: int64(i + 1), Event: e}, nil)
		if err != nil || len(a.Events) != 0 || !reflect.DeepEqual(a.Events, b.Events) || strings.Contains(string(a.Transcript), "PRIVATE_AUTHORITY") {
			t.Fatalf("private delegation exposed: %+v %v", a, err)
		}
	}
}
