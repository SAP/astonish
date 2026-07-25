package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"

	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// --- Credential API types ---

// credentialListItem represents a single credential in the list response.
type credentialListItem struct {
	Name     string               `json:"name"`
	Type     store.CredentialType `json:"type"`
	Scope    string               `json:"scope,omitempty"`    // "personal" or "team" (platform mode only)
	Shadowed bool                 `json:"shadowed,omitempty"` // true if personal overrides this team credential
}

// credentialListResponse is the response for GET /api/credentials.
type credentialListResponse struct {
	Credentials  []credentialListItem `json:"credentials"`
	Secrets      []secretListItem     `json:"secrets"`
	HasMasterKey bool                 `json:"has_master_key"`
	IsTeamAdmin  bool                 `json:"is_team_admin"` // true if user can manage team credentials (reveal, fork, edit)
}

// secretListItem represents a secret in the list response.
type secretListItem struct {
	Key   string `json:"key"`
	Scope string `json:"scope,omitempty"` // "personal" or "team" (platform mode only)
}

// credentialDetailResponse returns full credential fields (for reveal).
type credentialDetailResponse struct {
	Name                        string               `json:"name"`
	Type                        store.CredentialType `json:"type"`
	Scope                       string               `json:"scope,omitempty"`
	Header                      string               `json:"header,omitempty"`
	Value                       string               `json:"value,omitempty"`
	Token                       string               `json:"token,omitempty"`
	Username                    string               `json:"username,omitempty"`
	Password                    string               `json:"password,omitempty"`
	AuthURL                     string               `json:"auth_url,omitempty"`
	ClientID                    string               `json:"client_id,omitempty"`
	ClientSecret                string               `json:"client_secret,omitempty"`
	Scope_                      string               `json:"oauth_scope,omitempty"` // renamed to avoid JSON clash with "scope"
	TokenURL                    string               `json:"token_url,omitempty"`
	AccessToken                 string               `json:"access_token,omitempty"`
	RefreshToken                string               `json:"refresh_token,omitempty"`
	TokenExpiry                 string               `json:"token_expiry,omitempty"`
	UserDomain                  string               `json:"user_domain,omitempty"`
	ProjectID                   string               `json:"project_id,omitempty"`
	ProjectName                 string               `json:"project_name,omitempty"`
	ProjectDomain               string               `json:"project_domain,omitempty"`
	ApplicationCredentialID     string               `json:"application_credential_id,omitempty"`
	ApplicationCredentialSecret string               `json:"application_credential_secret,omitempty"`
	Content                     string               `json:"content,omitempty"`
	ContentType                 string               `json:"content_type,omitempty"`
}

// credentialSaveRequest is the body for POST /api/credentials.
type credentialSaveRequest struct {
	Name       string           `json:"name"`
	Credential store.Credential `json:"credential"`
}

// credentialPublishRequest is the body for POST /api/credentials/publish.
type credentialPublishRequest struct {
	Name string `json:"name"`
}

// secretSaveRequest is the body for PUT /api/secrets/{key}.
type secretSaveRequest struct {
	Value string `json:"value"`
}

// masterKeyRequest is the body for POST /api/credentials/master-key.
type masterKeyRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// verifyMasterKeyRequest is the body for POST /api/credentials/verify-master-key.
type verifyMasterKeyRequest struct {
	Password string `json:"password"`
}

// --- Helpers ---

// requireMasterKey checks the X-Master-Key header against the personal-mode
// credential store. Returns true if access is granted (no master key set, or
// valid key provided, or platform mode where master keys don't apply).
// Returns false and writes an HTTP error if access is denied.
func requireMasterKey(w http.ResponseWriter, r *http.Request) bool {
	// In platform mode, authentication is handled by JWT — no master key needed.
	if isPlatformMode(r) {
		return true
	}
	cs := getAPICredentialStore()
	if cs == nil || !cs.HasMasterKey() {
		return true
	}
	masterKey := r.Header.Get("X-Master-Key")
	if masterKey == "" {
		respondError(w, http.StatusForbidden, `{"error":"master_key_required"}`)
		return false
	}
	if !cs.VerifyMasterKey(masterKey) {
		respondError(w, http.StatusForbidden, `{"error":"invalid_master_key"}`)
		return false
	}
	return true
}

// hasMasterKey returns whether a master key is set. Only applies to personal mode.
func hasMasterKey() bool {
	cs := getAPICredentialStore()
	return cs != nil && cs.HasMasterKey()
}

