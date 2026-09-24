package api

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

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
