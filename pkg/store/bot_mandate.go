package store

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"time"
)

// Aliases ensure storage and execution use one versioned wire contract.
type BotMandate = execution.BotMandate
type BotMandateRef = execution.BotMandateRef
type BotMandatePurpose = execution.BotMandatePurpose
type BotMandateLimits = execution.BotMandateLimits

// BotMandateSource is protected adapter provenance, not request/model content.
type BotMandateSource string

const (
	BotMandateSourceAuthenticatedUser BotMandateSource = "authenticated_user"
	BotMandateSourceOperator          BotMandateSource = "operator"
)

type BotInitiationKind string

const (
	BotInitiationKindDirectOwnerMessage BotInitiationKind = "direct_owner_message"
	BotInitiationKindTitle              BotInitiationKind = "title"
)

type BotMandateProvenance struct {
	InitiationKind BotInitiationKind `json:"initiation_kind,omitempty"`
	Source         BotMandateSource  `json:"source"`
	ActorID        string            `json:"actor_id"`
	RequestID      string            `json:"request_id"`
	RecordedAt     time.Time         `json:"recorded_at"`
	UserEventID    string            `json:"user_event_id,omitempty"`
	SourceDigest   string            `json:"source_digest,omitempty"`
	RunID          string            `json:"run_id,omitempty"`
}

func (p BotMandateProvenance) Validate() error {
	if (p.Source != BotMandateSourceAuthenticatedUser && p.Source != BotMandateSourceOperator) || p.ActorID == "" || p.RequestID == "" || p.RecordedAt.IsZero() {
		return ErrBotInvalid
	}
	return nil
}

type BotMandateRecord struct {
	ScheduleCommand *BotOwnerScheduleCommand `json:"schedule_command,omitempty"`
	Mandate         BotMandate               `json:"mandate"`
	Provenance      BotMandateProvenance     `json:"provenance"`
	RevokedAt       *time.Time               `json:"revoked_at,omitempty"`
}

func (r BotMandateRecord) Clone() BotMandateRecord {
	r.Mandate = r.Mandate.Clone()
	if r.ScheduleCommand != nil {
		c := *r.ScheduleCommand
		c.Operations = append([]BotControlOperation(nil), c.Operations...)
		r.ScheduleCommand = &c
	}
	if r.RevokedAt != nil {
		v := *r.RevokedAt
		r.RevokedAt = &v
	}
	return r
}
func (r BotMandateRecord) Validate(now time.Time) error {
	if err := r.Mandate.Validate(now); err != nil {
		return err
	}
	if err := r.Provenance.Validate(); err != nil {
		return err
	}
	if r.RevokedAt != nil {
		return ErrBotForbidden
	}
	return nil
}

// BotMandateStore is tenant/owner scoped by the injected implementation. Create
// is append-only; replacement requires a new revision. No implementation or
// runtime registration is enabled by these contracts.
type BotMandateStore interface {
	CreateBotMandate(context.Context, BotMandateRecord) error
	ReadBotMandate(context.Context, BotMandateRef) (*BotMandateRecord, error)
	RevokeBotMandate(context.Context, BotMandateRef) error
}
