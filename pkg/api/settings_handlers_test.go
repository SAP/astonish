package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider/sap"
	"github.com/SAP/astonish/pkg/store"
)

type sapCatalogHTTPModelLimits struct {
	store.ModelLimitsStore // Unexpected writes fail rather than touching durable state.
}

func (sapCatalogHTTPModelLimits) Get(_ context.Context, provider, model string) (*store.ModelLimitEntry, error) {
	if provider != "sap_ai_core" || model != "gpt-6-astra" {
		return nil, nil
	}
	supportsTools := false
	return &store.ModelLimitEntry{MaxOutputTokens: 3210, ContextWindow: 4096, SupportsTools: &supportsTools, Source: "learned_400"}, nil
}

// TestSAPCatalogMetadataHTTP crosses both loopback HTTP boundaries: the platform
// metadata route resolves scoped settings, then SAP discovery and catalog calls
// use real OAuth HTTP requests. No external provider or database is required.
func TestSAPCatalogMetadataHTTP(t *testing.T) {
	assertFileUnchanged := isolateIndependentSettingsConfig(t)
	sap.SetModelLimitsStore(sapCatalogHTTPModelLimits{})
	t.Cleanup(func() { sap.SetModelLimitsStore(nil) })

	var mu sync.Mutex
	calls := make(map[string]int)
	catalogOutage := true
	sapServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		group := r.Header.Get("AI-Resource-Group")
		calls[group+":"+r.URL.Path]++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "loopback-client" || r.Form.Get("client_secret") != "loopback-secret" {
				t.Errorf("invalid OAuth request: method=%s", r.Method)
				http.Error(w, "invalid OAuth request", http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"access_token":"loopback-token","expires_in":3600,"token_type":"Bearer"}`)
			return
		}
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer loopback-token" || group == "" {
			t.Errorf("invalid SAP request: method=%s path=%s group=%q", r.Method, r.URL.Path, group)
			http.Error(w, "invalid SAP request", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v2/lm/deployments":
			if r.URL.Query().Get("status") != "RUNNING" || r.URL.Query().Get("resourceGroup") != group {
				t.Errorf("deployment query did not scope running models: %s", r.URL.RawQuery)
			}
			if group == "empty" {
				fmt.Fprint(w, `{"resources":[]}`)
				return
			}
			fmt.Fprint(w, `{"resources":[{"id":"running-deployment","configurationId":"astra-config","status":"RUNNING","details":{"resources":{"backendDetails":{"model":{"name":"gpt-6-astra"}}}}},{"id":"stopped-deployment","status":"STOPPED","details":{"resources":{"backendDetails":{"model":{"name":"undeployed-model"}}}}}]}`)
		case "/v2/lm/configurations/astra-config":
			fmt.Fprint(w, `{"id":"astra-config","parameterBindings":[{"key":"modelName","value":"gpt-6-astra"}]}`)
		case "/v2/lm/scenarios/foundation-models/models":
			if group == "outage" && catalogOutage {
				http.Error(w, "catalog unavailable", http.StatusServiceUnavailable)
				return
			}
			capacity := 1100000
			if group == "independent" {
				capacity = 222222
			}
			fmt.Fprintf(w, `{"resources":[{"model":"gpt-6-astra","versions":[{"name":"latest","isLatest":true,"contextLength":%d}]},{"model":"undeployed-model","versions":[{"name":"latest","isLatest":true,"contextLength":9999999}]}]}`, capacity)
		default:
			t.Errorf("unexpected SAP path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer sapServer.Close()

	providers := make(map[string]map[string]string)
	for _, group := range []string{"primary", "independent", "outage", "empty"} {
		providers["SAP-"+group] = map[string]string{
			"type": "sap_ai_core", "client_id": "loopback-client", "client_secret": "loopback-secret",
			"auth_url": sapServer.URL, "base_url": sapServer.URL, "resource_group": group,
		}
	}
	svc := &store.Services{
		Mode:             store.ModePlatform,
		PlatformSettings: &providerMatrixPlatformStore{value: &store.PlatformSettings{Providers: providers}},
		OrgSettings:      &providerMatrixOrgStore{value: &store.OrgSettings{}},
		Settings:         &urgentSettingsStore{value: &store.TeamSettings{}},
	}
	router := mux.NewRouter()
	router.HandleFunc("/api/providers/{providerId}/models-metadata", func(w http.ResponseWriter, r *http.Request) {
		ctx := store.WithTenantContext(store.WithServices(r.Context(), svc), &store.TenantContext{OrgSlug: "catalog-org", TeamSlug: "catalog-team"})
		ListProviderModelsWithMetadataHandler(w, r.WithContext(ctx))
	}).Methods(http.MethodGet)
	platformServer := httptest.NewServer(router)
	defer platformServer.Close()
	client := &http.Client{Timeout: 15 * time.Second}
	fetch := func(t *testing.T, group string) []sap.ModelInfo {
		t.Helper()
		resp, err := client.Get(platformServer.URL + "/api/providers/SAP-" + group + "/models-metadata")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result struct {
			Provider    string          `json:"provider"`
			HasMetadata bool            `json:"has_metadata"`
			Models      []sap.ModelInfo `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode metadata (status=%d): %v", resp.StatusCode, err)
		}
		if resp.StatusCode != http.StatusOK || !result.HasMetadata || result.Provider != "SAP-"+group {
			t.Fatalf("invalid metadata envelope: status=%d result=%+v", resp.StatusCode, result)
		}
		return result.Models
	}
	assertModel := func(t *testing.T, models []sap.ModelInfo, capacity int) {
		t.Helper()
		if len(models) != 1 || models[0].ID != "gpt-6-astra" || models[0].Name != "gpt-6-astra" {
			t.Fatalf("metadata must contain only deployed models: %+v", models)
		}
		model := models[0]
		if model.ContextLength != capacity || model.MaxOutputTokens != 3210 || model.SupportsTools == nil || *model.SupportsTools {
			t.Fatalf("catalog capacity/learned output and tools not preserved: %+v, want capacity=%d", model, capacity)
		}
	}
	t.Run("catalog-capacity", func(t *testing.T) { assertModel(t, fetch(t, "primary"), 1100000) })
	t.Run("independent-resource-group", func(t *testing.T) {
		assertModel(t, fetch(t, "independent"), 222222)
		assertModel(t, fetch(t, "primary"), 1100000)
	})
	t.Run("catalog-outage-and-recovery", func(t *testing.T) {
		assertModel(t, fetch(t, "outage"), sap.GetModelConfig("gpt-6-astra").ContextWindow)
		mu.Lock()
		catalogOutage = false
		mu.Unlock()
		assertModel(t, fetch(t, "outage"), 1100000)
	})
	t.Run("empty-deployments", func(t *testing.T) {
		if models := fetch(t, "empty"); len(models) != 0 {
			t.Fatalf("catalog manufactured models without deployments: %+v", models)
		}
	})
	mu.Lock()
	for _, group := range []string{"primary", "independent", "outage", "empty"} {
		for _, path := range []string{"/v2/lm/deployments", "/v2/lm/scenarios/foundation-models/models"} {
			if calls[group+":"+path] == 0 {
				t.Errorf("SAP %s did not receive %s", group, path)
			}
		}
	}
	mu.Unlock()
	assertFileUnchanged()
}

