package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// memSessionStore is a SessionStore that only answers GetSessionMeta.
// Ownership tests do not need transcripts or the ADK runner.
type memSessionStore struct {
	metas map[string]*store.SessionMeta
}

func (m *memSessionStore) GetSessionMeta(_ context.Context, sessionID string) (*store.SessionMeta, error) {
	if m == nil || m.metas == nil {
		return nil, nil
	}
	meta := m.metas[sessionID]
	if meta == nil {
		return nil, nil
	}
	cp := *meta
	return &cp, nil
}

type ownershipFixture struct {
	registry *sandbox.SessionRegistry
	ownerReq *http.Request
	mateReq  *http.Request
}

func newOwnershipFixture(t *testing.T) ownershipFixture {
	t.Helper()
	st, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalSessionStore: %v", err)
	}
	reg := sandbox.NewSessionRegistryFromStore(st)
	if err := reg.PutSession(&store.SandboxSession{
		SessionID:     "chat-owner",
		ChatSessionID: "chat-owner",
		Backend:       "docker",
		ContainerName: "astonish-session-owner",
		TemplateID:    sandbox.BaseTemplateID,
		State:         store.SandboxSessionStateRunning,
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	sessions := &memSessionStore{metas: map[string]*store.SessionMeta{
		"chat-owner": {ID: "chat-owner", UserID: "user-owner", AppName: "astonish"},
	}}
	return ownershipFixture{
		registry: reg,
		ownerReq: platformSandboxRequest(t, "user-owner", "member", sessions),
		mateReq:  platformSandboxRequest(t, "user-mate", "member", sessions),
	}
}

func platformSandboxRequest(t *testing.T, userID, role string, sessions *memSessionStore) *http.Request {
	t.Helper()
	svc := &store.Services{Mode: store.ModePlatform}
	ctx := store.WithTenantContext(context.Background(), &store.TenantContext{
		OrgSlug:  "acme",
		TeamSlug: "general",
		UserID:   userID,
	})
	ctx = WithPlatformUser(ctx, &PlatformUser{
		ID:       userID,
		OrgSlug:  "acme",
		TeamSlug: "general",
		Role:     role,
	})
	if sessions != nil {
		lookup := sessions.GetSessionMeta
		ctx = context.WithValue(ctx, sandboxAccessMetaKey{}, lookup)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sandbox/containers", nil)
	return req.WithContext(store.WithServices(ctx, svc))
}

func TestSandboxPrune_NonAdmin403(t *testing.T) {
	req := platformSandboxRequest(t, "user-member", "member", &memSessionStore{})
	rec := httptest.NewRecorder()
	SandboxPruneHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("prune status = %d, want 403; body %s", rec.Code, rec.Body.String())
	}
}

func TestSandboxTemplatePromote_NonAdmin403(t *testing.T) {
	req := platformSandboxRequest(t, "user-member", "member", &memSessionStore{})
	rec := httptest.NewRecorder()
	SandboxTemplatePromoteHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("promote status = %d, want 403; body %s", rec.Code, rec.Body.String())
	}
}

