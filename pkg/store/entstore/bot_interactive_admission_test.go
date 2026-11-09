package entstore

import (
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotInteractiveAdmissionReplayNeverRenews(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, ctx, _, _ := runtimeFixture(t, scope)
			p, _ := execution.PrincipalFromContext(ctx)
			v := store.DefaultBotVersion()
			v.Charter = "Interactive admission"
			b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Chat", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "interactive"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			registry := execution.NewBotTurnAuthorizationRegistry(1)
			ctx = store.WithBotMessageAdmission(ctx, registry)
			q := store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "turn", Instructions: "Hello"}
			first, err := repo.AdmitMessage(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if !first.Run.IsInteractiveChat() || !first.Run.RequiresPhysicalHome() || first.Run.CallerCeiling == nil {
				t.Fatal("interactive metadata lost")
			}
			identity := execution.BotRunIdentity{BotID: b.ID, RunID: first.Run.ID, Version: b.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
			if _, err := registry.Resolve(identity); err != nil {
				t.Fatal(err)
			}
			registry.Cancel(identity)
			again, err := repo.AdmitMessage(ctx, q)
			if err != nil || again.Run.ID != first.Run.ID {
				t.Fatalf("replay: %v", err)
			}
			if _, err := registry.Resolve(identity); err == nil {
				t.Fatal("replay recreated cancelled caller authority")
			}
			if again.Run.CallerCeiling == nil || !again.Run.CallerCeiling.ExpiresAt.Equal(first.Run.CallerCeiling.ExpiresAt) {
				t.Fatal("replay changed original ceiling")
			}
		})
	}
}