// isolateIndependentSettingsConfig prevents settings resets from starting provider
// initialization and confines any unexpected file access to this test's directory.
func isolateIndependentSettingsConfig(t *testing.T) func() {
	t.Helper()
	cm := GetChatManager()
	previousPreWarm := cm.preWarmCtxFn
	cm.preWarmCtxFn = nil
	t.Cleanup(func() { cm.preWarmCtxFn = previousPreWarm })
	dir := t.TempDir()
	t.Setenv("SUDO_USER", "")
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "APPDATA"} {
		t.Setenv(key, dir)
	}
	path, err := config.GetConfigPath()
	if err != nil || path != filepath.Join(dir, "astonish", "config.yaml") {
		t.Fatalf("config path is not isolated: %q, %v", path, err)
	}
	cfg := &config.AppConfig{}
	cfg.AgentIdentity.Name = "independent-settings-file-sentinel"
	if err := config.SaveAppConfig(cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) {
			t.Fatalf("scoped settings changed host config: %v", err)
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 1 || entries[0].Name() != "config.yaml" {
			t.Fatalf("scoped settings created unexpected config/credential files: %v", err)
		}
	}
}

// independentSettingsRequest gives the team admin authority only in team-a;
// the org admin and platform admin do not inherit that membership.
func independentSettingsRequest(method, path, body string, user *PlatformUser, svc *store.Services, team string) *http.Request {
	teams := newMockTeamMgmt()
	teams.teams["team-a"] = &store.Team{ID: "team-a-id", Slug: "team-a"}
	teams.teams["team-b"] = &store.Team{ID: "team-b-id", Slug: "team-b"}
	teams.memberRoles["independent-team-admin:team-a-id"] = "admin"
	svc.TenantRouter = &authzTenantRouter{orgs: map[string]store.OrgDataStore{
		"org-a": &authzOrgDataStore{teams: teams},
	}}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := store.WithTenantContext(store.WithServices(r.Context(), svc), &store.TenantContext{OrgSlug: "org-a", TeamSlug: team})
	if user != nil {
		ctx = WithPlatformUser(ctx, user)
	}
	return r.WithContext(ctx)
}

