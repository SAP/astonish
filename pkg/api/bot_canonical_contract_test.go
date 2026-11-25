package api

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotCanonicalPlanUsesSharedProgressContract(t *testing.T) {
	sid, bid := uuid.New(), uuid.New()
	snapshot := map[string]any{"plan_id": "plan", "goal": "Verify", "revision": 1, "sequence": 2, "document": map[string]any{"lifecycle": "approved"}, "steps": []any{map[string]any{"name": "check", "status": "pending"}}}
	row := &session.Event{ID: "plan-row", Author: "system", Actions: session.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: snapshot}}}
	out, err := (&SessionEventProjector{BotID: bid, SessionID: sid}).ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: 1, Event: row}, nil)
	if err != nil || len(out.Events) != 1 {
		t.Fatalf("plan missing: %+v %v", out, err)
	}
	event := out.Events[0]
	if event.Type != "subtask_progress" || event.Data["event_type"] != "plan_snapshot" || event.Data["snapshot"] == nil {
		t.Fatalf("plan bypasses shared renderer contract: %+v", event)
	}
	raw, _ := json.Marshal(map[string]any{"event_type": "plan_snapshot", "snapshot": snapshot, "private_checkpoint": "hidden"})
	row.Author = "bot_runtime"
	row.Actions.StateDelta = map[string]any{"bot_runtime_event": bot.RuntimeEvent{SessionID: sid, RunID: uuid.New(), TurnID: uuid.New(), Sequence: 1, Type: "subtask_progress", Payload: raw}}
	out, err = (&SessionEventProjector{BotID: bid, SessionID: sid}).ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: 2, Event: row}, nil)
	want, _ := json.Marshal(event.Data)
	var got []byte
	if len(out.Events) == 1 {
		got, _ = json.Marshal(out.Events[0].Data)
	}
	var wantValue, gotValue any
	if json.Unmarshal(want, &wantValue) != nil || json.Unmarshal(got, &gotValue) != nil {
		t.Fatal("invalid plan JSON")
	}
	if err != nil || len(out.Events) != 1 || !reflect.DeepEqual(wantValue, gotValue) {
		t.Fatalf("runtime plan lost shared snapshot: %+v %v", out, err)
	}
	for _, home := range []uuid.UUID{uuid.Nil, sid} {
		projection, err := (&SessionEventProjector{BotID: bid, SessionID: sid, HomeSessionID: home}).ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: 2, Event: row}, nil)
		if err != nil || len(projection.Events) != 1 || projection.Events[0].Data["event_type"] != "plan_snapshot" {
			t.Fatalf("plan missing from home=%s: %+v %v", home, projection.Events, err)
		}
	}
	for _, invalid := range []any{nil, "not a snapshot", map[string]any{"plan_id": "plan"}} {
		raw, _ := json.Marshal(map[string]any{"event_type": "plan_snapshot", "snapshot": invalid, "private_checkpoint": "hidden"})
		row.Actions.StateDelta = map[string]any{"bot_runtime_event": bot.RuntimeEvent{SessionID: sid, RunID: uuid.New(), TurnID: uuid.New(), Sequence: 1, Type: "subtask_progress", Payload: raw}}
		projection, err := (&SessionEventProjector{BotID: bid, SessionID: sid}).ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: 3, Event: row}, nil)
		if err != nil || len(projection.Events) != 0 {
			t.Fatalf("invalid plan became visible: %+v %v", projection, err)
		}
	}
}

func TestBotCanonicalSourceTextOwnership(t *testing.T) {
	bid, sid, run, turn := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, home := range []bool{false, true} {
		p := &SessionEventProjector{BotID: bid, SessionID: sid}
		if home {
			p.HomeSessionID = sid
		}
		for i, source := range []string{"adk-source", "", ""} {
			raw, _ := json.Marshal(map[string]any{"text": "same", "sourceEventId": source})
			row := &session.Event{ID: uuid.NewString(), Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{SessionID: sid, RunID: run, TurnID: turn, Sequence: int64(i + 1), Type: "text", Payload: raw}}}}
			out, err := p.ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: int64(i + 1), Event: row}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if source != "" && !home {
				if len(out.Events) != 0 {
					t.Fatal("source-owned runtime copy displayed")
				}
			} else if len(out.Events) != 1 || out.Events[0].Data["text"] != "same" {
				t.Fatalf("independent equal text lost: %+v", out)
			}
		}
	}
}

func TestBotCanonicalSharedPresentationFields(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		payload map[string]any
	}{
		{"artifact", map[string]any{"path": "/report.md", "fileName": "report.md", "fileType": "Markdown", "toolName": "write_file", "isReport": true, "reportTitle": "Findings"}},
		{"report_marker", map[string]any{"path": "/report.md", "title": "Findings"}},
		{"subtask_progress", map[string]any{"event_type": "delegation_start", "batch_id": "batch", "task_id": "task", "task_name": "Research", "sequence": float64(1), "tasks": []any{}}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			bid, sid, run, turn := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			payload := map[string]any{}
			for k, v := range tc.payload {
				payload[k] = v
			}
			payload["private_checkpoint"] = "must-not-leak"
			raw, _ := json.Marshal(payload)
			row := &session.Event{ID: "shared-event", Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{SessionID: sid, RunID: run, TurnID: turn, Sequence: 1, Type: tc.kind, Payload: raw}}}}
			p := &SessionEventProjector{BotID: bid, SessionID: sid}
			out, err := p.ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: 1, Event: row}, nil)
			if err != nil || len(out.Events) != 1 {
				t.Fatalf("missing shared event: %+v %v", out, err)
			}
			if out.Events[0].Type != tc.kind || !reflect.DeepEqual(out.Events[0].Data, tc.payload) {
				t.Fatalf("shared fields changed: %+v", out.Events[0])
			}
		})
	}
}

