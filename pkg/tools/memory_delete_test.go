package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

type memoryDeleteFakeStore struct {
	entries   map[string]*store.MemorySearchResult
	deletedID string
	deleteErr error
}

func (m *memoryDeleteFakeStore) Search(context.Context, string, int, float64) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (m *memoryDeleteFakeStore) SearchByCategory(context.Context, string, int, float64, string) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (m *memoryDeleteFakeStore) Add(context.Context, store.MemoryEntry) error { return nil }

func (m *memoryDeleteFakeStore) Get(_ context.Context, id string) (*store.MemorySearchResult, error) {
	if m.entries == nil {
		return nil, nil
	}
	return m.entries[id], nil
}

func (m *memoryDeleteFakeStore) Update(context.Context, string, string, string) error { return nil }

func (m *memoryDeleteFakeStore) Delete(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletedID = id
	delete(m.entries, id)
	return nil
}

func (m *memoryDeleteFakeStore) List(context.Context, string, int, int) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (m *memoryDeleteFakeStore) ListBySession(context.Context, string) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (m *memoryDeleteFakeStore) Count() int   { return len(m.entries) }
func (m *memoryDeleteFakeStore) Close() error { return nil }

func TestMemoryDeleteDeletesOwnedMemory(t *testing.T) {
	fake := &memoryDeleteFakeStore{entries: map[string]*store.MemorySearchResult{
		"mem-1": {ID: "mem-1", CreatedBy: "user-1", Scope: "team"},
	}}
	ctx := &memorySearchToolCtx{Context: store.WithUserID(
		store.WithMemoryScope(store.WithMemoryStore(context.Background(), fake), store.MemoryScopeTeam),
		"user-1",
	)}

	result, err := MemoryDelete()(ctx, MemoryDeleteArgs{ID: "mem-1", Scope: "team"})
	if err != nil {
		t.Fatalf("MemoryDelete failed: %v", err)
	}
	if !result.Deleted || result.ID != "mem-1" || result.Scope != "team" {
		t.Fatalf("result = %#v, want deleted mem-1 from team", result)
	}
	if fake.deletedID != "mem-1" {
		t.Fatalf("deletedID = %q, want mem-1", fake.deletedID)
	}
}

func TestMemoryDeleteRejectsAuthorizerDenial(t *testing.T) {
	fake := &memoryDeleteFakeStore{entries: map[string]*store.MemorySearchResult{
		"mem-1": {ID: "mem-1", CreatedBy: "user-1", Scope: "team"},
	}}
	ctx := &memorySearchToolCtx{Context: store.WithMemoryDeleteAuthorizer(
		store.WithMemoryScope(store.WithMemoryStore(context.Background(), fake), store.MemoryScopeTeam),
		func(context.Context, *store.MemorySearchResult, string) error {
			return errors.New("permission denied by policy")
		},
	)}

	_, err := MemoryDelete()(ctx, MemoryDeleteArgs{ID: "mem-1", Scope: "team"})
	if err == nil || !strings.Contains(err.Error(), "permission denied by policy") {
		t.Fatalf("err = %v, want authorizer denial", err)
	}
	if fake.deletedID != "" {
		t.Fatalf("deletedID = %q, want no delete", fake.deletedID)
	}
}

func TestMemoryDeleteRejectsDifferentCreatorWithoutAuthorizer(t *testing.T) {
	fake := &memoryDeleteFakeStore{entries: map[string]*store.MemorySearchResult{
		"mem-1": {ID: "mem-1", CreatedBy: "user-2", Scope: "team"},
	}}
	ctx := &memorySearchToolCtx{Context: store.WithUserID(
		store.WithMemoryScope(store.WithMemoryStore(context.Background(), fake), store.MemoryScopeTeam),
		"user-1",
	)}

	_, err := MemoryDelete()(ctx, MemoryDeleteArgs{ID: "mem-1", Scope: "team"})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want permission denied", err)
	}
	if fake.deletedID != "" {
		t.Fatalf("deletedID = %q, want no delete", fake.deletedID)
	}
}

func TestMemoryDeleteDeletesExplicitScopeStore(t *testing.T) {
	personal := &memoryDeleteFakeStore{entries: map[string]*store.MemorySearchResult{}}
	team := &memoryDeleteFakeStore{entries: map[string]*store.MemorySearchResult{
		"mem-1": {ID: "mem-1", CreatedBy: "user-1", Scope: "team"},
	}}
	ctx := &memorySearchToolCtx{Context: store.WithUserID(
		store.WithMemoryScope(
			store.WithMemoryStoresByScope(context.Background(), store.MemoryStoresByScope{Personal: personal, Team: team}),
			store.MemoryScopePersonal,
		),
		"user-1",
	)}

	result, err := MemoryDelete()(ctx, MemoryDeleteArgs{ID: "mem-1", Scope: "team"})
	if err != nil {
		t.Fatalf("MemoryDelete failed: %v", err)
	}
	if !result.Deleted || team.deletedID != "mem-1" || personal.deletedID != "" {
		t.Fatalf("result = %#v, team deleted = %q, personal deleted = %q", result, team.deletedID, personal.deletedID)
	}
}

func TestMemoryDeleteNotFound(t *testing.T) {
	fake := &memoryDeleteFakeStore{entries: map[string]*store.MemorySearchResult{}}
	ctx := &memorySearchToolCtx{Context: store.WithMemoryScope(store.WithMemoryStore(context.Background(), fake), store.MemoryScopeTeam)}

	result, err := MemoryDelete()(ctx, MemoryDeleteArgs{ID: "missing"})
	if err != nil {
		t.Fatalf("MemoryDelete failed: %v", err)
	}
	if result.Deleted || result.Message != "Memory not found." {
		t.Fatalf("result = %#v, want not found without delete", result)
	}
}

func TestMemoryDeletePropagatesDeleteError(t *testing.T) {
	fake := &memoryDeleteFakeStore{
		entries:   map[string]*store.MemorySearchResult{"mem-1": {ID: "mem-1", CreatedBy: "user-1"}},
		deleteErr: errors.New("database unavailable"),
	}
	ctx := &memorySearchToolCtx{Context: store.WithUserID(store.WithMemoryStore(context.Background(), fake), "user-1")}

	_, err := MemoryDelete()(ctx, MemoryDeleteArgs{ID: "mem-1"})
	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("err = %v, want wrapped delete error", err)
	}
}
