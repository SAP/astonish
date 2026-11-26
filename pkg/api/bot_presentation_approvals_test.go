package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

type presentationApprovalStore struct {
	*botSessionFixtureStore
	rows  []store.BotApproval
	pages []store.BotPage
	read  func(context.Context, store.BotPage) ([]store.BotApproval, error)
}

func (s *presentationApprovalStore) ListApprovals(ctx context.Context, _ store.BotID, p store.BotPage) ([]store.BotApproval, error) {
	s.pages = append(s.pages, p)
	if s.read != nil {
		return s.read(ctx, p)
	}
	last := uuid.Nil
	if p.After != "" {
		last = uuid.MustParse(p.After)
	}
	rows := []store.BotApproval{}
	for _, a := range s.rows {
		if bytes.Compare(a.ID[:], last[:]) > 0 && len(rows) < p.Limit {
			rows = append(rows, a)
		}
	}
	return rows, nil
}
func presentationApprovals(id uuid.UUID, count int) []store.BotApproval {
	rows := []store.BotApproval{}
	statuses := []store.ApprovalStatus{"pending", "approved", "denied", "consumed", "expired"}
	for i := 1; i <= count; i++ {
		rows = append(rows, store.BotApproval{ID: uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8000-%012x", i)), BotID: id, Status: statuses[i%len(statuses)]})
	}
	return rows
}
func TestBotPresentationApprovalsPages(t *testing.T) {
	for _, count := range []int{0, 8, 200, 201, 400} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			id := uuid.New()
			repo := &presentationApprovalStore{rows: presentationApprovals(id, count)}
			got, err := listBotPresentationApprovals(t.Context(), repo, id)
			if err != nil || !reflect.DeepEqual(got, repo.rows) {
				t.Fatalf("count=%d rows=%d err=%v", count, len(got), err)
			}
			if len(repo.pages) != count/200+1 {
				t.Fatal(repo.pages)
			}
			for i, p := range repo.pages {
				if p.Limit != 200 || (i > 0 && p.After != repo.rows[i*200-1].ID.String()) {
					t.Fatal(repo.pages)
				}
			}
		})
	}
}
func TestBotPresentationApprovalsInvalidAndAtomic(t *testing.T) {
	for _, kind := range []string{"wrong-bot", "zero", "descending", "cycle", "oversize", "storage", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			id := uuid.New()
			rows := presentationApprovals(id, 200)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sentinel := errors.New("storage")
			want := store.ErrBotInvalid
			repo := &presentationApprovalStore{}
			repo.read = func(_ context.Context, p store.BotPage) ([]store.BotApproval, error) {
				if p.After == "" {
					return rows, nil
				}
				switch kind {
				case "wrong-bot":
					a := presentationApprovals(uuid.New(), 201)[200]
					return []store.BotApproval{a}, nil
				case "zero":
					return []store.BotApproval{{BotID: id}}, nil
				case "descending":
					return []store.BotApproval{rows[10], rows[9]}, nil
				case "cycle":
					return rows, nil
				case "oversize":
					return presentationApprovals(id, 201), nil
				case "storage":
					return nil, sentinel
				case "cancel":
					cancel()
					return nil, nil
				}
				panic(kind)
			}
			if kind == "storage" {
				want = sentinel
			}
			if kind == "cancel" {
				want = context.Canceled
			}
			got, err := listBotPresentationApprovals(ctx, repo, id)
			if got != nil || !errors.Is(err, want) {
				t.Fatalf("partial result %d err=%v want=%v", len(got), err, want)
			}
		})
	}
}

// Last review deliberately lies beyond both the live eight-row and history
// 200-row boundaries. Its persisted response must remain available on reload.
func presentationReviewFixture(t *testing.T) (*presentationApprovalStore, *canonicalPresentationRows) {
	t.Helper()
	owner, id, sid, rid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repo := &presentationApprovalStore{botSessionFixtureStore: &botSessionFixtureStore{b: store.Bot{ID: id, OwnerID: owner, Scope: store.BotScopePersonal, HomeSessionID: sid}, run: store.BotRun{ID: rid, BotID: id, SessionID: sid}}, rows: presentationApprovals(id, 201)}
	a := &repo.rows[200]
	a.RunID = rid
	a.Status = "consumed"
	a.Interaction = &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "late-question", Payload: json.RawMessage(`{"questionId":"late-question","kind":"text","prompt":"Late saved question?"}`)}
	a.Response = &store.BotInteractionResponse{QuestionID: "late-question", Answer: "Saved late answer"}
	identity := uuid.NewSHA1(a.ID, []byte("bot-review:approval"))
	payload, _ := json.Marshal(map[string]any{"kind": "approval", "review_id": a.ID})
	event := &adksession.Event{ID: identity.String(), InvocationID: identity.String(), Author: "bot_runtime", Actions: adksession.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{SessionID: sid, RunID: rid, TurnID: identity, Sequence: 1, Type: "bot_review", Payload: payload}}}}
	if err := a.Interaction.Validate(); err != nil {
		t.Fatalf("invalid question fixture: %v", err)
	}
	ss := &canonicalPresentationRows{botSessionMetaFixture: &botSessionMetaFixture{meta: &store.SessionMeta{ID: sid.String(), UserID: owner.String(), BotID: id.String(), Purpose: store.SessionPurposeBotHome, Retention: store.SessionRetentionBot}}, rows: []store.BotTranscriptEvent{{Cursor: 1, Event: event}}}
	return repo, ss
}
