package store

import "context"

// BotTitleStartStore is optional: legacy CreateDraft and imports never start work.
// Implementations commit the draft, home, title message, run and dispatch together.
type BotTitleStartStore interface {
	CreateDraftAndStart(context.Context, BotCreateRequest) (*BotTitleStartReceipt, error)
}

type BotTitleStartReceipt struct {
	Bot
	Run           BotRun `json:"run"`
	UserEventID   string `json:"user_event_id"`
	HistoryCursor int64  `json:"history_cursor"`
}
