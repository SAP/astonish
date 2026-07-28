package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

const (
	ScenarioCardCategory          = "scenario_card/efficient_successful_path"
	ScenarioCardType              = "efficient_successful_path"
	ScenarioCardStatusDraft       = "draft"
	ScenarioCardStatusVerified    = "verified"
	ScenarioCardPlaceholderRecipe = "Review the source memories and replace this draft with the shortest verified successful path."
)

type ScenarioCard struct {
	CanonicalKey                  string                   `json:"canonical_key"`
	ScenarioID                    string                   `json:"scenario_id,omitempty"`
	Scope                         string                   `json:"scope,omitempty"`
	Title                         string                   `json:"title"`
	Aliases                       []string                 `json:"aliases,omitempty"`
	Category                      string                   `json:"category,omitempty"`
	Facts                         []string                 `json:"facts,omitempty"`
	RecommendedRecipe             []string                 `json:"recommended_recipe"`
	Conditions                    []string                 `json:"conditions,omitempty"`
	CautionsOrConditionalFailures []string                 `json:"cautions_or_conditional_failures,omitempty"`
	Verification                  []string                 `json:"verification,omitempty"`
	SourceMemoryIDs               []string                 `json:"source_memory_ids,omitempty"`
	SourceSessionIDs              []string                 `json:"source_session_ids,omitempty"`
	RelatedScenarioIDs            []string                 `json:"related_scenario_ids,omitempty"`
	Superseded                    []ScenarioSupersededItem `json:"superseded,omitempty"`
	Identity                      ScenarioIdentity         `json:"identity,omitempty"`
	Status                        string                   `json:"status"`
	Confidence                    float64                  `json:"confidence,omitempty"`
	LastVerifiedAt                string                   `json:"last_verified_at,omitempty"`
}

func IsScenarioCard(result store.MemorySearchResult) bool {
	return strings.Contains(result.Category, "scenario_card") || strings.Contains(result.Snippet, "astonish_memory_type: scenario_card")
}

func NormalizeScenarioKey(s string) string {
	s = strings.ToLower(s)
	words := regexp.MustCompile(`[a-z0-9]+`).FindAllString(s, -1)
	var selected []string
	seen := make(map[string]bool)
	for _, word := range words {
		if len(word) < 3 || scenarioStopWords[word] || seen[word] {
			continue
		}
		selected = append(selected, word)
		seen[word] = true
		if len(selected) == 4 {
			break
		}
	}
	if len(selected) == 0 {
		return "general-scenario"
	}
	return strings.Join(selected, "-")
}

func DraftScenarioCardFromMemoryEntry(targetScope string, entry store.MemoryEntry) ScenarioCard {
	result := store.MemorySearchResult{
		Snippet:   entry.Content,
		Category:  entry.Category,
		Scope:     targetScope,
		CreatedBy: entry.CreatedBy,
		SessionID: entry.SessionID,
	}
	return DraftScenarioCardFromMemories("", targetScope, []store.MemorySearchResult{result})
}

func DraftScenarioCardFromMemories(key, targetScope string, memories []store.MemorySearchResult) ScenarioCard {
	if key == "" && len(memories) > 0 {
		key = NormalizeScenarioKey(strings.Join([]string{memories[0].Category, memories[0].Path, memories[0].Snippet}, " "))
	}
	if key == "" {
		key = "general-scenario"
	}
	card := ScenarioCard{
		CanonicalKey: key,
		Scope:        targetScope,
		Title:        titleFromKey(key),
		Category:     ScenarioCardCategory,
		Status:       ScenarioCardStatusDraft,
		Confidence:   0.6,
	}
	for _, m := range memories {
		if m.ID != "" {
			card.SourceMemoryIDs = appendUnique(card.SourceMemoryIDs, m.ID)
		}
		if m.SessionID != "" {
			card.SourceSessionIDs = appendUnique(card.SourceSessionIDs, m.SessionID)
		}
		for _, bullet := range extractReusableBullets(m.Snippet) {
			if transientOrFailureLine(bullet) {
				card.CautionsOrConditionalFailures = appendUnique(card.CautionsOrConditionalFailures, softenCaution(bullet))
				continue
			}
			card.RecommendedRecipe = appendUnique(card.RecommendedRecipe, bullet)
		}
	}
	if len(card.RecommendedRecipe) == 0 && len(memories) > 0 {
		card.RecommendedRecipe = []string{ScenarioCardPlaceholderRecipe}
	}
	if len(card.Conditions) == 0 {
		card.Conditions = []string{"Applies when the request matches this scenario and the referenced environment, credentials, and permissions are available."}
	}
	if len(card.Verification) == 0 {
		card.Verification = []string{"Drafted from existing memories; verify on the next successful run before marking verified."}
	}
	return card
}

