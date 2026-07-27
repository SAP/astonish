package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestMemoryMergerFailsClosedWhenScenarioUpsertFails(t *testing.T) {
	memStore := &failingScenarioUpsertStore{listErr: errors.New("list unavailable")}
	merger := &MemoryMerger{}

	_, err := merger.SaveOrMerge(context.Background(), memStore, store.MemoryEntry{
		Content:  "Use the noVNC ticket endpoint, then open the websocket with the returned ticket.",
		Category: "proxmox-console-access",
	})
	if err == nil {
		t.Fatal("expected scenario upsert failure")
	}
	if memStore.addCalled {
		t.Fatal("SaveOrMerge inserted raw memory after scenario-card upsert failed")
	}
}

type failingScenarioUpsertStore struct {
	listErr   error
	addCalled bool
}

func (f *failingScenarioUpsertStore) Search(context.Context, string, int, float64) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (f *failingScenarioUpsertStore) SearchByCategory(context.Context, string, int, float64, string) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (f *failingScenarioUpsertStore) Add(context.Context, store.MemoryEntry) error {
	f.addCalled = true
	return nil
}

func (f *failingScenarioUpsertStore) Get(context.Context, string) (*store.MemorySearchResult, error) {
	return nil, nil
}

func (f *failingScenarioUpsertStore) Update(context.Context, string, string, string) error {
	return nil
}

func (f *failingScenarioUpsertStore) Delete(context.Context, string) error { return nil }

func (f *failingScenarioUpsertStore) List(context.Context, string, int, int) ([]store.MemorySearchResult, error) {
	return nil, f.listErr
}

func (f *failingScenarioUpsertStore) ListBySession(context.Context, string) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (f *failingScenarioUpsertStore) Count() int { return 0 }

func (f *failingScenarioUpsertStore) Close() error { return nil }
