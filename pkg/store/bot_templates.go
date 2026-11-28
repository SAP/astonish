package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// BotTemplateDefinition intentionally cannot contain home, history, credentials,
// enabled triggers, grants or private memory. Publishing still requires review
// and redaction of free text; a type alone cannot sanitize prose.
type BotTemplateDefinition struct {
	SchemaVersion      int                     `json:"schema_version"`
	Name               string                  `json:"name"`
	Charter            string                  `json:"charter"`
	Language           string                  `json:"language"`
	SuggestedGoals     []BotGoalDefinition     `json:"suggested_goals"`
	SuggestedSchedules []BotScheduleSuggestion `json:"suggested_schedules"`
}
type BotGoalDefinition struct {
	Title              string `json:"title"`
	Description        string `json:"description"`
	CompletionCriteria string `json:"completion_criteria"`
}
type BotScheduleSuggestion struct {
	// SkillSeed is optional. Its expectation is explicitly adopted by the
	// destination owner on instantiation; no source IDs or authority are copied.
	SkillSeed    *BotSkillSeed `json:"skill_seed,omitempty"`
	Cron         string        `json:"cron"`
	Timezone     string        `json:"timezone"`
	Instructions string        `json:"instructions"`
}

// BotSkillSeed is a reviewed, prose-only initial method, not a copied skill
// revision. OwnerExpectation is explicit owner adoption, never inferred from the
// method or legacy instructions. Instantiation creates a local goal source for it.
// History, observed evidence, IDs, credentials and authority have no fields here.
type BotSkillSeed struct {
	Title            string   `json:"title"`
	Responsibility   string   `json:"responsibility"`
	OwnerExpectation string   `json:"owner_expectation"`
	Method           []string `json:"method"`
}

type BotTemplate struct {
	ID          uuid.UUID             `json:"id"`
	Revision    int64                 `json:"revision"`
	Definition  BotTemplateDefinition `json:"definition"`
	PublishedBy uuid.UUID             `json:"published_by"`
	PublishedAt time.Time             `json:"published_at"`
}
type BotTemplateForkRequest struct {
	TemplateID       uuid.UUID `json:"template_id"`
	TemplateRevision int64     `json:"template_revision"`
	Name             string    `json:"name"`
	IdempotencyKey   string    `json:"idempotency_key"`
}

// Publication is explicit team-admin-authorized copying, not scope migration.
// Fork always returns a conservative draft requiring destination-owner review.
type BotTemplateStore interface {
	List(context.Context, BotPage) ([]BotTemplate, error)
	Get(context.Context, uuid.UUID) (*BotTemplate, error)
	Publish(context.Context, BotTemplateDefinition, int64) (*BotTemplate, error)
	// ForkToPrivate validates both stores' scopes before copying reviewed content.
	ForkToPrivate(context.Context, BotStore, BotTemplateForkRequest) (*Bot, error)
}
