package store

import (
	"context"
	"time"
)

// BotScheduledOccurrence contains identifiers only. The scoped store resolves the
// enabled schedule, version and instructions transactionally. An authenticated,
// live accountable owner is required; job payloads never establish authority.
type BotScheduledOccurrence struct {
	JobID          string
	BotID          BotID
	Version        int64
	TriggerID      RunID
	OccurrenceAt   time.Time
	IdempotencyKey string
}

type BotScheduledAdmission struct {
	// Acknowledged means the exact occurrence advanced in the admission transaction.
	Acknowledged bool
	Run          BotRun
	Duplicate    bool
}

// BotScheduledRunStore is an optional internal scheduler admission capability.
// HTTP RequestRun remains manual-only. Composition must supply an authenticated
// owner context and must not derive that principal from request/job hints.
type BotScheduledRunStore interface {
	AdmitScheduledRun(context.Context, BotScheduledOccurrence) (*BotScheduledAdmission, error)
}

// BotRunInput is durable input for one already admitted run. Instructions must
// come from the saved request, never from the worker's dispatch payload.
type BotRunInput struct {
	Bot     Bot
	Version BotVersion
	Run     BotRun
	Goals   []BotGoal
	// ExternalAdmissionValidated comes only from the persisted admission receipt.
	ExternalAdmissionValidated bool
	Instructions               string
	Attachments                []ChatFile
}

// BotRuntimeStore is an optional, owner-authenticated worker read capability.
// It does not admit, claim, resume, or authorize execution by itself. Implementors
// must recheck live scope membership and require the authenticated Bot owner.
// Service-principal delegation is intentionally not implied by this interface.
type BotRuntimeStore interface {
	LoadRunInput(context.Context, BotID, RunID) (*BotRunInput, error)
}
