package daemon

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

func TestBotWorkerInteractivePrincipalPreservesCallerCeiling(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	_, backend, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "interactive-worker", Name: "interactive", Status: "active"}
	if err := backend.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := backend.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := backend.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "team", SchemaName: "team_team"}
	if err := ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	w := &BotWorker{config: BotWorkerConfig{Backend: backend, WorkerID: "worker"}}
	for _, role := range []string{"member", "viewer"} {
		t.Run(role, func(t *testing.T) {
			user := &store.User{ID: uuid.NewString(), Email: uuid.NewString() + "@example.test", DisplayName: role, Status: "active"}
			if err := backend.Users().Create(ctx, user); err != nil {
				t.Fatal(err)
			}
			if err := backend.Organizations().AddMember(ctx, user.ID, org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: user.ID, TeamID: team.ID, Role: role}); err != nil {
				t.Fatal(err)
			}
			original := execution.Principal{Kind: execution.PrincipalKindUser, Subject: user.ID, OrgSlug: org.Slug, TeamSlug: team.Slug, Authentication: execution.AuthMethodOAuth, Surface: execution.Surface("api"), Authenticated: true, Scopes: []string{"chat"}}
			current, err := w.currentInteractivePrincipal(ctx, original)
			if role == "viewer" {
				if err == nil {
					t.Fatal("viewer retained interactive authority")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := original
			want.Roles = []string{role}
			if !reflect.DeepEqual(current, want) {
				t.Fatalf("caller ceiling changed: %+v", current)
			}
			if err := (execution.CapabilityAuthorizer{}).Authorize(current, execution.CapabilityToolExecute); err == nil {
				t.Fatal("daemon promoted chat-only caller to tools")
			}
			original.TeamSlug = "not-a-member"
			if _, err := w.currentInteractivePrincipal(ctx, original); err == nil {
				t.Fatal("lost team membership accepted")
			}
		})
	}
}
