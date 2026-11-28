package entstore

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/execution"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotEmployeeInterimFencedWriter(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, ctx, initial, _ := runtimeFixture(t, scope)
			b, err := repo.Get(ctx, initial.BotID)
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "interim fixture"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Intent: store.BotIntentConversation, Trigger: store.TriggerManual, IdempotencyKey: "interim", Instructions: "Check it"})
			if err != nil {
				t.Fatal(err)
			}
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "interim", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			// This legacy RequestRun fixture predates canonical Home admission;
			// pin its presentation in stored state, never via an event or caller flag.
			if err := repo.db.Transaction(worker, func(tx botDB) error {
				saved, err := tx.ReadRun(worker, b.ID, run.ID)
				if err != nil {
					return err
				}
				saved.Run.Presentation = store.BotRunPresentationHomeInteractive
				return tx.(botWorkDB).updateWorkRun(worker, *saved, lease, repo.clock())
			}); err != nil {
				t.Fatal(err)
			}
			wire := bot.RuntimeEvent{RunID: run.ID, SessionID: run.SessionID, TurnID: uuid.New(), Sequence: 1, Type: "text", Payload: json.RawMessage(`{"text":"I will check that."}`)}
			for _, kind := range []string{"runtime", "worker", "spoof", "trusted", "copy"} {
				e := &session.Event{ID: kind, Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": wire, store.BotEmployeeInterimKey: true}}}
				c := worker
				if kind == "worker" {
					e.Author = "chat"
				}
				if kind == "trusted" {
					c = store.WithBotEmployeeInterimRelease(worker, wire)
				}
				if kind == "copy" {
					binding, err := repo.readBotEmployeeInterim(ctx, b.ID, run.ID, "trusted")
					if err != nil {
						t.Fatal(err)
					}
					e.Actions.StateDelta[store.BotEmployeeInterimKey] = binding
				}
				if err := repo.appendBotRunEvent(c, lease, run.SessionID, e); err != nil {
					t.Fatal(err)
				}
				binding, err := repo.readBotEmployeeInterim(ctx, b.ID, run.ID, kind)
				if kind == "trusted" {
					if err != nil || binding == nil || binding.EventID != kind {
						t.Fatalf("trusted binding lost: %+v %v", binding, err)
					}
				} else if err == nil {
					t.Fatalf("%s acquired speech: %+v", kind, binding)
				}
				if e.Actions.StateDelta[store.BotEmployeeInterimKey] == nil {
					t.Fatal("caller mutated")
				}
			}
		})
	}
}

func TestBotWorkNarrationProjectionBoundary(t *testing.T) {
	run := store.BotRun{ID: uuid.New(), SessionID: uuid.New(), Presentation: store.BotRunPresentationHomeInteractive}
	work := &store.BotWorkState{Protocol: store.BotWorkProtocolAgentLedV1, EmployeeReviewPolicy: store.BotEmployeeReviewEvidenceBoundV1}
	for _, tc := range []struct {
		name, kind string
		want       bool
		mutate     func(*store.BotRun, *store.BotWorkState, *session.Event, *bot.RuntimeEvent)
	}{
		{name: "interim text", kind: "text", want: true},
		{name: "work call stays private", kind: "tool_call"},
		{name: "source response", kind: "tool_result"},
		{name: "final signal", kind: "done"},
		{name: "artifact", kind: "artifact"},
		{name: "unknown", kind: "made_up"},
		{name: "scheduled", kind: "text", mutate: func(r *store.BotRun, _ *store.BotWorkState, _ *session.Event, _ *bot.RuntimeEvent) {
			r.Presentation = ""
		}},
		{name: "unbuffered legacy", kind: "text", mutate: func(_ *store.BotRun, w *store.BotWorkState, _ *session.Event, _ *bot.RuntimeEvent) {
			w.EmployeeReviewPolicy = ""
		}},
		{name: "producer content", kind: "text", mutate: func(_ *store.BotRun, _ *store.BotWorkState, e *session.Event, _ *bot.RuntimeEvent) {
			e.Content = genai.NewContentFromText("private draft", "model")
		}},
		{name: "model spoof", kind: "text", mutate: func(_ *store.BotRun, _ *store.BotWorkState, e *session.Event, _ *bot.RuntimeEvent) { e.Author = "chat" }},
		{name: "wrong run", kind: "text", mutate: func(_ *store.BotRun, _ *store.BotWorkState, _ *session.Event, e *bot.RuntimeEvent) {
			e.RunID = uuid.New()
		}},
		{name: "wrong session", kind: "text", mutate: func(_ *store.BotRun, _ *store.BotWorkState, _ *session.Event, e *bot.RuntimeEvent) {
			e.SessionID = uuid.New()
		}},
		{name: "partial", kind: "text", mutate: func(_ *store.BotRun, _ *store.BotWorkState, e *session.Event, _ *bot.RuntimeEvent) { e.Partial = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w := run, *work
			envelope := bot.RuntimeEvent{RunID: r.ID, SessionID: r.SessionID, TurnID: uuid.New(), Sequence: 1, Type: tc.kind, Payload: json.RawMessage(`{"text":"actual producer text"}`)}
			e := &session.Event{Author: "bot_runtime"}
			if tc.mutate != nil {
				tc.mutate(&r, &w, e, &envelope)
			}
			e.Actions.StateDelta = map[string]any{"bot_runtime_event": envelope}
			if got := botWorkProgressEvent(r, &w, e); got != tc.want {
				t.Fatalf("project=%v want=%v", got, tc.want)
			}
		})
	}
}

