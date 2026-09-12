// Package execution defines the canonical identity and authorization context used
// by every Astonish execution surface.
package execution

import (
	"context"
	"fmt"
	"strings"
)

// PrincipalKind describes whose authority is being used for an execution.
type PrincipalKind string

const (
	PrincipalKindUser    PrincipalKind = "user"
	PrincipalKindService PrincipalKind = "service"
	PrincipalKindLocal   PrincipalKind = "local"
)

// AuthMethod describes how the principal was authenticated.
type AuthMethod string

const (
	AuthMethodPlatformJWT AuthMethod = "platform_jwt"
	AuthMethodChannel     AuthMethod = "channel_link"
	AuthMethodLocalOS     AuthMethod = "local_os"
	AuthMethodOAuth       AuthMethod = "oauth"
)

// Surface identifies the transport that initiated an execution.
type Surface string

const (
	SurfaceStudio   Surface = "studio"
	SurfaceTerminal Surface = "terminal"
	SurfaceChannel  Surface = "channel"
	SurfaceA2A      Surface = "a2a"
	SurfaceMCP      Surface = "mcp"
	SurfaceCode     Surface = "code"
)

// Principal is the canonical, authenticated identity available to an execution.
// Subject is the effective end user. Actor identifies a delegated workload when
// it performed an action for Subject. A service principal has no Subject.
type Principal struct {
	Kind           PrincipalKind
	Authentication AuthMethod
	Surface        Surface
	Subject        string
	Actor          string
	ClientID       string
	Issuer         string
	OrgSlug        string
	TeamSlug       string
	Roles          []string
	Scopes         []string
	Authenticated  bool
}

// Validate rejects incomplete or internally inconsistent identities before they
// are allowed to access tenant-scoped execution resources.
func (p Principal) Validate() error {
	if !p.Authenticated {
		return fmt.Errorf("principal is not authenticated")
	}
	if p.Kind == "" {
		return fmt.Errorf("principal kind is required")
	}
	if p.Authentication == "" {
		return fmt.Errorf("principal authentication method is required")
	}
	if p.Surface == "" {
		return fmt.Errorf("principal surface is required")
	}
	switch p.Kind {
	case PrincipalKindUser, PrincipalKindLocal:
		if p.Subject == "" {
			return fmt.Errorf("%s principal subject is required", p.Kind)
		}
	case PrincipalKindService:
		if p.ClientID == "" {
			return fmt.Errorf("service principal client ID is required")
		}
	default:
		return fmt.Errorf("unsupported principal kind %q", p.Kind)
	}
	if p.Kind != PrincipalKindLocal && (p.OrgSlug == "" || p.TeamSlug == "") {
		return fmt.Errorf("platform principal tenant is required")
	}
	return nil
}

// HasScope reports whether the principal has an exact scope.
func (p Principal) HasScope(scope string) bool {
	for _, granted := range p.Scopes {
		if granted == scope {
			return true
		}
	}
	return false
}

// HasRole reports whether the principal has an exact role.
func (p Principal) HasRole(role string) bool {
	for _, granted := range p.Roles {
		if strings.EqualFold(granted, role) {
			return true
		}
	}
	return false
}

type principalContextKey struct{}

// WithPrincipal attaches a validated principal to ctx.
func WithPrincipal(ctx context.Context, principal Principal) (context.Context, error) {
	if err := principal.Validate(); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, principalContextKey{}, principal), nil
}

// PrincipalFromContext returns the canonical principal associated with ctx.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}
