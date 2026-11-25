package api

import (
	"context"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/store"
)

// ApproveBotDefinitionPlan uses the shared session engine. Only the trusted
// resume adapter can install the owner decision in this attempt's context.
type botDefinitionConsentKey struct{}

// WithBotDefinitionConsentFromContext carries only the typed, repository-loaded
// owner consent through the value-free factory boundary.
func WithBotDefinitionConsentFromContext(dst, src context.Context) context.Context {
	approval, _ := src.Value(botResumeInteractionKey{}).(*store.BotApproval)
	if approval != nil && approval.Interaction != nil && approval.Interaction.Kind == store.BotInteractionPlanDefinition {
		return context.WithValue(dst, botDefinitionConsentKey{}, approval)
	}
	return dst
}

func ApproveBotDefinitionPlan(ctx context.Context, runtime *agent.PlanRuntime) error {
	approval, _ := ctx.Value(botDefinitionConsentKey{}).(*store.BotApproval)
	if approval == nil {
		approval, _ = ctx.Value(botResumeInteractionKey{}).(*store.BotApproval)
	}
	if approval == nil || approval.Interaction == nil || approval.Interaction.Kind != store.BotInteractionPlanDefinition {
		return nil
	}
	if approval.Status != store.ApprovalApproved || approval.DecidedBy == nil || approval.DecidedAt == nil || approval.Response == nil || approval.Interaction.ValidateResponse(approval.Response) != nil {
		return store.ErrBotForbidden
	}
	c, err := approval.Interaction.PlanDefinition()
	if err != nil {
		return err
	}
	s := runtime.Snapshot()
	if s.PlanID != c.PlanID || s.Revision != c.Revision || s.Sequence != c.Sequence {
		return store.ErrBotConflict
	}
	return approveInitialSessionPlan(runtime)
}
