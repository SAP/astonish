package a2a

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RegisteredAgent represents an external agent authorized to use the A2A endpoint.
type RegisteredAgent struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	APIKeyHash     string    `json:"-"` // never serialized
	LinkedUserID   string    `json:"linked_user_id,omitempty"`
	LinkedOrgSlug  string    `json:"linked_org_slug,omitempty"`
	LinkedTeamSlug string    `json:"linked_team_slug,omitempty"`
	RateLimit      int       `json:"rate_limit,omitempty"`     // requests per minute
	MaxConcurrent  int       `json:"max_concurrent,omitempty"` // max concurrent tasks
	CreatedAt      time.Time `json:"created_at"`
}

// AgentRegistry manages registered external agents.
type AgentRegistry interface {
	// Register adds a new agent. Returns the generated API key (plaintext, shown once).
	Register(agent RegisteredAgent) (apiKey string, err error)

	// GetByAPIKey looks up an agent by its API key. Uses constant-time comparison.
	GetByAPIKey(apiKey string) (*RegisteredAgent, error)

	// GetByID returns an agent by its ID.
	GetByID(id string) (*RegisteredAgent, error)

	// List returns all registered agents.
	List() []*RegisteredAgent

	// Delete removes an agent by ID.
	Delete(id string) error

	// RotateKey generates a new API key for an agent. Returns the new key (plaintext).
	RotateKey(id string) (newKey string, err error)
}

// InMemoryAgentRegistry is a thread-safe in-memory implementation of AgentRegistry.
type InMemoryAgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]*RegisteredAgent // keyed by ID
}

// NewInMemoryAgentRegistry creates a new in-memory agent registry.
func NewInMemoryAgentRegistry() *InMemoryAgentRegistry {
	return &InMemoryAgentRegistry{
		agents: make(map[string]*RegisteredAgent),
	}
}

func (r *InMemoryAgentRegistry) Register(agent RegisteredAgent) (string, error) {
	if agent.Name == "" {
		return "", fmt.Errorf("agent name is required")
	}
	if agent.ID == "" {
		agent.ID = uuid.New().String()
	}
	if agent.CreatedAt.IsZero() {
		agent.CreatedAt = time.Now()
	}

	// Generate API key
	apiKey := generateAPIKey()
	agent.APIKeyHash = hashAPIKey(apiKey)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[agent.ID] = &agent
	return apiKey, nil
}

func (r *InMemoryAgentRegistry) GetByAPIKey(apiKey string) (*RegisteredAgent, error) {
	hash := hashAPIKey(apiKey)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, agent := range r.agents {
		if constantTimeEqual(agent.APIKeyHash, hash) {
			return agent, nil
		}
	}
	return nil, fmt.Errorf("invalid API key")
}

func (r *InMemoryAgentRegistry) GetByID(id string) (*RegisteredAgent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.agents[id]
	if !ok {
		return nil, fmt.Errorf("agent %s not found", id)
	}
	return agent, nil
}

func (r *InMemoryAgentRegistry) List() []*RegisteredAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*RegisteredAgent, 0, len(r.agents))
	for _, agent := range r.agents {
		result = append(result, agent)
	}
	return result
}

func (r *InMemoryAgentRegistry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[id]; !ok {
		return fmt.Errorf("agent %s not found", id)
	}
	delete(r.agents, id)
	return nil
}

func (r *InMemoryAgentRegistry) RotateKey(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	agent, ok := r.agents[id]
	if !ok {
		return "", fmt.Errorf("agent %s not found", id)
	}
	newKey := generateAPIKey()
	agent.APIKeyHash = hashAPIKey(newKey)
	return newKey, nil
}

// hashAPIKey produces a SHA-256 hash of an API key for storage.
func hashAPIKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// constantTimeEqual compares two strings in constant time.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// generateAPIKey creates a random API key with a recognizable prefix.
func generateAPIKey() string {
	id := uuid.New().String()
	// Prefix with "a2a_" for easy identification
	return "a2a_" + id
}

// Compile-time assertion.
var _ AgentRegistry = (*InMemoryAgentRegistry)(nil)
