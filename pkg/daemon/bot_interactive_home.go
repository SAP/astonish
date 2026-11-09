package daemon

import (
	"context"
	"fmt"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

// BotHomePrerequisiteError leaves the admitted turn queued, without a lease or
// runtime effects. Retrying preflight uses the same deterministic retained home.
type BotHomePrerequisiteError struct{ Err error }

func (e *BotHomePrerequisiteError) Error() string {
	return fmt.Sprintf("bot_home_prerequisite: %v", e.Err)
}
func (e *BotHomePrerequisiteError) Unwrap() error { return e.Err }

// prepareInteractiveHome is shared by title and message admissions. It runs once
// before Supervisor loads/claims the run, never inside admission's SQL transaction.
func (w *BotWorker) prepareInteractiveHome(ctx context.Context, repo store.BotStore, id store.BotID, runID store.RunID) error {
	run, err := repo.GetRun(ctx, id, runID)
	if err != nil {
		return err
	}
	if run.MandateRef != nil {
		runtime, err := entstore.NewBotMandateRuntime(repo, w.config.MandatePolicy)
		if err != nil {
			return err
		}
		purpose := execution.BotMandatePurposeTask
		if run.Intent == store.BotIntentConversation && run.Presentation == store.BotRunPresentationHomeInteractive {
			record, readErr := runtime.ReadBotMandate(ctx, *run.MandateRef)
			if readErr != nil {
				return readErr
			}
			purpose = record.Mandate.Purpose
		}
		if _, err = execution.VerifyBotMandateAuthorization(ctx, runtime, *run.MandateRef, purpose); err != nil {
			return err
		}
		b, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if b.Home.ID != uuid.Nil {
			return nil
		}
		if _, err = w.provisionHome(ctx, repo, id, b.Revision); err != nil {
			return &BotHomePrerequisiteError{Err: err}
		}
		_, err = execution.VerifyBotMandateAuthorization(ctx, runtime, *run.MandateRef, purpose)
		return err
	}
	if !run.IsInteractiveChat() {
		return nil
	}
	if run.Status != store.RunQueued || run.CallerCeiling == nil {
		return store.ErrBotConflict
	}
	b, err := repo.Get(ctx, id)
	if err != nil {
		return err
	}
	p := run.CallerCeiling.Principal
	if b.OwnerID.String() != p.Subject || b.Version != run.Version || !run.Intent.AllowsStatus(b.Status) {
		return store.ErrBotForbidden
	}
	identity := execution.BotRunIdentity{BotID: id, RunID: runID, Version: run.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
	auth, err := w.config.TurnRegistry.Authorizer(identity, *run.CallerCeiling, w.currentInteractivePrincipal)
	if err != nil {
		return err
	}
	caller, err := execution.WithPrincipal(botWorkerContext{ctx}, p)
	if err != nil {
		return err
	}
	caller = execution.WithBotRunIdentity(caller, identity)
	caller = execution.WithBotTurnAuthorizer(caller, auth)
	caller, cancel := context.WithDeadline(caller, run.CallerCeiling.ExpiresAt)
	defer cancel()
	if err = auth.Authorize(caller, execution.CapabilityChat); err != nil {
		return err
	}
	if b.Home.ID != uuid.Nil {
		return nil
	}
	if _, err = w.provisionHome(caller, repo, id, b.Revision); err != nil {
		return &BotHomePrerequisiteError{Err: err}
	}
	return auth.Authorize(caller, execution.CapabilityChat)
}