// --- Handlers ---

// ListCredentialsHandler returns all credential names/types and secret keys.
// GET /api/credentials?scope=personal|team|<empty>
//
// In platform mode with no scope filter, returns merged results with scope labels.
// With scope=personal, returns only personal credentials.
// With scope=team, returns only team credentials.
func ListCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")

	if isPlatformMode(r) && scope == "" {
		// Return merged list with scope labels
		listCredentialsMerged(w, r)
		return
	}

	cs := effectiveCredentialStoreScoped(r, scope)
	if cs == nil {
		respondError(w, http.StatusServiceUnavailable, "Credential store not available")
		return
	}

	if err := cs.Reload(r.Context()); err != nil {
		slog.Warn("failed to reload credential store", "error", err)
	}

	creds := cs.List(r.Context())
	secrets := cs.ListSecrets(r.Context())
	sort.Strings(secrets)

	items := make([]credentialListItem, 0, len(creds))
	for name, credType := range creds {
		items = append(items, credentialListItem{Name: name, Type: credType, Scope: scope})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	secretItems := make([]secretListItem, 0, len(secrets))
	for _, k := range secrets {
		secretItems = append(secretItems, secretListItem{Key: k, Scope: scope})
	}

	resp := credentialListResponse{
		Credentials:  items,
		Secrets:      secretItems,
		HasMasterKey: hasMasterKey(),
		IsTeamAdmin:  IsTeamAdmin(r),
	}

	respondJSON(w, http.StatusOK, resp)
}

// listCredentialsMerged returns credentials from both personal and team stores
// with scope labels and shadowing indicators.
func listCredentialsMerged(w http.ResponseWriter, r *http.Request) {
	svc := store.FromRequest(r)
	if svc == nil {
		respondError(w, http.StatusServiceUnavailable, "Credential store not available")
		return
	}

	var items []credentialListItem
	var secretItems []secretListItem
	personalCreds := make(map[string]bool)
	personalSecrets := make(map[string]bool)

	// Personal credentials
	if svc.PersonalCredentials != nil {
		_ = svc.PersonalCredentials.Reload(r.Context())
		for name, credType := range svc.PersonalCredentials.List(r.Context()) {
			items = append(items, credentialListItem{Name: name, Type: credType, Scope: "personal"})
			personalCreds[name] = true
		}
		for _, key := range svc.PersonalCredentials.ListSecrets(r.Context()) {
			secretItems = append(secretItems, secretListItem{Key: key, Scope: "personal"})
			personalSecrets[key] = true
		}
	}

	// Team credentials
	if svc.Credentials != nil {
		_ = svc.Credentials.Reload(r.Context())
		for name, credType := range svc.Credentials.List(r.Context()) {
			items = append(items, credentialListItem{
				Name:     name,
				Type:     credType,
				Scope:    "team",
				Shadowed: personalCreds[name],
			})
		}
		for _, key := range svc.Credentials.ListSecrets(r.Context()) {
			secretItems = append(secretItems, secretListItem{Key: key, Scope: "team"})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			// Personal before team for same name
			return items[i].Scope == "personal"
		}
		return items[i].Name < items[j].Name
	})

	resp := credentialListResponse{
		Credentials:  items,
		Secrets:      secretItems,
		HasMasterKey: hasMasterKey(),
		IsTeamAdmin:  IsTeamAdmin(r),
	}
	respondJSON(w, http.StatusOK, resp)
}

