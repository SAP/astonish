package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

// BotIntakeMode selects content to review; it grants no creation or execution authority.
type BotIntakeMode string

const (
	BotIntakeBlank    BotIntakeMode = "blank"
	BotIntakeChat     BotIntakeMode = "chat"
	BotIntakeTemplate BotIntakeMode = "template"
	BotIntakeAdaptive BotIntakeMode = "adaptive"
	BotIntakeMaxBytes               = 64 << 10
)

// BotUnfinishedContent contains only user-entered prose and inert source selections.
// Empty fields are valid. Sources must be authorized again at reviewed commit.
// Free text is not automatically secret-redacted; clients must never import raw
// transcripts, credentials, session metadata or runtime bindings into these fields.
type BotUnfinishedContent struct {
	Mode             BotIntakeMode `json:"mode"`
	Name             string        `json:"name"`
	Charter          string        `json:"charter"`
	Language         string        `json:"language"`
	Timezone         string        `json:"timezone"`
	Identity         string        `json:"identity"`
	WorkingStyle     string        `json:"working_style"`
	OwnerPreferences string        `json:"owner_preferences"`
	SourceID         *uuid.UUID    `json:"source_id,omitempty"`
	TemplateRevision int64         `json:"template_revision,omitempty"`
	Carryover        BotCarryover  `json:"carryover"`
}

type BotUnfinishedPut struct {
	ExpectedRevision int64                `json:"expected_revision"`
	IdempotencyKey   string               `json:"idempotency_key"`
	Content          BotUnfinishedContent `json:"content"`
}

