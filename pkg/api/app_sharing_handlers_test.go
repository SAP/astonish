package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// ---------------------------------------------------------------------------
// Mock implementations for app sharing tests
// ---------------------------------------------------------------------------

type mockAppStore struct {
	apps map[string]any
}

func newMockAppStore() *mockAppStore {
	return &mockAppStore{apps: make(map[string]any)}
}

func (m *mockAppStore) Save(_ context.Context, app any) (string, error) {
	// Extract slug from the map or use a default
	slug := "test-app"
	if appMap, ok := app.(map[string]any); ok {
		if s, ok := appMap["slug"].(string); ok && s != "" {
			slug = s
		}
	}
	m.apps[slug] = app
	return slug, nil
}

func (m *mockAppStore) Load(_ context.Context, slug string) (any, error) {
	app, ok := m.apps[slug]
	if !ok {
		return nil, &appNotFoundError{slug: slug}
	}
	return app, nil
}

func (m *mockAppStore) Delete(_ context.Context, slug string) error {
	delete(m.apps, slug)
	return nil
}

func (m *mockAppStore) List(_ context.Context) ([]store.AppListItem, error) {
	items := make([]store.AppListItem, 0, len(m.apps))
	for name := range m.apps {
		items = append(items, store.AppListItem{Name: name})
	}
	return items, nil
}

type appNotFoundError struct{ slug string }

func (e *appNotFoundError) Error() string { return "app not found: " + e.slug }

// mockOrgDataStore implements store.OrgDataStore for testing
type mockOrgDataStore struct {
	teams    map[string]*mockTeamDataStore
	users    map[string]*mockPersonalDataStore
	orgApps  *mockAppStore
	memories store.MemoryStore
}

func newMockOrgDataStore() *mockOrgDataStore {
	return &mockOrgDataStore{
		teams:   make(map[string]*mockTeamDataStore),
		users:   make(map[string]*mockPersonalDataStore),
		orgApps: newMockAppStore(),
	}
}

func (m *mockOrgDataStore) ForTeam(teamSlug string) store.TeamDataStore {
	if ds, ok := m.teams[teamSlug]; ok {
		return ds
	}
	ds := newMockTeamDataStore()
	m.teams[teamSlug] = ds
	return ds
}

func (m *mockOrgDataStore) ForUser(userID string) store.PersonalDataStore {
	if ds, ok := m.users[userID]; ok {
		return ds
	}
	ds := newMockPersonalDataStore()
	m.users[userID] = ds
	return ds
}

func (m *mockOrgDataStore) OrgMemories() store.MemoryStore               { return m.memories }
func (m *mockOrgDataStore) OrgSkills() store.SkillStore                  { return nil }
func (m *mockOrgDataStore) OrgSlideTemplates() store.SlideTemplateStore  { return nil }
func (m *mockOrgDataStore) OrgMCPServers() store.MCPServerStore          { return nil }
func (m *mockOrgDataStore) OrgNetworkPolicies() store.NetworkPolicyStore { return nil }
func (m *mockOrgDataStore) OrgApps() store.AppStore                      { return m.orgApps }
func (m *mockOrgDataStore) OrgAudit() store.AuditStore                   { return nil }
func (m *mockOrgDataStore) OrgA2AAgents() store.A2AAgentStore            { return nil }
func (m *mockOrgDataStore) Teams() store.TeamManagementStore {
	return nil
}
func (m *mockOrgDataStore) ProvisionTeam(_ context.Context, _ string) error {
	return nil
}
func (m *mockOrgDataStore) ProvisionPersonalSchema(_ context.Context, _ string) error {
	return nil
}
func (m *mockOrgDataStore) DropTeamSchema(_ context.Context, _ string) error {
	return nil
}
func (m *mockOrgDataStore) Close() error { return nil }

// mockTeamDataStore implements store.TeamDataStore
type mockTeamDataStore struct {
	apps     *mockAppStore
	appState store.AppStateStore
	appPins  map[string]*store.AppPin
}

func newMockTeamDataStore() *mockTeamDataStore {
	return &mockTeamDataStore{apps: newMockAppStore(), appPins: make(map[string]*store.AppPin)}
}

