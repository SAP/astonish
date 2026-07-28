package agent

import (
	"context"
	"fmt"

	mem "github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

// MemoryMerger provides cross-session scenario-card upserts for platform memory.
type MemoryMerger struct {
	LLM       model.LLM
	DebugMode bool
}

// MergeResult describes the outcome of a scenario-card upsert.
type MergeResult struct {
	// Action is one of: "created", "merged", or "discarded".
	Action string

	// ExistingID is the ID of the entry that was updated (only set when Action="merged").
	ExistingID string
}

// SaveOrMerge saves platform memory as a structured scenario card. It fails
// closed if the card cannot be created or merged; falling back to raw memory
// would reintroduce scattered notes and weaken the scenario-card model.
//
// Returns the merge result indicating what action was taken.
func (mm *MemoryMerger) SaveOrMerge(ctx context.Context, memStore store.MemoryStore, entry store.MemoryEntry) (MergeResult, error) {
	return mm.SaveOrMergeWithStatus(ctx, memStore, entry, "")
}

func (mm *MemoryMerger) SaveOrMergeWithStatus(ctx context.Context, memStore store.MemoryStore, entry store.MemoryEntry, status string) (MergeResult, error) {
	if memStore == nil {
		return MergeResult{}, fmt.Errorf("memory store is nil")
	}
	var card mem.ScenarioCard
	if parsed, ok := mem.ParseScenarioCard(entry.Content); ok {
		card = parsed
		if card.Scope == "" {
			card.Scope = "team"
		}
	} else {
		card = mem.DraftScenarioCardFromMemoryEntry("team", entry)
	}
	if status != "" {
		card.Status = status
		if status == mem.ScenarioCardStatusVerified && card.Confidence < 0.8 {
			card.Confidence = 0.8
		}
	}
	if !mem.HasUsableScenarioRecipe(card) {
		return MergeResult{Action: "discarded"}, nil
	}
	result, err := mem.UpsertScenarioCard(ctx, memStore, card)
	if err != nil {
		return MergeResult{}, fmt.Errorf("failed to upsert scenario card memory: %w", err)
	}
	return MergeResult{Action: result.Action, ExistingID: result.ExistingID}, nil
}
