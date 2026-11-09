package store

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
)

// Explicit continuation never changes task input or activates autonomous schedules.
type BotContinueInteractiveRequest struct {
	BotID            BotID                   `json:"-"`
	RunID            RunID                   `json:"-"`
	ExpectedRevision int64                   `json:"expected_revision"`
	IdempotencyKey   string                  `json:"idempotency_key"`
	Response         *BotInteractionResponse `json:"response,omitempty"`
}
type BotContinuationReason string

const BotContinuationRequired BotContinuationReason = "continuation_required"

type BotInteractiveContinuationStore interface {
	ContinueInteractive(context.Context, BotContinueInteractiveRequest) (*BotRun, error)
	RequireInteractiveContinuation(context.Context, BotID, RunID) (*BotRun, error)
}

// Trusted context adapter; registration is permitted only after the repository
// has proven a never-started queue or a durable typed question checkpoint.
type BotContinuationAdmission interface {
	RegisterContinuation(context.Context, execution.BotRunIdentity, execution.BotCallerCeiling) (execution.BotTurnAuthorization, error)
	Cancel(execution.BotRunIdentity)
}
