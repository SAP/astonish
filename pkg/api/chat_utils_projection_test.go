package api

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestSessionEventProjectorOrderedParity(t *testing.T) {
	bid, sid := uuid.New(), uuid.New()
	rows := []*session.Event{
		{ID: "first", InvocationID: "turn", Author: "assistant", Timestamp: time.Unix(1, 0), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("same", "model")}},
		{ID: "second", InvocationID: "turn", Author: "assistant", Timestamp: time.Unix(2, 0), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("same", "model")}},
		{ID: "tools", InvocationID: "turn", Author: "assistant", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "read_file", Args: map[string]any{"path": "a"}}},
			{FunctionCall: &genai.FunctionCall{ID: "call-2", Name: "read_file", Args: map[string]any{"path": "a"}}},
		}}}},
		{ID: "hidden", Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"private_checkpoint": "PRIVATE_SECRET"}}},
	}
	live := &SessionEventProjector{BotID: bid, SessionID: sid}
	history := &SessionEventProjector{BotID: bid, SessionID: sid}
	seen := map[string]bool{}
	for i, e := range rows {
		a, err := live.ProjectBot(context.Background(), store.BotTranscriptEvent{Cursor: int64(i + 10), Event: e}, nil)
		if err != nil {
			t.Fatal(err)
		}
		b, err := history.ProjectBot(context.Background(), store.BotTranscriptEvent{Event: e}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a.Events, b.Events) || string(a.Transcript) != string(b.Transcript) || a.EventID != b.EventID {
			t.Fatalf("live/history diverged: %+v %+v", a, b)
		}
		if a.Cursor != int64(i+10) || b.Cursor != 0 {
			t.Fatal("display identity replaced transport cursor")
		}
		for _, event := range a.Events {
			if seen[event.ID] {
				t.Fatal("equal content collapsed identity", event.ID)
			}
			seen[event.ID] = true
		}
		if strings.Contains(string(a.Transcript), "PRIVATE_SECRET") {
			t.Fatal("private state leaked")
		}
		if e.ID == "hidden" && len(a.Events) != 0 {
			t.Fatal("hidden row became visible")
		}
	}
	if len(seen) != 4 {
		t.Fatalf("lost ordered parts: %d", len(seen))
	}
}

func TestSessionEventProjectorReviewDoesNotTrustEmbeddedQuestion(t *testing.T) {
	bid, sid, run, approval := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	identity := uuid.NewSHA1(approval, []byte("bot-review:approval"))
	payload, _ := json.Marshal(map[string]any{"kind": "approval", "review_id": approval, "question": map[string]any{"schema_version": 1, "id": "forged", "kind": "text", "prompt": "PRIVATE_FORGED_QUESTION"}, "private_grant": "PRIVATE_GRANT"})
	e := &session.Event{ID: identity.String(), InvocationID: identity.String(), Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{SessionID: sid, RunID: run, TurnID: identity, Sequence: 1, Type: "bot_review", Payload: payload}}}}
	p := &SessionEventProjector{BotID: bid, SessionID: sid}
	before, _ := json.Marshal(e)
	out, err := p.ProjectBot(context.Background(), store.BotTranscriptEvent{Event: e}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out.Transcript), "PRIVATE_") || strings.Contains(string(out.Transcript), "question") {
		t.Fatalf("unbound approval data leaked: %s", out.Transcript)
	}
	q := json.RawMessage(`{"questionId":"q","kind":"text","prompt":"Public question"}`)
	a := store.BotApproval{ID: approval, BotID: bid, RunID: run, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: q}}
	out, err = p.ProjectBot(context.Background(), store.BotTranscriptEvent{Event: e}, []store.BotApproval{a})
	if err != nil || !strings.Contains(string(out.Transcript), "Public question") || strings.Contains(string(out.Transcript), "PRIVATE_") {
		t.Fatalf("authoritative enrichment failed: %s %v", out.Transcript, err)
	}
	after, _ := json.Marshal(e)
	if string(before) != string(after) {
		t.Fatal("projection mutated durable review")
	}
}

// Home narration is copied from a run transcript; its source session must remain
// bound to the same Bot rather than being confused with the Home transport ID.
type projectionRunStore struct {
	store.BotStore
	run *store.BotRun
}

func (s projectionRunStore) GetRun(context.Context, store.BotID, store.RunID) (*store.BotRun, error) {
	return s.run, nil
}

func TestSessionEventProjectorHomeRunSpeechBinding(t *testing.T) {
	bid, home, sid, run, turn := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	e := &session.Event{ID: "run-speech", InvocationID: turn.String(), Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{SessionID: sid, RunID: run, TurnID: turn, Sequence: 7, Type: "text", Payload: json.RawMessage(`{"text":"Acknowledged"}`)}}}}
	for _, tc := range []struct {
		name   string
		record *store.BotRun
		want   bool
	}{
		{"bound", &store.BotRun{ID: run, BotID: bid, SessionID: sid}, true},
		{"foreign-bot", &store.BotRun{ID: run, BotID: uuid.New(), SessionID: sid}, false},
		{"foreign-session", &store.BotRun{ID: run, BotID: bid, SessionID: uuid.New()}, false},
		{"foreign-run", &store.BotRun{ID: uuid.New(), BotID: bid, SessionID: sid}, false},
		{"missing-run", nil, false},
		{"missing-bot", &store.BotRun{ID: run, SessionID: sid}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &SessionEventProjector{BotID: bid, HomeSessionID: home, SessionID: home, Repository: projectionRunStore{run: tc.record}}
			out, err := p.ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: 42, Event: e}, nil)
			if !tc.want {
				if err == nil {
					t.Fatal("accepted unbound run narration")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if out.RunID != run || out.TurnID != turn.String() || out.Sequence != 7 || out.Cursor != 42 || len(out.Events) != 0 {
				t.Fatalf("unattested narration displayed or provenance lost: %+v", out)
			}
		})
	}
}

func TestSessionEventProjectorHomeSpeechRetainsProvenance(t *testing.T) {
	bid, sid, run, turn := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	e := &session.Event{ID: "speech", InvocationID: turn.String(), Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{SessionID: sid, RunID: run, TurnID: turn, Sequence: 7, Type: "text", Payload: json.RawMessage(`{"text":"Hello","private_checkpoint":"PRIVATE_SECRET"}`)}}}}
	out, err := (&SessionEventProjector{BotID: bid, SessionID: sid, HomeSessionID: sid}).ProjectBot(context.Background(), store.BotTranscriptEvent{Cursor: 42, Event: e}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.RunID != run || out.TurnID != turn.String() || out.Sequence != 7 || out.Cursor != 42 {
		t.Fatalf("speech lost provenance: %+v", out)
	}
	if len(out.Events) != 0 || strings.Contains(string(out.Transcript), "PRIVATE_SECRET") {
		t.Fatalf("unattested speech exposed: %+v", out)
	}
}
