package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// A2AAgentListItem represents an A2A agent in listing responses.
type A2AAgentListItem struct {
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	CredentialName string            `json:"credential_name,omitempty"`
	AuthType       string            `json:"auth_type,omitempty"`
	Enabled        bool              `json:"enabled"`
	Headers        map[string]string `json:"headers,omitempty"`
	Timeout        string            `json:"timeout,omitempty"`
	Scope          string            `json:"scope"` // "platform", "org", or "team"
	HasCard        bool              `json:"has_card"`
	SkillCount     int               `json:"skill_count"`
	CachedSkills   json.RawMessage   `json:"cached_skills,omitempty"`
}

// A2AAgentsListResponse is the response for GET /api/a2a-agents.
type A2AAgentsListResponse struct {
	Agents      []A2AAgentListItem `json:"agents"`
	IsTeamAdmin bool               `json:"is_team_admin"`
	IsOrgAdmin  bool               `json:"is_org_admin"`
}

// A2AAgentCreateRequest is the request body for creating/updating an A2A agent.
type A2AAgentCreateRequest struct {
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	CredentialName string            `json:"credential_name,omitempty"`
	AuthType       string            `json:"auth_type,omitempty"` // bearer, api_key, oauth
	Enabled        *bool             `json:"enabled,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Timeout        string            `json:"timeout,omitempty"`
}

// ListA2AAgentsHandler handles GET /api/a2a-agents
//
// Query params:
//   - scope=team: return only team A2A agents
//   - scope=org: return only org A2A agents
//   - scope=platform: return only platform A2A agents
//   - (empty): return merged view (platform → org → team, lower tiers override by name)
func ListA2AAgentsHandler(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")

	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}

	items, err := listA2AAgentsPlatform(svc, scope)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to load A2A agents: "+err.Error())
		return
	}

	resp := A2AAgentsListResponse{
		Agents:      items,
		IsTeamAdmin: IsTeamAdmin(r),
		IsOrgAdmin:  CanManageOrg(GetPlatformUser(r)),
	}
	respondJSON(w, http.StatusOK, resp)
}

// GetA2AAgentHandler handles GET /api/a2a-agents/{name}
func GetA2AAgentHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	scope := r.URL.Query().Get("scope")

	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}

	agentStore := resolveA2AStoreForRead(svc, scope)
	if agentStore == nil {
		respondError(w, http.StatusServiceUnavailable, "A2A agent store not available for scope: "+scope)
		return
	}

	agent, err := agentStore.Get(r.Context(), name)
	if err != nil || agent == nil {
		respondError(w, http.StatusNotFound, "A2A agent not found: "+name)
		return
	}

	item := a2aAgentToListItem(agent, scope)
	respondJSON(w, http.StatusOK, item)
}

// CreateA2AAgentHandler handles POST /api/a2a-agents
func CreateA2AAgentHandler(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")

	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}

	targetStore := resolveA2AStoreForWrite(w, r, svc, scope)
	if targetStore == nil {
		return // auth error already written
	}

	var req A2AAgentCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "Name is required")
		return
	}
	if req.URL == "" {
		respondError(w, http.StatusBadRequest, "URL is required")
		return
	}
	// Validate URL format
	if _, err := url.ParseRequestURI(req.URL); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid URL format: "+err.Error())
		return
	}
	if req.AuthType == "" {
		req.AuthType = "bearer"
	}

	// Get user ID for created_by
	createdBy := ""
	if user := GetPlatformUser(r); user != nil {
		createdBy = user.ID
	}

	agent := &store.A2AAgent{
		Name:           req.Name,
		URL:            req.URL,
		CredentialName: req.CredentialName,
		AuthType:       req.AuthType,
		Enabled:        req.Enabled,
		Headers:        req.Headers,
		Timeout:        req.Timeout,
		CreatedBy:      createdBy,
	}

	if err := targetStore.Save(r.Context(), agent); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save A2A agent: "+err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, map[string]string{"status": "ok", "name": req.Name})
}

// UpdateA2AAgentHandler handles PUT /api/a2a-agents/{name}
func UpdateA2AAgentHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	scope := r.URL.Query().Get("scope")

	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}

	targetStore := resolveA2AStoreForWrite(w, r, svc, scope)
	if targetStore == nil {
		return // auth error already written
	}

	var req A2AAgentCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	// Use URL name for identity
	agentName := name
	if req.Name != "" {
		agentName = req.Name
	}

	if req.URL != "" {
		if _, err := url.ParseRequestURI(req.URL); err != nil {
			respondError(w, http.StatusBadRequest, "Invalid URL format: "+err.Error())
			return
		}
	}

	if req.AuthType == "" {
		req.AuthType = "bearer"
	}

	// Get user ID for created_by (upsert will keep original on conflict)
	createdBy := ""
	if user := GetPlatformUser(r); user != nil {
		createdBy = user.ID
	}

	agent := &store.A2AAgent{
		Name:           agentName,
		URL:            req.URL,
		CredentialName: req.CredentialName,
		AuthType:       req.AuthType,
		Enabled:        req.Enabled,
		Headers:        req.Headers,
		Timeout:        req.Timeout,
		CreatedBy:      createdBy,
	}

	if err := targetStore.Save(r.Context(), agent); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to update A2A agent: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "name": agentName})
}

// DeleteA2AAgentHandler handles DELETE /api/a2a-agents/{name}
func DeleteA2AAgentHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	scope := r.URL.Query().Get("scope")

	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}

	targetStore := resolveA2AStoreForWrite(w, r, svc, scope)
	if targetStore == nil {
		return // auth error already written
	}

	if err := targetStore.Delete(r.Context(), name); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to delete A2A agent: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ToggleA2AAgentHandler handles PATCH /api/a2a-agents/{name}
// Toggles the enabled state of an A2A agent.
func ToggleA2AAgentHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	scope := r.URL.Query().Get("scope")

	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}

	targetStore := resolveA2AStoreForWrite(w, r, svc, scope)
	if targetStore == nil {
		return // auth error already written
	}

	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if body.Enabled == nil {
		respondError(w, http.StatusBadRequest, "enabled field is required")
		return
	}

	// Load existing, update enabled, save
	existing, err := targetStore.Get(r.Context(), name)
	if err != nil || existing == nil {
		respondError(w, http.StatusNotFound, "A2A agent not found: "+name)
		return
	}

	existing.Enabled = body.Enabled
	if err := targetStore.Save(r.Context(), existing); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to toggle A2A agent: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "enabled": *body.Enabled})
}

// RefreshA2AAgentHandler handles POST /api/a2a-agents/{name}/refresh
// Fetches the agent card from the remote agent and caches it.
func RefreshA2AAgentHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	scope := r.URL.Query().Get("scope")

	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}

	targetStore := resolveA2AStoreForWrite(w, r, svc, scope)
	if targetStore == nil {
		return // auth error already written
	}

	existing, err := targetStore.Get(r.Context(), name)
	if err != nil || existing == nil {
		respondError(w, http.StatusNotFound, "A2A agent not found: "+name)
		return
	}

	// Fetch agent card from remote agent
	cardURL := resolveAgentCardURL(existing.URL)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, cardURL, nil) //nolint:gosec // URL is user-configured
	if err != nil {
		respondError(w, http.StatusBadGateway, "Invalid agent card URL: "+err.Error())
		return
	}

	// Resolve credential-based auth header
	resolver := credentialResolverForRequest(r)
	if existing.CredentialName != "" && resolver != nil {
		headerKey, headerValue, credErr := resolver.Resolve(existing.CredentialName)
		if credErr != nil {
			respondError(w, http.StatusBadGateway, "Credential resolution failed: "+credErr.Error())
			return
		}
		if headerKey != "" && headerValue != "" {
			req.Header.Set(headerKey, headerValue)
		}
	}

	// Apply custom headers if configured
	if existing.Headers != nil {
		for key, value := range existing.Headers {
			req.Header.Set(key, value)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		respondError(w, http.StatusBadGateway, "Failed to fetch agent card: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respondError(w, http.StatusBadGateway, "Agent card endpoint returned status: "+resp.Status)
		return
	}

	var cardData json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&cardData); err != nil {
		respondError(w, http.StatusBadGateway, "Invalid agent card JSON: "+err.Error())
		return
	}

	// Extract skills from the card for caching
	var card struct {
		Skills []json.RawMessage `json:"skills"`
	}
	var skillsData json.RawMessage
	if err := json.Unmarshal(cardData, &card); err == nil && len(card.Skills) > 0 {
		skillsData, _ = json.Marshal(card.Skills)
	}

	// Update cached card and skills
	if err := targetStore.UpdateCachedCard(r.Context(), name, cardData, skillsData); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to cache agent card: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"name":        name,
		"skill_count": len(card.Skills),
	})
}

// TestA2AAgentHandler handles POST /api/a2a-agents/{name}/test
// Sends a test message to verify connectivity to the remote agent.
func TestA2AAgentHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	scope := r.URL.Query().Get("scope")

	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}

	agentStore := resolveA2AStoreForRead(svc, scope)
	if agentStore == nil {
		respondError(w, http.StatusServiceUnavailable, "A2A agent store not available")
		return
	}

	existing, err := agentStore.Get(r.Context(), name)
	if err != nil || existing == nil {
		respondError(w, http.StatusNotFound, "A2A agent not found: "+name)
		return
	}

	// Test connectivity by fetching the agent card
	cardURL := resolveAgentCardURL(existing.URL)

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, cardURL, nil)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"status":  "error",
			"message": "Invalid URL: " + err.Error(),
		})
		return
	}

	// Resolve credential-based auth header
	resolver := credentialResolverForRequest(r)
	if existing.CredentialName != "" && resolver != nil {
		headerKey, headerValue, credErr := resolver.Resolve(existing.CredentialName)
		if credErr != nil {
			respondJSON(w, http.StatusOK, map[string]any{
				"status":  "error",
				"message": "Credential resolution failed: " + credErr.Error(),
			})
			return
		}
		if headerKey != "" && headerValue != "" {
			req.Header.Set(headerKey, headerValue)
		}
	}

	// Apply custom headers if configured
	if existing.Headers != nil {
		for key, value := range existing.Headers {
			req.Header.Set(key, value)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"status":  "error",
			"message": "Connection failed: " + err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respondJSON(w, http.StatusOK, map[string]any{
			"status":  "error",
			"message": "Agent card endpoint returned status: " + resp.Status,
		})
		return
	}

	var card struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Version     string `json:"version"`
		Skills      []any  `json:"skills"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"status":  "error",
			"message": "Invalid agent card response: " + err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"agent_name":  card.Name,
		"description": card.Description,
		"version":     card.Version,
		"skill_count": len(card.Skills),
	})
}

