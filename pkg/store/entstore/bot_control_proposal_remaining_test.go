package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"reflect"
	"sync"
	"testing"
	"time"
)

func remainingProposal(t *testing.T, scope store.BotScope, op store.BotControlOperation, status store.BotStatus) (*botRepository, context.Context, *store.BotControlProposal) {
	t.Helper()
	r, ctx, worker, run, lease, _ := botRunFixture(t, scope)
	if err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "done"}}); err != nil {
		t.Fatal(err)
	}
	if status != store.BotActive {
		err := r.db.Transaction(ctx, func(tx botDraftDB) error {
			rec, e := tx.GetBot(ctx, run.BotID)
			if e != nil {
				return e
			}
			next := rec.Bot
			next.Status = status
			next.Revision++
			next.UpdatedAt = r.clock()
			if e = tx.SetLifecycle(ctx, *rec, next); e != nil {
				return e
			}
			return tx.InsertActivity(ctx, store.BotActivity{BotID: run.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: json.RawMessage(`{}`), CreatedAt: r.clock()}, "fixture-status")
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := r.Get(ctx, run.BotID)
	if err != nil {
		t.Fatal(err)
	}
	r.activationCheck = func(context.Context, store.Bot, store.BotVersion) error { return nil }
	intent := store.BotIntentRegular
	if status == store.BotDraft {
		intent = store.BotIntentDraftTest
	}
	if status == store.BotPaused {
		intent = store.BotIntentConversation
	}
	source, err := r.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Intent: intent, Trigger: store.TriggerManual, Instructions: "review exact action", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	worker = execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: source.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
	lease, err = r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: source.ID, WorkerID: "review", ExpiresAt: r.clock().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	payload := store.BotControlPayload{Operation: op, Reason: "reviewed exact action"}
	switch op {
	case store.BotControlTaskOnceOperation:
		payload.TaskOnce = &store.BotControlTaskOnce{Instructions: "exact one-time instructions"}
	case store.BotControlMemoryRemember:
		payload.Memory = &store.BotControlMemory{Content: "remembered fact", Category: "fact", ExpectedRevision: b.Revision}
	case store.BotControlMemoryCorrect, store.BotControlMemoryForget:
		id := uuid.New()
		metadata, _ := store.EncodeMemoryAttribution(map[string]any{"evidence": "keep"}, &store.MemoryAttribution{BotID: b.ID, Source: "original-evidence", ObservedAt: r.clock(), Status: store.MemoryKnowledgeInferred})
		if err = insertReviewedMemory(ctx, r.db, id, run.SessionID, b.OwnerID, store.BotMemoryReviewEdit{Content: "original fact", Category: "fact"}, metadata, nil, DialectSQLite, nil, r.clock()); err != nil {
			t.Fatal(err)
		}
		payload.Memory = &store.BotControlMemory{MemoryID: id.String(), ExpectedRevision: b.Revision}
		if op == store.BotControlMemoryCorrect {
			payload.Memory.Content = "corrected fact"
			payload.Memory.Category = "fact"
		}
	}
	p, err := r.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "remaining", ExpiresAt: r.clock().Add(time.Hour), Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "review ready"}}); err != nil {
		t.Fatal(err)
	}
	return r, ctx, p
}

