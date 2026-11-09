package entstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func messageReviewReader(repo *botRepository) store.BotTranscriptReader {
	switch d := repo.db.(type) {
	case personalBotDB:
		return &personalSessionStore{client: d.client}
	case teamBotDB:
		return &teamSessionStore{client: d.client}
	}
	panic("unsupported fixture")
}

func messageReviewDraft(t *testing.T, scope store.BotScope, statuses ...store.BotStatus) (*botRepository, context.Context, *store.Bot) {
	t.Helper()
	repo, _, ctx, initial, _ := runtimeFixture(t, scope)
	if len(statuses) > 0 && statuses[0] != store.BotDraft {
		b, err := repo.Get(ctx, initial.BotID)
		if err != nil {
			t.Fatal(err)
		}
		if statuses[0] == store.BotPaused {
			b, err = repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "review paused conversation"})
			if err != nil {
				t.Fatal(err)
			}
		}
		return repo, ctx, b
	}
	p, _ := execution.PrincipalFromContext(ctx)
	v := store.DefaultBotVersion()
	v.Charter = "Review durable chat"
	b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Review", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "message-review"})
	if err != nil {
		t.Fatal(err)
	}
	return repo, ctx, b
}

func TestBotMessageReviewProjectionAndPublication(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, status := range []store.BotStatus{store.BotDraft, store.BotPaused, store.BotActive} {
			t.Run(string(scope)+"/"+string(status), func(t *testing.T) {
				repo, ctx, b := messageReviewDraft(t, scope, status)
				reader := messageReviewReader(repo)
				q := store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "first", Instructions: "First prompt", Attachments: []store.ChatFile{{Filename: "note.txt", MimeType: "text/plain", Data: []byte("attachment")}}}
				first, err := repo.AdmitMessage(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				rows, err := reader.ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
				rows = separateRunStates(t, rows, 1)
				if err != nil || len(rows) != 1 || rows[0].Event.ID != first.UserEventID || rows[0].Cursor != first.HistoryCursor {
					t.Fatalf("immediate home visibility: %+v %v", rows, err)
				}
				if len(rows[0].Event.Content.Parts) != 2 || string(rows[0].Event.Content.Parts[1].InlineData.Data) != "attachment" {
					t.Fatal("attachment missing")
				}
				user := rows[0].Event
				q.IdempotencyKey = "second"
				q.Instructions = "Future prompt"
				second, err := repo.AdmitMessage(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				if second.HistoryCursor <= first.HistoryCursor {
					t.Fatal("admission highwater not monotonic")
				}
				p, _ := execution.PrincipalFromContext(ctx)
				for _, mutate := range []func(*execution.Principal){func(p *execution.Principal) { p.Subject = uuid.NewString() }, func(p *execution.Principal) { p.TeamSlug = "other" },
					func(p *execution.Principal) { p.OrgSlug = "other" }} {
					foreign := p
					mutate(&foreign)
					if scope == store.BotScopePersonal && foreign.TeamSlug != p.TeamSlug {
						continue // Personal scope is bound to owner and organization, not a team.
					}
					denied, err := execution.WithPrincipal(context.Background(), foreign)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = repo.AdmitMessage(denied, q); !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("foreign replay: %v", err)
					}
				}
				worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: first.Run.ID, Version: b.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
				lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: first.Run.ID, WorkerID: "review", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				reply := &session.Event{ID: uuid.NewString(), Author: "assistant", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Actual answer", "model")}}
				trace := &session.Event{ID: uuid.NewString(), Author: "runtime"}
				for i := 0; i < 2; i++ {
					for _, e := range []*session.Event{user, reply, trace} {
						if err := repo.appendBotRunEvent(worker, lease, first.Run.SessionID, e); err != nil {
							t.Fatal(err)
						}
					}
				}
				home, err := reader.ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
				home = separateRunStates(t, home, 3)
				if err != nil || len(home) != 3 {
					t.Fatalf("home duplicated or trace leaked: %d %v", len(home), err)
				}
				run, err := reader.ReadBotTranscript(ctx, first.Run.SessionID, 0, 200)
				run = separateRunStates(t, run, 2)
				if err != nil || len(run) != 3 {
					t.Fatalf("independent run trace: %d %v", len(run), err)
				}
				replay, err := reader.ReadBotTranscript(ctx, b.HomeSessionID, second.HistoryCursor, 200)
				replay = separateRunStates(t, replay, 2)
				if err != nil || len(replay) != 1 || replay[0].Event.ID != reply.ID {
					t.Fatalf("reconnect replay: %+v %v", replay, err)
				}
				if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "Actual answer"}}); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					pub, err := repo.PublishReport(ctx, store.BotPublicationRequest{BotID: b.ID, RunID: first.Run.ID})
					if err != nil || pub.EventID != reply.ID {
						t.Fatalf("interactive publication: %+v %v", pub, err)
					}
				}
				home, err = reader.ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
				home = separateRunStates(t, home, 4)
				if err != nil || len(home) != 3 {
					t.Fatalf("publication duplicated reply: %d %v", len(home), err)
				}
				saved, err := repo.GetRun(ctx, b.ID, first.Run.ID)
				if err != nil || saved.HistoryCursor != first.HistoryCursor || saved.UserEventID != first.UserEventID {
					t.Fatalf("lost durable metadata: %+v %v", saved, err)
				}
			})
		}
	}
}

