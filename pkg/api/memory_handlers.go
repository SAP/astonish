package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
)

// --------------------------------------------------------------------------
// Request / response types
// --------------------------------------------------------------------------

// MemorySaveRequest is the payload for saving a memory to a specific scope.
type MemorySaveRequest struct {
	Content    string `json:"content"`
	Category   string `json:"category,omitempty"`
	SourcePath string `json:"source_path,omitempty"`
}

// MemoryPromoteRequest is the payload for promoting a team memory to org.
type MemoryPromoteRequest struct {
	MemoryID string `json:"memory_id"`
	TeamSlug string `json:"team_slug"`
}

// MemorySearchRequest is the payload for cross-scope memory search.
type MemorySearchRequest struct {
	Query      string `json:"query"`
	Category   string `json:"category,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

// MemoryListResponse is the response for memory list/search endpoints.
type MemoryListResponse struct {
	Results []store.MemorySearchResult `json:"results"`
	Count   int                        `json:"count"`
}

// --------------------------------------------------------------------------
// Share memory to team (5.3)
// --------------------------------------------------------------------------

// MemoryShareToTeamHandler saves a memory entry to the team scope.
//
//	POST /api/memories/team
//
// Platform mode only. The memory is saved to the current user's team store.
func MemoryShareToTeamHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}

	if svc.Memory == nil {
		respondError(w, http.StatusServiceUnavailable, "team memory store not available")
		return
	}

	var req MemorySaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" {
		respondError(w, http.StatusBadRequest, "content is required")
		return
	}

	entry := store.MemoryEntry{
		Content:    req.Content,
		Category:   req.Category,
		SourcePath: req.SourcePath,
		CreatedBy:  effectiveUserID(r),
	}
	card := memory.DraftScenarioCardFromMemoryEntry("team", entry)
	if !memory.HasUsableScenarioRecipe(card) {
		respondJSON(w, http.StatusOK, map[string]any{
			"saved":   false,
			"scope":   "team",
			"action":  "discarded",
			"message": "Memory was not saved because it did not form a usable scenario card",
		})
		return
	}
	result, err := memory.UpsertScenarioCard(r.Context(), svc.Memory, card)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save team scenario card: %v", err))
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"saved":   true,
		"scope":   "team",
		"action":  result.Action,
		"message": "Scenario card saved to team",
	})
}

// MemorySavePersonalHandler saves a memory entry to the user's personal scope.
//
//	POST /api/memories/personal
//
// Platform mode only. The memory is saved to the personal schema.
func MemorySavePersonalHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}

	// Resolve personal memory store via TenantRouter
	if svc.TenantRouter == nil {
		respondError(w, http.StatusServiceUnavailable, "tenant router not available")
		return
	}
	orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve org store")
		return
	}
	personalMem := orgStore.ForUser(pu.ID).Memories()

	var req MemorySaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" {
		respondError(w, http.StatusBadRequest, "content is required")
		return
	}

	entry := store.MemoryEntry{
		Content:    req.Content,
		Category:   req.Category,
		SourcePath: req.SourcePath,
		CreatedBy:  pu.ID,
	}
	card := memory.DraftScenarioCardFromMemoryEntry("personal", entry)
	if !memory.HasUsableScenarioRecipe(card) {
		respondJSON(w, http.StatusOK, map[string]any{
			"saved":   false,
			"scope":   "personal",
			"action":  "discarded",
			"message": "Memory was not saved because it did not form a usable scenario card",
		})
		return
	}
	result, err := memory.UpsertScenarioCard(r.Context(), personalMem, card)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save personal scenario card: %v", err))
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"saved":   true,
		"scope":   "personal",
		"action":  result.Action,
		"message": "Scenario card saved to personal store",
	})
}

// --------------------------------------------------------------------------
// Promote memory to org (5.4)
// --------------------------------------------------------------------------

// MemoryPromoteToOrgHandler copies a team memory to the org-wide store.
//
//	POST /api/memories/promote
//
// Admin-only. Requires "admin" role on the platform user.
func MemoryPromoteToOrgHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}
	if !CanManageOrg(pu) {
		respondError(w, http.StatusForbidden, "admin role required for knowledge promotion")
		return
	}

	var req MemoryPromoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.MemoryID == "" {
		respondError(w, http.StatusBadRequest, "memory_id is required")
		return
	}
	if req.TeamSlug == "" {
		respondError(w, http.StatusBadRequest, "team_slug is required")
		return
	}

	if svc.TenantRouter == nil {
		respondError(w, http.StatusServiceUnavailable, "tenant router not available")
		return
	}

	orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve org store")
		return
	}

	// Read the team memory
	teamMem := orgStore.ForTeam(req.TeamSlug).Memories()
	results, err := teamMem.List(r.Context(), "", 1000, 0)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to read team memories: %v", err))
		return
	}

	// Find the specific memory by ID
	var source *store.MemorySearchResult
	for i, m := range results {
		if m.ID == req.MemoryID {
			source = &results[i]
			break
		}
	}
	if source == nil {
		respondError(w, http.StatusNotFound, "memory not found in team store")
		return
	}

	orgMem := orgStore.OrgMemories()
	card := memory.DraftScenarioCardFromMemories("", "org", []store.MemorySearchResult{*source})
	if !memory.HasUsableScenarioRecipe(card) {
		if err := teamMem.Delete(r.Context(), req.MemoryID); err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete uncardable team memory: %v", err))
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"promoted": false,
			"scope":    "org",
			"action":   "discarded",
			"message":  fmt.Sprintf("Memory from team '%s' was discarded because it did not form a usable scenario card", req.TeamSlug),
		})
		return
	}
	result, err := memory.UpsertScenarioCard(r.Context(), orgMem, card)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to promote memory to org scenario card: %v", err))
		return
	}
	if err := teamMem.Delete(r.Context(), req.MemoryID); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("promoted memory but failed to delete team source memory: %v", err))
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"promoted": true,
		"scope":    "org",
		"action":   result.Action,
		"message":  fmt.Sprintf("Memory promoted from team '%s' into org scenario card", req.TeamSlug),
	})
}

// --------------------------------------------------------------------------
// Team/org memory browsing (5.5)
// --------------------------------------------------------------------------

// MemoryListTeamHandler lists memories in the current team store.
//
//	GET /api/memories/team?category=...&limit=...&offset=...
func MemoryListTeamHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	if svc.Memory == nil {
		respondError(w, http.StatusBadRequest, "platform mode with team context required")
		return
	}

	category := r.URL.Query().Get("category")
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)

	results, err := svc.Memory.List(r.Context(), category, limit, offset)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list team memories: %v", err))
		return
	}

	respondJSON(w, http.StatusOK, MemoryListResponse{
		Results: results,
		Count:   len(results),
	})
}

// MemoryListOrgHandler lists memories in the org-wide store.
//
//	GET /api/memories/org?category=...&limit=...&offset=...
func MemoryListOrgHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}

	orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve org store")
		return
	}

	orgMem := orgStore.OrgMemories()
	category := r.URL.Query().Get("category")
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)

	results, err := orgMem.List(r.Context(), category, limit, offset)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list org memories: %v", err))
		return
	}

	respondJSON(w, http.StatusOK, MemoryListResponse{
		Results: results,
		Count:   len(results),
	})
}

// MemorySearchCrossTierHandler performs a cross-tier memory search.
//
//	POST /api/memories/search
//
// In platform mode, searches across personal + team + org tiers.
// In personal mode, searches the local memory store.
func MemorySearchCrossTierHandler(w http.ResponseWriter, r *http.Request) {
	var req MemorySearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Query == "" {
		respondError(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.MaxResults <= 0 {
		req.MaxResults = 10
	}

	svc := store.FromRequest(r)

	// Platform mode: use three-tier searcher
	if svc != nil && svc.Mode == store.ModePlatform && svc.MemorySearcher != nil {
		var results []store.MemorySearchResult
		var err error
		if req.Category == "" {
			results, err = svc.MemorySearcher.SearchAllTiers(r.Context(), req.Query, req.MaxResults, 0)
		} else {
			results, err = svc.MemorySearcher.SearchAllTiersByCategory(r.Context(), req.Query, req.MaxResults, 0, req.Category)
		}
		if err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("cross-tier search failed: %v", err))
			return
		}
		results = memory.FilterPreferredScenarioResults(results)
		respondJSON(w, http.StatusOK, MemoryListResponse{Results: results, Count: len(results)})
		return
	}

	// Personal mode: use local memory store
	if svc != nil && svc.Memory != nil {
		var results []store.MemorySearchResult
		var err error
		if req.Category == "" {
			results, err = svc.Memory.Search(r.Context(), req.Query, req.MaxResults, 0)
		} else {
			results, err = svc.Memory.SearchByCategory(r.Context(), req.Query, req.MaxResults, 0, req.Category)
		}
		if err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("memory search failed: %v", err))
			return
		}
		results = memory.FilterPreferredScenarioResults(results)
		respondJSON(w, http.StatusOK, MemoryListResponse{Results: results, Count: len(results)})
		return
	}

	respondError(w, http.StatusServiceUnavailable, "memory store not available")
}

// MemoryDeleteTeamHandler deletes a memory from the team store.
//
//	DELETE /api/memories/team/{id}
//
// Access control: creator can delete their own OR team admin can delete any.
func MemoryDeleteTeamHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	if svc.Memory == nil {
		respondError(w, http.StatusBadRequest, "platform mode with team context required")
		return
	}
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}

	id := mux.Vars(r)["id"]
	if id == "" {
		respondError(w, http.StatusBadRequest, "memory ID required")
		return
	}

	// Check ownership
	existing, err := svc.Memory.Get(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get memory: %v", err))
		return
	}
	if existing == nil {
		respondError(w, http.StatusNotFound, "memory not found")
		return
	}
	if !canManageMemory(r, pu, existing, "team") {
		respondError(w, http.StatusForbidden, "permission denied: you can only delete memories you created or that you have admin access to")
		return
	}

	if err := svc.Memory.Delete(r.Context(), id); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete team memory: %v", err))
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"deleted": true,
		"id":      id,
	})
}

// MemoryDeleteOrgHandler deletes a memory from the org store.
//
//	DELETE /api/memories/org/{id}
//
// Access control: promoter/creator can delete their own OR org admin can delete any.
func MemoryDeleteOrgHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}

	id := mux.Vars(r)["id"]
	if id == "" {
		respondError(w, http.StatusBadRequest, "memory ID required")
		return
	}

	orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve org store")
		return
	}

	orgMem := orgStore.OrgMemories()

	// Check ownership
	existing, err := orgMem.Get(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get memory: %v", err))
		return
	}
	if existing == nil {
		respondError(w, http.StatusNotFound, "memory not found")
		return
	}
	if !canManageMemory(r, pu, existing, "org") {
		respondError(w, http.StatusForbidden, "permission denied: you can only delete memories you promoted or that you have admin access to")
		return
	}

	if err := orgMem.Delete(r.Context(), id); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete org memory: %v", err))
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"deleted": true,
		"id":      id,
	})
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// queryInt extracts an integer query parameter with a default value.
func queryInt(r *http.Request, key string, defaultVal int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 0 {
		return defaultVal
	}
	return n
}

// --------------------------------------------------------------------------
// List personal memories (Phase 1B)
// --------------------------------------------------------------------------

// MemoryListPersonalHandler lists memories in the user's personal store.
//
//	GET /api/memories/personal?category=...&limit=...&offset=...
func MemoryListPersonalHandler(w http.ResponseWriter, r *http.Request) {
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
	orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve org store")
		return
	}
	personalMem := orgStore.ForUser(pu.ID).Memories()

	category := r.URL.Query().Get("category")
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)

	results, err := personalMem.List(r.Context(), category, limit, offset)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list personal memories: %v", err))
		return
	}

	respondJSON(w, http.StatusOK, MemoryListResponse{
		Results: results,
		Count:   len(results),
	})
}

// --------------------------------------------------------------------------
// Delete personal memory (Phase 1E)
// --------------------------------------------------------------------------

// MemoryDeletePersonalHandler deletes a memory from the user's personal store.
//
//	DELETE /api/memories/personal/{id}
func MemoryDeletePersonalHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}

	id := mux.Vars(r)["id"]
	if id == "" {
		respondError(w, http.StatusBadRequest, "memory ID required")
		return
	}

	if svc.TenantRouter == nil {
		respondError(w, http.StatusServiceUnavailable, "tenant router not available")
		return
	}
	orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve org store")
		return
	}
	personalMem := orgStore.ForUser(pu.ID).Memories()

	if err := personalMem.Delete(r.Context(), id); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete personal memory: %v", err))
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"deleted": true,
		"id":      id,
	})
}

// --------------------------------------------------------------------------
// Promote personal memory to team (Phase 1C)
// --------------------------------------------------------------------------

// MemoryPromotePersonalToTeamRequest is the payload for personal→team promotion.
type MemoryPromotePersonalToTeamRequest struct {
	MemoryID string `json:"memory_id"`
}

// MemoryPromotePersonalToTeamHandler moves a personal memory to the team store.
//
//	POST /api/memories/promote-to-team
//
// Any user can promote their own personal memories to the team.
func MemoryPromotePersonalToTeamHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}

	var req MemoryPromotePersonalToTeamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.MemoryID == "" {
		respondError(w, http.StatusBadRequest, "memory_id is required")
		return
	}

	if svc.TenantRouter == nil {
		respondError(w, http.StatusServiceUnavailable, "tenant router not available")
		return
	}
	orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve org store")
		return
	}
	personalMem := orgStore.ForUser(pu.ID).Memories()

	// Read the memory from personal store
	entry, err := personalMem.Get(r.Context(), req.MemoryID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to read personal memory: %v", err))
		return
	}
	if entry == nil {
		respondError(w, http.StatusNotFound, "personal memory not found")
		return
	}

	// Verify team memory store is available
	if svc.Memory == nil {
		respondError(w, http.StatusServiceUnavailable, "team memory store not available")
		return
	}

	card := memory.DraftScenarioCardFromMemories("", "team", []store.MemorySearchResult{*entry})
	if !memory.HasUsableScenarioRecipe(card) {
		if err := personalMem.Delete(r.Context(), req.MemoryID); err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete uncardable personal memory: %v", err))
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"promoted": false,
			"scope":    "team",
			"action":   "discarded",
			"message":  "Memory was discarded because it did not form a usable scenario card",
		})
		return
	}
	result, err := memory.UpsertScenarioCard(r.Context(), svc.Memory, card)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to promote memory to team scenario card: %v", err))
		return
	}
	if err := personalMem.Delete(r.Context(), req.MemoryID); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("promoted memory but failed to delete personal source memory: %v", err))
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"promoted": true,
		"scope":    "team",
		"action":   result.Action,
		"message":  "Memory promoted into team scenario card",
	})
}

// --------------------------------------------------------------------------
// Update memory (Phase 1D)
// --------------------------------------------------------------------------

// MemoryEditRequest is the payload for updating a memory entry's content.
type MemoryEditRequest struct {
	Content  string `json:"content"`
	Category string `json:"category,omitempty"`
}

// MemoryUpdateHandler updates a memory entry in the specified scope.
//
//	PUT /api/memories/{scope}/{id}
//
// Access control: creator can edit their own OR admin for that scope.
func MemoryUpdateHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}

	vars := mux.Vars(r)
	scope := vars["scope"]
	id := vars["id"]
	if id == "" || scope == "" {
		respondError(w, http.StatusBadRequest, "scope and memory ID required")
		return
	}

	var req MemoryEditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" {
		respondError(w, http.StatusBadRequest, "content is required")
		return
	}

	// Resolve the appropriate memory store based on scope
	memStore, err := resolveMemoryStoreForScope(r, svc, pu, scope)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Check ownership/permissions
	existing, err := memStore.Get(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get memory: %v", err))
		return
	}
	if existing == nil {
		respondError(w, http.StatusNotFound, "memory not found")
		return
	}

	// Access control: creator can edit OR admin for the scope
	if !canManageMemory(r, pu, existing, scope) {
		respondError(w, http.StatusForbidden, "permission denied: you can only edit memories you created or that you have admin access to")
		return
	}

	content, category, usable := scenarioCardUpdateContent(scope, store.MemoryEntry{
		Content:   req.Content,
		Category:  req.Category,
		CreatedBy: pu.ID,
		SessionID: existing.SessionID,
	})
	if !usable {
		if err := memStore.Delete(r.Context(), id); err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete uncardable memory: %v", err))
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"updated": false,
			"deleted": true,
			"id":      id,
			"scope":   scope,
			"action":  "discarded",
		})
		return
	}

	// Perform the update as a scenario card. Raw edits are staging inputs only.
	if err := memStore.Update(r.Context(), id, content, category); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update memory: %v", err))
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"updated": true,
		"id":      id,
		"scope":   scope,
		"action":  "updated_scenario_card",
	})
}

// --------------------------------------------------------------------------
// Save memory to org (Phase 2B backend — for the new "Share with Org" option)
// --------------------------------------------------------------------------

// MemorySaveOrgHandler saves a memory entry directly to the org scope.
//
//	POST /api/memories/org
//
// Admin-only. Saves directly to org memories.
func MemorySaveOrgHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}
	if !CanManageOrg(pu) {
		respondError(w, http.StatusForbidden, "admin role required to save org memories")
		return
	}

	var req MemorySaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" {
		respondError(w, http.StatusBadRequest, "content is required")
		return
	}

	if svc.TenantRouter == nil {
		respondError(w, http.StatusServiceUnavailable, "tenant router not available")
		return
	}
	orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve org store")
		return
	}

	orgMem := orgStore.OrgMemories()
	entry := store.MemoryEntry{
		Content:   req.Content,
		Category:  req.Category,
		CreatedBy: pu.ID,
	}
	card := memory.DraftScenarioCardFromMemoryEntry("org", entry)
	if !memory.HasUsableScenarioRecipe(card) {
		respondJSON(w, http.StatusOK, map[string]any{
			"saved":   false,
			"scope":   "org",
			"action":  "discarded",
			"message": "Memory was not saved because it did not form a usable scenario card",
		})
		return
	}
	result, err := memory.UpsertScenarioCard(r.Context(), orgMem, card)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save org scenario card: %v", err))
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"saved":   true,
		"scope":   "org",
		"action":  result.Action,
		"message": "Scenario card saved to organization",
	})
}

// --------------------------------------------------------------------------
// Helpers (memory-specific)
// --------------------------------------------------------------------------

func scenarioCardUpdateContent(scope string, entry store.MemoryEntry) (string, string, bool) {
	if card, ok := memory.ParseScenarioCard(entry.Content); ok {
		if card.Scope == "" {
			card.Scope = scope
		}
		if !memory.HasUsableScenarioRecipe(card) {
			return "", "", false
		}
		return memory.RenderScenarioCard(card), memory.ScenarioCardCategory, true
	}
	card := memory.DraftScenarioCardFromMemoryEntry(scope, entry)
	if !memory.HasUsableScenarioRecipe(card) {
		return "", "", false
	}
	return memory.RenderScenarioCard(card), memory.ScenarioCardCategory, true
}

func deleteConsolidatedSources(r *http.Request, svc *store.Services, pu *PlatformUser, sourceIDs []string) int {
	if len(sourceIDs) == 0 || svc == nil || pu == nil {
		return 0
	}
	return deleteSourceIDsFromStores(r.Context(), sourceIDs, memoryStoresForVisibleScopes(r, svc, pu))
}

func deleteSourceIDsFromStores(ctx context.Context, sourceIDs []string, stores []store.MemoryStore) int {
	deleted := 0
	seen := make(map[string]bool)
	for _, id := range sourceIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		for _, memStore := range stores {
			if memStore == nil {
				continue
			}
			existing, err := memStore.Get(ctx, id)
			if err != nil || existing == nil || memory.IsScenarioCard(*existing) {
				continue
			}
			if err := memStore.Delete(ctx, id); err == nil {
				deleted++
			}
			break
		}
	}
	return deleted
}

func deleteDuplicateScenarioCards(r *http.Request, svc *store.Services, pu *PlatformUser, duplicateIDs []string, preservedID string) int {
	if len(duplicateIDs) == 0 || svc == nil || pu == nil {
		return 0
	}
	deleted := 0
	seen := make(map[string]bool)
	for _, id := range duplicateIDs {
		if id == "" || id == preservedID || seen[id] {
			continue
		}
		seen[id] = true
		for _, memStore := range memoryStoresForVisibleScopes(r, svc, pu) {
			if memStore == nil {
				continue
			}
			existing, err := memStore.Get(r.Context(), id)
			if err != nil || existing == nil || !memory.IsScenarioCard(*existing) {
				continue
			}
			if err := memStore.Delete(r.Context(), id); err == nil {
				deleted++
			}
			break
		}
	}
	return deleted
}

func memoryStoresForVisibleScopes(r *http.Request, svc *store.Services, pu *PlatformUser) []store.MemoryStore {
	stores := make([]store.MemoryStore, 0, 3)
	if svc.TenantRouter != nil {
		if orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug); err == nil {
			if personal := orgStore.ForUser(pu.ID).Memories(); personal != nil {
				stores = append(stores, personal)
			}
			teamSlug := activeTeamSlug(r, pu)
			if team := orgStore.ForTeam(teamSlug).Memories(); team != nil {
				stores = append(stores, team)
			}
			if orgMem := orgStore.OrgMemories(); orgMem != nil {
				stores = append(stores, orgMem)
			}
			return stores
		}
	}
	if svc.Memory != nil {
		stores = append(stores, svc.Memory)
	}
	return stores
}

// resolveMemoryStoreForScope returns the appropriate MemoryStore for a given scope.
func resolveMemoryStoreForScope(r *http.Request, svc *store.Services, pu *PlatformUser, scope string) (store.MemoryStore, error) {
	switch scope {
	case "personal":
		if svc.TenantRouter == nil {
			return nil, fmt.Errorf("tenant router not available")
		}
		orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve org store: %w", err)
		}
		return orgStore.ForUser(pu.ID).Memories(), nil
	case "team":
		if svc.Memory == nil {
			return nil, fmt.Errorf("team memory store not available")
		}
		return svc.Memory, nil
	case "org":
		if svc.TenantRouter == nil {
			return nil, fmt.Errorf("tenant router not available")
		}
		orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve org store: %w", err)
		}
		return orgStore.OrgMemories(), nil
	default:
		return nil, fmt.Errorf("invalid scope: %s (must be personal, team, or org)", scope)
	}
}

// canManageMemory checks whether a user has permission to edit/delete a memory.
// Rules:
//   - Personal scope: always allowed (it's their own store)
//   - Team scope: creator can manage their own OR team admin can manage any
//   - Org scope: promoter/creator can manage their own OR org admin can manage any
func canManageMemory(r *http.Request, pu *PlatformUser, entry *store.MemorySearchResult, scope string) bool {
	switch scope {
	case "personal":
		// Personal memories are always manageable by the user
		return true
	case "team":
		// Creator can manage their own, or admin can manage any
		if entry.CreatedBy == pu.ID {
			return true
		}
		return CanManageTeam(r, pu)
	case "org":
		// Creator/promoter can manage their own, or org admin can manage any
		if entry.CreatedBy == pu.ID {
			return true
		}
		return CanManageOrg(pu)
	}
	return false
}

// MemoryListBySessionHandler returns all memories created during a specific session.
// GET /api/memories/session/{id}
// Returns memories from whichever store is active (personal or team mode).
func MemoryListBySessionHandler(w http.ResponseWriter, r *http.Request) {
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}

	sessionID := mux.Vars(r)["id"]
	if sessionID == "" {
		respondError(w, http.StatusBadRequest, "session id required")
		return
	}

	svc := store.FromRequest(r)
	if svc == nil {
		respondError(w, http.StatusInternalServerError, "services not available")
		return
	}

	// Try team store first, then personal
	teamStore, err := resolveMemoryStoreForScope(r, svc, pu, "team")
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var results []store.MemorySearchResult
	if teamStore != nil {
		results, err = teamStore.ListBySession(r.Context(), sessionID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list session memories: %v", err))
			return
		}
	}

	// Also check personal store
	personalStore, err := resolveMemoryStoreForScope(r, svc, pu, "personal")
	if err == nil && personalStore != nil {
		personalResults, pErr := personalStore.ListBySession(r.Context(), sessionID)
		if pErr == nil {
			results = append(results, personalResults...)
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"memories":   results,
		"count":      len(results),
	})
}
