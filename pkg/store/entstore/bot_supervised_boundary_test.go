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

// Until a persisted, purpose-bound consent capability is implemented, neither
// a manual trigger nor an owner principal is sufficient to run an inactive Bot.
func TestBotInactiveSupervisedAdmissionFailClosed(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, scoped, ctx, queued, home := runtimeFixture(t, scope)
			b, err := repo.Get(ctx, queued.BotID)
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Owner pauses autonomous work"})
			if err != nil {
				t.Fatal(err)
			}
			for _, trigger := range []store.TriggerKind{store.TriggerManual, store.TriggerCron} {
				_, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: trigger, IdempotencyKey: "not-consent-" + string(trigger), Instructions: "This is only a test"})
				if err == nil {
					t.Fatalf("inactive admission accepted %s", trigger)
				}
			}
			if _, err := scoped.(store.BotRuntimeStore).LoadRunInput(ctx, b.ID, queued.ID); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("inactive input: %v", err)
			}
			adapter := &runtimeTestAdapter{start: func(context.Context, botdomain.RunRequest, botdomain.EventSink) (botdomain.RunResult, error) {
				t.Error("inactive runtime invoked")
				return botdomain.RunResult{}, store.ErrBotForbidden
			}}
			supervisor, err := botdomain.NewSupervisor(runtimeConfig(scoped, home, adapter))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := supervisor.Execute(ctx, b.ID, queued.ID); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("inactive execution: %v", err)
			}
			p, _ := execution.PrincipalFromContext(ctx)
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: queued.ID, Version: b.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
			if _, err := scoped.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: queued.ID, WorkerID: "explicit-owner", ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("inactive claim: %v", err)
			}
			after, err := repo.Get(ctx, b.ID)
			if err != nil || after.Status != store.BotPaused || after.Version != b.Version || adapter.calls.Load() != 0 {
				t.Fatalf("inactive state changed: %+v %v", after, err)
			}
			runs, err := repo.ListRuns(ctx, b.ID, store.BotPage{Limit: 100})
			if err != nil || len(runs) != 1 {
				t.Fatalf("refusal created runs: %+v %v", runs, err)
			}
		})
	}
}