// resolveAgentCardURL returns the agent card URL for the given base URL.
// If the URL already ends with /.well-known/agent-card.json, it is used as-is.
// Otherwise, the well-known path is appended.
func resolveAgentCardURL(rawURL string) string {
	trimmed := strings.TrimRight(rawURL, "/")
	if strings.HasSuffix(trimmed, "/.well-known/agent-card.json") {
		return trimmed
	}
	return trimmed + "/.well-known/agent-card.json"
}

// --- Internal helpers ---

// resolveA2AStoreForRead returns the appropriate A2AAgentStore for a read operation.
func resolveA2AStoreForRead(svc *store.Services, scope string) store.A2AAgentStore {
	switch scope {
	case "platform":
		return svc.PlatformA2AAgents
	case "org":
		return svc.A2AAgents
	case "team":
		return svc.TeamA2AAgents
	default:
		// Default to team for reads
		if svc.TeamA2AAgents != nil {
			return svc.TeamA2AAgents
		}
		return svc.A2AAgents
	}
}

// resolveA2AStoreForWrite returns the appropriate A2AAgentStore for a write operation
// based on the requested scope. Returns nil and writes an error if auth fails.
func resolveA2AStoreForWrite(w http.ResponseWriter, r *http.Request, svc *store.Services, scope string) store.A2AAgentStore {
	switch scope {
	case "platform":
		if svc.PlatformA2AAgents == nil {
			respondError(w, http.StatusServiceUnavailable, "Platform A2A agent store not available")
			return nil
		}
		if RequirePlatformAdmin(w, r) == nil {
			return nil
		}
		return svc.PlatformA2AAgents
	case "team":
		if svc.TeamA2AAgents == nil {
			respondError(w, http.StatusServiceUnavailable, "Team A2A agent store not available")
			return nil
		}
		if !RequireTeamAdmin(w, r) {
			return nil
		}
		return svc.TeamA2AAgents
	case "org":
		if svc.A2AAgents == nil {
			respondError(w, http.StatusServiceUnavailable, "Org A2A agent store not available")
			return nil
		}
		user := GetPlatformUser(r)
		if user == nil {
			respondError(w, http.StatusUnauthorized, "Authentication required")
			return nil
		}
		if !CanManageOrg(user) {
			respondError(w, http.StatusForbidden, "Organization admin access required to manage org A2A agents")
			return nil
		}
		return svc.A2AAgents
	default:
		// No scope specified — default to team
		if svc.TeamA2AAgents == nil {
			respondError(w, http.StatusServiceUnavailable, "Team A2A agent store not available")
			return nil
		}
		if !RequireTeamAdmin(w, r) {
			return nil
		}
		return svc.TeamA2AAgents
	}
}

