package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mem "github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// memoryMergePrompt is the system instruction for the LLM call that merges
// an existing memory entry with proposed new content for the same topic.
const memoryMergePrompt = `You are a memory merge assistant. You receive an EXISTING memory entry and PROPOSED new content for the same topic.

Your job:
1. If the proposed content adds NO genuinely new information beyond what the existing entry already contains, respond with exactly: SKIP
2. If the proposed content contains at least one genuinely new fact not in the existing entry, output ONLY the merged result — a single, deduplicated set of concise bullet points that combines both inputs.

Rules:
- Never lose information from the existing entry
- Never duplicate facts (same URL, hostname, credential, endpoint, file path, pattern, or configuration value = same fact even if worded differently)
- Keep the merged output as concise bullet points using "- " prefix
- Never add information not present in either input
- Never include secret values (passwords, tokens, API keys)
- Output ONLY "SKIP" or the merged bullet points — no explanations, headers, or other text`

// MemoryMerger provides cross-session memory deduplication and merging.
// It searches existing team memories before saving new ones, and uses an
// LLM to intelligently merge overlapping content.
type MemoryMerger struct {
	LLM       model.LLM
	DebugMode bool
}

// effectiveLLM returns the per-request LLM from context if available,
// otherwise falls back to the merger's default LLM.
func (mm *MemoryMerger) effectiveLLM(ctx context.Context) model.LLM {
	if override := LLMFromContext(ctx); override != nil {
		return override
	}
	return mm.LLM
}

// MergeResult describes the outcome of a merge attempt.
type MergeResult struct {
	// Action is one of: "insert" (no existing match), "skip" (pure duplicate),
	// or "merged" (updated existing entry with combined content).
	Action string

	// ExistingID is the ID of the entry that was updated (only set when Action="merged").
	ExistingID string
}

// SaveOrMerge attempts to save a memory entry, first checking for existing
// entries with matching categories across all sessions. If a match is found,
// it uses an LLM call to merge the content rather than creating a duplicate.
//
// Returns the merge result indicating what action was taken.
func (mm *MemoryMerger) SaveOrMerge(ctx context.Context, memStore store.MemoryStore, entry store.MemoryEntry) (MergeResult, error) {
	if memStore == nil {
		return MergeResult{}, fmt.Errorf("memory store is nil")
	}
	if !mem.IsScenarioCard(store.MemorySearchResult{Snippet: entry.Content, Category: entry.Category}) {
		card := mem.DraftScenarioCardFromMemoryEntry("team", entry)
		result, err := mem.UpsertScenarioCard(ctx, memStore, card)
		if err == nil {
			return MergeResult{Action: result.Action, ExistingID: result.ExistingID}, nil
		}
		if mm.DebugMode {
			slog.Debug("scenario card upsert failed, falling back to legacy memory merge", "component", "memory-merger", "error", err)
		}
	}

	// Search for existing memories with a matching or related category.
	// We extract the topic keyword from the category (after the "/" if present)
	// and do a keyword search to find semantically related entries.
	existing := mm.findRelatedMemories(ctx, memStore, entry.Category)

	if len(existing) == 0 {
		// No related memory exists — straight insert
		if err := memStore.Add(ctx, entry); err != nil {
			return MergeResult{}, fmt.Errorf("failed to save memory: %w", err)
		}
		return MergeResult{Action: "insert"}, nil
	}

	// Found related entries — use LLM to decide: skip or merge
	// Pick the best match (highest score, or first with exact category match)
	target := mm.pickBestMatch(existing, entry.Category)
	if target == nil {
		// No sufficiently good match — insert as new
		if err := memStore.Add(ctx, entry); err != nil {
			return MergeResult{}, fmt.Errorf("failed to save memory: %w", err)
		}
		return MergeResult{Action: "insert"}, nil
	}

	// Call LLM to merge existing + proposed
	merged, err := mm.mergeViaLLM(ctx, target.Snippet, entry.Content)
	if err != nil {
		slog.Debug("memory merge LLM failed, falling back to insert",
			"component", "memory-merger",
			"error", err)
		// Fallback: insert as new (don't lose data)
		if err := memStore.Add(ctx, entry); err != nil {
			return MergeResult{}, fmt.Errorf("failed to save memory: %w", err)
		}
		return MergeResult{Action: "insert"}, nil
	}

	if merged == "" {
		// LLM said SKIP — pure duplicate
		slog.Debug("memory merge: skipped (pure duplicate)",
			"component", "memory-merger",
			"category", entry.Category,
			"existingID", target.ID)
		return MergeResult{Action: "skip", ExistingID: target.ID}, nil
	}

	// LLM produced merged content — update the existing entry in-place
	// Use the more specific/descriptive category between old and new
	mergedCategory := mm.pickBetterCategory(target.Category, entry.Category)
	if err := memStore.Update(ctx, target.ID, merged, mergedCategory); err != nil {
		slog.Warn("memory merge: failed to update existing entry, falling back to insert",
			"component", "memory-merger",
			"existingID", target.ID,
			"error", err)
		if err := memStore.Add(ctx, entry); err != nil {
			return MergeResult{}, fmt.Errorf("failed to save memory: %w", err)
		}
		return MergeResult{Action: "insert"}, nil
	}

	slog.Debug("memory merge: updated existing entry",
		"component", "memory-merger",
		"category", mergedCategory,
		"existingID", target.ID)
	return MergeResult{Action: "merged", ExistingID: target.ID}, nil
}

