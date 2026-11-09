package execution

import (
	"context"
	"errors"
	"slices"
)

// RegisterContinuation is only called after a transactional safe-checkpoint check.
// It never replaces live authority; the original capability ceiling only narrows.
func (r *BotTurnAuthorizationRegistry) RegisterContinuation(ctx context.Context, id BotRunIdentity, original BotCallerCeiling) (BotTurnAuthorization, error) {
	fresh := NewBotTurnAuthorizationRegistry(1)
	a, err := fresh.Register(ctx, id)
	if err != nil {
		return BotTurnAuthorization{}, err
	}
	p, old := a.Principal, original.Principal
	if p.Kind != old.Kind || p.Subject != old.Subject || p.OrgSlug != old.OrgSlug || p.TeamSlug != old.TeamSlug || p.Authentication != old.Authentication || a.Evidence.LocalLoopback != original.LocalLoopback || slices.Contains(p.Roles, "viewer") {
		return BotTurnAuthorization{}, errors.New("continuation caller changed")
	}
	if p.Authentication == AuthMethodOAuth {
		a.Principal.Scopes = nil
		for _, scope := range old.Scopes {
			if slices.Contains(p.Scopes, scope) {
				a.Principal.Scopes = append(a.Principal.Scopes, scope)
			}
		}
	}
	a.Principal.Roles = nil
	for _, role := range old.Roles {
		if slices.Contains(p.Roles, role) {
			a.Principal.Roles = append(a.Principal.Roles, role)
		}
	}
	if err := (CapabilityAuthorizer{}).Authorize(a.Principal, CapabilityChat); err != nil {
		return BotTurnAuthorization{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune()
	if r.closed || len(r.entries) >= r.capacity {
		return BotTurnAuthorization{}, errors.New("caller authority unavailable")
	}
	if _, exists := r.entries[id]; exists {
		return BotTurnAuthorization{}, errors.New("live caller authority cannot be renewed")
	}
	r.entries[id] = a.Clone()
	return a.Clone(), nil
}