func TestSettingsIndependentOrgAdminUpdatesTeamDefaults(t *testing.T) {
	assertFileUnchanged := isolateIndependentSettingsConfig(t)
	original := &store.TeamSettings{DefaultModel: "before", Providers: map[string]map[string]string{"team-provider": {"type": "openai", "api_key": "team-secret"}}}
	ts := &urgentSettingsStore{value: original}
	ps := &providerMatrixPlatformStore{value: &store.PlatformSettings{DefaultModel: "platform-before"}}
	org := &providerMatrixOrgStore{value: &store.OrgSettings{DefaultModel: "org-before"}}
	svc := &store.Services{Mode: store.ModePlatform, Settings: ts, OrgSettings: org, PlatformSettings: ps}
	user := &PlatformUser{ID: "independent-org-admin", Role: "admin"}
	r := independentSettingsRequest(http.MethodPut, "/api/settings/config", `{"general":{"default_provider":"team-provider","default_model":"after","web_search_tool":"brave","web_extract_tool":"jina","context_length":8192}}`, user, svc, "team-a")
	w := httptest.NewRecorder()
	UpdateSettingsHandler(w, r)
	if w.Code != http.StatusOK || ts.saves != 1 {
		t.Fatalf("status=%d saves=%d body=%s", w.Code, ts.saves, w.Body)
	}
	if ts.value.DefaultProvider != "team-provider" || ts.value.DefaultModel != "after" || ts.value.WebSearchTool != "brave" || ts.value.WebExtractTool != "jina" || ts.value.ContextLength != 8192 {
		t.Fatal("org admin's team defaults were not persisted")
	}
	if original.DefaultModel != "before" || !reflect.DeepEqual(ts.value.Providers, original.Providers) || ps.saves != 0 || org.saves != 0 || ps.value.DefaultModel != "platform-before" || org.value.DefaultModel != "org-before" {
		t.Fatal("team update changed source providers or another scope")
	}
	w = httptest.NewRecorder()
	GetSettingsHandler(w, independentSettingsRequest(http.MethodGet, "/api/settings/config", "", user, svc, "team-a"))
	var response AppSettingsResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.General.DefaultModel != "after" || len(response.Providers) != 1 {
		t.Fatalf("updated settings not readable: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "team-secret") || response.Providers[0].Fields["api_key"] != maskValue("team-secret") {
		t.Fatal("settings read did not mask the preserved secret")
	}
	assertFileUnchanged()
}

func TestSettingsIndependentPrincipalsForbiddenScopes(t *testing.T) {
	for _, tc := range []struct {
		name, path, team string
		user             *PlatformUser
		handler          http.HandlerFunc
		status           int
	}{
		{"anonymous-team", "/api/settings/config", "team-a", nil, UpdateSettingsHandler, http.StatusUnauthorized},
		{"member-team", "/api/settings/config", "team-a", &PlatformUser{ID: "independent-member", Role: "member"}, UpdateSettingsHandler, http.StatusForbidden},
		{"team-admin-sibling", "/api/settings/config", "team-b", &PlatformUser{ID: "independent-team-admin", Role: "member"}, UpdateSettingsHandler, http.StatusForbidden},
		{"team-admin-org", "/api/settings/org/providers", "team-a", &PlatformUser{ID: "independent-team-admin", Role: "member"}, SaveOrgProvidersHandler, http.StatusForbidden},
		{"team-admin-platform", "/api/settings/platform/providers", "team-a", &PlatformUser{ID: "independent-team-admin", Role: "member"}, SavePlatformProvidersHandler, http.StatusForbidden},
		{"org-admin-platform", "/api/settings/platform/providers", "team-a", &PlatformUser{ID: "independent-org-admin", Role: "admin"}, SavePlatformProvidersHandler, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertFileUnchanged := isolateIndependentSettingsConfig(t)
			ts := &urgentSettingsStore{value: &store.TeamSettings{DefaultModel: "team-before"}}
			ps := &providerMatrixPlatformStore{value: &store.PlatformSettings{DefaultModel: "platform-before"}}
			org := &providerMatrixOrgStore{value: &store.OrgSettings{DefaultModel: "org-before"}}
			svc := &store.Services{Mode: store.ModePlatform, Settings: ts, OrgSettings: org, PlatformSettings: ps}
			before, err := json.Marshal([]any{ts.value, org.value, ps.value})
			if err != nil {
				t.Fatal(err)
			}
			for _, body := range []string{`{"providers":{"new":{"type":"openai","api_key":"rejected-secret"}}}`, `not-json`} {
				w := httptest.NewRecorder()
				tc.handler(w, independentSettingsRequest(http.MethodPut, tc.path, body, tc.user, svc, tc.team))
				if w.Code != tc.status || strings.Contains(w.Body.String(), "rejected-secret") {
					t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body)
				}
			}
			after, err := json.Marshal([]any{ts.value, org.value, ps.value})
			if err != nil || string(before) != string(after) || ts.saves+org.saves+ps.saves != 0 {
				t.Fatalf("forbidden request changed scoped settings: %v", err)
			}
			assertFileUnchanged()
		})
	}
}

