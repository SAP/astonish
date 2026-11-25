package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	po "github.com/SAP/astonish/ent/personal/botoutbox"
	to "github.com/SAP/astonish/ent/team/botoutbox"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotContentLegacyExplicitDelete(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, legacy := range []bool{false, true} {
			name := string(scope) + "/missing-disk"
			if legacy {
				name = string(scope) + "/legacy-inline"
			}
			t.Run(name, func(t *testing.T) {
				r, rt, owner, worker, q := authorizedScheduleFixture(t, scope)
				pending, err := rt.EnqueueAuthorizedOperation(worker, q)
				if err != nil {
					t.Fatal(err)
				}
				q.IdempotencyKey = "applying"
				applying, err := rt.EnqueueAuthorizedOperation(worker, q)
				if err != nil {
					t.Fatal(err)
				}
				err = r.db.Transaction(owner, func(tx botDraftDB) error {
					rec, err := tx.GetBot(owner, q.MandateRef.BotID)
					if err != nil {
						return err
					}
					applying.Status = store.BotOperationApplying
					applying.Receipt = &store.BotOperationReceipt{Status: store.BotOperationApplying, ObservedAt: r.clock(), Reason: "effect may have occurred"}
					applying.Revision++
					return saveAuthorizedOperation(owner, tx, rec, *applying, rt)
				})
				if err != nil {
					t.Fatal(err)
				}
				historical, err := readMandate(owner, r.db, q.MandateRef)
				if err != nil {
					t.Fatal(err)
				}
				historical.Mandate.Ref.ID = uuid.New()
				historicalRef := historical.Mandate.Ref
				if err = r.db.Transaction(owner, func(tx botDraftDB) error {
					rec, err := tx.GetBot(owner, q.MandateRef.BotID)
					if err != nil {
						return err
					}
					return appendMandateRecord(owner, tx, rec, mandateKey(historicalRef), historical, r.clock())
				}); err != nil {
					t.Fatal(err)
				}
				home := store.BotHomeBinding{ID: uuid.New(), BotID: q.MandateRef.BotID, Scope: scope, Backend: "fixture", StorageKey: "legacy-cleanup", Revision: 1}
				hm, _ := botJSONMap(home)
				var codec *botContentCodec
				switch db := r.db.(type) {
				case personalBotDB:
					codec = db.contentCodec()
					err = db.client.Bot.UpdateOneID(home.BotID).SetHomeBinding(hm).Exec(owner)
				case teamBotDB:
					codec = db.contentCodec()
					err = db.client.Bot.UpdateOneID(home.BotID).SetHomeBinding(hm).Exec(owner)
				}
				if err != nil {
					t.Fatal(err)
				}
				if legacy {
					// Replace only fixture records with their old inline representation. This
					// is test setup, not a production migration or execution fallback.
					switch db := r.db.(type) {
					case personalBotDB:
						rows, e := db.client.BotOutbox.Query().Where(po.BotIDEQ(home.BotID)).All(owner)
						if e != nil {
							t.Fatal(e)
						}
						for _, row := range rows {
							a, e := codec.decodeActivity(owner, home.BotID, row.Sequence, store.BotActivityKind(row.Kind), row.CreatedAt, row.Payload)
							if e != nil {
								t.Fatal(e)
							}
							var value any
							if e = json.Unmarshal(a.Payload, &value); e != nil {
								t.Fatal(e)
							}
							data, e := botJSONMap(value)
							if e != nil {
								t.Fatal(e)
							}
							if e = db.client.BotOutbox.UpdateOneID(row.ID).SetPayload(data).Exec(owner); e != nil {
								t.Fatal(e)
							}
						}
						if e = db.client.BotRun.UpdateOneID(q.Lease.RunID).SetData(map[string]any{"bot_document": `{"Run":{"Instructions":"never hydrate this legacy body"},"CommitHash":""}`}).Exec(owner); e != nil {
							t.Fatal(e)
						}
					case teamBotDB:
						rows, e := db.client.BotOutbox.Query().Where(to.BotIDEQ(home.BotID)).All(owner)
						if e != nil {
							t.Fatal(e)
						}
						for _, row := range rows {
							a, e := codec.decodeActivity(owner, home.BotID, row.Sequence, store.BotActivityKind(row.Kind), row.CreatedAt, row.Payload)
							if e != nil {
								t.Fatal(e)
							}
							var value any
							if e = json.Unmarshal(a.Payload, &value); e != nil {
								t.Fatal(e)
							}
							data, e := botJSONMap(value)
							if e != nil {
								t.Fatal(e)
							}
							if e = db.client.BotOutbox.UpdateOneID(row.ID).SetPayload(data).Exec(owner); e != nil {
								t.Fatal(e)
							}
						}
						if e = db.client.BotRun.UpdateOneID(q.Lease.RunID).SetData(map[string]any{"bot_document": `{"Run":{"Instructions":"never hydrate this legacy body"},"CommitHash":""}`}).Exec(owner); e != nil {
							t.Fatal(e)
						}
					}
				}
				open := codec.Open
				calls := 0
				codec.Open = func(context.Context, store.BotID) (store.BotContentStaging, error) {
					calls++
					return nil, store.ErrBotNotFound
				}
				defer func() { codec.Open = open }()
				b, err := r.Get(owner, home.BotID)
				if err != nil {
					t.Fatal(err)
				}
				request := store.BotPermanentDeleteRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Confirm: true}
				bad := request
				bad.ActorID = uuid.New()
				if _, err = r.BeginPermanentDelete(owner, bad); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("actor forgery: %v", err)
				}
				p, _ := execution.PrincipalFromContext(owner)
				p.OrgSlug = "other"
				cross, err := execution.WithPrincipal(context.Background(), p)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = r.BeginPermanentDelete(cross, request); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("cross-scope: %v", err)
				}
				deleted, err := r.BeginPermanentDelete(owner, request)
				if err != nil {
					t.Fatal(err)
				}
				if calls != 0 {
					t.Fatalf("delete opened unavailable content %d times", calls)
				}
				if err = r.PurgeDeleted(owner, b.ID, deleted.Revision); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("unverified purge: %v", err)
				}
				rec, err := r.db.GetBot(owner, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				exists, err := cleanupRevokedExists(owner, r.db, q.MandateRef)
				if err != nil || !exists {
					t.Fatalf("missing revocation: %v", err)
				}
				exists, err = cleanupRevokedExists(owner, r.db, historicalRef)
				if err != nil || !exists {
					t.Fatalf("missing historical revocation: %v", err)
				}
				raw, _ := json.Marshal(rec.Metadata[botOperationCleanupPrefix+pending.ID.String()])
				var cancelled botCleanupAuthority
				if json.Unmarshal(raw, &cancelled) != nil || cancelled.Status != store.BotOperationCancelled || cancelled.Revision != pending.Revision+1 {
					t.Fatal("pending operation not cancelled")
				}
				if _, ok := rec.Metadata[botOperationCleanupPrefix+applying.ID.String()]; ok {
					t.Fatal("applying operation cancelled")
				}
				if _, ok := rec.Metadata["authorized-pending:"+applying.ID.String()]; !ok {
					t.Fatal("applying uncertainty discarded")
				}
				if !legacy {
					unavailable := codec.Open
					codec.Open = open
					current, readErr := readMandateOperation(owner, r.db, rec, q.MandateRef, applying.ID)
					codec.Open = unavailable
					if readErr != nil || current.Status != store.BotOperationApplying || current.Revision != applying.Revision || current.Receipt == nil || current.Receipt.Reason != applying.Receipt.Reason || !current.Receipt.ObservedAt.Equal(applying.Receipt.ObservedAt) {
						t.Fatalf("applying evidence changed: %+v %v", current, readErr)
					}
				}
				run, err := readCleanupRun(owner, r.db, b.ID, q.Lease.RunID)
				if err != nil || run.Run.Status != store.RunInterrupted {
					t.Fatalf("active run not interrupted: %+v %v", run, err)
				}
				runtime := cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error { return nil })
				teardown := store.BotCleanupRequest{Kind: store.BotCleanupRunTeardown, BotID: b.ID, ExpectedRevision: deleted.Revision, Fence: rec.Fence, Home: home, RunID: q.Lease.RunID, RunFence: q.Lease.Fence}
				if err = r.ConfirmCleanup(runtime, teardown); err != nil {
					t.Fatal(err)
				}
				claim, err := r.ReserveHomeCleanup(runtime, home, deleted.Revision)
				if err != nil {
					t.Fatal(err)
				}
				if err = r.PurgeDeleted(owner, b.ID, deleted.Revision); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("claim bypassed receipt: %v", err)
				}
				if err = r.ConfirmCleanup(runtime, claim.Request); err != nil {
					t.Fatal(err)
				}
				if err = r.PurgeDeleted(owner, b.ID, deleted.Revision); err != nil {
					t.Fatal(err)
				}
				if calls != 0 {
					t.Fatalf("cleanup opened unavailable content %d times", calls)
				}
			})
		}
	}
}