func (m *mockTeamDataStore) Sessions() store.SessionStore       { return nil }
func (m *mockTeamDataStore) Memories() store.MemoryStore        { return nil }
func (m *mockTeamDataStore) Credentials() store.CredentialStore { return nil }
func (m *mockTeamDataStore) Docs() store.DocsStore              { return nil }
func (m *mockTeamDataStore) Apps() store.AppStore               { return m.apps }
func (m *mockTeamDataStore) AppState() store.AppStateStore      { return m.appState }
func (m *mockTeamDataStore) Flows() store.FlowStore             { return nil }
func (m *mockTeamDataStore) ScheduledJobs() store.SchedulerStore {
	return nil
}
func (m *mockTeamDataStore) Skills() store.SkillStore                  { return nil }
func (m *mockTeamDataStore) MCPServers() store.MCPServerStore          { return nil }
func (m *mockTeamDataStore) NetworkPolicies() store.NetworkPolicyStore { return nil }
func (m *mockTeamDataStore) FleetTemplates() store.FleetTemplateStore  { return nil }
func (m *mockTeamDataStore) FleetPlans() store.FleetPlanStore          { return nil }
func (m *mockTeamDataStore) FleetSetupProfiles() store.FleetSetupProfileStore {
	return nil
}
func (m *mockTeamDataStore) FleetSetupDrafts() store.FleetSetupDraftStore {
	return nil
}
func (m *mockTeamDataStore) FleetRunStates() store.FleetRunStateStore { return nil }
func (m *mockTeamDataStore) FleetMailbox() store.FleetMailboxStore    { return nil }
func (m *mockTeamDataStore) FleetTaskBoard() store.FleetTaskBoardStore {
	return nil
}
func (m *mockTeamDataStore) DrillReports() store.DrillReportStore { return nil }
func (m *mockTeamDataStore) Settings() store.SettingsStore        { return nil }
func (m *mockTeamDataStore) Audit() store.AuditStore              { return nil }
func (m *mockTeamDataStore) AppStateSQL() store.AppStateSQLStore  { return nil }
func (m *mockTeamDataStore) A2AAgents() store.A2AAgentStore       { return nil }

func (m *mockTeamDataStore) SessionPin(_ context.Context, _ string) (*store.SessionPin, error) {
	return nil, nil
}
func (m *mockTeamDataStore) SetSessionPin(_ context.Context, _, _, _ string) error { return nil }
func (m *mockTeamDataStore) AppPin(_ context.Context, slug string) (*store.AppPin, error) {
	if pin, ok := m.appPins[slug]; ok {
		return pin, nil
	}
	return nil, nil
}
func (m *mockTeamDataStore) SetAppPin(_ context.Context, slug, provider, model string) error {
	if _, ok := m.apps.apps[slug]; !ok {
		return &appNotFoundError{slug: slug}
	}
	if provider == "" && model == "" {
		delete(m.appPins, slug)
		return nil
	}
	m.appPins[slug] = &store.AppPin{Provider: provider, Model: model}
	return nil
}

// mockPersonalDataStore implements store.PersonalDataStore
type mockPersonalDataStore struct {
	apps        *mockAppStore
	appState    store.AppStateStore
	appPins     map[string]*store.AppPin
	sessionPins map[string]*store.SessionPin
}

func newMockPersonalDataStore() *mockPersonalDataStore {
	return &mockPersonalDataStore{
		apps:        newMockAppStore(),
		appPins:     make(map[string]*store.AppPin),
		sessionPins: make(map[string]*store.SessionPin),
	}
}

func (m *mockPersonalDataStore) Memories() store.MemoryStore         { return nil }
func (m *mockPersonalDataStore) Docs() store.DocsStore               { return nil }
func (m *mockPersonalDataStore) Apps() store.AppStore                { return m.apps }
func (m *mockPersonalDataStore) Sessions() store.SessionStore        { return nil }
func (m *mockPersonalDataStore) AppState() store.AppStateStore       { return m.appState }
func (m *mockPersonalDataStore) Flows() store.FlowStore              { return nil }
func (m *mockPersonalDataStore) Credentials() store.CredentialStore  { return nil }
func (m *mockPersonalDataStore) ScheduledJobs() store.SchedulerStore { return nil }

