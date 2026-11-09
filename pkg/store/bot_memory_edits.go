package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"strings"
)

// Reviewed operations accept data, never actor, authority, or evidence assignments.
type BotMemoryReviewKind string

const (
	BotMemoryReviewDocument    BotMemoryReviewKind = "document"
	BotMemoryReviewConsolidate BotMemoryReviewKind = "consolidate"
	BotActivityMemoryReview    BotActivityKind     = "memory_review"
)

type BotMemoryReviewSource struct {
	ID                  string `json:"id"`
	SessionID           string `json:"session_id"`
	ExpectedFingerprint string `json:"expected_fingerprint"`
}
type BotMemoryReviewEdit struct {
	SessionID string                  `json:"session_id"`
	Sources   []BotMemoryReviewSource `json:"sources"`
	Content   string                  `json:"content"`
	Category  string                  `json:"category"`
}
type BotMemoryReview struct {
	ID               uuid.UUID             `json:"id"`
	ExpectedRevision int64                 `json:"expected_revision"`
	Kind             BotMemoryReviewKind   `json:"kind"`
	Document         string                `json:"document,omitempty"`
	Reason           string                `json:"reason"`
	Confirmed        bool                  `json:"confirmed"`
	Edits            []BotMemoryReviewEdit `json:"edits"`
}
type BotMemoryReviewReceipt struct {
	ID                 uuid.UUID `json:"id"`
	BotID              BotID     `json:"bot_id"`
	Revision           int64     `json:"revision"`
	ActorID            uuid.UUID `json:"actor_id"`
	RequestFingerprint string    `json:"request_fingerprint"`
	ReplacementIDs     []string  `json:"replacement_ids"`
	// Sources are durable snapshots, including provenance, not just mutable references.
	Sources []MemorySearchResult `json:"sources"`
	Reason  string               `json:"reason"`
}
type BotMemoryReviewStore interface {
	ApplyReviewedBotMemory(context.Context, MemoryStore, BotID, BotMemoryReview) (*BotMemoryReviewReceipt, error)
}

// MemoryContentFingerprint excludes ranking and presentation fields but binds all
// editable record content and attribution. Memory has no numeric row revision.
func MemoryContentFingerprint(row MemorySearchResult) string {
	raw, _ := json.Marshal(struct {
		ID, Session, Content, Category, Path, Creator, CreatedAt string
		Attribution                                              *MemoryAttribution
	}{row.ID, row.SessionID, row.Snippet, row.Category, row.Path, row.CreatedBy, row.CreatedAt, row.Attribution})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (q BotMemoryReview) Fingerprint() string {
	raw, _ := json.Marshal(q)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (q BotMemoryReview) Validate() error {
	if !q.Confirmed {
		return ErrBotForbidden
	}
	if q.ID == uuid.Nil || q.ExpectedRevision < 1 || strings.TrimSpace(q.Reason) == "" || len(q.Edits) < 1 || len(q.Edits) > 100 {
		return ErrBotInvalid
	}
	if q.Kind != BotMemoryReviewDocument && q.Kind != BotMemoryReviewConsolidate {
		return ErrBotInvalid
	}
	if q.Kind == BotMemoryReviewConsolidate && q.Document != "" {
		return ErrBotInvalid
	}
	seen := map[string]bool{}
	for _, e := range q.Edits {
		sid, err := uuid.Parse(e.SessionID)
		if err != nil || sid == uuid.Nil || strings.TrimSpace(e.Content) == "" || len(e.Content) > 100000 || len(e.Sources) > 100 {
			return ErrBotInvalid
		}
		if q.Kind == BotMemoryReviewConsolidate && len(e.Sources) < 2 {
			return ErrBotInvalid
		}
		for _, s := range e.Sources {
			id, err := uuid.Parse(s.ID)
			if err != nil || id == uuid.Nil || seen[id.String()] {
				return ErrBotInvalid
			}
			// UUID parsing accepts aliases; identity, not spelling, must be unique.
			seen[id.String()] = true
			sid, err := uuid.Parse(s.SessionID)
			if err != nil || sid == uuid.Nil {
				return ErrBotInvalid
			}
			bytes, err := hex.DecodeString(s.ExpectedFingerprint)
			if err != nil || len(bytes) != 32 {
				return ErrBotInvalid
			}
		}
	}
	return nil
}
func (s *BotMemoryService) ApplyReviewed(ctx context.Context, q BotMemoryReview) (*BotMemoryReviewReceipt, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	_, allowed, err := s.sessions(ctx)
	if err != nil {
		return nil, err
	}
	for _, edit := range q.Edits {
		if !allowed[edit.SessionID] {
			return nil, ErrBotForbidden
		}
		for _, source := range edit.Sources {
			if !allowed[source.SessionID] {
				return nil, ErrBotForbidden
			}
		}
	}
	if _, err := s.authorizeWrite(ctx); err != nil {
		return nil, err
	}
	backend, ok := s.bots.(BotMemoryReviewStore)
	if !ok {
		return nil, ErrBotUnsupported
	}
	return backend.ApplyReviewedBotMemory(ctx, s.memory, s.id, q)
}
