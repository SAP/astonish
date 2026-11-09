package daemon

import (
	"context"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"time"
)

// newProductionBotMandatePolicy shares current membership, limits and network policy between admission and execution.
func newProductionBotMandatePolicy(backend store.PlatformBackend, cfg *config.AppConfig) store.BotMandatePolicyResolver {
	return func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
		limits, err := cfg.EffectiveBotMandateConfig()
		if err != nil {
			return store.BotMandatePolicy{}, err
		}
		w := BotWorker{config: BotWorkerConfig{Backend: backend, WorkerID: "mandate-policy"}}
		if _, err = w.currentInteractivePrincipal(ctx, execution.Principal{Subject: ref.OwnerID, OrgSlug: ref.OrgSlug, TeamSlug: ref.TeamSlug}); err != nil {
			return store.BotMandatePolicy{}, err
		}
		org, err := backend.ForOrg(ref.OrgSlug)
		if err != nil {
			return store.BotMandatePolicy{}, err
		}
		if org == nil || org.ForTeam(ref.TeamSlug) == nil {
			return store.BotMandatePolicy{}, store.ErrBotForbidden
		}
		networks, ok := backend.(interface {
			PlatformNetworkPolicies() store.NetworkPolicyStore
		})
		if !ok {
			return store.BotMandatePolicy{}, store.ErrBotUnsupported
		}
		svc := &store.Services{PlatformNetworkPolicies: networks.PlatformNetworkPolicies(), NetworkPolicies: org.OrgNetworkPolicies(), TeamNetworkPolicies: org.ForTeam(ref.TeamSlug).NetworkPolicies()}
		policy, err := launcher.NewBotRoutineReadMandatePolicy(svc, limits)(ctx, ref)
		if err != nil {
			return store.BotMandatePolicy{}, err
		}
		policy = addBotOwnControlBaseline(policy)
		var routine config.BotRoutinePolicyConfig
		if cfg != nil {
			routine = cfg.BotRoutinePolicy
		}
		return restrictBotRoutinePolicy(policy, routine)
	}
}
func (w *BotWorker) prepareMandate(ctx context.Context, repo store.BotStore, in store.BotRunInput, identity execution.BotRunIdentity) (bot.RunPreparation, error) {
	if in.Run.MandateRef == nil || w.config.MandatePolicy == nil {
		return bot.RunPreparation{}, &bot.RunError{Class: bot.ErrorPermission, Message: "missing durable mandate"}
	}
	runtime, err := entstore.NewBotMandateRuntime(repo, w.config.MandatePolicy)
	if err != nil {
		return bot.RunPreparation{}, err
	}
	purpose := execution.BotMandatePurposeTask
	if in.Run.Intent == store.BotIntentConversation && in.Run.Presentation == store.BotRunPresentationHomeInteractive {
		record, readErr := runtime.ReadBotMandate(ctx, *in.Run.MandateRef)
		if readErr != nil {
			return bot.RunPreparation{}, readErr
		}
		purpose = record.Mandate.Purpose
	}
	auth, err := execution.VerifyBotMandateAuthorization(ctx, runtime, *in.Run.MandateRef, purpose)
	if err != nil {
		return bot.RunPreparation{}, &bot.RunError{Class: bot.ErrorPermission, Message: "mandate invalid or revoked"}
	}
	m := &bot.MandateExecution{Authorization: auth, Runtime: runtime, Identity: identity}
	if err := m.Check(ctx); err != nil {
		return bot.RunPreparation{}, err
	}
	var tools []string
	for _, c := range auth.Snapshot().Constraints {
		if c.Action == execution.CapabilityToolExecute {
			tools = append(tools, c.Resources...)
		}
	}
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: identity, Level: execution.AutonomyScoped, AllowedTools: tools, DeniedTools: in.Version.Autonomy.DeniedTools})
	if err != nil {
		return bot.RunPreparation{}, err
	}
	limits := auth.Snapshot().Limits
	if in.Version.Budget.MaxRuntimeSeconds <= 0 || in.Version.Budget.MaxRuntimeSeconds > limits.MaxRuntimeSeconds {
		in.Version.Budget.MaxRuntimeSeconds = limits.MaxRuntimeSeconds
	}
	if in.Version.Budget.MaxTokens == nil || *in.Version.Budget.MaxTokens > limits.MaxTokens {
		in.Version.Budget.MaxTokens = &limits.MaxTokens
	}
	budget, err := botRunBudget(in, time.Now())
	if err != nil {
		return bot.RunPreparation{}, err
	}
	if err = runtime.ReserveBotMandateRun(ctx, auth.Ref(), in.Run.ID); err != nil {
		return bot.RunPreparation{}, err
	}
	return bot.RunPreparation{Mandate: m, Policy: policy, Budget: &bot.MandateBudget{SharedRunBudget: budget, Mandate: m, OnReleaseFailure: w.queueMandateCleanup}}, nil
}
