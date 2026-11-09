package api

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store/entstore"
	"net/http"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type botModelAction uint8

const (
	botModelRead botModelAction = iota
	botModelConfigure
)

type botModelPinStore interface {
	SessionPin(context.Context, string) (*store.SessionPin, error)
	SetSessionPin(context.Context, string, string, string) error
}

// handleBotHomeModel is deliberately separate from generic Chat ownership and
// runtime access. The viewer authorizes the operation; the stored owner and scope
// select the pin. No viewer default or process-global LLM is used by a Bot.
func handleBotHomeModel(w http.ResponseWriter, r *http.Request, sessionID string, action botModelAction) bool {
	svc := store.FromRequest(r)
	meta, err := botSessionMetadata(r.Context(), svc, sessionID)
	if err != nil {
		respondError(w, 404, "Session not found")
		return true
	}
	if meta == nil {
		return false
	}
	deny := func() bool { respondError(w, 404, "Session not found"); return true }
	p, ok := execution.PrincipalFromContext(r.Context())
	tc := store.TenantContextFrom(r.Context())
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser || tc == nil || tc.UserID != p.Subject || tc.OrgSlug != p.OrgSlug || tc.TeamSlug != p.TeamSlug || svc.TenantRouter == nil {
		return deny()
	}
	if meta.ID != sessionID || meta.Purpose != store.SessionPurposeBotHome || meta.Retention != store.SessionRetentionBot || meta.BotRunID != "" {
		return deny()
	}
	id, err := uuid.Parse(meta.BotID)
	if err != nil || id == uuid.Nil {
		return deny()
	}
	// Determine scope from the server-selected session store, never a body actor.
	scope := store.BotScopeTeam
	if svc.PersonalSessions != nil {
		m, e := svc.PersonalSessions.GetSessionMeta(r.Context(), sessionID)
		if e != nil {
			return deny()
		}
		if m != nil {
			scope = store.BotScopePersonal
		}
	}
	if values, exists := r.URL.Query()["scope"]; exists && (len(values) != 1 || values[0] != string(scope)) {
		return deny()
	}
	repo := svc.Bots
	if scope == store.BotScopePersonal {
		repo = svc.PersonalBots
	}
	if repo == nil {
		return deny()
	}
	b, err := repo.Get(r.Context(), id)
	if err != nil || b == nil || b.ID != id || b.Scope != scope || b.HomeSessionID.String() != sessionID || b.OwnerID.String() != meta.UserID {
		return deny()
	}
	org, err := svc.TenantRouter.ForOrg(p.OrgSlug)
	if err != nil || org == nil {
		return deny()
	}
	var pins botModelPinStore
	if scope == store.BotScopePersonal {
		if b.OwnerID.String() != p.Subject {
			return deny()
		}
		pins = org.ForUser(b.OwnerID.String())
	} else {
		pins = org.ForTeam(p.TeamSlug)
	}
	if pins == nil {
		return deny()
	}
	if action == botModelConfigure {
		// The central repository resolves live scope and owner/admin authority.
		// Do not reuse the GET-only session viewer guard for mutation.
		if _, err := entstore.AuthorizeBotModelConfiguration(r.Context(), repo, id); err != nil {
			if errors.Is(err, store.ErrBotForbidden) || errors.Is(err, store.ErrBotNotFound) {
				return deny()
			}
			botHTTPError(w, err)
			return true
		}
		var q PatchSessionModelRequest
		if !botDecodeRequest(w, r, &q) {
			return true
		}
		q.Provider, q.Model = strings.TrimSpace(q.Provider), strings.TrimSpace(q.Model)
		if (q.Provider == "") != (q.Model == "") {
			botHTTPError(w, store.ErrBotInvalid)
			return true
		}
		if err := pins.SetSessionPin(r.Context(), sessionID, q.Provider, q.Model); err != nil {
			botHTTPError(w, err)
			return true
		}
	}
	pin, err := pins.SessionPin(r.Context(), sessionID)
	if err != nil {
		botHTTPError(w, err)
		return true
	}
	result := SessionModelStatusResponse{AvailableProviders: []string{}}
	if pin != nil {
		result.PinnedProvider, result.PinnedModel = pin.Provider, pin.Model
	}
	var personalSettings store.PersonalSettingsStore
	if scope == store.BotScopePersonal {
		personalSettings = org.ForUser(b.OwnerID.String()).PersonalSettings()
	}
	selected, err := store.ResolveBotModelSelection(r.Context(), pin, svc.PlatformSettings, svc.OrgSettings, org.ForTeam(p.TeamSlug).Settings(), personalSettings)
	if err != nil && !errors.Is(err, store.ErrBotUnsupported) {
		botHTTPError(w, err)
		return true
	}
	result.EffectiveProvider, result.EffectiveModel = selected.Provider, selected.Model
	// Provider availability follows the execution owner's credential scope; never
	// inspect a team viewer's private defaults or secrets.
	resolved := provider.ResolveEffectiveConfig(r.Context(), svc.PlatformSettings, svc.OrgSettings, svc.Settings)
	ownerServices := &store.Services{Credentials: svc.Credentials}
	if scope == store.BotScopePersonal {
		ownerServices.PersonalCredentials = org.ForUser(b.OwnerID.String()).Credentials()
	}
	result.CredentialsAvailable = result.EffectiveProvider != "" && sessionProviderHasCredential(r.Context(), ownerServices, result.EffectiveProvider, resolved.Providers[result.EffectiveProvider])
	for name := range resolved.Providers {
		result.AvailableProviders = append(result.AvailableProviders, name)
	}
	sort.Strings(result.AvailableProviders)
	respondJSON(w, http.StatusOK, result)
	return true
}
