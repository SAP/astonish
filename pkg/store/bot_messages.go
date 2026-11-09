package store

import (
	"context"
	"github.com/google/uuid"
)

// BotRunPresentation controls transcript destinations independently of execution consent.
// The zero value retains the historical per-intent projection contract.
type BotRunPresentation string

const (
	BotRunPresentationLegacy          BotRunPresentation = ""
	BotRunPresentationHomeInteractive BotRunPresentation = "home_interactive"
)

// BotMessageRequest is a home conversation turn, not consent to activate a Bot.
type BotMessageRequest struct {
	ScheduleCommand *BotOwnerScheduleCommand `json:"schedule_command,omitempty"`
	BotID           BotID                    `json:"-"`
	Version         int64                    `json:"version"`
	IdempotencyKey  string                   `json:"idempotency_key"`
	Instructions    string                   `json:"instructions"`
	Attachments     []ChatFile               `json:"attachments,omitempty"`
}

type BotMessageReceipt struct {
	Run           BotRun    `json:"run"`
	HomeSessionID uuid.UUID `json:"home_session_id"`
	UserEventID   string    `json:"user_event_id"`
	HistoryCursor int64     `json:"history_cursor"`
}

// BotMessageStore atomically admits a run and its canonical user transcript event.
// Optional so legacy repositories and callers retain their existing contracts.
type BotMessageStore interface {
	AdmitMessage(context.Context, BotMessageRequest) (*BotMessageReceipt, error)
}

func (r BotRun) ControlOnly() bool {
	// Unknown modes never acquire execution merely through a regular intent.
	if !r.ExecutionMode.Valid() {
		return true
	}
	return r.Intent == BotIntentDraftConversation || r.Intent == BotIntentConversation
}
func (r BotRun) UsesHomeHistory() bool {
	return r.Presentation == BotRunPresentationHomeInteractive || r.Intent == BotIntentConversation
}
