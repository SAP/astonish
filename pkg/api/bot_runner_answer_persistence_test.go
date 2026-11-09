package api

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

type answerTranscriptCapture struct{ events []*session.Event }

func (s *answerTranscriptCapture) AppendBotRunEvent(_ context.Context, _ store.BotLease, _ uuid.UUID, e *session.Event) error {
	s.events = append(s.events, e)
	return nil
}

func TestBotConversationAnswerDoesNotReuseAdmissionID(t *testing.T) {
	for _, answerID := range []string{"", "bot-owner-answer:approval"} {
		t.Run(answerID, func(t *testing.T) {
			transcript := &answerTranscriptCapture{}
			s := botConversationSessions{transcript: transcript, userEventID: "bot-message:run", answerEventID: answerID}
			wrapped := &botConversationSession{}
			hidden := &session.Event{ID: "context-id", Author: "user", Actions: session.EventActions{StateDelta: map[string]any{"_astonish_turn_context": true}}, LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("internal context", "user")}}
			input := &session.Event{ID: "adk-input", Author: "user", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(`{"type":"bot_owner_answer","response":{"question_id":"q","answer":"custom"}}`, "user")}}
			for _, e := range []*session.Event{hidden, input} {
				if err := s.AppendEvent(context.Background(), wrapped, e); err != nil {
					t.Fatal(err)
				}
			}
			want := s.userEventID
			if answerID != "" {
				want = answerID
			}
			if len(transcript.events) != 2 || transcript.events[0].ID != "context-id" || transcript.events[1].ID != want {
				t.Fatalf("input/context identity collision: %+v", transcript.events)
			}
			if s.userEventID != "bot-message:run" || input.ID != "adk-input" {
				t.Fatal("changed history boundary or source event")
			}
			if transcript.events[1].Content != input.Content {
				t.Fatal("changed structured model answer")
			}
		})
	}
}
