package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

type controlBusyBarrierDB struct {
	botDraftDB
	active store.RunID
}

func (d controlBusyBarrierDB) GetBot(ctx context.Context, id store.BotID) (*botStoredRecord, error) {
	rec, err := d.botDraftDB.GetBot(ctx, id)
	if err == nil {
		rec.ActiveRunID = &d.active
	}
	return rec, err
}

func (d controlBusyBarrierDB) Transaction(ctx context.Context, work func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return work(controlBusyBarrierDB{botDraftDB: tx, active: d.active}) })
}

func TestBotControlProposalBusyRetry(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, op := range []store.BotControlOperation{store.BotControlResponsibility, store.BotControlTaskOnceOperation} {
			t.Run(string(scope)+"/"+string(op), func(t *testing.T) {
				r, ctx, worker, run, lease, _ := botRunFixture(t, scope)
				r.activationCheck = func(context.Context, store.Bot, store.BotVersion) error { return nil }
				text := "Reviewed responsibility"
				payload := store.BotControlPayload{Operation: op, Reason: "review"}
				if op == store.BotControlResponsibility {
					payload.Responsibility = &text
				} else {
					payload.TaskOnce = &store.BotControlTaskOnce{Instructions: "Run reviewed task"}
				}
				p, err := r.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "busy", ExpiresAt: r.clock().Add(time.Hour), Payload: payload})
				if err != nil {
					t.Fatal(err)
				}
				d := store.BotControlProposalDecision{BotID: run.BotID, ProposalID: p.ID, Digest: p.Digest}
				if _, err = r.ConfirmControlProposal(ctx, d); !errors.Is(err, store.ErrBotBusy) || !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("live source must be retryable busy with legacy compatibility: %v", err)
				}
				saved, err := r.ReadControlProposal(ctx, run.BotID, p.ID)
				if err != nil || saved.Status != store.BotControlProposed || saved.DecidedAt != nil {
					t.Fatalf("busy consumed review: %+v %v", saved, err)
				}
				bad := d
				bad.Digest = "forged"
				if _, err = r.ConfirmControlProposal(ctx, bad); !errors.Is(err, store.ErrBotConflict) || errors.Is(err, store.ErrBotBusy) {
					t.Fatalf("digest mismatch must remain changed: %v", err)
				}
				if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, lease, "ready")}); err != nil {
					t.Fatal(err)
				}
				original := r.db
				r.db = controlBusyBarrierDB{botDraftDB: original, active: run.ID}
				if _, err = r.ConfirmControlProposal(ctx, d); !errors.Is(err, store.ErrBotBusy) {
					t.Fatalf("terminal source with retained writer/teardown barrier: %v", err)
				}
				r.db = original
				applied, err := r.ConfirmControlProposal(ctx, d)
				if err != nil || applied.Status != store.BotControlApplied {
					t.Fatalf("retry after finish: %+v %v", applied, err)
				}
			})
		}
	}
}
