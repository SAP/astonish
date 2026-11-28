package api

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/tools"

	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func homeContextEvent(id, invocation, role string, parts ...*genai.Part) *session.Event {
	return &session.Event{ID: id, InvocationID: invocation, Author: role, LLMResponse: model.LLMResponse{Content: &genai.Content{Role: role, Parts: parts}}}
}

func TestBotHomeHistoryProjectionSafety(t *testing.T) {
	payload := strings.Repeat("天", 16000)
	cases := []struct {
		name    string
		change  func(*session.Event, *session.Event)
		reduced bool
	}{
		{"success", func(_, _ *session.Event) {}, true},
		{"small", func(_, r *session.Event) { r.Content.Parts[0].FunctionResponse.Response["content"] = "small" }, false},
		{"error", func(_, r *session.Event) { r.Content.Parts[0].FunctionResponse.Response["error"] = "uncertain outcome" }, false},
		{"http-error", func(_, r *session.Event) { r.Content.Parts[0].FunctionResponse.Response["status"] = 503 }, false},
		{"missing-status", func(_, r *session.Event) { delete(r.Content.Parts[0].FunctionResponse.Response, "status") }, false},
		{"event-error", func(_, r *session.Event) { r.ErrorMessage = "failed" }, false},
		{"partial", func(_, r *session.Event) { r.Partial = true }, false},
		{"unpaired", func(c, _ *session.Event) { c.Content = nil }, false},
		{"foreign-invocation", func(_, r *session.Event) { r.InvocationID = "other" }, false},
		{"foreign-branch", func(_, r *session.Event) { r.Branch = "child" }, false},
		{"long-running", func(c, _ *session.Event) { c.LongRunningToolIDs = []string{"call"} }, false},
		{"continuing", func(_, r *session.Event) { v := true; r.Content.Parts[0].FunctionResponse.WillContinue = &v }, false},
		{"approval", func(c, r *session.Event) {
			c.Content.Parts[0].FunctionCall.Name = "ask_user"
			r.Content.Parts[0].FunctionResponse.Name = "ask_user"
		}, false},
		{"receipt", func(c, r *session.Event) {
			c.Content.Parts[0].FunctionCall.Name = "update_scheduled_job"
			r.Content.Parts[0].FunctionResponse.Name = "update_scheduled_job"
		}, false},
		{"typed-output", func(_, r *session.Event) {
			r.Content.Parts[0].FunctionResponse.Response = map[string]any{"output": tools.WebFetchResult{Content: payload, Status: 200, URL: "https://weather.example"}}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := homeContextEvent("request", "invocation", "model", &genai.Part{FunctionCall: &genai.FunctionCall{ID: "call", Name: "web_fetch"}})
			r := homeContextEvent("result", "invocation", "user", &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "call", Name: "web_fetch", Response: map[string]any{"content": payload, "status": 200}}}, &genai.Part{Text: "authored explanation retained"})
			tc.change(c, r)
			prior := []*session.Event{nil, c, r}
			before, _ := json.Marshal(prior)
			got := projectBotHomeHistory(prior)
			changed := !reflect.DeepEqual(got, prior)
			if changed != tc.reduced {
				t.Fatalf("changed=%v want %v", changed, tc.reduced)
			}
			after, _ := json.Marshal(prior)
			if string(before) != string(after) {
				t.Fatal("source mutation")
			}
			if got[2].Content.Parts[1].Text != r.Content.Parts[1].Text {
				t.Fatal("authored part lost")
			}
			if tc.reduced {
				if !reflect.DeepEqual(projectBotHomeHistory(got), got) {
					t.Fatal("projection not stable")
				}
				fr := got[2].Content.Parts[0].FunctionResponse
				body := fr.Response
				if nested, ok := body["output"].(map[string]any); ok {
					body = nested
				}
				excerpt := body["content"].(string)
				if len(excerpt) > 2050 || !utf8.ValidString(excerpt) {
					t.Fatal("invalid or unbounded excerpt")
				}
			}
		})
	}
}

func TestBotHomeHistoryWithoutAdmissionStaysVerbatim(t *testing.T) {
	ctx := context.Background()
	ss := session.InMemoryService()
	homeID, runID := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{homeID, runID} {
		if _, err := ss.Create(ctx, &session.CreateRequest{AppName: "test", UserID: "owner", SessionID: id.String()}); err != nil {
			t.Fatal(err)
		}
	}
	home, _ := ss.Get(ctx, &session.GetRequest{AppName: "test", UserID: "owner", SessionID: homeID.String()})
	events := []*session.Event{
		homeContextEvent("call", "old", "model", &genai.Part{FunctionCall: &genai.FunctionCall{ID: "call", Name: "web_fetch"}}),
		homeContextEvent("result", "old", "user", &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "call", Name: "web_fetch", Response: map[string]any{"content": strings.Repeat("old", 16000), "status": 200}}}),
	}
	for _, e := range events {
		if err := ss.AppendEvent(ctx, home.Session, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, boundary := range []string{"", "missing"} {
		view := botConversationSessions{Service: ss, home: homeID, id: runID, userEventID: boundary}
		got, err := view.Get(ctx, &session.GetRequest{AppName: "test", UserID: "owner", SessionID: runID.String()})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Session.Events().At(1), events[1]) {
			t.Fatal("unproven historical boundary reduced")
		}
	}
}