type BotUnfinishedDelete struct {
	ExpectedRevision int64  `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}

// BotUnfinishedIntake is NOT a Bot root and cannot be passed to BotStore creation
// or runtime methods. This pure aggregate still requires a scoped durable adapter.
// OwnerID/Scope come only from authenticated, live-membership-checked server context.
// LastMutationHash is persistence-only retry evidence, not a client field.
type BotUnfinishedIntake struct {
	ID               uuid.UUID             `json:"id"`
	OwnerID          uuid.UUID             `json:"owner_id"`
	Scope            BotScope              `json:"scope"`
	Revision         int64                 `json:"revision"`
	Content          *BotUnfinishedContent `json:"content,omitempty"`
	UpdatedAt        time.Time             `json:"updated_at"`
	Discarded        bool                  `json:"-"`
	LastMutationHash string                `json:"-"`
}

func intakeMutationHash(operation string, request any) (string, error) {
	data, err := json.Marshal(struct {
		Operation string
		Request   any
	}{operation, request})
	if err != nil {
		return "", ErrBotInvalid
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

func validateUnfinishedPut(q BotUnfinishedPut) error {
	if q.ExpectedRevision < 0 || q.ExpectedRevision == math.MaxInt64 || strings.TrimSpace(q.IdempotencyKey) == "" || len(q.IdempotencyKey) > 200 {
		return ErrBotInvalid
	}
	switch q.Content.Mode {
	case BotIntakeBlank, BotIntakeChat, BotIntakeTemplate, BotIntakeAdaptive:
	default:
		return ErrBotInvalid
	}
	if q.Content.SourceID != nil && *q.Content.SourceID == uuid.Nil || q.Content.TemplateRevision < 0 {
		return ErrBotInvalid
	}
	if len(q.Content.Name) > 200 || len(q.Content.Carryover.Memories) > 100 || len(q.Content.Carryover.Goals) > 100 || len(q.Content.Carryover.Artifacts) > 100 {
		return ErrBotInvalid
	}
	data, err := json.Marshal(q)
	if err != nil || len(data) > BotIntakeMaxBytes {
		return ErrBotInvalid
	}
	return nil
}

func cloneUnfinishedContent(c BotUnfinishedContent) *BotUnfinishedContent {
	if c.SourceID != nil {
		id := *c.SourceID
		c.SourceID = &id
	}
	c.Carryover.Memories = append([]BotCarryoverMemory(nil), c.Carryover.Memories...)
	c.Carryover.Goals = append([]BotCarryoverGoal(nil), c.Carryover.Goals...)
	c.Carryover.Artifacts = append([]BotCarryoverArtifact(nil), c.Carryover.Artifacts...)
	return &c
}

// NewBotUnfinishedIntake constructs revision 1. Persistence MUST use a unique
// insert (never an upsert); on conflict load the owned row and call Replace.
func NewBotUnfinishedIntake(id, owner uuid.UUID, scope BotScope, q BotUnfinishedPut, now time.Time) (*BotUnfinishedIntake, error) {
	if id == uuid.Nil || owner == uuid.Nil || scope != BotScopePersonal && scope != BotScopeTeam {
		return nil, ErrBotInvalid
	}
	if err := validateUnfinishedPut(q); err != nil {
		return nil, err
	}
	if q.ExpectedRevision != 0 {
		return nil, ErrBotConflict
	}
	hash, err := intakeMutationHash("put", q)
	if err != nil {
		return nil, err
	}
	return &BotUnfinishedIntake{ID: id, OwnerID: owner, Scope: scope, Revision: 1, Content: cloneUnfinishedContent(q.Content), UpdatedAt: now.UTC(), LastMutationHash: hash}, nil
}

// Authorize requires exact ownership even in team scope: team admins may not read
// another user's unreviewed private content. This does not replace live membership
// validation or structurally scoped tenant routing in the durable adapter.
func (b BotUnfinishedIntake) Authorize(owner uuid.UUID, scope BotScope) error {
	if owner == uuid.Nil || b.OwnerID != owner || b.Scope != scope {
		return ErrBotForbidden
	}
	return nil
}

// Replace is an immutable state transition, NOT a database write. The adapter must
// atomically compare the persisted revision to b.Revision before committing it.
// Only the immediately preceding identical mutation may be replayed.
func (b BotUnfinishedIntake) Replace(owner uuid.UUID, scope BotScope, q BotUnfinishedPut, now time.Time) (*BotUnfinishedIntake, error) {
	if err := b.Authorize(owner, scope); err != nil {
		return nil, err
	}
	if err := validateUnfinishedPut(q); err != nil {
		return nil, err
	}
	if b.Discarded {
		return nil, ErrBotConflict
	}
	hash, err := intakeMutationHash("put", q)
	if err != nil {
		return nil, err
	}
	if b.LastMutationHash == hash && b.Revision == q.ExpectedRevision+1 {
		if b.Content != nil {
			b.Content = cloneUnfinishedContent(*b.Content)
		}
		return &b, nil
	}
	if b.Revision != q.ExpectedRevision {
		return nil, ErrBotConflict
	}
	b.Revision++
	b.Content = cloneUnfinishedContent(q.Content)
	b.UpdatedAt = now.UTC()
	b.LastMutationHash = hash
	return &b, nil
}

// Discard produces a payload-free tombstone to prevent late autosaves from
// resurrecting content. Start a new intake ID after discard; do not recycle IDs.
func (b BotUnfinishedIntake) Discard(owner uuid.UUID, scope BotScope, q BotUnfinishedDelete, now time.Time) (*BotUnfinishedIntake, error) {
	if err := b.Authorize(owner, scope); err != nil {
		return nil, err
	}
	if q.ExpectedRevision < 1 || q.ExpectedRevision == math.MaxInt64 || strings.TrimSpace(q.IdempotencyKey) == "" || len(q.IdempotencyKey) > 200 {
		return nil, ErrBotInvalid
	}
	hash, err := intakeMutationHash("delete", q)
	if err != nil {
		return nil, err
	}
	if b.Discarded && b.LastMutationHash == hash && b.Revision == q.ExpectedRevision+1 {
		return &b, nil
	}
	if b.Discarded || b.Revision != q.ExpectedRevision {
		return nil, ErrBotConflict
	}
	b.Revision++
	b.Content = nil
	b.Discarded = true
	b.LastMutationHash = hash
	b.UpdatedAt = now.UTC()
	return &b, nil
}