// listA2AAgentsPlatform loads A2A agents with three-tier merge: platform → org → team.
// Lower tiers override higher tiers by name.
func listA2AAgentsPlatform(svc *store.Services, scope string) ([]A2AAgentListItem, error) {
	switch scope {
	case "platform":
		return listA2AAgentsFromStore(svc.PlatformA2AAgents, "platform")
	case "team":
		return listA2AAgentsFromStore(svc.TeamA2AAgents, "team")
	case "org":
		return listA2AAgentsFromStore(svc.A2AAgents, "org")
	default:
		return listA2AAgentsMerged(svc)
	}
}

// listA2AAgentsFromStore lists A2A agents from a single store.
func listA2AAgentsFromStore(agentStore store.A2AAgentStore, scope string) ([]A2AAgentListItem, error) {
	if agentStore == nil {
		return []A2AAgentListItem{}, nil
	}
	agents, err := agentStore.List(context.TODO())
	if err != nil {
		return nil, err
	}
	items := make([]A2AAgentListItem, 0, len(agents))
	for _, a := range agents {
		item := a2aAgentToListItem(&a, scope)
		items = append(items, item)
	}
	sortA2AAgentItems(items)
	return items, nil
}

// listA2AAgentsMerged returns the merged view: platform → org → team (team overrides).
func listA2AAgentsMerged(svc *store.Services) ([]A2AAgentListItem, error) {
	merged := make(map[string]A2AAgentListItem)

	// Load platform agents first (lowest priority)
	if svc.PlatformA2AAgents != nil {
		agents, err := svc.PlatformA2AAgents.List(context.TODO())
		if err == nil {
			for _, a := range agents {
				merged[a.Name] = a2aAgentToListItem(&a, "platform")
			}
		}
	}

	// Org agents override platform
	if svc.A2AAgents != nil {
		agents, err := svc.A2AAgents.List(context.TODO())
		if err == nil {
			for _, a := range agents {
				merged[a.Name] = a2aAgentToListItem(&a, "org")
			}
		}
	}

	// Team agents override org
	if svc.TeamA2AAgents != nil {
		agents, err := svc.TeamA2AAgents.List(context.TODO())
		if err == nil {
			for _, a := range agents {
				merged[a.Name] = a2aAgentToListItem(&a, "team")
			}
		}
	}

	items := make([]A2AAgentListItem, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sortA2AAgentItems(items)
	return items, nil
}

func a2aAgentToListItem(agent *store.A2AAgent, scope string) A2AAgentListItem {
	enabled := true
	if agent.Enabled != nil {
		enabled = *agent.Enabled
	}

	skillCount := 0
	if agent.CachedSkills != nil {
		var skills []any
		if err := json.Unmarshal(agent.CachedSkills, &skills); err == nil {
			skillCount = len(skills)
		}
	}

	return A2AAgentListItem{
		Name:           agent.Name,
		URL:            agent.URL,
		CredentialName: agent.CredentialName,
		AuthType:       agent.AuthType,
		Enabled:        enabled,
		Headers:        agent.Headers,
		Timeout:        agent.Timeout,
		Scope:          scope,
		HasCard:        agent.CachedCard != nil && len(agent.CachedCard) > 0,
		SkillCount:     skillCount,
		CachedSkills:   agent.CachedSkills,
	}
}

func sortA2AAgentItems(items []A2AAgentListItem) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})
}
