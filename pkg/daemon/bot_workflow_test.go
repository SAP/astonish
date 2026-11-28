package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type workflowBackend struct {
	store.PlatformBackend
	org *workflowOrg
}

func (b workflowBackend) ForOrg(n string) (store.OrgDataStore, error) {
	if n != "owner-org" {
		panic("wrong org")
	}
	return b.org, nil
}
func (workflowBackend) PlatformSlideTemplates() store.SlideTemplateStore { return nil }

type workflowOrg struct {
	store.OrgDataStore
	team *workflowTeam
}

func (o workflowOrg) ForTeam(n string) store.TeamDataStore {
	if n != "owner-team" {
		panic("wrong team")
	}
	return o.team
}
func (workflowOrg) OrgSlideTemplates() store.SlideTemplateStore { return nil }

type workflowTeam struct {
	store.TeamDataStore
	reports int
}

func (t *workflowTeam) DrillReports() store.DrillReportStore           { t.reports++; return nil }
func (*workflowTeam) FleetSetupProfiles() store.FleetSetupProfileStore { return nil }

func TestProductionBotWorkflowRegistration(t *testing.T) {
	id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: uuid.NewString(), OrgSlug: "owner-org", TeamSlug: "owner-team"}
	p, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised})
	if err != nil {
		t.Fatal(err)
	}
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req := bot.RunRequest{Bot: store.Bot{Scope: store.BotScopePersonal}, Policy: p, Budget: meter, Principal: execution.Principal{OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug}}
	team := &workflowTeam{}
	backend := workflowBackend{org: &workflowOrg{team: team}}
	svc := &store.Services{}
	ts, err := productionBotWorkflowTools(context.Background(), backend, req, &launcher.BotChatScope{Services: svc})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, x := range ts {
		if names[x.Name()] {
			t.Fatalf("duplicate %s", x.Name())
		}
		names[x.Name()] = true
	}
	for _, n := range []string{"resolve_credential", "read_drill", "create_deck", "save_fleet_plan"} {
		if !names[n] {
			t.Fatalf("missing %s", n)
		}
	}
	if team.reports != 1 || svc.DrillReports != nil {
		t.Fatal("scope mutation or missing team lookup")
	}
}