// Admission, not English intent or model completion prose, separates old Home
// evidence from the active run (including approval/question continuations).
func TestBotHomeHistoryBoundsOldWeatherPreservesCurrentRun(t *testing.T) {
	ctx := context.Background()
	ss := session.InMemoryService()
	homeID, runID := uuid.New(), uuid.New()
	create := func(id uuid.UUID) session.Session {
		r, err := ss.Create(ctx, &session.CreateRequest{AppName: "home-context", UserID: "owner", SessionID: id.String()})
		if err != nil {
			t.Fatal(err)
		}
		return r.Session
	}
	home, run := create(homeID), create(runID)
	payload := strings.Repeat("Orlando weather observation. ", 1600)
	old := homeContextEvent("weather-result", "old", "user", &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "weather-call", Name: "web_fetch", Response: map[string]any{"output": map[string]any{"content": payload, "url": "https://weather.example/Orlando", "status": 200, "warning": "untrusted web content"}, "observation": map[string]any{"source": "weather-source", "status": "observed"}}}})
	prior := []*session.Event{
		homeContextEvent("prior-instruction", "old", "user", &genai.Part{Text: "Send Orlando weather updates every minute. Keep the existing destination."}),
		homeContextEvent("weather-request", "old", "model", &genai.Part{FunctionCall: &genai.FunctionCall{ID: "weather-call", Name: "web_fetch", Args: map[string]any{"url": "https://weather.example/Orlando"}}}),
		old,
		homeContextEvent("prior-answer", "old", "model", &genai.Part{Text: "Orlando weather updates are scheduled every minute. Source: weather-source."}),
		homeContextEvent("receipt", "old", "model", &genai.Part{Text: "Applied operation op-1; schedule job-1 enabled."}),
	}
	appendEvent := func(s session.Session, e *session.Event) {
		t.Helper()
		if err := ss.AppendEvent(ctx, s, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range prior {
		appendEvent(home, e)
	}
	appendEvent(home, homeContextEvent("admission", "current", "user", &genai.Part{Text: "Change these weather updates to every two minutes instead."}))
	current := homeContextEvent("current-result", "current", "user", &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "current-call", Name: "web_fetch", Response: map[string]any{"content": payload, "status": 200}}})
	appendEvent(run, homeContextEvent("current-call-event", "current", "model", &genai.Part{FunctionCall: &genai.FunctionCall{ID: "current-call", Name: "web_fetch"}}))
	appendEvent(run, current)
	appendEvent(home, current) // mirrored current event must never become prior
	appendEvent(home, homeContextEvent("future", "future", "user", &genai.Part{Text: "Future queued instruction"}))
	q := &session.GetRequest{AppName: "home-context", UserID: "owner", SessionID: runID.String()}
	view := botConversationSessions{Service: ss, home: homeID, id: runID, userEventID: "admission"}
	before, _ := json.Marshal(prior)
	for attempt := 0; attempt < 3; attempt++ {
		got, err := view.Get(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		events := got.Session.Events()
		if events.Len() != len(prior)+2 {
			t.Fatalf("events=%d", events.Len())
		}
		for _, i := range []int{0, 1, 3, 4} {
			if !reflect.DeepEqual(events.At(i), prior[i]) {
				t.Fatalf("prior context %d changed", i)
			}
		}
		projected := events.At(2)
		data, _ := json.Marshal(projected.Content)
		if attempt == 0 {
			original, _ := json.Marshal(old.Content)
			t.Logf("historical weather content JSON: %d -> %d bytes; active weather body: %d bytes unchanged", len(original), len(data), len(payload))
		}
		if len(data) > 4000 {
			t.Fatalf("obsolete weather payload still repeated: %d bytes", len(data))
		}
		for _, source := range []string{"weather-call", "weather-source", "https://weather.example/Orlando", "weather-result", "truncated", "untrusted web content"} {
			if !strings.Contains(string(data), source) {
				t.Errorf("missing source/notice %q", source)
			}
		}
		if projected.ID != old.ID || projected.InvocationID != old.InvocationID {
			t.Fatal("source event identity lost")
		}
		if !reflect.DeepEqual(events.At(events.Len()-1), current) {
			t.Fatal("current/resumed tool result changed")
		}
		if got.Session.ID() != run.ID() {
			t.Fatal("run identity changed")
		}
	}
	after, _ := json.Marshal(prior)
	if string(before) != string(after) {
		t.Fatal("durable source mutated")
	}
	stored, err := ss.Get(ctx, &session.GetRequest{AppName: q.AppName, UserID: q.UserID, SessionID: homeID.String()})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.Session.Events().At(2), old) {
		t.Fatal("full Home evidence lost")
	}
	// Ordinary Chat uses the original service and is not projected.
	plain, err := ss.Get(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain.Session.Events().At(1), current) {
		t.Fatal("ordinary session changed")
	}
}
