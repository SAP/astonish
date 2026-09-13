package astonish

import (
	"context"

	"google.golang.org/adk/session"
)

// AutoInitSessionService wraps a real service and ensures State is initialized.
// This fixes a regression in ADK v0.2.0 where InMemoryService creates sessions
// with nil state maps (see GitHub Issue #324).
type AutoInitSessionService struct {
	session.Service
}

// NewAutoInitService creates a new AutoInitSessionService wrapper.
func NewAutoInitService(s session.Service) *AutoInitSessionService {
	return &AutoInitSessionService{Service: s}
}

// Create intercepts session creation and ensures the state map is never nil.
func (s *AutoInitSessionService) Create(ctx context.Context, req *session.CreateRequest) (*session.CreateResponse, error) {
	// FIX: Force initialization of the State map if the framework forgot it
	if req.State == nil {
		req.State = make(map[string]any)
	}
	// Pass to the actual implementation
	return s.Service.Create(ctx, req)
}

// Unwrap returns the wrapped session service so callers can type-assert
// persistence-only methods like ArchiveAndReplaceEvents.
func (s *AutoInitSessionService) Unwrap() session.Service {
	return s.Service
}