func TestBotContentDeleteRejectsMalformedAuthority(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, rt, owner, worker, q := authorizedScheduleFixture(t, scope)
			if _, err := rt.EnqueueAuthorizedOperation(worker, q); err != nil {
				t.Fatal(err)
			}
			b, err := r.Get(owner, q.MandateRef.BotID)
			if err != nil {
				t.Fatal(err)
			}
			err = r.db.Transaction(owner, func(tx botDraftDB) error {
				rec, err := tx.GetBot(owner, b.ID)
				if err != nil {
					return err
				}
				return appendMandateRecord(owner, tx, rec, "mandate:test:1", map[string]any{"reserved": true}, r.clock())
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.BeginPermanentDelete(owner, store.BotPermanentDeleteRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Confirm: true}); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("malformed authority accepted: %v", err)
			}
			after, err := r.Get(owner, b.ID)
			if err != nil || after.Status != b.Status || after.Revision != b.Revision {
				t.Fatalf("failed deletion was not atomic: %+v %v", after, err)
			}
			revoked, err := cleanupRevokedExists(owner, r.db, q.MandateRef)
			if err != nil || revoked {
				t.Fatalf("partial revocation: %v %v", revoked, err)
			}
		})
	}
}

func TestBotContentCleanupRunStateAllowlist(t *testing.T) {
	id, sid, home := uuid.New(), uuid.New(), uuid.New()
	s := botCleanupRunState{BotID: id, RunID: uuid.New(), SessionID: sid, HomeSessionID: home, Status: store.RunInterrupted, Sequence: 1, At: time.Now().UTC()}
	raw, _ := json.Marshal(s)
	data := map[string]any{botCleanupRunStateKey: string(raw)}
	c := &botContentCodec{Open: func(context.Context, store.BotID) (store.BotContentStaging, error) {
		t.Fatal("opened content for cleanup state")
		return nil, nil
	}}
	e, err := c.decodeSessionEvent(context.Background(), id, sid.String(), data)
	if err != nil || e.Content != nil {
		t.Fatalf("metadata event: %+v %v", e, err)
	}
	if _, err = botSessionEventID(id, sid.String(), data); err != nil {
		t.Fatal(err)
	}
	if _, err = c.decodeSessionEvent(context.Background(), uuid.New(), sid.String(), data); err == nil {
		t.Fatal("cross Bot accepted")
	}
	if _, err = c.decodeSessionEvent(context.Background(), id, uuid.NewString(), data); err == nil {
		t.Fatal("cross session accepted")
	}
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["message"] = "must never hydrate"
	raw, _ = json.Marshal(fields)
	data[botCleanupRunStateKey] = string(raw)
	if _, err = c.decodeSessionEvent(context.Background(), id, sid.String(), data); err == nil {
		t.Fatal("content accepted")
	}
}

