//go:build integration

package entstore

import (
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

// Explicit opt-in only; the shared guard requires a disposable loopback server,
// nondefault port and dedicated role before the fixture may create/drop databases.
func TestIntegrationPostgresBotTitleStartState(t *testing.T) {
	requireDraftActivationPostgres(t)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newPostgresBotServicesFixture(t, scope, 100, 100)
			p, _ := execution.PrincipalFromContext(f.owner)
			ctx, err := execution.WithAuthenticationEvidence(f.owner, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			registry := execution.NewBotTurnAuthorizationRegistry(4)
			defer registry.Close()
			ctx = store.WithBotMessageAdmission(ctx, registry)
			v := store.DefaultBotVersion()
			v.Charter = "Research the title"
			q := store.BotCreateRequest{Name: "Research the title", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "pg-title-state"}
			first, err := f.repo.CreateDraftAndStart(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			again, err := f.repo.CreateDraftAndStart(ctx, q)
			if err != nil || first.ID != again.ID || first.Run.ID != again.Run.ID || first.UserEventID != again.UserEventID {
				t.Fatalf("replay: %+v %v", again, err)
			}
			runs, err := f.repo.ListRuns(ctx, first.ID, store.BotPage{})
			if err != nil || len(runs) != 1 || runs[0].Status != store.RunQueued {
				t.Fatalf("runs: %+v %v", runs, err)
			}
			snap, err := f.bots.(store.BotSnapshotReader).Snapshot(ctx, first.ID)
			if err != nil || len(snap.RunStates) != 1 || snap.RunStates[0].Status != store.RunQueued || snap.HistoryCursor < first.HistoryCursor {
				t.Fatalf("snapshot: %+v %v", snap, err)
			}
		})
	}
}