func TestAppSettingsResponse_JSONStructure(t *testing.T) {
	response := AppSettingsResponse{
		General: GeneralSettings{
			DefaultProvider:            "openai-prod",
			DefaultProviderDisplayName: "OpenAI",
			DefaultModel:               "gpt-4o",
			WebSearchTool:              "",
			WebExtractTool:             "",
		},
		Providers: []ProviderSettings{
			{
				Name:        "openai-prod",
				Type:        "openai",
				DisplayName: "OpenAI",
				Configured:  true,
				Fields: map[string]string{
					"api_key": "****",
				},
			},
		},
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}

	var decoded AppSettingsResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if decoded.General.DefaultProvider != "openai-prod" {
		t.Errorf("DefaultProvider = %q, expected %q", decoded.General.DefaultProvider, "openai-prod")
	}

	if len(decoded.Providers) != 1 {
		t.Errorf("Providers length = %d, expected 1", len(decoded.Providers))
	}

	if decoded.Providers[0].Name != "openai-prod" {
		t.Errorf("Provider[0].Name = %q, expected %q", decoded.Providers[0].Name, "openai-prod")
	}
}

func TestProviderSettings_FieldsMasking(t *testing.T) {
	settings := ProviderSettings{
		Name:        "test-provider",
		Type:        "openai",
		DisplayName: "OpenAI",
		Configured:  true,
		Fields: map[string]string{
			"api_key":  "****",
			"base_url": "http://localhost:11434",
		},
	}

	if settings.Fields["api_key"] != "****" {
		t.Error("api_key should be masked")
	}

	if settings.Fields["base_url"] != "http://localhost:11434" {
		t.Error("base_url should not be masked")
	}
}

