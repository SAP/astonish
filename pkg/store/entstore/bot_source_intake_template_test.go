package entstore

import (
	"context"
	"errors"
	team "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestBotSourceIntakeTemplateReceiptRecovery(t *testing.T) {
	ctx, _, _, _, reopen := ApprovalScopeReopenFixture(t, store.BotScopePersonal)
	destination, _ := reopen()
	ownerPrincipal, _ := execution.PrincipalFromContext(ctx)
	owner := uuid.MustParse(ownerPrincipal.Subject)
	tc := newTeamClient(t)
	auth := botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "admin", nil }}
	source := &teamBotStore{client: tc, auth: auth}
	templates := &teamBotTemplateStore{client: tc, auth: auth}
	template, err := templates.Publish(ctx, store.BotTemplateDefinition{SchemaVersion: store.BotSchemaVersion, Name: "Template", Charter: "Observe"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	p, err := source.ProposeSourceIntake(ctx, store.BotSourceIntakeRequest{Mode: store.BotIntakeTemplate, SourceID: template.ID, TemplateRevision: template.Revision, Name: "Private", IdempotencyKey: "recovery"}, templates)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	tc.BotIntake.Use(func(next team.Mutator) team.Mutator {
		return team.MutateFunc(func(ctx context.Context, m team.Mutation) (team.Value, error) {
			writes++
			if writes == 3 {
				return nil, errors.New("final receipt fails")
			}
			return next.Mutate(ctx, m)
		})
	})
	q := store.BotSourceIntakeDecision{Digest: p.Digest, Decision: "confirm"}
	if _, err := source.DecideSourceIntake(ctx, p.ID, q, templates, destination); err == nil {
		t.Fatal("missing final receipt failure")
	}
	saved, err := source.GetSourceIntake(ctx, p.ID)
	if err != nil || saved.Status != store.BotSourceIntakeApplying || saved.BotID != nil {
		t.Fatalf("ambiguous receipt %+v %v", saved, err)
	}
	key, _ := botHash([]string{owner.String(), sourceDraftKey(p)})
	created, err := destination.(*personalBotStore).repository().db.FindCreate(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if created.Bot.Status != store.BotDraft {
		t.Fatal("fork activated")
	}
	if _, err := source.DecideSourceIntake(ctx, p.ID, store.BotSourceIntakeDecision{Digest: p.Digest, Decision: "reject"}, templates, destination); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("rejected committed fork: %v", err)
	}
	if _, err := tc.BotTemplate.UpdateOneID(template.ID).AddRevision(1).Save(ctx); err != nil {
		t.Fatal(err)
	}
	destination, _ = reopen()
	recovered, err := source.DecideSourceIntake(ctx, p.ID, q, templates, destination)
	if err != nil || recovered.Status != store.BotSourceIntakeApplied || recovered.BotID == nil || *recovered.BotID != created.Bot.ID {
		t.Fatalf("recovery %+v %v", recovered, err)
	}
}
