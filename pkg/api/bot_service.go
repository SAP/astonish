package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// botRequestStore consumes middleware identity, never request-supplied actors or
// tenant IDs. Scope selects a repository, not authorization to its contents.
func botRequestStore(w http.ResponseWriter, r *http.Request) (store.BotStore, uuid.UUID, bool) {
	*r = *r.WithContext(WithBotOperationWakeups(r.Context()))
	p, ok := execution.PrincipalFromContext(r.Context())
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser {
		http.Error(w, "Authenticated user required", http.StatusUnauthorized)
		return nil, uuid.Nil, false
	}
	actor, err := uuid.Parse(p.Subject)
	tc := store.TenantContextFrom(r.Context())
	if err != nil || tc == nil || tc.UserID != p.Subject || tc.OrgSlug != p.OrgSlug || tc.TeamSlug != p.TeamSlug {
		http.Error(w, "Bot access forbidden", http.StatusForbidden)
		return nil, uuid.Nil, false
	}
	scope := store.BotScopePersonal
	if values, exists := r.URL.Query()["scope"]; exists {
		if len(values) != 1 {
			http.Error(w, "Invalid Bot scope", 400)
			return nil, uuid.Nil, false
		}
		scope = store.BotScope(values[0])
	}
	if scope != store.BotScopePersonal && scope != store.BotScopeTeam {
		http.Error(w, "Invalid Bot scope", 400)
		return nil, uuid.Nil, false
	}
	repo := store.BotStoreFromContext(r.Context(), scope)
	if repo == nil {
		http.Error(w, "Bot storage unavailable", http.StatusServiceUnavailable)
		return nil, uuid.Nil, false
	}
	return repo, actor, true
}

// botInteractiveAdmissionContext never falls back to legacy/autonomy authority.
// It is used only after explicit startup readiness enables interactive admission.
func botInteractiveAdmissionContext(r *http.Request) (context.Context, error) {
	svc := store.FromContext(r.Context())
	if svc == nil {
		return nil, store.ErrBotUnsupported
	}
	// Durable source-bound admission is independent of a process-local registry.
	if svc.BotMandatePolicy != nil {
		p, ok := execution.PrincipalFromContext(r.Context())
		e, verified := execution.AuthenticationEvidenceFromContext(r.Context())
		if !ok || !verified || p.Validate() != nil || e.Validate(time.Now()) != nil || p.Kind != execution.PrincipalKindUser || (e.Method != execution.AuthMethodPlatformJWT && !(e.Method == execution.AuthMethodLocalOS && e.LocalLoopback)) {
			return nil, store.ErrBotForbidden
		}
		if err := (execution.CapabilityAuthorizer{}).Authorize(p, execution.CapabilityChat); err != nil {
			return nil, store.ErrBotForbidden
		}
		return store.WithBotMessageAdmission(store.WithBotMandateAdmission(r.Context(), svc.BotMandatePolicy), botMandateSourceAdmission{}), nil
	}
	if !svc.BotInteractiveReady || svc.BotTurnRegistry == nil {
		return nil, store.ErrBotUnsupported
	}
	p, ok := execution.PrincipalFromContext(r.Context())
	e, verified := execution.AuthenticationEvidenceFromContext(r.Context())
	if !ok || !verified || e.Validate(time.Now()) != nil || p.Kind != execution.PrincipalKindUser {
		return nil, store.ErrBotForbidden
	}
	if err := (execution.CapabilityAuthorizer{}).Authorize(p, execution.CapabilityChat); err != nil {
		return nil, store.ErrBotForbidden
	}
	return store.WithBotMessageAdmission(r.Context(), svc.BotTurnRegistry), nil
}

func botHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrBotForbidden):
		http.Error(w, "Bot access forbidden", http.StatusForbidden)
	case errors.Is(err, store.ErrBotNotFound):
		http.Error(w, "Bot resource not found", http.StatusNotFound)
	case errors.Is(err, store.ErrBotBusy):
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Bot is still running or finishing cleanup; wait briefly and retry this proposal", http.StatusConflict)
	case errors.Is(err, store.ErrBotConflict):
		http.Error(w, "Bot changed; reload and review before retrying", http.StatusConflict)
	case errors.Is(err, store.ErrBotInvalid):
		http.Error(w, "Invalid Bot request", http.StatusBadRequest)
	case errors.Is(err, store.ErrBotExpired):
		http.Error(w, "Bot request expired", http.StatusGone)
	case errors.Is(err, store.ErrBotUnsupported):
		http.Error(w, "Bot operation unavailable", http.StatusServiceUnavailable)
	default:
		http.Error(w, "Bot storage operation failed", http.StatusInternalServerError)
	}
}
func botDecodeRequest(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		http.Error(w, "Invalid Bot request body", 400)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "Expected one JSON object", 400)
		return false
	}
	return true
}
func botPathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(mux.Vars(r)["botID"])
	if err != nil || id == uuid.Nil {
		http.Error(w, "Invalid Bot ID", 400)
		return uuid.Nil, false
	}
	return id, true
}
func botPageRequest(w http.ResponseWriter, r *http.Request) (store.BotPage, bool) {
	p := store.BotPage{Limit: 50, After: r.URL.Query().Get("after")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			http.Error(w, "Invalid page limit", 400)
			return p, false
		}
		p.Limit = n
	}
	if p.After != "" {
		if _, err := uuid.Parse(p.After); err != nil {
			http.Error(w, "Invalid page cursor", 400)
			return p, false
		}
	}
	return p, true
}
func botWriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