// findRelatedMemories searches the memory store for entries with categories
// related to the given category. Uses keyword search on the topic portion.
func (mm *MemoryMerger) findRelatedMemories(ctx context.Context, memStore store.MemoryStore, category string) []store.MemorySearchResult {
	// Extract the topic keyword from category (e.g., "infrastructure/Proxmox API" → "Proxmox")
	topic := extractTopicKeyword(category)
	if topic == "" {
		return nil
	}

	// Search by keyword to find related entries (regardless of session)
	results, err := memStore.Search(ctx, topic, 10, 0.0)
	if err != nil {
		slog.Debug("memory merge: search failed",
			"component", "memory-merger",
			"query", topic,
			"error", err)
		return nil
	}

	// Filter results to only those with related categories
	var related []store.MemorySearchResult
	topicLower := strings.ToLower(topic)
	for _, r := range results {
		catLower := strings.ToLower(r.Category)
		// Match if the category contains the topic keyword, or vice versa
		if strings.Contains(catLower, topicLower) || strings.Contains(topicLower, catLower) {
			related = append(related, r)
		}
	}

	if mm.DebugMode && len(related) > 0 {
		slog.Debug("memory merge: found related memories",
			"component", "memory-merger",
			"topic", topic,
			"count", len(related))
	}

	return related
}

// pickBestMatch selects the most appropriate existing entry to merge into.
// Prefers exact category match, then falls back to the first related entry.
func (mm *MemoryMerger) pickBestMatch(results []store.MemorySearchResult, category string) *store.MemorySearchResult {
	if len(results) == 0 {
		return nil
	}

	categoryLower := strings.ToLower(category)

	// First pass: exact category match
	for i := range results {
		if strings.ToLower(results[i].Category) == categoryLower {
			return &results[i]
		}
	}

	// Second pass: category contains the same topic (after the "/")
	_, topicPart := splitCategory(category)
	if topicPart != "" {
		topicLower := strings.ToLower(topicPart)
		for i := range results {
			_, existingTopic := splitCategory(results[i].Category)
			if strings.ToLower(existingTopic) == topicLower {
				return &results[i]
			}
			// Also match if topics share significant words
			if significantWordOverlap(topicLower, strings.ToLower(existingTopic)) {
				return &results[i]
			}
		}
	}

	// Third pass: first result with non-empty content (relaxed match)
	for i := range results {
		if results[i].Snippet != "" {
			return &results[i]
		}
	}

	return nil
}

