package entstore

import (
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotProgressSettlementAfterWorkAdvance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, _, ctx, _, lease, now := progressFixture(t, scope)
			mutation := progressCheckpoint(t, r, ctx, lease, *now).Mutation
			begin := store.BotProgressBeginDispatchRequest{Mutation: mutation, DispatchID: uuid.New(), Kind: store.BotProgressDispatchModel, OperationDigest: strings.Repeat("a", 64), ReservedUsage: store.BotUsage{InputTokens: 10}}
			if _, err := r.BeginProgressDispatch(ctx, begin); err != nil {
				t.Fatal(err)
			}
			work, err := r.LoadWork(ctx, lease)
			if err != nil {
				t.Fatal(err)
			}
			updated, err := r.SaveWork(ctx, store.BotWorkUpdate{Lease: lease, ExpectedRevision: work.Revision, State: *work})
			if err != nil {
				t.Fatal(err)
			}
			settle := store.BotProgressSettleDispatchRequest{Mutation: mutation, DispatchID: begin.DispatchID, Outcome: store.BotProgressDispatchSettled, Usage: store.BotUsage{InputTokens: 15}, EvidenceRef: "provider-receipt:work-advance"}
			if _, err := r.SettleProgressDispatch(ctx, settle); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale work revision accepted: %v", err)
			}
			settle.Mutation.Binding.WorkRevision = updated.Revision
			badIdentity := settle
			badIdentity.Mutation.Binding.SessionID = uuid.New()
			if _, err := r.SettleProgressDispatch(ctx, badIdentity); err == nil {
				t.Fatal("substituted session accepted")
			}
			badFence := settle
			badFence.Mutation.Lease.Fence++
			if _, err := r.SettleProgressDispatch(ctx, badFence); err == nil {
				t.Fatal("substituted fence accepted")
			}
			if _, err := r.SettleProgressDispatch(ctx, settle); err != nil {
				t.Fatalf("legitimate work advance rejected: %v", err)
			}
			saved, err := r.db.ReadRun(ctx, lease.BotID, lease.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.ProgressPending != 0 || saved.Run.Usage.InputTokens != 15 {
				t.Fatalf("settlement lost ledger/accounting: %+v", saved)
			}
		})
	}
}
