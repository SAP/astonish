package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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
	targetScope := ""
	if entry.Metadata != nil {
		targetScope, _ = entry.Metadata["scope"].(string)
		targetScope = strings.TrimSpace(targetScope)
	}
	if targetScope == "" {
		targetScope = strings.TrimSpace(string(store.MemoryScopeFromContext(ctx)))
	}
	if targetScope == "" {
		targetScope = string(store.MemoryScopeTeam)
	}
	var card mem.ScenarioCard
	if parsed, ok := mem.ParseScenarioCard(entry.Content); ok {
		card = parsed
		card.Scope = targetScope
	} else {
		card = mem.DraftScenarioCardFromMemoryEntry(targetScope, entry)
	}
	if status != "" {
		card.Status = status
		if status == mem.ScenarioCardStatusVerified && card.Confidence < 0.8 {
			card.Confidence = 0.8
		}
	}
	if !mem.HasUsableScenarioRecipe(card) {
		// Fallback: if content was extracted but all classified as cautions,
		// promote non-ephemeral cautions to recipe so the card isn't silently
		// dropped. Operational knowledge like "do not use X, use Y" may end up
		// here. Genuinely ephemeral cautions (timeout, outage, 503) stay out.
		if len(card.CautionsOrConditionalFailures) > 0 {
			var promoted []string
			var kept []string
			for _, c := range card.CautionsOrConditionalFailures {
				if mem.IsEphemeralCaution(c) {
					kept = append(kept, c)
				} else {
					promoted = append(promoted, c)
				}
			}
			if len(promoted) > 0 {
				card.RecommendedRecipe = append(card.RecommendedRecipe, promoted...)
				card.CautionsOrConditionalFailures = kept
			}
		}
		if !mem.HasUsableScenarioRecipe(card) {
			slog.Info("platform reflector: discarded card with no usable recipe",
				"component", "platform-reflector",
				"category", entry.Category,
				"contentSnippet", truncateForMergeLog(entry.Content, 120))
			return MergeResult{Action: "discarded"}, nil
		}
	}
	result, err := mem.UpsertScenarioCard(ctx, memStore, card)
	if err != nil {
		return MergeResult{}, fmt.Errorf("failed to upsert scenario card memory: %w", err)
	}
	return MergeResult{Action: result.Action, ExistingID: result.ExistingID}, nil
}

// truncateForMergeLog shortens a string for log output, replacing newlines.
func truncateForMergeLog(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", "; ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
