package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	orgent "github.com/SAP/astonish/ent/org"
	"github.com/SAP/astonish/ent/org/orga2aagent"
	"github.com/SAP/astonish/pkg/store"
)

// orgA2AAgentStore implements store.A2AAgentStore for org-level A2A agents.
type orgA2AAgentStore struct {
	client *orgent.Client
}

var _ store.A2AAgentStore = (*orgA2AAgentStore)(nil)

func (s *orgA2AAgentStore) List(ctx context.Context) ([]store.A2AAgent, error) {
	ents, err := s.client.OrgA2AAgent.Query().
		Order(orga2aagent.ByName()).
		All(ctx)
	if err != nil {
		return nil, err
	}

	agents := make([]store.A2AAgent, len(ents))
	for i, e := range ents {
		agents[i] = entOrgA2AAgentToStore(e)
	}
	return agents, nil
}

func (s *orgA2AAgentStore) Get(ctx context.Context, name string) (*store.A2AAgent, error) {
	ent, err := s.client.OrgA2AAgent.Query().
		Where(orga2aagent.NameEQ(name)).
		Only(ctx)
	if err != nil {
		if orgent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	agent := entOrgA2AAgentToStore(ent)
	return &agent, nil
}

func (s *orgA2AAgentStore) Save(ctx context.Context, agent *store.A2AAgent) error {
	// Check if agent with this name already exists.
	existing, err := s.client.OrgA2AAgent.Query().
		Where(orga2aagent.NameEQ(agent.Name)).
		Only(ctx)
	if err != nil && !orgent.IsNotFound(err) {
		return err
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

	create := s.client.OrgA2AAgent.Create().
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
		return err
	}
	agent.ID = created.ID.String()
	return nil
}

func (s *orgA2AAgentStore) Delete(ctx context.Context, name string) error {
	_, err := s.client.OrgA2AAgent.Delete().
		Where(orga2aagent.NameEQ(name)).
		Exec(ctx)
	return err
}

func (s *orgA2AAgentStore) UpdateCachedCard(ctx context.Context, name string, card json.RawMessage, skills json.RawMessage) error {
	update := s.client.OrgA2AAgent.Update().
		Where(orga2aagent.NameEQ(name)).
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
	return err
}

func entOrgA2AAgentToStore(e *orgent.OrgA2AAgent) store.A2AAgent {
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
