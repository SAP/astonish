//go:build integration

package entstore

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Unlike the optional historical PG suites, these requested checks must not skip.
func requireReviewedPostgres(t *testing.T) {
	t.Helper()
	if os.Getenv("ASTONISH_TEST_DSN") == "" {
		t.Fatal("ASTONISH_TEST_DSN must name an isolated disposable PostgreSQL server")
	}
}

// Close every connection and reopen through the production router, not just a
// fresh repository over the same client. The original harness still owns DB cleanup.
func (f *postgresBotServicesFixture) reopenReviewed(t *testing.T) {
	t.Helper()
	cfg := Config{DSN: f.platform.platformDSN, InstanceSuffix: f.platform.instanceSuffix}
	closePostgresFixtureStore(t, f.platform)
	s, err := New(f.owner, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePostgresFixtureStore(t, s) })
	f.platform = s
	p, ok := execution.PrincipalFromContext(f.owner)
	if !ok {
		t.Fatal("reopen: missing authenticated owner principal")
	}
	org, err := s.ForOrg(p.OrgSlug)
	if err != nil {
		t.Fatalf("reopen org %q: %v", p.OrgSlug, err)
	}
	if f.scope == store.BotScopeTeam {
		routed := org.ForTeam(p.TeamSlug)
		if routed == nil {
			t.Fatalf("reopen team %q/%q failed; see router error above", p.OrgSlug, p.TeamSlug)
		}
		f.bots = routed.Bots()
		repo, ok := f.bots.(*teamBotStore)
		if !ok || repo == nil {
			t.Fatalf("reopen: unexpected team bot store %T", f.bots)
		}
		f.repo = repo.repository()
	} else {
		routed := org.ForUser(p.Subject)
		if routed == nil {
			t.Fatalf("reopen personal %q/%q failed; see router error above", p.OrgSlug, p.Subject)
		}
		f.bots = routed.Bots()
		repo, ok := f.bots.(*personalBotStore)
		if !ok || repo == nil {
			t.Fatalf("reopen: unexpected personal bot store %T", f.bots)
		}
		f.repo = repo.repository()
	}
}

func postgresReviewedRace(t *testing.T, fn func() error) {
	t.Helper()
	const n = 12
	start := make(chan struct{})
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; errs <- fn() }()
	}
	close(start)
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, store.ErrBotConflict) {
			t.Errorf("concurrent replay: %v", err)
		}
	}
	if successes == 0 {
		t.Fatal("no concurrent request committed")
	}
	// Optimistic sequence losers must be safely replayable after the winner.
	for range n {
		if err := fn(); err != nil {
			t.Fatalf("post-race replay: %v", err)
		}
	}
}