func HasUsableScenarioRecipe(card ScenarioCard) bool {
	for _, step := range card.RecommendedRecipe {
		if strings.TrimSpace(step) != "" && !strings.EqualFold(strings.TrimSpace(step), ScenarioCardPlaceholderRecipe) {
			return true
		}
	}
	return false
}

func RenderScenarioCard(card ScenarioCard) string {
	if card.Status == "" {
		card.Status = ScenarioCardStatusDraft
	}
	if card.Category == "" {
		card.Category = ScenarioCardCategory
	}
	var b strings.Builder
	b.WriteString("---\n")
	writeYAMLScalar(&b, "astonish_memory_type", "scenario_card")
	writeYAMLScalar(&b, "card_type", ScenarioCardType)
	writeYAMLScalar(&b, "canonical_key", card.CanonicalKey)
	writeYAMLScalar(&b, "status", card.Status)
	writeYAMLScalar(&b, "scope", card.Scope)
	if card.Confidence > 0 {
		writeYAMLScalar(&b, "confidence", fmt.Sprintf("%.2f", card.Confidence))
	}
	if card.LastVerifiedAt != "" {
		writeYAMLScalar(&b, "last_verified_at", card.LastVerifiedAt)
	}
	writeYAMLScalar(&b, "scenario_id", card.ScenarioID)
	writeYAMLList(&b, "aliases", card.Aliases)
	writeYAMLList(&b, "source_memory_ids", card.SourceMemoryIDs)
	writeYAMLList(&b, "source_session_ids", card.SourceSessionIDs)
	writeYAMLList(&b, "related_scenario_ids", card.RelatedScenarioIDs)
	writeYAMLJSON(&b, "identity_json", card.Identity)
	writeYAMLJSON(&b, "superseded_json", card.Superseded)
	b.WriteString("---\n\n")
	b.WriteString("# ")
	b.WriteString(strings.TrimSpace(card.Title))
	b.WriteString("\n\n")
	writeSection(&b, "Recommended path", card.RecommendedRecipe)
	writeSection(&b, "Conditions", card.Conditions)
	writeSection(&b, "Verification", card.Verification)
	writeSection(&b, "Cautions or conditional failures", card.CautionsOrConditionalFailures)
	writeSection(&b, "Facts", card.Facts)
	return strings.TrimSpace(b.String()) + "\n"
}