func TestBotMessageReviewConcurrentAdmission(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, b := messageReviewDraft(t, scope)
			q := store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "race", Instructions: "Exactly once"}
			results := make(chan *store.BotMessageReceipt, 8)
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					var receipt *store.BotMessageReceipt
					var err error
					for attempt := 0; attempt < 80; attempt++ {
						receipt, err = repo.AdmitMessage(ctx, q)
						var busy interface{ Code() int }
						if err == nil || !errors.As(err, &busy) || busy.Code()&255 != 5 {
							break
						}
						time.Sleep(5 * time.Millisecond)
					}
					if err != nil {
						t.Errorf("admission: %v", err)
						return
					}
					results <- receipt
				}()
			}
			wg.Wait()
			close(results)
			var first *store.BotMessageReceipt
			for r := range results {
				if first == nil {
					first = r
				}
				if r.Run.ID != first.Run.ID || r.HistoryCursor != first.HistoryCursor || r.UserEventID != first.UserEventID {
					t.Error("race returned different receipts")
				}
			}
			rows, err := messageReviewReader(repo).ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
			rows = separateRunStates(t, rows, 1)
			if err != nil || len(rows) != 1 {
				t.Fatalf("race home rows=%d %v", len(rows), err)
			}
			runs, err := repo.ListRuns(ctx, b.ID, store.BotPage{Limit: 20})
			if err != nil || len(runs) != 1 {
				t.Fatalf("race runs=%d %v", len(runs), err)
			}
		})
	}
}

var errMessageReviewRollback = errors.New("injected run insertion failure")

type messageReviewFailDB struct{ botDraftDB }
type messageReviewFailTx struct {
	botDraftDB
	botMessageDB
}

func (d messageReviewFailDB) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return fn(messageReviewFailTx{tx, tx.(botMessageDB)}) })
}
func (d messageReviewFailTx) InsertRun(context.Context, botStoredRun, uuid.UUID) error {
	return errMessageReviewRollback
}
func TestBotMessageReviewAdmissionRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, b := messageReviewDraft(t, scope)
			original := repo.db
			repo.db = messageReviewFailDB{original}
			q := store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "rollback", Instructions: "Do not expose uncommitted prompt"}
			if receipt, err := repo.AdmitMessage(ctx, q); !errors.Is(err, errMessageReviewRollback) || receipt != nil {
				t.Fatalf("rollback receipt=%+v err=%v", receipt, err)
			}
			repo.db = original
			rows, err := messageReviewReader(repo).ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
			if err != nil || len(rows) != 0 {
				t.Fatalf("rolled back user visible: %d %v", len(rows), err)
			}
			if _, err := repo.AdmitMessage(ctx, q); err != nil {
				t.Fatalf("rollback poisoned idempotency: %v", err)
			}
		})
	}
}
