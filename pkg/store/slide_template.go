package store

import (
	"context"
	"encoding/json"
	"sync"
)

// SlideTemplateRecord is a persisted imported slide template (not a built-in).
// Palettes and Archetypes are JSON of pkg/docs/slides/themes types so this
// package does not import the slides IR.
type SlideTemplateRecord struct {
	Name          string
	Label         string
	Description   string
	SchemaVersion int
	Skin          string
	Tokens        map[string]string
	Assets        map[string]string
	Palettes      json.RawMessage
	Archetypes    json.RawMessage
	TemplateModel string
	CreatedBy     string
}

// SlideTemplateStore persists imported slide templates in one already-resolved
// tenant scope (platform, org, or a DocsStore adapter for team/personal).
type SlideTemplateStore interface {
	Save(ctx context.Context, rec *SlideTemplateRecord) error
	Get(ctx context.Context, name string) (*SlideTemplateRecord, error)
	List(ctx context.Context) ([]SlideTemplateRecord, error)
	Delete(ctx context.Context, name string) error
}

// MemorySlideTemplateStore is an in-memory SlideTemplateStore for tests.
type MemorySlideTemplateStore struct {
	mu    sync.Mutex
	items map[string]*SlideTemplateRecord
}

func NewMemorySlideTemplateStore() *MemorySlideTemplateStore {
	return &MemorySlideTemplateStore{items: map[string]*SlideTemplateRecord{}}
}

func (m *MemorySlideTemplateStore) Save(_ context.Context, rec *SlideTemplateRecord) error {
	if rec == nil || rec.Name == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *rec
	if rec.Tokens != nil {
		cp.Tokens = cloneStringMap(rec.Tokens)
	}
	if rec.Assets != nil {
		cp.Assets = cloneStringMap(rec.Assets)
	}
	if rec.Palettes != nil {
		cp.Palettes = append(json.RawMessage(nil), rec.Palettes...)
	}
	if rec.Archetypes != nil {
		cp.Archetypes = append(json.RawMessage(nil), rec.Archetypes...)
	}
	m.items[rec.Name] = &cp
	return nil
}

func (m *MemorySlideTemplateStore) Get(_ context.Context, name string) (*SlideTemplateRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.items[name]
	if rec == nil {
		return nil, nil
	}
	cp := *rec
	return &cp, nil
}

func (m *MemorySlideTemplateStore) List(_ context.Context) ([]SlideTemplateRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SlideTemplateRecord, 0, len(m.items))
	for _, rec := range m.items {
		out = append(out, *rec)
	}
	return out, nil
}

func (m *MemorySlideTemplateStore) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, name)
	return nil
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
