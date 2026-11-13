//go:build integration

package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Strict acceptance never skips and never opens a connection unless the caller
// explicitly acknowledges a disposable server. The shared harness owns all
// generated database names and cleanup; it must not point at a shared server.
func requireAgentContinuityPostgres(t *testing.T) {
	t.Helper()
	if os.Getenv("ASTONISH_TEST_DSN") == "" || os.Getenv("ASTONISH_BOTS_TEST_DB_ISOLATED") != "1" {
		t.Fatal("ASTONISH_TEST_DSN and ASTONISH_BOTS_TEST_DB_ISOLATED=1 required: isolated disposable PostgreSQL only")
	}
}

func continuityPostgresRepository(t *testing.T, platform *Store, scope store.BotScope, owner context.Context) *botRepository {
	t.Helper()
	org, err := platform.ForOrg("restorepg")
	if err != nil {
		t.Fatal(err)
	}
	if scope == store.BotScopeTeam {
		return org.ForTeam("ops").Bots().(*teamBotStore).repository()
	}
	principal, ok := execution.PrincipalFromContext(owner)
	if !ok {
		t.Fatal("missing fixture principal")
	}
	return org.ForUser(principal.Subject).Bots().(*personalBotStore).repository()
}

func TestBotAgentPostgresContinuityCASProjection(t *testing.T) {
	requireAgentContinuityPostgres(t)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newPostgresBotServicesFixture(t, scope, 100, 100)
			r := f.repo
			before := continuityAgentWork(t, r, f.owner, f.worker, f.lease)
			beforeSnapshot, err := r.Snapshot(f.owner, f.run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			// Inject a failure after the actual PostgreSQL projection write.
			// The transaction must also undo its sequence and transcript changes.
			var notifications []store.BotRunState
			worker := store.WithBotRunStateNotifier(f.worker, func(_ context.Context, state store.BotRunState) {
				notifications = append(notifications, state)
			})
			original := r.db
			r.db = botProgressFailDB{botDraftDB: original}
			next := before.Clone()
			next.AgentWork.Notes = "Must never become visible"
			_, err = r.SaveWork(worker, store.BotWorkUpdate{Lease: f.lease, ExpectedRevision: before.Revision, State: *next})
			r.db = original
			if !errors.Is(err, errBotProgressFixture) {
				t.Fatalf("expected post-write failure: %v", err)
			}
			after, err := r.LoadWork(f.worker, f.lease)
			if err != nil || !reflect.DeepEqual(before, after) || len(notifications) != 0 {
				t.Fatalf("failed transaction leaked work or notification: %v", err)
			}
			afterSnapshot, err := r.Snapshot(f.owner, f.run.BotID)
			if err != nil || !reflect.DeepEqual(beforeSnapshot, afterSnapshot) {
				t.Fatalf("failed transaction leaked projection or sequence: %v", err)
			}

			type result struct {
				work *store.BotWorkState
				err  error
			}
			results := make(chan result, 2)
			start := make(chan struct{})
			// Only the winning transaction may notify; a buffered channel avoids
			// racing the test's reader or invoking Fatal from a worker goroutine.
			events := make(chan store.BotRunState, 2)
			worker = store.WithBotRunStateNotifier(f.worker, func(_ context.Context, state store.BotRunState) { events <- state })
			for _, note := range []string{"Compare the second source", "Recheck the first source"} {
				go func(note string) {
					<-start
					next := before.Clone()
					next.AgentWork.Notes = note
					w, err := r.SaveWork(worker, store.BotWorkUpdate{Lease: f.lease, ExpectedRevision: before.Revision, State: *next})
					results <- result{w, err}
				}(note)
			}
			close(start)
			wins, conflicts := 0, 0
			var winner *store.BotWorkState
			for range 2 {
				got := <-results
				if got.err == nil {
					wins++
					winner = got.work
				} else if errors.Is(got.err, store.ErrBotConflict) {
					conflicts++
				} else {
					t.Errorf("unexpected CAS error: %v", got.err)
				}
			}
			if wins != 1 || conflicts != 1 || winner == nil || winner.Revision != before.Revision+1 || len(events) != 1 {
				t.Fatalf("CAS winners=%d conflicts=%d notifications=%d", wins, conflicts, len(events))
			}
			event := <-events
			if !reflect.DeepEqual(event.Work, store.BuildBotWorkProjection(winner)) || event.StateSequence <= beforeSnapshot.LatestSequence {
				t.Fatal("winning notification is not the committed projection")
			}
			legacy := store.NewBotWorkState(winner.Contract)
			legacy.Revision, legacy.SkillState = winner.Revision, winner.Clone().SkillState
			if err := legacy.Validate(); err != nil {
				t.Fatalf("invalid downgrade fixture: %v", err)
			}
			if _, err := r.SaveWork(f.worker, store.BotWorkUpdate{Lease: f.lease, ExpectedRevision: winner.Revision, State: *legacy}); err == nil {
				t.Fatal("stored agent protocol downgraded")
			}

			f.platform = reopenPreBotPostgres(t, f.platform)
			r = continuityPostgresRepository(t, f.platform, scope, f.owner)
			saved, err := r.db.ReadRun(f.owner, f.run.BotID, f.run.ID)
			if err != nil || !reflect.DeepEqual(saved.Work, winner) || !reflect.DeepEqual(saved.Run.Work, event.Work) {
				t.Fatalf("reopen lost winning work/projection: %v", err)
			}
			rows, err := messageReviewReader(r).ReadBotTranscript(f.owner, f.run.SessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			matches := 0
			for _, row := range rows {
				value, ok := row.Event.Actions.StateDelta[store.BotRunStateKey]
				if !ok {
					continue
				}
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				var persisted store.BotRunState
				if err := json.Unmarshal(raw, &persisted); err != nil {
					t.Fatal(err)
				}
				if persisted.StateSequence == event.StateSequence {
					matches++
					if !reflect.DeepEqual(persisted, event) {
						t.Fatal("reopened transcript differs from live progress")
					}
				}
			}
			if matches != 1 {
				t.Fatalf("winning transcript records=%d want 1", matches)
			}
		})
	}
}

