package entstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotRecoveredCleanupFencesRaces(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scenario := range []string{"new-worker", "pause", "delete", "audit"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				r, owner, _, run, lease, now, _, _ := scheduledInterruptionFixture(t, scope)
				*now = now.Add(2 * time.Minute)
				if err := r.RecoverRun(owner, lease); err != nil {
					t.Fatal(err)
				}
				root, err := r.db.GetBot(owner, run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				q := store.BotCleanupRequest{Kind: store.BotCleanupRunTeardown, BotID: run.BotID, RunID: run.ID, RunFence: lease.Fence, Fence: root.Fence, ExpectedRevision: root.Bot.Revision, Home: root.Bot.Home}
				original := r.db
				var newID uuid.UUID
				runtime := cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error {
					switch scenario {
					case "new-worker":
						fresh, err := r.RequestRun(owner, store.BotRunRequest{BotID: run.BotID, Version: root.Bot.Version, Trigger: store.TriggerManual, Instructions: "New owner work", IdempotencyKey: "new-work"})
						if err != nil {
							return err
						}
						newID = fresh.ID
						worker := execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: run.BotID, RunID: fresh.ID, Version: fresh.Version, ActorID: root.Bot.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
						_, err = r.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: fresh.ID, WorkerID: "fresh", ExpiresAt: now.Add(time.Minute)})
						return err
					case "pause":
						_, err := r.Pause(owner, store.BotLifecycleRequest{BotID: run.BotID, ExpectedRevision: root.Bot.Revision, ActorID: root.Bot.OwnerID, Reason: "racing pause"})
						return err
					case "delete":
						_, err := r.BeginDelete(owner, store.BotLifecycleRequest{BotID: run.BotID, ExpectedRevision: root.Bot.Revision, ActorID: root.Bot.OwnerID, Reason: "racing delete"})
						return err
					}
					return nil
				})
				if scenario == "audit" {
					r.db = cleanupFailAuditTransaction{botDraftDB: original, botCleanupDB: original.(botCleanupDB)}
				}
				err = r.ConfirmCleanup(runtime, q)
				r.db = original
				if err == nil {
					t.Fatal("racing or unaudited receipt accepted")
				}
				after, e := r.db.GetBot(owner, run.BotID)
				if e != nil {
					t.Fatal(e)
				}
				if _, ok := after.Metadata[botCleanupReceiptKey(q.Kind)]; ok {
					t.Fatal("failed receipt persisted")
				}
				if scenario == "new-worker" && (newID == uuid.Nil || after.ActiveRunID == nil || *after.ActiveRunID != newID) {
					t.Fatal("cleanup cleared new writer")
				}
				if scenario == "pause" && after.Bot.Status != store.BotPaused {
					t.Fatal("cleanup unpaused Bot")
				}
				if scenario == "delete" && after.Bot.Status != store.BotDeleted {
					t.Fatal("cleanup restored Bot")
				}
			})
		}
	}
}

func TestBotRecoveredCleanupWithoutRetainedHome(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, _, run, lease, now, _, _ := scheduledInterruptionFixture(t, scope)
			*now = now.Add(2 * time.Minute)
			if err := r.RecoverRun(owner, lease); err != nil {
				t.Fatal(err)
			}
			switch db := r.db.(type) {
			case personalBotDB:
				if _, err := db.client.Bot.UpdateOneID(run.BotID).ClearHomeBinding().Save(owner); err != nil {
					t.Fatal(err)
				}
			case teamBotDB:
				if _, err := db.client.Bot.UpdateOneID(run.BotID).ClearHomeBinding().Save(owner); err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatal("unexpected database")
			}
			root, err := r.db.GetBot(owner, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			if root.Bot.Home.ID != uuid.Nil {
				t.Fatal("retained Home still present")
			}
			q := store.BotCleanupRequest{Kind: store.BotCleanupRunTeardown, BotID: run.BotID, RunID: run.ID, RunFence: lease.Fence, Fence: root.Fence, ExpectedRevision: root.Bot.Revision}
			calls := 0
			verified := cleanupRuntimeContext(t, func(_ context.Context, got store.BotCleanupRequest) error {
				calls++
				if got != q {
					return store.ErrBotForbidden
				}
				return nil
			})
			if err := r.ConfirmCleanup(verified, q); err != nil {
				t.Fatalf("title-only teardown receipt: %v", err)
			}
			if calls != 1 {
				t.Fatalf("physical verifier calls=%d", calls)
			}
		})
	}
}

func TestBotRecoveredCleanupReceipt(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, worker, run, lease, now, _, reopen := scheduledInterruptionFixture(t, scope)
			*now = now.Add(2 * time.Minute)
			if err := r.RecoverRun(owner, lease); err != nil {
				t.Fatal(err)
			}
			root, err := r.db.GetBot(owner, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := r.db.ReadRun(owner, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotCleanupRequest{Kind: store.BotCleanupRunTeardown, BotID: run.BotID, RunID: run.ID, RunFence: lease.Fence, Fence: root.Fence, ExpectedRevision: root.Bot.Revision, Home: root.Bot.Home}
			calls := 0
			verify := cleanupRuntimeContext(t, func(_ context.Context, got store.BotCleanupRequest) error {
				calls++
				if got != q {
					t.Fatal("wrong cleanup identity")
				}
				return nil
			})
			denied := errors.New("old session still exists")
			if err := r.ConfirmCleanup(cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error { return denied }), q); !errors.Is(err, denied) {
				t.Fatalf("physical verifier not reached: %v", err)
			}
			if err := r.ConfirmCleanup(verify, q); err != nil {
				t.Fatalf("recovered teardown receipt: %v", err)
			}
			reopen()
			if err := r.ConfirmCleanup(verify, q); err != nil || calls != 1 {
				t.Fatalf("durable replay calls=%d err=%v", calls, err)
			}
			after, err := r.db.ReadRun(owner, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("cleanup rewrote interrupted run")
			}
			if _, err := r.RenewLease(worker, lease, now.Add(time.Minute)); err == nil {
				t.Fatal("old worker revived")
			}
		})
	}
}