func (m *mockPersonalDataStore) PersonalSettings() store.PersonalSettingsStore { return nil }
func (m *mockPersonalDataStore) SessionPin(_ context.Context, sessionID string) (*store.SessionPin, error) {
	if pin, ok := m.sessionPins[sessionID]; ok {
		return pin, nil
	}
	// Match entstore: unknown session → not found (so callers fall through to team).
	return nil, fmt.Errorf("session pin: session %q not found", sessionID)
}
func (m *mockPersonalDataStore) SetSessionPin(_ context.Context, sessionID, provider, model string) error {
	if m.sessionPins == nil {
		m.sessionPins = make(map[string]*store.SessionPin)
	}
	m.sessionPins[sessionID] = &store.SessionPin{Provider: provider, Model: model}
	return nil
}
func (m *mockPersonalDataStore) AppPin(_ context.Context, slug string) (*store.AppPin, error) {
	if pin, ok := m.appPins[slug]; ok {
		return pin, nil
	}
	return nil, nil
}
func (m *mockPersonalDataStore) SetAppPin(_ context.Context, slug, provider, model string) error {
	if _, ok := m.apps.apps[slug]; !ok {
		return &appNotFoundError{slug: slug}
	}
	if provider == "" && model == "" {
		delete(m.appPins, slug)
		return nil
	}
	m.appPins[slug] = &store.AppPin{Provider: provider, Model: model}
	return nil
}

// mockTenantRouter implements store.TenantRouter
type mockTenantRouter struct {
	orgs map[string]*mockOrgDataStore
}

func newMockTenantRouter() *mockTenantRouter {
	return &mockTenantRouter{orgs: make(map[string]*mockOrgDataStore)}
}

func (m *mockTenantRouter) ForOrg(orgID string) (store.OrgDataStore, error) {
	if org, ok := m.orgs[orgID]; ok {
		return org, nil
	}
	org := newMockOrgDataStore()
	m.orgs[orgID] = org
	return org, nil
}

func (m *mockTenantRouter) ProvisionOrg(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockTenantRouter) DecommissionOrg(_ context.Context, _ string) error {
	return nil
}

// ---------------------------------------------------------------------------
// Helper: build a request with platform context
// ---------------------------------------------------------------------------

func appSharingRequest(t *testing.T, method, path string, body any, svc *store.Services, pu *PlatformUser) *http.Request {
	t.Helper()
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(bodyBytes))
	r.Header.Set("Content-Type", "application/json")
	ctx := store.WithServices(r.Context(), svc)
	if pu != nil {
		ctx = WithPlatformUser(ctx, pu)
	}
	r = r.WithContext(ctx)
	return r
}

// ---------------------------------------------------------------------------
// 6.9a: Publish → Fork lifecycle
// ---------------------------------------------------------------------------

func TestAppPublishToTeam(t *testing.T) {
	t.Parallel()

	router := newMockTenantRouter()
	orgStore := newMockOrgDataStore()
	router.orgs["acme"] = orgStore

	// Seed a personal app
	personalApps := orgStore.ForUser("user-1").(*mockPersonalDataStore).apps
	personalApps.apps["weather-app"] = map[string]any{
		"slug": "weather-app",
		"code": "console.log('weather')",
	}

	teamApps := newMockAppStore()

	svc := &store.Services{
		Mode:         store.ModePlatform,
		TenantRouter: router,
		Apps:         teamApps,
	}

	pu := &PlatformUser{ID: "user-1", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}

	r := appSharingRequest(t, "POST", "/api/apps/publish", AppPublishRequest{Slug: "weather-app"}, svc, pu)
	w := httptest.NewRecorder()

	AppPublishToTeamHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["published"] != true {
		t.Errorf("expected published=true, got %v", resp["published"])
	}
	if resp["scope"] != "team" {
		t.Errorf("expected scope=team, got %v", resp["scope"])
	}

	// Verify the app now exists in the team store
	if _, err := teamApps.Load(context.Background(), "weather-app"); err != nil {
		t.Errorf("app should exist in team store after publish: %v", err)
	}
}