func TestIntegrationPostgresBotControlProposals(t *testing.T) {
	requireReviewedPostgres(t)
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		for _, scenario := range []string{"concurrent-replay-reopen", "rollback", "stale"} {
			t.Run(scenario, func(t *testing.T) {
				f := newPostgresBotServicesFixture(t, scope, 100, 100)
				b, err := f.repo.Get(f.owner, f.run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				q := store.BotControlProposalRequest{Lease: f.lease, IdempotencyKey: "pg-review", ExpiresAt: time.Now().Add(time.Hour), Payload: store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "review daily schedule", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 8 * * *", Timezone: "UTC", Instructions: "check", Enabled: true}}}}
				p, err := f.repo.ProposeControl(f.worker, q)
				if err != nil {
					t.Fatal(err)
				}
				duplicate, err := f.repo.ProposeControl(f.worker, q)
				if err != nil || duplicate.ID != p.ID {
					t.Fatalf("proposal replay: %+v %v", duplicate, err)
				}
				d := store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest}
				if _, err = f.repo.ConfirmControlProposal(f.owner, d); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("live source: %v", err)
				}
				if err = f.repo.CommitRun(f.worker, store.BotRunCommit{Lease: f.lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "review ready"}}); err != nil {
					t.Fatal(err)
				}
				f.reopenReviewed(t)
				saved, err := f.repo.ReadControlProposal(f.owner, b.ID, p.ID)
				if err != nil || saved.Status != store.BotControlProposed {
					t.Fatalf("pending reopen: %+v %v", saved, err)
				}
				if scenario == "stale" {
					if _, err = f.repo.Pause(f.owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "changed"}); err != nil {
						t.Fatal(err)
					}
					stale, e := f.repo.ConfirmControlProposal(f.owner, d)
					if e != nil || stale.Status != store.BotControlStale {
						t.Fatalf("stale proposal: %+v %v", stale, e)
					}
					after, e := f.repo.Get(f.owner, b.ID)
					if e != nil || after.Version != b.Version {
						t.Fatalf("stale mutated version: %+v %v", after, e)
					}
					return
				}
				if scenario == "rollback" {
					original := f.repo.db
					before, e := original.GetBot(f.owner, b.ID)
					if e != nil {
						t.Fatal(e)
					}
					f.repo.db = controlReceiptFailTransaction{original}
					_, err = f.repo.ConfirmControlProposal(f.owner, d)
					f.repo.db = original
					if err == nil {
						t.Fatal("missing receipt failure")
					}
					after, e := original.GetBot(f.owner, b.ID)
					if e != nil || after.Bot.Version != b.Version || after.LastSequence != before.LastSequence {
						t.Fatalf("mutation escaped rollback: %+v %v", after, e)
					}
					if original.(botTriggerDB).triggerScheduler().Get(f.owner, uuid.NewSHA1(p.ID, []byte("schedule")).String()) != nil {
						t.Fatal("scheduler escaped rollback")
					}
					saved, e := f.repo.ReadControlProposal(f.owner, b.ID, p.ID)
					if e != nil || saved.Status != store.BotControlProposed {
						t.Fatalf("receipt escaped rollback: %+v %v", saved, e)
					}
				}
				postgresReviewedRace(t, func() error { _, e := f.repo.ConfirmControlProposal(f.owner, d); return e })
				applied, err := f.repo.ReadControlProposal(f.owner, b.ID, p.ID)
				if err != nil || applied.Status != store.BotControlApplied || applied.DecidedAt == nil {
					t.Fatalf("receipt: %+v %v", applied, err)
				}
				activities, err := f.repo.db.ReadActivity(f.owner, b.ID, 0, 200)
				if err != nil {
					t.Fatal(err)
				}
				receipts := 0
				for _, activity := range activities {
					var envelope controlProposalEnvelope
					if json.Unmarshal(activity.Payload, &envelope) == nil && envelope.Record == controlProposalRecord && envelope.Proposal.ID == p.ID && envelope.Proposal.Status == store.BotControlApplied {
						receipts++
					}
				}
				if receipts != 1 {
					t.Fatalf("applied receipt count=%d want 1", receipts)
				}
				before, err := f.repo.db.GetBot(f.owner, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				f.reopenReviewed(t)
				replay, err := f.repo.ConfirmControlProposal(f.owner, d)
				if err != nil || replay.DecidedAt == nil || !replay.DecidedAt.Equal(*applied.DecidedAt) {
					t.Fatalf("durable replay: %+v %v", replay, err)
				}
				after, err := f.repo.db.GetBot(f.owner, b.ID)
				if err != nil || after.Bot.Version != b.Version+1 || after.LastSequence != before.LastSequence {
					t.Fatalf("duplicate effect/receipt: %+v %v", after, err)
				}
				v, err := f.repo.db.ReadVersion(f.owner, b.ID, b.Version+1)
				if err != nil || len(v.Triggers) != 1 {
					t.Fatalf("single schedule version: %+v %v", v, err)
				}
				job := f.repo.db.(botTriggerDB).triggerScheduler().Get(f.owner, uuid.NewSHA1(p.ID, []byte("schedule")).String())
				if job == nil || !job.Enabled || job.Payload.BotVersion != b.Version+1 {
					t.Fatalf("durable schedule: %+v", job)
				}
			})
		}
	})
}

