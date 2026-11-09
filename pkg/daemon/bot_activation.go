package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

// activationReady verifies configuration and recovers an already registered
// durable home. It does not provision a home, start compute, generate model
// content or infer isolation from an HTTP caller's claims. Run preparation still
// verifies live isolation, policy, budget and lease immediately before execution.
func (w *BotWorker) activationReady(ctx context.Context, b store.Bot, v store.BotVersion) error {
	if v.Version != b.Version {
		return store.ErrBotConflict
	}
	return w.versionReady(ctx, b, v)
}
func (w *BotWorker) scheduleCandidateReady(ctx context.Context, b store.Bot, v store.BotVersion) error {
	if v.Version != b.Version+1 {
		return store.ErrBotConflict
	}
	return w.versionReady(ctx, b, v)
}
func (w *BotWorker) versionReady(ctx context.Context, b store.Bot, v store.BotVersion) error {
	if w.activationStopped.Load() || w.runtime == nil {
		return store.ErrBotUnsupported
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser {
		return store.ErrBotForbidden
	}
	ownerCtx, org, err := w.authenticateOwner(ctx, BotSchedulerScope{p.OrgSlug, p.TeamSlug, b.OwnerID.String()})
	if err != nil {
		return err
	}
	owner, _ := execution.PrincipalFromContext(ownerCtx)
	repo := org.ForTeam(owner.TeamSlug).Bots()
	pin := org.ForTeam(owner.TeamSlug).SessionPin
	if b.Scope == store.BotScopePersonal {
		repo = org.ForUser(owner.Subject).Bots()
		pin = org.ForUser(owner.Subject).SessionPin
	} else if b.Scope != store.BotScopeTeam {
		return store.ErrBotForbidden
	}
	current, err := repo.Get(ownerCtx, b.ID)
	if err != nil {
		return err
	}
	if current == nil || current.OwnerID != b.OwnerID || current.Version != b.Version || current.Revision != b.Revision || v.BotID != b.ID {
		return store.ErrBotConflict
	}
	for _, tr := range v.Triggers {
		if tr.Enabled && !w.schedulerReady.Load() {
			return store.ErrBotUnsupported
		}
	}
	homes := launcher.BotHomeManager{Backend: w.config.Sandbox.(sandbox.BotHomeBackend), Pool: w.config.Pool, Bots: repo}
	if _, err := homes.Ensure(ownerCtx, bot.HomeRequest{BotID: b.ID, Scope: b.Scope}); err != nil {
		return fmt.Errorf("%w: durable home: %v", store.ErrBotUnsupported, err)
	}
	resolver := launcher.BotChatModelResolver{SessionPin: func(c context.Context, id string) (*store.SessionPin, error) {
		selected, err := pin(c, id)
		if err != nil {
			return nil, err
		}
		var personal store.PersonalSettingsStore
		if b.Scope == store.BotScopePersonal {
			personal = org.ForUser(owner.Subject).PersonalSettings()
		}
		resolved, err := store.ResolveBotModelSelection(c, selected, w.config.Backend.PlatformSettings(), w.config.Backend.OrgSettings(owner.OrgSlug), org.ForTeam(owner.TeamSlug).Settings(), personal)
		return &resolved, err
	}, ResolveProvider: func(c context.Context, name, modelName string) (model.LLM, error) {
		return w.config.ResolveModel(c, owner, b.Scope, name, modelName)
	}}
	resolved, err := resolver.Resolve(ownerCtx, b.HomeSessionID)
	if err != nil {
		return fmt.Errorf("%w: pinned model: %v", store.ErrBotUnsupported, err)
	}
	// A throw-away identity validates policy construction without creating a run
	// or granting any authority. It is never installed in a context or persisted.
	_, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: execution.BotRunIdentity{BotID: b.ID, RunID: uuid.New(), Version: v.Version, ActorID: owner.Subject, OrgSlug: owner.OrgSlug, TeamSlug: owner.TeamSlug}, Level: v.Autonomy.Level, AllowedTools: v.Autonomy.AllowedTools, DeniedTools: v.Autonomy.DeniedTools})
	if err != nil {
		return fmt.Errorf("%w: policy: %v", store.ErrBotUnsupported, err)
	}
	budget, err := bot.NewRunBudget(v.Budget, time.Now())
	if err != nil {
		return fmt.Errorf("%w: budget: %v", store.ErrBotUnsupported, err)
	}
	_, err = provider.NewBudgetLLM(resolved.LLM, budget, w.config.ModelOptions)
	if err != nil {
		return fmt.Errorf("%w: model budget: %v", store.ErrBotUnsupported, err)
	}
	return nil
}
