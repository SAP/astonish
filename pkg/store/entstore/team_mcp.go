package entstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/mcpserver"
	"github.com/SAP/astonish/pkg/store"
)

// teamMCPServerStore implements store.MCPServerStore using the Ent team client.
type teamMCPServerStore struct {
	client *teament.Client
}

var _ store.MCPServerStore = (*teamMCPServerStore)(nil)

func (s *teamMCPServerStore) List(ctx context.Context) ([]store.MCPServer, error) {
	// Mandate policy resolves configured search inside Bot transactions. Match
	// the exact owning client, just as network-policy and team-settings reads do.
	client := s.client
	if tx, ok := ctx.Value(botPolicyTransactionKey{}).(botPolicyTransaction); ok && tx.outer == client {
		client = tx.current
	}
	servers, err := client.McpServer.Query().
		Order(mcpserver.ByName()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("entstore: MCPServerStore.List: %w", err)
	}

	result := make([]store.MCPServer, len(servers))
	for i, srv := range servers {
		result[i] = teamEntMCPServerToStore(srv)
	}
	return result, nil
}

func (s *teamMCPServerStore) Get(ctx context.Context, name string) (*store.MCPServer, error) {
	srv, err := s.client.McpServer.Query().
		Where(mcpserver.NameEQ(name)).
		Only(ctx)
	if err != nil {
		if teament.IsNotFound(err) {
			// Match the platform/org stores: not-found is (nil, nil), not an
			// error. Callers that iterate all tiers rely on this; returning an
			// error aborts scoped runtime builds under FallbackForbidden.
			return nil, nil
		}
		return nil, fmt.Errorf("entstore: MCPServerStore.Get: %w", err)
	}
	result := teamEntMCPServerToStore(srv)
	return &result, nil
}

func (s *teamMCPServerStore) Save(ctx context.Context, server *store.MCPServer) error {
	createdBy := uuid.Nil
	if server.CreatedBy != "" {
		if id, err := uuid.Parse(server.CreatedBy); err == nil {
			createdBy = id
		}
	}

	args := server.Args
	if args == nil {
		args = []string{}
	}

	env := map[string]any{}
	for k, v := range server.Env {
		env[k] = v
	}

	enabled := true
	if server.Enabled != nil {
		enabled = *server.Enabled
	}

	// Try update first.
	update := s.client.McpServer.Update().
		Where(mcpserver.NameEQ(server.Name)).
		SetTransport(server.Transport).
		SetArgs(args).
		SetEnv(env).
		SetEnabled(enabled)

	if server.Command != "" {
		update.SetCommand(server.Command)
	} else {
		update.ClearCommand()
	}
	if server.URL != "" {
		update.SetURL(server.URL)
	} else {
		update.ClearURL()
	}
	if server.CachedTools != nil {
		var cachedTools []any
		if err := json.Unmarshal(server.CachedTools, &cachedTools); err == nil {
			update.SetCachedTools(cachedTools)
		}
	}

	n, err := update.Save(ctx)
	if err != nil {
		return fmt.Errorf("entstore: MCPServerStore.Save: update: %w", err)
	}
	if n == 0 {
		create := s.client.McpServer.Create().
			SetName(server.Name).
			SetTransport(server.Transport).
			SetArgs(args).
			SetEnv(env).
			SetEnabled(enabled).
			SetCreatedBy(createdBy)

		if server.Command != "" {
			create.SetCommand(server.Command)
		}
		if server.URL != "" {
			create.SetURL(server.URL)
		}
		if server.CachedTools != nil {
			var cachedTools []any
			if err := json.Unmarshal(server.CachedTools, &cachedTools); err == nil {
				create.SetCachedTools(cachedTools)
			}
		}

		_, err = create.Save(ctx)
		if err != nil {
			return fmt.Errorf("entstore: MCPServerStore.Save: create: %w", err)
		}
	}
	return nil
}

func (s *teamMCPServerStore) Delete(ctx context.Context, name string) error {
	_, err := s.client.McpServer.Delete().
		Where(mcpserver.NameEQ(name)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("entstore: MCPServerStore.Delete: %w", err)
	}
	return nil
}

func (s *teamMCPServerStore) UpdateCachedTools(ctx context.Context, name string, tools json.RawMessage) error {
	var cachedTools []any
	if tools != nil {
		if err := json.Unmarshal(tools, &cachedTools); err != nil {
			return fmt.Errorf("entstore: MCPServerStore.UpdateCachedTools: unmarshal: %w", err)
		}
	}

	n, err := s.client.McpServer.Update().
		Where(mcpserver.NameEQ(name)).
		SetCachedTools(cachedTools).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("entstore: MCPServerStore.UpdateCachedTools: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("mcp server %q not found", name)
	}
	return nil
}

// --- Helpers ---

func teamEntMCPServerToStore(srv *teament.McpServer) store.MCPServer {
	enabled := srv.Enabled
	result := store.MCPServer{
		ID:        srv.ID.String(),
		Name:      srv.Name,
		Transport: srv.Transport,
		Enabled:   &enabled,
		CreatedBy: srv.CreatedBy.String(),
		CreatedAt: srv.CreatedAt,
		UpdatedAt: srv.UpdatedAt,
	}
	if srv.Command != nil {
		result.Command = *srv.Command
	}
	if srv.URL != nil {
		result.URL = *srv.URL
	}
	result.Args = srv.Args
	if srv.Env != nil {
		result.Env = make(map[string]string, len(srv.Env))
		for k, v := range srv.Env {
			result.Env[k] = fmt.Sprintf("%v", v)
		}
	}
	if srv.CachedTools != nil {
		if data, err := json.Marshal(srv.CachedTools); err == nil {
			result.CachedTools = data
		}
	}
	return result
}
