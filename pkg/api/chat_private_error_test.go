package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestChatPrivateFailurePublicBoundary(t *testing.T) {
	const secret = "provider_private_token endpoint=https://internal.example.invalid/private?token=do-not-display"
	cr := newChatRunner("session", "user", studioChatAppName, false)
	defer cr.cancel()
	var persisted ChatEvent
	cr.SetEventSink(func(_ context.Context, e ChatEvent) error { persisted = e; return nil })
	cr.emitEvent("error", map[string]any{"error": secret, "private": secret})
	for _, e := range []ChatEvent{persisted, cr.GetHistory()[0]} {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), secret) {
			t.Fatal("private diagnostics entered public delivery", string(raw))
		}
		if e.Data["error"] == "" {
			t.Fatal("failure indication lost")
		}
	}
	if !strings.Contains(cr.Completion().Err.Error(), secret) {
		t.Fatal("trusted failure evidence lost")
	}
	cr.emitEvent("error_info", map[string]any{"title": "Provider rejected request", "reason": secret, "originalError": secret})
	raw, _ := json.Marshal(cr.GetHistory())
	if strings.Contains(string(raw), secret) {
		t.Fatal("structured private diagnostics leaked")
	}
}

func TestChatPrivateFailureLegacyHistory(t *testing.T) {
	const secret = "provider_private_token endpoint=https://internal.example.invalid/private"
	e := &session.Event{ID: "error-123", Author: "model", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("[Error: "+secret+"]", "model")}}
	before, _ := json.Marshal(e)
	messages := eventsToMessages(linkedBotSessionEvents{e}, nil)
	raw, _ := json.Marshal(messages)
	if strings.Contains(string(raw), secret) || len(messages) != 1 || messages[0].Content == "" {
		t.Fatal("legacy Chat failure unsafe", string(raw))
	}
	p := &SessionEventProjector{BotID: uuid.New(), SessionID: uuid.New()}
	out, err := p.ProjectBot(t.Context(), store.BotTranscriptEvent{Event: e}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 0 || strings.Contains(string(out.Transcript), secret) {
		t.Fatal("legacy Bot synthetic failure escaped committed report", string(out.Transcript))
	}
	after, _ := json.Marshal(e)
	if string(before) != string(after) {
		t.Fatal("read projection rewrote trusted history")
	}
}

func TestChatPrivateFailureRuntimeHistory(t *testing.T) {
	for _, kind := range []string{"error", "error_info"} {
		runtime := bot.RuntimeEvent{Type: kind, Payload: json.RawMessage(`{"error":"PRIVATE_PROVIDER","message":"PRIVATE_PROVIDER","cause":"PRIVATE_PROVIDER","originalError":"PRIVATE_PROVIDER"}`)}
		got, ok := publicBotRuntime(runtime)
		if !ok || strings.Contains(string(got.Payload), "PRIVATE_PROVIDER") || !strings.Contains(string(got.Payload), "operational failure") {
			t.Fatalf("unsafe historical runtime error: %s", got.Payload)
		}
	}
}

func TestChatPrivateFailureOrdinaryRun(t *testing.T) {
	const secret = "provider_private_token endpoint=https://internal.example.invalid/private"
	env := setupIntegrationTest(t, NewMockLLM(ErrorTurn("INTERNAL", secret)), nil)
	events := runAndCollect(t, env, "Do the work")
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), secret) || !strings.Contains(string(raw), publicChatFailure) {
		t.Fatal("unsafe ordinary Chat stream", string(raw))
	}
	got, err := env.SessionService.Get(t.Context(), &session.GetRequest{AppName: studioChatAppName, UserID: studioChatUserID, SessionID: env.Runner.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(eventsToMessages(got.Session.Events(), nil))
	if strings.Contains(string(raw), secret) || !strings.Contains(string(raw), publicChatFailure) {
		t.Fatal("unsafe ordinary Chat restore", string(raw))
	}
	if !strings.Contains(env.Runner.Completion().Err.Error(), secret) {
		t.Fatal("lost trusted failure")
	}
}

func TestChatPrivateFailureSafeVocabularyParity(t *testing.T) {
	for _, text := range []string{publicChatEmptyResponse, publicChatTruncatedStream, "rate_limit PRIVATE_TOKEN", "One optional source was unavailable.", "Failed PRIVATE_TOKEN"} {
		t.Run(text, func(t *testing.T) {
			live := publicChatErrorData("error", map[string]any{"error": text, "private": "PRIVATE_TOKEN"})
			raw, _ := json.Marshal(map[string]any{"error": text})
			restored, ok := publicBotRuntime(bot.RuntimeEvent{Type: "error", Payload: raw})
			want, _ := json.Marshal(live)
			if !ok || string(restored.Payload) != string(want) || strings.Contains(string(want), "PRIVATE_TOKEN") {
				t.Fatalf("live/history privacy mismatch: %s / %s", want, restored.Payload)
			}
			legacy := &session.Event{ID: "error-123", Author: "model", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("[Error: "+text+"]", "model")}}
			messages := eventsToMessages(linkedBotSessionEvents{legacy}, nil)
			if len(messages) != 1 || messages[0].Content != live["error"] {
				t.Fatalf("ordinary restore mismatch: %+v", messages)
			}
		})
	}
	for _, reason := range []string{"Failed after 2 attempts.", "Failed after 3 attempts. The error persisted across all retry attempts.", "Failed after 3 attempts. PRIVATE_TOKEN"} {
		data := map[string]any{"title": "Max Retries Exceeded", "reason": reason, "originalError": "PRIVATE_TOKEN"}
		live := publicChatErrorData("error_info", data)
		raw, _ := json.Marshal(data)
		restored, ok := publicBotRuntime(bot.RuntimeEvent{Type: "error_info", Payload: raw})
		want, _ := json.Marshal(live)
		if !ok || string(restored.Payload) != string(want) || strings.Contains(string(want), "PRIVATE_TOKEN") {
			t.Fatalf("structured live/history mismatch: %s / %s", want, restored.Payload)
		}
	}
}

func TestPersistRunErrorPublicText(t *testing.T) {
	svc := session.InMemoryService()
	created, err := svc.Create(t.Context(), &session.CreateRequest{AppName: studioChatAppName, UserID: "user", SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	persistRunError(t.Context(), svc, "user", created.Session.ID(), studioChatAppName, errors.New("provider_private_token"))
	got, err := svc.Get(t.Context(), &session.GetRequest{AppName: studioChatAppName, UserID: "user", SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	messages := eventsToMessages(got.Session.Events(), nil)
	raw, _ := json.Marshal(messages)
	if len(messages) != 1 || strings.Contains(string(raw), "provider_private_token") {
		t.Fatal("persisted private failure", string(raw))
	}
}
