package store

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
)

// BotMessageAdmission is trusted process-local composition, never HTTP input.
// Register is invoked inside the admission transaction only AFTER replay lookup.
// Cancel revokes precommit authority on rollback or uncertain commit outcome.
type BotMessageAdmission interface {
	Register(context.Context, execution.BotRunIdentity) (execution.BotTurnAuthorization, error)
	Cancel(execution.BotRunIdentity)
}
type botMessageAdmissionKey struct{}

func WithBotMessageAdmission(ctx context.Context, admission BotMessageAdmission) context.Context {
	return context.WithValue(ctx, botMessageAdmissionKey{}, admission)
}
func BotMessageAdmissionFromContext(ctx context.Context) (BotMessageAdmission, bool) {
	if ctx == nil {
		return nil, false
	}
	a, ok := ctx.Value(botMessageAdmissionKey{}).(BotMessageAdmission)
	return a, ok && a != nil
}

// RequiresPhysicalHome distinguishes actual execution from legacy control-only
// conversations. Unknown execution modes do not gain physical execution.
func (r BotRun) RequiresPhysicalHome() bool {
	return r.ExecutionMode.Valid() && (r.ExecutionMode == execution.BotExecutionModeChatInteractive || !r.ControlOnly())
}

// RequiresAutonomousPolicy retains strict policy validation for every non-chat mode.
func (r BotRun) RequiresAutonomousPolicy() bool { return !r.IsInteractiveChat() }

func (r BotRun) IsInteractiveChat() bool {
	return r.ExecutionMode == execution.BotExecutionModeChatInteractive
}
