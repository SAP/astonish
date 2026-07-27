package api

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/store"
)

type MemoryMapResponse struct {
	Groups []MemoryMapGroup `json:"groups"`
	Stats  MemoryMapStats   `json:"stats"`
}

type MemoryMapStats struct {
	TotalMemories       int `json:"total_memories"`
	GroupCount          int `json:"group_count"`
	DuplicateRiskCount  int `json:"duplicate_risk_count"`
	ScatteredTopicCount int `json:"scattered_topic_count"`
	TransientRiskCount  int `json:"transient_risk_count"`
	TrialErrorRiskCount int `json:"trial_error_risk_count"`
}

type MemoryMapGroup struct {
	Key            string                     `json:"key"`
	Title          string                     `json:"title"`
	MemoryCount    int                        `json:"memory_count"`
	Scopes         []string                   `json:"scopes"`
	Categories     []string                   `json:"categories"`
	SessionIDs     []string                   `json:"session_ids,omitempty"`
	CreatedBy      []string                   `json:"created_by,omitempty"`
	Flags          []MemoryMapFlag            `json:"flags,omitempty"`
	Representative store.MemorySearchResult   `json:"representative"`
	Memories       []store.MemorySearchResult `json:"memories"`
}

type MemoryMapFlag struct {
	Type        string `json:"type"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
}

// MemoryMapHandler returns a read-only diagnostic view of existing memories.
//
//	GET /api/memories/map?limit=500
//
// The report groups likely related memories by a simple canonical topic key and
// flags duplicate/scattering risks. It does not change memory retrieval or write
// semantics.
func MemoryMapHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}
	if svc.TenantRouter == nil {
		respondError(w, http.StatusServiceUnavailable, "tenant router not available")
		return
	}

	limit := queryInt(r, "limit", 500)
	if limit <= 0 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}

	orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve org store")
		return
	}

	var memories []store.MemorySearchResult
	if personalMem := orgStore.ForUser(pu.ID).Memories(); personalMem != nil {
		results, err := personalMem.List(r.Context(), "", limit, 0)
		if err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list personal memories: %v", err))
			return
		}
		memories = append(memories, ensureMemoryScope(results, string(store.MemoryScopePersonal))...)
	}
	if svc.Memory != nil {
		results, err := svc.Memory.List(r.Context(), "", limit, 0)
		if err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list team memories: %v", err))
			return
		}
		memories = append(memories, ensureMemoryScope(results, string(store.MemoryScopeTeam))...)
	}
	if orgMem := orgStore.OrgMemories(); orgMem != nil {
		results, err := orgMem.List(r.Context(), "", limit, 0)
		if err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list org memories: %v", err))
			return
		}
		memories = append(memories, ensureMemoryScope(results, string(store.MemoryScopeOrg))...)
	}

	respondJSON(w, http.StatusOK, BuildMemoryMap(memories))
}

func ensureMemoryScope(results []store.MemorySearchResult, scope string) []store.MemorySearchResult {
	for i := range results {
		if results[i].Scope == "" {
			results[i].Scope = scope
		}
	}
	return results
}

// BuildMemoryMap groups memory entries and attaches diagnostics. It is pure so
// tests can exercise the report without tenant store setup.
func BuildMemoryMap(memories []store.MemorySearchResult) MemoryMapResponse {
	groupsByKey := make(map[string][]store.MemorySearchResult)
	for _, memory := range memories {
		key := canonicalMemoryTopic(memory)
		groupsByKey[key] = append(groupsByKey[key], memory)
	}

	groups := make([]MemoryMapGroup, 0, len(groupsByKey))
	stats := MemoryMapStats{TotalMemories: len(memories)}
	for key, entries := range groupsByKey {
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].CreatedAt > entries[j].CreatedAt
		})
		group := MemoryMapGroup{
			Key:            key,
			Title:          memoryMapTitle(key, entries),
			MemoryCount:    len(entries),
			Scopes:         uniqueMemoryStrings(entries, func(m store.MemorySearchResult) string { return m.Scope }),
			Categories:     uniqueMemoryStrings(entries, func(m store.MemorySearchResult) string { return m.Category }),
			SessionIDs:     uniqueMemoryStrings(entries, func(m store.MemorySearchResult) string { return m.SessionID }),
			CreatedBy:      uniqueMemoryStrings(entries, func(m store.MemorySearchResult) string { return m.CreatedBy }),
			Representative: entries[0],
			Memories:       entries,
		}
		group.Flags = memoryMapFlags(group)
		for _, flag := range group.Flags {
			switch flag.Type {
			case "duplicate_risk":
				stats.DuplicateRiskCount++
			case "scattered_topic":
				stats.ScatteredTopicCount++
			case "transient_failure_risk":
				stats.TransientRiskCount++
			case "trial_error_risk":
				stats.TrialErrorRiskCount++
			}
		}
		groups = append(groups, group)
	}

	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].MemoryCount == groups[j].MemoryCount {
			return groups[i].Key < groups[j].Key
		}
		return groups[i].MemoryCount > groups[j].MemoryCount
	})
	stats.GroupCount = len(groups)
	return MemoryMapResponse{Groups: groups, Stats: stats}
}

func memoryMapFlags(group MemoryMapGroup) []MemoryMapFlag {
	var flags []MemoryMapFlag
	if group.MemoryCount > 1 {
		flags = append(flags, MemoryMapFlag{
			Type:        "duplicate_risk",
			Severity:    "info",
			Description: "Multiple memories share a likely topic. Review whether they should be consolidated into one scenario.",
		})
	}
	if len(group.Scopes) > 1 || len(group.Categories) > 1 {
		flags = append(flags, MemoryMapFlag{
			Type:        "scattered_topic",
			Severity:    "warning",
			Description: "Related memories are spread across scopes or categories, which can make retrieval inconsistent.",
		})
	}
	if groupContains(group, transientFailurePattern) {
		flags = append(flags, MemoryMapFlag{
			Type:        "transient_failure_risk",
			Severity:    "warning",
			Description: "This group mentions temporary outage or failure language. Avoid turning unstable conditions into broad permanent rules.",
		})
	}
	if groupContains(group, trialErrorPattern) {
		flags = append(flags, MemoryMapFlag{
			Type:        "trial_error_risk",
			Severity:    "info",
			Description: "This group appears to preserve exploratory failed attempts. Prefer distilling the shortest successful path.",
		})
	}
	return flags
}

func groupContains(group MemoryMapGroup, pattern *regexp.Regexp) bool {
	for _, memory := range group.Memories {
		if pattern.MatchString(memory.Snippet) || pattern.MatchString(memory.Category) || pattern.MatchString(memory.Path) {
			return true
		}
	}
	return false
}

var memoryWordPattern = regexp.MustCompile(`[a-z0-9]+`)
var transientFailurePattern = regexp.MustCompile(`(?i)\b(temporar(?:y|ily)|intermittent|flaky|unstable|outage|timeout|timed out|503|502|unavailable|rate limit)\b`)
var trialErrorPattern = regexp.MustCompile(`(?i)\b(failed attempt|trial and error|tried .* but|did not work|doesn't work|avoid|do not use|wrong path)\b`)

var memoryTopicStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "but": true,
	"by": true, "can": true, "for": true, "from": true, "how": true, "if": true, "in": true, "is": true,
	"it": true, "memory": true, "not": true, "of": true, "on": true, "or": true, "should": true, "that": true,
	"the": true, "this": true, "to": true, "use": true, "using": true, "when": true, "with": true,
}

func canonicalMemoryTopic(memory store.MemorySearchResult) string {
	text := strings.ToLower(strings.Join([]string{memory.Category, memory.Path, memory.Snippet}, " "))
	words := memoryWordPattern.FindAllString(text, -1)
	selected := make([]string, 0, 4)
	seen := make(map[string]bool)
	for _, word := range words {
		if len(word) < 3 || memoryTopicStopWords[word] || seen[word] {
			continue
		}
		selected = append(selected, word)
		seen[word] = true
		if len(selected) == 3 {
			break
		}
	}
	if len(selected) == 0 {
		return "uncategorized"
	}
	return strings.Join(selected, "-")
}

func memoryMapTitle(key string, entries []store.MemorySearchResult) string {
	for _, entry := range entries {
		if entry.Category != "" && entry.Category != "general" {
			return entry.Category
		}
	}
	return strings.ReplaceAll(key, "-", " ")
}

func uniqueMemoryStrings(entries []store.MemorySearchResult, value func(store.MemorySearchResult) string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, entry := range entries {
		v := value(entry)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
