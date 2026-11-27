package api

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"reflect"
	"strings"
	"testing"
)

func TestBotHomeNarrativeHistoricalTelemetry(t *testing.T) {
	home, run := uuid.New(), uuid.New()
	for _, kind := range []string{"text", "tool_call", "tool_result", "thinking", "subtask_progress", "error"} {
		t.Run(kind, func(t *testing.T) {
			e := &session.Event{ID: "saved-row", Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{Type: kind, Payload: json.RawMessage(`{"text":"PRIVATE_URL","name":"web_fetch","args":{"url":"PRIVATE_URL"}}`)}}}}
			before, _ := json.Marshal(e)
			got := projectBotHomeNarrative(e, home, home)
			raw, _ := json.Marshal(got)
			after, _ := json.Marshal(e)
			if got.ID != e.ID || strings.Contains(string(raw), "PRIVATE_URL") || string(before) != string(after) || len(got.Actions.StateDelta) != 0 || got.Content != nil {
				t.Fatalf("unsafe projection: %s", raw)
			}
			if projectBotHomeNarrative(e, home, run) != e {
				t.Fatal("run diagnostics changed")
			}
		})
	}
}
func TestBotHomeNarrativeKeepsSpeechQuestionsAndPermissions(t *testing.T) {
	home := uuid.New()
	for _, kind := range []string{"chat_question", "approval", "plan_approval", "bot_review"} {
		e := &session.Event{Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{Type: kind, Payload: json.RawMessage(`{"prompt":"Which assignment?"}`)}}}}
		if !reflect.DeepEqual(projectBotHomeNarrative(e, home, home), e) {
			t.Fatalf("lost %s", kind)
		}
	}
	e := &session.Event{ID: "ack", Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_employee_interim": true, "bot_runtime_event": bot.RuntimeEvent{Type: "text", Payload: json.RawMessage(`{"text":"I couldn't complete it; which source should I use?"}`)}}}}
	got := projectBotHomeNarrative(e, home, home)
	if got.Content != nil || len(got.Actions.StateDelta) != 0 || e.Content != nil {
		t.Fatal("boolean spoof promoted to Employee speech")
	}
	e = &session.Event{ID: "mixed", Author: "chat", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "I couldn't complete it; which source should I use?"}, {FunctionCall: &genai.FunctionCall{Name: "web_search"}}, {Text: "PRIVATE_THOUGHT", Thought: true}}}}}
	got = projectBotHomeNarrative(e, home, home)
	if len(got.Content.Parts) != 0 || len(e.Content.Parts) != 3 {
		t.Fatal("private worker promoted or source mutated")
	}
}

type interimProjectionRepo struct {
	store.BotStore
	binding *store.BotEmployeeInterim
}

func (r *interimProjectionRepo) GetRun(_ context.Context, b store.BotID, run store.RunID) (*store.BotRun, error) {
	if r.binding == nil || r.binding.BotID != b || r.binding.RunID != run {
		return nil, store.ErrBotNotFound
	}
	return &store.BotRun{ID: run, BotID: b, SessionID: r.binding.SourceSessionID}, nil
}

