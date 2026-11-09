package store

import "context"

// BotSnapshotReader is an optional read-only projection over the existing scoped
// Bot store. The viewer is always derived from the authenticated principal.
type BotSnapshotReader interface {
	Snapshot(context.Context, BotID) (*BotSnapshot, error)
	ReadCursor(context.Context, BotID) (*BotReadCursor, error)
}

type BotSnapshot struct {
	Operations []BotOperationView `json:"operations"`
	Mandate    *BotMandateSummary `json:"mandate"`
	// RunStates are read-only projections at LatestSequence, including legacy runs.
	RunStates []BotRunState `json:"run_states"`
	// HistoryCursor is the home transcript high-water mark captured by this snapshot.
	HistoryCursor    int64                `json:"history_cursor"`
	Bot              Bot                  `json:"bot"`
	ReadCursor       BotReadCursor        `json:"read_cursor"`
	LatestSequence   int64                `json:"latest_sequence"`
	UnreadCount      int64                `json:"unread_count"`
	PendingProposals []BotControlProposal `json:"pending_proposals"`
	PendingApprovals []BotApproval        `json:"pending_approvals"`
	LatestOutcome    *BotRun              `json:"latest_outcome,omitempty"`
}
