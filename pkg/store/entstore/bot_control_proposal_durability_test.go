package entstore

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestBotControlProposalReopen(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx, botID, runID, _, reopen := ApprovalScopeReopenFixture(t, scope)
			scoped, _ := reopen()
			b, err := scoped.Get(ctx, botID)
			if err != nil {
				t.Fatal(err)
			}
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: botID, RunID: runID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
			lease, err := scoped.ClaimRun(worker, store.BotClaimRequest{BotID: botID, RunID: runID, WorkerID: "review", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			proposals := scoped.(store.BotControlProposalStore)
			p, err := proposals.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "persist", ExpiresAt: time.Now().Add(time.Hour), Payload: store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "daily", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 8 * * *", Timezone: "Europe/Berlin", Instructions: "check", Enabled: true}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = scoped.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, scoped.(interface{ repository() *botRepository }).repository(), worker, lease, "ready")}); err != nil {
				t.Fatal(err)
			}
			scoped, _ = reopen()
			proposals = scoped.(store.BotControlProposalStore)
			saved, err := proposals.ReadControlProposal(ctx, botID, p.ID)
			if err != nil || saved.Digest != p.Digest {
				t.Fatalf("pending reopen: %+v %v", saved, err)
			}
			d := store.BotControlProposalDecision{BotID: botID, ProposalID: p.ID, Digest: p.Digest}
			applied, err := proposals.ConfirmControlProposal(ctx, d)
			if err != nil || applied.Status != store.BotControlApplied {
				t.Fatalf("apply reopened: %+v %v", applied, err)
			}
			scoped, _ = reopen()
			proposals = scoped.(store.BotControlProposalStore)
			replay, err := proposals.ConfirmControlProposal(ctx, d)
			if err != nil || !replay.DecidedAt.Equal(*applied.DecidedAt) {
				t.Fatalf("receipt reopen: %+v %v", replay, err)
			}
			b, err = scoped.Get(ctx, botID)
			if err != nil || b.Version != p.Version+1 {
				t.Fatalf("repeat version: %+v %v", b, err)
			}
		})
	}
}

// Preserve the transaction-local extension interfaces while injecting a failure
// at the final receipt, after mutation and scheduler work have succeeded.
type controlReceiptFailDB struct{ botDraftDB }

func (d controlReceiptFailDB) controlMemoryMutation(ctx context.Context, p store.BotControlProposal, session, actor uuid.UUID, now time.Time) error {
	return d.botDraftDB.(botControlMemoryDB).controlMemoryMutation(ctx, p, session, actor, now)
}

func (d controlReceiptFailDB) updateControlVersion(ctx context.Context, r botStoredRecord, v int64, now time.Time) error {
	return d.botDraftDB.(botControlVersionDB).updateControlVersion(ctx, r, v, now)
}
func (d controlReceiptFailDB) saveTriggerVersion(ctx context.Context, r botStoredRecord, v int64, now time.Time) error {
	return d.botDraftDB.(botTriggerDB).saveTriggerVersion(ctx, r, v, now)
}
func (d controlReceiptFailDB) triggerScheduler() store.SchedulerStore {
	return d.botDraftDB.(botTriggerDB).triggerScheduler()
}
func (d controlReceiptFailDB) InsertActivity(ctx context.Context, a store.BotActivity, key string) error {
	if strings.HasSuffix(key, ":decision") {
		return errors.New("injected receipt failure")
	}
	return d.botDraftDB.InsertActivity(ctx, a, key)
}

type controlReceiptFailTransaction struct{ botDraftDB }

func (d controlReceiptFailTransaction) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return fn(controlReceiptFailDB{tx}) })
}

func TestBotControlProposalReceiptRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, worker, run, lease, _ := botRunFixture(t, scope)
			b, _ := r.Get(ctx, run.BotID)
			p, err := r.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "rollback", ExpiresAt: r.clock().Add(time.Hour), Payload: store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "daily", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 8 * * *", Timezone: "UTC", Instructions: "check", Enabled: true}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, lease, "ready")}); err != nil {
				t.Fatal(err)
			}
			original := r.db
			r.db = controlReceiptFailTransaction{original}
			d := store.BotControlProposalDecision{BotID: run.BotID, ProposalID: p.ID, Digest: p.Digest}
			if _, err = r.ConfirmControlProposal(ctx, d); err == nil {
				t.Fatal("missing injected failure")
			}
			r.db = original
			after, _ := r.Get(ctx, b.ID)
			if after.Version != b.Version || after.Revision != b.Revision {
				t.Fatal("mutation escaped transaction")
			}
			jobs := r.db.(botTriggerDB).triggerScheduler()
			if jobs.Get(ctx, uuid.NewSHA1(p.ID, []byte("schedule")).String()) != nil {
				t.Fatal("schedule escaped transaction")
			}
			saved, err := r.ReadControlProposal(ctx, b.ID, p.ID)
			if err != nil || saved.Status != store.BotControlProposed {
				t.Fatalf("rollback receipt: %+v %v", saved, err)
			}
			applied, err := r.ConfirmControlProposal(ctx, d)
			if err != nil || applied.Status != store.BotControlApplied {
				t.Fatalf("retry: %+v %v", applied, err)
			}
		})
	}
}
