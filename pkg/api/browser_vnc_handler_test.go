package api

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
)

func TestScopedBrowserHandoffDoneOwnerAndLifecycle(t *testing.T) {
	owner := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: "owner", OrgSlug: "org", TeamSlug: "team", Authenticated: true}
	ctx, err := execution.WithPrincipal(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	register := func(id string) (string, func()) {
		mgr := browser.NewManager(browser.DefaultConfig())
		mgr.SetSessionID(id)
		mgr.SetContainerForTest(id, "127.0.0.1")
		fn, release, err := BindScopedBrowserHandoff(ctx, id, mgr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
		t.Cleanup(mgr.Cleanup)
		token, err := fn(id, id)
		if err != nil {
			t.Fatal(err)
		}
		return token, release
	}
	token, release := register("scoped-handoff-a")
	for _, mode := range []string{"missing", "other-owner", "other-team"} {
		request := httptest.NewRequest(http.MethodPost, "/api/browser/handoff-done", nil)
		if mode != "missing" {
			foreign := owner
			if mode == "other-owner" {
				foreign.Subject = "other"
			} else {
				foreign.TeamSlug = "other"
			}
			foreignCtx, _ := execution.WithPrincipal(t.Context(), foreign)
			request = request.WithContext(foreignCtx)
		}
		response := httptest.NewRecorder()
		BrowserHandoffDoneHandler(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status %d", mode, response.Code)
		}
		if _, valid := browser.GetHandoffTokenRegistry().ValidateToken(token); !valid {
			t.Fatal("foreign Done revoked token")
		}
	}
	second, releaseSecond := register("scoped-handoff-b")
	request := httptest.NewRequest(http.MethodPost, "/api/browser/handoff-done", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	BrowserHandoffDoneHandler(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("ambiguous Done status %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/browser/handoff-done?session_id=scoped-handoff-a", nil).WithContext(ctx)
	response = httptest.NewRecorder()
	BrowserHandoffDoneHandler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("owner Done status %d", response.Code)
	}
	if _, valid := browser.GetHandoffTokenRegistry().ValidateToken(token); valid {
		t.Fatal("Done did not revoke own token")
	}
	if _, valid := browser.GetHandoffTokenRegistry().ValidateToken(second); !valid {
		t.Fatal("Done revoked other session")
	}
	release()
	release()
	releaseSecond()
	if _, valid := browser.GetHandoffTokenRegistry().ValidateToken(second); valid {
		t.Fatal("cleanup left token active")
	}
	staleToken, releaseStale := register("scoped-handoff-expired")
	scopedBrowserHandoffs.Lock()
	for entry := range scopedBrowserHandoffs.active {
		if entry.container == "scoped-handoff-expired" {
			entry.created = time.Now().Add(-31 * time.Minute)
		}
	}
	scopedBrowserHandoffs.Unlock()
	replacement, releaseReplacement := register("scoped-handoff-expired")
	if _, valid := browser.GetHandoffTokenRegistry().ValidateToken(staleToken); valid {
		t.Fatal("expired registry entry retained token")
	}
	releaseStale()
	if _, valid := browser.GetHandoffTokenRegistry().ValidateToken(replacement); !valid {
		t.Fatal("stale cleanup revoked replacement")
	}
	releaseReplacement()
}

func TestGetVNCDialFunc_DockerIgnoresStaleGlobalDialer(t *testing.T) {
	var globalCalls, requestCalls int
	SetVNCContainerDialFunc(func(string, int) (net.Conn, error) {
		globalCalls++
		return nil, errors.New("container not found: stale registry")
	})
	t.Cleanup(func() {
		SetVNCContainerDialFunc(nil)
	})

	oldEnsure := ensureVNCSessionRunning
	oldDial := dialVNCBackend
	oldTransport := transportVNCBackend
	t.Cleanup(func() {
		ensureVNCSessionRunning = oldEnsure
		dialVNCBackend = oldDial
		transportVNCBackend = oldTransport
	})

	ensureVNCSessionRunning = func(*http.Request, string) error { return nil }
	dialVNCBackend = func(_ *http.Request, sessionID string, port int) (net.Conn, error) {
		requestCalls++
		if sessionID != "session-current" || port != vncDialerPort {
			t.Fatalf("request-scoped dial received session=%q port=%d", sessionID, port)
		}
		return nil, nil
	}
	transportVNCBackend = func(*http.Request, string, int) *http.Transport { return &http.Transport{} }

	dial, _, err := getVNCDialFuncForBackend(httpRequestForVNC(), "session-current", sandbox.BackendKindDocker)
	if err != nil {
		t.Fatalf("getVNCDialFuncForBackend: %v", err)
	}
	if _, err := dial(); err != nil {
		t.Fatalf("request-scoped dial: %v", err)
	}
	if requestCalls != 1 {
		t.Fatalf("request-scoped dial calls = %d, want 1", requestCalls)
	}
	if globalCalls != 0 {
		t.Fatalf("stale global dial calls = %d, want 0", globalCalls)
	}
}

func TestGetVNCDialFunc_KubernetesIgnoresStaleGlobalDialer(t *testing.T) {
	var globalCalls, requestCalls int
	SetVNCContainerDialFunc(func(string, int) (net.Conn, error) {
		globalCalls++
		return nil, errors.New("container not found: stale registry")
	})
	t.Cleanup(func() { SetVNCContainerDialFunc(nil) })

	oldEnsure := ensureVNCSessionRunning
	oldDial := dialVNCBackend
	oldTransport := transportVNCBackend
	t.Cleanup(func() {
		ensureVNCSessionRunning = oldEnsure
		dialVNCBackend = oldDial
		transportVNCBackend = oldTransport
	})

	ensureVNCSessionRunning = func(*http.Request, string) error { return nil }
	dialVNCBackend = func(*http.Request, string, int) (net.Conn, error) {
		requestCalls++
		return nil, nil
	}
	transportVNCBackend = func(*http.Request, string, int) *http.Transport { return &http.Transport{} }

	dial, _, err := getVNCDialFuncForBackend(httpRequestForVNC(), "session-current", sandbox.BackendKindK8s)
	if err != nil {
		t.Fatalf("getVNCDialFuncForBackend: %v", err)
	}
	if _, err := dial(); err != nil {
		t.Fatalf("request-scoped dial: %v", err)
	}
	if requestCalls != 1 || globalCalls != 0 {
		t.Fatalf("calls = request %d, global %d; want request 1, global 0", requestCalls, globalCalls)
	}
}

func TestGetVNCDialFunc_RequestScopedReadinessFailure(t *testing.T) {
	var globalCalls, requestCalls int
	SetVNCContainerDialFunc(func(string, int) (net.Conn, error) {
		globalCalls++
		return nil, errors.New("container not found: stale registry")
	})
	t.Cleanup(func() { SetVNCContainerDialFunc(nil) })

	oldEnsure := ensureVNCSessionRunning
	oldDial := dialVNCBackend
	oldTransport := transportVNCBackend
	t.Cleanup(func() {
		ensureVNCSessionRunning = oldEnsure
		dialVNCBackend = oldDial
		transportVNCBackend = oldTransport
	})

	sentinel := errors.New("session is not running")
	ensureVNCSessionRunning = func(*http.Request, string) error { return sentinel }
	dialVNCBackend = func(*http.Request, string, int) (net.Conn, error) {
		requestCalls++
		return nil, nil
	}
	transportVNCBackend = func(*http.Request, string, int) *http.Transport { return &http.Transport{} }

	_, _, err := getVNCDialFuncForBackend(httpRequestForVNC(), "session-current", sandbox.BackendKindDocker)
	if err == nil || !strings.Contains(err.Error(), "failed to reach sandbox") || !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatalf("error = %v, want wrapped readiness error", err)
	}
	if requestCalls != 0 || globalCalls != 0 {
		t.Fatalf("calls = request %d, global %d; want both 0", requestCalls, globalCalls)
	}
}

func TestGetVNCDialFunc_OpenShellUsesRegisteredDialer(t *testing.T) {
	var globalCalls, requestCalls int
	SetVNCContainerDialFunc(func(sessionID string, port int) (net.Conn, error) {
		globalCalls++
		if sessionID != "session-openshell" || port != vncDialerPort {
			t.Fatalf("registered dial received session=%q port=%d", sessionID, port)
		}
		return nil, nil
	})
	t.Cleanup(func() { SetVNCContainerDialFunc(nil) })

	oldEnsure := ensureVNCSessionRunning
	oldDial := dialVNCBackend
	oldTransport := transportVNCBackend
	t.Cleanup(func() {
		ensureVNCSessionRunning = oldEnsure
		dialVNCBackend = oldDial
		transportVNCBackend = oldTransport
	})
	ensureVNCSessionRunning = func(*http.Request, string) error {
		t.Fatal("OpenShell must not use the request-scoped readiness path")
		return nil
	}
	dialVNCBackend = func(*http.Request, string, int) (net.Conn, error) {
		requestCalls++
		return nil, nil
	}
	transportVNCBackend = func(*http.Request, string, int) *http.Transport { return &http.Transport{} }

	dial, _, err := getVNCDialFuncForBackend(httpRequestForVNC(), "session-openshell", sandbox.BackendKindOpenShell)
	if err != nil {
		t.Fatalf("getVNCDialFuncForBackend: %v", err)
	}
	if _, err := dial(); err != nil {
		t.Fatalf("registered dial: %v", err)
	}
	if globalCalls != 1 || requestCalls != 0 {
		t.Fatalf("calls = global %d, request %d; want global 1, request 0", globalCalls, requestCalls)
	}
}

func httpRequestForVNC() *http.Request {
	return httptest.NewRequest(http.MethodGet, "/api/browser/vnc/session-current/", nil)
}