func TestUpdateAppSettingsRequest_ProvidersMap(t *testing.T) {
	request := UpdateAppSettingsRequest{
		General: &GeneralSettings{
			DefaultProvider: "openai-prod",
			DefaultModel:    "gpt-4o",
		},
		Providers: map[string]map[string]string{
			"openai-prod": {
				"type":    "openai",
				"api_key": "sk-...",
			},
			"anthropic-dev": {
				"type":    "anthropic",
				"api_key": "sk-ant-...",
			},
		},
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	var decoded UpdateAppSettingsRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal request: %v", err)
	}

	if decoded.General.DefaultProvider != "openai-prod" {
		t.Errorf("DefaultProvider = %q, expected %q", decoded.General.DefaultProvider, "openai-prod")
	}

	if len(decoded.Providers) != 2 {
		t.Errorf("Providers length = %d, expected 2", len(decoded.Providers))
	}

	if decoded.Providers["openai-prod"]["type"] != "openai" {
		t.Errorf("Providers[openai-prod][type] = %q, expected %q", decoded.Providers["openai-prod"]["type"], "openai")
	}
}

func TestProviderSettings_EmptyFields(t *testing.T) {
	settings := ProviderSettings{
		Name:        "empty-provider",
		Type:        "openai",
		DisplayName: "OpenAI",
		Configured:  false,
		Fields:      map[string]string{},
	}

	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("failed to marshal settings: %v", err)
	}

	if !strings.Contains(string(data), `"configured":false`) {
		t.Error("response should contain configured: false")
	}
}

func TestGeneralSettings_AllFields(t *testing.T) {
	settings := GeneralSettings{
		DefaultProvider:            "litellm-prod",
		DefaultProviderDisplayName: "LiteLLM",
		DefaultModel:               "gpt-4",
		WebSearchTool:              "brave",
		WebExtractTool:             "jina",
	}

	if settings.DefaultProvider != "litellm-prod" {
		t.Errorf("DefaultProvider = %q, expected %q", settings.DefaultProvider, "litellm-prod")
	}

	if settings.WebSearchTool != "brave" {
		t.Errorf("WebSearchTool = %q, expected %q", settings.WebSearchTool, "brave")
	}

	if settings.WebExtractTool != "jina" {
		t.Errorf("WebExtractTool = %q, expected %q", settings.WebExtractTool, "jina")
	}
}

func TestUpdateAppSettingsRequest_ReplaceAll(t *testing.T) {
	newProvidersJSON := json.RawMessage(`[{"name":"new-provider","type":"openai","api_key":"sk-..."}]`)

	request := UpdateAppSettingsRequest{
		Providers: map[string]map[string]string{
			"__replace_all__": {
				"__array__": string(newProvidersJSON),
			},
		},
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal request: %v", err)
	}

	providers, ok := decoded["providers"].(map[string]interface{})
	if !ok {
		t.Fatal("providers should be a map")
	}

	replaceAll, ok := providers["__replace_all__"].(map[string]interface{})
	if !ok {
		t.Fatal("__replace_all__ should be a map")
	}

	arrayStr, ok := replaceAll["__array__"].(string)
	if !ok {
		t.Fatal("__array__ should be a string")
	}

	var providersArray []map[string]interface{}
	if err := json.Unmarshal([]byte(arrayStr), &providersArray); err != nil {
		t.Fatalf("failed to unmarshal providers array: %v", err)
	}

	if len(providersArray) != 1 {
		t.Errorf("providers array length = %d, expected 1", len(providersArray))
	}
}