func remainingStatus(op store.BotControlOperation) store.BotStatus {
	if op == store.BotControlActivate {
		return store.BotDraft
	}
	if op == store.BotControlResume {
		return store.BotPaused
	}
	return store.BotActive
}
func assertRemainingOutcome(t *testing.T, r *botRepository, ctx context.Context, p *store.BotControlProposal) {
	t.Helper()
	b, err := r.Get(ctx, p.BotID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Revision != p.ExpectedRevision+1 || b.Version != p.Version {
		t.Fatalf("root changed incorrectly: %+v", b)
	}
	switch p.Payload.Operation {
	case store.BotControlMemoryRemember, store.BotControlMemoryCorrect:
		row, e := r.db.ReadCorrectionMemory(ctx, uuid.NewSHA1(p.ID, []byte("memory")).String())
		if e != nil {
			t.Fatal(e)
		}
		if row.Snippet != p.Payload.Memory.Content || row.Category != p.Payload.Memory.Category || row.Attribution == nil || row.Attribution.Source != controlProposalKey(p.ID) {
			t.Fatalf("memory: %+v", row)
		}
	case store.BotControlActivate, store.BotControlResume:
		if b.Status != store.BotActive {
			t.Fatal("not active")
		}
	case store.BotControlTaskOnceOperation:
		run, e := r.db.ReadRun(ctx, p.BotID, uuid.NewSHA1(p.ID, []byte("run")))
		if e != nil {
			t.Fatal(e)
		}
		expected := store.BotIntentRegular
		if b.Status == store.BotDraft {
			expected = store.BotIntentDraftTest
		}
		if run.Instructions != p.Payload.TaskOnce.Instructions || run.Run.Intent != expected || run.Run.Status != store.RunQueued {
			t.Fatalf("run: %+v", run)
		}
	}
	if p.Payload.Operation == store.BotControlMemoryCorrect || p.Payload.Operation == store.BotControlMemoryForget {
		row, e := r.db.ReadCorrectionMemory(ctx, p.Payload.Memory.MemoryID)
		if e != nil {
			t.Fatal(e)
		}
		if row.Snippet != "original fact" || row.Attribution == nil || row.Attribution.Source != "original-evidence" || row.Attribution.SupersededBy != controlProposalKey(p.ID) {
			t.Fatalf("lost source: %+v", row)
		}
	}
}
func TestBotControlProposalRemainingOperations(t *testing.T) {
	ops := []store.BotControlOperation{store.BotControlMemoryRemember, store.BotControlMemoryCorrect, store.BotControlMemoryForget, store.BotControlTaskOnceOperation, store.BotControlActivate, store.BotControlResume}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, op := range ops {
			for _, mode := range []string{"apply-replay", "stale", "revoked", "concurrent", "rollback"} {
				t.Run(string(scope)+"/"+string(op)+"/"+mode, func(t *testing.T) {
					r, ctx, p := remainingProposal(t, scope, op, remainingStatus(op))
					d := store.BotControlProposalDecision{BotID: p.BotID, ProposalID: p.ID, Digest: p.Digest}
					if mode == "stale" {
						err := r.db.Transaction(ctx, func(tx botDraftDB) error {
							rec, e := tx.GetBot(ctx, p.BotID)
							if e != nil {
								return e
							}
							if e = tx.(botControlVersionDB).updateControlVersion(ctx, *rec, rec.Bot.Version, r.clock()); e != nil {
								return e
							}
							return tx.InsertActivity(ctx, store.BotActivity{BotID: p.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: json.RawMessage(`{}`), CreatedAt: r.clock()}, "fixture-stale")
						})
						if err != nil {
							t.Fatal(err)
						}
						got, err := r.ConfirmControlProposal(ctx, d)
						if err != nil || got.Status != store.BotControlStale {
							t.Fatalf("stale: %+v %v", got, err)
						}
						return
					}
					if mode == "revoked" {
						r.auth.check = func(context.Context, string) (string, error) { return "viewer", nil }
						if _, err := r.ConfirmControlProposal(ctx, d); !errors.Is(err, store.ErrBotForbidden) {
							t.Fatalf("revoked: %v", err)
						}
						return
					}
					if mode == "rollback" {
						original := r.db
						r.db = controlReceiptFailTransaction{original}
						_, err := r.ConfirmControlProposal(ctx, d)
						r.db = original
						if err == nil || err.Error() != "injected receipt failure" {
							t.Fatalf("did not reach final receipt: %v", err)
						}
						saved, e := r.ReadControlProposal(ctx, p.BotID, p.ID)
						if e != nil || saved.Status != store.BotControlProposed {
							t.Fatalf("receipt escaped: %+v %v", saved, e)
						}
						b, _ := r.Get(ctx, p.BotID)
						if b.Revision != p.ExpectedRevision || b.Status != remainingStatus(op) {
							t.Fatal("root escaped rollback")
						}
						if op == store.BotControlMemoryRemember || op == store.BotControlMemoryCorrect {
							if row, e := r.db.ReadCorrectionMemory(ctx, uuid.NewSHA1(p.ID, []byte("memory")).String()); e == nil && row != nil {
								t.Fatal("memory escaped rollback")
							}
						}
						if op == store.BotControlMemoryCorrect || op == store.BotControlMemoryForget {
							row, _ := r.db.ReadCorrectionMemory(ctx, p.Payload.Memory.MemoryID)
							if row.Attribution.SupersededBy != "" {
								t.Fatal("source escaped rollback")
							}
						}
						if op == store.BotControlTaskOnceOperation {
							if _, e = r.db.ReadRun(ctx, p.BotID, uuid.NewSHA1(p.ID, []byte("run"))); e == nil {
								t.Fatal("run escaped rollback")
							}
						}
					}
					if mode == "concurrent" {
						var wg sync.WaitGroup
						results := make(chan bool, 2)
						for i := 0; i < 2; i++ {
							wg.Add(1)
							go func() {
								defer wg.Done()
								got, err := r.ConfirmControlProposal(ctx, d)
								results <- err == nil && got != nil && got.Status == store.BotControlApplied
							}()
						}
						wg.Wait()
						close(results)
						succeeded := false
						for ok := range results {
							succeeded = succeeded || ok
						}
						if !succeeded {
							t.Fatal("neither concurrent confirmation applied; serial retry must not hide failure")
						}
					}
					got, err := r.ConfirmControlProposal(ctx, d)
					if err != nil || got.Status != store.BotControlApplied {
						t.Fatalf("apply: %+v %v", got, err)
					}
					replay, err := r.ConfirmControlProposal(ctx, d)
					if err != nil || !reflect.DeepEqual(got, replay) {
						t.Fatalf("replay: %+v %v", replay, err)
					}
					assertRemainingOutcome(t, r, ctx, p)
				})
			}
		}
	}
}
func TestBotControlProposalMemorySourceAndReadinessRace(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, op := range []store.BotControlOperation{store.BotControlMemoryCorrect, store.BotControlMemoryForget} {
			t.Run(string(scope)+"/source/"+string(op), func(t *testing.T) {
				r, ctx, p := remainingProposal(t, scope, op, store.BotActive)
				row, _ := r.db.ReadCorrectionMemory(ctx, p.Payload.Memory.MemoryID)
				if err := r.db.ApplyCorrectionMemory(ctx, uuid.MustParse(row.ID), uuid.MustParse(row.SessionID), row.Snippet, "changed outside proposal", true); err != nil {
					t.Fatal(err)
				}
				_, err := r.ConfirmControlProposal(ctx, store.BotControlProposalDecision{BotID: p.BotID, ProposalID: p.ID, Digest: p.Digest})
				if !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("source fingerprint: %v", err)
				}
				saved, _ := r.ReadControlProposal(ctx, p.BotID, p.ID)
				if saved.Status != store.BotControlProposed {
					t.Fatal("fake applied")
				}
			})
		}
		for _, op := range []store.BotControlOperation{store.BotControlActivate, store.BotControlResume, store.BotControlTaskOnceOperation} {
			t.Run(string(scope)+"/readiness-race/"+string(op), func(t *testing.T) {
				r, ctx, p := remainingProposal(t, scope, op, remainingStatus(op))
				r.activationCheck = func(ctx context.Context, b store.Bot, v store.BotVersion) error {
					// A transaction here proves readiness is outside the decision transaction.
					return r.db.Transaction(ctx, func(tx botDraftDB) error {
						rec, e := tx.GetBot(ctx, b.ID)
						if e != nil {
							return e
						}
						next := rec.Bot
						next.Revision++
						next.UpdatedAt = r.clock()
						if e = tx.SetLifecycle(ctx, *rec, next); e != nil {
							return e
						}
						return tx.InsertActivity(ctx, store.BotActivity{BotID: b.ID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: json.RawMessage(`{}`), CreatedAt: r.clock()}, "readiness-edit")
					})
				}
				got, err := r.ConfirmControlProposal(ctx, store.BotControlProposalDecision{BotID: p.BotID, ProposalID: p.ID, Digest: p.Digest})
				if err != nil || got.Status != store.BotControlStale {
					t.Fatalf("race: %+v %v", got, err)
				}
			})
		}
	}
}

