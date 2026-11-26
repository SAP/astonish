package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
)

func TestBotPreparedPlanInitialStateAndObsolete(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, initial := range []bool{false, true} {
			name := string(scope)
			if initial {
				name += "/initial"
			} else {
				name += "/obsolete"
			}
			t.Run(name, func(t *testing.T) {
				repo, _, worker, run, lease, _ := botRunFixture(t, scope)
				base := store.BotPlanBase{PlanID: "initial-plan", Revision: 1, Sequence: 1}
				event := &session.Event{ID: "initial-state", Author: "system", Actions: session.EventActions{StateDelta: map[string]any{botPlanSnapshotKey: base}}}
				input := preparedPlanFixture(t, run)
				if initial {
					switch d := repo.db.(type) {
					case personalBotDB:
						row, err := d.client.Session.Get(worker, run.SessionID.String())
						if err != nil {
							t.Fatal(err)
						}
						data, err := d.contentCodec().encodeSessionEvent(worker, run.BotID, run.SessionID.String(), event)
						if err != nil {
							t.Fatal(err)
						}
						row.Metadata["bot_initial_state"] = data
						if err := d.client.Session.UpdateOneID(row.ID).SetMetadata(row.Metadata).Exec(worker); err != nil {
							t.Fatal(err)
						}
					case teamBotDB:
						row, err := d.client.Session.Get(worker, run.SessionID.String())
						if err != nil {
							t.Fatal(err)
						}
						data, err := d.contentCodec().encodeSessionEvent(worker, run.BotID, run.SessionID.String(), event)
						if err != nil {
							t.Fatal(err)
						}
						row.Metadata["bot_initial_state"] = data
						if err := d.client.Session.UpdateOneID(row.ID).SetMetadata(row.Metadata).Exec(worker); err != nil {
							t.Fatal(err)
						}
					}
					input.Base = base
				}
				p, err := repo.PrepareBotPlan(worker, lease, input)
				if err != nil {
					t.Fatal("prepare authoritative base", err)
				}
				base.Sequence++
				event.ID = "new-base"
				event.Actions.StateDelta[botPlanSnapshotKey] = base
				err = repo.withPreparedPlan(worker, lease, func(_ botDB, transcript botTranscriptDB, rec *botStoredRecord, saved *botStoredRun) error {
					return transcript.appendRunTranscript(worker, rec.Bot, saved.Run, event)
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := repo.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanDispatchStarted, DispatchID: uuid.New()}); !errors.Is(err, store.ErrBotConflict) {
					t.Fatal("stale dispatch", err)
				}
				if _, err := repo.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanObsolete}); err != nil {
					t.Fatal("obsolete stale base", err)
				}
				input = preparedPlanFixture(t, run)
				input.Base = base
				if _, err := repo.PrepareBotPlan(worker, lease, input); err != nil {
					t.Fatal("new base", err)
				}
			})
		}
	}
}

func TestBotPreparedPlanPublicationCollisionAndRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, collision := range []bool{false, true} {
			name := string(scope) + "/rollback"
			if collision {
				name = string(scope) + "/collision"
			}
			t.Run(name, func(t *testing.T) {
				repo, _, worker, run, lease, _ := botRunFixture(t, scope)
				p, err := repo.PrepareBotPlan(worker, lease, preparedPlanFixture(t, run))
				if err != nil {
					t.Fatal(err)
				}
				p, err = repo.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanDispatchStarted, DispatchID: uuid.New()})
				if err != nil {
					t.Fatal(err)
				}
				p, err = repo.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanSettled, DispatchID: p.DispatchID, TrustedResult: json.RawMessage(`{"exit_code":0}`), Snapshot: json.RawMessage(`{"plan_id":"publish-review","revision":1,"sequence":1}`)})
				if err != nil {
					t.Fatal(err)
				}
				events, err := preparedPlanEvents(worker, repo.db, *run)
				if err != nil {
					t.Fatal(err)
				}
				base, err := preparedPlanBase(events)
				if err != nil || base != (store.BotPlanBase{}) {
					t.Fatal("settled snapshot visible before publication", base, err)
				}
				original := repo.db
				if collision {
					e := &session.Event{ID: p.EventID, Author: "system", Actions: session.EventActions{StateDelta: map[string]any{"conflicting_payload": true}}}
					err = repo.withPreparedPlan(worker, lease, func(_ botDB, transcript botTranscriptDB, rec *botStoredRecord, saved *botStoredRun) error {
						return transcript.appendRunTranscript(worker, rec.Bot, saved.Run, e)
					})
					if err != nil {
						t.Fatal(err)
					}
				} else {
					repo.db = failRuntimeTransaction{original}
				}
				q := store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanPublished}
				if _, err = repo.ChangeBotPlan(worker, lease, q); err == nil {
					t.Fatal("publication failure ignored")
				}
				repo.db = original
				loaded, err := repo.LoadBotPlan(worker, lease, p.OperationID)
				if err != nil || loaded.Stage != store.BotPlanSettled {
					t.Fatal("failed publication changed stage", loaded, err)
				}
				events, err = preparedPlanEvents(worker, repo.db, *run)
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, e := range events {
					if e.ID == p.EventID {
						count++
						if !collision || e.Actions.StateDelta["conflicting_payload"] != true {
							t.Fatal("publication leaked or overwrote collision")
						}
					}
				}
				if collision {
					if count != 1 {
						t.Fatal("collision count", count)
					}
					return
				}
				if count != 0 {
					t.Fatal("publication escaped rollback", count)
				}
				if _, err = repo.ChangeBotPlan(worker, lease, q); err != nil {
					t.Fatal("publication retry", err)
				}
				if _, err = repo.ChangeBotPlan(worker, lease, q); err != nil {
					t.Fatal("duplicate publication", err)
				}
				events, err = preparedPlanEvents(worker, repo.db, *run)
				if err != nil {
					t.Fatal(err)
				}
				count = 0
				for _, e := range events {
					if e.ID == p.EventID {
						count++
					}
				}
				if count != 1 {
					t.Fatal("retry count", count)
				}
			})
		}
	}
}

func TestBotPreparedPlanRenewedLease(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, worker, run, lease, now := botRunFixture(t, scope)
			p, err := repo.PrepareBotPlan(worker, lease, preparedPlanFixture(t, run))
			if err != nil {
				t.Fatal(err)
			}
			renewed, err := repo.RenewLease(worker, lease, lease.ExpiresAt.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			*now = lease.ExpiresAt.Add(time.Second)
			for _, l := range []store.BotLease{lease, renewed} {
				if _, err := repo.LoadBotPlan(worker, l, p.OperationID); err != nil {
					t.Fatal("persisted renewal", err)
				}
			}
			wrong := renewed
			wrong.WorkerID = "replaced-worker"
			if _, err := repo.LoadBotPlan(worker, wrong, p.OperationID); err == nil {
				t.Fatal("wrong worker accepted")
			}
			repo.auth.check = func(context.Context, string) (string, error) { return "", store.ErrBotForbidden }
			if _, err := repo.LoadBotPlan(worker, renewed, p.OperationID); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal("revoked membership", err)
			}
		})
	}
}