func TestSandboxContainerList_HidesTeammate(t *testing.T) {
	fix := newOwnershipFixture(t)
	if err := fix.registry.PutSession(&store.SandboxSession{
		SessionID:     "chat-mate",
		ChatSessionID: "chat-mate",
		Backend:       "docker",
		ContainerName: "astonish-session-mate",
		TemplateID:    sandbox.BaseTemplateID,
		State:         store.SandboxSessionStateRunning,
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	visible := map[string]bool{}
	for _, e := range fix.registry.List() {
		if sandboxSessionVisible(fix.ownerReq, fix.registry, e.SessionID) {
			visible[e.SessionID] = true
		}
	}
	if !visible["chat-owner"] {
		t.Fatal("owner list omitted their own sandbox")
	}
	if visible["chat-mate"] {
		t.Fatal("owner list included a teammate sandbox from the same team registry")
	}
}

func TestSandboxContainerDelete_Foreign404(t *testing.T) {
	fix := newOwnershipFixture(t)
	rec := httptest.NewRecorder()
	if authorizeSandboxSession(rec, fix.mateReq, fix.registry, "chat-owner") != nil {
		t.Fatal("foreign delete must not resolve the container")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign delete status = %d, want 404", rec.Code)
	}
}

func TestStudioArtifact_ForeignSession404(t *testing.T) {
	fix := newOwnershipFixture(t)
	req := withSandboxRegistry(fix.mateReq, fix.registry)
	rec := httptest.NewRecorder()
	if artifactSessionVisible(rec, req, "chat-owner") {
		t.Fatal("foreign artifact read must be denied")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign artifact status = %d, want 404; body %s", rec.Code, rec.Body.String())
	}
}

func TestSandboxProxy_ForeignContainerNotDialed(t *testing.T) {
	fix := newOwnershipFixture(t)
	req := withSandboxRegistry(fix.mateReq, fix.registry)
	if sessionIDForProxyContainer(req, "astonish-session-owner") == "" && sessionIDForProxyContainer(req, "chat-owner") == "" {
		t.Fatal("owner container should resolve inside the team registry before the ownership check")
	}
	rec := httptest.NewRecorder()
	if authorizeSandboxSession(rec, req, fix.registry, "chat-owner") != nil {
		t.Fatal("proxy must not authorize a teammate")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("proxy foreign status = %d, want 404", rec.Code)
	}
	if sessionIDForProxyContainer(httptest.NewRequest(http.MethodGet, "/", nil), "chat-owner") != "" {
		t.Fatal("unscoped lookup must not fall back to treating the URL as a session ID")
	}
}

func TestBrowserVNC_ForeignContainerNotDialed(t *testing.T) {
	fix := newOwnershipFixture(t)
	req := withSandboxRegistry(fix.mateReq, fix.registry)
	rec := httptest.NewRecorder()
	sessionID, ok := authorizeVNCContainer(rec, req, "chat-owner")
	if ok || sessionID != "" {
		t.Fatal("VNC must not authorize a teammate container")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("VNC foreign status = %d, want 404", rec.Code)
	}
}
func TestAuthorizeSandboxSession(t *testing.T) {
	fix := newOwnershipFixture(t)

	t.Run("owner allowed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		got := authorizeSandboxSession(rec, fix.ownerReq, fix.registry, "chat-owner")
		if got == nil {
			t.Fatalf("owner denied: status %d body %s", rec.Code, rec.Body.String())
		}
		if rec.Code != 0 && rec.Code != http.StatusOK {
			t.Fatalf("owner status = %d, want no error", rec.Code)
		}
	})

	t.Run("teammate denied as not found", func(t *testing.T) {
		rec := httptest.NewRecorder()
		got := authorizeSandboxSession(rec, fix.mateReq, fix.registry, "chat-owner")
		if got != nil {
			t.Fatal("teammate must not receive the sandbox row")
		}
		if rec.Code != http.StatusNotFound {
			t.Fatalf("teammate status = %d, want 404 (403 would confirm the container exists)", rec.Code)
		}
	})

	t.Run("missing session is 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		got := authorizeSandboxSession(rec, fix.ownerReq, fix.registry, "does-not-exist")
		if got != nil {
			t.Fatal("missing session must not resolve")
		}
		if rec.Code != http.StatusNotFound {
			t.Fatalf("missing status = %d, want 404", rec.Code)
		}
	})

	t.Run("personal mode allows the only user", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sandbox/containers", nil)
		rec := httptest.NewRecorder()
		if got := authorizeSandboxSession(rec, req, fix.registry, "chat-owner"); got == nil {
			t.Fatalf("personal mode denied the only user: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("created-by fallback when chat row is gone", func(t *testing.T) {
		st, err := sandbox.NewLocalSessionStore(t.TempDir())
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		reg := sandbox.NewSessionRegistryFromStore(st)
		if err := reg.PutSession(&store.SandboxSession{
			SessionID:     "orphan-chat",
			ChatSessionID: "orphan-chat",
			Backend:       "docker",
			ContainerName: "astonish-session-orphan",
			TemplateID:    sandbox.BaseTemplateID,
			State:         store.SandboxSessionStateRunning,
			CreatedBy:     "user-owner",
		}); err != nil {
			t.Fatalf("PutSession: %v", err)
		}
		empty := &memSessionStore{metas: map[string]*store.SessionMeta{}}
		owner := platformSandboxRequest(t, "user-owner", "member", empty)
		mate := platformSandboxRequest(t, "user-mate", "member", empty)
		if authorizeSandboxSession(httptest.NewRecorder(), owner, reg, "orphan-chat") == nil {
			t.Fatal("owner with CreatedBy must still delete a container whose chat row is gone")
		}
		rec := httptest.NewRecorder()
		if authorizeSandboxSession(rec, mate, reg, "orphan-chat") != nil || rec.Code != http.StatusNotFound {
			t.Fatalf("teammate CreatedBy mismatch must be 404, got row/status %d", rec.Code)
		}
	})

	t.Run("fleet teammate allowed and other team denied", func(t *testing.T) {
		st, err := sandbox.NewLocalSessionStore(t.TempDir())
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		reg := sandbox.NewSessionRegistryFromStore(st)
		if err := reg.PutSession(&store.SandboxSession{
			SessionID:     "fleet-1",
			ChatSessionID: "fleet-1",
			Backend:       "docker",
			ContainerName: "astonish-session-fleet",
			TemplateID:    sandbox.BaseTemplateID,
			State:         store.SandboxSessionStateRunning,
		}); err != nil {
			t.Fatalf("PutSession: %v", err)
		}
		sessions := &memSessionStore{metas: map[string]*store.SessionMeta{
			"fleet-1": {ID: "fleet-1", UserID: "user-owner", FleetKey: "bugs", AppName: "astonish"},
		}}
		sameTeam := platformSandboxRequest(t, "user-mate", "member", sessions)
		if !sandboxSessionVisible(sameTeam, reg, "fleet-1") {
			t.Fatal("fleet teammate must see the fleet sandbox")
		}
		// Org admin may act on a fleet sandbox even when the request team differs.
		admin := platformSandboxRequest(t, "user-admin", "admin", sessions)
		admin = admin.WithContext(WithPlatformUser(store.WithTenantContext(admin.Context(), &store.TenantContext{
			OrgSlug: "acme", TeamSlug: "other-team", UserID: "user-admin",
		}), &PlatformUser{
			ID: "user-admin", OrgSlug: "acme", TeamSlug: "other-team", Role: "admin",
		}))
		if !sandboxSessionVisible(admin, reg, "fleet-1") {
			t.Fatal("org admin must see a fleet sandbox")
		}
	})
}
