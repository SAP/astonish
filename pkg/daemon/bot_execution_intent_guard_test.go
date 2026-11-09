package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotIntentLiveGuardNeverElevatesConversation(t *testing.T) {
	ctx := context.Background()
	budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req := bot.RunRequest{Bot: store.Bot{ID: uuid.New(), OwnerID: uuid.New(), Status: store.BotPaused, Version: 1}, Version: store.BotVersion{Version: 1}, Run: store.BotRun{Intent: store.BotIntentConversation, ID: uuid.New(), SessionID: uuid.New(), Status: store.RunRunning, Lease: store.BotLease{Fence: 1, WorkerID: "worker", ExpiresAt: time.Now().Add(time.Minute)}}, Budget: budget}
	repo := &workerEventRepo{b: req.Bot, r: req.Run}
	guard := botLiveGuard{repo: repo, request: req}
	if err := guard.check(ctx); err != nil {
		t.Fatal(err)
	}
	for _, status := range []store.BotStatus{store.BotPaused, store.BotActive, store.BotStopped, store.BotDraft} {
		repo.b.Status = status
		for _, name := range []string{"read_file", "run_flow", "delegate_tasks", "unknown_tool", "ask_user"} {
			decision, err := guard.Authorize(ctx, execution.ActionDescriptor{Tool: name})
			if decision.Kind != execution.PolicyDeny || !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("status=%s tool=%s decision=%+v error=%v", status, name, decision, err)
			}
		}
		if status != store.BotPaused && guard.check(ctx) == nil {
			t.Fatalf("conversation lease valid after status=%s", status)
		}
	}
	repo.b = req.Bot
	repo.r.Intent = store.BotIntentRegular
	if err := guard.check(ctx); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("changed durable intent accepted: %v", err)
	}
	repo.r = req.Run
	repo.r.Lease.Fence++
	if err := guard.check(ctx); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("stale fence accepted: %v", err)
	}
}