// GetCredentialHandler reveals a credential's full values.
// GET /api/credentials/{name}?scope=personal|team
//
// For team credentials in platform mode, only admins can see values.
// Regular members get a 403.
func GetCredentialHandler(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")

	// Team credential values: admin-only
	if scope == "team" && isPlatformMode(r) {
		if !RequireTeamAdmin(w, r) {
			return
		}
	}

	cs := effectiveCredentialStoreScoped(r, scope)
	if cs == nil {
		respondError(w, http.StatusServiceUnavailable, "Credential store not available")
		return
	}

	if !requireMasterKey(w, r) {
		return
	}

	name := mux.Vars(r)["name"]
	if err := cs.Reload(r.Context()); err != nil {
		slog.Warn("failed to reload credential store", "error", err)
	}

	cred := cs.Get(r.Context(), name)
	if cred == nil {
		respondError(w, http.StatusNotFound, "Credential not found")
		return
	}

	resp := credentialDetailResponse{
		Name:                        name,
		Type:                        cred.Type,
		Scope:                       scope,
		Header:                      cred.Header,
		Value:                       cred.Value,
		Token:                       cred.Token,
		Username:                    cred.Username,
		Password:                    cred.Password,
		AuthURL:                     cred.AuthURL,
		ClientID:                    cred.ClientID,
		ClientSecret:                cred.ClientSecret,
		Scope_:                      cred.Scope,
		TokenURL:                    cred.TokenURL,
		AccessToken:                 cred.AccessToken,
		RefreshToken:                cred.RefreshToken,
		TokenExpiry:                 cred.TokenExpiry,
		UserDomain:                  cred.UserDomain,
		ProjectID:                   cred.ProjectID,
		ProjectName:                 cred.ProjectName,
		ProjectDomain:               cred.ProjectDomain,
		ApplicationCredentialID:     cred.ApplicationCredentialID,
		ApplicationCredentialSecret: cred.ApplicationCredentialSecret,
		Content:                     cred.Content,
		ContentType:                 cred.ContentType,
	}

	respondJSON(w, http.StatusOK, resp)
}

// SaveCredentialHandler creates or updates a named credential.
// POST /api/credentials?scope=personal|team
//
// For team scope in platform mode, requires admin access.
func SaveCredentialHandler(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")

	// Team credential writes: admin-only
	if scope == "team" && isPlatformMode(r) {
		if !RequireTeamAdmin(w, r) {
			return
		}
	}

	cs := effectiveCredentialStoreScoped(r, scope)
	if cs == nil {
		respondError(w, http.StatusServiceUnavailable, "Credential store not available")
		return
	}

	var req credentialSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "Credential name is required")
		return
	}

	if err := cs.Set(r.Context(), req.Name, &req.Credential); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save credential: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "name": req.Name})
}

// DeleteCredentialHandler removes a credential or flat secret.
// DELETE /api/credentials/{name}?scope=personal|team
//
// For team scope in platform mode, requires admin access.
func DeleteCredentialHandler(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")

	// Team credential deletes: admin-only
	if scope == "team" && isPlatformMode(r) {
		if !RequireTeamAdmin(w, r) {
			return
		}
	}

	cs := effectiveCredentialStoreScoped(r, scope)
	if cs == nil {
		respondError(w, http.StatusServiceUnavailable, "Credential store not available")
		return
	}

	name := mux.Vars(r)["name"]

	// Try HTTP credential first
	if cs.Get(r.Context(), name) != nil {
		if err := cs.Remove(r.Context(), name); err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to remove credential: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "removed": name})
		return
	}

	// Try flat secret
	if cs.GetSecret(r.Context(), name) != "" {
		if err := cs.RemoveSecret(r.Context(), name); err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to remove secret: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "removed": name})
		return
	}

	respondError(w, http.StatusNotFound, "Credential or secret not found")
}

// PublishCredentialHandler copies a personal credential to the team store.
// POST /api/credentials/publish
//
// Requires team admin access. Copy semantics: both stores have their own copy afterward.
func PublishCredentialHandler(w http.ResponseWriter, r *http.Request) {
	if !isPlatformMode(r) {
		slog.Warn("publish credential: not in platform mode")
		respondError(w, http.StatusNotFound, "Publish is only available in platform mode")
		return
	}
	if !RequireTeamAdmin(w, r) {
		return
	}

	var req credentialPublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "Credential name is required")
		return
	}

	personalStore := effectivePersonalCredentialStore(r)
	teamStore := effectiveTeamCredentialStore(r)
	if personalStore == nil || teamStore == nil {
		slog.Warn("publish credential: stores not available", "personal_nil", personalStore == nil, "team_nil", teamStore == nil)
		respondError(w, http.StatusServiceUnavailable, "Credential stores not available")
		return
	}

	cred := personalStore.Get(r.Context(), req.Name)
	if cred == nil {
		slog.Warn("publish credential: personal credential not found", "name", req.Name)
		respondError(w, http.StatusNotFound, "Personal credential not found")
		return
	}

	if err := teamStore.Set(r.Context(), req.Name, cred); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to publish credential: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "published": req.Name})
}

