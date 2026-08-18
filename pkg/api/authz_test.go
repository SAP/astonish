package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// ---------------------------------------------------------------------------
// Mocks for authz tests
// ---------------------------------------------------------------------------

// mockTeamMgmt implements store.TeamManagementStore for testing team access control.
type mockTeamMgmt struct {
	teams       map[string]*store.Team // slug → team
	memberRoles map[string]string      // "userID:teamID" → role
	members     map[string]bool        // "userID:teamSlug" → isMember
}

func newMockTeamMgmt() *mockTeamMgmt {
	return &mockTeamMgmt{
		teams:       make(map[string]*store.Team),
		memberRoles: make(map[string]string),
		members:     make(map[string]bool),
	}
}

func (m *mockTeamMgmt) CreateTeam(_ context.Context, _ *store.Team) error { return nil }
func (m *mockTeamMgmt) GetTeam(_ context.Context, _ string) (*store.Team, error) {
	return nil, nil
}
func (m *mockTeamMgmt) GetTeamBySlug(_ context.Context, slug string) (*store.Team, error) {
	if t, ok := m.teams[slug]; ok {
		return t, nil
	}
	return nil, fmt.Errorf("team not found: %s", slug)
}
func (m *mockTeamMgmt) ListTeams(_ context.Context) ([]*store.Team, error)  { return nil, nil }
func (m *mockTeamMgmt) ListTeamsForUser(_ context.Context, _ string) ([]*store.Team, error) {
	return nil, nil
}
func (m *mockTeamMgmt) DeleteTeam(_ context.Context, _ string) error { return nil }
func (m *mockTeamMgmt) RenameTeam(_ context.Context, _, _ string) error { return nil }
func (m *mockTeamMgmt) CountTeams(_ context.Context) (int, error)      { return len(m.teams), nil }
func (m *mockTeamMgmt) CountMembers(_ context.Context, _ string) (int, error) { return 0, nil }
func (m *mockTeamMgmt) AddMember(_ context.Context, _ *store.TeamMembership) error {
	return nil
}
func (m *mockTeamMgmt) RemoveMember(_ context.Context, _, _ string) error { return nil }
func (m *mockTeamMgmt) SetRole(_ context.Context, _, _, _ string) error   { return nil }
func (m *mockTeamMgmt) ListMembers(_ context.Context, _ string) ([]*store.TeamMembership, error) {
	return nil, nil
}
func (m *mockTeamMgmt) GetUserTeams(_ context.Context, _ string) ([]*store.TeamMembership, error) {
	return nil, nil
}
func (m *mockTeamMgmt) IsTeamMember(_ context.Context, userID, teamSlug string) (bool, error) {
	return m.members[userID+":"+teamSlug], nil
}
func (m *mockTeamMgmt) GetMemberRole(_ context.Context, userID, teamID string) (string, error) {
	role, ok := m.memberRoles[userID+":"+teamID]
	if !ok {
		return "", fmt.Errorf("no membership found")
	}
	return role, nil
}

// authzOrgDataStore wraps mockOrgDataStore but with a real TeamManagementStore mock.
type authzOrgDataStore struct {
	teams *mockTeamMgmt
}

func (m *authzOrgDataStore) ForTeam(_ string) store.TeamDataStore     { return nil }
func (m *authzOrgDataStore) ForUser(_ string) store.PersonalDataStore { return nil }
func (m *authzOrgDataStore) OrgMemories() store.MemoryStore           { return nil }
func (m *authzOrgDataStore) OrgSkills() store.SkillStore              { return nil }
func (m *authzOrgDataStore) OrgMCPServers() store.MCPServerStore      { return nil }
func (m *authzOrgDataStore) OrgNetworkPolicies() store.NetworkPolicyStore { return nil }
func (m *authzOrgDataStore) OrgApps() store.AppStore                  { return nil }
func (m *authzOrgDataStore) OrgAudit() store.AuditStore               { return nil }
func (m *authzOrgDataStore) OrgA2AAgents() store.A2AAgentStore        { return nil }
func (m *authzOrgDataStore) Teams() store.TeamManagementStore         { return m.teams }
func (m *authzOrgDataStore) ProvisionTeam(_ context.Context, _ string) error {
	return nil
}
func (m *authzOrgDataStore) ProvisionPersonalSchema(_ context.Context, _ string) error {
	return nil
}
func (m *authzOrgDataStore) DropTeamSchema(_ context.Context, _ string) error {
	return nil
}
func (m *authzOrgDataStore) Close() error { return nil }

