package launcher

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBotAnswerDoesNotRenewAuthorityHTTP(t *testing.T) {
	for _, expired := range []bool{false, true} {
		id, aid, rid := uuid.New(), uuid.New(), uuid.New()
		answerCalls, continueCalls := 0, 0
		base := "/api/studio/bots/" + id.String()
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Query().Get("scope") != "team" {
				t.Errorf("method/scope %s %s", r.Method, r.URL)
			}
			switch r.URL.Path {
			case base + "/approvals/" + aid.String() + "/approve-and-continue":
				answerCalls++
				var q struct {
					Digest   string                        `json:"action_digest"`
					Response *store.BotInteractionResponse `json:"response"`
				}
				if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
					t.Error(err)
				}
				if q.Digest != "saved" || q.Response == nil || q.Response.QuestionID != "q" || q.Response.Answer != "no" {
					t.Errorf("answer binding %+v", q)
				}
				if expired {
					http.Error(w, "continuation required", http.StatusConflict)
					return
				}
				_ = json.NewEncoder(w).Encode(store.BotApproveContinueResult{RunID: rid, Continuation: store.BotContinuationQueued})
			case base + "/runs/" + rid.String() + "/continue-interactive":
				continueCalls++
				var q store.BotContinueInteractiveRequest
				if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
					t.Error(err)
				}
				if q.ExpectedRevision != 3 || q.IdempotencyKey == "" || q.Response != nil {
					t.Errorf("explicit continuation binding %+v", q)
				}
				_ = json.NewEncoder(w).Encode(store.BotRun{ID: rid})
			default:
				t.Errorf("unexpected request %s", r.URL)
				http.NotFound(w, r)
			}
		}))
		b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(s.URL)}, "team")
		b.selected = &store.Bot{ID: id, Revision: 3}
		a := store.BotApproval{ID: aid, BotID: id, RunID: rid, ActionDigest: "saved", Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"yesno","prompt":"Proceed?"}`)}}
		_, err := b.ApproveBotAndContinue(context.Background(), a, &store.BotInteractionResponse{QuestionID: "q", Answer: "no"})
		if (err != nil) != expired {
			t.Fatalf("expired=%v err=%v", expired, err)
		}
		if answerCalls != 1 || continueCalls != 0 {
			t.Fatalf("answer renewed authority: %d/%d", answerCalls, continueCalls)
		}
		if expired {
			_, err = b.ContinueBotInteractive(context.Background(), store.BotRun{ID: rid, BotID: id, ExecutionMode: execution.BotExecutionModeChatInteractive}, nil)
			if err != nil || continueCalls != 1 {
				t.Fatalf("explicit continue failed: %v calls=%d", err, continueCalls)
			}
		}
		s.Close()
	}
}
