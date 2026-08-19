package a2aclient

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/credentials"
)

// AgentInfo provides summary information about a connected remote agent.
type AgentInfo struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Connected   bool   `json:"connected"`
	SkillCount  int    `json:"skill_count"`
	Description string `json:"description,omitempty"`
}

// Manager manages the lifecycle of A2A client connections to remote agents.
type Manager struct {
	config   *A2AClientConfig
	clients  map[string]*Client
	cards    map[string]*a2a.AgentCard
	resolver credentials.CredentialResolver
	mu       sync.RWMutex
}

// NewManager creates a new Manager with the given configuration.
func NewManager(cfg *A2AClientConfig) *Manager {
	if cfg == nil {
		cfg = &A2AClientConfig{Agents: make(map[string]A2AAgentConfig)}
	}
	return &Manager{
		config:  cfg,
		clients: make(map[string]*Client),
		cards:   make(map[string]*a2a.AgentCard),
	}
}

// NewManagerFromConfig creates a new Manager (alias for platform mode).
func NewManagerFromConfig(cfg *A2AClientConfig) *Manager {
	return NewManager(cfg)
}

// SetCredentialResolver sets the credential resolver used for auth header injection.
func (m *Manager) SetCredentialResolver(resolver credentials.CredentialResolver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolver = resolver
}

// Initialize creates clients for all enabled agents and fetches their agent cards.
// Errors during card fetching are logged but do not cause Initialize to fail.
func (m *Manager) Initialize(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, agentCfg := range m.config.Agents {
		if !agentCfg.IsEnabled() {
			continue
		}

		// Ensure the agent config has the name set
		if agentCfg.Name == "" {
			agentCfg.Name = name
		}

		client := NewClient(agentCfg, m.resolver)
		m.clients[name] = client

		// Attempt to fetch the agent card
		card, err := client.FetchAgentCard(ctx)
		if err != nil {
			log.Printf("a2aclient: warning: failed to fetch agent card for %q: %v", name, err)
			continue
		}
		m.cards[name] = card

		if err := client.ApplyAgentCard(card); err != nil {
			log.Printf("a2aclient: warning: failed to apply agent card for %q: %v", name, err)
			delete(m.cards, name)
			continue
		}
	}

	return nil
}

// GetClient returns the client for the named agent.
func (m *Manager) GetClient(agentName string) (*Client, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	client, ok := m.clients[agentName]
	if !ok {
		return nil, fmt.Errorf("a2aclient: no client for agent %q", agentName)
	}
	return client, nil
}

// GetAgentCard returns the cached agent card for the named agent.
func (m *Manager) GetAgentCard(agentName string) (*a2a.AgentCard, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	card, ok := m.cards[agentName]
	if !ok {
		return nil, fmt.Errorf("a2aclient: no agent card for %q", agentName)
	}
	return card, nil
}

// ListAgents returns summary information about all configured agents.
func (m *Manager) ListAgents() []AgentInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	agents := make([]AgentInfo, 0, len(m.config.Agents))
	for name, cfg := range m.config.Agents {
		if !cfg.IsEnabled() {
			continue
		}

		info := AgentInfo{
			Name: name,
			URL:  cfg.URL,
		}

		if card, ok := m.cards[name]; ok {
			info.Connected = true
			info.SkillCount = len(card.Skills)
			info.Description = card.Description
		}

		agents = append(agents, info)
	}

	return agents
}

// RefreshCard re-fetches the agent card for the named agent.
func (m *Manager) RefreshCard(ctx context.Context, agentName string) (*a2a.AgentCard, error) {
	m.mu.RLock()
	client, ok := m.clients[agentName]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("a2aclient: no client for agent %q", agentName)
	}

	card, err := client.FetchAgentCard(ctx)
	if err != nil {
		return nil, err
	}

	if err := client.ApplyAgentCard(card); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.cards[agentName] = card
	m.mu.Unlock()

	return card, nil
}

// Cleanup releases any resources held by the manager.
// Currently a no-op since HTTP clients don't need explicit cleanup.
func (m *Manager) Cleanup() {
	// No-op: HTTP clients are stateless and don't need explicit cleanup.
}
