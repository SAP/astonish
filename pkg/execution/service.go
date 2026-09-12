package execution

import (
	"context"
	"fmt"
)

// Service is the common pre-execution boundary used by protocol adapters. It
// validates and attaches the principal before the adapter reaches agent code.
type Service struct {
	authorizer Authorizer
}

// NewService constructs an execution service with a fail-closed default
// authorizer when none is supplied.
func NewService(authorizer Authorizer) *Service {
	if authorizer == nil {
		authorizer = CapabilityAuthorizer{}
	}
	return &Service{authorizer: authorizer}
}

// Authorize validates principal identity, authorizes a capability, and returns
// an execution context carrying the canonical principal.
func (s *Service) Authorize(ctx context.Context, principal Principal, capability Capability) (context.Context, error) {
	if s == nil {
		return nil, fmt.Errorf("execution service is required")
	}
	if err := s.authorizer.Authorize(principal, capability); err != nil {
		return nil, err
	}
	return WithPrincipal(ctx, principal)
}
