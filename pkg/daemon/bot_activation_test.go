package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

type activationHomeBackend struct{ sandbox.Backend }

func (activationHomeBackend) EnsureBotHome(_ context.Context, s sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	return &sandbox.BotHomeBinding{Scope: s, Backend: "controlled", Ref: "registered-home"}, nil
}

func TestBotActivationConfiguredRuntime(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			org := &store.Organization{ID: uuid.NewString(), Slug: "activation", Name: "Activation", Status: "active"}
			if err := db.Organizations().Create(ctx, org); err != nil {
				t.Fatal(err)
			}
			if err := db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			ods, err := db.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
			if err := ods.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			if err := db.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := db.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			auth, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			repo := ods.ForTeam(team.Slug).Bots()
			pin := ods.ForTeam(team.Slug).SetSessionPin
			if scope == store.BotScopePersonal {
				repo = ods.ForUser(owner.String()).Bots()
				pin = ods.ForUser(owner.String()).SetSessionPin
			}
			v := store.DefaultBotVersion()
			v.Charter = "Require explicit reviewed activation"
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Activation", OwnerID: owner, Version: v, IdempotencyKey: "activation"})
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "Review"}
			if _, err := repo.Activate(auth, q); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("missing worker: %v", err)
			}
			llm := &workerTestModel{}
			w, err := NewBotWorker(BotWorkerConfig{Backend: db, Sandbox: activationHomeBackend{}, Pool: workerTestPool{}, WorkerID: "activation-worker", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(_ context.Context, p execution.Principal, s store.BotScope, n, m string) (model.LLM, error) {
				if p.Subject != owner.String() || s != scope || n != "controlled" || m != "model" {
					return nil, store.ErrBotForbidden
				}
				return llm, nil
			}, Prepare: func(context.Context, bot.RunRequest, *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				t.Fatal("activation started execution")
				return nil, store.ErrBotUnsupported
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer w.Stop()
			if _, err := repo.Activate(auth, q); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("missing home: %v", err)
			}
			registry, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: "registered-home", Revision: 1}
			if err := entstore.RegisterBotHomeBinding(registry, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: home}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			q.ExpectedRevision = b.Revision
			if _, err := repo.Activate(auth, q); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("missing pin: %v", err)
			}
			// Activation and execution must inherit the same owner-scoped defaults
			// without writing an implicit home pin or activating the draft.
			if err := db.PlatformSettings().Save(auth, &store.PlatformSettings{DefaultProvider: "controlled", DefaultModel: "model"}); err != nil {
				t.Fatal(err)
			}
			versionForReadiness, err := repo.GetVersion(auth, b.ID, b.Version)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.activationReady(auth, *b, *versionForReadiness); err != nil {
				t.Fatalf("inherited activation readiness: %v", err)
			}
			principal, _ := execution.PrincipalFromContext(auth)
			loaded, err := w.loadScope(auth, bot.RunRequest{Bot: *b, Principal: principal})
			if err != nil {
				t.Fatal(err)
			}
			selected, err := loaded.Models.Resolve(auth, b.HomeSessionID)
			loaded.Cleanup()
			if err != nil || selected.Provider != "controlled" || selected.Model != "model" {
				t.Fatalf("inherited worker model: %+v %v", selected, err)
			}
			current, err := repo.Get(auth, b.ID)
			if err != nil || current.Status != store.BotDraft {
				t.Fatalf("readiness activated draft: %+v %v", current, err)
			}
			if err := pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.RequestRun(auth, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "draft must not start", IdempotencyKey: "draft"}); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("draft request: %v", err)
			}
			active, err := repo.Activate(auth, q)
			if err != nil || active.Status != store.BotActive {
				t.Fatalf("activation: %+v %v", active, err)
			}
			if llm.calls != 0 {
				t.Fatal("activation called model")
			}
			version, err := repo.GetVersion(auth, b.ID, b.Version)
			if err != nil {
				t.Fatal(err)
			}
			version.Triggers = []store.TriggerSpec{{Enabled: true, Kind: store.TriggerCron}}
			if err := w.activationReady(auth, *active, *version); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("missing scheduler: %v", err)
			}
			callbacks := w.SchedulerCallbacks()
			ConfigureBotScheduler(nil, nil, callbacks)
			if w.schedulerReady.Load() {
				t.Fatal("nil executor marked ready")
			}
			executor := &scheduler.Executor{}
			ConfigureBotScheduler(executor, nil, callbacks)
			if executor.BotTrigger == nil || !w.schedulerReady.Load() {
				t.Fatal("scheduler installation missing")
			}
			if err := w.activationReady(auth, *active, *version); err != nil {
				t.Fatalf("configured scheduler: %v", err)
			}
			w.Stop()
			if err := w.activationReady(auth, *active, *version); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("stopped worker: %v", err)
			}
		})
	}
}
