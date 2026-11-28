package entstore

import (
	"errors"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// Queued progress is not a never-started queue. Authority loss must preserve
// its checkpoint, while corrupt/pending ledgers must never become resumable.
func TestBotProgressQueuedAuthorityLoss(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scenario := range []string{"settled", "pending", "uncertain", "wrong-binding", "committed"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				r, owner, worker, run, lease, now := progressFixture(t, scope)
				cp := progressCheckpoint(t, r, worker, lease, *now)
				cp.State.Phase = store.BotProgressSuspended
				if _, err := r.CheckpointProgress(worker, cp); err != nil {
					t.Fatal(err)
				}
				saved, err := r.db.ReadRun(owner, run.BotID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				p, _ := execution.PrincipalFromContext(owner)
				saved.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
				saved.Run.Presentation = store.BotRunPresentationHomeInteractive
				saved.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: p}
				switch scenario {
				case "pending":
					saved.ProgressPending++
				case "uncertain":
					saved.ProgressUncertain = true
				case "wrong-binding":
					saved.Progress.Checkpoint.Binding.Version++
				case "committed":
					saved.CommitHash = "already-committed"
				}
				if err = saveProgressEnvelope(owner, r.db, saved); err != nil {
					t.Fatal(err)
				}
				before := saved.Run.Usage
				held, err := r.RequireInteractiveContinuation(owner, run.BotID, run.ID)
				if scenario != "settled" {
					if !errors.Is(err, store.ErrBotConflict) {
						t.Fatalf("unsafe checkpoint accepted: %v", err)
					}
					return
				}
				if err != nil || held.Status != store.RunInterrupted || held.ContinuationReason != store.BotContinuationRequired {
					t.Fatalf("checkpoint stranded: %+v %v", held, err)
				}
				after, err := r.db.ReadRun(owner, run.BotID, run.ID)
				if err != nil || after.Progress.Phase != store.BotProgressWaitingAuthorization || after.Progress.Reason != store.BotProgressReasonAuthorization || !reflect.DeepEqual(before, after.Run.Usage) || !progressSafe(after) {
					t.Fatalf("lost saved work: %+v %v", after, err)
				}
				if _, err = r.RequireInteractiveContinuation(owner, run.BotID, run.ID); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
