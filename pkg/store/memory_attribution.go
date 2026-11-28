package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MemoryKnowledgeStatus describes knowledge, never authority or permission.
type MemoryKnowledgeStatus string

const (
	MemoryKnowledgeInferred MemoryKnowledgeStatus = "inferred"
	MemoryKnowledgeVerified MemoryKnowledgeStatus = "verified"
)

// MemoryAttribution is optional provenance. IDs identify origin, not access grants.
// Verified requires explicit observation/owner evidence; retrieval never promotes
// an inference. SupersededBy references the durable correction that replaced this
// observation (the existing correction path replaces content in-place).
type MemoryAttribution struct {
	BotID        BotID                 `json:"bot_id"`
	GoalID       uuid.UUID             `json:"goal_id,omitempty"`
	RunID        RunID                 `json:"run_id,omitempty"`
	Source       string                `json:"source"`
	Evidence     *BotEvidence          `json:"evidence,omitempty"`
	ObservedAt   time.Time             `json:"observed_at"`
	Status       MemoryKnowledgeStatus `json:"status"`
	SupersededBy string                `json:"superseded_by,omitempty"`
}

func (a MemoryAttribution) Validate() error {
	if a.BotID == uuid.Nil || strings.TrimSpace(a.Source) == "" || a.ObservedAt.IsZero() {
		return fmt.Errorf("%w: incomplete memory attribution", ErrBotInvalid)
	}
	switch a.Status {
	case MemoryKnowledgeInferred:
	case MemoryKnowledgeVerified:
		if a.Evidence == nil {
			return fmt.Errorf("%w: verified memory requires evidence", ErrBotInvalid)
		}
		if err := a.Evidence.ValidateCompletion(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unknown memory knowledge status", ErrBotInvalid)
	}
	return nil
}

// MemoryAttributionFilter selects explicitly attributed knowledge only. Zero
// GoalID/RunID means any goal/run of BotID, never any Bot. Legacy rows are not
// implicitly attributed. Superseded observations are excluded by default.
type MemoryAttributionFilter struct {
	BotID             BotID
	GoalID            uuid.UUID
	RunID             RunID
	IncludeSuperseded bool
}

// AttributedMemoryStore is optional, leaving MemoryStore implementers compatible.
// SessionIDs must come from an authorized scoped Bot repository. Implementations
// intersect them with validated Bot/goal/run provenance BEFORE ranking/top-K.
// Empty sessionIDs returns no rows. This capability itself grants no authority.
type AttributedMemoryStore interface {
	SearchAttributed(context.Context, []string, MemoryAttributionFilter, string, int, float64, string) ([]MemorySearchResult, error)
}

// SupersedeMemoryAttribution records correction provenance while retaining the
// original observation/evidence and confidence; it never upgrades an inference.
func SupersedeMemoryAttribution(metadata map[string]any, source string) (map[string]any, error) {
	a, err := DecodeMemoryAttribution(metadata)
	if err != nil || a == nil {
		return metadata, err
	}
	if strings.TrimSpace(source) == "" {
		return nil, ErrBotInvalid
	}
	a.SupersededBy = source
	clean := make(map[string]any, len(metadata))
	for k, v := range metadata {
		if k != memoryAttributionMetadataKey {
			clean[k] = v
		}
	}
	return EncodeMemoryAttribution(clean, a)
}

const memoryAttributionMetadataKey = "astonish_memory_attribution_v1"

// EncodeMemoryAttribution preserves unrelated metadata and never mutates the
// caller's map. Raw callers cannot smuggle a competing attribution envelope.
func EncodeMemoryAttribution(metadata map[string]any, a *MemoryAttribution) (map[string]any, error) {
	if _, exists := metadata[memoryAttributionMetadataKey]; exists {
		return nil, fmt.Errorf("%w: reserved memory attribution metadata", ErrBotInvalid)
	}
	if a == nil {
		return metadata, nil
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	result := make(map[string]any, len(metadata)+1)
	for k, v := range metadata {
		result[k] = v
	}
	result[memoryAttributionMetadataKey] = string(raw)
	return result, nil
}

// DecodeMemoryAttribution distinguishes legacy absence from corrupt provenance.
func DecodeMemoryAttribution(metadata map[string]any) (*MemoryAttribution, error) {
	value, exists := metadata[memoryAttributionMetadataKey]
	if !exists {
		return nil, nil
	}
	raw, ok := value.(string)
	if !ok {
		return nil, ErrBotInvalid
	}
	var a MemoryAttribution
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("%w: malformed memory attribution", ErrBotInvalid)
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return &a, nil
}
