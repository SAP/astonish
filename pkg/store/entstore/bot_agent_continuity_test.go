package entstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func continuityAgentWork(t *testing.T, r *botRepository, owner, worker context.Context, lease store.BotLease) *store.BotWorkState {
	t.Helper()
	v := skillTestRevision(t, r, owner, lease.BotID)
	w, err := r.LoadWork(worker, lease)
	if err != nil {
		t.Fatal(err)
	}
	oriented, err := r.OrientSkill(worker, store.BotSkillOrientation{Kind: store.BotSkillCreateDraft, Key: "continuity", Title: "Continuity", Intent: "Preserve a method", Draft: &v, Mutation: store.BotSkillMutation{BotID: lease.BotID, Lease: lease, ExpectedRevision: w.Revision, IdempotencyKey: "continuity"}})
	if err != nil {
		t.Fatal(err)
	}
	w = oriented.Work.Clone()
	digest := strings.Repeat("a", 64)
	itemID := uuid.New()
	w.AgentWork.Notes = "Private working notes survive interruption."
	w.AgentWork.Evidence = []store.BotWorkEvidenceReference{{Ref: "capture:source", RunID: lease.RunID, EventID: "event", CallID: "read", Digest: digest}}
	w.AgentWork.SourceAccess = []store.BotSourceAccessFact{{EvidenceRef: "capture:source", CallID: "read", OriginalURL: "https://example.test/source", Status: store.BotSourceAccessReturned, HTTPStatus: 200, ObservedAt: time.Now().UTC(), BodyDigest: digest}}
	w.AgentWork.Items = []store.BotWorkItem{{ID: itemID, Summary: "Compare sources", Status: store.BotWorkItemInProgress, EvidenceRefs: []string{"capture:source"}}}
	w.AgentWork.Remaining = []store.BotWorkObligation{{ID: uuid.New(), SourceRunID: lease.RunID, WorkItemID: &itemID, Summary: "Verify second source", Reason: "Not yet read", Continuation: store.BotWorkContinueOwnerRequest}}
	w.AgentWork.Checks = []store.BotWorkCheckReceipt{{ID: uuid.New(), CallID: "check", Kind: store.BotWorkCheckModelFeedback, Status: store.BotWorkCheckReturned, Judgment: store.BotWorkCheckNeedsWork, Summary: "Needs a second source", DraftDigest: digest, ObservedAt: time.Now().UTC(), EvidenceRefs: []string{"capture:source"}}}
	w.AgentWork.Calls = []store.BotWorkCallReceipt{{CallID: "uncertain-check", RequestDigest: digest, Revision: w.Revision, Check: true}}
	w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "candidate", Text: "Useful partial draft"}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	w, err = r.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: w.Revision, State: *w})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func assertContinuityWork(t *testing.T, before, after *store.BotWorkState, phase store.BotWorkPhase) {
	t.Helper()
	if after == nil || after.Phase != phase {
		t.Fatalf("work phase: %+v; want %s", after, phase)
	}
	if err := after.Validate(); err != nil {
		t.Fatalf("invalid saved work: %v", err)
	}
	if !reflect.DeepEqual(before.Contract, after.Contract) || !reflect.DeepEqual(before.SkillState, after.SkillState) || !reflect.DeepEqual(before.Candidate, after.Candidate) {
		t.Fatal("lost contract, skill or candidate")
	}
	expected := before.AgentWork
	actual := after.AgentWork
	if expected.Notes != actual.Notes || !reflect.DeepEqual(expected.Remaining, actual.Remaining) || !reflect.DeepEqual(expected.Evidence, actual.Evidence) || !reflect.DeepEqual(expected.SourceAccess, actual.SourceAccess) || !reflect.DeepEqual(expected.Checks, actual.Checks) || !reflect.DeepEqual(expected.Calls, actual.Calls) {
		t.Fatal("lost notes, obligations, source/check journal or uncertain call reservation")
	}
	if phase == store.BotWorkInterrupted && actual.Items[0].Status != store.BotWorkItemBlocked {
		t.Fatal("interrupted item still running")
	}
}