func ParseScenarioCard(content string) (ScenarioCard, bool) {
	if !strings.Contains(content, "astonish_memory_type: scenario_card") {
		return ScenarioCard{}, false
	}
	card := ScenarioCard{Status: ScenarioCardStatusDraft, Category: ScenarioCardCategory}
	frontmatter := ""
	body := content
	if strings.HasPrefix(content, "---\n") {
		parts := strings.SplitN(strings.TrimPrefix(content, "---\n"), "\n---\n", 2)
		if len(parts) == 2 {
			frontmatter = parts[0]
			body = parts[1]
		}
	}
	var listKey string
	for _, rawLine := range strings.Split(frontmatter, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "- ") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			switch listKey {
			case "aliases":
				card.Aliases = appendUnique(card.Aliases, value)
			case "source_memory_ids":
				card.SourceMemoryIDs = appendUnique(card.SourceMemoryIDs, value)
			case "source_session_ids":
				card.SourceSessionIDs = appendUnique(card.SourceSessionIDs, value)
			case "related_scenario_ids":
				card.RelatedScenarioIDs = appendUnique(card.RelatedScenarioIDs, value)
			}
			continue
		}
		listKey = ""
		if strings.HasPrefix(line, "canonical_key:") {
			card.CanonicalKey = strings.TrimSpace(strings.TrimPrefix(line, "canonical_key:"))
		} else if strings.HasPrefix(line, "scenario_id:") {
			card.ScenarioID = strings.TrimSpace(strings.TrimPrefix(line, "scenario_id:"))
		} else if strings.HasPrefix(line, "status:") {
			card.Status = strings.TrimSpace(strings.TrimPrefix(line, "status:"))
		} else if strings.HasPrefix(line, "scope:") {
			card.Scope = strings.TrimSpace(strings.TrimPrefix(line, "scope:"))
		} else if strings.HasPrefix(line, "aliases:") {
			listKey = "aliases"
		} else if strings.HasPrefix(line, "source_memory_ids:") {
			listKey = "source_memory_ids"
		} else if strings.HasPrefix(line, "source_session_ids:") {
			listKey = "source_session_ids"
		} else if strings.HasPrefix(line, "related_scenario_ids:") {
			listKey = "related_scenario_ids"
		} else if strings.HasPrefix(line, "identity_json:") {
			_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "identity_json:"))), &card.Identity)
		} else if strings.HasPrefix(line, "superseded_json:") {
			_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "superseded_json:"))), &card.Superseded)
		}
	}
	card.Title = extractMarkdownTitle(body)
	card.RecommendedRecipe = extractMarkdownSection(body, "Recommended path")
	card.Conditions = extractMarkdownSection(body, "Conditions")
	card.Verification = extractMarkdownSection(body, "Verification")
	card.CautionsOrConditionalFailures = extractMarkdownSection(body, "Cautions or conditional failures")
	card.Facts = extractMarkdownSection(body, "Facts")
	if card.CanonicalKey == "" {
		card.CanonicalKey = NormalizeScenarioKey(card.Title)
	}
	return card, true
}

