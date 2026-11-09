package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// BotDeadlineSchedule marks a persisted single due occurrence, not a recurring cron.
const BotDeadlineSchedule = "@bot-goal-deadline"

// BotTriggerAction is a reviewed configuration operation, never a model tool.
type BotTriggerAction string

const (
	BotTriggerSave    BotTriggerAction = "save"
	BotTriggerEnable  BotTriggerAction = "enable"
	BotTriggerDisable BotTriggerAction = "disable"
	BotTriggerRemove  BotTriggerAction = "remove"
	BotTriggerDryRun  BotTriggerAction = "dry-run"
)

type BotTriggerChange struct {
	BotID            BotID
	TriggerID        uuid.UUID
	ExpectedRevision int64
	Action           BotTriggerAction
	Reason           string
	Definition       TriggerSpec
	Proof            *BotExternalEvent
}

// BotExternalEvent contains evidence, not executable instructions or ownership.
// Signature covers the target, immutable version, purpose, timestamp, unique
// event ID and exact body. Replays are rejected durably by run idempotency.
type BotExternalEvent struct {
	BotID     BotID              `json:"bot_id"`
	TriggerID uuid.UUID          `json:"trigger_id"`
	Version   int64              `json:"version"`
	EventID   string             `json:"event_id"`
	Timestamp int64              `json:"timestamp"`
	Signature string             `json:"signature"`
	Payload   []byte             `json:"payload,omitempty"`
	Binding   *BotChannelBinding `json:"binding,omitempty"`
}

const BotTriggerSignatureWindow = 5 * time.Minute

func BotTriggerSignature(secret string, event BotExternalEvent, purpose string) string {
	h := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(h, "bot-trigger-v1\n%s\n%s\n%d\n%s\n%d\n%s\n%x", event.BotID, event.TriggerID, event.Version, purpose, event.Timestamp, event.EventID, sha256.Sum256(event.Payload))
	return hex.EncodeToString(h.Sum(nil))
}

// BotOwnedSchedule detects both the explicit mode and a persisted Bot binding.
func BotOwnedSchedule(j *ScheduledJob) bool {
	return j != nil && (j.Mode == "bot_trigger" || j.Payload.BotID != "" || j.Payload.BotTriggerID != "")
}

// BotTriggerStore is optional so unsupported backends fail closed.
type BotTriggerStore interface {
	ChangeTrigger(context.Context, BotTriggerChange) (*Bot, error)
	AdmitExternalRun(context.Context, BotExternalEvent) (*BotRun, error)
}
