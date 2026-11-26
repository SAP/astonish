package entstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotContentRunPersistenceStateCoalescesAuthoritativeMatch(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, worker, run, lease, now := botRunFixture(t, scope)
			if err := repo.db.Transaction(worker, func(tx botDB) error {
				if err := tx.(botTranscriptDB).fenceTranscript(worker, lease, *now); err != nil {
					return err
				}
				rec, err := tx.GetBot(worker, run.BotID)
				if err != nil {
					return err
				}
				saved, err := tx.ReadRun(worker, run.BotID, run.ID)
				if err != nil {
					return err
				}
				sequence := rec.LastSequence + 1
				saved.Run.StateSequence, saved.Run.StateCommittedAt = sequence, *now
				if err := tx.(botWorkDB).updateWorkRun(worker, *saved, lease, *now); err != nil {
					return err
				}
				before, _ := botRunPersistenceData(t, worker, tx, run)
				if err := tx.AppendRunState(worker, rec.Bot, saved.Run, sequence, *now); err != nil {
					return err
				}
				after, _ := botRunPersistenceData(t, worker, tx, run)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("exact persisted state rewrote envelope")
				}
				if err := tx.AppendRunState(worker, rec.Bot, saved.Run, sequence, now.Add(time.Nanosecond)); err != nil {
					return err
				}
				changed, _ := botRunPersistenceData(t, worker, tx, run)
				if reflect.DeepEqual(after, changed) {
					t.Fatal("timestamp mismatch skipped authoritative write")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func botRunPersistenceData(t *testing.T, ctx context.Context, db botDB, run *store.BotRun) (map[string]any, *botContentCodec) {
	t.Helper()
	switch d := db.(type) {
	case personalBotDB:
		row, err := d.client.BotRun.Get(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		return row.Data, d.contentCodec()
	case teamBotDB:
		row, err := d.client.BotRun.Get(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		return row.Data, d.contentCodec()
	default:
		t.Fatal("unsupported test adapter")
		return nil, nil
	}
}

func assertBotRunPersistenceEnvelope(t *testing.T, data map[string]any) botRunRecord {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"RUN_DISK_SENTINEL", base64.StdEncoding.EncodeToString([]byte("RUN_DISK_SENTINEL_attachment"))} {
		if bytes.Contains(raw, []byte(marker)) {
			t.Fatalf("run body leaked into SQL: %s", marker)
		}
	}
	var record botRunRecord
	if err := decodeBotRecord(data, &record); err != nil {
		t.Fatalf("run SQL is not a reference envelope: %v", err)
	}
	return record
}

func TestBotContentLeaseControlDoesNotHydrateCheckpoint(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, now := botRunFixture(t, scope)
			unavailable := errors.New("checkpoint storage unavailable")
			opens := 0
			wrap := func(c *botContentCodec) *botContentCodec {
				copy := *c
				copy.Open = func(context.Context, store.BotID) (store.BotContentStaging, error) {
					opens++
					return nil, unavailable
				}
				return &copy
			}
			switch d := repo.db.(type) {
			case personalBotDB:
				d.content = wrap(d.contentCodec())
				repo.db = d
			case teamBotDB:
				d.content = wrap(d.contentCodec())
				repo.db = d
			}
			if _, err := repo.checkLease(ctx, repo.db, lease, *now); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("owner without worker identity accepted: %v", err)
			}
			if _, err := repo.checkLease(worker, repo.db, lease, *now); err != nil {
				t.Fatalf("SQL lease check hydrated checkpoint: %v", err)
			}
			if err := repo.db.Transaction(worker, func(tx botDB) error {
				if err := tx.(botTranscriptDB).fenceTranscript(worker, lease, *now); err != nil {
					return err
				}
				_, err := repo.checkLease(worker, tx, lease, *now)
				return err
			}); err != nil {
				t.Fatalf("transaction lease check hydrated checkpoint: %v", err)
			}
			stale := lease
			stale.Fence++
			if _, err := repo.checkLease(worker, repo.db, stale, *now); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale fence accepted: %v", err)
			}
			if _, err := repo.checkLease(worker, repo.db, lease, lease.ExpiresAt); !errors.Is(err, store.ErrBotExpired) {
				t.Fatalf("expired lease accepted: %v", err)
			}
			if _, err := repo.RenewLease(worker, lease, lease.ExpiresAt.Add(time.Minute)); err != nil {
				t.Fatalf("non-progress renewal hydrated checkpoint: %v", err)
			}
			if err := repo.db.Transaction(worker, func(tx botDB) error {
				rec, err := tx.GetBot(worker, lease.BotID)
				if err != nil {
					return err
				}
				return tx.AdvanceLeaseSequence(worker, lease, rec.LastSequence, *now)
			}); err != nil {
				t.Fatalf("sequence fence hydrated checkpoint: %v", err)
			}
			if opens != 0 {
				t.Fatalf("control checks opened retained content %d times", opens)
			}
			// Runtime records are database-inline, so a full run read never opens
			// workspace content even when the opener would fail.
			if _, err := repo.db.ReadRun(ctx, run.BotID, run.ID); err != nil {
				t.Fatalf("full run read failed: %v", err)
			}
			if opens != 0 {
				t.Fatalf("full run read opened retained content %d times", opens)
			}
		})
	}
}