// authzTenantRouter only serves known orgs; returns error for unknown.
type authzTenantRouter struct {
	orgs map[string]store.OrgDataStore
}

func (m *authzTenantRouter) ForOrg(orgSlug string) (store.OrgDataStore, error) {
	if ds, ok := m.orgs[orgSlug]; ok {
		return ds, nil
	}
	return nil, fmt.Errorf("org not found: %s", orgSlug)
}
func (m *authzTenantRouter) ProvisionOrg(_ context.Context, _, _ string) error {
	return nil
}
func (m *authzTenantRouter) DecommissionOrg(_ context.Context, _ string) error {
	return nil
}

// ---------------------------------------------------------------------------
// Helper: build an HTTP request with full context for authz tests
// ---------------------------------------------------------------------------

func authzRequest(user *PlatformUser, svc *store.Services, tc *store.TenantContext) *http.Request {
	req := httptest.NewRequest("GET", "/api/test", nil)
	ctx := req.Context()
	if user != nil {
		ctx = WithPlatformUser(ctx, user)
	}
	if svc != nil {
		ctx = store.WithServices(ctx, svc)
	}
	if tc != nil {
		ctx = store.WithTenantContext(ctx, tc)
	}
	return req.WithContext(ctx)
}

// ---------------------------------------------------------------------------
// Tests: RequireAuth
// ---------------------------------------------------------------------------

