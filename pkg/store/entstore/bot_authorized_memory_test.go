package entstore

import (
	"context"
	"encoding/json"
	"entgo.io/ent"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"reflect"
	"testing"
	"time"
)

// The supported routine memory target is the same scoped Ent database. There is
// deliberately no fake external replica/pending reconciliation receipt here.
func TestRoutineMemorySearchCorrectionAndForget(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			_, bots, memories, ctx, b, peer := botMemorySearchFixture(t, scope, "fts")
			var db botDraftDB
			switch s := bots.(type) {
			case *personalBotStore:
				db = personalBotDB{client: s.client}
			case *teamBotStore:
				db = teamBotDB{client: s.client}
			}
			ctx = context.WithValue(ctx, routineMutationKey{}, true)
			search := func(text string, count int) {
				t.Helper()
				current, err := memories.(store.BotMemoryPrefilterStore).SearchBySessions(ctx, []string{b.HomeSessionID.String()}, text, 1, 0, "")
				if err != nil || len(current) != count {
					t.Errorf("ordinary Bot recall %q: %+v %v (want %d)", text, current, err, count)
				}
				rows, err := memories.(store.AttributedMemoryStore).SearchAttributed(ctx, []string{b.HomeSessionID.String()}, store.MemoryAttributionFilter{BotID: b.ID}, text, 10, 0, "")
				if err != nil || len(rows) != count {
					t.Fatalf("search %q: %+v %v (want %d)", text, rows, err, count)
				}
			}
			apply := func(op store.BotControlOperation, source, text string) store.BotControlProposal {
				t.Helper()
				p := store.BotControlProposal{ID: uuid.New(), BotID: b.ID, SourceUserID: b.OwnerID, Payload: store.BotControlPayload{Operation: op, Memory: &store.BotControlMemory{Content: text, Category: "fact"}}}
				if source != "" {
					row, err := memories.Get(ctx, source)
					if err != nil {
						t.Fatal(err)
					}
					p.Payload.Memory.MemoryID = source
					p.Payload.Memory.ExpectedFingerprint = store.MemoryContentFingerprint(*row)
				}
				if op == store.BotControlMemoryForget {
					p.Payload.Memory.Category = ""
				}
				if err := db.Transaction(ctx, func(tx botDraftDB) error {
					if err := mutateControlMemory(ctx, tx, p, b.HomeSessionID, b.OwnerID, time.Now()); err != nil {
						return err
					}
					return verifyControlMemoryMutation(ctx, tx, p)
				}); err != nil {
					t.Fatal(err)
				}
				return p
			}
			first := apply(store.BotControlMemoryRemember, "", "Thursday")
			firstID := uuid.NewSHA1(first.ID, []byte("memory")).String()
			search("Thursday", 1)
			correction := apply(store.BotControlMemoryCorrect, firstID, "Friday")
			correctedID := uuid.NewSHA1(correction.ID, []byte("memory")).String()
			search("Thursday", 0)
			search("Friday", 1)
			old, err := memories.Get(ctx, firstID)
			if err != nil || old.Snippet != "Thursday" || old.Attribution.SupersededBy != "routine-operation:"+correction.ID.String() {
				t.Fatalf("source evidence: %+v %v", old, err)
			}
			if err := memories.Add(ctx, store.MemoryEntry{Content: "peer unchanged", SessionID: peer.HomeSessionID.String()}); err != nil {
				t.Fatal(err)
			}
			peerBefore, err := memories.ListBySession(ctx, peer.HomeSessionID.String())
			if err != nil {
				t.Fatal(err)
			}
			targetBefore, err := memories.Get(ctx, correctedID)
			if err != nil || targetBefore == nil {
				t.Fatalf("target before: %+v %v", targetBefore, err)
			}
			forgot := apply(store.BotControlMemoryForget, correctedID, "")
			targetAfter, err := memories.Get(ctx, correctedID)
			expected := *targetBefore
			attr := *expected.Attribution
			attr.SupersededBy = "routine-operation:" + forgot.ID.String()
			expected.Attribution = &attr
			if err != nil || targetAfter == nil || !reflect.DeepEqual(expected, *targetAfter) {
				t.Fatalf("exact tombstone/evidence: %+v %v", targetAfter, err)
			}
			oldAfter, err := memories.Get(ctx, firstID)
			if err != nil || !reflect.DeepEqual(old, oldAfter) {
				t.Fatalf("neighbor history changed: %+v %v", oldAfter, err)
			}
			peerAfter, err := memories.ListBySession(ctx, peer.HomeSessionID.String())
			if err != nil || !reflect.DeepEqual(peerBefore, peerAfter) {
				t.Fatalf("peer changed: %+v %v", peerAfter, err)
			}
			search("Thursday", 0)
			search("Friday", 0)
			all, err := memories.ListBySession(ctx, b.HomeSessionID.String())
			if err != nil || len(all) != 2 {
				t.Fatalf("evidence rows: %+v %v", all, err)
			}
		})
	}
}