func TestBotContentCleanupActivityRejectsAuthority(t *testing.T) {
	id, now := uuid.New(), time.Now().UTC()
	ref := store.BotMandateRef{ID: uuid.New(), BotID: id, OwnerID: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Revision: 1}
	a := store.BotActivity{BotID: id, Sequence: 1, Kind: mandateRecordKind, CreatedAt: now}
	a.Payload, _ = json.Marshal(map[string]any{"ref": ref, "revoked": true})
	data, err := cleanupRecordData(a)
	if err != nil {
		t.Fatal(err)
	}
	c := &botContentCodec{Open: func(context.Context, store.BotID) (store.BotContentStaging, error) {
		t.Fatal("cleanup activity opened storage")
		return nil, nil
	}}
	if _, err = c.decodeActivity(context.Background(), id, 1, mandateRecordKind, now, data); err != nil {
		t.Fatal(err)
	}
	for _, intent := range []store.BotOutboxKind{store.BotOutboxDispatch, store.BotOutboxPublish, store.BotOutboxReconcile, store.BotOutboxNotify} {
		var fields map[string]any
		_ = json.Unmarshal([]byte(data[botCleanupRecordKey].(string)), &fields)
		fields["intent"] = intent
		raw, _ := json.Marshal(fields)
		if _, err := c.decodeActivity(context.Background(), id, 1, mandateRecordKind, now, map[string]any{botCleanupRecordKey: string(raw)}); !errors.Is(err, store.ErrBotInvalid) {
			t.Fatalf("non-cleanup intent accepted: %s %v", intent, err)
		}
	}
	for _, field := range []string{"mandate", "mandate_ref", "provenance", "payload", "work", "message"} {
		t.Run(field, func(t *testing.T) {
			var fields map[string]any
			_ = json.Unmarshal([]byte(data[botCleanupRecordKey].(string)), &fields)
			fields[field] = map[string]any{"ref": ref}
			raw, _ := json.Marshal(fields)
			if _, err := c.decodeActivity(context.Background(), id, 1, mandateRecordKind, now, map[string]any{botCleanupRecordKey: string(raw)}); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("non-cleanup field accepted: %v", err)
			}
		})
	}
}

