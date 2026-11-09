package backend

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

// BotBackend is an optional remote capability. Local code backends do not
// implement it. WatchBot observes durable work and must never cancel that work.
type BotBackend interface {
	ListBots(context.Context) ([]store.Bot, error)
	SelectBot(context.Context, string) (*store.Bot, error)
	SelectedBot() *store.Bot
	DetachBot()
	WatchBot(context.Context, func(events.Event) error) error
	BotApprovals(context.Context) ([]store.BotApproval, error)
	DecideBotApproval(context.Context, store.BotApproval, store.ApprovalStatus) error
}

// BotSessionBackend selects persisted traces without resuming or cancelling runs.
// uuid.Nil selects the current home session.
type BotSessionBackend interface {
	BotRuns(context.Context) ([]store.BotRun, error)
	SelectBotSession(context.Context, uuid.UUID) error
}

// BotOverviewBackend reads daemon-owned status without calculating a schedule locally.
type BotOverviewBackend interface {
	BotSnapshot(context.Context) (*store.BotSnapshot, error)
	BotGoals(context.Context) ([]store.BotGoal, error)
	BotStudioURL() string
}

// BotInteractionBackend binds a typed answer to a persisted approval.
type BotInteractionBackend interface {
	AnswerBotApproval(context.Context, store.BotApproval, store.BotInteractionResponse) error
}

// BotProposalBackend reviews saved proposals, never executable assistant prose.
type BotProposalBackend interface {
	BotProposals(context.Context) ([]store.BotControlProposal, error)
	DecideBotProposal(context.Context, store.BotControlProposal, bool) error
}

type BotInteractiveContinuationBackend interface {
	ContinueBotInteractive(context.Context, store.BotRun, *store.BotInteractionResponse) (*store.BotRun, error)
}

// BotContinuationBackend returns the atomic decision receipt, including paused blocking.
type BotContinuationBackend interface {
	ApproveBotAndContinue(context.Context, store.BotApproval, *store.BotInteractionResponse) (*store.BotApproveContinueResult, error)
}
