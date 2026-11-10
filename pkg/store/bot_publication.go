package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// BotPublicationStore is an optional scoped capability, not an HTTP mutation API.
// PublishReport atomically appends to the existing home transcript and records a
// receipt. ClaimNotification commits uncertainty BEFORE external IO: only its
// winning caller may send. Claims are never automatically reclaimed.
type BotPublicationStore interface {
	PublishReport(context.Context, BotPublicationRequest) (*BotPublication, error)
	ClaimNotification(context.Context, BotNotificationClaim) (*BotNotification, bool, error)
	FinishNotification(context.Context, BotNotificationFinish) error
}

type BotPublicationRequest struct {
	ReportSequence int64 `json:"report_sequence,omitempty"`
	BotID          BotID
	RunID          RunID
	Now            time.Time
}

type BotNotificationState string

const (
	BotNotificationPending   BotNotificationState = "pending"
	BotNotificationDeferred  BotNotificationState = "deferred"
	BotNotificationSkipped   BotNotificationState = "skipped"
	BotNotificationUncertain BotNotificationState = "uncertain"
	BotNotificationDelivered BotNotificationState = "delivered"
)

type BotPublication struct {
	Suppressed     bool              `json:"suppressed,omitempty"`
	ReportSequence int64             `json:"report_sequence,omitempty"`
	BotID          BotID             `json:"bot_id"`
	RunID          RunID             `json:"run_id"`
	Version        int64             `json:"version"`
	HomeSessionID  uuid.UUID         `json:"home_session_id"`
	EventID        string            `json:"event_id"`
	Text           string            `json:"text"`
	PublishedAt    time.Time         `json:"published_at"`
	Notifications  []BotNotification `json:"notifications"`
}

type BotNotification struct {
	ReportSequence int64                `json:"report_sequence,omitempty"`
	BotID          BotID                `json:"bot_id"`
	RunID          RunID                `json:"run_id"`
	Target         BotChannelBinding    `json:"target"`
	State          BotNotificationState `json:"state"`
	Decision       NotificationDecision `json:"decision"`
	Reason         string               `json:"reason"`
	AvailableAt    time.Time            `json:"available_at"`
	AttemptID      uuid.UUID            `json:"attempt_id"`
	Receipt        string               `json:"receipt,omitempty"`
	Text           string               `json:"text"`
}

type BotNotificationClaim struct {
	ReportSequence int64 `json:"report_sequence,omitempty"`
	BotID          BotID
	RunID          RunID
	BindingID      uuid.UUID
	Now            time.Time
}

type BotNotificationFinish struct {
	ReportSequence int64 `json:"report_sequence,omitempty"`
	BotID          BotID
	RunID          RunID
	BindingID      uuid.UUID
	AttemptID      uuid.UUID
	// Delivered requires positive transport evidence. False preserves uncertainty.
	Delivered bool
	Receipt   string
	Now       time.Time
}