func MergeScenarioCards(existing, incoming ScenarioCard) ScenarioCard {
	merged := existing
	if merged.CanonicalKey == "" {
		merged.CanonicalKey = incoming.CanonicalKey
	}
	if merged.Title == "" || merged.Title == titleFromKey(merged.CanonicalKey) {
		merged.Title = incoming.Title
	}
	merged.Scope = firstNonEmpty(merged.Scope, incoming.Scope)
	merged.Category = ScenarioCardCategory
	merged.Status = strongerStatus(existing.Status, incoming.Status)
	merged.Confidence = max(existing.Confidence, incoming.Confidence)
	if merged.ScenarioID == "" {
		merged.ScenarioID = incoming.ScenarioID
	}
	if incoming.CanonicalKey != "" && !strings.EqualFold(incoming.CanonicalKey, merged.CanonicalKey) {
		merged.Aliases = appendUnique(merged.Aliases, incoming.CanonicalKey)
	}
	merged.Aliases = appendUniqueMany(merged.Aliases, incoming.Aliases)
	merged.RelatedScenarioIDs = appendUniqueMany(merged.RelatedScenarioIDs, incoming.RelatedScenarioIDs)
	merged.Superseded = appendScenarioSuperseded(merged.Superseded, incoming.Superseded)
	merged.Identity = mergeScenarioIdentity(ExtractScenarioIdentity(merged), ExtractScenarioIdentity(incoming))
	merged.Facts = appendUniqueMany(merged.Facts, incoming.Facts)
	merged.RecommendedRecipe = appendUniqueMany(merged.RecommendedRecipe, incoming.RecommendedRecipe)
	merged.Conditions = appendUniqueMany(merged.Conditions, incoming.Conditions)
	merged.CautionsOrConditionalFailures = appendUniqueMany(merged.CautionsOrConditionalFailures, incoming.CautionsOrConditionalFailures)
	merged.Verification = appendUniqueMany(merged.Verification, incoming.Verification)
	merged.SourceMemoryIDs = appendUniqueMany(merged.SourceMemoryIDs, incoming.SourceMemoryIDs)
	merged.SourceSessionIDs = appendUniqueMany(merged.SourceSessionIDs, incoming.SourceSessionIDs)
	if merged.Status == ScenarioCardStatusVerified && merged.LastVerifiedAt == "" {
		merged.LastVerifiedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return merged
}

type ScenarioUpsertResult struct {
	Action     string `json:"action"`
	ExistingID string `json:"existing_id,omitempty"`
}

func UpsertScenarioCard(ctx context.Context, memStore store.MemoryStore, card ScenarioCard) (ScenarioUpsertResult, error) {
	if memStore == nil {
		return ScenarioUpsertResult{}, fmt.Errorf("memory store is nil")
	}
	if card.CanonicalKey == "" {
		card.CanonicalKey = NormalizeScenarioKey(card.Title)
	}
	if card.Category == "" {
		card.Category = ScenarioCardCategory
	}
	if card.Status == "" {
		card.Status = ScenarioCardStatusDraft
	}
	card.Identity = ExtractScenarioIdentity(card)
	existingCards, err := memStore.List(ctx, ScenarioCardCategory, 1000, 0)
	if err != nil {
		return ScenarioUpsertResult{}, fmt.Errorf("failed to list scenario cards: %w", err)
	}
	parsedCards := make([]ScenarioCard, 0, len(existingCards))
	for _, existing := range existingCards {
		if existingCard, ok := ParseScenarioCard(existing.Snippet); ok {
			parsedCards = append(parsedCards, existingCard)
		}
	}
	stats := BuildScenarioCorpusStats(append(parsedCards, card))
	var candidates []ScenarioCandidate
	for _, existing := range existingCards {
		existingCard, ok := ParseScenarioCard(existing.Snippet)
		if !ok {
			continue
		}
		score := ScoreScenarioPair(existingCard, card, stats)
		if score.Decision == "merge" || score.Decision == "review" {
			candidates = append(candidates, ScenarioCandidate{ID: existing.ID, Card: existingCard, Score: score})
		}
	}
	if best, ok := FindBestScenarioCandidate(candidates); ok {
		merged := MergeScenarioCards(best.Card, card)
		if err := memStore.Update(ctx, best.ID, RenderScenarioCard(merged), ScenarioCardCategory); err != nil {
			return ScenarioUpsertResult{}, fmt.Errorf("failed to update scenario card: %w", err)
		}
		return ScenarioUpsertResult{Action: "merged", ExistingID: best.ID}, nil
	}
	if err := memStore.Add(ctx, store.MemoryEntry{
		Content:  RenderScenarioCard(card),
		Category: ScenarioCardCategory,
	}); err != nil {
		return ScenarioUpsertResult{}, fmt.Errorf("failed to create scenario card: %w", err)
	}
	return ScenarioUpsertResult{Action: "created"}, nil
}

func FilterPreferredScenarioResults(results []store.MemorySearchResult) []store.MemorySearchResult {
	if len(results) == 0 {
		return results
	}
	superseded := make(map[string]bool)
	cardsByKey := make(map[string]bool)
	var cards []store.MemorySearchResult
	var parsedCards []ScenarioCard
	for _, r := range results {
		if !IsScenarioCard(r) {
			continue
		}
		card, ok := ParseScenarioCard(r.Snippet)
		if !ok {
			cards = append(cards, r)
			continue
		}
		cardsByKey[card.CanonicalKey] = true
		parsedCards = append(parsedCards, card)
		cards = append(cards, r)
		for _, id := range card.SourceMemoryIDs {
			superseded[id] = true
		}
	}
	if len(cards) == 0 {
		return results
	}
	stats := BuildScenarioCorpusStats(parsedCards)
	keptCardIDs := make(map[string]bool)
	for _, r := range cards {
		card, ok := ParseScenarioCard(r.Snippet)
		if !ok {
			keptCardIDs[r.ID] = true
			continue
		}
		duplicate := false
		for keptID := range keptCardIDs {
			var keptCard ScenarioCard
			for _, candidate := range cards {
				if candidate.ID == keptID {
					keptCard, _ = ParseScenarioCard(candidate.Snippet)
					break
				}
			}
			if ScoreScenarioPair(keptCard, card, stats).Decision == "merge" {
				duplicate = true
				break
			}
		}
		if !duplicate {
			keptCardIDs[r.ID] = true
		}
	}
	var filtered []store.MemorySearchResult
	for _, r := range results {
		if IsScenarioCard(r) {
			if keptCardIDs[r.ID] || r.ID == "" {
				filtered = append(filtered, r)
			}
			continue
		}
		if r.ID != "" && superseded[r.ID] {
			continue
		}
		if cardsByKey[NormalizeScenarioKey(strings.Join([]string{r.Category, r.Path, r.Snippet}, " "))] {
			continue
		}
		filtered = append(filtered, r)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		iCard := IsScenarioCard(filtered[i])
		jCard := IsScenarioCard(filtered[j])
		if iCard != jCard {
			return iCard
		}
		return filtered[i].Score > filtered[j].Score
	})
	return filtered
}