// mergeViaLLM calls the LLM to merge existing and proposed content.
// Returns empty string if the LLM decides to skip (pure duplicate),
// or the merged content string on success.
func (mm *MemoryMerger) mergeViaLLM(ctx context.Context, existingContent, proposedContent string) (string, error) {
	userPrompt := fmt.Sprintf("EXISTING MEMORY:\n%s\n\nPROPOSED NEW CONTENT:\n%s", existingContent, proposedContent)

	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Parts: []*genai.Part{{Text: userPrompt}},
				Role:  "user",
			},
		},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: &genai.Content{
				Parts: []*genai.Part{{Text: memoryMergePrompt}},
			},
		},
	}

	mergeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var lastResp *model.LLMResponse
	for resp, err := range mm.effectiveLLM(ctx).GenerateContent(mergeCtx, req, false) {
		if err != nil {
			return "", fmt.Errorf("merge LLM error: %w", err)
		}
		lastResp = resp
	}

	if lastResp == nil || lastResp.Content == nil {
		return "", fmt.Errorf("merge LLM returned no content")
	}

	// Extract text response
	var sb strings.Builder
	for _, part := range lastResp.Content.Parts {
		if part.Text != "" {
			sb.WriteString(part.Text)
		}
	}

	response := strings.TrimSpace(sb.String())

	// Check if LLM said SKIP
	if strings.EqualFold(response, "SKIP") || strings.EqualFold(response, "skip") {
		return "", nil
	}

	// Validate the response looks like bullet points (basic sanity check)
	if response == "" {
		return "", fmt.Errorf("merge LLM returned empty response")
	}

	return response, nil
}

// pickBetterCategory returns the more descriptive of two categories.
// Prefers longer, more specific categories. If they have the same kind prefix,
// keeps the one with the longer topic part.
func (mm *MemoryMerger) pickBetterCategory(existing, proposed string) string {
	existingKind, existingTopic := splitCategory(existing)
	proposedKind, proposedTopic := splitCategory(proposed)

	// If same kind, pick the longer/more descriptive topic
	if existingKind == proposedKind {
		if len(proposedTopic) > len(existingTopic) {
			return proposed
		}
		return existing
	}

	// If proposed has a kind prefix and existing doesn't, prefer proposed
	if proposedKind != "" && existingKind == "" {
		return proposed
	}

	// Default: keep existing category (stability)
	return existing
}

// --- Helpers ---

// extractTopicKeyword extracts the most distinctive keyword from a category
// for use in memory search. For "infrastructure/Proxmox API" returns "Proxmox".
func extractTopicKeyword(category string) string {
	_, topic := splitCategory(category)
	if topic == "" {
		topic = category
	}

	// Take the first significant word (skip very generic words)
	words := strings.Fields(topic)
	genericWords := map[string]bool{
		"api": true, "server": true, "config": true, "configuration": true,
		"setup": true, "patterns": true, "details": true, "info": true,
		"connection": true, "the": true, "a": true, "an": true,
	}

	for _, w := range words {
		if !genericWords[strings.ToLower(w)] && len(w) > 2 {
			return w
		}
	}

	// Fallback: return the full topic
	if len(words) > 0 {
		return words[0]
	}
	return topic
}

// splitCategory splits "kind/topic" into (kind, topic).
// If no "/" is present, returns ("", fullCategory).
func splitCategory(category string) (string, string) {
	if idx := strings.Index(category, "/"); idx >= 0 {
		return category[:idx], category[idx+1:]
	}
	return "", category
}

// significantWordOverlap checks if two strings share significant words
// (ignoring common/generic words). Returns true if at least one distinctive
// word appears in both strings.
func significantWordOverlap(a, b string) bool {
	genericWords := map[string]bool{
		"api": true, "server": true, "config": true, "configuration": true,
		"setup": true, "patterns": true, "details": true, "info": true,
		"connection": true, "the": true, "a": true, "an": true,
	}

	wordsA := strings.Fields(a)
	wordsB := make(map[string]bool, len(strings.Fields(b)))
	for _, w := range strings.Fields(b) {
		wordsB[w] = true
	}

	for _, w := range wordsA {
		if genericWords[w] || len(w) <= 2 {
			continue
		}
		if wordsB[w] {
			return true
		}
	}
	return false
}
