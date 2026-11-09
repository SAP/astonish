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

func TestBotLiveGuardRevocationAndFence(t *testing.T) {
	ctx := context.Background()
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	id, runID, owner := uuid.New(), uuid.New(), uuid.New()
	req := bot.RunRequest{Bot: store.Bot{ID: id, OwnerID: owner, Status: store.BotActive, Version: 1}, Version: store.BotVersion{Version: 1}, Run: store.BotRun{ID: runID, SessionID: uuid.New(), Status: store.RunRunning, Lease: store.BotLease{Fence: 1, WorkerID: "worker", ExpiresAt: time.Now().Add(time.Minute)}}, Budget: meter}
	repo := &workerEventRepo{b: req.Bot, r: req.Run}
	guard := botLiveGuard{repo: repo, request: req}
	if err := guard.check(ctx); err != nil {
		t.Fatal(err)
	}
	repo.b.Status = store.BotPaused
	if err := guard.check(ctx); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	repo.b = req.Bot
	repo.r.Lease.Fence++
	if err := guard.check(ctx); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
	repo.r = req.Run
	repo.r.Lease.ExpiresAt = time.Now().Add(-time.Second)
	if err := guard.check(ctx); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
}

type workerDraftRepo struct {
	store.BotStore
	bots []store.Bot
}

func (r workerDraftRepo) List(context.Context, store.BotPage) ([]store.Bot, error) {
	return r.bots, nil
}
func (workerDraftRepo) ListRuns(_ context.Context, id store.BotID, _ store.BotPage) ([]store.BotRun, error) {
	// Drafts are inspected for explicit test consent, but a legacy/ordinary
	// queued row must never activate or execute merely because it is queued.
	return []store.BotRun{{ID: uuid.New(), BotID: id, Status: store.RunQueued, Intent: store.BotIntentRegular}}, nil
}
func TestBotWorkerPollNeverActivatesDraft(t *testing.T) {
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	w := &BotWorker{}
	if err := w.pollStore(ctx, workerDraftRepo{bots: []store.Bot{{ID: uuid.New(), OwnerID: owner, Status: store.BotDraft}}}, nil); err != nil && !errors.Is(err, store.ErrBotUnsupported) {
		// The deliberately minimal repository has no publication/retention
		// adapter. Execution would panic on its missing runtime methods.
		t.Fatal(err)
	}
}

type meteredFailureRuntime struct{ bot.Runtime }

func (meteredFailureRuntime) Start(c context.Context, q bot.RunRequest, _ bot.EventSink) (bot.RunResult, error) {
	_ = q.Budget.ReserveTool(c, "controlled")
	return bot.RunResult{}, store.ErrBotUnsupported
}
func (r meteredFailureRuntime) Resume(c context.Context, q bot.RunRequest, s bot.EventSink) (bot.RunResult, error) {
	return r.Start(c, q, s)
}
func TestBotMeteredRuntimeRetainsUsageOnFailure(t *testing.T) {
	for _, resume := range []bool{false, true} {
		meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		runtime := botMeteredRuntime{Runtime: meteredFailureRuntime{}}
		call := runtime.Start
		if resume {
			call = runtime.Resume
		}
		result, err := call(context.Background(), bot.RunRequest{Budget: meter}, nil)
		if !errors.Is(err, store.ErrBotUnsupported) || result.Usage.ToolCalls != 1 {
			t.Fatalf("resume=%v result=%+v err=%v", resume, result, err)
		}
	}
}