type narrationApprovalDB struct {
	botDB
	approval store.BotApproval
}

func (d narrationApprovalDB) ReadApproval(context.Context, uuid.UUID, uuid.UUID) (*store.BotApproval, error) {
	a := d.approval
	return &a, nil
}

func TestBotWorkNarrationOwnerAnswerRequiresConsumedBinding(t *testing.T) {
	run := store.BotRun{ID: uuid.New(), BotID: uuid.New(), Version: 1}
	approval := store.BotApproval{ID: uuid.New(), RunID: run.ID, BotID: run.BotID, Version: 1, Status: store.ApprovalConsumed, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"text","prompt":"Which task?"}`)}, Response: &store.BotInteractionResponse{QuestionID: "q", Answer: "Weekly roundup"}}
	for _, tc := range []struct {
		name   string
		want   bool
		mutate func(*store.BotApproval, *session.Event)
	}{
		{name: "bound answer", want: true},
		{name: "unconsumed", mutate: func(a *store.BotApproval, _ *session.Event) { a.Status = store.ApprovalApproved }},
		{name: "other run", mutate: func(a *store.BotApproval, _ *session.Event) { a.RunID = uuid.New() }},
		{name: "other version", mutate: func(a *store.BotApproval, _ *session.Event) { a.Version++ }},
		{name: "different answer", mutate: func(a *store.BotApproval, _ *session.Event) {
			a.Response = &store.BotInteractionResponse{QuestionID: "q", Answer: "Different"}
		}},
		{name: "model user role", mutate: func(_ *store.BotApproval, e *session.Event) { e.Author = "chat" }},
		{name: "internal input", mutate: func(_ *store.BotApproval, e *session.Event) {
			e.Actions.StateDelta = map[string]any{"_astonish_turn_context": true}
		}},
		{name: "uncorrelated ID", mutate: func(_ *store.BotApproval, e *session.Event) { e.ID = uuid.NewString() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := approval
			raw, _ := json.Marshal(map[string]any{"type": "bot_owner_answer", "approval_id": a.ID, "tool": "ask_user", "response": a.Response})
			e := &session.Event{ID: "bot-owner-answer:" + a.ID.String(), Author: "user", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(string(raw), "user")}}
			if tc.mutate != nil {
				tc.mutate(&a, e)
			}
			got, err := botWorkAnswerEvent(context.Background(), narrationApprovalDB{approval: a}, run, e)
			if err != nil || got != tc.want {
				t.Fatalf("project=%v want=%v err=%v", got, tc.want, err)
			}
		})
	}
}
