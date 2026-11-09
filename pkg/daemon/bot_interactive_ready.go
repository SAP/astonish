package daemon

import (
	"context"

	"github.com/SAP/astonish/pkg/store"
)

// setBotInteractiveReady opts in only after the worker, live authority registry,
// scoped factory and typed home provisioner have all been installed.
func setBotInteractiveReady(svc *store.Services, w *BotWorker) {
	if svc == nil {
		return
	}
	svc.BotInteractiveReady = w != nil && w.runtime != nil && w.config.Prepare != nil && w.config.TurnRegistry != nil && svc.BotTurnRegistry == w.config.TurnRegistry && svc.BotHomeSetup != nil && !w.activationStopped.Load()
	svc.BotAuthorizedOperationFactory = nil
	if svc.BotInteractiveReady && w.config.MandatePolicy != nil {
		// Resolve from request-scoped services, never capture a tenant's repo.
		svc.BotAuthorizedOperationFactory = func(_ context.Context, scoped *store.Services, scope store.BotScope) (store.BotAuthorizedOperationStore, error) {
			if scoped == nil || w.activationStopped.Load() {
				return nil, store.ErrBotUnsupported
			}
			var repo store.BotStore
			switch scope {
			case store.BotScopePersonal:
				repo = scoped.PersonalBots
			case store.BotScopeTeam:
				repo = scoped.Bots
			default:
				return nil, store.ErrBotForbidden
			}
			if repo == nil {
				return nil, store.ErrBotUnsupported
			}
			return w.AuthorizedOperations(repo)
		}
	}
}
