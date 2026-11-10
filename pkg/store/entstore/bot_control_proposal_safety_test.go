package entstore

import (
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"testing"
	"time"
)

func TestBotControlProposalTerminalReceipts(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, kind := range []string{"expired", "stale", "rejected", "invalid_transition"} {
			t.Run(string(scope)+"/"+kind, func(t *testing.T) {
				r, ctx, worker, run, lease, now := botRunFixture(t, scope)
				text := "review"
				payload := store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "review", Responsibility: &text}
				if kind == "invalid_transition" {
					payload = store.BotControlPayload{Operation: store.BotControlActivate, Reason: "activate"}
				}
				p, err := r.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "status", ExpiresAt: now.Add(time.Hour), Payload: payload})
				if err != nil {
					t.Fatal(err)
				}
				if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, lease, "ready")}); err != nil {
					t.Fatal(err)
				}
				if kind == "expired" {
					*now = now.Add(2 * time.Hour)
				}
				if kind == "stale" {
					b, _ := r.Get(ctx, run.BotID)
					if _, err = r.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "changed"}); err != nil {
						t.Fatal(err)
					}
				}
				d := store.BotControlProposalDecision{BotID: run.BotID, ProposalID: p.ID, Digest: p.Digest}
				var receipt *store.BotControlProposal
				if kind == "rejected" {
					receipt, err = r.RejectControlProposal(ctx, d)
				} else {
					receipt, err = r.ConfirmControlProposal(ctx, d)
				}
				if kind == "invalid_transition" {
					if !errors.Is(err, store.ErrBotConflict) {
						t.Fatalf("unsupported: %v", err)
					}
					receipt, err = r.ReadControlProposal(ctx, run.BotID, p.ID)
					if err != nil || receipt.Status != store.BotControlProposed {
						t.Fatalf("fake applied: %+v %v", receipt, err)
					}
					return
				}
				expected := map[string]store.BotControlProposalStatus{"expired": store.BotControlExpired, "stale": store.BotControlStale, "rejected": store.BotControlRejected}[kind]
				if err != nil || receipt.Status != expected {
					t.Fatalf("status: %+v %v", receipt, err)
				}
				replay, err := r.ConfirmControlProposal(ctx, d)
				if err != nil || replay.Status != expected || !replay.DecidedAt.Equal(*receipt.DecidedAt) {
					t.Fatalf("immutable: %+v %v", replay, err)
				}
			})
		}
	}
}
