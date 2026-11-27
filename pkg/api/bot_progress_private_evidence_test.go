package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func privateProgressFixture(t *testing.T) (*botProgressSessions, session.Session) {
	t.Helper()
	base := session.InMemoryService()
	created, err := base.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "owner", SessionID: "run"})
	if err != nil {
		t.Fatal(err)
	}
	return &botProgressSessions{Service: base, current: created.Session, app: "app", user: "owner", id: "run"}, created.Session
}

func TestBotProgressPrivateEvidenceIncremental(t *testing.T) {
	s, current := privateProgressFixture(t)
	events := privateEvidenceEvents(t)
	appendEvent := func(e *session.Event) {
		t.Helper()
		if err := s.AppendEvent(t.Context(), current, e); err != nil {
			t.Fatal(err)
		}
	}
	// Snapshot between dispatch, call and response: binding state must survive the cursor.
	for _, e := range events[:2] {
		appendEvent(e)
		if _, _, err := s.snapshot(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	prose := &session.Event{Author: "user", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("PRIVATE OWNER SPEECH", "user")}}
	appendEvent(privateEvidenceTrace(t, "prose", botEmployeeWorkerTrace{WorkerID: "worker", DispatchID: "dispatch", Phase: botEmployeeWorkerEvent, Event: prose}))
	appendEvent(events[2])
	got, n, err := s.snapshot(t.Context())
	if err != nil || n != 4 || got.Events().Len() != 1 {
		t.Fatalf("private response not observed: n=%d events=%d err=%v", n, got.Events().Len(), err)
	}
	_, progress, _, _, _ := progressContextFixture(t)
	observation, observeErr := progress.observeContext(t.Context(), &BotChatResources{}, got)
	observed, _ := json.Marshal(observation)
	if observeErr != nil || len(observation.EvidenceRefs) != 1 || observation.EvidenceRefs[0] != "tool-response:read" || !strings.Contains(string(observed), "exact private bytes") || strings.Contains(string(observed), "PRIVATE OWNER SPEECH") {
		t.Fatalf("observer lacks actual private evidence: %s err=%v", observed, observeErr)
	}
	row := got.Events().At(0)
	raw, _ := json.Marshal(row)
	if row.ID != "response-envelope" || !strings.Contains(string(raw), "exact private bytes") || strings.Contains(string(raw), "PRIVATE OWNER SPEECH") || strings.Contains(string(raw), "private-response") || len(s.owners) != 0 {
		t.Fatalf("evidence identity/privacy: %s", raw)
	}
	rows, available, err := s.promptEvidence(t.Context(), 20)
	if err != nil || !available || len(rows) != 1 || rows[0].Event.ID != "response-envelope" {
		t.Fatalf("prompt missing evidence: %+v %v", rows, err)
	}
	response, err := s.currentResponse(t.Context(), "read")
	if err != nil || response == nil || response.Event.ID != "response-envelope" {
		t.Fatalf("current response missing: %+v %v", response, err)
	}
	// The internal evidence reader must still bind the outer persisted identity.
	ctx, work, reader := privateEvidenceService(t, nil)
	s.app, s.user, s.id = "app", "owner", "run"
	work.resources.SessionService = s
	evidence, err := work.Evidence(ctx, bot.BotEvidenceReadRequest{CallID: "read"})
	if err != nil || evidence.Reference.EventID != "response-envelope" || reader.reads != 0 {
		t.Fatalf("current evidence binding: %+v %v reads=%d", evidence, err, reader.reads)
	}
	// Explicit durable Get resets the incremental validator; replaying still finds evidence.
	if _, err := s.Get(t.Context(), &session.GetRequest{AppName: "app", UserID: "owner", SessionID: "run"}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := s.snapshot(t.Context()); err != nil || got.Events().Len() != 1 {
		t.Fatalf("reset loses private evidence: %v", err)
	}
}

func TestBotProgressPrivateEvidenceRejectsUntrusted(t *testing.T) {
	for _, name := range []string{"forged-author", "partial", "wrong-name", "stale-dispatch", "prior-home"} {
		t.Run(name, func(t *testing.T) {
			s, current := privateProgressFixture(t)
			events := privateEvidenceEvents(t)
			switch name {
			case "forged-author":
				events[2].Author = "model"
			case "partial":
				events[2].Partial = true
			case "wrong-name", "stale-dispatch":
				raw, _ := json.Marshal(events[2].Actions.StateDelta[botEmployeeWorkerTraceKey])
				var trace botEmployeeWorkerTrace
				_ = json.Unmarshal(raw, &trace)
				if name == "wrong-name" {
					trace.Event.Content.Parts[0].FunctionResponse.Name = "write_file"
				} else {
					trace.DispatchID = "new"
					events = append(events[:2], privateEvidenceTrace(t, "settled", botEmployeeWorkerTrace{WorkerID: "worker", DispatchID: "dispatch", Phase: botEmployeeWorkerSettled}), privateEvidenceTrace(t, "new", botEmployeeWorkerTrace{WorkerID: "worker", DispatchID: "new", Phase: botEmployeeWorkerStarted}), events[2])
				}
				events[len(events)-1] = privateEvidenceTrace(t, "response-envelope", trace)
			case "prior-home":
				s.current = &botConversationSession{Session: current, prior: events}
				events = nil
			}
			for _, e := range events {
				if err := s.AppendEvent(t.Context(), current, e); err != nil {
					t.Fatal(err)
				}
			}
			rows, _, err := s.promptEvidence(t.Context(), 20)
			if err != nil || len(rows) != 0 {
				t.Fatalf("untrusted prompt evidence: %+v %v", rows, err)
			}
			response, err := s.currentResponse(t.Context(), "read")
			if err != nil || response != nil {
				t.Fatalf("untrusted current evidence: %+v %v", response, err)
			}
			// Progress can include owner history, but never forge private responses.
			got, _, err := s.snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for e := range got.Events().All() {
				for _, p := range e.Content.Parts {
					if p.FunctionResponse != nil {
						t.Fatal("untrusted observer response")
					}
				}
			}
		})
	}
}

func TestBotProgressPrivateEvidenceBoundedOrdering(t *testing.T) {
	s, current := privateProgressFixture(t)
	start := privateEvidenceTrace(t, "start", botEmployeeWorkerTrace{WorkerID: "worker", DispatchID: "dispatch", Phase: botEmployeeWorkerStarted})
	if err := s.AppendEvent(t.Context(), current, start); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 70; i++ {
		id := fmt.Sprintf("call-%02d", i)
		call := &session.Event{Author: "worker", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: id, Name: "read_file"}}}}}}
		response := &session.Event{Author: "read_file", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{Text: "PRIVATE PROSE"}, {FunctionResponse: &genai.FunctionResponse{ID: id, Name: "read_file", Response: map[string]any{"n": i}}}}}}}
		for j, e := range []*session.Event{call, response} {
			outer := privateEvidenceTrace(t, fmt.Sprintf("outer-%d-%d", i, j), botEmployeeWorkerTrace{WorkerID: "worker", DispatchID: "dispatch", Phase: botEmployeeWorkerEvent, Event: e})
			if err := s.AppendEvent(t.Context(), current, outer); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, _, err := s.snapshot(t.Context())
	if err != nil || got.Events().Len() != 64 {
		t.Fatalf("response bound: %d %v", got.Events().Len(), err)
	}
	rows, _, err := s.promptEvidence(t.Context(), 3)
	if err != nil || len(rows) != 3 {
		t.Fatalf("prompt bound: %d %v", len(rows), err)
	}
	for i, row := range rows {
		if row.Event.Content.Parts[0].FunctionResponse.ID != fmt.Sprintf("call-%02d", i+67) {
			t.Fatal("prompt chronology changed")
		}
	}
	// Projection never edits the persisted envelopes or publishes their private prose.
	for e := range got.Events().All() {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), "PRIVATE PROSE") {
			t.Fatalf("private speech leaked: %s", raw)
		}
	}
	if current.Events().At(2).Content != nil || current.Events().At(2).Actions.StateDelta[botEmployeeWorkerTraceKey] == nil {
		t.Fatal("persisted envelope mutated")
	}
}
