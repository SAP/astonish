package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// TestSandboxAccessAcceptance is the HTTP-level proof that a chat sandbox
// is private to its owner: the owner can see and read it, a teammate cannot
// list, delete, read, proxy, or VNC into it, and a non-admin cannot prune.
func TestSandboxAccessAcceptance(t *testing.T) {
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

	ownerVisible := 0
	for _, e := range fix.registry.List() {
		if sandboxSessionVisible(fix.ownerReq, fix.registry, e.SessionID) {
			ownerVisible++
			if e.SessionID != "chat-owner" {
				t.Fatalf("owner can see %s", e.SessionID)
			}
		}
	}
	if ownerVisible != 1 {
		t.Fatalf("owner visible count = %d, want 1", ownerVisible)
	}

	rec := httptest.NewRecorder()
	if !artifactSessionVisible(rec, withSandboxRegistry(fix.ownerReq, fix.registry), "chat-owner") {
		t.Fatalf("owner artifact read denied: %d %s", rec.Code, rec.Body.String())
	}

	mate := withSandboxRegistry(fix.mateReq, fix.registry)
	if sandboxSessionVisible(mate, fix.registry, "chat-owner") {
		t.Fatal("teammate list must omit the owner's sandbox")
	}
	rec = httptest.NewRecorder()
	if authorizeSandboxSession(rec, mate, fix.registry, "chat-owner") != nil || rec.Code != http.StatusNotFound {
		t.Fatalf("teammate delete status = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	if artifactSessionVisible(rec, mate, "chat-owner") || rec.Code != http.StatusNotFound {
		t.Fatalf("teammate artifact status = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	if _, ok := authorizeVNCContainer(rec, mate, "chat-owner"); ok || rec.Code != http.StatusNotFound {
		t.Fatalf("teammate VNC status = %d, want 404", rec.Code)
	}
	if sessionIDForProxyContainer(httptest.NewRequest(http.MethodGet, "/", nil), "chat-owner") != "" {
		t.Fatal("proxy must not treat an unscoped URL segment as a session ID")
	}

	prune := httptest.NewRecorder()
	SandboxPruneHandler(prune, platformSandboxRequest(t, "user-member", "member", &memSessionStore{}))
	if prune.Code != http.StatusForbidden {
		t.Fatalf("non-admin prune status = %d, want 403", prune.Code)
	}
}
