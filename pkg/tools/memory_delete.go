package tools

import (
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// MemoryDeleteArgs defines the arguments for the memory_delete tool.
type MemoryDeleteArgs struct {
	ID    string `json:"id" jsonschema:"The exact memory ID to delete. Get this from memory_search results."`
	Scope string `json:"scope,omitempty" jsonschema:"Memory tier containing the ID: personal, team, or org. Use the scope from memory_search results when available. Defaults to the current save tier."`
}

// MemoryDeleteResult is returned after deleting a memory.
type MemoryDeleteResult struct {
	Deleted bool   `json:"deleted"`
	ID      string `json:"id,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Message string `json:"message"`
}

// MemoryDelete creates the memory_delete handler. In platform mode it routes to
// the tenant-scoped memory store from the tool context.
func MemoryDelete() func(ctx tool.Context, args MemoryDeleteArgs) (MemoryDeleteResult, error) {
	return func(ctx tool.Context, args MemoryDeleteArgs) (MemoryDeleteResult, error) {
		if strings.TrimSpace(args.ID) == "" {
			return MemoryDeleteResult{}, fmt.Errorf("id is required")
		}

		return platformMemoryDeleteWithExplicitStore(ctx, args, nil)
	}
}

func memoryStoreForDeleteScope(ctx tool.Context, scope string) store.MemoryStore {
	if stores, ok := store.MemoryStoresByScopeFromContext(ctx); ok {
		switch scope {
		case string(store.MemoryScopePersonal):
			return stores.Personal
		case string(store.MemoryScopeTeam):
			return stores.Team
		case string(store.MemoryScopeOrg):
			return stores.Org
		}
	}
	if activeScope := store.MemoryScopeFromContext(ctx); activeScope == "" || scope == string(activeScope) {
		return store.MemoryStoreFromContext(ctx)
	}
	return nil
}

func platformMemoryDeleteWithExplicitStore(ctx tool.Context, args MemoryDeleteArgs, memStore store.MemoryStore) (MemoryDeleteResult, error) {
	id := strings.TrimSpace(args.ID)
	scope := strings.TrimSpace(strings.ToLower(args.Scope))
	if scope == "" {
		scope = string(store.MemoryScopeFromContext(ctx))
	}
	if scope == "" {
		scope = string(store.MemoryScopeTeam)
	}
	if scope != string(store.MemoryScopePersonal) && scope != string(store.MemoryScopeTeam) && scope != string(store.MemoryScopeOrg) {
		return MemoryDeleteResult{}, fmt.Errorf("invalid scope %q: must be personal, team, or org", args.Scope)
	}

	if memStore == nil {
		memStore = memoryStoreForDeleteScope(ctx, scope)
	}
	if memStore == nil {
		return MemoryDeleteResult{
			Deleted: false,
			ID:      id,
			Scope:   scope,
			Message: "Memory delete is not available for that scope.",
		}, nil
	}

	existing, err := memStore.Get(ctx, id)
	if err != nil {
		return MemoryDeleteResult{}, fmt.Errorf("failed to get memory: %w", err)
	}
	if existing == nil {
		return MemoryDeleteResult{
			Deleted: false,
			ID:      id,
			Scope:   scope,
			Message: "Memory not found.",
		}, nil
	}

	if authorizer := store.MemoryDeleteAuthorizerFromContext(ctx); authorizer != nil {
		if err := authorizer(ctx, existing, scope); err != nil {
			return MemoryDeleteResult{}, err
		}
	} else if createdBy := strings.TrimSpace(existing.CreatedBy); createdBy != "" {
		userID := store.UserIDFromContext(ctx)
		if userID == "" || createdBy != userID {
			return MemoryDeleteResult{}, fmt.Errorf("permission denied: memory was created by a different user")
		}
	}

	if err := memStore.Delete(ctx, id); err != nil {
		return MemoryDeleteResult{}, fmt.Errorf("failed to delete memory: %w", err)
	}

	return MemoryDeleteResult{
		Deleted: true,
		ID:      id,
		Scope:   scope,
		Message: fmt.Sprintf("Deleted memory %s from %s memory.", id, scope),
	}, nil
}

// PlatformMemoryDelete deletes a memory using the store.MemoryStore interface.
func PlatformMemoryDelete(memStore store.MemoryStore) func(ctx tool.Context, args MemoryDeleteArgs) (MemoryDeleteResult, error) {
	return func(ctx tool.Context, args MemoryDeleteArgs) (MemoryDeleteResult, error) {
		if strings.TrimSpace(args.ID) == "" {
			return MemoryDeleteResult{}, fmt.Errorf("id is required")
		}
		if memStore == nil {
			return MemoryDeleteResult{
				Deleted: false,
				Message: "Memory delete is not available.",
			}, nil
		}
		return platformMemoryDeleteWithExplicitStore(ctx, args, memStore)
	}
}

// NewMemoryDeleteTool creates the memory_delete tool.
// In platform mode, the tool checks the context for a PG-backed MemoryStore
// injected by ChatRunner.InjectMemoryStoresWithScope and deletes there.
func NewMemoryDeleteTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "memory_delete",
		Description: "Delete an existing memory by exact ID. Use memory_search first to find the ID and scope. " +
			"Only delete memories the user explicitly asks to remove or clearly identifies as obsolete or incorrect. " +
			"Deletion targets the result's memory tier when scope is provided.",
	}, MemoryDelete())
}

// NewPlatformMemoryDeleteTool creates the memory_delete tool for platform mode.
func NewPlatformMemoryDeleteTool(memStore store.MemoryStore) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "memory_delete",
		Description: "Delete an existing memory by exact ID. Use memory_search first to find the ID and scope. " +
			"Only delete memories the user explicitly asks to remove or clearly identifies as obsolete or incorrect.",
	}, PlatformMemoryDelete(memStore))
}
