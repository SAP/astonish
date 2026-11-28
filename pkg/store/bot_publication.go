package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"time"
)

// BotPublicationStore is an optional scoped capability, not an HTTP mutation API.
// Claims commit uncertainty BEFORE external IO and are never reclaimed.
type BotPublicationStore interface {
	PublishReport(context.Context, BotPublicationRequest) (*BotPublication, error)
	ClaimNotification(context.Context, BotNotificationClaim) (*BotNotification, bool, error)
	FinishNotification(context.Context, BotNotificationFinish) error
}
type BotCommittedArtifactStore interface {
	ReadCommittedArtifact(context.Context, BotID, int64, uuid.UUID) (*BotArtifact, error)
}
type BotFilePublicationStore interface {
	ReadFilePublication(context.Context, BotID, RunID, int64) (*BotFilePublication, error)
}

// BotPublicationReader resolves an exact receipt without executing or reconciling.
type BotPublicationReader interface {
	ReadBotPublication(context.Context, BotID, RunID, int64) (*BotPublication, error)
}

const BotPublicationKey = "bot_publication"

// BotEmployeeInterimKey is an exact persisted speech binding, never a boolean grant.
const BotEmployeeInterimKey = "bot_employee_interim"

type BotEmployeeInterim struct {
	BotID           BotID     `json:"bot_id"`
	RunID           RunID     `json:"run_id"`
	SourceSessionID uuid.UUID `json:"source_session_id"`
	HomeSessionID   uuid.UUID `json:"home_session_id"`
	EventID         string    `json:"event_id"`
	TurnID          uuid.UUID `json:"turn_id"`
	Sequence        int64     `json:"sequence"`
	TextDigest      string    `json:"text_digest"`
	EnvelopeDigest  string    `json:"envelope_digest"`
}

type BotEmployeeInterimReader interface {
	ReadBotEmployeeInterim(context.Context, BotID, RunID, string) (*BotEmployeeInterim, error)
}

type employeeInterimReleaseKey struct{}

// WithBotEmployeeInterimRelease is trusted server composition at the Employee's
// tool-boundary text release. Bind one exact runtime envelope, not the whole run;
// private Chat and synthetic runtime producers must never receive this context.
func WithBotEmployeeInterimRelease(ctx context.Context, envelope any) context.Context {
	return context.WithValue(ctx, employeeInterimReleaseKey{}, BotEmployeeInterimDigest(envelope))
}
func BotEmployeeInterimReleased(ctx context.Context, envelope any) bool {
	digest, _ := ctx.Value(employeeInterimReleaseKey{}).(string)
	return digest != "" && digest == BotEmployeeInterimDigest(envelope)
}
func BotEmployeeInterimDigest(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	// Canonicalize structs and decoded JSON maps identically across persistence.
	var canonical any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return ""
	}
	raw, err = json.Marshal(canonical)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
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
	EmployeeSpeech    bool          `json:"employee_speech,omitempty"`
	SourceSessionID   uuid.UUID     `json:"source_session_id,omitempty"`
	SourceUserEventID string        `json:"source_user_event_id,omitempty"`
	ReportDigest      string        `json:"report_digest,omitempty"`
	Artifacts         []BotArtifact `json:"artifacts,omitempty"`
	// Suppressed is retained for historical receipts, never file authorization.
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
	// Delivered requires positive transport evidence.
	Delivered bool
	Receipt   string
	Now       time.Time
}
