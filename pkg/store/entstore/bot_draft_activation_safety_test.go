package entstore

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

type draftActivationReceiptFailDB struct {
	controlReceiptFailDB
	botScheduledDB
}

type draftActivationReceiptFailTransaction struct {
	botDraftDB
	botScheduledDB
}

func (d draftActivationReceiptFailTransaction) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error {
		return fn(draftActivationReceiptFailDB{controlReceiptFailDB{tx}, tx.(botScheduledDB)})
	})
}

func TestBotDraftScheduledActivationRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, b := draftActivationFixture(t, scope)
			b, tr := confirmDraftSchedule(t, r, ctx, b, store.BotControlScheduleCreate, store.TriggerSpec{Kind: store.TriggerCron, Cron: "* * * * *", Timezone: "Etc/UTC", Instructions: "Reviewed report", Enabled: true})
			p := draftActivationProposal(t, r, ctx, b, store.BotControlPayload{Operation: store.BotControlActivate, Reason: "Activate"})
			r.activationCheck = func(context.Context, store.Bot, store.BotVersion) error { return nil }
			jobs := r.db.(botTriggerDB).triggerScheduler()
			before := jobs.Get(ctx, tr.ScheduleID.String())
			original := r.db
			r.db = draftActivationReceiptFailTransaction{original, original.(botScheduledDB)}
			d := store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest}
			_, err := r.ConfirmControlProposal(ctx, d)
			r.db = original
			if err == nil || err.Error() != "injected receipt failure" {
				t.Fatalf("receipt failure not reached: %v", err)
			}
			if !reflect.DeepEqual(before, jobs.Get(ctx, tr.ScheduleID.String())) {
				t.Fatal("schedule escaped rollback")
			}
			assertDraftRecurrenceInert(t, r, ctx, b, tr)
			saved, err := r.ReadControlProposal(ctx, b.ID, p.ID)
			if err != nil || saved.Status != store.BotControlProposed {
				t.Fatalf("receipt escaped rollback: %+v %v", saved, err)
			}
			got, err := r.ConfirmControlProposal(ctx, d)
			if err != nil || got.Status != store.BotControlApplied {
				t.Fatalf("retry: %+v %v", got, err)
			}
			job := jobs.Get(ctx, tr.ScheduleID.String())
			if !job.Enabled || job.NextRun == nil {
				t.Fatal("retry did not enable reviewed recurrence")
			}
		})
	}
}

func TestBotDraftScheduledActivationLegacyReadinessCAS(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []string{"missing-readiness", "readiness-failed", "binding-changed", "revision-changed"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				r, ctx, b := draftActivationFixture(t, scope)
				b, tr := confirmDraftSchedule(t, r, ctx, b, store.BotControlScheduleCreate, store.TriggerSpec{Kind: store.TriggerCron, Cron: "* * * * *", Timezone: "Etc/UTC", Instructions: "Reviewed report", Enabled: true})
				jobs := r.db.(botTriggerDB).triggerScheduler()
				if mode != "missing-readiness" {
					r.activationCheck = func(ctx context.Context, snapshot store.Bot, v store.BotVersion) error {
						switch mode {
						case "readiness-failed":
							return store.ErrBotUnsupported
						case "binding-changed":
							job := jobs.Get(ctx, tr.ScheduleID.String())
							job.Payload.BotVersion++
							return jobs.Update(ctx, job)
						case "revision-changed":
							v.Charter = "Concurrent reviewed responsibility"
							v.ChangeReason = "Concurrent edit"
							_, err := r.ApplyVersion(ctx, store.BotVersionRequest{BotID: snapshot.ID, ExpectedRevision: snapshot.Revision, Version: v})
							return err
						}
						return nil
					}
				}
				want := store.ErrBotConflict
				if mode == "missing-readiness" || mode == "readiness-failed" {
					want = store.ErrBotUnsupported
				}
				if _, err := r.Activate(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Activate"}); !errors.Is(err, want) {
					t.Fatalf("legacy gate: %v", err)
				}
				after, err := r.Get(ctx, b.ID)
				if err != nil || after.Status != store.BotDraft {
					t.Fatalf("failed activation changed state: %+v %v", after, err)
				}
				job := jobs.Get(ctx, tr.ScheduleID.String())
				if job.Enabled || job.NextRun != nil {
					t.Fatal("failed activation enabled recurrence")
				}
			})
		}
	}
}