func TestIntegrationPostgresBotApproveContinue(t *testing.T) {
	requireReviewedPostgres(t)
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		for _, scenario := range []string{"queued", "paused", "rollback"} {
			t.Run(scenario, func(t *testing.T) {
				f := newPostgresBotServicesFixture(t, scope, 100, 100)
				action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: json.RawMessage(`{"body":"exact"}`)}
				digest, err := action.Digest()
				if err != nil {
					t.Fatal(err)
				}
				a, err := f.repo.RequestApproval(f.worker, f.lease, store.BotApproval{BotID: f.run.BotID, RunID: f.run.ID, Version: f.run.Version, Action: action, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: a.ID, BotID: f.run.BotID, RunID: f.run.ID, SessionID: f.run.SessionID, Version: f.run.Version, ActionDigest: digest, Fence: f.lease.Fence})
				if err != nil {
					t.Fatal(err)
				}
				if err = f.repo.CommitRun(f.worker, store.BotRunCommit{Lease: f.lease, Status: store.RunWaitingApproval, Checkpoint: raw, Report: store.BotReport{Summary: "Waiting", NeedsApproval: []uuid.UUID{a.ID}}}); err != nil {
					t.Fatal(err)
				}
				b, err := f.repo.Get(f.owner, f.run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				q := store.BotApproveContinueRequest{BotID: b.ID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: digest}
				if scenario == "paused" {
					if _, err = f.repo.Pause(f.owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "review"}); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "rollback" {
					original := f.repo.db
					before, e := original.GetBot(f.owner, b.ID)
					if e != nil {
						t.Fatal(e)
					}
					f.repo.db = failApproveContinueTransaction{original}
					_, err = f.repo.DecideAndRequeueApprovedRun(f.owner, q)
					f.repo.db = original
					if err == nil {
						t.Fatal("missing injected commit failure")
					}
					saved, e := original.ReadApproval(f.owner, b.ID, a.ID)
					if e != nil || saved.Status != store.ApprovalPending {
						t.Fatalf("decision escaped rollback: %+v %v", saved, e)
					}
					run, e := original.ReadRun(f.owner, b.ID, f.run.ID)
					if e != nil || run.Run.Status != store.RunWaitingApproval {
						t.Fatalf("queue escaped rollback: %+v %v", run, e)
					}
					after, e := original.GetBot(f.owner, b.ID)
					if e != nil || after.LastSequence != before.LastSequence {
						t.Fatalf("activity escaped rollback: %+v %v", after, e)
					}
				}
				f.reopenReviewed(t)
				initial, err := f.repo.db.GetBot(f.owner, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				want := store.BotContinuationQueued
				if scenario == "paused" {
					want = store.BotContinuationBlockedByPause
				}
				postgresReviewedRace(t, func() error {
					result, e := f.repo.DecideAndRequeueApprovedRun(f.owner, q)
					if e == nil && (result.Continuation != want || result.Approval.Status != store.ApprovalApproved || result.RunID != f.run.ID) {
						return errors.New("incorrect continuation")
					}
					return e
				})
				before, err := f.repo.db.GetBot(f.owner, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				wantDelta := int64(2) // one decision activity and one requeue activity
				if scenario == "paused" {
					wantDelta = 1
				}
				if before.LastSequence-initial.LastSequence != wantDelta {
					t.Fatalf("decision/requeue count delta=%d want=%d", before.LastSequence-initial.LastSequence, wantDelta)
				}
				f.reopenReviewed(t)
				result, err := f.repo.DecideAndRequeueApprovedRun(f.owner, q)
				if err != nil || result.Continuation != want {
					t.Fatalf("durable continuation: %+v %v", result, err)
				}
				after, err := f.repo.db.GetBot(f.owner, b.ID)
				if err != nil || after.LastSequence != before.LastSequence {
					t.Fatalf("duplicate decision/requeue activity: %+v %v", after, err)
				}
				run, err := f.repo.GetRun(f.owner, b.ID, f.run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "paused" {
					if after.Bot.Status != store.BotPaused || run.Status != store.RunWaitingApproval {
						t.Fatal("paused bot secretly activated")
					}
					return
				}
				if run.Status != store.RunQueued {
					t.Fatalf("run not queued: %s", run.Status)
				}
				lease, err := f.repo.ClaimRun(f.worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "continue", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				loaded, err := f.repo.LoadResumeApproval(f.worker, lease)
				if err != nil || loaded.ActionDigest != digest {
					t.Fatalf("resume: %+v %v", loaded, err)
				}
				consumption := store.BotApprovalConsumption{Lease: lease, ApprovalID: a.ID, Version: run.Version, ActionDigest: digest}
				if err = f.repo.ConsumeApproval(f.worker, consumption); err != nil {
					t.Fatal(err)
				}
				if err = f.repo.ConsumeApproval(f.worker, consumption); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("duplicate consume: %v", err)
				}
			})
		}
	})
}

func TestIntegrationPostgresBotMessagesReviewed(t *testing.T) {
	requireReviewedPostgres(t)
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newPostgresBotServicesFixture(t, scope, 100, 100)
		if err := f.repo.CommitRun(f.worker, store.BotRunCommit{Lease: f.lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "done"}}); err != nil {
			t.Fatal(err)
		}
		b, err := f.repo.Get(f.owner, f.run.BotID)
		if err != nil {
			t.Fatal(err)
		}
		q := store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "pg-message", Instructions: "Review this", Attachments: []store.ChatFile{{Filename: "note.txt", MimeType: "text/plain", Data: []byte("attachment")}}}
		postgresReviewedRace(t, func() error { _, e := f.repo.AdmitMessage(f.owner, q); return e })
		first, err := f.repo.AdmitMessage(f.owner, q)
		if err != nil {
			t.Fatal(err)
		}
		f.reopenReviewed(t)
		again, err := f.repo.AdmitMessage(f.owner, q)
		if err != nil || again.UserEventID != first.UserEventID || again.HistoryCursor != first.HistoryCursor {
			t.Fatalf("durable message replay: %+v %v", again, err)
		}
		rows, err := messageReviewReader(f.repo).ReadBotTranscript(f.owner, b.HomeSessionID, 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, row := range rows {
			if row.Event.ID == first.UserEventID {
				count++
				if len(row.Event.Content.Parts) != 2 || string(row.Event.Content.Parts[1].InlineData.Data) != "attachment" {
					t.Fatal("attachment lost")
				}
			}
		}
		if count != 1 {
			t.Fatalf("home message count=%d", count)
		}
	})
}
