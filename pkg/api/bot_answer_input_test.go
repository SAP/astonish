package api

import (
	"context"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotAnswerInputViewKeepsDurableHistory(t *testing.T) {
	ctx := context.Background()
	service := session.InMemoryService()
	saved, err := service.Create(ctx, &session.CreateRequest{AppName: "test", UserID: "owner", SessionID: "answer-view"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"question", "bot-owner-answer:accepted", "other"} {
		if err := service.AppendEvent(ctx, saved.Session, &session.Event{ID: id, Author: "user", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(id, "user")}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := service.Get(ctx, &session.GetRequest{AppName: "test", UserID: "owner", SessionID: "answer-view"})
	if err != nil {
		t.Fatal(err)
	}
	view := botAnswerInputSession{Session: got.Session, answerID: "bot-owner-answer:accepted"}
	if view.Events().Len() != 2 || view.Events().At(0).ID != "question" || view.Events().At(1).ID != "other" || got.Session.Events().Len() != 3 {
		t.Fatal("producer view rewrote durable history or hid unrelated input")
	}
}

func TestBotAnswerInputViewForwardsStateToNativeSession(t *testing.T) {
	ctx := context.Background()
	service := session.InMemoryService()
	_, err := service.Create(ctx, &session.CreateRequest{AppName: "test", UserID: "owner", SessionID: "answer-state"})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := botConversationSessions{Service: service, answerEventID: "bot-owner-answer:accepted"}
	got, err := wrapped.Get(ctx, &session.GetRequest{AppName: "test", UserID: "owner", SessionID: "answer-state"})
	if err != nil {
		t.Fatal(err)
	}
	event := &session.Event{ID: "state-only", Actions: session.EventActions{StateDelta: map[string]any{"resumed": true}}}
	if err := wrapped.AppendEvent(ctx, got.Session, event); err != nil {
		t.Fatal(err)
	}
	native, err := service.Get(ctx, &session.GetRequest{AppName: "test", UserID: "owner", SessionID: "answer-state"})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := native.Session.State().Get("resumed"); err != nil || value != true {
		t.Fatalf("state was not forwarded to the native session: %v %v", value, err)
	}
}
