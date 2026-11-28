package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

// Exercises the real worker constructor, scoped SQLite commit hook, source
// receipt, model adapter and durable shared usage without a physical publisher.
func TestBotKnowledgeProductionWiringEveryTurnWithoutPublisher(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			c := context.Background()
			dir := t.TempDir()
			_, platform, err := entstore.NewPlatformServices(c, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer platform.Close()
			org := &store.Organization{ID: uuid.NewString(), Slug: "knowledge", Name: "Knowledge", Status: "active"}
			if err = platform.Organizations().Create(c, org); err != nil {
				t.Fatal(err)
			}
			if err = platform.ProvisionOrg(c, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			data, err := platform.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
			if err = data.Teams().CreateTeam(c, team); err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			if err = platform.Users().Create(c, &store.User{ID: owner.String(), Email: owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err = platform.Organizations().AddMember(c, owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err = data.Teams().AddMember(c, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			llm := &knowledgeAdapterModel{response: &model.LLMResponse{Content: nil}}
			w, err := NewBotWorker(BotWorkerConfig{Backend: platform, Sandbox: workerTestBackend{}, Pool: workerTestPool{}, WorkerID: "knowledge-worker", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return llm, nil
			}, Prepare: func(context.Context, bot.RunRequest, *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				return nil, store.ErrBotUnsupported
			}})
			if err != nil {
				t.Fatal(err)
			}
			if w.config.KnowledgeSafety == nil || w.config.KnowledgeRedactor == nil {
				t.Fatal("production safety not installed")
			}
			auth, _, err := w.authenticateOwner(c, BotSchedulerScope{org.Slug, team.Slug, owner.String()})
			if err != nil {
				t.Fatal(err)
			}
			repo := data.ForTeam(team.Slug).Bots()
			ss := data.ForTeam(team.Slug).Sessions()
			if scope == store.BotScopePersonal {
				repo = data.ForUser(owner.String()).Bots()
				ss = data.ForUser(owner.String()).Sessions()
			}
			v := store.DefaultBotVersion()
			v.Charter = "Retain honest source limitations"
			b, err := repo.Create(auth, store.BotCreateRequest{Name: "Knowledge", OwnerID: owner, Version: v, IdempotencyKey: "create"})
			if err != nil {
				t.Fatal(err)
			}
			platform.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
			registry, err := execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: "home", Revision: 1}
			if err = entstore.RegisterBotHomeBinding(registry, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: home}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			run, err := repo.RequestRun(auth, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "Reply without tools", IdempotencyKey: "turn"})
			if err != nil {
				t.Fatal(err)
			}
			worker := execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: w.config.WorkerID, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			worker = w.knowledgeCommitContext(worker, repo, ss)
			report := reviewedRecoveryReply(t, repo, worker, lease, "No tools needed")
			if err = repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: report}); err != nil {
				t.Fatal(err)
			}
			activities, err := repo.ListActivity(auth, b.ID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			receipts := 0
			for _, a := range activities {
				var envelope struct {
					Source *store.BotKnowledgeExtractionSource `json:"knowledge_source"`
				}
				if err = json.Unmarshal(a.Payload, &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Source == nil {
					continue
				}
				record, err := repo.(store.BotKnowledgeExtractionStore).ReadBotKnowledgeExtraction(auth, *envelope.Source)
				if err != nil {
					t.Fatal(err)
				}
				receipts++
				if record.State != store.BotKnowledgeExtractionHeld || record.Input.Complete || record.Input.OwnerMessage != "Reply without tools" {
					t.Fatalf("missing honest Held receipt: %+v", record)
				}
			}
			if receipts != 1 || llm.calls != 0 {
				t.Fatalf("receipts=%d paid calls=%d", receipts, llm.calls)
			}
			if err = w.reconcileKnowledge(auth, repo, ss, *b); err != nil {
				t.Fatal(err)
			}
			if llm.calls != 0 {
				t.Fatal("Held source generated")
			}
			// Preserve real sourced activity while withholding the journal interface;
			// an empty store has nothing to reconcile.
			if err = w.reconcileKnowledge(auth, &workerEventRepo{BotStore: repo}, ss, *b); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("silent unsupported journal: %v", err)
			}
		})
	}
}
