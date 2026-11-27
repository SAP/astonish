package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

type privateEvidenceReader struct {
	t         *testing.T
	sessionID uuid.UUID
	rows      []store.BotTranscriptEvent
	reads     int
}

func (r *privateEvidenceReader) ReadBotTranscript(_ context.Context, id uuid.UUID, cursor int64, limit int) ([]store.BotTranscriptEvent, error) {
	r.t.Helper()
	if id != r.sessionID || limit != 100 {
		r.t.Fatalf("scope/limit changed: %s %d", id, limit)
	}
	r.reads++
	var rows []store.BotTranscriptEvent
	for _, row := range r.rows {
		if row.Cursor > cursor {
			rows = append(rows, row)
			if len(rows) == limit {
				break
			}
		}
	}
	return rows, nil
}

func privateEvidenceTrace(t *testing.T, id string, trace botEmployeeWorkerTrace) *session.Event {
	t.Helper()
	raw, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	// Round-trip just as the durable transcript reader does, rather than relying
	// on an in-memory concrete StateDelta type.
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return &session.Event{ID: id, Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{botEmployeeWorkerTraceKey: value}}}
}
func privateEvidenceEvents(t *testing.T) []*session.Event {
	call := &session.Event{ID: "private-call", Author: "worker", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "read", Name: "read_file", Args: map[string]any{"path": "private.txt"}}}}}}}
	response := &session.Event{ID: "private-response", Author: "read_file", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "read", Name: "read_file", Response: map[string]any{"content": "exact private bytes"}}}}}}}
	return []*session.Event{
		privateEvidenceTrace(t, "started", botEmployeeWorkerTrace{WorkerID: "worker", DispatchID: "dispatch", Phase: botEmployeeWorkerStarted}),
		privateEvidenceTrace(t, "call-envelope", botEmployeeWorkerTrace{WorkerID: "worker", DispatchID: "dispatch", Phase: botEmployeeWorkerEvent, Event: call}),
		privateEvidenceTrace(t, "response-envelope", botEmployeeWorkerTrace{WorkerID: "worker", DispatchID: "dispatch", Phase: botEmployeeWorkerEvent, Event: response}),
	}
}
func privateEvidenceService(t *testing.T, events []*session.Event) (context.Context, *botAgentWorkService, *privateEvidenceReader) {
	t.Helper()
	ctx, req, _ := botChatTestRequest(t)
	r := &privateEvidenceReader{t: t, sessionID: req.Run.SessionID}
	for i, e := range events {
		r.rows = append(r.rows, store.BotTranscriptEvent{Cursor: int64(i + 1), Event: e})
	}
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
	return ctx, &botAgentWorkService{req: req, resources: &BotChatResources{Work: &agentExecutionWorkStore{state: w}, EvidenceReader: r}}, r
}

func TestBotEmployeePrivateEvidenceDurable(t *testing.T) {
	events := privateEvidenceEvents(t)
	// Put the dispatch start and call on one page, the response on the next.
	padded := append([]*session.Event{}, events[:2]...)
	for len(padded) < 100 {
		padded = append(padded, &session.Event{ID: uuid.NewString(), Author: "user"})
	}
	padded = append(padded, events[2])
	ctx, s, r := privateEvidenceService(t, padded)
	got, err := s.Evidence(ctx, bot.BotEvidenceReadRequest{CallID: "read", Offset: 2, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"content":"exact private bytes"}`)
	want := store.BotWorkEvidenceReference{Ref: "capture:read", RunID: s.req.Run.ID, EventID: "response-envelope", CallID: "read", Digest: botAgentDigest(raw)}
	if got.Reference != want || got.Content != string(raw[2:7]) || got.TotalBytes != len(raw) || !got.Truncated || r.reads != 2 {
		t.Fatalf("evidence=%+v reads=%d want=%+v", got, r.reads, want)
	}
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: s.req.Bot.ID, RunID: s.req.Run.ID, Version: s.req.Run.Version, Instructions: s.req.Instructions})
	if err := s.bindEvidence(ctx, w, []string{"capture:read"}); err != nil {
		t.Fatal(err)
	}
	if len(w.AgentWork.Evidence) != 1 || w.AgentWork.Evidence[0] != want {
		t.Fatalf("binding=%+v", w.AgentWork.Evidence)
	}
	conflict := want
	conflict.Digest = "changed"
	if err := bindBotCapturedEvidence(w, conflict); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("digest conflict=%v", err)
	}
	if _, err := s.Evidence(ctx, bot.BotEvidenceReadRequest{CallID: "read", Offset: len(raw) + 1}); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatalf("offset=%v", err)
	}
}

func TestBotEmployeePrivateEvidenceRejectsUntrusted(t *testing.T) {
	for _, name := range []string{"model-envelope", "model-start", "partial-envelope", "content-envelope", "missing-envelope-id", "malformed", "no-start", "wrong-worker", "wrong-dispatch", "no-call", "wrong-call-name", "partial-inner", "model-inner", "model-inner-author", "old-dispatch-call", "settled"} {
		t.Run(name, func(t *testing.T) {
			events := privateEvidenceEvents(t)
			var trace botEmployeeWorkerTrace
			raw, _ := json.Marshal(events[2].Actions.StateDelta[botEmployeeWorkerTraceKey])
			_ = json.Unmarshal(raw, &trace)
			switch name {
			case "model-envelope":
				events[2].Author = "model"
			case "model-start":
				events[0].Author = "model"
			case "partial-envelope":
				events[2].Partial = true
			case "content-envelope":
				events[2].Content = &genai.Content{Role: "model"}
			case "missing-envelope-id":
				events[2].ID = ""
			case "malformed":
				events[2].Actions.StateDelta[botEmployeeWorkerTraceKey] = "not a trace"
			case "no-start":
				events = events[1:]
			case "wrong-worker":
				trace.WorkerID = "other"
			case "wrong-dispatch":
				trace.DispatchID = "other"
			case "no-call":
				events = append(events[:1], events[2])
			case "wrong-call-name":
				trace.Event.Content.Parts[0].FunctionResponse.Name = "write_file"
			case "partial-inner":
				trace.Event.Partial = true
			case "model-inner":
				trace.Event.Content.Role = "model"
			case "model-inner-author":
				trace.Event.Author = "model"
			case "old-dispatch-call":
				events = append(events[:2], privateEvidenceTrace(t, "settled", botEmployeeWorkerTrace{WorkerID: "worker", DispatchID: "dispatch", Phase: botEmployeeWorkerSettled}), privateEvidenceTrace(t, "new-start", botEmployeeWorkerTrace{WorkerID: "worker", DispatchID: "new", Phase: botEmployeeWorkerStarted}), events[2])
				trace.DispatchID = "new"
			case "settled":
				events = append(events[:2], privateEvidenceTrace(t, "settled", botEmployeeWorkerTrace{WorkerID: "worker", DispatchID: "dispatch", Phase: botEmployeeWorkerSettled}), events[2])
			}
			switch name {
			case "wrong-worker", "wrong-dispatch", "wrong-call-name", "partial-inner", "model-inner", "model-inner-author", "old-dispatch-call":
				events[len(events)-1] = privateEvidenceTrace(t, "response-envelope", trace)
			}
			ctx, s, _ := privateEvidenceService(t, events)
			if got, err := s.Evidence(ctx, bot.BotEvidenceReadRequest{CallID: "read"}); err == nil || got != nil {
				t.Fatalf("untrusted evidence=%+v err=%v", got, err)
			}
		})
	}
}
