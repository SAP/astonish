package entstore

import (
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"testing"
	"time"
)

func TestBotControlProposalReviewAndReplay(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, worker, run, lease, _ := botRunFixture(t, scope)
			before, _ := r.Get(ctx, run.BotID)
			text := "Reviewed responsibility"
			q := store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "proposal", ExpiresAt: time.Now().Add(time.Hour), Payload: store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "review", Responsibility: &text}}
			p, err := r.ProposeControl(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			again, err := r.ProposeControl(worker, q)
			if err != nil || again.ID != p.ID {
				t.Fatalf("retry: %+v %v", again, err)
			}
			after, _ := r.Get(ctx, run.BotID)
			if after.Version != before.Version || after.Revision != before.Revision {
				t.Fatal("proposal mutated bot")
			}
			d := store.BotControlProposalDecision{BotID: run.BotID, ProposalID: p.ID, Digest: p.Digest}
			if _, err = r.ConfirmControlProposal(ctx, d); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("live source accepted: %v", err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, lease, "proposal ready")}); err != nil {
				t.Fatal(err)
			}
			bad := d
			bad.Digest = "forged"
			if _, err = r.ConfirmControlProposal(ctx, bad); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal(err)
			}
			applied, err := r.ConfirmControlProposal(ctx, d)
			if err != nil || applied.Status != store.BotControlApplied {
				t.Fatalf("apply: %+v %v", applied, err)
			}
			replay, err := r.ConfirmControlProposal(ctx, d)
			if err != nil || !replay.DecidedAt.Equal(*applied.DecidedAt) {
				t.Fatalf("replay: %+v %v", replay, err)
			}
			v, err := r.db.ReadVersion(ctx, run.BotID, before.Version+1)
			if err != nil || v.Charter != text {
				t.Fatalf("saved: %+v %v", v, err)
			}
			snapshot, err := r.Snapshot(ctx, run.BotID)
			if err != nil || len(snapshot.PendingProposals) != 0 {
				t.Fatalf("snapshot: %+v %v", snapshot, err)
			}
		})
	}
}
