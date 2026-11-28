package api

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"reflect"
	"testing"
)

func TestBotReviewQuestionProjectionBoundSchema(t *testing.T) {
	b, sid, run, aid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	q := json.RawMessage(`{"questionId":"q","kind":"select","prompt":"Which assignment?","options":[{"id":"a","label":"A"}],"allow_custom_answer":true}`)
	a := store.BotApproval{ID: aid, BotID: b, RunID: run, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: q}}
	id := uuid.NewSHA1(aid, []byte("bot-review:approval"))
	payload, _ := json.Marshal(map[string]any{"kind": "approval", "review_id": aid})
	env := bot.RuntimeEvent{SessionID: sid, RunID: run, TurnID: id, Sequence: 1, Type: "bot_review", Payload: payload}
	event := session.Event{ID: id.String(), InvocationID: id.String(), Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": env}}}
	before, _ := json.Marshal(event)
	projected := projectBotReviewQuestion(&event, []store.BotApproval{a}, b, sid)
	wire, err := marshalBotTranscriptEvent(projected, b, sid)
	if err != nil {
		t.Fatal(err)
	}
	var decoded session.Event
	if err = json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(decoded.Actions.StateDelta["bot_runtime_event"])
	var got bot.RuntimeEvent
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	var ref struct {
		Kind     string          `json:"kind"`
		ID       uuid.UUID       `json:"review_id"`
		Question json.RawMessage `json:"question"`
	}
	if err = json.Unmarshal(got.Payload, &ref); err != nil {
		t.Fatal(err)
	}
	var gotQuestion, wantQuestion any
	if json.Unmarshal(ref.Question, &gotQuestion) != nil || json.Unmarshal(q, &wantQuestion) != nil {
		t.Fatal("invalid projected question")
	}
	if ref.Kind != "approval" || ref.ID != aid || !reflect.DeepEqual(gotQuestion, wantQuestion) || decoded.ID != event.ID || got.TurnID != id || got.RunID != run || got.SessionID != sid {
		t.Fatalf("lost canonical question identity: %s", wire)
	}
	after, _ := json.Marshal(event)
	if string(before) != string(after) {
		t.Fatal("mutated persisted event")
	}
	for _, tc := range []struct {
		name   string
		change func(*session.Event, *store.BotApproval)
	}{
		{"foreign-bot", func(_ *session.Event, a *store.BotApproval) { a.BotID = uuid.New() }},
		{"foreign-run", func(_ *session.Event, a *store.BotApproval) { a.RunID = uuid.New() }},
		{"foreign-approval", func(_ *session.Event, a *store.BotApproval) { a.ID = uuid.New() }},
		{"invalid-question", func(_ *session.Event, a *store.BotApproval) {
			a.Interaction = &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "wrong", Payload: q}
		}},
		{"not-question", func(_ *session.Event, a *store.BotApproval) { a.Interaction = nil }},
		{"model", func(e *session.Event, _ *store.BotApproval) { e.Author = "model" }},
		{"unbound-event", func(e *session.Event, _ *store.BotApproval) { e.ID = uuid.NewString() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, approval := event, a
			tc.change(&e, &approval)
			if projectBotReviewQuestion(&e, []store.BotApproval{approval}, b, sid) != &e {
				t.Fatal("enriched untrusted or unbound question")
			}
		})
	}
	if projectBotReviewQuestion(&event, []store.BotApproval{a}, b, uuid.New()) != &event || projectBotReviewQuestion(&event, nil, b, sid) != &event {
		t.Fatal("enriched out-of-scope question")
	}
}