func TestBotContentCleanupEvidenceRejectsUnknownFormat(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, _, owner, _, q := authorizedScheduleFixture(t, scope)
			b, err := r.Get(owner, q.MandateRef.BotID)
			if err != nil {
				t.Fatal(err)
			}
			key := "recovery:" + q.Lease.RunID.String() + ":1"
			now := r.clock()
			// A runtime record with no inline body is not valid cleanup evidence.
			record := botActivityRecord{SchemaVersion: store.BotSchemaVersion, BotID: b.ID, RunID: &q.Lease.RunID, Sequence: 999, Kind: store.BotActivityReport, CreatedAt: now, RunStatus: store.RunInterrupted}
			raw, _ := json.Marshal(record)
			data := map[string]any{"bot_record_v2": string(raw)}
			switch db := r.db.(type) {
			case personalBotDB:
				_, err = db.client.BotOutbox.Create().SetBotID(b.ID).SetSequence(record.Sequence).SetKind(string(record.Kind)).SetDeduplicationKey(key).SetPayload(data).SetAvailableAt(now).SetCreatedAt(now).Save(owner)
			case teamBotDB:
				_, err = db.client.BotOutbox.Create().SetBotID(b.ID).SetSequence(record.Sequence).SetKind(string(record.Kind)).SetDeduplicationKey(key).SetPayload(data).SetAvailableAt(now).SetCreatedAt(now).Save(owner)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = readCleanupRunActivity(owner, r.db, b.ID, key); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("unknown format accepted as cleanup evidence: %v", err)
			}
		})
	}
}

func TestBotContentDeleteRevocationSurvivesRestore(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, rt, owner, worker, q := authorizedScheduleFixture(t, scope)
			o, err := rt.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			b, err := r.Get(owner, q.MandateRef.BotID)
			if err != nil {
				t.Fatal(err)
			}
			deleted, err := r.BeginDelete(owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "delete"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.Restore(owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: deleted.Revision, Reason: "restore"})
			if err != nil {
				t.Fatal(err)
			}
			mandate, err := readMandate(owner, r.db, q.MandateRef)
			if err != nil || mandate.RevokedAt == nil {
				t.Fatalf("revocation lost: %v", err)
			}
			rec, err := r.db.GetBot(owner, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			current, err := readMandateOperation(owner, r.db, rec, q.MandateRef, o.ID)
			if err != nil || current.Status != store.BotOperationCancelled {
				t.Fatalf("cancelled state lost: %+v %v", current, err)
			}
			if _, err = rt.ApplyAuthorizedOperation(context.Background(), q.MandateRef, o.ID); err == nil {
				t.Fatal("restored revoked operation executable")
			}
		})
	}
}
