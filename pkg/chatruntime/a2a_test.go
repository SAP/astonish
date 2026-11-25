package chatruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

type unavailableA2AStore struct{ agent store.A2AAgent }

func (s unavailableA2AStore) List(context.Context) ([]store.A2AAgent, error) {
	return []store.A2AAgent{s.agent}, nil
}
func (unavailableA2AStore) Get(context.Context, string) (*store.A2AAgent, error) { return nil, nil }
func (unavailableA2AStore) Save(context.Context, *store.A2AAgent) error          { return nil }
func (unavailableA2AStore) Delete(context.Context, string) error                 { return nil }
func (unavailableA2AStore) UpdateCachedCard(context.Context, string, json.RawMessage, json.RawMessage) error {
	return nil
}

func TestStrictA2AToolsSkipsUnavailableOptionalCard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "offline", http.StatusServiceUnavailable) }))
	defer server.Close()
	enabled := true
	svc := &store.Services{A2AAgents: unavailableA2AStore{agent: store.A2AAgent{Name: "SCI Autonomous Operation", URL: server.URL, Enabled: &enabled}}}
	tools, err := strictA2ATools(t.Context(), svc, nil)
	if err != nil || len(tools) != 0 {
		t.Fatalf("optional unavailable A2A card broke scoped Chat construction: tools=%d err=%v", len(tools), err)
	}
}
