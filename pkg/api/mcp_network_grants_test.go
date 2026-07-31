package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestMCPNetworkGrantHandler_SavesTeamAllowRule(t *testing.T) {
	teamStore := &recordingNetworkPolicyStore{}
	svc := &store.Services{
		Mode:                store.ModePlatform,
		TeamNetworkPolicies: teamStore,
	}
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/context7/network-grants?scope=team", bytes.NewBufferString(`{"host":"registry.npmjs.org","port":443}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(store.WithServices(req.Context(), svc))
	req = req.WithContext(WithPlatformUser(req.Context(), &PlatformUser{ID: "user-1", Role: "admin"}))

	w := httptest.NewRecorder()
	MCPNetworkGrantHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if teamStore.saved == nil {
		t.Fatal("expected saved network policy rule")
	}
	if teamStore.saved.Host != "registry.npmjs.org" || teamStore.saved.Port != 443 || teamStore.saved.Action != store.NetworkPolicyAllow {
		t.Fatalf("saved rule = %+v", teamStore.saved)
	}
	if teamStore.saved.CreatedBy != "user-1" {
		t.Fatalf("CreatedBy = %q, want user-1", teamStore.saved.CreatedBy)
	}

	var resp mcpNetworkGrantResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Approved || resp.Host != "registry.npmjs.org" || resp.Port != 443 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestMCPNetworkGrantHandler_DefaultsPortTo443(t *testing.T) {
	teamStore := &recordingNetworkPolicyStore{}
	svc := &store.Services{
		Mode:                store.ModePlatform,
		TeamNetworkPolicies: teamStore,
	}
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/context7/network-grants?scope=team", bytes.NewBufferString(`{"host":"registry.npmjs.org"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(store.WithServices(req.Context(), svc))
	req = req.WithContext(WithPlatformUser(req.Context(), &PlatformUser{ID: "user-1", Role: "admin"}))

	w := httptest.NewRecorder()
	MCPNetworkGrantHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if teamStore.saved == nil || teamStore.saved.Port != 443 {
		t.Fatalf("saved rule = %+v, want port 443", teamStore.saved)
	}
}

func TestMCPNetworkGrantHandler_RequiresHost(t *testing.T) {
	teamStore := &recordingNetworkPolicyStore{}
	svc := &store.Services{
		Mode:                store.ModePlatform,
		TeamNetworkPolicies: teamStore,
	}
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/context7/network-grants?scope=team", bytes.NewBufferString(`{"port":443}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(store.WithServices(req.Context(), svc))
	req = req.WithContext(WithPlatformUser(req.Context(), &PlatformUser{ID: "user-1", Role: "admin"}))

	w := httptest.NewRecorder()
	MCPNetworkGrantHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if teamStore.saved != nil {
		t.Fatalf("unexpected saved rule: %+v", teamStore.saved)
	}
}

type recordingNetworkPolicyStore struct {
	saved *store.NetworkPolicyRule
}

func (s *recordingNetworkPolicyStore) List(context.Context) ([]store.NetworkPolicyRule, error) {
	return nil, nil
}

func (s *recordingNetworkPolicyStore) Get(context.Context, string) (*store.NetworkPolicyRule, error) {
	return nil, nil
}

func (s *recordingNetworkPolicyStore) Save(_ context.Context, rule *store.NetworkPolicyRule) error {
	cp := *rule
	s.saved = &cp
	return nil
}

func (s *recordingNetworkPolicyStore) Delete(context.Context, string) error {
	return nil
}
