package a2a

import (
	"fmt"
	"sync"
)

// TrustedIssuerStore defines CRUD operations for TrustedIssuer entities.
type TrustedIssuerStore interface {
	List() []TrustedIssuer
	Create(issuer TrustedIssuer) error
	Delete(id string) error
	GetByIssuer(issuerURL string) (*TrustedIssuer, error)
}

// AllowedAgentStore defines CRUD operations for AllowedAgent entities.
type AllowedAgentStore interface {
	List() []AllowedAgent
	Create(agent AllowedAgent) error
	Delete(id string) error
	GetByActorSub(actorSub string) (*AllowedAgent, error)
}

// InMemoryIssuerStore is a thread-safe in-memory store for TrustedIssuers.
type InMemoryIssuerStore struct {
	mu      sync.RWMutex
	issuers []TrustedIssuer
}

// NewInMemoryIssuerStore creates a new empty InMemoryIssuerStore.
func NewInMemoryIssuerStore() *InMemoryIssuerStore {
	return &InMemoryIssuerStore{}
}

// List returns all trusted issuers.
func (s *InMemoryIssuerStore) List() []TrustedIssuer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TrustedIssuer, len(s.issuers))
	copy(out, s.issuers)
	return out
}

// Create adds a new trusted issuer. Returns an error if the ID already exists.
func (s *InMemoryIssuerStore) Create(issuer TrustedIssuer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.issuers {
		if existing.ID == issuer.ID {
			return fmt.Errorf("trusted issuer with ID %q already exists", issuer.ID)
		}
	}
	s.issuers = append(s.issuers, issuer)
	return nil
}

// Delete removes a trusted issuer by ID. Returns an error if not found.
func (s *InMemoryIssuerStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, issuer := range s.issuers {
		if issuer.ID == id {
			s.issuers = append(s.issuers[:i], s.issuers[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("trusted issuer with ID %q not found", id)
}

// GetByIssuer finds a trusted issuer by its Issuer URL (the `iss` claim value).
// Returns nil and an error if not found.
func (s *InMemoryIssuerStore) GetByIssuer(issuerURL string) (*TrustedIssuer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.issuers {
		if s.issuers[i].Issuer == issuerURL {
			result := s.issuers[i]
			return &result, nil
		}
	}
	return nil, fmt.Errorf("trusted issuer with issuer URL %q not found", issuerURL)
}

// InMemoryAgentAllowStore is a thread-safe in-memory store for AllowedAgents.
type InMemoryAgentAllowStore struct {
	mu     sync.RWMutex
	agents []AllowedAgent
}

// NewInMemoryAgentAllowStore creates a new empty InMemoryAgentAllowStore.
func NewInMemoryAgentAllowStore() *InMemoryAgentAllowStore {
	return &InMemoryAgentAllowStore{}
}

// List returns all allowed agents.
func (s *InMemoryAgentAllowStore) List() []AllowedAgent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AllowedAgent, len(s.agents))
	copy(out, s.agents)
	return out
}

// Create adds a new allowed agent. Returns an error if the ID already exists.
func (s *InMemoryAgentAllowStore) Create(agent AllowedAgent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.agents {
		if existing.ID == agent.ID {
			return fmt.Errorf("allowed agent with ID %q already exists", agent.ID)
		}
	}
	s.agents = append(s.agents, agent)
	return nil
}

// Delete removes an allowed agent by ID. Returns an error if not found.
func (s *InMemoryAgentAllowStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, agent := range s.agents {
		if agent.ID == id {
			s.agents = append(s.agents[:i], s.agents[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("allowed agent with ID %q not found", id)
}

// GetByActorSub finds an allowed agent by its ActorSub field.
// Returns nil and an error if not found.
func (s *InMemoryAgentAllowStore) GetByActorSub(actorSub string) (*AllowedAgent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.agents {
		if s.agents[i].ActorSub == actorSub {
			result := s.agents[i]
			return &result, nil
		}
	}
	return nil, fmt.Errorf("allowed agent with actor_sub %q not found", actorSub)
}
