package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

type unavailableWorkerBackend struct {
	store.PlatformBackend
	org store.OrgDataStore
}

func (b unavailableWorkerBackend) ForOrg(string) (store.OrgDataStore, error) { return b.org, nil }

type unavailableWorkerOrg struct {
	store.OrgDataStore
	team     store.TeamDataStore
	personal store.PersonalDataStore
}

func (o unavailableWorkerOrg) ForTeam(string) store.TeamDataStore     { return o.team }
func (o unavailableWorkerOrg) ForUser(string) store.PersonalDataStore { return o.personal }

type availableWorkerRepo struct {
	store.BotStore
	lists int
}

func (r *availableWorkerRepo) List(context.Context, store.BotPage) ([]store.Bot, error) {
	r.lists++
	return nil, nil
}

type availableWorkerTeam struct {
	store.TeamDataStore
	repo     store.BotStore
	sessions store.SessionStore
}

func (s availableWorkerTeam) Bots() store.BotStore         { return s.repo }
func (s availableWorkerTeam) Sessions() store.SessionStore { return s.sessions }

type availableWorkerPersonal struct {
	store.PersonalDataStore
	repo     store.BotStore
	sessions store.SessionStore
}

func (s availableWorkerPersonal) Bots() store.BotStore         { return s.repo }
func (s availableWorkerPersonal) Sessions() store.SessionStore { return s.sessions }

func TestBotWorkerPollUnavailableScopeFailsClosed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	_, backend, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "unavailable", Name: "Unavailable", Status: "active"}
	if err := backend.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := backend.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	data, err := backend.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "general", Name: "General", SchemaName: "team_general"}
	if err := data.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	if err := backend.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@example.test", DisplayName: "Owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := backend.Organizations().AddMember(ctx, owner, org.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if err := data.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"team", "personal", "team-bots", "team-sessions", "org"} {
		t.Run(missing, func(t *testing.T) {
			tr, pr := &availableWorkerRepo{}, &availableWorkerRepo{}
			ss := data.ForTeam(team.Slug).Sessions()
			td := availableWorkerTeam{repo: tr, sessions: ss}
			routed := unavailableWorkerOrg{OrgDataStore: data, team: td, personal: availableWorkerPersonal{repo: pr, sessions: ss}}
			switch missing {
			case "team":
				routed.team = nil
			case "personal":
				routed.personal = nil
			case "team-bots":
				td.repo = nil
				routed.team = td
			case "team-sessions":
				td.sessions = nil
				routed.team = td
			}
			var ods store.OrgDataStore = routed
			if missing == "org" {
				ods = nil
			}
			w := &BotWorker{config: BotWorkerConfig{Backend: unavailableWorkerBackend{PlatformBackend: backend, org: ods}, WorkerID: "availability-test"}}
			err := w.Poll(ctx)
			if err == nil || !strings.Contains(err.Error(), "unavailable") {
				t.Fatalf("unavailable scope must report error: %v", err)
			}
			if missing == "org" {
				if tr.lists+pr.lists != 0 {
					t.Fatal("unavailable org accessed")
				}
				return
			}
			if missing == "personal" {
				if tr.lists != 1 || pr.lists != 0 {
					t.Fatalf("wrong scoped polls team=%d personal=%d", tr.lists, pr.lists)
				}
			} else if tr.lists != 0 || pr.lists != 1 {
				t.Fatalf("wrong scoped polls team=%d personal=%d", tr.lists, pr.lists)
			}
		})
	}
}
