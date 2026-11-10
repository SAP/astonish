package entstore

import (
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"testing"
	"time"
)

func TestBotWorkClaimPinsAdmittedAttachment(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, ctx, run, _ := runtimeFixture(t, scope)
			owner, _ := execution.PrincipalFromContext(ctx)
			ctx = execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: owner.Subject, OrgSlug: "org", TeamSlug: "team"})
			lease, err := repo.ClaimRun(ctx, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "attachment-test", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			w, err := repo.LoadWork(ctx, lease)
			if err != nil {
				t.Fatal(err)
			}
			if len(w.Contract.Attachments) != 1 || w.Contract.Attachments[0].Text != "persisted file" || w.Contract.Attachments[0].Filename != "note.txt" {
				t.Fatalf("admitted attachment omitted from contract: %+v", w.Contract)
			}
		})
	}
}