func TestBotContentSQLRunControlBinding(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, lease, _ := botRunFixture(t, scope)
			data, _ := botRunPersistenceData(t, ctx, repo.db, run)
			record := assertBotRunPersistenceEnvelope(t, data)
			for _, mutate := range []struct {
				name  string
				apply func(*botRunRecord)
			}{
				{"bot", func(r *botRunRecord) { r.Control.BotID = run.ID }},
				{"run", func(r *botRunRecord) { r.Control.ID = run.BotID }},
				{"version", func(r *botRunRecord) { r.Control.Version++ }},
			} {
				t.Run(mutate.name, func(t *testing.T) {
					bad := record
					mutate.apply(&bad)
					raw, err := json.Marshal(bad)
					if err != nil {
						t.Fatal(err)
					}
					_, err = sqlRunControlFromRow(run.BotID, run.ID, run.Version, string(store.RunRunning), lease.WorkerID, lease.Fence, &lease.ExpiresAt, map[string]any{"bot_record_v2": string(raw)})
					if !errors.Is(err, store.ErrBotInvalid) {
						t.Fatalf("mismatched envelope accepted: %v", err)
					}
				})
			}
			expires := lease.ExpiresAt.Add(time.Minute)
			control, err := sqlRunControlFromRow(run.BotID, run.ID, run.Version, string(store.RunInterrupted), "replacement", lease.Fence+1, &expires, data)
			if err != nil || control.Status != store.RunInterrupted || control.Lease.WorkerID != "replacement" || control.Lease.Fence != lease.Fence+1 || !control.Lease.ExpiresAt.Equal(expires) {
				t.Fatalf("SQL columns did not override envelope lease/status: %+v %v", control, err)
			}
		})
	}
}

func TestBotContentRunPersistence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, lease, now := botRunFixture(t, scope)
			data, _ := botRunPersistenceData(t, ctx, repo.db, run)
			assertBotRunPersistenceEnvelope(t, data) // InsertRun and ClaimLease must both use refs.
			saved, err := repo.db.ReadRun(ctx, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			saved.Instructions = "RUN_DISK_SENTINEL_\"quoted\"_雪"
			saved.Attachments = []store.ChatFile{{Filename: "input.txt", MimeType: "text/plain", Data: []byte("RUN_DISK_SENTINEL_attachment")}}
			saved.Run.Report.Summary = "RUN_DISK_SENTINEL_report"
			saved.Run.Checkpoint = json.RawMessage(`{ "text": "RUN_DISK_SENTINEL_checkpoint", "counter": 1152921504606847013 }`)
			err = repo.db.Transaction(ctx, func(tx botDB) error { return tx.(botWorkDB).updateWorkRun(ctx, *saved, lease, *now) })
			if err != nil {
				t.Fatal(err)
			}
			data, _ = botRunPersistenceData(t, ctx, repo.db, run)
			assertBotRunPersistenceEnvelope(t, data)
			got, err := repo.db.ReadRun(ctx, run.BotID, run.ID)
			if err != nil || !reflect.DeepEqual(got, saved) {
				t.Fatalf("hydrated run differs: %v", err)
			}
			// Lease columns remain authoritative even though immutable body has the old expiry.
			expires := lease.ExpiresAt.Add(time.Minute)
			err = repo.db.Transaction(ctx, func(tx botDB) error { return tx.RenewRunLease(ctx, lease, *now, expires) })
			if err != nil {
				t.Fatal(err)
			}
			got, err = repo.db.ReadRun(ctx, run.BotID, run.ID)
			if err != nil || !got.Run.Lease.ExpiresAt.Equal(expires) {
				t.Fatalf("SQL lease overlay lost: %+v %v", got, err)
			}
			if _, err = repo.CancelRun(ctx, run.BotID, run.ID); err != nil {
				t.Fatal(err)
			}
			data, _ = botRunPersistenceData(t, ctx, repo.db, run)
			assertBotRunPersistenceEnvelope(t, data)
			got, err = repo.db.ReadRun(ctx, run.BotID, run.ID)
			if err != nil || got.Instructions != saved.Instructions || !got.Run.Lease.ExpiresAt.IsZero() || got.Run.Status != store.RunInterrupted {
				t.Fatalf("cancellation hydration: %+v %v", got, err)
			}
		})
	}
}

