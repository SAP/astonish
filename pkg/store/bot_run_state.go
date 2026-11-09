package store

import "github.com/google/uuid"

const BotRunStateSchemaVersion = 1
const BotRunStateKey = "bot_run_state"

// BotRunState is durable presentation metadata, not model content or authority.
type BotRunState struct {
	SchemaVersion int                   `json:"schema_version"`
	BotID         uuid.UUID             `json:"bot_id"`
	RunID         uuid.UUID             `json:"run_id"`
	SessionID     uuid.UUID             `json:"session_id"`
	HomeSessionID uuid.UUID             `json:"home_session_id"`
	Presentation  BotRunPresentation    `json:"presentation"`
	Status        RunStatus             `json:"status"`
	StateSequence int64                 `json:"state_sequence"`
	Reason        BotContinuationReason `json:"reason,omitempty"`
	UserEventID   string                `json:"user_event_id,omitempty"`
}
