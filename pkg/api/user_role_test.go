package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// ---------------------------------------------------------------------------
// Mocks for handleSetUserOrgRole tests
// ---------------------------------------------------------------------------

// mockOrgStoreForRole implements store.OrganizationStore for role-change tests.
type mockOrgStoreForRole struct {
	orgs    map[string]*store.Organization // slug → org
	roles   map[string]string              // "userID:orgID" → role
	members []*store.UserWithRole

	// Tracking
	setMemberRoleCalled bool
	setMemberRoleArgs   struct{ userID, orgID, role string }
	setMemberRoleErr    error
}

func (m *mockOrgStoreForRole) GetBySlug(_ context.Context, slug string) (*store.Organization, error) {
	if org, ok := m.orgs[slug]; ok {
		return org, nil
	}
	return nil, nil
}

func (m *mockOrgStoreForRole) GetMemberRole(_ context.Context, userID, orgID string) (string, error) {
	key := userID + ":" + orgID
	if role, ok := m.roles[key]; ok {
		return role, nil
	}
	return "", fmt.Errorf("not a member")
}

func (m *mockOrgStoreForRole) ListMembers(_ context.Context, _ string) ([]*store.UserWithRole, error) {
	return m.members, nil
}

func (m *mockOrgStoreForRole) SetMemberRole(_ context.Context, userID, orgID, role string) error {
	m.setMemberRoleCalled = true
	m.setMemberRoleArgs = struct{ userID, orgID, role string }{userID, orgID, role}
	return m.setMemberRoleErr
}

// Unused methods — satisfy interface
func (m *mockOrgStoreForRole) Create(_ context.Context, _ *store.Organization) error { return nil }
func (m *mockOrgStoreForRole) GetByID(_ context.Context, _ string) (*store.Organization, error) {
	return nil, nil
}
func (m *mockOrgStoreForRole) List(_ context.Context) ([]*store.Organization, error) {
	return nil, nil
}
func (m *mockOrgStoreForRole) Update(_ context.Context, _ *store.Organization) error { return nil }
func (m *mockOrgStoreForRole) Delete(_ context.Context, _ string) error              { return nil }
func (m *mockOrgStoreForRole) Count(_ context.Context) (int, error)                  { return 0, nil }
func (m *mockOrgStoreForRole) AddMember(_ context.Context, _, _, _ string) error     { return nil }
func (m *mockOrgStoreForRole) RemoveMember(_ context.Context, _, _ string) error     { return nil }
func (m *mockOrgStoreForRole) GetUserOrgs(_ context.Context, _ string) ([]*store.OrgMembership, error) {
	return nil, nil
}

// mockPlatformBackendForRole satisfies store.PlatformBackend, routing only
// Organizations() to the mock. All other methods are no-ops or nil returns.
type mockPlatformBackendForRole struct {
	orgStore *mockOrgStoreForRole
}