func writeYAMLScalar(b *strings.Builder, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(strings.TrimSpace(value))
	b.WriteString("\n")
}

func writeYAMLList(b *strings.Builder, key string, values []string) {
	if len(values) == 0 {
		return
	}
	b.WriteString(key)
	b.WriteString(":\n")
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		b.WriteString("  - ")
		b.WriteString(strings.TrimSpace(value))
		b.WriteString("\n")
	}
}

func writeYAMLJSON(b *strings.Builder, key string, value any) {
	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) == "null" || string(encoded) == "{}" || string(encoded) == "[]" {
		return
	}
	b.WriteString(key)
	b.WriteString(": ")
	b.Write(encoded)
	b.WriteString("\n")
}

func writeSection(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	b.WriteString("## ")
	b.WriteString(title)
	b.WriteString("\n\n")
	for _, item := range items {
		item = strings.TrimSpace(strings.TrimPrefix(item, "-"))
		if item == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(item)
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func extractReusableBullets(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")
		if len(line) < 12 {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		content = strings.Join(strings.Fields(content), " ")
		if len(content) > 0 {
			out = append(out, content)
		}
	}
	return out
}

func transientOrFailureLine(line string) bool {
	lower := strings.ToLower(line)
	markers := []string{"temporary", "temporarily", "outage", "timeout", "timed out", "503", "502", "failed attempt", "trial and error", "did not work", "doesn't work", "do not use", "avoid"}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func softenCaution(line string) string {
	line = strings.TrimSpace(line)
	if strings.Contains(strings.ToLower(line), "do not use") || strings.Contains(strings.ToLower(line), "avoid") {
		return "Treat as conditional only; verify current service status before avoiding this path: " + line
	}
	return line
}

func titleFromKey(key string) string {
	parts := strings.Split(key, "-")
	for i := range parts {
		if parts[i] == "" {
			continue
		}
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, " ")
}

func extractMarkdownTitle(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return "Scenario Card"
}

func extractMarkdownSection(body, title string) []string {
	lines := strings.Split(body, "\n")
	needle := "## " + strings.ToLower(title)
	inSection := false
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(trimmed), "## ") {
			inSection = strings.ToLower(trimmed) == needle
			continue
		}
		if !inSection || !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		out = append(out, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
	}
	return out
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if strings.EqualFold(existing, value) {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueMany(values []string, more []string) []string {
	for _, value := range more {
		values = appendUnique(values, value)
	}
	return values
}

func appendScenarioSuperseded(values []ScenarioSupersededItem, more []ScenarioSupersededItem) []ScenarioSupersededItem {
	for _, item := range more {
		item.Value = strings.TrimSpace(item.Value)
		if item.Value == "" {
			continue
		}
		duplicate := false
		for _, existing := range values {
			if strings.EqualFold(existing.Value, item.Value) && strings.EqualFold(existing.SupersededBy, item.SupersededBy) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			values = append(values, item)
		}
	}
	return values
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func strongerStatus(a, b string) string {
	if a == ScenarioCardStatusVerified || b == ScenarioCardStatusVerified {
		return ScenarioCardStatusVerified
	}
	return ScenarioCardStatusDraft
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

var scenarioStopWords = map[string]bool{
	"and": true, "are": true, "but": true, "for": true, "from": true, "how": true, "memory": true,
	"not": true, "the": true, "this": true, "that": true, "use": true, "using": true, "with": true,
}