func TestBotControlProposalTaskStatesAndReadiness(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, status := range []store.BotStatus{store.BotDraft, store.BotPaused} {
			t.Run(string(scope)+"/"+string(status), func(t *testing.T) {
				r, ctx, p := remainingProposal(t, scope, store.BotControlTaskOnceOperation, status)
				d := store.BotControlProposalDecision{BotID: p.BotID, ProposalID: p.ID, Digest: p.Digest}
				got, err := r.ConfirmControlProposal(ctx, d)
				if status == store.BotPaused {
					if !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("paused: %v", err)
					}
				} else {
					if err != nil || got.Status != store.BotControlApplied {
						t.Fatalf("draft: %+v %v", got, err)
					}
					assertRemainingOutcome(t, r, ctx, p)
				}
				b, _ := r.Get(ctx, p.BotID)
				if b.Status != status {
					t.Fatal("implicit activation/resume")
				}
			})
		}
		for _, op := range []store.BotControlOperation{store.BotControlTaskOnceOperation, store.BotControlActivate, store.BotControlResume} {
			t.Run(string(scope)+"/readiness/"+string(op), func(t *testing.T) {
				r, ctx, p := remainingProposal(t, scope, op, remainingStatus(op))
				r.activationCheck = nil
				r.auth.readiness = nil
				_, err := r.ConfirmControlProposal(ctx, store.BotControlProposalDecision{BotID: p.BotID, ProposalID: p.ID, Digest: p.Digest})
				if !errors.Is(err, store.ErrBotUnsupported) {
					t.Fatalf("readiness: %v", err)
				}
				saved, _ := r.ReadControlProposal(ctx, p.BotID, p.ID)
				if saved.Status != store.BotControlProposed {
					t.Fatal("fake applied")
				}
			})
		}
	}
}
