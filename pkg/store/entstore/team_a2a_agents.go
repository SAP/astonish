package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/a2aagent"
	"github.com/SAP/astonish/pkg/store"
)

// teamA2AAgentStore implements store.A2AAgentStore using the Ent team client.
type teamA2AAgentStore struct {
	client *teament.Client
}

var _ store.A2AAgentStore = (*teamA2AAgentStore)(nil)

func (s *teamA2AAgentStore) List(ctx context.Context) ([]store.A2AAgent, error) {
	agents, err := s.client.A2aAgent.Query().
		Order(a2aagent.ByName()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("entstore: A2AAgentStore.List: %w", err)
	}

	result := make([]store.A2AAgent, len(agents))
	for i, e := range agents {
		result[i] = teamEntA2AAgentToStore(e)
	}
	return result, nil
}

func (s *teamA2AAgentStore) Get(ctx context.Context, name string) (*store.A2AAgent, error) {
	ent, err := s.client.A2aAgent.Query().
		Where(a2aagent.NameEQ(name)).
		Only(ctx)
	if err != nil {
		if teament.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("entstore: A2AAgentStore.Get: %w", err)
	}
	agent := teamEntA2AAgentToStore(ent)
	return &agent, nil
}

func (s *teamA2AAgentStore) Save(ctx context.Context, agent *store.A2AAgent) error {
	// Check if agent with this name already exists.
	existing, err := s.client.A2aAgent.Query().
		Where(a2aagent.NameEQ(agent.Name)).
		Only(ctx)
	if err != nil && !teament.IsNotFound(err) {
		return fmt.Errorf("entstore: A2AAgentStore.Save: %w", err)
	}

	if existing != nil {
		// Update existing.
		update := existing.Update().
			SetURL(agent.URL).
			SetNillableCredentialName(nilStrPtr(agent.CredentialName)).
			SetAuthType(agent.AuthType).
			SetHeaders(agent.Headers).
			SetNillableTimeout(nilStrPtr(agent.Timeout)).
			SetUpdatedAt(time.Now())

		if agent.Enabled != nil {
			update.SetEnabled(*agent.Enabled)
		}
		if agent.CredentialName == "" {
			update.ClearCredentialName()
		}
		if agent.Timeout == "" {
			update.ClearTimeout()
		}

		return update.Exec(ctx)
	}

	// Create new.
	var createdBy uuid.UUID
	if agent.CreatedBy != "" {
		uid, err := uuid.Parse(agent.CreatedBy)
		if err == nil {
			createdBy = uid
		}
	}

	create := s.client.A2aAgent.Create().
		SetName(agent.Name).
		SetURL(agent.URL).
		SetNillableCredentialName(nilStrPtr(agent.CredentialName)).
		SetAuthType(agent.AuthType).
		SetHeaders(agent.Headers).
		SetNillableTimeout(nilStrPtr(agent.Timeout)).
		SetCreatedBy(createdBy)

	if agent.Enabled != nil {
		create.SetEnabled(*agent.Enabled)
	}

	if agent.CachedCard != nil {
		create.SetCachedCard([]byte(agent.CachedCard))
	}
	if agent.CachedSkills != nil {
		var skills []interface{}
		if err := json.Unmarshal(agent.CachedSkills, &skills); err == nil {
			create.SetCachedSkills(skills)
		}
	}

	created, err := create.Save(ctx)
	if err != nil {
		return fmt.Errorf("entstore: A2AAgentStore.Save (create): %w", err)
	}
	agent.ID = created.ID.String()
	return nil
}

func (s *teamA2AAgentStore) Delete(ctx context.Context, name string) error {
	_, err := s.client.A2aAgent.Delete().
		Where(a2aagent.NameEQ(name)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("entstore: A2AAgentStore.Delete: %w", err)
	}
	return nil
}

func (s *teamA2AAgentStore) UpdateCachedCard(ctx context.Context, name string, card json.RawMessage, skills json.RawMessage) error {
	update := s.client.A2aAgent.Update().
		Where(a2aagent.NameEQ(name)).
		SetUpdatedAt(time.Now())

	if card == nil {
		update.ClearCachedCard()
	} else {
		update.SetCachedCard([]byte(card))
	}

	if skills == nil {
		update.ClearCachedSkills()
	} else {
		var parsed []interface{}
		if err := json.Unmarshal(skills, &parsed); err != nil {
			return fmt.Errorf("invalid cached_skills JSON: %w", err)
		}
		update.SetCachedSkills(parsed)
	}

	_, err := update.Save(ctx)
	if err != nil {
		return fmt.Errorf("entstore: A2AAgentStore.UpdateCachedCard: %w", err)
	}
	return nil
}

func teamEntA2AAgentToStore(e *teament.A2aAgent) store.A2AAgent {
	agent := store.A2AAgent{
		ID:        e.ID.String(),
		Name:      e.Name,
		URL:       e.URL,
		AuthType:  e.AuthType,
		Enabled:   &e.Enabled,
		Headers:   e.Headers,
		CreatedBy: e.CreatedBy.String(),
		CreatedAt: e.CreatedAt,
		UpdatedAt: e.UpdatedAt,
	}
	if e.CredentialName != nil {
		agent.CredentialName = *e.CredentialName
	}
	if e.Timeout != nil {
		agent.Timeout = *e.Timeout
	}
	if e.CachedCard != nil {
		agent.CachedCard = json.RawMessage(e.CachedCard)
	}
	if e.CachedSkills != nil {
		if data, err := json.Marshal(e.CachedSkills); err == nil {
			agent.CachedSkills = data
		}
	}
	return agent
}
