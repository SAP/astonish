package execution

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"time"
)

// BotExecutionMode is independent of presentation and conversational intent.
// Legacy's zero value preserves existing workers; unknown modes must fail closed.
type BotExecutionMode string

const (
	BotExecutionModeLegacy          BotExecutionMode = ""
	BotExecutionModeChatInteractive BotExecutionMode = "chat_interactive"
)

func (m BotExecutionMode) Valid() bool {
	return m == BotExecutionModeLegacy || m == BotExecutionModeChatInteractive
}

// AuthenticationEvidence is supplied by an authentication adapter only after
// verification. Never accept it from a request body or reconstruct it from roles.
type AuthenticationEvidence struct {
	Method        AuthMethod
	ExpiresAt     time.Time
	LocalLoopback bool
}

func (e AuthenticationEvidence) Validate(now time.Time) error {
	if e.LocalLoopback {
		if e.Method != AuthMethodLocalOS || e.ExpiresAt.IsZero() || !e.ExpiresAt.After(now) {
			return errors.New("invalid local authentication evidence")
		}
	} else if (e.Method != AuthMethodPlatformJWT && e.Method != AuthMethodOAuth) || !e.ExpiresAt.After(now) {
		return errors.New("invalid token authentication evidence")
	}
	return nil
}

type authenticationEvidenceKey struct{}
type authenticatedEvidence struct {
	evidence  AuthenticationEvidence
	principal Principal
}

func WithAuthenticationEvidence(ctx context.Context, e AuthenticationEvidence) (context.Context, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil {
		return nil, errors.New("authenticated principal required")
	}
	if err := e.Validate(time.Now()); err != nil {
		return nil, err
	}
	if !e.LocalLoopback && p.Authentication != e.Method {
		return nil, errors.New("authentication method mismatch")
	}
	if e.LocalLoopback && (p.Kind != PrincipalKindUser || p.Authentication != AuthMethodPlatformJWT) {
		return nil, errors.New("local evidence requires resolved single-user identity")
	}
	return context.WithValue(ctx, authenticationEvidenceKey{}, authenticatedEvidence{evidence: e, principal: cloneBotCallerPrincipal(p)}), nil
}
func AuthenticationEvidenceFromContext(ctx context.Context) (AuthenticationEvidence, bool) {
	if ctx == nil {
		return AuthenticationEvidence{}, false
	}
	e, ok := ctx.Value(authenticationEvidenceKey{}).(authenticatedEvidence)
	p, present := PrincipalFromContext(ctx)
	if !ok || !present || !reflect.DeepEqual(p, e.principal) {
		return AuthenticationEvidence{}, false
	}
	return e.evidence, true
}

// BotCallerCeiling is a persisted restriction, NEVER live authorization. It has
// no bearer, secrets, policy grants, or capability to survive a process restart.
type BotCallerCeiling struct {
	Principal     Principal
	ExpiresAt     time.Time
	LocalLoopback bool
}

func (c BotCallerCeiling) Clone() BotCallerCeiling {
	// A ceiling is persistable restriction, never a live mandate capability.
	c.Principal.mandate = nil
	c.Principal = cloneBotCallerPrincipal(c.Principal)
	return c
}
func cloneBotCallerPrincipal(p Principal) Principal {
	p.Roles = slices.Clone(p.Roles)
	p.Scopes = slices.Clone(p.Scopes)
	return p
}

type BotTurnAuthorization struct {
	Identity  BotRunIdentity
	Principal Principal
	Evidence  AuthenticationEvidence
}

func (a BotTurnAuthorization) Clone() BotTurnAuthorization {
	a.Principal = cloneBotCallerPrincipal(a.Principal)
	return a
}
func (a BotTurnAuthorization) CallerCeiling() BotCallerCeiling {
	return (BotCallerCeiling{Principal: cloneBotCallerPrincipal(a.Principal), ExpiresAt: a.Evidence.ExpiresAt, LocalLoopback: a.Evidence.LocalLoopback}).Clone()
}

// BotTurnAuthorizationRegistry is bounded and process-local. There is deliberately
// no restore API: a persisted ceiling cannot recreate live caller authority.
// Expiry is checked on every access; Close and Cancel synchronously remove it.
type BotTurnAuthorizationRegistry struct {
	mu       sync.Mutex
	capacity int
	closed   bool
	entries  map[BotRunIdentity]BotTurnAuthorization
}

func NewBotTurnAuthorizationRegistry(capacity int) *BotTurnAuthorizationRegistry {
	if capacity < 1 {
		capacity = 256
	}
	return &BotTurnAuthorizationRegistry{capacity: capacity, entries: make(map[BotRunIdentity]BotTurnAuthorization)}
}
func (r *BotTurnAuthorizationRegistry) Register(ctx context.Context, id BotRunIdentity) (BotTurnAuthorization, error) {
	if err := ctx.Err(); err != nil {
		return BotTurnAuthorization{}, err
	}
	p, ok := PrincipalFromContext(ctx)
	e, eok := AuthenticationEvidenceFromContext(ctx)
	if id.Validate() != nil || !ok || !eok || p.Validate() != nil || p.Kind != PrincipalKindUser || p.Subject != id.ActorID || p.OrgSlug != id.OrgSlug || p.TeamSlug != id.TeamSlug || e.Validate(time.Now()) != nil {
		return BotTurnAuthorization{}, errors.New("verified caller authority required")
	}
	if err := (CapabilityAuthorizer{}).Authorize(p, CapabilityChat); err != nil {
		return BotTurnAuthorization{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune()
	if r.closed {
		return BotTurnAuthorization{}, errors.New("caller authority registry closed")
	}
	if _, exists := r.entries[id]; exists {
		return BotTurnAuthorization{}, errors.New("caller authority already registered")
	}
	if len(r.entries) >= r.capacity {
		return BotTurnAuthorization{}, errors.New("caller authority registry full")
	}
	a := BotTurnAuthorization{Identity: id, Principal: cloneBotCallerPrincipal(p), Evidence: e}
	r.entries[id] = a
	return a.Clone(), nil
}
func (r *BotTurnAuthorizationRegistry) Resolve(id BotRunIdentity) (BotTurnAuthorization, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune()
	a, ok := r.entries[id]
	if r.closed || !ok {
		return BotTurnAuthorization{}, errors.New("live caller authority unavailable")
	}
	return a.Clone(), nil
}
func (r *BotTurnAuthorizationRegistry) prune() {
	for id, a := range r.entries {
		if a.Evidence.Validate(time.Now()) != nil {
			delete(r.entries, id)
		}
	}
}
func (r *BotTurnAuthorizationRegistry) Cancel(id BotRunIdentity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, id)
}
func (r *BotTurnAuthorizationRegistry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	clear(r.entries)
}