func (m *mockPlatformBackendForRole) Organizations() store.OrganizationStore { return m.orgStore }
func (m *mockPlatformBackendForRole) Users() store.UserStore                 { return nil }
func (m *mockPlatformBackendForRole) LoginSessions() store.LoginSessionStore { return nil }
func (m *mockPlatformBackendForRole) OIDCProviders() store.OIDCProviderStore { return nil }
func (m *mockPlatformBackendForRole) UserChannels() store.UserChannelStore   { return nil }
func (m *mockPlatformBackendForRole) Close() error                           { return nil }
func (m *mockPlatformBackendForRole) ForOrg(_ string) (store.OrgDataStore, error) {
	return nil, nil
}
func (m *mockPlatformBackendForRole) ProvisionOrg(_ context.Context, _, _ string) error { return nil }
func (m *mockPlatformBackendForRole) DecommissionOrg(_ context.Context, _ string) error { return nil }
func (m *mockPlatformBackendForRole) InstanceSuffix() string                            { return "" }
func (m *mockPlatformBackendForRole) PlatformSettings() store.PlatformSettingsStore     { return nil }
func (m *mockPlatformBackendForRole) OrgSettings(_ string) store.OrgSettingsStore       { return nil }
func (m *mockPlatformBackendForRole) PlatformMCPServers() store.MCPServerStore          { return nil }
func (m *mockPlatformBackendForRole) PlatformSkills() store.SkillStore                  { return nil }
func (m *mockPlatformBackendForRole) PlatformSlideTemplates() store.SlideTemplateStore  { return nil }
func (m *mockPlatformBackendForRole) SetEmbedFunc(_ store.EmbedFunc)                    {}
func (m *mockPlatformBackendForRole) GetEmbedFunc() store.EmbedFunc                     { return nil }
func (m *mockPlatformBackendForRole) SandboxLayers() store.LayerStore                   { return nil }
func (m *mockPlatformBackendForRole) SandboxTemplates() store.SandboxTemplateStore      { return nil }
func (m *mockPlatformBackendForRole) SecretGetter() func(string) string                 { return nil }
func (m *mockPlatformBackendForRole) MigrateAll(_ context.Context) error                { return nil }
func (m *mockPlatformBackendForRole) CleanupExpired(_ context.Context) error            { return nil }
func (m *mockPlatformBackendForRole) PlatformDB() *sql.DB                               { return nil }
func (m *mockPlatformBackendForRole) MigrateAllSchemas(_ context.Context) error         { return nil }
func (m *mockPlatformBackendForRole) NewLinkCodeStore() store.LinkCodeStore             { return nil }

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

const (
	testOrgID    = "org-11111111-1111-1111-1111-111111111111"
	testOrgSlug  = "test-org"
	testCallerID = "caller-2222-2222-2222-222222222222"
	testTargetID = "target-3333-3333-3333-333333333333"
)

func newTestOrgStore() *mockOrgStoreForRole {
	return &mockOrgStoreForRole{
		orgs: map[string]*store.Organization{
			testOrgSlug: {ID: testOrgID, Slug: testOrgSlug, Name: "Test Org"},
		},
		roles: map[string]string{
			testTargetID + ":" + testOrgID: "member",
		},
		members: []*store.UserWithRole{
			{User: store.User{ID: testCallerID}, Role: "owner"},
			{User: store.User{ID: testTargetID}, Role: "member"},
		},
	}
}

func newTestPlatformAuth(orgStore *mockOrgStoreForRole) *PlatformAuth {
	backend := &mockPlatformBackendForRole{orgStore: orgStore}
	return &PlatformAuth{
		pgStore: backend,
	}
}

