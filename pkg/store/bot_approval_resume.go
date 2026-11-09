package store

import (
	"context"

	"github.com/google/uuid"
)

// BotApprovalResumeStore is an optional owner-authenticated capability. A decision
// does not queue a run or activate a Bot. Only a safe waiting checkpoint may be
// explicitly requeued; interrupted/consumed attempts are never replayed.
type BotApprovalResumeStore interface {
	RequeueApprovedRun(context.Context, BotID, RunID, uuid.UUID) error
	LoadResumeApproval(context.Context, BotLease) (*BotApproval, error)
}

// BotApproveContinueRequest identifies only a saved approval. It cannot replace
// the saved run, checkpoint, action, or execution intent.
type BotApproveContinueRequest struct {
	BotID        BotID
	ApprovalID   uuid.UUID
	ActorID      uuid.UUID
	ActionDigest string
	Response     *BotInteractionResponse
}

type BotContinuationState string

const (
	BotContinuationQueued         BotContinuationState = "queued"
	BotContinuationBlockedByPause BotContinuationState = "blocked_by_pause"
)

// BotApproveContinueResult reports admission, not execution or Bot activation.
type BotApproveContinueResult struct {
	Approval     BotApproval          `json:"approval"`
	RunID        RunID                `json:"run_id"`
	Continuation BotContinuationState `json:"continuation"`
}

// BotApproveContinueStore atomically approves a saved checkpoint and queues its
// exact continuation. Paused Bots retain the approval without being resumed.
// Existing decision-only and resume-only capabilities remain independent.
type BotApproveContinueStore interface {
	DecideAndRequeueApprovedRun(context.Context, BotApproveContinueRequest) (*BotApproveContinueResult, error)
}
