package entstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// This fixture uses the production router, live membership, and persisted linked
// identities. No verifier seam or Bot body boolean grants authority in this test.
func TestBotNotificationsScopedConfigurationAndRevokedDeferredSend(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			_, backend, err := NewPlatformServices(ctx, Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			owner := uuid.New()
			org := &store.Organization{ID: uuid.NewString(), Slug: "org-a", Name: "A", Status: "active"}
			if err := backend.Organizations().Create(ctx, org); err != nil {
				t.Fatal(err)
			}
			if err := backend.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			if err := backend.Users().Create(ctx, &store.User{ID: owner.String(), Email: "owner@example.test", DisplayName: "A", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := backend.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			data, err := backend.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: "team-a", Name: "A", SchemaName: "team_team_a"}
			if err := data.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			if err := data.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			ctx, err = execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			var repo *botRepository
			if scope == store.BotScopePersonal {
				repo = data.ForUser(owner.String()).Bots().(*personalBotStore).repository()
			} else {
				repo = data.ForTeam(team.Slug).Bots().(*teamBotStore).repository()
			}
			v := store.DefaultBotVersion()
			v.Charter = "Observe"
			b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "A", OwnerID: owner, Version: v, IdempotencyKey: "a"})
			if err != nil {
				t.Fatal(err)
			}
			target := store.BotChannelBinding{ID: uuid.New(), Channel: "telegram", ChatID: "123456"}
			other := uuid.New()
			if err := backend.Users().Create(ctx, &store.User{ID: other.String(), Email: "other@example.test", DisplayName: "B", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			orgB := &store.Organization{ID: uuid.NewString(), Slug: "org-b", Name: "B", DBName: "org_b", Status: "active"}
			if err := backend.Organizations().Create(ctx, orgB); err != nil {
				t.Fatal(err)
			}
			if err := backend.Organizations().AddMember(ctx, other.String(), orgB.ID, "member"); err != nil {
				t.Fatal(err)
			}
			foreign := store.BotChannelBinding{ID: uuid.New(), Channel: "slack", ChatID: "TOTHER:COTHER"}
			if err := backend.UserChannels().Link(ctx, &store.UserChannel{ID: foreign.ID.String(), UserID: other.String(), ChannelType: "slack", ExternalID: "TOTHER:UOTHER", Enabled: true, Verified: true}); err != nil {
				t.Fatal(err)
			}
			q := store.BotConfigurationRequest{BotID: b.ID, ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "review", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: store.DeliveryPolicy{Notify: store.NotifyAlways, Bindings: []store.BotChannelBinding{foreign}}}
			if _, err := repo.ConfigureReviewed(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("foreign workspace configuration: %v", err)
			}
			if err := repo.AuthorizeBotDelivery(ctx, b.ID, foreign); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("foreign send: %v", err)
			}
			if err := backend.UserChannels().Link(ctx, &store.UserChannel{ID: target.ID.String(), UserID: owner.String(), ChannelType: target.Channel, ExternalID: target.ChatID, Enabled: true, Verified: true}); err != nil {
				t.Fatal(err)
			}
			ownedSlack := store.BotChannelBinding{ID: uuid.New(), Channel: "slack", ChatID: "TOWN:CROOM"}
			if err := backend.UserChannels().Link(ctx, &store.UserChannel{ID: ownedSlack.ID.String(), UserID: owner.String(), ChannelType: "slack", ExternalID: "TOWN:UOWNER", Enabled: true, Verified: true}); err != nil {
				t.Fatal(err)
			}
			q.Delivery.Bindings = []store.BotChannelBinding{ownedSlack}
			if _, err := repo.ConfigureReviewed(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("Slack identity is not room/installation proof: %v", err)
			}
			// Even a valid owned link cannot authorize a different chat or thread.
			for _, bad := range []store.BotChannelBinding{
				{ID: target.ID, Channel: target.Channel, ChatID: "999999"},
				{ID: target.ID, Channel: target.Channel, ChatID: target.ChatID, ThreadID: "thread"},
				{ID: uuid.New(), Channel: target.Channel, ChatID: target.ChatID},
			} {
				q.Delivery.Bindings = []store.BotChannelBinding{bad}
				if _, err := repo.ConfigureReviewed(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("unproven configuration: %v", err)
				}
			}
			q.Delivery.Bindings = []store.BotChannelBinding{target}
			q.Delivery.Preferences = `{"defer_seconds":60}`
			b, err = repo.ConfigureReviewed(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			// Runtime readiness is not under test; activate only this disposable fixture.
			switch d := repo.db.(type) {
			case personalBotDB:
				err = d.client.Bot.UpdateOneID(b.ID).SetStatus("active").Exec(ctx)
			case teamBotDB:
				err = d.client.Bot.UpdateOneID(b.ID).SetStatus("active").Exec(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.AuthorizeBotDelivery(ctx, b.ID, target); err != nil {
				t.Fatal(err)
			}
			tampered := target
			tampered.ChatID = "999999"
			if err := repo.AuthorizeBotDelivery(ctx, b.ID, tampered); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal("destination substitution allowed")
			}
			run, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, IdempotencyKey: "run", Instructions: "observe"})
			if err != nil {
				t.Fatal(err)
			}
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
			now := time.Now()
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "worker", ExpiresAt: now.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "private A report"}}); err != nil {
				t.Fatal(err)
			}
			transport := &publicationTransport{}
			publisher := bot.Publisher{Store: repo, Notifier: transport, Now: func() time.Time { return now }}
			p, err := publisher.Publish(ctx, b.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if p.Notifications[0].State != store.BotNotificationDeferred || len(transport.calls) != 0 {
				t.Fatal("not deferred")
			}
			if err := backend.UserChannels().Unlink(ctx, target.ID.String()); err != nil {
				t.Fatal(err)
			}
			if err := repo.AuthorizeBotDelivery(ctx, b.ID, target); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("revoked send: %v", err)
			}
			now = now.Add(time.Minute)
			p, err = publisher.Publish(ctx, b.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(transport.calls) != 0 || p.Notifications[0].State != store.BotNotificationSkipped {
				t.Fatalf("revoked destination sent: %+v", p)
			}
		})
	}
}