func roleRequest(callerRole, targetID, newRole string) (*http.Request, *httptest.ResponseRecorder) {
	body, _ := json.Marshal(map[string]string{"role": newRole})
	req := httptest.NewRequest("PUT", "/api/admin/users/"+targetID+"/role", bytes.NewReader(body))
	req = mux.SetURLVars(req, map[string]string{"id": targetID})

	caller := &PlatformUser{
		ID:      testCallerID,
		OrgSlug: testOrgSlug,
		Role:    callerRole,
	}
	ctx := WithPlatformUser(req.Context(), caller)
	req = req.WithContext(ctx)

	return req, httptest.NewRecorder()
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestSetUserOrgRole_AdminPromotesMemberToAdmin(t *testing.T) {
	orgStore := newTestOrgStore()
	pa := newTestPlatformAuth(orgStore)

	req, w := roleRequest("admin", testTargetID, "admin")
	pa.handleSetUserOrgRole(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !orgStore.setMemberRoleCalled {
		t.Fatal("expected SetMemberRole to be called")
	}
	if orgStore.setMemberRoleArgs.role != "admin" {
		t.Fatalf("expected role 'admin', got %q", orgStore.setMemberRoleArgs.role)
	}
	if orgStore.setMemberRoleArgs.userID != testTargetID {
		t.Fatalf("expected targetID %q, got %q", testTargetID, orgStore.setMemberRoleArgs.userID)
	}
}

func TestSetUserOrgRole_OwnerPromotesToOwner(t *testing.T) {
	orgStore := newTestOrgStore()
	pa := newTestPlatformAuth(orgStore)

	req, w := roleRequest("owner", testTargetID, "owner")
	pa.handleSetUserOrgRole(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !orgStore.setMemberRoleCalled {
		t.Fatal("expected SetMemberRole to be called")
	}
	if orgStore.setMemberRoleArgs.role != "owner" {
		t.Fatalf("expected role 'owner', got %q", orgStore.setMemberRoleArgs.role)
	}
}

func TestSetUserOrgRole_NonOwnerCannotPromoteToOwner(t *testing.T) {
	orgStore := newTestOrgStore()
	pa := newTestPlatformAuth(orgStore)

	req, w := roleRequest("admin", testTargetID, "owner")
	pa.handleSetUserOrgRole(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if orgStore.setMemberRoleCalled {
		t.Fatal("SetMemberRole should NOT be called")
	}
}

func TestSetUserOrgRole_CannotChangeSelf(t *testing.T) {
	orgStore := newTestOrgStore()
	pa := newTestPlatformAuth(orgStore)

	// Target is the same as the caller
	req, w := roleRequest("owner", testCallerID, "member")
	pa.handleSetUserOrgRole(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if orgStore.setMemberRoleCalled {
		t.Fatal("SetMemberRole should NOT be called")
	}
}

func TestSetUserOrgRole_InvalidRole(t *testing.T) {
	orgStore := newTestOrgStore()
	pa := newTestPlatformAuth(orgStore)

	req, w := roleRequest("owner", testTargetID, "superadmin")
	pa.handleSetUserOrgRole(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSetUserOrgRole_LastOwnerCannotBeDemoted(t *testing.T) {
	orgStore := newTestOrgStore()
	// Make target an owner and be the only owner
	orgStore.roles[testTargetID+":"+testOrgID] = "owner"
	orgStore.members = []*store.UserWithRole{
		{User: store.User{ID: testCallerID}, Role: "admin"},
		{User: store.User{ID: testTargetID}, Role: "owner"},
	}
	pa := newTestPlatformAuth(orgStore)

	// Caller is owner (we override to test the last-owner check)
	req, w := roleRequest("owner", testTargetID, "member")
	pa.handleSetUserOrgRole(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if orgStore.setMemberRoleCalled {
		t.Fatal("SetMemberRole should NOT be called")
	}
}

func TestSetUserOrgRole_NonOwnerCannotChangeOwnerRole(t *testing.T) {
	orgStore := newTestOrgStore()
	// Target is an owner
	orgStore.roles[testTargetID+":"+testOrgID] = "owner"
	orgStore.members = []*store.UserWithRole{
		{User: store.User{ID: testCallerID}, Role: "admin"},
		{User: store.User{ID: testTargetID}, Role: "owner"},
		{User: store.User{ID: "other-owner"}, Role: "owner"},
	}
	pa := newTestPlatformAuth(orgStore)

	// Admin tries to demote an owner
	req, w := roleRequest("admin", testTargetID, "member")
	pa.handleSetUserOrgRole(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if orgStore.setMemberRoleCalled {
		t.Fatal("SetMemberRole should NOT be called")
	}
}

func TestSetUserOrgRole_NonAdminForbidden(t *testing.T) {
	orgStore := newTestOrgStore()
	pa := newTestPlatformAuth(orgStore)

	// Caller is a regular member (not admin/owner)
	req, w := roleRequest("member", testTargetID, "admin")
	pa.handleSetUserOrgRole(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSetUserOrgRole_StoreError(t *testing.T) {
	orgStore := newTestOrgStore()
	orgStore.setMemberRoleErr = fmt.Errorf("constraint violation")
	pa := newTestPlatformAuth(orgStore)

	req, w := roleRequest("admin", testTargetID, "admin")
	pa.handleSetUserOrgRole(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSetUserOrgRole_Unauthenticated(t *testing.T) {
	orgStore := newTestOrgStore()
	pa := newTestPlatformAuth(orgStore)

	// No user in context
	body, _ := json.Marshal(map[string]string{"role": "admin"})
	req := httptest.NewRequest("PUT", "/api/admin/users/"+testTargetID+"/role", bytes.NewReader(body))
	req = mux.SetURLVars(req, map[string]string{"id": testTargetID})
	w := httptest.NewRecorder()

	pa.handleSetUserOrgRole(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}
