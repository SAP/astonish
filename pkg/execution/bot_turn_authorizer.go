package execution

import (
	"context"
	"errors"
	"reflect"
	"slices"
)

// BotCurrentPrincipal resolves current membership/role authority for the ORIGINAL
// caller. It must not substitute a viewer or the Bot's autonomy principal.
type BotCurrentPrincipal func(context.Context, Principal) (Principal, error)

// BotTurnAuthorizer is the mandatory per-capability and per-tool interactive gate.
// It is not a Bot autonomy RunPolicy and contains no wildcard tool grants.
type BotTurnAuthorizer interface {
	Authorize(context.Context, Capability) error
	AuthorizeTool(context.Context, string) error
}

type botTurnAuthorizer struct {
	registry *BotTurnAuthorizationRegistry
	identity BotRunIdentity
	ceiling  BotCallerCeiling
	current  BotCurrentPrincipal
}

// Authorizer binds persisted restrictions to existing live admission authority.
// Merely restoring a ceiling cannot create an authorizer after restart.
func sameBotCallerCeiling(a, b BotCallerCeiling) bool {
	return reflect.DeepEqual(a.Principal, b.Principal) && a.ExpiresAt.Equal(b.ExpiresAt) && a.LocalLoopback == b.LocalLoopback
}

func (r *BotTurnAuthorizationRegistry) Authorizer(id BotRunIdentity, ceiling BotCallerCeiling, current BotCurrentPrincipal) (BotTurnAuthorizer, error) {
	if r == nil || current == nil {
		return nil, errors.New("interactive caller authorization unavailable")
	}
	a, err := r.Resolve(id)
	if err != nil {
		return nil, err
	}
	if !sameBotCallerCeiling(a.CallerCeiling(), ceiling) {
		return nil, errors.New("interactive caller ceiling mismatch")
	}
	return &botTurnAuthorizer{registry: r, identity: id, ceiling: ceiling.Clone(), current: current}, nil
}
func (a *botTurnAuthorizer) Authorize(ctx context.Context, capability Capability) error {
	switch capability {
	case CapabilityChat, CapabilityA2A, CapabilityMemoryRead, CapabilityMemoryWrite, CapabilityIntegration, CapabilityToolExecute:
	default:
		return errors.New("unknown interactive capability")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	original, err := a.registry.Resolve(a.identity)
	if err != nil {
		return err
	}
	if !sameBotCallerCeiling(original.CallerCeiling(), a.ceiling) {
		return errors.New("interactive caller ceiling changed")
	}
	// Both chat and the requested capability must have been granted originally.
	baseline := CapabilityAuthorizer{}
	if err := baseline.Authorize(original.Principal, CapabilityChat); err != nil {
		return err
	}
	if err := baseline.Authorize(original.Principal, capability); err != nil {
		return err
	}
	current, err := a.current(ctx, cloneBotCallerPrincipal(original.Principal))
	if err != nil {
		return err
	}
	if current.Authentication != original.Principal.Authentication || current.Kind != original.Principal.Kind || current.Subject != original.Principal.Subject || current.OrgSlug != original.Principal.OrgSlug || current.TeamSlug != original.Principal.TeamSlug {
		return errors.New("interactive caller identity changed")
	}
	if slices.Contains(current.Roles, "viewer") {
		return errors.New("interactive caller is read-only")
	}
	if err := baseline.Authorize(current, CapabilityChat); err != nil {
		return err
	}
	if err := baseline.Authorize(current, capability); err != nil {
		return err
	}
	// Recheck cancellation/expiry after the potentially blocking live resolver.
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = a.registry.Resolve(a.identity)
	return err
}
func (a *botTurnAuthorizer) AuthorizeTool(ctx context.Context, name string) error {
	if name == "" || name == "*" {
		return errors.New("concrete tool name required")
	}
	return a.Authorize(ctx, CapabilityToolExecute)
}

type botTurnAuthorizerKey struct{}

func WithBotTurnAuthorizer(ctx context.Context, a BotTurnAuthorizer) context.Context {
	return context.WithValue(ctx, botTurnAuthorizerKey{}, a)
}
func BotTurnAuthorizerFromContext(ctx context.Context) (BotTurnAuthorizer, bool) {
	if ctx == nil {
		return nil, false
	}
	a, ok := ctx.Value(botTurnAuthorizerKey{}).(BotTurnAuthorizer)
	return a, ok && a != nil
}
