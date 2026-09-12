package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/SAP/astonish/pkg/oauthserver"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// oauthClientBackend is the narrow platform boundary needed to prove the
// authenticated user can bind an OAuth client to an organization and team.
type oauthClientBackend interface {
	Organizations() store.OrganizationStore
	ForOrg(string) (store.OrgDataStore, error)
}

type oauthClientResponse struct {
	ID           string   `json:"id"`
	OrgID        string   `json:"org_id"`
	TeamID       string   `json:"team_id"`
	ClientID     string   `json:"client_id"`
	Name         string   `json:"name"`
	ClientType   string   `json:"client_type"`
	RedirectURIs []string `json:"redirect_uris"`
	GrantTypes   []string `json:"grant_types"`
	Resources    []string `json:"resources"`
	Scopes       []string `json:"scopes"`
	Active       bool     `json:"active"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

type oauthClientRequest struct {
	Name         string   `json:"name"`
	ClientType   string   `json:"client_type"`
	OrgID        string   `json:"org_id"`
	TeamID       string   `json:"team_id"`
	RedirectURIs []string `json:"redirect_uris"`
	GrantTypes   []string `json:"grant_types"`
	Resources    []string `json:"resources"`
	Scopes       []string `json:"scopes"`
	Active       bool     `json:"active"`
	RotateSecret bool     `json:"rotate_secret"`
}

type oauthClientContext struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug,omitempty"`
	Teams []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"teams"`
}

// RegisterOAuthAdminRoutes mounts authenticated, personal OAuth-client
// management endpoints. The OAuth protocol itself remains platform scoped.
func RegisterOAuthAdminRoutes(router *mux.Router, server *oauthserver.Server, backend oauthClientBackend) {
	if server == nil || backend == nil {
		return
	}
	currentUser := func(w http.ResponseWriter, r *http.Request) *PlatformUser {
		user := PlatformUserFromContext(r.Context())
		if user == nil || user.ID == "" {
			respondError(w, http.StatusUnauthorized, "authentication required")
			return nil
		}
		return user
	}

	router.HandleFunc("/api/oauth/discovery", func(w http.ResponseWriter, r *http.Request) {
		if currentUser(w, r) == nil {
			return
		}
		respondJSON(w, http.StatusOK, server.Discovery())
	}).Methods(http.MethodGet)

	router.HandleFunc("/api/oauth/contexts", func(w http.ResponseWriter, r *http.Request) {
		user := currentUser(w, r)
		if user == nil {
			return
		}
		contexts, err := accessibleOAuthContexts(r, backend, user.ID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to list accessible OAuth contexts")
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"organizations": contexts})
	}).Methods(http.MethodGet)

	router.HandleFunc("/api/oauth/clients", func(w http.ResponseWriter, r *http.Request) {
		user := currentUser(w, r)
		if user == nil {
			return
		}
		clients, err := server.ListClientsForOwner(r.Context(), user.ID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to list OAuth clients")
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"clients": oauthClientViews(clients)})
	}).Methods(http.MethodGet)

	router.HandleFunc("/api/oauth/clients", func(w http.ResponseWriter, r *http.Request) {
		user := currentUser(w, r)
		if user == nil {
			return
		}
		var req oauthClientRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid OAuth client request")
			return
		}
		if !canUseOAuthContext(r, backend, user.ID, req.OrgID, req.TeamID) {
			respondError(w, http.StatusForbidden, "organization and team are not accessible to the current user")
			return
		}
		client, secret, err := server.CreateClient(r.Context(), oauthClientInput(req, user.ID))
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusCreated, map[string]any{"client": oauthClientView(client), "client_secret": secret})
	}).Methods(http.MethodPost)

	router.HandleFunc("/api/oauth/clients/{clientID}", func(w http.ResponseWriter, r *http.Request) {
		user := currentUser(w, r)
		if user == nil {
			return
		}
		client, err := ownedOAuthClient(r, server, user.ID, mux.Vars(r)["clientID"])
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to resolve OAuth client")
			return
		}
		if client == nil {
			respondError(w, http.StatusNotFound, "OAuth client not found")
			return
		}
		var req oauthClientRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid OAuth client request")
			return
		}
		// Owner and tenant binding are immutable after creation.
		clientInput := oauthClientInput(req, user.ID)
		clientInput.OrgID, clientInput.TeamID = client.OrgID, client.TeamID
		updated, secret, err := server.UpdateClient(r.Context(), client.ClientID, clientInput, req.RotateSecret)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"client": oauthClientView(updated), "client_secret": secret})
	}).Methods(http.MethodPatch)

	router.HandleFunc("/api/oauth/clients/{clientID}", func(w http.ResponseWriter, r *http.Request) {
		user := currentUser(w, r)
		if user == nil {
			return
		}
		client, err := ownedOAuthClient(r, server, user.ID, mux.Vars(r)["clientID"])
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to resolve OAuth client")
			return
		}
		if client == nil {
			respondError(w, http.StatusNotFound, "OAuth client not found")
			return
		}
		if err := server.DeleteClient(r.Context(), client.ClientID); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to delete OAuth client")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}).Methods(http.MethodDelete)
}

func accessibleOAuthContexts(r *http.Request, backend oauthClientBackend, userID string) ([]oauthClientContext, error) {
	memberships, err := backend.Organizations().GetUserOrgs(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	contexts := make([]oauthClientContext, 0, len(memberships))
	for _, membership := range memberships {
		org, err := backend.Organizations().GetByID(r.Context(), membership.OrgID)
		if err != nil || org == nil {
			continue
		}
		orgStore, err := backend.ForOrg(org.Slug)
		if err != nil {
			continue
		}
		teams, err := orgStore.Teams().ListTeamsForUser(r.Context(), userID)
		if err != nil {
			continue
		}
		context := oauthClientContext{ID: org.ID, Name: org.Name, Slug: org.Slug}
		for _, team := range teams {
			context.Teams = append(context.Teams, struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}{ID: team.ID, Name: team.Name})
		}
		contexts = append(contexts, context)
	}
	return contexts, nil
}

func canAdministerOAuthClients(role string) bool {
	return role == "owner" || role == "admin"
}

func canUseOAuthContext(r *http.Request, backend oauthClientBackend, userID, orgID, teamID string) bool {
	if orgID == "" || teamID == "" {
		return false
	}
	org, err := backend.Organizations().GetByID(r.Context(), orgID)
	if err != nil || org == nil {
		return false
	}
	role, err := backend.Organizations().GetMemberRole(r.Context(), userID, orgID)
	if err != nil || !canAdministerOAuthClients(role) {
		return false
	}
	orgStore, err := backend.ForOrg(org.Slug)
	if err != nil {
		return false
	}
	team, err := orgStore.Teams().GetTeam(r.Context(), teamID)
	if err != nil || team == nil {
		return false
	}
	member, err := orgStore.Teams().IsTeamMember(r.Context(), userID, team.Slug)
	return err == nil && member
}

func ownedOAuthClient(r *http.Request, server *oauthserver.Server, ownerUserID, clientID string) (*store.OAuthClient, error) {
	clients, err := server.ListClientsForOwner(r.Context(), ownerUserID)
	if err != nil {
		return nil, err
	}
	for i := range clients {
		if clients[i].ClientID == clientID {
			return &clients[i], nil
		}
	}
	return nil, nil
}

func oauthClientInput(req oauthClientRequest, ownerUserID string) oauthserver.ClientInput {
	return oauthserver.ClientInput{Name: req.Name, ClientType: req.ClientType, OwnerUserID: ownerUserID, OrgID: req.OrgID, TeamID: req.TeamID, RedirectURIs: req.RedirectURIs, GrantTypes: req.GrantTypes, Resources: req.Resources, Scopes: req.Scopes, Active: req.Active}
}

func oauthClientViews(clients []store.OAuthClient) []oauthClientResponse {
	out := make([]oauthClientResponse, 0, len(clients))
	for _, client := range clients {
		out = append(out, oauthClientView(client))
	}
	return out
}

func oauthClientView(client store.OAuthClient) oauthClientResponse {
	return oauthClientResponse{ID: client.ID, OrgID: client.OrgID, TeamID: client.TeamID, ClientID: client.ClientID, Name: client.Name, ClientType: client.ClientType, RedirectURIs: client.RedirectURIs, GrantTypes: client.GrantTypes, Resources: client.Resources, Scopes: client.Scopes, Active: client.Active, CreatedAt: client.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: client.UpdatedAt.UTC().Format(time.RFC3339)}
}
