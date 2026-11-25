package entstore

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// The supervisor joins a renewal already in progress after the adapter returns.
// Its runtime accounting may therefore be newer than the adapter's final meter.
func TestBotProgressCommitAfterFinalRenewal(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, _, ctx, _, lease, now := progressFixture(t, scope)
			q := progressCheckpoint(t, r, ctx, lease, *now)
			cost := int64(40)
			q.State.Checkpoint.Usage = store.BotUsage{ToolCalls: 2, InputTokens: 30, OutputTokens: 20, CostMicros: &cost}
			if _, err := r.CheckpointProgress(ctx, q); err != nil {
				t.Fatal(err)
			}
			before, err := r.db.ReadRun(ctx, lease.BotID, lease.RunID)
			if err != nil {
				t.Fatal(err)
			}
			*now = now.Add(time.Second)
			lease, err = r.RenewLease(ctx, lease, now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			durable, err := r.db.ReadRun(ctx, lease.BotID, lease.RunID)
			if err != nil {
				t.Fatal(err)
			}
			report := finishedAgentWorkReport(t, r, ctx, lease, "Observed", store.AgentWorkAnswered)
			commit := store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Usage: before.Run.Usage, Report: report}
			for _, tc := range []struct {
				name   string
				change func(*store.BotRunCommit)
				want   error
			}{
				{"lower_tools", func(c *store.BotRunCommit) { c.Usage.ToolCalls-- }, store.ErrBotConflict},
				{"lower_input", func(c *store.BotRunCommit) { c.Usage.InputTokens-- }, store.ErrBotConflict},
				{"lower_output", func(c *store.BotRunCommit) { c.Usage.OutputTokens-- }, store.ErrBotConflict},
				{"lower_cost", func(c *store.BotRunCommit) { v := cost - 1; c.Usage.CostMicros = &v }, store.ErrBotConflict},
				{"missing_cost", func(c *store.BotRunCommit) { c.Usage.CostMicros = nil }, store.ErrBotConflict},
				{"negative_runtime", func(c *store.BotRunCommit) { c.Usage.RuntimeNanos = -1 }, store.ErrBotInvalid},
				{"stale_fence", func(c *store.BotRunCommit) { c.Lease.Fence++ }, store.ErrBotConflict},
				{"wrong_worker", func(c *store.BotRunCommit) { c.Lease.WorkerID = "stale-worker" }, store.ErrBotConflict},
			} {
				t.Run(tc.name, func(t *testing.T) {
					bad := commit
					tc.change(&bad)
					if err := r.CommitRun(ctx, bad); !errors.Is(err, tc.want) {
						t.Fatalf("unsafe commit: got %v, want %v", err, tc.want)
					}
					saved, err := r.db.ReadRun(ctx, lease.BotID, lease.RunID)
					if err != nil {
						t.Fatal(err)
					}
					if saved.Run.Status != store.RunRunning || !reflect.DeepEqual(saved.Run.Usage, durable.Run.Usage) {
						t.Fatalf("rejected commit changed durable run: %+v", saved.Run)
					}
				})
			}
			liveNow := *now
			*now = lease.ExpiresAt.Add(time.Second)
			if err := r.CommitRun(ctx, commit); !errors.Is(err, store.ErrBotExpired) {
				t.Fatalf("expired live lease: %v", err)
			}
			*now = liveNow
			// Charge the final interval after the heartbeat as well, without
			// changing the original request's replay identity.
			*now = now.Add(time.Second)
			if err := r.CommitRun(ctx, commit); err != nil {
				t.Fatalf("final renewal must not reject earlier runtime snapshot: %v", err)
			}
			if err := r.CommitRun(ctx, commit); err != nil {
				t.Fatalf("original request replay: %v", err)
			}
			saved, err := r.db.ReadRun(ctx, lease.BotID, lease.RunID)
			if err != nil {
				t.Fatal(err)
			}
			want := before.Run.Usage
			want.RuntimeNanos = int64(2 * time.Second)
			if !reflect.DeepEqual(saved.Run.Usage, want) {
				t.Fatalf("usage changed or elapsed runtime lost: got %+v, want %+v", saved.Run.Usage, want)
			}
		})
	}
}
