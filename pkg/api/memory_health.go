package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
)

const memoryHealthTTL = 5 * 24 * time.Hour

type MemoryHealthResponse struct {
	EvaluatedAt         string                 `json:"evaluated_at"`
	ExpiresAt           string                 `json:"expires_at"`
	Generated           bool                   `json:"generated"`
	RecommendationCount int                    `json:"recommendation_count"`
	Recommendations     []MemoryRecommendation `json:"recommendations"`
	Map                 MemoryMapResponse      `json:"map"`
}

type MemoryRecommendation struct {
	ID          string              `json:"id"`
	Type        string              `json:"type"`
	Severity    string              `json:"severity"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	TargetScope string              `json:"target_scope"`
	GroupKey    string              `json:"group_key"`
	MemoryIDs   []string            `json:"memory_ids"`
	Flags       []MemoryMapFlag     `json:"flags,omitempty"`
	Card        memory.ScenarioCard `json:"card"`
}

type memoryHealthCacheEntry struct {
	response MemoryHealthResponse
	snapshot string
}

var memoryHealthCache = struct {
	sync.Mutex
	entries map[string]memoryHealthCacheEntry
}{entries: make(map[string]memoryHealthCacheEntry)}

// MemoryHealthHandler returns product-facing memory reorganization suggestions.
// It refreshes lazily: no background schedule runs, and a cached evaluation is
// reused for five days unless the caller explicitly asks for a refresh.
//
//	GET /api/memories/health?limit=500&refresh=true
func MemoryHealthHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}
	limit := queryInt(r, "limit", 500)
	if limit <= 0 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}
	force := strings.EqualFold(r.URL.Query().Get("refresh"), "true") || r.URL.Query().Get("refresh") == "1"

	memories, err := collectVisibleMemories(r, svc, pu, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	snapshot := memoryHealthSnapshot(memories)
	cacheKey := memoryHealthCacheKey(pu, activeTeamSlug(r, pu))
	now := time.Now().UTC()

	memoryHealthCache.Lock()
	cached, ok := memoryHealthCache.entries[cacheKey]
	if ok && !force && cached.snapshot == snapshot {
		expiresAt, _ := time.Parse(time.RFC3339, cached.response.ExpiresAt)
		if now.Before(expiresAt) {
			resp := cached.response
			resp.Generated = false
			memoryHealthCache.Unlock()
			respondJSON(w, http.StatusOK, resp)
			return
		}
	}
	memoryHealthCache.Unlock()

	report := BuildMemoryMap(memories)
	resp := BuildMemoryHealth(report, CanManageOrg(pu), now)
	resp.Generated = true

	memoryHealthCache.Lock()
	memoryHealthCache.entries[cacheKey] = memoryHealthCacheEntry{response: resp, snapshot: snapshot}
	memoryHealthCache.Unlock()

	respondJSON(w, http.StatusOK, resp)
}

func BuildMemoryHealth(report MemoryMapResponse, canManageOrg bool, now time.Time) MemoryHealthResponse {
	recommendations := make([]MemoryRecommendation, 0)
	for _, group := range report.Groups {
		if rec, ok := memoryRecommendationForGroup(group, canManageOrg); ok {
			recommendations = append(recommendations, rec)
		}
	}
	sort.SliceStable(recommendations, func(i, j int) bool {
		if recommendationSeverityRank(recommendations[i].Severity) == recommendationSeverityRank(recommendations[j].Severity) {
			return recommendations[i].Title < recommendations[j].Title
		}
		return recommendationSeverityRank(recommendations[i].Severity) > recommendationSeverityRank(recommendations[j].Severity)
	})
	evaluatedAt := now.UTC()
	return MemoryHealthResponse{
		EvaluatedAt:         evaluatedAt.Format(time.RFC3339),
		ExpiresAt:           evaluatedAt.Add(memoryHealthTTL).Format(time.RFC3339),
		RecommendationCount: len(recommendations),
		Recommendations:     recommendations,
		Map:                 report,
	}
}

func memoryRecommendationForGroup(group MemoryMapGroup, canManageOrg bool) (MemoryRecommendation, bool) {
	rawMemories := nonScenarioMemories(group.Memories)
	if group.HasScenarioCard {
		_, card, ok := scenarioCardInGroup(group.Memories)
		if !ok {
			return MemoryRecommendation{}, false
		}
		newRaw := rawMemoriesNotInCard(rawMemories, card)
		if len(newRaw) > 0 {
			incoming := memory.DraftScenarioCardFromMemories(group.Key, firstNonEmptyString(card.Scope, preferredTargetScope(group, canManageOrg)), newRaw)
			merged := memory.MergeScenarioCards(card, incoming)
			return MemoryRecommendation{
				ID:          "update-" + group.Key,
				Type:        "update_scenario_card",
				Severity:    "medium",
				Title:       fmt.Sprintf("Update %s", group.Title),
				Description: fmt.Sprintf("Add %d newer source memor%s to the existing scenario card.", len(newRaw), pluralY(len(newRaw))),
				TargetScope: firstNonEmptyString(merged.Scope, preferredTargetScope(group, canManageOrg)),
				GroupKey:    group.Key,
				MemoryIDs:   memoryIDs(newRaw),
				Flags:       group.Flags,
				Card:        merged,
			}, true
		}
		return MemoryRecommendation{}, false
	}
	if group.MemoryCount < 2 && !groupHasRiskFlags(group) {
		return MemoryRecommendation{}, false
	}
	targetScope := preferredTargetScope(group, canManageOrg)
	card := memory.DraftScenarioCardFromMemories(group.Key, targetScope, rawMemories)
	severity := "medium"
	if groupHasRiskFlags(group) || len(group.Scopes) > 1 {
		severity = "high"
	}
	return MemoryRecommendation{
		ID:          "create-" + group.Key,
		Type:        "create_scenario_card",
		Severity:    severity,
		Title:       fmt.Sprintf("Create scenario card for %s", group.Title),
		Description: fmt.Sprintf("Consolidate %d related memor%s into one efficient successful path.", len(rawMemories), pluralY(len(rawMemories))),
		TargetScope: targetScope,
		GroupKey:    group.Key,
		MemoryIDs:   memoryIDs(rawMemories),
		Flags:       group.Flags,
		Card:        card,
	}, true
}

func memoryHealthSnapshot(memories []store.MemorySearchResult) string {
	parts := make([]string, 0, len(memories))
	for _, m := range memories {
		parts = append(parts, strings.Join([]string{m.ID, m.Scope, m.Category, m.CreatedAt, m.SessionID, m.Snippet}, "\x00"))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return hex.EncodeToString(sum[:])
}

func memoryHealthCacheKey(pu *PlatformUser, teamSlug string) string {
	if pu == nil {
		return "anonymous"
	}
	return strings.Join([]string{pu.OrgSlug, teamSlug, pu.ID}, ":")
}

func activeTeamSlug(r *http.Request, pu *PlatformUser) string {
	if team := strings.TrimSpace(r.Header.Get("X-Astonish-Team")); team != "" {
		return team
	}
	if pu != nil {
		return pu.TeamSlug
	}
	return ""
}

func nonScenarioMemories(memories []store.MemorySearchResult) []store.MemorySearchResult {
	var out []store.MemorySearchResult
	for _, m := range memories {
		if !memory.IsScenarioCard(m) {
			out = append(out, m)
		}
	}
	return out
}

func scenarioCardInGroup(memories []store.MemorySearchResult) (store.MemorySearchResult, memory.ScenarioCard, bool) {
	for _, m := range memories {
		if card, ok := memory.ParseScenarioCard(m.Snippet); ok {
			return m, card, true
		}
	}
	return store.MemorySearchResult{}, memory.ScenarioCard{}, false
}

func rawMemoriesNotInCard(raw []store.MemorySearchResult, card memory.ScenarioCard) []store.MemorySearchResult {
	sourceIDs := make(map[string]bool, len(card.SourceMemoryIDs))
	for _, id := range card.SourceMemoryIDs {
		sourceIDs[id] = true
	}
	var out []store.MemorySearchResult
	for _, m := range raw {
		if m.ID == "" || !sourceIDs[m.ID] {
			out = append(out, m)
		}
	}
	return out
}

func groupHasRiskFlags(group MemoryMapGroup) bool {
	for _, flag := range group.Flags {
		switch flag.Type {
		case "duplicate_risk", "scattered_topic", "transient_failure_risk", "trial_error_risk":
			return true
		}
	}
	return false
}

func preferredTargetScope(group MemoryMapGroup, canManageOrg bool) string {
	if hasString(group.Scopes, string(store.MemoryScopeOrg)) && canManageOrg {
		return string(store.MemoryScopeOrg)
	}
	if hasString(group.Scopes, string(store.MemoryScopeTeam)) || len(group.Scopes) > 1 {
		return string(store.MemoryScopeTeam)
	}
	return string(store.MemoryScopePersonal)
}

func memoryIDs(memories []store.MemorySearchResult) []string {
	ids := make([]string, 0, len(memories))
	for _, m := range memories {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

func hasString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func pluralY(count int) string {
	if count == 1 {
		return "y"
	}
	return "ies"
}

func recommendationSeverityRank(severity string) int {
	switch severity {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}
