package tools

import (
	"context"
	"testing"

	"google.golang.org/adk/agent"
	adkmemory "google.golang.org/adk/memory"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"

	astonishmemory "github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
)

type memorySearchToolCtx struct {
	context.Context
}

var _ tool.Context = (*memorySearchToolCtx)(nil)

func (m *memorySearchToolCtx) UserContent() *genai.Content          { return nil }
func (m *memorySearchToolCtx) InvocationID() string                 { return "test" }
func (m *memorySearchToolCtx) AgentName() string                    { return "test" }
func (m *memorySearchToolCtx) ReadonlyState() session.ReadonlyState { return nil }
func (m *memorySearchToolCtx) UserID() string                       { return "" }
func (m *memorySearchToolCtx) AppName() string                      { return "" }
func (m *memorySearchToolCtx) SessionID() string                    { return "" }
func (m *memorySearchToolCtx) Branch() string                       { return "" }
func (m *memorySearchToolCtx) Artifacts() agent.Artifacts           { return nil }
func (m *memorySearchToolCtx) State() session.State                 { return nil }
func (m *memorySearchToolCtx) FunctionCallID() string               { return "" }
func (m *memorySearchToolCtx) Actions() *session.EventActions       { return nil }
func (m *memorySearchToolCtx) SearchMemory(_ context.Context, _ string) (*adkmemory.SearchResponse, error) {
	return nil, nil
}
func (m *memorySearchToolCtx) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (m *memorySearchToolCtx) RequestConfirmation(_ string, _ any) error            { return nil }

type memorySearchFakeStore struct {
	results []store.MemorySearchResult
}

func (m *memorySearchFakeStore) Search(context.Context, string, int, float64) ([]store.MemorySearchResult, error) {
	return append([]store.MemorySearchResult(nil), m.results...), nil
}

func (m *memorySearchFakeStore) SearchByCategory(context.Context, string, int, float64, string) ([]store.MemorySearchResult, error) {
	return append([]store.MemorySearchResult(nil), m.results...), nil
}

func (m *memorySearchFakeStore) Add(context.Context, store.MemoryEntry) error { return nil }
func (m *memorySearchFakeStore) Get(context.Context, string) (*store.MemorySearchResult, error) {
	return nil, nil
}
func (m *memorySearchFakeStore) Update(context.Context, string, string, string) error { return nil }
func (m *memorySearchFakeStore) Delete(context.Context, string) error                 { return nil }
func (m *memorySearchFakeStore) List(context.Context, string, int, int) ([]store.MemorySearchResult, error) {
	return nil, nil
}
func (m *memorySearchFakeStore) ListBySession(context.Context, string) ([]store.MemorySearchResult, error) {
	return nil, nil
}
func (m *memorySearchFakeStore) Count() int   { return len(m.results) }
func (m *memorySearchFakeStore) Close() error { return nil }

func TestMemorySearchFiltersUnrelatedScenarioCards(t *testing.T) {
	github := astonishmemory.ScenarioCard{
		CanonicalKey:      "infrastructure-sap-github-enterprise",
		Title:             "SAP GitHub Enterprise issues API",
		RecommendedRecipe: []string{"Use GET https://github.wdf.sap.corp/api/v3/issues?filter=assigned&state=open&per_page=50."},
		Status:            astonishmemory.ScenarioCardStatusVerified,
	}
	openstack := astonishmemory.ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-service-endpoints",
		Title:             "OpenStack Octavia load balancer list",
		RecommendedRecipe: []string{"Use openstack-keystone and GET https://loadbalancer-3.qa-de-1.cloud.sap/v2/lbaas/loadbalancers."},
		Status:            astonishmemory.ScenarioCardStatusVerified,
	}
	fallback := &memorySearchFakeStore{results: []store.MemorySearchResult{
		{ID: "github-card", Snippet: astonishmemory.RenderScenarioCard(github), Category: astonishmemory.ScenarioCardCategory, Score: 0.60},
		{ID: "openstack-card", Snippet: astonishmemory.RenderScenarioCard(openstack), Category: astonishmemory.ScenarioCardCategory, Score: 0.60},
	}}
	ctx := &memorySearchToolCtx{Context: store.WithMemoryStore(context.Background(), fallback)}

	result, err := MemorySearch()(ctx, MemorySearchArgs{Query: "github enterprise issues API"})
	if err != nil {
		t.Fatalf("MemorySearch failed: %v", err)
	}
	searchResult, ok := result.(PlatformMemorySearchResult)
	if !ok {
		t.Fatalf("result type = %T, want PlatformMemorySearchResult", result)
	}
	if searchResult.Count != 1 || searchResult.Results[0].ID != "github-card" {
		t.Fatalf("result = %#v, want only the GitHub scenario card", searchResult)
	}
}
