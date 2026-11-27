package daemon

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/google/uuid"
	"google.golang.org/adk/model"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

// completeEmployeeSchedule settles deferred schedules and authors missing failure
// feedback only after source commit and lease release, without execution authority.
func (w *BotWorker) completeEmployeeSchedule(ctx context.Context, repo store.BotStore, id store.BotID, runID store.RunID) error {
	if w.config.MandatePolicy == nil {
		return nil
	} // legacy composition
	run, err := repo.GetRun(ctx, id, runID)
	if err != nil {
		return err
	}
	if run.MandateRef == nil || !run.Status.Terminal() || !run.UsesHomeHistory() {
		return nil
	}
	rt, err := w.AuthorizedOperations(repo)
	if err != nil {
		return err
	}
	coordinator, ok := rt.(interface {
		CoordinateEmployeeSchedules(context.Context, store.BotMandateRef, uuid.UUID) ([]store.BotOperation, error)
	})
	if !ok {
		return store.ErrBotUnsupported
	}
	ops, applyErr := coordinator.CoordinateEmployeeSchedules(ctx, *run.MandateRef, runID)
	if len(ops) == 0 {
		// No deferred schedule is not a reason to leave a committed failure
		// silent. Keep already useful, source-bound Employee speech intact.
		if run.Status != store.RunFailed && run.Status != store.RunPartial ||
			run.Report.EmployeeMessage != nil && (run.Report.EmployeeMessage.Origin == store.BotEmployeeMessageAgent || run.Report.EmployeeMessage.Origin == store.BotEmployeeMessageAssessed || run.Report.EmployeeMessage.Origin == store.BotEmployeeMessageModel) && run.Report.ValidateEmployeeMessageIntegrity(run.Status) == nil {
			if errors.Is(applyErr, store.ErrBotForbidden) {
				return nil
			}
			return applyErr
		}
		if errors.Is(applyErr, store.ErrBotForbidden) {
			applyErr = nil // Presentation independently checks original authority.
		}
	}
	preparer, ok := repo.(store.BotEmployeePresentationPreparer)
	if !ok {
		return store.ErrBotUnsupported
	}
	presentations, ok := repo.(store.BotEmployeePresentationStore)
	if !ok {
		return store.ErrBotUnsupported
	}
	claim := store.BotEmployeePresentationRequest{BotID: id, RunID: runID, ClaimToken: "employee-final:" + runID.String()}
	p, err := preparer.PrepareEmployeePresentation(ctx, claim)
	if err != nil {
		return errors.Join(applyErr, err)
	}
	if p.State == store.BotEmployeePresentationCompleted || p.State == store.BotEmployeePresentationUnavailable || len(p.Turns) != 0 {
		return applyErr
	}
	p, err = presentations.ClaimEmployeePresentation(ctx, store.BotEmployeePresentationClaim{BotEmployeePresentationRequest: claim, ExpectedReportSequence: p.ReportSequence, ExpectedReportDigest: p.ReportDigest})
	if err != nil {
		return errors.Join(applyErr, err)
	}
	if p.State != store.BotEmployeePresentationClaimed {
		return applyErr // authority may expire between preparation and claim
	}
	// The private envelope owns the complete, canonically ordered evidence list.
	// Coordinator results may be unordered and include only schedules.
	ops = make([]store.BotOperation, 0, len(p.Operations))
	for _, identity := range p.Operations {
		op, readErr := rt.ReadAuthorizedOperation(ctx, *run.MandateRef, identity.OperationID)
		if readErr != nil {
			return errors.Join(applyErr, readErr)
		}
		ops = append(ops, *op)
	}
	b, err := repo.Get(ctx, id)
	if err != nil {
		return err
	}
	factory, err := launcher.NewBotEmployeePresentationFactory(launcher.BotEmployeePresentationFactoryConfig{LoadScope: w.loadScope, ResolvePinned: func(c context.Context, _ *launcher.BotChatScope, name, modelName string) (model.LLM, error) {
		principal, ok := execution.PrincipalFromContext(c)
		if !ok {
			return nil, store.ErrBotForbidden
		}
		return w.config.ResolveModel(c, principal, b.Scope, name, modelName)
	}, WrapModel: func(c context.Context, llm model.LLM, budget execution.RunBudget) (model.LLM, error) {
		return provider.NewBudgetLLM(llm, budget, botModelBudgetOptions(llm, w.config.ModelOptions))
	}})
	if err != nil {
		return err
	}
	dispatch := "employee-final-dispatch:" + runID.String()
	text, err := factory(ctx, launcher.BotEmployeePresentationRequest{Bot: *b, Source: *run, Claim: claim, Store: presentations, DispatchToken: dispatch, Operations: ops})
	completion := store.BotEmployeePresentationCompletion{BotEmployeePresentationRequest: claim, DispatchToken: dispatch, Text: text}
	if err != nil {
		completion.Text = ""
		completion.UnavailableReason = store.BotEmployeeReasonUnknown
	}
	_, completeErr := presentations.CompleteEmployeePresentation(ctx, completion)
	return errors.Join(applyErr, err, completeErr)
}

// ReconcileBotPublications must run after committed outcomes and on subsequent
// owner-authenticated polls. It scans committed intents, not mutable run status,
// so a waiting outcome and its resumed final outcome both survive a restart.
// Replaying deferred entries revalidates once due; uncertain sends are never retried.
func ReconcileBotPublications(ctx context.Context, repo store.BotStore, id store.BotID, notifier bot.Notifier, indexers ...func(context.Context, *store.BotPublication) error) error {
	publications, ok := repo.(store.BotPublicationStore)
	if !ok {
		return store.ErrBotUnsupported
	}
	list := repo.ListActivity
	if reader, ok := repo.(interface {
		ListBotPublicationActivity(context.Context, store.BotID, int64, int) ([]store.BotActivity, error)
	}); ok {
		list = reader.ListBotPublicationActivity
	}
	publisher := &bot.Publisher{Store: publications, Notifier: notifier}
	var failures []error
	var after int64
	for {
		activities, err := list(ctx, id, after, 200)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		for _, activity := range activities {
			after = activity.Sequence
			if activity.Kind != store.BotActivityReport || activity.RunID == nil {
				continue
			}
			var intent struct {
				Intent store.BotOutboxKind `json:"intent"`
			}
			if err := json.Unmarshal(activity.Payload, &intent); err != nil {
				failures = append(failures, err)
				continue
			}
			if intent.Intent != store.BotOutboxPublish {
				continue
			}
			publication, err := publisher.PublishOutcome(ctx, id, *activity.RunID, activity.Sequence)
			if err != nil {
				failures = append(failures, err)
			}
			// A notification error can coexist with a committed Home publication.
			// Index failures leave the same receipt available for metadata-only retry.
			if publication != nil && !publication.Suppressed {
				for _, index := range indexers {
					if err := index(ctx, publication); err != nil {
						failures = append(failures, err)
					}
				}
			}
		}
		if len(activities) < 200 {
			return errors.Join(failures...)
		}
	}
}