func TestBotAgentContinuityInterruption(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, operation := range []string{"cancel", "recover", "waiting-cancel", "finished-recover", "finished-cancel"} {
			t.Run(string(scope)+"/"+operation, func(t *testing.T) {
				r, owner, worker, run, lease, now := botRunFixture(t, scope)
				before := continuityAgentWork(t, r, owner, worker, lease)
				phase := store.BotWorkInterrupted
				if strings.HasPrefix(operation, "finished-") {
					next := before.Clone()
					next.Phase = store.BotWorkFinished
					next.AgentWork.Disposition = store.AgentWorkPartial
					next.AgentWork.Items[0].Status = store.BotWorkItemBlocked
					next.AgentWork.Items[0].Reason = "Second source unavailable"
					var err error
					before, err = r.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: before.Revision, State: *next})
					if err != nil {
						t.Fatal(err)
					}
					phase = store.BotWorkFinished
				}
				if operation == "waiting-cancel" {
					action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: []byte(`{}`)}
					digest, _ := action.Digest()
					a, err := r.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
					if err != nil {
						t.Fatal(err)
					}
					if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: []byte(`{"step":1}`), Report: store.BotReport{Summary: "Waiting", Work: store.BuildBotWorkProjection(before), NeedsApproval: []uuid.UUID{a.ID}}}); err != nil {
						t.Fatal(err)
					}
				}
				var notifications []store.BotRunState
				owner = store.WithBotRunStateNotifier(owner, func(_ context.Context, state store.BotRunState) { notifications = append(notifications, state) })
				if strings.HasSuffix(operation, "recover") {
					*now = lease.ExpiresAt.Add(time.Second)
					if err := r.RecoverRun(owner, lease); err != nil {
						t.Fatal(err)
					}
				} else if _, err := r.CancelRun(owner, run.BotID, run.ID); err != nil {
					t.Fatal(err)
				}
				saved, err := r.db.ReadRun(owner, run.BotID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				assertContinuityWork(t, before, saved.Work, phase)
				if len(notifications) != 1 || !reflect.DeepEqual(notifications[0].Work, saved.Run.Work) || notifications[0].Status != saved.Run.Status {
					t.Fatal("live interruption projection differs from durable run")
				}
				if !reflect.DeepEqual(saved.Run.Work, store.BuildBotWorkProjection(saved.Work)) {
					t.Fatal("stale persisted run projection")
				}
				if saved.Run.Report.Work != nil && !reflect.DeepEqual(saved.Run.Report.Work, saved.Run.Work) {
					t.Fatal("stale report projection")
				}
				if _, err = r.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: saved.Work.Revision, State: *saved.Work}); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("late write: %v", err)
				}
				if _, err = r.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "replay", ExpiresAt: now.Add(time.Minute)}); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("automatic replay: %v", err)
				}
			})
		}
	}
}

func TestBotAgentContinuityApprovalAndNoDowngrade(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, worker, run, lease, now := botRunFixture(t, scope)
			before := continuityAgentWork(t, r, owner, worker, lease)
			legacy := store.NewBotWorkState(before.Contract)
			legacy.Revision = before.Revision
			legacy.SkillState = before.Clone().SkillState
			if err := legacy.Validate(); err != nil {
				t.Fatalf("legacy downgrade fixture must otherwise be valid: %v", err)
			}
			if _, err := r.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: before.Revision, State: *legacy}); err == nil {
				t.Fatal("agent protocol downgraded")
			}
			action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: []byte(`{"body":"exact"}`)}
			digest, _ := action.Digest()
			a, err := r.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: a.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
			if err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: raw, Report: store.BotReport{Summary: "Waiting", Work: store.BuildBotWorkProjection(before), NeedsApproval: []uuid.UUID{a.ID}}}); err != nil {
				t.Fatal(err)
			}
			b, err := r.Get(owner, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.DecideApproval(owner, store.BotApprovalDecision{BotID: b.ID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: digest, Status: store.ApprovalApproved}); err != nil {
				t.Fatal(err)
			}
			if err = r.RequeueApprovedRun(owner, b.ID, run.ID, a.ID); err != nil {
				t.Fatal(err)
			}
			fresh, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "authorized", ExpiresAt: now.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			after, err := r.LoadWork(worker, fresh)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("resume changed protocol, pin or journal: %v", err)
			}
			if err = r.ConsumeApproval(worker, store.BotApprovalConsumption{Lease: fresh, ApprovalID: a.ID, Version: run.Version, ActionDigest: strings.Repeat("f", 64)}); err == nil {
				t.Fatal("foreign action consumed approval")
			}
			consume := store.BotApprovalConsumption{Lease: fresh, ApprovalID: a.ID, Version: run.Version, ActionDigest: digest}
			if err = r.ConsumeApproval(worker, consume); err != nil {
				t.Fatal(err)
			}
			if err = r.ConsumeApproval(worker, consume); err == nil {
				t.Fatal("approved action replayed")
			}
		})
	}
}

func TestBotAgentContinuityRestoreRejectsUnknownWork(t *testing.T) {
	for _, mode := range []string{"protocol", "schema"} {
		t.Run(mode, func(t *testing.T) {
			work := store.NewBotAgentWorkState(store.BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: "Inspect sources"})
			if mode == "protocol" {
				work.Protocol = "future_protocol"
			} else {
				work.SchemaVersion++
			}
			data, err := botJSONMap(botStoredRun{Work: work})
			if err != nil {
				t.Fatal(err)
			}
			row := map[string]any{"id": work.Contract.RunID.String(), "bot_id": work.Contract.BotID.String(), "fence": 1, "status": string(store.RunRunning), "data": data}
			if err := quiesceRestoredBotRow("bot_runs", row); err == nil {
				t.Fatal("unknown work accepted on restore")
			}
		})
	}
}

