package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// botSessionMetadata identifies Bot linkage without granting read or execution
// access. Callers must fail closed on lookup errors, not use CreatedBy fallback.
func botSessionMetadata(ctx context.Context, svc *store.Services, id string) (*store.SessionMeta, error) {
	if svc == nil {
		return nil, nil
	}
	for _, ss := range []store.SessionStore{svc.PersonalSessions, svc.Sessions} {
		if ss == nil {
			continue
		}
		meta, err := ss.GetSessionMeta(ctx, id)
		if err != nil {
			return nil, err
		}
		if meta != nil {
			if meta.HasBotLinkage() {
				return meta, nil
			}
			return nil, nil
		}
	}
	return nil, nil
}

// requireChatRuntimeSessionOwner keeps Bot history reads separate from generic
// Chat runtime access. A Bot adapter must bind the run, execution principal and
// model policy before exposing runner events or resolving credentials.
func requireChatRuntimeSessionOwner(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	if !requireSessionIDOwner(w, r, sessionID) {
		return false
	}
	if meta, err := botSessionMetadata(r.Context(), store.FromRequest(r), sessionID); err != nil || meta != nil {
		respondError(w, http.StatusNotFound, "Session not found")
		return false
	}
	return true
}

// readAuthorizedSessionArtifact preserves request cancellation and passes the
// recorded execution owner only after the scoped resolver authorizes the viewer.
func readAuthorizedSessionArtifact(r *http.Request, ss store.SessionStore, sessionID, filePath string) (string, bool) {
	meta, err := ss.GetSessionMeta(r.Context(), sessionID)
	if err != nil || meta == nil {
		return "", false
	}
	userID := effectiveUserID(r)
	if meta.HasBotLinkage() {
		userID = meta.UserID
	}
	events, err := ss.ReadTranscriptEvents(r.Context(), studioChatAppName, userID, sessionID)
	if err != nil {
		return "", false
	}
	return readArtifactContentFromEventSlice(events, filePath)
}

// botSessionReadAllowed resolves the authoritative Bot in the SAME scope as
// the session. Viewer identity is never substituted for the execution owner.
// Generic Chat mutations remain closed until the Bot runtime adapters exist.
func botSessionReadAllowed(r *http.Request, svc *store.Services, meta *store.SessionMeta, scope store.BotScope) bool {
	if r == nil || svc == nil || meta == nil || r.Method != http.MethodGet || !meta.HasBotLinkage() {
		return false
	}
	p, ok := execution.PrincipalFromContext(r.Context())
	tc := store.TenantContextFrom(r.Context())
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser || tc == nil || tc.UserID != p.Subject || tc.OrgSlug != p.OrgSlug || tc.TeamSlug != p.TeamSlug {
		return false
	}
	id, err := uuid.Parse(meta.BotID)
	if err != nil || id == uuid.Nil || meta.Retention != store.SessionRetentionBot {
		return false
	}
	var repo store.BotStore
	switch scope {
	case store.BotScopePersonal:
		repo = svc.PersonalBots
	case store.BotScopeTeam:
		repo = svc.Bots
	default:
		return false
	}
	if repo == nil {
		return false
	}
	b, err := repo.Get(r.Context(), id)
	if err != nil || b == nil || b.ID != id || b.Scope != scope || b.OwnerID.String() != meta.UserID {
		return false
	}
	switch meta.Purpose {
	case store.SessionPurposeBotHome:
		return meta.BotRunID == "" && b.HomeSessionID.String() == meta.ID
	case store.SessionPurposeBotRun:
		runID, err := uuid.Parse(meta.BotRunID)
		if err != nil || runID == uuid.Nil {
			return false
		}
		run, err := repo.GetRun(r.Context(), id, runID)
		return err == nil && run != nil && run.ID == runID && run.BotID == id && run.SessionID.String() == meta.ID
	default:
		return false
	}
}
