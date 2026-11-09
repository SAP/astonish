package store

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// BotSourceIntakeRequest is an explicit selected/redacted snapshot, never a transcript
// import. The server binds this selection to the live source revision and actor.
type BotSourceIntakeRequest struct {
	Mode             BotIntakeMode `json:"mode"`
	SourceID         uuid.UUID     `json:"source_id"`
	TemplateRevision int64         `json:"template_revision,omitempty"`
	Name             string        `json:"name"`
	Charter          string        `json:"charter"`
	Carryover        BotCarryover  `json:"carryover"`
	IdempotencyKey   string        `json:"idempotency_key"`
}
type BotSourceIntakeStatus string

const (
	BotSourceIntakeProposed BotSourceIntakeStatus = "proposed"
	BotSourceIntakeApplying BotSourceIntakeStatus = "applying"
	BotSourceIntakeApplied  BotSourceIntakeStatus = "applied"
	BotSourceIntakeRejected BotSourceIntakeStatus = "rejected"
	BotSourceIntakeStale    BotSourceIntakeStatus = "stale"
)

type BotSourceIntakeProposal struct {
	ID              uuid.UUID              `json:"id"`
	OwnerID         uuid.UUID              `json:"owner_id"`
	Scope           BotScope               `json:"scope"`
	Request         BotSourceIntakeRequest `json:"request"`
	SourceHash      string                 `json:"source_hash"`
	SourceUpdatedAt time.Time              `json:"source_updated_at"`
	Template        *BotTemplate           `json:"template,omitempty"`
	Adaptive        *BotAdaptiveReview     `json:"adaptive,omitempty"`
	Digest          string                 `json:"digest"`
	Status          BotSourceIntakeStatus  `json:"status"`
	BotID           *uuid.UUID             `json:"bot_id,omitempty"`
	CreatedAt       time.Time              `json:"created_at"`
	DecidedAt       *time.Time             `json:"decided_at,omitempty"`
}
type BotSourceIntakeDecision struct {
	Digest          string `json:"digest"`
	Decision        string `json:"decision"`
	ReviewedForTeam bool   `json:"reviewed_for_team"`
}

// Source intake records share owner-private intake storage, not the mutable
// unfinished-save contract. Only these methods may read/write the typed envelope.
// Template destination is server-selected, never supplied by the HTTP body.
type BotSourceIntakeStore interface {
	ProposeSourceIntake(context.Context, BotSourceIntakeRequest, BotTemplateStore) (*BotSourceIntakeProposal, error)
	GetSourceIntake(context.Context, uuid.UUID) (*BotSourceIntakeProposal, error)
	DecideSourceIntake(context.Context, uuid.UUID, BotSourceIntakeDecision, BotTemplateStore, BotStore) (*BotSourceIntakeProposal, error)
}