// Exercise the importer and an actual connection reopen, not only row conversion.
func TestBotAgentContinuitySQLiteRestoreReopen(t *testing.T) {
	r, owner, worker, run, lease, _ := botRunFixture(t, store.BotScopePersonal)
	before := continuityAgentWork(t, r, owner, worker, lease)
	for _, finished := range []bool{false, true} {
		name := "working"
		if finished {
			name = "finished"
		}
		t.Run(name, func(t *testing.T) {
			saved, err := r.db.ReadRun(owner, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if finished {
				saved.Work.Phase = store.BotWorkFinished
				saved.Work.AgentWork.Disposition = store.AgentWorkPartial
				saved.Work.AgentWork.Items[0].Status = store.BotWorkItemBlocked
				saved.Work.AgentWork.Items[0].Reason = "Awaiting second source"
			}
			saved.Run.Report.Work = store.BuildBotWorkProjection(saved.Work)
			data, err := botJSONMap(saved)
			if err != nil {
				t.Fatal(err)
			}
			row := map[string]any{"id": run.ID.String(), "bot_id": run.BotID.String(), "fence": lease.Fence, "status": string(store.RunRunning), "data": data}
			var archive strings.Builder
			if err := backup.NewRecordWriter(&archive, "bot_runs").Write(run.ID.String(), row); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "restore.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if _, err := db.Exec(`CREATE TABLE bot_runs (id TEXT PRIMARY KEY, bot_id TEXT, fence INTEGER, status TEXT, worker_id TEXT, lease_expires_at TEXT, data TEXT)`); err != nil {
				t.Fatal(err)
			}
			scope := backup.Scope{Kind: "personal", OrgSlug: "org", UserID: r.auth.userID}
			entry := backup.Entry{Entity: "bot_runs", Path: "runs", Kind: "jsonl", Scope: scope}
			if err := restoreLogicalEntries(owner, db, DialectSQLite, "", scope, map[string][]byte{"runs": []byte(archive.String())}, []backup.Entry{entry}, PlatformRestoreOptions{IncludeTransient: true, EnableScheduledJobs: true}, &backup.RestoreResult{}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			var raw, status, workerID string
			var fence int64
			var expiry sql.NullString
			if err := db.QueryRow(`SELECT data, status, fence, worker_id, lease_expires_at FROM bot_runs WHERE id=?`, run.ID.String()).Scan(&raw, &status, &fence, &workerID, &expiry); err != nil {
				t.Fatal(err)
			}
			doc, _, err := restoredBotStoredDocument(raw)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			var restored botStoredRun
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			phase := store.BotWorkInterrupted
			if finished {
				phase = store.BotWorkFinished
			}
			assertContinuityWork(t, before, restored.Work, phase)
			if status != string(store.RunInterrupted) || restored.Run.Status != store.RunInterrupted || fence <= lease.Fence || workerID != "" || expiry.Valid {
				t.Fatal("restored run retained execution authority")
			}
			if !reflect.DeepEqual(restored.Run.Work, store.BuildBotWorkProjection(restored.Work)) || !reflect.DeepEqual(restored.Run.Work, restored.Run.Report.Work) {
				t.Fatal("persisted restore projections differ")
			}
		})
	}
}

func TestBotAgentContinuityRestoreWork(t *testing.T) {
	r, owner, worker, run, lease, _ := botRunFixture(t, store.BotScopePersonal)
	before := continuityAgentWork(t, r, owner, worker, lease)
	for _, status := range []store.RunStatus{store.RunRunning, store.RunWaitingApproval, store.RunQueued, store.RunSucceeded} {
		t.Run(string(status), func(t *testing.T) {
			saved, err := r.db.ReadRun(owner, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			saved.Run.Status = status
			saved.Run.Report.Work = store.BuildBotWorkProjection(saved.Work)
			data, err := botJSONMap(saved)
			if err != nil {
				t.Fatal(err)
			}
			row := map[string]any{"id": run.ID.String(), "bot_id": run.BotID.String(), "fence": lease.Fence, "status": string(status), "data": data}
			if err = quiesceRestoredBotRow("bot_runs", row); err != nil {
				t.Fatal(err)
			}
			doc, _, err := restoredBotStoredDocument(row["data"])
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(doc)
			var restored botStoredRun
			if err = json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			expected := store.BotWorkInterrupted
			if status == store.RunSucceeded {
				expected = before.Phase
			}
			assertContinuityWork(t, before, restored.Work, expected)
			if !reflect.DeepEqual(restored.Run.Work, store.BuildBotWorkProjection(restored.Work)) || !reflect.DeepEqual(restored.Run.Work, restored.Run.Report.Work) {
				t.Fatal("restore projection mismatch")
			}
		})
	}
}