func TestBotCanonicalFramesAtomicCursor(t *testing.T) {
	sid := uuid.New()
	p := SessionEventProjection{Cursor: 42, SessionID: sid, TurnID: "turn", Sequence: 9,
		Events: []ChatEvent{
			{ID: sid.String() + "/row/0", Type: "text", Data: map[string]any{"text": "same"}},
			{ID: sid.String() + "/row/1", Type: "tool_call", Data: map[string]any{"id": "call", "name": "read_file", "args": map[string]any{"path": "/workspace/report.md"}}},
			{ID: sid.String() + "/row/2", Type: "text", Data: map[string]any{"text": "same"}},
		},
	}
	frames, err := p.BotFrames(30)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("got %d frames", len(frames))
	}
	seen := map[string]bool{}
	for i, frame := range frames {
		var data map[string]any
		if err := json.Unmarshal(frame.Data, &data); err != nil {
			t.Fatal(err)
		}
		metadata, ok := data["_projection"].(map[string]any)
		if !ok {
			t.Fatalf("missing metadata: %s", frame.Data)
		}
		if frame.Type != p.Events[i].Type || metadata["event_id"] != p.Events[i].ID {
			t.Fatalf("wrong event: %+v", frame)
		}
		if seen[metadata["event_id"].(string)] {
			t.Fatal("content equality erased event identity")
		}
		seen[metadata["event_id"].(string)] = true
		want := int64(30)
		if i == len(frames)-1 {
			want = 42
		}
		if frame.Cursor != want || metadata["cursor"] != botEventCursor(sid, want) {
			t.Fatalf("row committed prematurely: %+v", frame)
		}
		if metadata["sequence"] != float64(9) || metadata["turn_id"] != "turn" {
			t.Fatalf("lost turn metadata: %v", metadata)
		}
		delete(data, "_projection")
		original, _ := json.Marshal(p.Events[i].Data)
		roundtrip, _ := json.Marshal(data)
		if string(original) != string(roundtrip) {
			t.Fatalf("changed Chat payload: %s != %s", original, roundtrip)
		}
	}
	// Every interruption before the final frame reconnects before this row. The
	// replay then delivers all parts with the same identities, allowing exact dedup.
	replay, err := p.BotFrames(frames[0].Cursor)
	if err != nil || !reflect.DeepEqual(frames, replay) {
		t.Fatalf("unstable replay: %v", err)
	}
	if _, ok := p.Events[0].Data["_projection"]; ok {
		t.Fatal("mutated Chat event payload")
	}
}

func TestBotCanonicalFramesHiddenAndInvalidRows(t *testing.T) {
	sid := uuid.New()
	p := SessionEventProjection{Cursor: 42, SessionID: sid}
	frames, err := p.BotFrames(30)
	if err != nil || len(frames) != 1 || frames[0].Type != "cursor" || frames[0].Cursor != 42 {
		t.Fatalf("hidden row did not advance: %+v %v", frames, err)
	}
	if strings.Contains(string(frames[0].Data), "event_id") {
		t.Fatal("hidden cursor invented a display event")
	}
	for _, previous := range []int64{-1, 42, 43} {
		if _, err := p.BotFrames(previous); err == nil {
			t.Fatalf("accepted invalid previous cursor %d", previous)
		}
	}
	p.Events = []ChatEvent{{ID: "valid", Type: "text", Data: map[string]any{"text": "public"}}, {ID: "invalid", Type: "text", Data: map[string]any{"bad": make(chan int)}}}
	if frames, err := p.BotFrames(30); err == nil || len(frames) != 0 {
		t.Fatalf("partial row escaped encoding failure: %+v %v", frames, err)
	}
}

func TestSessionEventProjectorRedactsCanonicalPayload(t *testing.T) {
	bid, sid := uuid.New(), uuid.New()
	secret := "canonical-test-secret-value"
	redactor := credentials.NewRedactor()
	redactor.AddSecret("test", secret)
	row := &session.Event{ID: "row", Author: "assistant", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{
		{Text: "Public " + secret},
		{FunctionCall: &genai.FunctionCall{ID: "call", Name: "read_file", Args: map[string]any{"path": secret}}},
		{FunctionResponse: &genai.FunctionResponse{ID: "call", Name: "read_file", Response: map[string]any{"body": secret}}},
		{Text: "PRIVATE_THOUGHT", Thought: true},
	}}}, Actions: session.EventActions{StateDelta: map[string]any{"private_checkpoint": "PRIVATE_CHECKPOINT"}}}
	before, _ := json.Marshal(row)
	p := &SessionEventProjector{BotID: bid, SessionID: sid, Redactor: redactor}
	out, err := p.ProjectBot(context.Background(), store.BotTranscriptEvent{Cursor: 42, Event: row}, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(out.Events)
	if len(out.Events) != 3 || strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "PRIVATE_") {
		t.Fatalf("unsafe public content: %s", encoded)
	}
	after, _ := json.Marshal(row)
	if string(before) != string(after) {
		t.Fatal("mutated saved event")
	}
	row.ID = "user"
	row.Content = &genai.Content{Role: "user", Parts: []*genai.Part{{Text: secret}}}
	out, err = p.ProjectBot(context.Background(), store.BotTranscriptEvent{Cursor: 43, Event: row}, nil)
	if err != nil || len(out.Events) != 1 || out.Events[0].Data["text"] != secret {
		t.Fatalf("ordinary Chat user-text semantics changed: %+v %v", out, err)
	}
}
