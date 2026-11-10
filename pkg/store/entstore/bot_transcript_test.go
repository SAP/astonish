package entstore

import (
	"context"
	"errors"
	personalbot "github.com/SAP/astonish/ent/personal/bot"
	teambot "github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"sync"
	"testing"
	"time"
)

func TestBotTranscriptConcurrentCursor(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, ctx, b, lease, _ := botRunFixture(t, scope)
			run, err := repo.GetRun(ctx, b.BotID, lease.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var reader store.BotTranscriptReader
			switch d := repo.db.(type) {
			case teamBotDB:
				reader = &teamSessionStore{client: d.client}
			case personalBotDB:
				reader = &personalSessionStore{client: d.client}
			}
			e := &session.Event{ID: uuid.NewSHA1(run.ID, []byte("turn:1")).String(), Author: "bot_runtime"}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					// Fixture clients allow multiple SQLite writers (production uses
					// one). Retry only SQLite BUSY; any successful transaction must
					// remain idempotent, including retries after concurrent commits.
					var err error
					for attempt := 0; attempt < 40; attempt++ {
						err = repo.appendBotRunEvent(ctx, lease, run.SessionID, e)
						var busy interface{ Code() int }
						if err == nil || !errors.As(err, &busy) || busy.Code() != 5 {
							break
						}
						time.Sleep(time.Millisecond * 5)
					}
					if err != nil {
						t.Errorf("append: %v", err)
					}
				}()
			}
			wg.Wait()
			rows, err := reader.ReadBotTranscript(ctx, run.SessionID, 0, 200)
			rows = separateRunStates(t, rows, 2)
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
			cursor := rows[0].Cursor
			if rows[0].Event.ID != e.ID {
				t.Fatal("wrong event")
			}
			// Distinct concurrent events must all survive paginated replay, in
			// committed cursor order, without being mistaken for duplicate turns.
			want := make(map[string]bool)
			for i := 0; i < 8; i++ {
				next := &session.Event{ID: uuid.NewString(), Author: "bot_runtime"}
				want[next.ID] = true
				wg.Add(1)
				go func() {
					defer wg.Done()
					var err error
					for attempt := 0; attempt < 40; attempt++ {
						err = repo.appendBotRunEvent(ctx, lease, run.SessionID, next)
						var busy interface{ Code() int }
						if err == nil || !errors.As(err, &busy) || busy.Code() != 5 {
							break
						}
						time.Sleep(time.Millisecond * 5)
					}
					if err != nil {
						t.Errorf("append distinct: %v", err)
					}
				}()
			}
			wg.Wait()
			for i := 0; i < 8; i++ {
				rows, err = reader.ReadBotTranscript(ctx, run.SessionID, cursor, 1)
				if err != nil || len(rows) != 1 || rows[0].Cursor <= cursor {
					t.Fatalf("distinct replay=%+v err=%v", rows, err)
				}
				if !want[rows[0].Event.ID] {
					t.Fatalf("unexpected or repeated event %q", rows[0].Event.ID)
				}
				delete(want, rows[0].Event.ID)
				cursor = rows[0].Cursor
			}
			if len(want) != 0 {
				t.Fatalf("missing events: %v", want)
			}
			if err := repo.appendBotRunEvent(ctx, lease, run.SessionID, &session.Event{ID: uuid.NewString()}); err != nil {
				t.Fatal(err)
			}
			rows, err = reader.ReadBotTranscript(ctx, run.SessionID, cursor, 1)
			if err != nil || len(rows) != 1 || rows[0].Cursor <= cursor {
				t.Fatalf("replay=%+v %v", rows, err)
			}
			rows, err = reader.ReadBotTranscript(ctx, run.SessionID, rows[0].Cursor, 1)
			if err != nil || len(rows) != 0 {
				t.Fatal("cursor repeated event", err)
			}
		})
	}
}

func TestBotTranscriptAtomicFence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, worker, b, lease, _ := botRunFixture(t, scope)
			run, err := repo.GetRun(worker, b.BotID, lease.RunID)
			if err != nil {
				t.Fatal(err)
			}
			event := &session.Event{ID: uuid.NewString(), Author: "bot_runtime"}
			if err := repo.appendBotRunEvent(worker, lease, run.SessionID, event); err != nil {
				t.Fatal(err)
			}
			count := func() int {
				var n int
				switch d := repo.db.(type) {
				case teamBotDB:
					n, _ = d.client.SessionEvent.Query().Count(context.Background())
				case personalBotDB:
					n, _ = d.client.SessionEvent.Query().Count(context.Background())
				}
				return n
			}
			before := count()
			if err := repo.appendBotRunEvent(worker, lease, run.SessionID, event); err != nil {
				t.Fatal(err)
			}
			if count() != before {
				t.Fatal("retry duplicated event")
			}
			for _, invalid := range []*session.Event{nil, {Author: "missing-id"}} {
				if err := repo.appendBotRunEvent(worker, lease, run.SessionID, invalid); !errors.Is(err, store.ErrBotInvalid) {
					t.Fatalf("invalid event: %v", err)
				}
			}
			clock := repo.now
			repo.now = func() time.Time { return lease.ExpiresAt.Add(time.Second) }
			if err := repo.appendBotRunEvent(worker, lease, run.SessionID, event); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("expired duplicate: %v", err)
			}
			repo.now = clock
			wrong := lease
			wrong.Fence++
			if err := repo.appendBotRunEvent(worker, wrong, run.SessionID, event); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale fence: %v", err)
			}
			wrong = lease
			wrong.WorkerID = "another-worker"
			if err := repo.appendBotRunEvent(worker, wrong, run.SessionID, event); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("wrong worker: %v", err)
			}
			// Keep the active run/fence intact to prove status is checked after
			// the write-first lock, not merely by lease revocation side effects.
			switch d := repo.db.(type) {
			case teamBotDB:
				err = d.client.Bot.UpdateOneID(b.BotID).SetStatus(teambot.StatusPaused).Exec(worker)
			case personalBotDB:
				err = d.client.Bot.UpdateOneID(b.BotID).SetStatus(personalbot.StatusPaused).Exec(worker)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.appendBotRunEvent(worker, lease, run.SessionID, &session.Event{ID: uuid.NewString()}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("incompatible lifecycle status: %v", err)
			}
			switch d := repo.db.(type) {
			case teamBotDB:
				err = d.client.Bot.UpdateOneID(b.BotID).SetStatus(teambot.StatusActive).Exec(worker)
			case personalBotDB:
				err = d.client.Bot.UpdateOneID(b.BotID).SetStatus(personalbot.StatusActive).Exec(worker)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.appendBotRunEvent(worker, lease, uuid.New(), event); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("wrong session: %v", err)
			}
			if count() != before {
				t.Fatal("rejected append left transcript rows")
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, repo, worker, lease, "done")}); err != nil {
				t.Fatal(err)
			}
			if err := repo.appendBotRunEvent(worker, lease, run.SessionID, event); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("terminal append: %v", err)
			}
			assertRunStateSequence(t, repo, worker, *run, store.RunQueued, store.RunRunning, store.RunSucceeded)
			rows, readErr := messageReviewReader(repo).ReadBotTranscript(worker, run.SessionID, 0, 200)
			if readErr != nil {
				t.Fatal(readErr)
			}
			visible := separateRunStates(t, rows, 3)
			if len(visible) != 1 || visible[0].Event.ID != event.ID {
				t.Fatalf("original transcript changed: %+v", visible)
			}
			if count() != before+1 {
				t.Fatal("terminal append persisted")
			}
		})
	}
}