func assertContinuityPostgresOutboxHeld(t *testing.T, platform *Store, scope store.BotScope, owner context.Context) {
	t.Helper()
	principal, ok := execution.PrincipalFromContext(owner)
	if !ok {
		t.Fatal("missing fixture principal")
	}
	archiveScope := backup.Scope{Kind: "team", OrgSlug: "restorepg", TeamSlug: "ops"}
	if scope == store.BotScopePersonal {
		archiveScope = backup.Scope{Kind: "personal", OrgSlug: "restorepg", UserID: principal.Subject}
	}
	db, err := platform.openRestorePostgresDB("restorepg", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	table := quoteIdent(DialectPostgres, postgresSchemaForScope(archiveScope)) + ".bot_outbox"
	var total, unheld int
	if err := db.QueryRowContext(owner, "SELECT COUNT(*) FROM "+table).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(owner, "SELECT COUNT(*) FROM "+table+" WHERE worker_id <> '' OR lease_expires_at IS NOT NULL OR (delivered_at IS NULL AND available_at < '9999-12-31T23:59:59Z')").Scan(&unheld); err != nil {
		t.Fatal(err)
	}
	if total == 0 || unheld != 0 {
		t.Fatalf("restored outbox total=%d unheld=%d", total, unheld)
	}
}

func TestBotAgentPostgresContinuityRestoreQuiescence(t *testing.T) {
	requireAgentContinuityPostgres(t)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, legacy := range []bool{false, true} {
			protocol := "agent"
			if legacy {
				protocol = "legacy"
			}
			for _, waiting := range []bool{false, true} {
				state := "running"
				if waiting {
					state = "waiting"
				}
				t.Run(string(scope)+"/"+protocol+"/"+state, func(t *testing.T) {
					f := newPostgresBotServicesFixture(t, scope, 100, 100)
					r := f.repo
					var before *store.BotWorkState
					if legacy {
						pinLegacyWorkFixture(t, r, f.worker, f.lease)
						var err error
						before, err = r.LoadWork(f.worker, f.lease)
						if err != nil {
							t.Fatal(err)
						}
					} else {
						before = continuityAgentWork(t, r, f.owner, f.worker, f.lease)
					}
					var approvalID uuid.UUID
					if waiting {
						action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: []byte(`{"body":"exact"}`)}
						digest, err := action.Digest()
						if err != nil {
							t.Fatal(err)
						}
						a, err := r.RequestApproval(f.worker, f.lease, store.BotApproval{BotID: f.run.BotID, RunID: f.run.ID, Version: f.run.Version, Action: action, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
						if err != nil {
							t.Fatal(err)
						}
						approvalID = a.ID
						checkpoint, err := json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: a.ID, BotID: f.run.BotID, RunID: f.run.ID, SessionID: f.run.SessionID, Version: f.run.Version, ActionDigest: digest, Fence: f.lease.Fence})
						if err != nil {
							t.Fatal(err)
						}
						if err := r.CommitRun(f.worker, store.BotRunCommit{Lease: f.lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Summary: "Waiting for exact action", Work: store.BuildBotWorkProjection(before), NeedsApproval: []uuid.UUID{a.ID}}}); err != nil {
							t.Fatal(err)
						}
					}
					// Ordinary reopen must retain the selected protocol and journal.
					f.platform = reopenPreBotPostgres(t, f.platform)
					r = continuityPostgresRepository(t, f.platform, scope, f.owner)
					sourceRun, err := r.db.ReadRun(f.owner, f.run.BotID, f.run.ID)
					if err != nil || !reflect.DeepEqual(sourceRun.Work, before) {
						t.Fatalf("source reopen changed work: %v", err)
					}
					archive := filepath.Join(t.TempDir(), "continuity.astonish-backup")
					if err := f.platform.ExportPlatformBackup(context.Background(), archive, PlatformBackupExportOptions{Backend: "postgres"}); err != nil {
						t.Fatal(err)
					}
					if _, err := backup.Verify(archive); err != nil {
						t.Fatal(err)
					}
					target := setupPostgresBackupRestoreStore(t, "agent_continuity")
					if _, err := target.RestorePlatformBackup(context.Background(), archive, PlatformRestoreOptions{IncludeTransient: true, EnableScheduledJobs: true}); err != nil {
						t.Fatal(err)
					}
					for range 2 {
						target = reopenPreBotPostgres(t, target)
						r = continuityPostgresRepository(t, target, scope, f.owner)
						saved, err := r.db.ReadRun(f.owner, f.run.BotID, f.run.ID)
						if err != nil {
							t.Fatal(err)
						}
						if saved.Work.Protocol != before.Protocol || saved.Work.SchemaVersion != before.SchemaVersion {
							t.Fatal("restore relabeled historical work")
						}
						if legacy {
							if saved.Work.AgentWork != nil || !reflect.DeepEqual(saved.Work.Contract, before.Contract) || saved.Work.Phase != store.BotWorkInterrupted {
								t.Fatal("legacy contract not retained quiescently")
							}
						} else {
							assertContinuityWork(t, before, saved.Work, store.BotWorkInterrupted)
						}
						if saved.Run.Status != store.RunInterrupted || saved.Run.Lease.WorkerID != "" || !saved.Run.Lease.ExpiresAt.IsZero() || saved.Run.Lease.Fence <= f.lease.Fence {
							t.Fatalf("restored execution authority: %+v", saved.Run.Lease)
						}
						if !reflect.DeepEqual(saved.Run.Work, store.BuildBotWorkProjection(saved.Work)) || waiting && !reflect.DeepEqual(saved.Run.Report.Work, saved.Run.Work) {
							t.Fatal("restored work and report projections differ")
						}
						root, err := r.Get(f.owner, f.run.BotID)
						if err != nil || root.Status != store.BotPaused {
							t.Fatalf("restored Bot not paused: %+v %v", root, err)
						}
						if waiting {
							a, err := r.db.ReadApproval(f.owner, f.run.BotID, approvalID)
							if err != nil || a.Status != store.ApprovalRevoked {
								t.Fatalf("restored approval remains actionable: %+v %v", a, err)
							}
						}
						assertContinuityPostgresOutboxHeld(t, target, scope, f.owner)
						if _, err := r.SaveWork(f.worker, store.BotWorkUpdate{Lease: f.lease, ExpectedRevision: saved.Work.Revision, State: *saved.Work}); !errors.Is(err, store.ErrBotConflict) {
							t.Fatalf("late worker write accepted: %v", err)
						}
						if _, err := r.ClaimRun(f.worker, store.BotClaimRequest{BotID: f.run.BotID, RunID: f.run.ID, WorkerID: "automatic-replay", ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, store.ErrBotConflict) {
							t.Fatalf("restore permitted automatic replay: %v", err)
						}
					}
				})
			}
		}
	}
}