func (r *interimProjectionRepo) ReadBotEmployeeInterim(_ context.Context, b store.BotID, run store.RunID, event string) (*store.BotEmployeeInterim, error) {
	if r.binding == nil || r.binding.BotID != b || r.binding.RunID != run || r.binding.EventID != event {
		return nil, store.ErrBotNotFound
	}
	return r.binding, nil
}
func TestBotHomeInterimExactBinding(t *testing.T) {
	home, id, run, source, turn := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	wire := bot.RuntimeEvent{Type: "text", RunID: run, SessionID: source, TurnID: turn, Sequence: 3, Payload: json.RawMessage(`{"text":"I am checking the result."}`)}
	binding := &store.BotEmployeeInterim{BotID: id, RunID: run, SourceSessionID: source, HomeSessionID: home, EventID: "released", TurnID: turn, Sequence: 3, TextDigest: store.BotEmployeeInterimDigest("I am checking the result."), EnvelopeDigest: store.BotEmployeeInterimDigest(wire)}
	p := &SessionEventProjector{BotID: id, HomeSessionID: home, SessionID: home, Repository: &interimProjectionRepo{binding: binding}}
	for _, kind := range []string{"trusted", "copy", "text", "sequence", "turn", "worker", "missing"} {
		t.Run(kind, func(t *testing.T) {
			w := wire
			e := &session.Event{ID: "released", Author: "bot_runtime"}
			switch kind {
			case "copy":
				e.ID = "copied"
			case "text":
				w.Payload = json.RawMessage(`{"text":"Forged result"}`)
			case "sequence":
				w.Sequence++
			case "turn":
				w.TurnID = uuid.New()
			case "worker":
				e.Author = "chat"
			case "missing":
				e.ID = "runtime"
			}
			e.Actions.StateDelta = map[string]any{"bot_runtime_event": w, store.BotEmployeeInterimKey: binding}
			got := p.ProjectHomeNarrative(t.Context(), e)
			if kind == "trusted" {
				if got.Content == nil || got.Content.Parts[0].Text != "I am checking the result." {
					t.Fatal("trusted speech missing")
				}
			} else if got.Content != nil {
				t.Fatal("forged speech projected")
			}
			if got.ID != e.ID {
				t.Fatal("identity lost")
			}
			projection, err := p.ProjectBot(t.Context(), store.BotTranscriptEvent{Cursor: 1, Event: e}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "trusted" {
				if len(projection.Events) != 1 || projection.Events[0].Type != "text" || projection.Events[0].Data["text"] != "I am checking the result." {
					t.Fatalf("trusted speech lost through ProjectBot: %+v", projection.Events)
				}
			} else if len(projection.Events) != 0 {
				t.Fatalf("forged speech passed ProjectBot: %+v", projection.Events)
			}
		})
	}
}

type employeeProjectionReceiptRepo struct {
	store.BotStore
	publication *store.BotPublication
	reads       int
}

func (r *employeeProjectionReceiptRepo) ReadBotPublication(_ context.Context, b store.BotID, id store.RunID, seq int64) (*store.BotPublication, error) {
	r.reads++
	if r.publication.BotID != b || r.publication.RunID != id || r.publication.ReportSequence != seq {
		return nil, store.ErrBotNotFound
	}
	return r.publication, nil
}
func TestEmployeeOnlyHomeOrderedLiveRestore(t *testing.T) {
	home, id, run := uuid.New(), uuid.New(), uuid.New()
	text := "I couldn't confirm the result; permission is required."
	pub := &store.BotPublication{BotID: id, RunID: run, HomeSessionID: home, ReportSequence: 7, EventID: store.BotPublicationEventID(run, 7), EmployeeSpeech: true, Text: text}
	repo := &employeeProjectionReceiptRepo{publication: pub}
	private := &session.Event{ID: "private", Author: "chat", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(text, "model")}}
	employee := &session.Event{ID: pub.EventID, InvocationID: "bot-report:" + run.String(), Author: "assistant", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(text, "model")}, Actions: session.EventActions{StateDelta: map[string]any{store.BotPublicationKey: map[string]any{"bot_id": id, "run_id": run, "report_sequence": int64(7), "event_id": pub.EventID}}}}
	rows := []store.BotTranscriptEvent{{Cursor: 1, Event: &session.Event{ID: "user", Author: "user", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Check it", "user")}}}, {Cursor: 2, Event: private}, {Cursor: 3, Event: employee}}
	var live []SessionEventProjection
	for mode := 0; mode < 2; mode++ {
		p := &SessionEventProjector{BotID: id, HomeSessionID: home, SessionID: home, Repository: repo}
		var restored []SessionEventProjection
		for _, row := range rows {
			projection, err := p.ProjectBot(t.Context(), row, nil)
			if err != nil {
				t.Fatal(err)
			}
			if projection.Cursor != row.Cursor || projection.EventID != row.Event.ID {
				t.Fatal("cursor identity lost")
			}
			restored = append(restored, projection)
		}
		if len(restored[0].Events) != 1 || len(restored[1].Events) != 0 || len(restored[2].Events) != 1 || restored[2].Events[0].Data["text"] != text {
			t.Fatalf("wrong voices: %+v", restored)
		}
		if mode == 0 {
			live = restored
		} else if !reflect.DeepEqual(live, restored) {
			t.Fatal("live/restore differ")
		}
	}
	// A reconnect starts at the exact original row, without needing a seen-text set.
	p := &SessionEventProjector{BotID: id, HomeSessionID: home, SessionID: home, Repository: repo}
	projection, err := p.ProjectBot(t.Context(), rows[2], nil)
	if err != nil || len(projection.Events) != 1 {
		t.Fatal("source-resume lost speech", err)
	}
	employee.Content = genai.NewContentFromText(text+" altered", "model")
	if _, err := p.ProjectBot(t.Context(), rows[2], nil); err == nil {
		t.Fatal("forged publication text accepted")
	}
}
