package store

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// BotConfigurationRequest is an explicit reviewed policy replacement, not a prose
// edit or activation. The version and root revision must both still match.
type BotConfigurationRequest struct {
	BotID            BotID          `json:"-"`
	ExpectedRevision int64          `json:"expected_revision"`
	ExpectedVersion  int64          `json:"expected_version"`
	Reviewed         bool           `json:"reviewed"`
	Reason           string         `json:"reason"`
	Autonomy         BotAutonomy    `json:"autonomy"`
	Budget           BudgetSpec     `json:"budget"`
	Delivery         DeliveryPolicy `json:"delivery"`
}
type BotConfigurationStore interface {
	ConfigureReviewed(context.Context, BotConfigurationRequest) (*Bot, error)
}

// Carryover is a user-selected, reviewed/redacted copy, never a transcript,
// credential reference, sandbox path or session metadata import.
type BotCarryoverMemory struct {
	Content  string `json:"content"`
	Category string `json:"category"`
}
type BotCarryoverGoal struct {
	Title              string `json:"title"`
	CompletionCriteria string `json:"completion_criteria"`
}

// Artifacts are reviewed text snapshots, not filesystem paths or live bindings.
type BotCarryoverArtifact struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}
type BotCarryover struct {
	Summary   string                 `json:"summary"`
	Memories  []BotCarryoverMemory   `json:"memories"`
	Goals     []BotCarryoverGoal     `json:"goals"`
	Artifacts []BotCarryoverArtifact `json:"artifacts"`
}
type BotPromotionRequest struct {
	SourceSessionID uuid.UUID    `json:"-"`
	Name            string       `json:"name"`
	IdempotencyKey  string       `json:"idempotency_key"`
	Charter         string       `json:"charter"`
	Reviewed        bool         `json:"reviewed"`
	ReviewedForTeam bool         `json:"reviewed_for_team"`
	Carryover       BotCarryover `json:"carryover"`
}
type BotConversionRequest struct {
	SkillSeed         *BotSkillSeed `json:"skill_seed,omitempty"`
	SourceJobID       uuid.UUID     `json:"-"`
	ExpectedUpdatedAt time.Time     `json:"expected_updated_at"`
	Name              string        `json:"name"`
	IdempotencyKey    string        `json:"idempotency_key"`
	Charter           string        `json:"charter"`
	Reviewed          bool          `json:"reviewed"`
	ReviewedForTeam   bool          `json:"reviewed_for_team"`
}
type BotAdaptiveReview struct {
	SourceJobID  uuid.UUID   `json:"source_job_id"`
	UpdatedAt    time.Time   `json:"updated_at"`
	Schedule     JobSchedule `json:"schedule"`
	Instructions string      `json:"instructions"`
}
type BotIntakeStore interface {
	ReviewAdaptiveJob(context.Context, uuid.UUID) (*BotAdaptiveReview, error)
	PromoteChat(context.Context, BotPromotionRequest) (*Bot, error)
	ConvertAdaptiveJob(context.Context, BotConversionRequest) (*Bot, error)
}
