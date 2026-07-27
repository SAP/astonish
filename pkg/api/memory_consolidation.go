package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
)

type MemoryConsolidationRequest struct {
	Key         string   `json:"key"`
	TargetScope string   `json:"target_scope"`
	MemoryIDs   []string `json:"memory_ids,omitempty"`
}

type MemoryConsolidationPreviewResponse struct {
	Card    memory.ScenarioCard `json:"card"`
	Content string              `json:"content"`
	Sources []string            `json:"sources"`
}

type MemoryConsolidationApplyRequest struct {
	Card        memory.ScenarioCard `json:"card"`
	TargetScope string              `json:"target_scope"`
}

type MemoryConsolidationApplyResponse struct {
	Applied        bool                        `json:"applied"`
	Scope          string                      `json:"scope"`
	Action         string                      `json:"action"`
	ExistingID     string                      `json:"existing_id,omitempty"`
	Card           memory.ScenarioCard         `json:"card"`
	Result         memory.ScenarioUpsertResult `json:"result"`
	DeletedSources int                         `json:"deleted_sources"`
}

// MemoryConsolidationPreviewHandler drafts a structured scenario card from a
// Memory Map group without saving anything.
//
//	POST /api/memories/consolidate/preview
func MemoryConsolidationPreviewHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}

	var req MemoryConsolidationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TargetScope == "" {
		req.TargetScope = "personal"
	}
	if err := validateConsolidationScope(r, pu, req.TargetScope); err != nil {
		respondError(w, http.StatusForbidden, err.Error())
		return
	}

	memories, err := collectVisibleMemories(r, svc, pu, 2000)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	selected := selectConsolidationMemories(memories, req.Key, req.MemoryIDs)
	if len(selected) == 0 {
		respondError(w, http.StatusNotFound, "no memories matched the requested group")
		return
	}

	card := memory.DraftScenarioCardFromMemories(req.Key, req.TargetScope, selected)
	respondJSON(w, http.StatusOK, MemoryConsolidationPreviewResponse{
		Card:    card,
		Content: memory.RenderScenarioCard(card),
		Sources: card.SourceMemoryIDs,
	})
}

// MemoryConsolidationApplyHandler saves or merges a scenario card into the
// requested target scope. Raw source memories referenced by the card are deleted
// after a successful upsert; the card is the durable memory.
//
//	POST /api/memories/consolidate/apply
func MemoryConsolidationApplyHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	pu := RequireAuth(w, r)
	if pu == nil {
		return
	}

	var req MemoryConsolidationApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TargetScope == "" {
		req.TargetScope = req.Card.Scope
	}
	if req.TargetScope == "" {
		req.TargetScope = "personal"
	}
	if err := validateConsolidationScope(r, pu, req.TargetScope); err != nil {
		respondError(w, http.StatusForbidden, err.Error())
		return
	}
	if req.Card.CanonicalKey == "" || len(req.Card.RecommendedRecipe) == 0 {
		respondError(w, http.StatusBadRequest, "card canonical_key and recommended_recipe are required")
		return
	}
	if !memory.HasUsableScenarioRecipe(req.Card) {
		deletedSources := deleteConsolidatedSources(r, svc, pu, req.Card.SourceMemoryIDs)
		respondJSON(w, http.StatusOK, MemoryConsolidationApplyResponse{
			Applied:        true,
			Scope:          req.TargetScope,
			Action:         "discarded",
			Card:           req.Card,
			DeletedSources: deletedSources,
		})
		return
	}

	memStore, err := resolveMemoryStoreForScope(r, svc, pu, req.TargetScope)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Card.Scope = req.TargetScope
	result, err := memory.UpsertScenarioCard(r.Context(), memStore, req.Card)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save scenario card: %v", err))
		return
	}
	deletedSources := deleteConsolidatedSources(r, svc, pu, req.Card.SourceMemoryIDs)

	respondJSON(w, http.StatusOK, MemoryConsolidationApplyResponse{
		Applied:        true,
		Scope:          req.TargetScope,
		Action:         result.Action,
		ExistingID:     result.ExistingID,
		Card:           req.Card,
		Result:         result,
		DeletedSources: deletedSources,
	})
}

func collectVisibleMemories(r *http.Request, svc *store.Services, pu *PlatformUser, limit int) ([]store.MemorySearchResult, error) {
	if svc.TenantRouter == nil {
		return nil, fmt.Errorf("tenant router not available")
	}
	orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve org store")
	}

	var memories []store.MemorySearchResult
	if personalMem := orgStore.ForUser(pu.ID).Memories(); personalMem != nil {
		results, err := personalMem.List(r.Context(), "", limit, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to list personal memories: %w", err)
		}
		memories = append(memories, ensureMemoryScope(results, string(store.MemoryScopePersonal))...)
	}
	if svc.Memory != nil {
		results, err := svc.Memory.List(r.Context(), "", limit, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to list team memories: %w", err)
		}
		memories = append(memories, ensureMemoryScope(results, string(store.MemoryScopeTeam))...)
	}
	if orgMem := orgStore.OrgMemories(); orgMem != nil {
		results, err := orgMem.List(r.Context(), "", limit, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to list org memories: %w", err)
		}
		memories = append(memories, ensureMemoryScope(results, string(store.MemoryScopeOrg))...)
	}
	return memories, nil
}

func selectConsolidationMemories(memories []store.MemorySearchResult, key string, ids []string) []store.MemorySearchResult {
	idSet := make(map[string]bool)
	for _, id := range ids {
		idSet[id] = true
	}
	var selected []store.MemorySearchResult
	for _, candidate := range memories {
		if len(idSet) > 0 {
			if idSet[candidate.ID] {
				selected = append(selected, candidate)
			}
			continue
		}
		if key != "" && canonicalMemoryTopic(candidate) == key {
			selected = append(selected, candidate)
		}
	}
	return selected
}

func validateConsolidationScope(r *http.Request, pu *PlatformUser, scope string) error {
	switch scope {
	case "personal":
		return nil
	case "team":
		return nil
	case "org":
		if !CanManageOrg(pu) {
			return fmt.Errorf("admin role required to save org scenario cards")
		}
		return nil
	default:
		return fmt.Errorf("invalid target_scope: %s", scope)
	}
}
