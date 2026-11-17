package store

import (
	"context"
	"encoding/hex"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
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
	BotInitiationKindTitleConfirmation  BotInitiationKind = "title_confirmation"
)

type BotTitleConfirmationProvenance struct {
	BotID            string `json:"bot_id"`
	RunID            string `json:"run_id"`
	OwnerID          string `json:"owner_id"`
	UserEventID      string `json:"user_event_id"`
	ApprovalID       string `json:"approval_id"`
	QuestionID       string `json:"question_id"`
	AssignmentDigest string `json:"assignment_digest"`
}

type BotMandateProvenance struct {
	TitleConfirmation *BotTitleConfirmationProvenance `json:"title_confirmation,omitempty"`
	InitiationKind    BotInitiationKind               `json:"initiation_kind,omitempty"`
	Source            BotMandateSource                `json:"source"`
	ActorID           string                          `json:"actor_id"`
	RequestID         string                          `json:"request_id"`
	RecordedAt        time.Time                       `json:"recorded_at"`
	UserEventID       string                          `json:"user_event_id,omitempty"`
	SourceDigest      string                          `json:"source_digest,omitempty"`
	RunID             string                          `json:"run_id,omitempty"`
}

func (p BotMandateProvenance) Validate() error {
	if p.InitiationKind == BotInitiationKindTitleConfirmation {
		c := p.TitleConfirmation
		if c == nil || p.Source != BotMandateSourceAuthenticatedUser || c.OwnerID != p.ActorID || c.RunID != p.RunID || c.UserEventID == "" || c.UserEventID != p.UserEventID || c.QuestionID == "" || c.AssignmentDigest != p.SourceDigest {
			return ErrBotInvalid
		}
		for _, id := range []string{c.BotID, c.RunID, c.OwnerID, c.ApprovalID} {
			v, err := uuid.Parse(id)
			if err != nil || v == uuid.Nil {
				return ErrBotInvalid
			}
		}
		if digest, err := hex.DecodeString(c.AssignmentDigest); err != nil || len(digest) != 32 {
			return ErrBotInvalid
		}
	} else if p.TitleConfirmation != nil {
		return ErrBotInvalid
	}
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
	if r.Provenance.TitleConfirmation != nil {
		c := *r.Provenance.TitleConfirmation
		r.Provenance.TitleConfirmation = &c
	}
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
	if c := r.Provenance.TitleConfirmation; c != nil && (c.BotID != r.Mandate.Ref.BotID.String() || c.OwnerID != r.Mandate.Ref.OwnerID) {
		return ErrBotInvalid
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
