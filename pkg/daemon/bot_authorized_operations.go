package daemon

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"strings"
	"time"
)

// AuthorizeBotOwnerSchedule checks the issuer's own-control ceiling against a
// candidate from the canonical direct owner turn. Natural-language interpretation
// belongs to that turn's model, not a second model or keyword consent detector.
// This does not prove semantic intent; storage independently enforces the exact
// source, live lease, finite quotas, bot scope and immutable candidate grant.
func AuthorizeBotOwnerSchedule(_ context.Context, a store.BotScheduleAuthorization) error {
	if a.Mandate.Validate(time.Now()) != nil || (a.Mandate.Purpose != execution.BotMandatePurposeTask && a.Mandate.Purpose != execution.BotMandatePurposeOperation) || a.InitiationKind != store.BotInitiationKindDirectOwnerMessage || strings.TrimSpace(a.Instructions) == "" || a.Provenance.SourceUserID != a.Mandate.Ref.OwnerID || a.Provenance.UserEventID == "" || a.Provenance.SourceDigest == "" {
		return store.ErrBotForbidden
	}
	if a.Mandate.Purpose == execution.BotMandatePurposeOperation && (entstore.RoutineControlResource(a.Payload.Operation) == "" || a.Payload.Operation == store.BotControlTaskOnceOperation) {
		return store.ErrBotForbidden
	}
	resource := ""
	switch a.Payload.Operation {
	case store.BotControlScheduleCreate:
		resource = "schedule_job"
	case store.BotControlScheduleEdit, store.BotControlSchedulePause:
		resource = "update_scheduled_job"
	case store.BotControlScheduleRemove:
		resource = "remove_scheduled_job"
	default:
		// This callback establishes canonical direct-owner intent, not a grant.
		// The store independently chooses Execute or RequireAuthorization using
		// the exact issuer ceiling. Unknown controls never enter that path.
		if entstore.RoutineControlResource(a.Payload.Operation) != "" {
			return nil
		}
		return store.ErrBotForbidden
	}
	for _, c := range a.Mandate.Constraints {
		if c.Action == execution.CapabilityToolExecute {
			for _, r := range c.Resources {
				if r == resource {
					return nil
				}
			}
		}
	}
	return store.ErrBotForbidden
}

// prepareAuthorizedSchedules replaces scheduler entries with run-bound capture
// adapters. Only these exact adapters receive read-class policy: they persist a
// proposal, never execute a scheduler mutation under the source lease.
func (w *BotWorker) prepareAuthorizedSchedules(ctx context.Context, q bot.RunRequest, scope *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
	safe, err := w.config.Prepare(ctx, q, scope)
	if err != nil || safe == nil || q.Mandate == nil {
		return safe, err
	}
	adapters, err := launcher.BotScheduleTools(q, scope)
	if err != nil {
		return safe, err
	}
	names := map[string]bool{}
	for _, t := range adapters {
		names[t.Name()] = true
	}
	kept := safe.Tools[:0]
	for _, t := range safe.Tools {
		if t == nil || !names[t.Name()] {
			kept = append(kept, t)
		}
	}
	safe.Tools = append(kept, adapters...)
	safe.PolicyRuntime.Classifier = authorizedScheduleCaptureClassifier{base: safe.PolicyRuntime.Classifier, names: names}
	return safe, nil
}

type authorizedScheduleCaptureClassifier struct {
	base  agent.BotActionClassifier
	names map[string]bool
}

func (c authorizedScheduleCaptureClassifier) DescribeAction(ctx context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	if c.names[name] {
		return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
	}
	if c.base == nil {
		return execution.ActionDescriptor{}, store.ErrBotForbidden
	}
	return c.base.DescribeAction(ctx, name, args)
}

// AuthorizedOperations is composition-root capability; launcher receives this
// runtime, never a generic scheduler store or an interactive registry fallback.
func (w *BotWorker) AuthorizedOperations(repo store.BotStore) (store.BotAuthorizedOperationStore, error) {
	return entstore.NewBotAuthorizedOperationRuntime(repo, w.config.MandatePolicy, AuthorizeBotOwnerSchedule, w.scheduleCandidateReady)
}
func (w *BotWorker) reconcileAuthorizedOperations(ctx context.Context, repo store.BotStore, id store.BotID) error {
	if w.config.MandatePolicy == nil {
		return nil
	} // legacy composition
	rt, err := w.AuthorizedOperations(repo)
	if err != nil {
		return err
	}
	drain, ok := rt.(interface {
		ReconcileAuthorizedOperations(context.Context, store.BotID, int) error
	})
	if !ok {
		return store.ErrBotUnsupported
	}
	return drain.ReconcileAuthorizedOperations(api.WithBotOperationWakeups(ctx), id, 32)
}
