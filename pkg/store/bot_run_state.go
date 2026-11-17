package store

import (
	"github.com/google/uuid"
	"strings"
	"time"
)

const BotRunStateSchemaVersion = 1
const BotRunStateKey = "bot_run_state"

// BotProgressProjection is employee-facing status only. It deliberately excludes
// context references, dispatch ledger, tool arguments, usage and private notes.
// No progress phase denotes terminal completion; RunStatus remains authoritative.
type BotProgressProjection struct {
	CoalescedOccurrences int64             `json:"coalesced_occurrences,omitempty"`
	ActiveDurationNanos  int64             `json:"active_duration_nanos"`
	ActiveSince          *time.Time        `json:"active_since,omitempty"`
	NextEligibleAt       *time.Time        `json:"next_eligible_at,omitempty"`
	SchemaVersion        int               `json:"schema_version"`
	BotID                BotID             `json:"bot_id"`
	RunID                RunID             `json:"run_id"`
	SessionID            uuid.UUID         `json:"session_id"`
	Revision             int64             `json:"revision"`
	Phase                BotProgressPhase  `json:"phase"`
	Reason               BotProgressReason `json:"reason,omitempty"`
	Summary              string            `json:"summary,omitempty"`
	EvaluatedAt          *time.Time        `json:"evaluated_at,omitempty"`
}

func (p *BotProgressProjection) Validate() error {
	if p == nil {
		return nil
	}
	if p.SchemaVersion != BotProgressSchemaVersion {
		return ErrBotUnsupported
	}
	if p.CoalescedOccurrences < 0 || p.BotID == uuid.Nil || p.RunID == uuid.Nil || p.SessionID == uuid.Nil || p.Revision < 1 || !p.Phase.Valid() || p.Reason != "" && !p.Reason.Valid() || len(p.Summary) > BotProgressMaxSummaryBytes || p.Summary != "" && strings.TrimSpace(p.Summary) == "" || p.EvaluatedAt != nil && p.EvaluatedAt.IsZero() {
		return ErrBotInvalid
	}
	return nil
}
func (p *BotProgressProjection) Clone() *BotProgressProjection {
	if p == nil {
		return nil
	}
	out := *p
	if p.EvaluatedAt != nil {
		at := *p.EvaluatedAt
		out.EvaluatedAt = &at
	}
	return &out
}

// ValidateProgress validates ONLY the additive projection and its exact binding.
// Legacy BotRunState shape and publication behavior are unchanged by this helper.
func (s BotRunState) ValidateProgress() error {
	if s.Progress == nil {
		return nil
	}
	if err := s.Progress.Validate(); err != nil {
		return err
	}
	if s.Progress.BotID != s.BotID || s.Progress.RunID != s.RunID || s.Progress.SessionID != s.SessionID {
		return ErrBotConflict
	}
	return nil
}

// BotRunState is durable presentation metadata, not model content or authority.
type BotRunState struct {
	CommittedAt   time.Time              `json:"committed_at,omitempty"`
	Progress      *BotProgressProjection `json:"progress,omitempty"`
	Work          *BotWorkProjection     `json:"work,omitempty"`
	SchemaVersion int                    `json:"schema_version"`
	BotID         uuid.UUID              `json:"bot_id"`
	RunID         uuid.UUID              `json:"run_id"`
	SessionID     uuid.UUID              `json:"session_id"`
	HomeSessionID uuid.UUID              `json:"home_session_id"`
	Presentation  BotRunPresentation     `json:"presentation"`
	Status        RunStatus              `json:"status"`
	StateSequence int64                  `json:"state_sequence"`
	Reason        BotContinuationReason  `json:"reason,omitempty"`
	UserEventID   string                 `json:"user_event_id,omitempty"`
}