func TestRequireAuth(t *testing.T) {
	t.Parallel()

	t.Run("missing user returns 401", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := authzRequest(nil, nil, nil)
		result := RequireAuth(w, r)
		if result != nil {
			t.Error("expected nil, got user")
		}
		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", w.Code)
		}
	})

	t.Run("authenticated user passes", func(t *testing.T) {
		w := httptest.NewRecorder()
		user := &PlatformUser{ID: "u1", Role: "member"}
		r := authzRequest(user, nil, nil)
		result := RequireAuth(w, r)
		if result == nil {
			t.Fatal("expected user, got nil")
		}
		if result.ID != "u1" {
			t.Errorf("expected user ID u1, got %s", result.ID)
		}
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// Tests: RequirePlatformAdmin
// ---------------------------------------------------------------------------

func TestRequirePlatformAdmin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		user       *PlatformUser
		expectNil  bool
		expectCode int
	}{
		{
			name:       "no user → 401",
			user:       nil,
			expectNil:  true,
			expectCode: http.StatusUnauthorized,
		},
		{
			name:       "regular member → 403",
			user:       &PlatformUser{ID: "u1", Role: "member", PlatformRole: ""},
			expectNil:  true,
			expectCode: http.StatusForbidden,
		},
		{
			name:       "org admin → 403",
			user:       &PlatformUser{ID: "u2", Role: "admin", PlatformRole: ""},
			expectNil:  true,
			expectCode: http.StatusForbidden,
		},
		{
			name:       "org owner → 403",
			user:       &PlatformUser{ID: "u3", Role: "owner", PlatformRole: ""},
			expectNil:  true,
			expectCode: http.StatusForbidden,
		},
		{
			name:       "superadmin → passes",
			user:       &PlatformUser{ID: "u4", Role: "owner", PlatformRole: "superadmin"},
			expectNil:  false,
			expectCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := authzRequest(tt.user, nil, nil)
			result := RequirePlatformAdmin(w, r)
			if tt.expectNil && result != nil {
				t.Error("expected nil, got user")
			}
			if !tt.expectNil && result == nil {
				t.Error("expected user, got nil")
			}
			if w.Code != tt.expectCode {
				t.Errorf("expected %d, got %d", tt.expectCode, w.Code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: RequireOrgAdmin
// ---------------------------------------------------------------------------

func TestRequireOrgAdmin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		user       *PlatformUser
		expectNil  bool
		expectCode int
	}{
		{
			name:       "no user → 401",
			user:       nil,
			expectNil:  true,
			expectCode: http.StatusUnauthorized,
		},
		{
			name:       "member → 403",
			user:       &PlatformUser{ID: "u1", Role: "member"},
			expectNil:  true,
			expectCode: http.StatusForbidden,
		},
		{
			name:       "admin → passes",
			user:       &PlatformUser{ID: "u2", Role: "admin"},
			expectNil:  false,
			expectCode: http.StatusOK,
		},
		{
			name:       "owner → passes",
			user:       &PlatformUser{ID: "u3", Role: "owner"},
			expectNil:  false,
			expectCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := authzRequest(tt.user, nil, nil)
			result := RequireOrgAdmin(w, r)
			if tt.expectNil && result != nil {
				t.Error("expected nil, got user")
			}
			if !tt.expectNil && result == nil {
				t.Error("expected user, got nil")
			}
			if w.Code != tt.expectCode {
				t.Errorf("expected %d, got %d", tt.expectCode, w.Code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: RequireOrgOwner
// ---------------------------------------------------------------------------

func TestRequireOrgOwner(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		user       *PlatformUser
		expectNil  bool
		expectCode int
	}{
		{
			name:       "no user → 401",
			user:       nil,
			expectNil:  true,
			expectCode: http.StatusUnauthorized,
		},
		{
			name:       "member → 403",
			user:       &PlatformUser{ID: "u1", Role: "member"},
			expectNil:  true,
			expectCode: http.StatusForbidden,
		},
		{
			name:       "admin → 403",
			user:       &PlatformUser{ID: "u2", Role: "admin"},
			expectNil:  true,
			expectCode: http.StatusForbidden,
		},
		{
			name:       "owner → passes",
			user:       &PlatformUser{ID: "u3", Role: "owner"},
			expectNil:  false,
			expectCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := authzRequest(tt.user, nil, nil)
			result := RequireOrgOwner(w, r)
			if tt.expectNil && result != nil {
				t.Error("expected nil, got user")
			}
			if !tt.expectNil && result == nil {
				t.Error("expected user, got nil")
			}
			if w.Code != tt.expectCode {
				t.Errorf("expected %d, got %d", tt.expectCode, w.Code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: RequireTeamAdmin
// ---------------------------------------------------------------------------

func TestRequireTeamAdmin(t *testing.T) {
	t.Parallel()

	// Setup: org "acme" with team "engineering" (ID: "team-1")
	// User "u-member" is a team member (role "member")
	// User "u-team-admin" is a team admin (role "admin")
	teamMgmt := newMockTeamMgmt()
	teamMgmt.teams["engineering"] = &store.Team{ID: "team-1", Slug: "engineering"}
	teamMgmt.memberRoles["u-team-admin:team-1"] = "admin"
	teamMgmt.memberRoles["u-member:team-1"] = "member"

	orgDS := &authzOrgDataStore{teams: teamMgmt}
	router := &authzTenantRouter{orgs: map[string]store.OrgDataStore{"acme": orgDS}}
	svc := &store.Services{Mode: store.ModePlatform, TenantRouter: router}
	tc := &store.TenantContext{OrgSlug: "acme", TeamSlug: "engineering"}

	tests := []struct {
		name       string
		user       *PlatformUser
		expectPass bool
		expectCode int
	}{
		{
			name:       "personal mode always passes",
			user:       &PlatformUser{ID: "anyone", Role: "member"},
			expectPass: true,
			expectCode: http.StatusOK,
		},
		{
			name:       "no user → 401",
			user:       nil,
			expectPass: false,
			expectCode: http.StatusUnauthorized,
		},
		{
			name:       "org owner → passes (bypass)",
			user:       &PlatformUser{ID: "u-owner", Role: "owner"},
			expectPass: true,
			expectCode: http.StatusOK,
		},
		{
			name:       "org admin → passes (bypass)",
			user:       &PlatformUser{ID: "u-admin", Role: "admin"},
			expectPass: true,
			expectCode: http.StatusOK,
		},
		{
			name:       "team admin → passes",
			user:       &PlatformUser{ID: "u-team-admin", Role: "member"},
			expectPass: true,
			expectCode: http.StatusOK,
		},
		{
			name:       "regular team member → 403",
			user:       &PlatformUser{ID: "u-member", Role: "member"},
			expectPass: false,
			expectCode: http.StatusForbidden,
		},
		{
			name:       "unknown user → 403",
			user:       &PlatformUser{ID: "u-unknown", Role: "member"},
			expectPass: false,
			expectCode: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			var r *http.Request

			if tt.name == "personal mode always passes" {
				// Personal mode — no platform services
				personalSvc := &store.Services{Mode: "personal"}
				r = authzRequest(tt.user, personalSvc, nil)
			} else {
				r = authzRequest(tt.user, svc, tc)
			}

			result := RequireTeamAdmin(w, r)
			if result != tt.expectPass {
				t.Errorf("RequireTeamAdmin() = %v, want %v", result, tt.expectPass)
			}
			if w.Code != tt.expectCode {
				t.Errorf("expected status %d, got %d: %s", tt.expectCode, w.Code, w.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: RequirePlatformServices
// ---------------------------------------------------------------------------

func TestRequirePlatformServices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		svc        *store.Services
		expectNil  bool
		expectCode int
	}{
		{
			name:       "nil services → 503",
			svc:        nil,
			expectNil:  true,
			expectCode: http.StatusServiceUnavailable,
		},
		{
			name:       "personal mode → 503",
			svc:        &store.Services{Mode: "personal"},
			expectNil:  true,
			expectCode: http.StatusServiceUnavailable,
		},
		{
			name:       "platform mode → passes",
			svc:        &store.Services{Mode: store.ModePlatform},
			expectNil:  false,
			expectCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := authzRequest(nil, tt.svc, nil)
			result := RequirePlatformServices(w, r)
			if tt.expectNil && result != nil {
				t.Error("expected nil, got services")
			}
			if !tt.expectNil && result == nil {
				t.Error("expected services, got nil")
			}
			if w.Code != tt.expectCode {
				t.Errorf("expected %d, got %d", tt.expectCode, w.Code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: Pure boolean helpers — IsPlatformAdmin, CanManageOrg, IsOrgOwner
// ---------------------------------------------------------------------------

func TestIsPlatformAdmin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		user   *PlatformUser
		expect bool
	}{
		{"nil user", nil, false},
		{"regular member", &PlatformUser{PlatformRole: ""}, false},
		{"org admin (not platform)", &PlatformUser{Role: "admin", PlatformRole: ""}, false},
		{"superadmin", &PlatformUser{PlatformRole: "superadmin"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPlatformAdmin(tt.user); got != tt.expect {
				t.Errorf("IsPlatformAdmin() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestCanManageOrg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		user   *PlatformUser
		expect bool
	}{
		{"nil user", nil, false},
		{"member", &PlatformUser{Role: "member"}, false},
		{"admin", &PlatformUser{Role: "admin"}, true},
		{"owner", &PlatformUser{Role: "owner"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanManageOrg(tt.user); got != tt.expect {
				t.Errorf("CanManageOrg() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestIsOrgOwner(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		user   *PlatformUser
		expect bool
	}{
		{"nil user", nil, false},
		{"member", &PlatformUser{Role: "member"}, false},
		{"admin", &PlatformUser{Role: "admin"}, false},
		{"owner", &PlatformUser{Role: "owner"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsOrgOwner(tt.user); got != tt.expect {
				t.Errorf("IsOrgOwner() = %v, want %v", got, tt.expect)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: CanManageTeam
// ---------------------------------------------------------------------------

func TestCanManageTeam(t *testing.T) {
	t.Parallel()

	teamMgmt := newMockTeamMgmt()
	teamMgmt.teams["eng"] = &store.Team{ID: "team-eng", Slug: "eng"}
	teamMgmt.memberRoles["u-team-admin:team-eng"] = "admin"
	teamMgmt.memberRoles["u-member:team-eng"] = "member"

	orgDS := &authzOrgDataStore{teams: teamMgmt}
	router := &authzTenantRouter{orgs: map[string]store.OrgDataStore{"acme": orgDS}}
	svc := &store.Services{Mode: store.ModePlatform, TenantRouter: router}
	tc := &store.TenantContext{OrgSlug: "acme", TeamSlug: "eng"}

	tests := []struct {
		name   string
		user   *PlatformUser
		useSvc *store.Services
		useTc  *store.TenantContext
		expect bool
	}{
		{
			name:   "personal mode — always true",
			user:   &PlatformUser{ID: "anyone", Role: "member"},
			useSvc: &store.Services{Mode: "personal"},
			expect: true,
		},
		{
			name: "nil user — false",
			user: nil, useSvc: svc, useTc: tc,
			expect: false,
		},
		{
			name: "org owner — true (bypass)",
			user: &PlatformUser{ID: "u-owner", Role: "owner"}, useSvc: svc, useTc: tc,
			expect: true,
		},
		{
			name: "org admin — true (bypass)",
			user: &PlatformUser{ID: "u-admin", Role: "admin"}, useSvc: svc, useTc: tc,
			expect: true,
		},
		{
			name: "team admin — true",
			user: &PlatformUser{ID: "u-team-admin", Role: "member"}, useSvc: svc, useTc: tc,
			expect: true,
		},
		{
			name: "regular member — false",
			user: &PlatformUser{ID: "u-member", Role: "member"}, useSvc: svc, useTc: tc,
			expect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := authzRequest(tt.user, tt.useSvc, tt.useTc)
			got := CanManageTeam(r, tt.user)
			if got != tt.expect {
				t.Errorf("CanManageTeam() = %v, want %v", got, tt.expect)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: CanManageTeamByID
// ---------------------------------------------------------------------------

func TestCanManageTeamByID(t *testing.T) {
	t.Parallel()

	teamMgmt := newMockTeamMgmt()
	teamMgmt.memberRoles["u-admin:team-1"] = "admin"
	teamMgmt.memberRoles["u-member:team-1"] = "member"

	orgDS := &authzOrgDataStore{teams: teamMgmt}

	tests := []struct {
		name   string
		user   *PlatformUser
		teamID string
		expect bool
	}{
		{"nil user", nil, "team-1", false},
		{"org owner bypasses", &PlatformUser{ID: "x", Role: "owner"}, "team-1", true},
		{"org admin bypasses", &PlatformUser{ID: "x", Role: "admin"}, "team-1", true},
		{"team admin passes", &PlatformUser{ID: "u-admin", Role: "member"}, "team-1", true},
		{"team member fails", &PlatformUser{ID: "u-member", Role: "member"}, "team-1", false},
		{"unknown user fails", &PlatformUser{ID: "u-unknown", Role: "member"}, "team-1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/test", nil)
			got := CanManageTeamByID(r, tt.user, orgDS, tt.teamID)
			if got != tt.expect {
				t.Errorf("CanManageTeamByID() = %v, want %v", got, tt.expect)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: IsTeamAdmin (convenience wrapper)
// ---------------------------------------------------------------------------

func TestIsTeamAdmin(t *testing.T) {
	t.Parallel()

	t.Run("personal mode returns true", func(t *testing.T) {
		svc := &store.Services{Mode: "personal"}
		user := &PlatformUser{ID: "u1", Role: "member"}
		r := authzRequest(user, svc, nil)
		if !IsTeamAdmin(r) {
			t.Error("expected true in personal mode")
		}
	})

	t.Run("platform mode with org admin returns true", func(t *testing.T) {
		svc := &store.Services{Mode: store.ModePlatform}
		user := &PlatformUser{ID: "u1", Role: "admin"}
		r := authzRequest(user, svc, nil)
		if !IsTeamAdmin(r) {
			t.Error("expected true for org admin")
		}
	})

	t.Run("platform mode with no user returns false", func(t *testing.T) {
		svc := &store.Services{Mode: store.ModePlatform}
		r := authzRequest(nil, svc, nil)
		if IsTeamAdmin(r) {
			t.Error("expected false with no user")
		}
	})
}

// ---------------------------------------------------------------------------
// Tests: Cross-tenant isolation (TenantRouter boundary)
// ---------------------------------------------------------------------------

func TestCrossTenantIsolation(t *testing.T) {
	t.Parallel()

	// Setup: only "org-a" exists in the router
	teamMgmtA := newMockTeamMgmt()
	teamMgmtA.teams["team-a"] = &store.Team{ID: "ta-1", Slug: "team-a"}
	teamMgmtA.members["user-a:team-a"] = true
	orgDSA := &authzOrgDataStore{teams: teamMgmtA}

	router := &authzTenantRouter{orgs: map[string]store.OrgDataStore{
		"org-a": orgDSA,
	}}

	t.Run("ForOrg with known org succeeds", func(t *testing.T) {
		ds, err := router.ForOrg("org-a")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ds == nil {
			t.Fatal("expected OrgDataStore, got nil")
		}
	})

	t.Run("ForOrg with unknown org fails", func(t *testing.T) {
		ds, err := router.ForOrg("org-b")
		if err == nil {
			t.Fatal("expected error for unknown org, got nil")
		}
		if ds != nil {
			t.Fatal("expected nil OrgDataStore for unknown org")
		}
	})

	t.Run("user from org-a cannot resolve org-b stores via canManageCurrentTeam", func(t *testing.T) {
		// User is authenticated in org-a context but TenantContext points to org-b
		svc := &store.Services{Mode: store.ModePlatform, TenantRouter: router}
		tc := &store.TenantContext{OrgSlug: "org-b", TeamSlug: "team-b"}
		user := &PlatformUser{ID: "user-a", Role: "member"}

		r := authzRequest(user, svc, tc)
		// canManageCurrentTeam should fail because org-b doesn't exist in router
		result := CanManageTeam(r, user)
		if result {
			t.Error("expected false — user should not be able to access org-b resources")
		}
	})

	t.Run("user from org-a can access their own team", func(t *testing.T) {
		teamMgmtA.memberRoles["user-a:ta-1"] = "admin"
		svc := &store.Services{Mode: store.ModePlatform, TenantRouter: router}
		tc := &store.TenantContext{OrgSlug: "org-a", TeamSlug: "team-a"}
		user := &PlatformUser{ID: "user-a", Role: "member"}

		r := authzRequest(user, svc, tc)
		result := CanManageTeam(r, user)
		if !result {
			t.Error("expected true — user is team admin in their own org")
		}
	})
}

// ---------------------------------------------------------------------------
// Tests: Team switch authorization (checkTeamAccess via middleware)
// ---------------------------------------------------------------------------

func TestCheckTeamAccess(t *testing.T) {
	t.Parallel()

	teamMgmt := newMockTeamMgmt()
	teamMgmt.members["user-1:eng"] = true
	teamMgmt.members["user-1:ops"] = false // explicitly not a member

	orgDS := &authzOrgDataStore{teams: teamMgmt}

	// Build a minimal PlatformAuth to test checkTeamAccess
	pa := &PlatformAuth{
		orgResolver: &mockPGStoreForAuthz{orgs: map[string]store.OrgDataStore{"acme": orgDS}},
	}

	tests := []struct {
		name      string
		claims    *PlatformClaims
		teamSlug  string
		expectErr bool
	}{
		{
			name:      "empty team slug — pass",
			claims:    &PlatformClaims{Role: "member", UserID: "user-1"},
			teamSlug:  "",
			expectErr: false,
		},
		{
			name:      "org owner bypasses team check",
			claims:    &PlatformClaims{Role: "owner", UserID: "user-2", OrgSlug: "acme"},
			teamSlug:  "any-team",
			expectErr: false,
		},
		{
			name:      "org admin bypasses team check",
			claims:    &PlatformClaims{Role: "admin", UserID: "user-2", OrgSlug: "acme"},
			teamSlug:  "any-team",
			expectErr: false,
		},
		{
			name:      "member of team — passes",
			claims:    &PlatformClaims{Role: "member", UserID: "user-1", OrgSlug: "acme"},
			teamSlug:  "eng",
			expectErr: false,
		},
		{
			name:      "NOT member of team — rejected",
			claims:    &PlatformClaims{Role: "member", UserID: "user-1", OrgSlug: "acme"},
			teamSlug:  "ops",
			expectErr: true,
		},
		{
			name:      "unknown org — rejected",
			claims:    &PlatformClaims{Role: "member", UserID: "user-1", OrgSlug: "unknown-org"},
			teamSlug:  "eng",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := pa.checkTeamAccess(ctx, tt.claims, tt.teamSlug)
			if tt.expectErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
		})
	}
}

// mockPGStoreForAuthz is a minimal mock that satisfies the orgResolver interface
// for checkTeamAccess testing.
type mockPGStoreForAuthz struct {
	orgs map[string]store.OrgDataStore
}

func (m *mockPGStoreForAuthz) ForOrg(orgSlug string) (store.OrgDataStore, error) {
	if ds, ok := m.orgs[orgSlug]; ok {
		return ds, nil
	}
	return nil, fmt.Errorf("org not found: %s", orgSlug)
}
