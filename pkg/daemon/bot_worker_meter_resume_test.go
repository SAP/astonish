package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

type lifecycleMeterRuntime struct{ bot.Runtime }

func (lifecycleMeterRuntime) Resume(ctx context.Context, q bot.RunRequest, _ bot.EventSink) (bot.RunResult, error) {
	if err := q.Budget.ReserveTool(ctx, "read"); err != nil {
		return bot.RunResult{}, err
	}
	// A per-attempt runtime result must not replace the shared cumulative ledger.
	return bot.RunResult{Status: store.RunSucceeded, Usage: store.BotUsage{ToolCalls: 1}}, nil
}

func TestBotWorkerMeteredResumeReturnsCumulativeCommitUsage(t *testing.T) {
	cost := int64(12)
	usage := store.BotUsage{ToolCalls: 3, InputTokens: 8, OutputTokens: 2, CostMicros: &cost, RuntimeNanos: int64(time.Second)}
	in := store.BotRunInput{Version: store.DefaultBotVersion(), Run: store.BotRun{Usage: usage, Lease: store.BotLease{Fence: 2}}}
	budget, err := botRunBudget(in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := botMeteredRuntime{Runtime: lifecycleMeterRuntime{}}
	result, err := r.Resume(context.Background(), bot.RunRequest{Run: in.Run, Budget: budget}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := result.Usage
	if got.ToolCalls != 4 || got.InputTokens != 8 || got.OutputTokens != 2 || got.CostMicros == nil || *got.CostMicros != cost || got.RuntimeNanos < usage.RuntimeNanos {
		t.Fatalf("commit received per-attempt usage rather than cumulative ledger: %+v", got)
	}
}