func TestRoutineMemoryReceiptAtomicReplayAndDenial(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, tc := range []struct {
			op   store.BotControlOperation
			deny bool
		}{{store.BotControlMemoryRemember, false}, {store.BotControlMemoryCorrect, false}, {store.BotControlMemoryForget, false}, {store.BotControlMemoryForget, true}} {
			op := tc.op
			name := string(scope) + "/" + string(op)
			if tc.deny {
				name += "/denied"
			}
			t.Run(name, func(t *testing.T) {
				r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
				policy := s.policy
				s.policy = func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
					p, err := policy(ctx, ref)
					p.ExceptionConstraints = append(p.ExceptionConstraints, execution.BotActionResourceConstraint{Action: execution.CapabilityToolExecute, Resources: []string{RoutineControlResource(op)}})
					return p, err
				}
				b, err := r.Get(owner, q.MandateRef.BotID)
				if err != nil {
					t.Fatal(err)
				}
				run, err := r.GetRun(owner, b.ID, q.Lease.RunID)
				if err != nil {
					t.Fatal(err)
				}
				memory := &store.BotControlMemory{ExpectedRevision: b.Revision, Content: "Friday", Category: "fact"}
				if op != store.BotControlMemoryRemember {
					id := uuid.New()
					if err := insertControlMemory(owner, r.db, id, run.SessionID, b.OwnerID, "Thursday", "fact", nil, time.Now()); err != nil {
						t.Fatal(err)
					}
					row, err := r.db.ReadCorrectionMemory(owner, id.String())
					if err != nil {
						t.Fatal(err)
					}
					memory.MemoryID = id.String()
					memory.ExpectedFingerprint = store.MemoryContentFingerprint(*row)
				}
				if op == store.BotControlMemoryForget {
					memory.Content = ""
					memory.Category = ""
				}
				q.Payload = store.BotControlPayload{Operation: op, Reason: "requested memory change", Memory: memory}
				o, err := s.EnqueueAuthorizedOperation(worker, q)
				if err != nil {
					t.Fatal(err)
				}
				if o.Status != store.BotOperationBlocked {
					t.Fatalf("expected exact exception: %+v", o)
				}
				// Denied forget must neither mutate nor re-open under another narration/key.
				if tc.deny {
					_, err = s.DecideRoutineException(owner, BotRoutineExceptionDecision{MandateRef: q.MandateRef, OperationID: o.ID, ExpectedRevision: o.Revision, PayloadDigest: o.Provenance.PayloadDigest})
					if err != nil {
						t.Fatal(err)
					}
					denied := q
					denied.IdempotencyKey = "denied-replay"
					denied.Payload.Reason = "different narration"
					if _, err = s.EnqueueAuthorizedOperation(worker, denied); !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("denial replay: %v", err)
					}
					row, err := r.db.ReadCorrectionMemory(owner, memory.MemoryID)
					if err != nil || row.Attribution != nil {
						t.Fatalf("denied forget mutated: %+v %v", row, err)
					}
					return
				}
				o, err = s.DecideRoutineException(owner, BotRoutineExceptionDecision{MandateRef: q.MandateRef, OperationID: o.ID, ExpectedRevision: o.Revision, PayloadDigest: o.Provenance.PayloadDigest, Approve: true, ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, q.Lease, "queued")}); err != nil {
					t.Fatal(err)
				}
				fail := true
				hook := func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
						if fail {
							return nil, errors.New("receipt unavailable")
						}
						return next.Mutate(ctx, m)
					})
				}
				switch d := r.db.(type) {
				case personalBotDB:
					d.client.BotOutbox.Use(hook)
				case teamBotDB:
					d.client.BotOutbox.Use(hook)
				}
				if failed, err := s.ApplyAuthorizedOperation(owner, q.MandateRef, o.ID); err == nil || failed != nil {
					t.Fatalf("failed transaction returned success: %+v %v", failed, err)
				}
				persisted, err := s.ReadAuthorizedOperation(owner, q.MandateRef, o.ID)
				if err != nil || persisted.Status != store.BotOperationPending || persisted.Receipt != nil {
					t.Fatalf("partial receipt: %+v %v", persisted, err)
				}
				id := uuid.NewSHA1(o.ID, []byte("memory")).String()
				if row, err := r.db.ReadCorrectionMemory(owner, id); err != nil || row != nil {
					t.Fatalf("partial memory: %+v %v", row, err)
				}
				fail = false
				applied, err := s.ApplyAuthorizedOperation(owner, q.MandateRef, o.ID)
				if err != nil {
					t.Fatal(err)
				}
				if applied.Status != store.BotOperationApplied || applied.Receipt.Validate() != nil {
					t.Fatalf("receipt: %+v", applied)
				}
				before, err := r.db.GetBot(owner, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					again, err := s.ApplyAuthorizedOperation(owner, q.MandateRef, o.ID)
					if err != nil || !reflect.DeepEqual(applied, again) {
						t.Fatalf("replay: %+v %v", again, err)
					}
				}
				after, err := r.db.GetBot(owner, b.ID)
				if err != nil || before.LastSequence != after.LastSequence {
					t.Fatal("duplicate mutation/receipt")
				}
				if op == store.BotControlMemoryForget {
					row, err := r.db.ReadCorrectionMemory(owner, memory.MemoryID)
					if err != nil || row == nil || row.Attribution == nil || row.Attribution.SupersededBy != "routine-operation:"+o.ID.String() {
						t.Fatalf("forgotten source: %+v %v", row, err)
					}
					return
				}
				var metadata map[string]any
				switch d := r.db.(type) {
				case personalBotDB:
					row, e := d.client.Memory.Get(owner, uuid.MustParse(id))
					if e != nil {
						t.Fatal(e)
					}
					metadata = row.Metadata
				case teamBotDB:
					row, e := d.client.Memory.Get(owner, uuid.MustParse(id))
					if e != nil {
						t.Fatal(e)
					}
					metadata = row.Metadata
				}
				if metadata["reviewed_by"] != nil || metadata["proposal_id"] != nil || metadata["operation_id"] != o.ID.String() {
					t.Fatalf("human-review attribution: %+v", metadata)
				}
				activity, err := r.db.FindActivity(owner, b.ID, "control-memory:"+o.ID.String())
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				if err = json.Unmarshal(activity.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload["actor_id"] != nil || payload["proposal_id"] != nil || payload["authorization"] != "mandate" {
					t.Fatalf("human-review activity: %+v", payload)
				}
			})
		}
	}
}