func TestAppSettingsResponse_MultipleProviders(t *testing.T) {
	response := AppSettingsResponse{
		General: GeneralSettings{
			DefaultProvider: "openai-prod",
		},
		Providers: []ProviderSettings{
			{Name: "openai-prod", Type: "openai", DisplayName: "OpenAI", Configured: true, Fields: map[string]string{"api_key": "****"}},
			{Name: "anthropic-dev", Type: "anthropic", DisplayName: "Anthropic", Configured: true, Fields: map[string]string{"api_key": "****"}},
			{Name: "litellm-prod", Type: "litellm", DisplayName: "LiteLLM", Configured: true, Fields: map[string]string{"api_key": "****", "base_url": "http://localhost:4000"}},
		},
	}

	if len(response.Providers) != 3 {
		t.Errorf("Providers length = %d, expected 3", len(response.Providers))
	}

	providerNames := make(map[string]bool)
	for _, p := range response.Providers {
		if providerNames[p.Name] {
			t.Errorf("duplicate provider name: %s", p.Name)
		}
		providerNames[p.Name] = true
	}
}

func TestAppSettingsResponse_ProviderTypes(t *testing.T) {
	tests := []struct {
		name       string
		provider   ProviderSettings
		expectType string
		expectName string
	}{
		{
			name:       "openai provider",
			provider:   ProviderSettings{Name: "openai-prod", Type: "openai", DisplayName: "OpenAI", Configured: true, Fields: map[string]string{"api_key": "****"}},
			expectType: "openai",
			expectName: "openai-prod",
		},
		{
			name:       "anthropic provider",
			provider:   ProviderSettings{Name: "anthropic-dev", Type: "anthropic", DisplayName: "Anthropic", Configured: true, Fields: map[string]string{"api_key": "****"}},
			expectType: "anthropic",
			expectName: "anthropic-dev",
		},
		{
			name:       "litellm with base_url",
			provider:   ProviderSettings{Name: "litellm-prod", Type: "litellm", DisplayName: "LiteLLM", Configured: true, Fields: map[string]string{"api_key": "****", "base_url": "http://localhost:4000"}},
			expectType: "litellm",
			expectName: "litellm-prod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.provider.Type != tt.expectType {
				t.Errorf("Type = %q, expected %q", tt.provider.Type, tt.expectType)
			}
			if tt.provider.Name != tt.expectName {
				t.Errorf("Name = %q, expected %q", tt.provider.Name, tt.expectName)
			}
		})
	}
}

func TestUpdateAppSettingsRequest_MultipleProviderUpdates(t *testing.T) {
	request := UpdateAppSettingsRequest{
		Providers: map[string]map[string]string{
			"openai-prod": {
				"type":    "openai",
				"api_key": "sk-prod-123",
			},
			"openai-dev": {
				"type":    "openai",
				"api_key": "sk-dev-456",
			},
			"anthropic-qa": {
				"type":    "anthropic",
				"api_key": "sk-ant-qa",
			},
		},
	}

	if len(request.Providers) != 3 {
		t.Errorf("Providers length = %d, expected 3", len(request.Providers))
	}

	expectedKeys := []string{"openai-prod", "openai-dev", "anthropic-qa"}
	for _, key := range expectedKeys {
		if _, ok := request.Providers[key]; !ok {
			t.Errorf("expected provider key %q not found", key)
		}
	}
}

func TestGeneralSettings_DefaultProviderDisplayName(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		displayName string
	}{
		{"openai provider", "openai-prod", "OpenAI"},
		{"anthropic provider", "anthropic-dev", "Anthropic"},
		{"litellm provider", "litellm-prod", "LiteLLM"},
		{"gemini provider", "gemini-prod", "Google GenAI"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := GeneralSettings{
				DefaultProvider:            tt.provider,
				DefaultProviderDisplayName: tt.displayName,
			}
			if settings.DefaultProviderDisplayName != tt.displayName {
				t.Errorf("DefaultProviderDisplayName = %q, expected %q", settings.DefaultProviderDisplayName, tt.displayName)
			}
		})
	}
}