// ForkCredentialHandler copies a team credential to the user's personal store.
// POST /api/credentials/fork
//
// Requires team admin access. Forking copies secret values, so non-admins must not be able to use
// this as a bypass to reveal team credentials.
func ForkCredentialHandler(w http.ResponseWriter, r *http.Request) {
	if !isPlatformMode(r) {
		respondError(w, http.StatusNotFound, "Fork is only available in platform mode")
		return
	}
	if !RequireTeamAdmin(w, r) {
		return
	}

	var req credentialPublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "Credential name is required")
		return
	}

	teamStore := effectiveTeamCredentialStore(r)
	personalStore := effectivePersonalCredentialStore(r)
	if teamStore == nil || personalStore == nil {
		respondError(w, http.StatusServiceUnavailable, "Credential stores not available")
		return
	}

	cred := teamStore.Get(r.Context(), req.Name)
	if cred == nil {
		respondError(w, http.StatusNotFound, "Team credential not found")
		return
	}

	if err := personalStore.Set(r.Context(), req.Name, cred); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to fork credential: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "forked": req.Name})
}

// GetSecretHandler reveals a flat secret value.
// GET /api/secrets/{key}?scope=personal|team
func GetSecretHandler(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")

	// Team secrets: admin-only to reveal
	if scope == "team" && isPlatformMode(r) {
		if !RequireTeamAdmin(w, r) {
			return
		}
	}

	cs := effectiveCredentialStoreScoped(r, scope)
	if cs == nil {
		respondError(w, http.StatusServiceUnavailable, "Credential store not available")
		return
	}

	if !requireMasterKey(w, r) {
		return
	}

	key := mux.Vars(r)["key"]
	if err := cs.Reload(r.Context()); err != nil {
		slog.Warn("failed to reload credential store", "error", err)
	}

	value := cs.GetSecret(r.Context(), key)
	if value == "" {
		respondError(w, http.StatusNotFound, "Secret not found")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"key": key, "value": value})
}

// SaveSecretHandler creates or updates a flat secret.
// PUT /api/secrets/{key}?scope=personal|team
func SaveSecretHandler(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")

	// Team secrets: admin-only
	if scope == "team" && isPlatformMode(r) {
		if !RequireTeamAdmin(w, r) {
			return
		}
	}

	cs := effectiveCredentialStoreScoped(r, scope)
	if cs == nil {
		respondError(w, http.StatusServiceUnavailable, "Credential store not available")
		return
	}

	key := mux.Vars(r)["key"]

	var req secretSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if req.Value == "" {
		respondError(w, http.StatusBadRequest, "Secret value is required")
		return
	}

	if err := cs.SetSecret(r.Context(), key, req.Value); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save secret: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "key": key})
}

// SetMasterKeyHandler sets, changes, or removes the master key.
// POST /api/credentials/master-key
//
// Master keys are a personal-mode concept. In platform mode, authentication
// is handled by JWT tokens and this endpoint returns 404.
func SetMasterKeyHandler(w http.ResponseWriter, r *http.Request) {
	if isPlatformMode(r) {
		respondError(w, http.StatusNotFound, "Master keys are not used in platform mode")
		return
	}

	cs := getAPICredentialStore()
	if cs == nil {
		respondError(w, http.StatusServiceUnavailable, "Credential store not available")
		return
	}

	var req masterKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	// If a master key already exists, verify the current password
	if cs.HasMasterKey() {
		if !cs.VerifyMasterKey(req.Current) {
			respondError(w, http.StatusForbidden, `{"error":"invalid_current_key"}`)
			return
		}
	}

	if err := cs.SetMasterKey(req.New); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to set master key: "+err.Error())
		return
	}

	action := "set"
	if req.New == "" {
		action = "removed"
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "action": action})
}

// VerifyMasterKeyHandler checks if a password matches the master key.
// POST /api/credentials/verify-master-key
//
// Master keys are a personal-mode concept. In platform mode, this endpoint
// returns 404.
func VerifyMasterKeyHandler(w http.ResponseWriter, r *http.Request) {
	if isPlatformMode(r) {
		respondError(w, http.StatusNotFound, "Master keys are not used in platform mode")
		return
	}

	cs := getAPICredentialStore()
	if cs == nil {
		respondError(w, http.StatusServiceUnavailable, "Credential store not available")
		return
	}

	var req verifyMasterKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	valid := cs.VerifyMasterKey(req.Password)

	respondJSON(w, http.StatusOK, map[string]bool{"valid": valid})
}