func TestAppPublishToTeam_NotFound(t *testing.T) {
	t.Parallel()

	router := newMockTenantRouter()
	svc := &store.Services{
		Mode:         store.ModePlatform,
		TenantRouter: router,
		Apps:         newMockAppStore(),
	}
	pu := &PlatformUser{ID: "user-1", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}

	r := appSharingRequest(t, "POST", "/api/apps/publish", AppPublishRequest{Slug: "nonexistent"}, svc, pu)
	w := httptest.NewRecorder()

	AppPublishToTeamHandler(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing personal app, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAppForkFromTeam(t *testing.T) {
	t.Parallel()

	router := newMockTenantRouter()
	orgStore := newMockOrgDataStore()
	router.orgs["acme"] = orgStore

	// Seed a team app
	teamApps := newMockAppStore()
	teamApps.apps["dashboard"] = map[string]any{
		"slug": "dashboard",
		"code": "console.log('dashboard')",
	}

	svc := &store.Services{
		Mode:         store.ModePlatform,
		TenantRouter: router,
		Apps:         teamApps,
	}
	pu := &PlatformUser{ID: "user-1", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}

	r := appSharingRequest(t, "POST", "/api/apps/fork", AppForkRequest{Slug: "dashboard", Source: "team"}, svc, pu)
	w := httptest.NewRecorder()

	AppForkToPersonalHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["forked"] != true {
		t.Errorf("expected forked=true, got %v", resp["forked"])
	}
	if resp["source"] != "team" {
		t.Errorf("expected source=team, got %v", resp["source"])
	}

	// Verify the app now exists in the user's personal store
	personalApps := orgStore.ForUser("user-1").(*mockPersonalDataStore).apps
	if _, err := personalApps.Load(context.Background(), "dashboard"); err != nil {
		t.Errorf("app should exist in personal store after fork: %v", err)
	}
}

func TestAppForkFromOrg(t *testing.T) {
	t.Parallel()

	router := newMockTenantRouter()
	orgStore := newMockOrgDataStore()
	router.orgs["acme"] = orgStore

	// Seed an org app
	orgStore.orgApps.apps["company-tools"] = map[string]any{
		"slug": "company-tools",
		"code": "console.log('tools')",
	}

	svc := &store.Services{
		Mode:         store.ModePlatform,
		TenantRouter: router,
	}
	pu := &PlatformUser{ID: "user-2", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}

	r := appSharingRequest(t, "POST", "/api/apps/fork", AppForkRequest{Slug: "company-tools", Source: "org"}, svc, pu)
	w := httptest.NewRecorder()

	AppForkToPersonalHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the app now exists in the user's personal store
	personalApps := orgStore.ForUser("user-2").(*mockPersonalDataStore).apps
	if _, err := personalApps.Load(context.Background(), "company-tools"); err != nil {
		t.Errorf("app should exist in personal store after fork: %v", err)
	}
}

func TestAppForkInvalidSource(t *testing.T) {
	t.Parallel()

	svc := &store.Services{Mode: store.ModePlatform, TenantRouter: newMockTenantRouter()}
	pu := &PlatformUser{ID: "user-1", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}

	r := appSharingRequest(t, "POST", "/api/apps/fork", AppForkRequest{Slug: "app", Source: "invalid"}, svc, pu)
	w := httptest.NewRecorder()

	AppForkToPersonalHandler(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid source, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// 6.9b: Promote team app to org (admin-only)
// ---------------------------------------------------------------------------

func TestAppPromoteToOrg_Admin(t *testing.T) {
	t.Parallel()

	router := newMockTenantRouter()
	orgStore := newMockOrgDataStore()
	router.orgs["acme"] = orgStore

	// Seed a team app
	teamDS := newMockTeamDataStore()
	teamDS.apps.apps["analytics"] = map[string]any{
		"slug": "analytics",
		"code": "console.log('analytics')",
	}
	orgStore.teams["data-team"] = teamDS

	svc := &store.Services{
		Mode:         store.ModePlatform,
		TenantRouter: router,
	}
	pu := &PlatformUser{ID: "admin-1", OrgSlug: "acme", TeamSlug: "eng", Role: "admin"}

	r := appSharingRequest(t, "POST", "/api/apps/promote", AppPromoteRequest{Slug: "analytics", TeamSlug: "data-team"}, svc, pu)
	w := httptest.NewRecorder()

	AppPromoteToOrgHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["promoted"] != true {
		t.Errorf("expected promoted=true, got %v", resp["promoted"])
	}
	if resp["scope"] != "org" {
		t.Errorf("expected scope=org, got %v", resp["scope"])
	}

	// Verify the app exists in org store
	if _, err := orgStore.orgApps.Load(context.Background(), "analytics"); err != nil {
		t.Errorf("app should exist in org store after promotion: %v", err)
	}
}

func TestAppPromoteToOrg_NonAdmin(t *testing.T) {
	t.Parallel()

	svc := &store.Services{Mode: store.ModePlatform, TenantRouter: newMockTenantRouter()}
	pu := &PlatformUser{ID: "user-1", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}

	r := appSharingRequest(t, "POST", "/api/apps/promote", AppPromoteRequest{Slug: "app", TeamSlug: "team"}, svc, pu)
	w := httptest.NewRecorder()

	AppPromoteToOrgHandler(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAppPromoteToOrg_PersonalMode(t *testing.T) {
	t.Parallel()

	svc := &store.Services{Mode: "personal"}
	r := appSharingRequest(t, "POST", "/api/apps/promote", AppPromoteRequest{Slug: "app", TeamSlug: "team"}, svc, nil)
	w := httptest.NewRecorder()

	AppPromoteToOrgHandler(w, r)

	// RequirePlatformServices returns 503 for personal mode (feature unavailable)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for personal mode, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// 6.9c: Org app listing and deletion
// ---------------------------------------------------------------------------

func TestListOrgApps(t *testing.T) {
	t.Parallel()

	router := newMockTenantRouter()
	orgStore := newMockOrgDataStore()
	router.orgs["acme"] = orgStore

	orgStore.orgApps.apps["app-a"] = map[string]any{"slug": "app-a"}
	orgStore.orgApps.apps["app-b"] = map[string]any{"slug": "app-b"}

	svc := &store.Services{Mode: store.ModePlatform, TenantRouter: router}
	pu := &PlatformUser{ID: "user-1", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}

	r := appSharingRequest(t, "GET", "/api/apps/org", nil, svc, pu)
	w := httptest.NewRecorder()

	ListOrgAppsHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	apps, ok := resp["apps"].([]any)
	if !ok {
		t.Fatal("expected apps array in response")
	}
	if len(apps) != 2 {
		t.Errorf("expected 2 org apps, got %d", len(apps))
	}
}

func TestDeleteOrgApp_Admin(t *testing.T) {
	t.Parallel()

	router := newMockTenantRouter()
	orgStore := newMockOrgDataStore()
	router.orgs["acme"] = orgStore
	orgStore.orgApps.apps["old-app"] = map[string]any{"slug": "old-app"}

	svc := &store.Services{Mode: store.ModePlatform, TenantRouter: router}
	pu := &PlatformUser{ID: "admin-1", OrgSlug: "acme", TeamSlug: "eng", Role: "admin"}

	r := appSharingRequest(t, "DELETE", "/api/apps/org/old-app", nil, svc, pu)
	r = mux.SetURLVars(r, map[string]string{"name": "old-app"})
	w := httptest.NewRecorder()

	DeleteOrgAppHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify deleted
	if _, err := orgStore.orgApps.Load(context.Background(), "old-app"); err == nil {
		t.Error("app should have been deleted from org store")
	}
}

func TestDeleteOrgApp_NonAdmin(t *testing.T) {
	t.Parallel()

	svc := &store.Services{Mode: store.ModePlatform, TenantRouter: newMockTenantRouter()}
	pu := &PlatformUser{ID: "user-1", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}

	r := appSharingRequest(t, "DELETE", "/api/apps/org/some-app", nil, svc, pu)
	r = mux.SetURLVars(r, map[string]string{"name": "some-app"})
	w := httptest.NewRecorder()

	DeleteOrgAppHandler(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// 6.9d: Full lifecycle: personal → publish → fork → promote
// ---------------------------------------------------------------------------

func TestAppSharingFullLifecycle(t *testing.T) {
	t.Parallel()

	router := newMockTenantRouter()
	orgStore := newMockOrgDataStore()
	router.orgs["acme"] = orgStore

	// Team store shared across handlers
	teamApps := newMockAppStore()

	admin := &PlatformUser{ID: "admin-1", OrgSlug: "acme", TeamSlug: "eng", Role: "admin"}
	member := &PlatformUser{ID: "user-1", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}

	// 1. Member creates a personal app
	personalApps := orgStore.ForUser("user-1").(*mockPersonalDataStore).apps
	personalApps.apps["my-tool"] = map[string]any{
		"slug": "my-tool",
		"code": "function run() { return 'hello' }",
	}

	// 2. Member publishes to team
	svcWithTeam := &store.Services{
		Mode:         store.ModePlatform,
		TenantRouter: router,
		Apps:         teamApps,
	}
	r := appSharingRequest(t, "POST", "/api/apps/publish", AppPublishRequest{Slug: "my-tool"}, svcWithTeam, member)
	w := httptest.NewRecorder()
	AppPublishToTeamHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("step 2 (publish) failed: %d: %s", w.Code, w.Body.String())
	}

	// 3. Another user forks from team to personal
	svcForFork := &store.Services{
		Mode:         store.ModePlatform,
		TenantRouter: router,
		Apps:         teamApps,
	}
	user2 := &PlatformUser{ID: "user-2", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}
	r = appSharingRequest(t, "POST", "/api/apps/fork", AppForkRequest{Slug: "my-tool", Source: "team"}, svcForFork, user2)
	w = httptest.NewRecorder()
	AppForkToPersonalHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("step 3 (fork) failed: %d: %s", w.Code, w.Body.String())
	}

	// Verify user-2 has a personal copy
	user2Personal := orgStore.ForUser("user-2").(*mockPersonalDataStore).apps
	if _, err := user2Personal.Load(context.Background(), "my-tool"); err != nil {
		t.Errorf("user-2 should have forked copy: %v", err)
	}

	// 4. Admin promotes team app to org
	// Set up the team in the org store so ForTeam("eng") returns the right store
	orgStore.teams["eng"] = &mockTeamDataStore{apps: teamApps}

	svcForPromote := &store.Services{
		Mode:         store.ModePlatform,
		TenantRouter: router,
	}
	r = appSharingRequest(t, "POST", "/api/apps/promote", AppPromoteRequest{Slug: "my-tool", TeamSlug: "eng"}, svcForPromote, admin)
	w = httptest.NewRecorder()
	AppPromoteToOrgHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("step 4 (promote) failed: %d: %s", w.Code, w.Body.String())
	}

	// Verify the app exists at org level
	if _, err := orgStore.orgApps.Load(context.Background(), "my-tool"); err != nil {
		t.Errorf("app should exist in org store after promotion: %v", err)
	}

	// 5. Another user forks from org
	user3 := &PlatformUser{ID: "user-3", OrgSlug: "acme", TeamSlug: "eng", Role: "member"}
	r = appSharingRequest(t, "POST", "/api/apps/fork", AppForkRequest{Slug: "my-tool", Source: "org"}, &store.Services{
		Mode:         store.ModePlatform,
		TenantRouter: router,
	}, user3)
	w = httptest.NewRecorder()
	AppForkToPersonalHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("step 5 (fork from org) failed: %d: %s", w.Code, w.Body.String())
	}

	user3Personal := orgStore.ForUser("user-3").(*mockPersonalDataStore).apps
	if _, err := user3Personal.Load(context.Background(), "my-tool"); err != nil {
		t.Errorf("user-3 should have forked copy from org: %v", err)
	}
}
