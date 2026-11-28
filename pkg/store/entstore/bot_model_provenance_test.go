package entstore

import (
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotRunCommitModelSelectionPersistence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, worker, run, lease, _ := botRunFixture(t, scope)
			report := finishedAgentWorkReport(t, repo, worker, lease, "Answer", store.AgentWorkAnswered)
			selection := store.SessionPin{Provider: "owner-instance", Model: "resolved-model"}
			q := store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: report, ModelSelection: &selection}
			stale := q
			stale.Lease.Fence++
			if err := repo.CommitRun(worker, stale); err == nil {
				t.Fatal("stale lease wrote model identity")
			}
			for i := 0; i < 2; i++ {
				if err := repo.CommitRun(worker, q); err != nil {
					t.Fatalf("commit/retry %d: %v", i, err)
				}
			}
			saved, err := repo.GetRun(owner, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Provider != selection.Provider || saved.Model != selection.Model {
				t.Fatalf("lost model selection: %q/%q", saved.Provider, saved.Model)
			}
			if saved.Usage != q.Usage {
				t.Fatal("provenance changed usage accounting")
			}
			selection.Model = "different-model"
			if err := repo.CommitRun(worker, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("retry changed committed model: %v", err)
			}
		})
	}
}

func TestBotRunCommitModelSelectionValidation(t *testing.T) {
	repo, _, ctx, _, _ := runtimeFixture(t, store.BotScopePersonal)
	for _, selection := range []store.SessionPin{{}, {Provider: "instance"}, {Model: "model"}, {Provider: " ", Model: "model"}} {
		if err := repo.CommitRun(ctx, store.BotRunCommit{ModelSelection: &selection}); !errors.Is(err, store.ErrBotInvalid) {
			t.Fatalf("incomplete identity accepted: %+v: %v", selection, err)
		}
	}
}
