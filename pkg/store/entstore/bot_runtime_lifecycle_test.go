package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotRuntimeSupervisorRenewalAndOwnerCancellation(t *testing.T) {
	repo, scoped, ctx, run, home := runtimeFixture(t, store.BotScopePersonal)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	adapter := &runtimeTestAdapter{start: func(c context.Context, q botdomain.RunRequest, _ botdomain.EventSink) (botdomain.RunResult, error) {
		deadline := time.After(time.Second)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-deadline:
				t.Fatal("lease was not renewed")
			case <-ticker.C:
				saved, err := repo.GetRun(ctx, run.BotID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if saved.Lease.ExpiresAt.After(q.Run.Lease.ExpiresAt) {
					cancel()
					<-c.Done()
					return botdomain.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "Too late"}}, nil
				}
			}
		}
	}}
	supervisor, err := botdomain.NewSupervisor(runtimeConfig(scoped, home, adapter))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Execute(ctx, run.BotID, run.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation hidden: %v", err)
	}
	if adapter.cancels.Load() != 1 {
		t.Fatal("runtime Cancel not invoked")
	}
	saved, err := repo.GetRun(context.WithoutCancel(ctx), run.BotID, run.ID)
	if err != nil || saved.Status != store.RunRunning {
		t.Fatalf("cancel must leave attempt to fenced recovery: %+v %v", saved, err)
	}
}

func TestBotRuntimeSupervisorApprovalCheckpoint(t *testing.T) {
	repo, scoped, ctx, run, home := runtimeFixture(t, store.BotScopeTeam)
	adapter := &runtimeTestAdapter{start: func(c context.Context, q botdomain.RunRequest, _ botdomain.EventSink) (botdomain.RunResult, error) {
		action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Recipient: "reviewed-target", Arguments: []byte(`{"body":"review"}`)}
		digest, err := action.Digest()
		if err != nil {
			t.Fatal(err)
		}
		approval, err := scoped.RequestApproval(c, q.Run.Lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return botdomain.RunResult{Status: store.RunWaitingApproval, Checkpoint: []byte(`{"step":1}`), Report: store.BotReport{Summary: "Awaiting exact approval", NeedsApproval: []store.BotID{approval.ID}}}, nil
	}}
	supervisor, err := botdomain.NewSupervisor(runtimeConfig(scoped, home, adapter))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Execute(ctx, run.BotID, run.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := repo.GetRun(ctx, run.BotID, run.ID)
	if err != nil || saved.Status != store.RunWaitingApproval || len(saved.Checkpoint) == 0 || saved.FinishedAt != nil {
		t.Fatalf("checkpoint not committed: %+v %v", saved, err)
	}
	if _, err := supervisor.Execute(ctx, run.BotID, run.ID); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("checkpoint implicitly resumed: %v", err)
	}
}
