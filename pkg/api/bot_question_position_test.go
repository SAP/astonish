package api

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotQuestionOriginalPositionLiveRestore(t *testing.T) {
	raw, err := os.ReadFile("testdata/boston-question-approvals.json")
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Approvals []store.BotApproval `json:"approvals"`
	}
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/boston-question-history.sse")
	if err != nil {
		t.Fatal(err)
	}
	var question *session.Event
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var e session.Event
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e) == nil && e.Author == "model" && e.Content != nil && len(e.Content.Parts) == 1 && tryParseChatQuestionMessage(e.Content.Parts[0].Text) != nil {
			question = &e
			break
		}
	}
	if question == nil {
		t.Fatal("missing original question fixture")
	}
	a := saved.Approvals[0]
	for _, status := range []store.ApprovalStatus{store.ApprovalPending, store.ApprovalApproved} {
		a.Status = status
		for _, mode := range []string{"live", "restore"} {
			t.Run(string(status)+"/"+mode, func(t *testing.T) {
				p := SessionEventProjector{BotID: a.BotID, HomeSessionID: uuid.New(), SessionID: uuid.New()}
				p.HomeSessionID = p.SessionID
				out, err := p.ProjectBot(context.Background(), store.BotTranscriptEvent{Cursor: 41646, Event: question}, []store.BotApproval{a})
				if err != nil {
					t.Fatal(err)
				}
				if out.Cursor != 41646 || out.EventID != question.ID || len(out.Events) != 1 || out.Events[0].Type != "bot_review" || out.Events[0].Data["reviewId"] != a.ID.String() {
					t.Fatalf("original question lost its approval-bound position: %+v", out)
				}
				if out.Events[0].Data["question"] != nil || out.Events[0].Data["eventId"] != question.ID || out.Events[0].Data["invocationId"] != question.InvocationID {
					t.Fatal("position reference copied authority or changed row identity")
				}
				// The run surface keeps Employee speech; Home was exercised above.
				p.HomeSessionID = uuid.New()
				identity := uuid.NewSHA1(a.ID, []byte("bot-review:approval"))
				payload, _ := json.Marshal(map[string]any{"kind": "approval", "review_id": a.ID})
				rows := []store.BotTranscriptEvent{
					{Cursor: 41646, Event: question},
					{Cursor: 41653, Event: &session.Event{ID: "owner-answer", Author: "user", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Selected news", "user")}}},
					{Cursor: 41665, Event: &session.Event{ID: "employee", Author: "assistant", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Employee acknowledgement", "model")}}},
					{Cursor: 41670, Event: &session.Event{ID: identity.String(), InvocationID: identity.String(), Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{Type: "bot_review", SessionID: p.SessionID, RunID: a.RunID, TurnID: identity, Sequence: 1, Payload: payload}}}}},
					{Cursor: 41680, Event: &session.Event{ID: "later-employee", Author: "assistant", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Later Employee", "model")}}},
				}
				for _, row := range rows {
					projected, err := p.ProjectBot(context.Background(), row, []store.BotApproval{a})
					if err != nil || projected.Cursor != row.Cursor || projected.EventID != row.Event.ID || len(projected.Events) != 1 {
						t.Fatalf("ordered %s row %d lost: events=%+v err=%v", mode, row.Cursor, projected.Events, err)
					}
				}
				if question.Content == nil || question.Author != "model" || question.Actions.StateDelta["bot_runtime_event"] != nil {
					t.Fatal("mutated persisted question")
				}
			})
		}
	}
	if projectBotQuestionTranscript(question, nil) != question {
		t.Fatal("question projected without approval")
	}
	unrelated := a
	interaction := *a.Interaction
	interaction.QuestionID = "unrelated-question"
	unrelated.Interaction = &interaction
	if projectBotQuestionTranscript(question, []store.BotApproval{unrelated}) != question {
		t.Fatal("question projected from a different identity")
	}
}
