package entstore

import (
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

const botDiskRecordFormat = 2

// botVersionRecord is the SQL allowlist. Instruction text, delivery preferences
// and trigger assignments are never members of this persisted type.
type botVersionRecord struct {
	Format          int                       `json:"format"`
	BotID           store.BotID               `json:"bot_id"`
	Version         int64                     `json:"version"`
	SchemaVersion   int                       `json:"schema_version"`
	Autonomy        store.BotAutonomy         `json:"autonomy"`
	Budget          store.BudgetSpec          `json:"budget"`
	Notify          store.NotifyPolicy        `json:"notify"`
	Bindings        []store.BotChannelBinding `json:"bindings"`
	SandboxTemplate string                    `json:"sandbox_template"`
	Language        string                    `json:"language"`
	Timezone        string                    `json:"timezone"`
	CreatedBy       uuid.UUID                 `json:"created_by"`
	CreatedAt       time.Time                 `json:"created_at"`
	Triggers        []botTriggerRecord        `json:"triggers"`
	Content         store.BotContentRef       `json:"content"`
}

type botTriggerRecord struct {
	ID             uuid.UUID                `json:"id"`
	TaskID         *uuid.UUID               `json:"task_id,omitempty"`
	SkillID        *uuid.UUID               `json:"skill_id,omitempty"`
	Kind           store.TriggerKind        `json:"kind"`
	ScheduleID     *uuid.UUID               `json:"schedule_id,omitempty"`
	GoalID         *uuid.UUID               `json:"goal_id,omitempty"`
	Cron           string                   `json:"cron,omitempty"`
	Timezone       string                   `json:"timezone,omitempty"`
	Enabled        bool                     `json:"enabled"`
	HookStatus     store.HookStatus         `json:"hook_status,omitempty"`
	CredentialRef  string                   `json:"credential_ref,omitempty"`
	Fingerprint    string                   `json:"fingerprint,omitempty"`
	CatchUpOnce    bool                     `json:"catch_up_once"`
	Poll           *store.BotHookPoll       `json:"poll,omitempty"`
	HookWatermark  string                   `json:"hook_watermark,omitempty"`
	ChannelBinding *store.BotChannelBinding `json:"channel_binding,omitempty"`
}

// botVersionContent is disk-only, never handed to an Ent JSON setter.
type botVersionContent struct {
	Charter      string              `json:"charter"`
	Soul         string              `json:"soul"`
	Identity     string              `json:"identity"`
	User         string              `json:"user"`
	Avatar       string              `json:"avatar"`
	Preferences  string              `json:"preferences"`
	ChangeReason string              `json:"change_reason"`
	Triggers     []botTriggerContent `json:"triggers"`
}
type botTriggerContent struct {
	ID           uuid.UUID `json:"id"`
	Instructions string    `json:"instructions"`
	Invocation   string    `json:"invocation"`
}