func TestBotContentRunPersistenceReopen(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx, botID, runID, _, reopen := ApprovalScopeReopenFixture(t, scope)
			scoped, _ := reopen()
			repository := func(s store.BotStore) *botRepository {
				switch s := s.(type) {
				case *personalBotStore:
					return s.repository()
				case *teamBotStore:
					return s.repository()
				default:
					t.Fatal("unknown scope")
					return nil
				}
			}
			repo := repository(scoped)
			saved, err := repo.db.ReadRun(ctx, botID, runID)
			if err != nil {
				t.Fatal(err)
			}
			saved.Instructions = "RUN_DISK_SENTINEL_reopen"
			saved.Run.Report.Summary = "RUN_DISK_SENTINEL_report"
			if err := repo.db.Transaction(ctx, func(tx botDB) error { return saveProgressEnvelope(ctx, tx, saved) }); err != nil {
				t.Fatal(err)
			}
			scoped, _ = reopen() // All connections close; fresh adapters hydrate selected refs.
			repo = repository(scoped)
			got, err := repo.db.ReadRun(ctx, botID, runID)
			if err != nil || !reflect.DeepEqual(got, saved) {
				t.Fatalf("reopened run differs: %v", err)
			}
			data, _ := botRunPersistenceData(t, ctx, repo.db, &got.Run)
			assertBotRunPersistenceEnvelope(t, data)
		})
	}
}

func TestBotContentRunPersistenceDirectWriters(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, writer := range []string{"title", "progress", "finish", "quiesce"} {
			t.Run(string(scope)+"/"+writer, func(t *testing.T) {
				repo, ctx, _, run, lease, now := botRunFixture(t, scope)
				err := repo.db.Transaction(ctx, func(tx botDB) error {
					saved, err := tx.ReadRun(ctx, run.BotID, run.ID)
					if err != nil {
						return err
					}
					saved.Instructions = "RUN_DISK_SENTINEL_" + writer
					switch writer {
					case "title":
						return persistTitleQuestion(ctx, tx, saved)
					case "progress":
						return saveProgressEnvelope(ctx, tx, saved)
					case "finish":
						rec, err := tx.GetBot(ctx, run.BotID)
						if err != nil {
							return err
						}
						saved.Run.Status = store.RunPartial
						saved.Run.Report.Summary = "RUN_DISK_SENTINEL_finished"
						return tx.FinishRun(ctx, *rec, *saved, lease, *now, false)
					case "quiesce":
						if err := tx.(botWorkDB).updateWorkRun(ctx, *saved, lease, *now); err != nil {
							return err
						}
						_, err := tx.QuiesceBotRuns(ctx, run.BotID, true, *now)
						return err
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				data, _ := botRunPersistenceData(t, ctx, repo.db, run)
				assertBotRunPersistenceEnvelope(t, data)
				got, err := repo.db.ReadRun(ctx, run.BotID, run.ID)
				if err != nil || got.Instructions != "RUN_DISK_SENTINEL_"+writer {
					t.Fatalf("writer lost content: %v", err)
				}
			})
		}
	}
}

func TestBotContentRunPersistenceRollbackMissing(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, lease, now := botRunFixture(t, scope)
			original, err := repo.db.ReadRun(ctx, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("rollback staged run")
			err = repo.db.Transaction(ctx, func(tx botDB) error {
				next := *original
				next.Instructions = "RUN_DISK_SENTINEL_rollback"
				if err := tx.(botWorkDB).updateWorkRun(ctx, next, lease, *now); err != nil {
					return err
				}
				return injected
			})
			if !errors.Is(err, injected) {
				t.Fatal(err)
			}
			got, err := repo.db.ReadRun(ctx, run.BotID, run.ID)
			if err != nil || !reflect.DeepEqual(got, original) {
				t.Fatalf("rollback selected staged body: %v", err)
			}
		})
	}
}
