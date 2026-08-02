package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/mock"
	"github.com/SAP/astonish/pkg/sandbox/netpolicy"
	"github.com/SAP/astonish/pkg/store"
)

func TestCredentialSuffixParsing(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantURL  string
		wantCred string
	}{
		{
			name:     "no credential",
			url:      "https://api.example.com/data",
			wantURL:  "https://api.example.com/data",
			wantCred: "",
		},
		{
			name:     "simple credential",
			url:      "https://api.example.com/data@my-api-key",
			wantURL:  "https://api.example.com/data",
			wantCred: "my-api-key",
		},
		{
			name:     "credential with underscores",
			url:      "https://api.example.com/v2/users@github_token",
			wantURL:  "https://api.example.com/v2/users",
			wantCred: "github_token",
		},
		{
			name:     "URL with @ in basic auth (should not match)",
			url:      "https://user:pass@api.example.com/data",
			wantURL:  "https://user:pass@api.example.com/data",
			wantCred: "",
		},
		{
			name:     "URL with @ in path (should not match — has / after @)",
			url:      "https://api.example.com/@user/repos",
			wantURL:  "https://api.example.com/@user/repos",
			wantCred: "",
		},
		{
			name:     "URL with query params and credential",
			url:      "https://api.example.com/data?format=json@my-cred",
			wantURL:  "https://api.example.com/data?format=json",
			wantCred: "my-cred",
		},
		{
			name:     "credential starts with uppercase",
			url:      "https://api.example.com/data@MyCredential",
			wantURL:  "https://api.example.com/data",
			wantCred: "MyCredential",
		},
		{
			name:     "@ but no valid name after (digit start)",
			url:      "https://api.example.com/data@123",
			wantURL:  "https://api.example.com/data@123",
			wantCred: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := tt.url
			var credentialName string
			if m := credentialSuffixRe.FindStringSubmatchIndex(url); m != nil {
				credentialName = url[m[2]:m[3]]
				url = url[:m[0]]
			}

			if url != tt.wantURL {
				t.Errorf("url = %q, want %q", url, tt.wantURL)
			}
			if credentialName != tt.wantCred {
				t.Errorf("credential = %q, want %q", credentialName, tt.wantCred)
			}
		})
	}
}

// ── Apps HTTP sandbox egress tests ───────────────────────────────────

func TestIsHardBlockedIP(t *testing.T) {
	tests := []struct {
		ip   string
		hard bool
	}{
		{"127.0.0.1", true},
		{"169.254.169.254", true},
		{"0.0.0.0", true},
		{"::1", true},
		{"fe80::1", true},
		{"10.0.0.1", false},
		{"192.168.1.1", false},
		{"8.8.8.8", false},
		{"fd00::1", false},
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("invalid IP %q", tt.ip)
			}
			if got := isHardBlockedIP(ip); got != tt.hard {
				t.Errorf("isHardBlockedIP(%s) = %v, want %v", tt.ip, got, tt.hard)
			}
		})
	}
}

func TestValidateAppHTTPURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{"public https", "https://api.example.com/data", ""},
		{"public http", "http://api.example.com/data", ""},
		{"soft-private hostname allowed for sandbox", "https://github.wdf.sap.corp/api/graphql", ""},
		{"soft-private IP literal allowed for sandbox", "http://10.0.0.1/secret", ""},
		{"ftp scheme blocked", "ftp://evil.com/file", "unsupported URL scheme"},
		{"file scheme blocked", "file:///etc/passwd", "unsupported URL scheme"},
		{"localhost IP", "http://127.0.0.1/admin", "private/internal"},
		{"localhost v6", "http://[::1]/admin", "private/internal"},
		{"metadata endpoint", "http://169.254.169.254/latest/meta-data/", "private/internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAppHTTPURL(tt.url)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestParseCurlHTTPOutput(t *testing.T) {
	marker := "\nASTONISH_HTTP_STATUS_deadbeef:"
	body, status, err := parseCurlHTTPOutput([]byte(`{"ok":true}`+marker+"200"), marker)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body = %q", body)
	}
	if _, _, err := parseCurlHTTPOutput([]byte("no marker"), marker); err == nil {
		t.Fatal("expected error without status marker")
	}
}

func TestValidateCurlHeader(t *testing.T) {
	if err := validateCurlHeader("Authorization", "Bearer tok"); err != nil {
		t.Fatalf("valid header: %v", err)
	}
	if err := validateCurlHeader("", "x"); err == nil {
		t.Fatal("expected error for empty key")
	}
	if err := validateCurlHeader("X-A\nB", "v"); err == nil {
		t.Fatal("expected error for CR/LF in key")
	}
	if err := validateCurlHeader("Authorization", "Bearer tok\r\nX-Injected: evil"); err == nil {
		t.Fatal("expected error for CR/LF in value")
	}
}

func TestFetchHTTPViaSandbox_RejectsHeaderInjection(t *testing.T) {
	origEnsure := ensureAppSandboxSession
	defer func() { ensureAppSandboxSession = origEnsure }()

	backend := mock.New()
	sessionID := "app-mcp-hdr-inject"
	if _, err := backend.CreateSession(context.Background(), sandbox.SessionSpec{SessionID: sessionID}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	backend.ExecResultFn = func(string, sandbox.ExecSpec) (*sandbox.ExecResult, error) {
		t.Fatal("Exec must not run when headers contain CR/LF")
		return nil, nil
	}
	ensureAppSandboxSession = func(context.Context, *http.Request, string) (sandbox.Backend, string, *config.AppConfig, func(), error) {
		return backend, sessionID, &config.AppConfig{}, func() {}, nil
	}

	_, _, err := fetchHTTPViaSandbox(context.Background(), nil, "GET", "https://api.example.com/v1", map[string]string{
		"Authorization": "Bearer tok\r\nX-Injected: evil",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "CR/LF") {
		t.Fatalf("expected CR/LF header error, got %v", err)
	}
	if n := len(backend.ExecCalls()); n != 0 {
		t.Fatalf("expected 0 Exec calls, got %d", n)
	}
}

// curlStatusMarkerFromArgs extracts the per-request -w status marker from curl argv.
func curlStatusMarkerFromArgs(t *testing.T, cmd []string) string {
	t.Helper()
	for i, arg := range cmd {
		if arg == "-w" && i+1 < len(cmd) {
			w := cmd[i+1]
			const suffix = "%{http_code}"
			if strings.HasSuffix(w, suffix) {
				return strings.TrimSuffix(w, suffix)
			}
		}
	}
	t.Fatalf("curl -w marker not found in %v", cmd)
	return ""
}

func TestResolveHTTPSource_UsesSandboxFetch(t *testing.T) {
	orig := appHTTPFetch
	defer func() { appHTTPFetch = orig }()

	var gotMethod, gotURL string
	var gotHeaders map[string]string
	var gotBody []byte
	appHTTPFetch = func(_ context.Context, _ *http.Request, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		gotMethod, gotURL, gotHeaders, gotBody = method, rawURL, headers, body
		return 200, []byte(`{"status":"ok"}`), nil
	}

	data, err := resolveHTTPSource(nil, "POST:https://github.wdf.sap.corp/api/graphql", map[string]any{
		"body": map[string]any{"query": "{ viewer { login } }"},
	})
	if err != nil {
		t.Fatalf("resolveHTTPSource: %v", err)
	}
	if gotMethod != "POST" || gotURL != "https://github.wdf.sap.corp/api/graphql" {
		t.Fatalf("fetch got %s %s", gotMethod, gotURL)
	}
	if !strings.Contains(string(gotBody), "viewer") {
		t.Fatalf("expected GraphQL body, got %s", gotBody)
	}
	if gotHeaders["Content-Type"] != "application/json" {
		t.Fatalf("missing Content-Type header: %v", gotHeaders)
	}
	m, ok := data.(map[string]any)
	if !ok || m["status"] != "ok" {
		t.Fatalf("unexpected result: %v", data)
	}
}

func TestResolveHTTPSource_HardBlockedBeforeFetch(t *testing.T) {
	orig := appHTTPFetch
	defer func() { appHTTPFetch = orig }()
	called := false
	appHTTPFetch = func(context.Context, *http.Request, string, string, map[string]string, []byte) (int, []byte, error) {
		called = true
		return 200, []byte(`{}`), nil
	}

	for _, spec := range []string{
		"GET:http://127.0.0.1/admin",
		"GET:http://169.254.169.254/latest/meta-data/",
	} {
		_, err := resolveHTTPSource(nil, spec, nil)
		if err == nil || !strings.Contains(err.Error(), "private/internal") {
			t.Fatalf("%s: expected private/internal error, got %v", spec, err)
		}
	}
	if called {
		t.Fatal("appHTTPFetch must not be called for hard-blocked URLs")
	}
}

func TestResolveHTTPSource_CredentialInjectedIntoFetch(t *testing.T) {
	orig := appHTTPFetch
	defer func() { appHTTPFetch = orig }()

	var gotAuth string
	appHTTPFetch = func(_ context.Context, _ *http.Request, _, _ string, headers map[string]string, _ []byte) (int, []byte, error) {
		gotAuth = headers["Authorization"]
		return 200, []byte(`{"ok":true}`), nil
	}

	credStore := &oauthRetryCredentialStore{staleToken: "tok", freshToken: "tok"}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(store.WithServices(r.Context(), &store.Services{
		Mode:        store.ModePlatform,
		Credentials: credStore,
	}))

	if _, err := resolveHTTPSource(r, "GET:https://api.example.com/data@my-cred", nil); err != nil {
		t.Fatalf("resolveHTTPSource: %v", err)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("expected credential header, got %q", gotAuth)
	}
}

func TestAppHTTPCurlCommand_WrapsK8sThroughChroot(t *testing.T) {
	cmd := []string{"curl", "-sS", "https://api.example.com/v1"}
	got := appHTTPCurlCommand(sandbox.BackendKindK8s, cmd)
	wantPrefix := []string{"/usr/local/bin/astonish-shell", "curl"}
	if len(got) < len(wantPrefix) || got[0] != wantPrefix[0] || got[1] != wantPrefix[1] {
		t.Fatalf("K8s curl command = %v, want prefix %v", got, wantPrefix)
	}
	if cmd[0] != "curl" {
		t.Fatalf("appHTTPCurlCommand mutated input: %v", cmd)
	}

	got = appHTTPCurlCommand(sandbox.BackendKindMock, cmd)
	if len(got) != len(cmd) || got[0] != "curl" {
		t.Fatalf("non-K8s curl command = %v, want original %v", got, cmd)
	}
}

func TestFetchHTTPViaSandbox_ExecCurl(t *testing.T) {
	origEnsure := ensureAppSandboxSession
	defer func() { ensureAppSandboxSession = origEnsure }()

	backend := mock.New()
	sessionID := "app-mcp-test-user"
	if _, err := backend.CreateSession(context.Background(), sandbox.SessionSpec{SessionID: sessionID}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	backend.ExecResultFn = func(_ string, opts sandbox.ExecSpec) (*sandbox.ExecResult, error) {
		if len(opts.Command) < 1 || opts.Command[0] != "curl" {
			t.Fatalf("expected curl, got %v", opts.Command)
		}
		foundURL := false
		for _, arg := range opts.Command {
			if arg == "https://api.example.com/v1" {
				foundURL = true
			}
		}
		if !foundURL {
			t.Fatalf("URL missing from curl args: %v", opts.Command)
		}
		marker := curlStatusMarkerFromArgs(t, opts.Command)
		return &sandbox.ExecResult{
			ExitCode: 0,
			Stdout:   []byte(`{"hello":"world"}` + marker + "200"),
		}, nil
	}

	ensureAppSandboxSession = func(context.Context, *http.Request, string) (sandbox.Backend, string, *config.AppConfig, func(), error) {
		return backend, sessionID, &config.AppConfig{}, func() {}, nil
	}

	status, body, err := fetchHTTPViaSandbox(context.Background(), nil, "GET", "https://api.example.com/v1", map[string]string{
		"Accept": "application/json",
	}, nil)
	if err != nil {
		t.Fatalf("fetchHTTPViaSandbox: %v", err)
	}
	if status != 200 || string(body) != `{"hello":"world"}` {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if n := len(backend.ExecCalls()); n != 1 {
		t.Fatalf("expected 1 Exec call, got %d", n)
	}
}

func TestFetchHTTPViaSandbox_Status000Errors(t *testing.T) {
	origEnsure := ensureAppSandboxSession
	defer func() { ensureAppSandboxSession = origEnsure }()

	backend := mock.New()
	sessionID := "app-mcp-status-000"
	if _, err := backend.CreateSession(context.Background(), sandbox.SessionSpec{SessionID: sessionID}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	backend.ExecResultFn = func(_ string, opts sandbox.ExecSpec) (*sandbox.ExecResult, error) {
		marker := curlStatusMarkerFromArgs(t, opts.Command)
		return &sandbox.ExecResult{
			ExitCode: 7,
			Stdout:   []byte(marker + "000"),
		}, nil
	}
	ensureAppSandboxSession = func(context.Context, *http.Request, string) (sandbox.Backend, string, *config.AppConfig, func(), error) {
		return backend, sessionID, &config.AppConfig{}, func() {}, nil
	}

	_, _, err := fetchHTTPViaSandbox(context.Background(), nil, "GET", "https://api.example.com/v1", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "curl status 000") {
		t.Fatalf("expected curl status 000 error, got %v", err)
	}
	if n := len(backend.ExecCalls()); n != 2 {
		t.Fatalf("expected 2 Exec calls (initial + retry), got %d", n)
	}
}

func TestFetchHTTPViaSandbox_Status000ClearsSeedAndRetries(t *testing.T) {
	origEnsure := ensureAppSandboxSession
	defer func() { ensureAppSandboxSession = origEnsure }()

	backend := mock.New()
	sessionID := "app-mcp-retry-000"
	netpolicy.MarkSessionSeeded(sessionID)
	t.Cleanup(func() { netpolicy.ClearSessionSeeded(sessionID) })

	if _, err := backend.CreateSession(context.Background(), sandbox.SessionSpec{SessionID: sessionID}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var calls atomic.Int32
	backend.ExecResultFn = func(_ string, opts sandbox.ExecSpec) (*sandbox.ExecResult, error) {
		n := calls.Add(1)
		marker := curlStatusMarkerFromArgs(t, opts.Command)
		if n == 1 {
			if !netpolicy.SessionIsSeeded(sessionID) {
				t.Error("expected session still seeded on first curl")
			}
			return &sandbox.ExecResult{
				ExitCode: 7,
				Stdout:   []byte(marker + "000"),
			}, nil
		}
		if netpolicy.SessionIsSeeded(sessionID) {
			t.Error("expected ClearSessionSeeded before retry curl")
		}
		return &sandbox.ExecResult{
			ExitCode: 0,
			Stdout:   []byte(`{"ok":true}` + marker + "200"),
		}, nil
	}
	ensureAppSandboxSession = func(context.Context, *http.Request, string) (sandbox.Backend, string, *config.AppConfig, func(), error) {
		return backend, sessionID, &config.AppConfig{}, func() {}, nil
	}

	status, body, err := fetchHTTPViaSandbox(context.Background(), nil, "GET", "https://api.example.com/v1", nil, nil)
	if err != nil {
		t.Fatalf("fetchHTTPViaSandbox: %v", err)
	}
	if status != 200 || string(body) != `{"ok":true}` {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 curl attempts, got %d", calls.Load())
	}
}

func TestWithRuntimeNetworkPolicyContext_AttachesStoresAndGateway(t *testing.T) {
	teamStore := &stubNetworkPolicyStore{rules: []store.NetworkPolicyRule{{
		Host:   "github.wdf.sap.corp",
		Port:   443,
		Action: store.NetworkPolicyAllow,
	}}}
	svc := &store.Services{TeamNetworkPolicies: teamStore}
	r := httptest.NewRequest(http.MethodPost, "/api/apps/data", nil)
	r = r.WithContext(store.WithServices(r.Context(), svc))

	appCfg := &config.AppConfig{}
	appCfg.Sandbox.OpenShell.GatewayAddr = "openshell.example:8443"

	ctx := withRuntimeNetworkPolicyContext(context.Background(), r, appCfg)
	nps := store.NetworkPolicyStoresFromContext(ctx)
	if nps == nil || nps.Team == nil {
		t.Fatal("expected NetworkPolicyStores on context")
	}
	gw := netpolicy.GatewayConfigFromContext(ctx)
	if gw == nil || gw.Addr != "openshell.example:8443" {
		t.Fatalf("expected gateway config, got %+v", gw)
	}
}

func TestDetachedRuntimeNetworkPolicyContext_DoesNotInheritRequestCancellation(t *testing.T) {
	teamStore := &stubNetworkPolicyStore{rules: []store.NetworkPolicyRule{{
		Host:   "registry.npmjs.org",
		Port:   443,
		Action: store.NetworkPolicyAllow,
	}}}
	svc := &store.Services{TeamNetworkPolicies: teamStore}
	reqCtx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/api/mcp/context7/refresh", nil).WithContext(reqCtx)
	r = r.WithContext(store.WithServices(r.Context(), svc))

	appCfg := &config.AppConfig{}
	appCfg.Sandbox.OpenShell.GatewayAddr = "openshell.example:8443"

	ctx := detachedRuntimeNetworkPolicyContext(r, appCfg)
	cancel()

	select {
	case <-ctx.Done():
		t.Fatal("detached runtime context should outlive request cancellation")
	default:
	}
	if nps := store.NetworkPolicyStoresFromContext(ctx); nps == nil || nps.Team != teamStore {
		t.Fatalf("expected detached context to carry team network policy store, got %+v", nps)
	}
	if gw := netpolicy.GatewayConfigFromContext(ctx); gw == nil || gw.Addr != "openshell.example:8443" {
		t.Fatalf("expected detached context to carry gateway config, got %+v", gw)
	}
}

type stubNetworkPolicyStore struct {
	rules []store.NetworkPolicyRule
	err   error
}

func (s *stubNetworkPolicyStore) List(context.Context) ([]store.NetworkPolicyRule, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.rules, nil
}
func (s *stubNetworkPolicyStore) Get(context.Context, string) (*store.NetworkPolicyRule, error) {
	return nil, nil
}
func (s *stubNetworkPolicyStore) Save(context.Context, *store.NetworkPolicyRule) error { return nil }
func (s *stubNetworkPolicyStore) Delete(context.Context, string) error                 { return nil }

// ── HTTP body unwrapping tests ───────────────────────────────────────

func TestExtractHTTPBodyAndHeaders(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		args        map[string]any
		wantBody    string            // JSON-encoded expected body, or "" for nil
		wantHeaders map[string]string // expected extracted headers
	}{
		{
			name:   "structured body+headers convention (POST)",
			method: "POST",
			args: map[string]any{
				"headers": map[string]any{
					"AI-Resource-Group": "default",
					"Content-Type":      "application/json",
				},
				"body": map[string]any{
					"messages":   []any{map[string]any{"role": "user", "content": "hello"}},
					"max_tokens": float64(4096),
				},
			},
			wantBody: `{"max_tokens":4096,"messages":[{"content":"hello","role":"user"}]}`,
			wantHeaders: map[string]string{
				"AI-Resource-Group": "default",
				"Content-Type":      "application/json",
			},
		},
		{
			name:   "flat args convention — no body key (POST)",
			method: "POST",
			args: map[string]any{
				"query": "SELECT * FROM tasks",
			},
			wantBody:    `{"query":"SELECT * FROM tasks"}`,
			wantHeaders: map[string]string{},
		},
		{
			name:   "flat args with headers — headers stripped from body (POST)",
			method: "POST",
			args: map[string]any{
				"headers": map[string]any{
					"Authorization": "Bearer token",
				},
				"query": "INSERT INTO tasks VALUES (1, 'test')",
			},
			wantBody: `{"query":"INSERT INTO tasks VALUES (1, 'test')"}`,
			wantHeaders: map[string]string{
				"Authorization": "Bearer token",
			},
		},
		{
			name:        "nil args — no body (POST)",
			method:      "POST",
			args:        nil,
			wantBody:    "",
			wantHeaders: map[string]string{},
		},
		{
			name:   "GET method — no body even with args",
			method: "GET",
			args: map[string]any{
				"query": "test",
			},
			wantBody:    "",
			wantHeaders: map[string]string{},
		},
		{
			name:   "GET method with headers — headers extracted, no body",
			method: "GET",
			args: map[string]any{
				"headers": map[string]any{"X-API-Key": "secret"},
				"query":   "test",
			},
			wantBody:    "",
			wantHeaders: map[string]string{"X-API-Key": "secret"},
		},
		{
			name:   "PUT method with structured body",
			method: "PUT",
			args: map[string]any{
				"headers": map[string]any{"X-Custom": "val"},
				"body":    map[string]any{"name": "updated"},
			},
			wantBody:    `{"name":"updated"}`,
			wantHeaders: map[string]string{"X-Custom": "val"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bodyData, headers := extractHTTPBodyAndHeaders(tt.method, tt.args)

			// Check body
			if tt.wantBody == "" {
				if bodyData != nil {
					t.Errorf("expected nil body, got %v", bodyData)
				}
			} else {
				if bodyData == nil {
					t.Fatal("expected non-nil body, got nil")
				}
				gotJSON, err := json.Marshal(bodyData)
				if err != nil {
					t.Fatalf("failed to marshal body: %v", err)
				}
				if string(gotJSON) != tt.wantBody {
					t.Errorf("body mismatch:\n  want: %s\n  got:  %s", tt.wantBody, string(gotJSON))
				}
				// Verify "headers" key never leaks into body
				if m, ok := bodyData.(map[string]any); ok {
					if _, leaked := m["headers"]; leaked {
						t.Error("headers key leaked into the HTTP body")
					}
				}
			}

			// Check headers
			if len(tt.wantHeaders) == 0 {
				if len(headers) != 0 {
					t.Errorf("expected no headers, got %v", headers)
				}
			} else {
				for k, v := range tt.wantHeaders {
					if got, ok := headers[k]; !ok {
						t.Errorf("missing header %q", k)
					} else if got != v {
						t.Errorf("header %q = %q, want %q", k, got, v)
					}
				}
				if len(headers) != len(tt.wantHeaders) {
					t.Errorf("header count = %d, want %d: %v", len(headers), len(tt.wantHeaders), headers)
				}
			}
		})
	}
}

// ── Retry-on-401 with token invalidation tests ──────────────────────

// oauthRetryCredentialStore is a test credential store that simulates an OAuth
// credential returning a stale token on first call and a fresh token after
// InvalidateToken is called.
type oauthRetryCredentialStore struct {
	resolveCount    atomic.Int32
	invalidateCount atomic.Int32
	staleToken      string
	freshToken      string
}

func (s *oauthRetryCredentialStore) Get(_ context.Context, _ string) *store.Credential { return nil }
func (s *oauthRetryCredentialStore) Set(_ context.Context, _ string, _ *store.Credential) error {
	return nil
}
func (s *oauthRetryCredentialStore) Remove(_ context.Context, _ string) error { return nil }
func (s *oauthRetryCredentialStore) List(_ context.Context) map[string]store.CredentialType {
	return nil
}
func (s *oauthRetryCredentialStore) Count(_ context.Context) int { return 0 }
func (s *oauthRetryCredentialStore) Resolve(_ context.Context, _ string) (string, string, error) {
	n := s.resolveCount.Add(1)
	if n == 1 {
		return "Authorization", "Bearer " + s.staleToken, nil
	}
	return "Authorization", "Bearer " + s.freshToken, nil
}
func (s *oauthRetryCredentialStore) InvalidateToken(_ context.Context, _ string) {
	s.invalidateCount.Add(1)
}
func (s *oauthRetryCredentialStore) SetSecret(_ context.Context, _, _ string) error { return nil }
func (s *oauthRetryCredentialStore) SetSecretBatch(_ context.Context, _ map[string]string) error {
	return nil
}
func (s *oauthRetryCredentialStore) GetSecret(_ context.Context, _ string) string   { return "" }
func (s *oauthRetryCredentialStore) RemoveSecret(_ context.Context, _ string) error { return nil }
func (s *oauthRetryCredentialStore) HasSecrets(_ context.Context) bool              { return false }
func (s *oauthRetryCredentialStore) SecretCount(_ context.Context) int              { return 0 }
func (s *oauthRetryCredentialStore) ListSecrets(_ context.Context) []string         { return nil }
func (s *oauthRetryCredentialStore) Reload(_ context.Context) error                 { return nil }

func TestResolveHTTPSource_RetryOn401(t *testing.T) {
	orig := appHTTPFetch
	defer func() { appHTTPFetch = orig }()

	var requestCount atomic.Int32
	appHTTPFetch = func(_ context.Context, _ *http.Request, _, _ string, headers map[string]string, _ []byte) (int, []byte, error) {
		n := requestCount.Add(1)
		auth := headers["Authorization"]
		if n == 1 {
			if auth != "Bearer stale-token" {
				t.Errorf("first request: expected stale token, got %q", auth)
			}
			return http.StatusUnauthorized, []byte(`{"error":"token expired"}`), nil
		}
		if auth != "Bearer fresh-token" {
			t.Errorf("second request: expected fresh token, got %q", auth)
		}
		return http.StatusOK, []byte(`{"status":"ok","data":"hello"}`), nil
	}

	credStore := &oauthRetryCredentialStore{
		staleToken: "stale-token",
		freshToken: "fresh-token",
	}
	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(store.WithServices(r.Context(), &store.Services{
		Mode:        store.ModePlatform,
		Credentials: credStore,
	}))

	result, err := resolveHTTPSource(r, "GET:https://api.example.com/data@my-oauth-cred", nil)
	if err != nil {
		t.Fatalf("resolveHTTPSource failed: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T: %v", result, result)
	}
	if m["status"] != "ok" {
		t.Errorf("unexpected result: %v", m)
	}
	if got := credStore.resolveCount.Load(); got != 2 {
		t.Errorf("expected 2 Resolve calls, got %d", got)
	}
	if got := credStore.invalidateCount.Load(); got != 1 {
		t.Errorf("expected 1 InvalidateToken call, got %d", got)
	}
	if got := requestCount.Load(); got != 2 {
		t.Errorf("expected 2 HTTP fetches, got %d", got)
	}
}

func TestResolveHTTPSource_NoRetryOnNonCredential401(t *testing.T) {
	orig := appHTTPFetch
	defer func() { appHTTPFetch = orig }()

	var requestCount atomic.Int32
	appHTTPFetch = func(context.Context, *http.Request, string, string, map[string]string, []byte) (int, []byte, error) {
		requestCount.Add(1)
		return http.StatusUnauthorized, []byte(`{"error":"unauthorized"}`), nil
	}

	_, err := resolveHTTPSource(nil, "GET:https://api.example.com/data", nil)
	if err == nil {
		t.Fatal("expected error from 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got: %v", err)
	}
	if got := requestCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 HTTP fetch (no retry), got %d", got)
	}
}

func TestResolveHTTPSource_RetryStill401(t *testing.T) {
	orig := appHTTPFetch
	defer func() { appHTTPFetch = orig }()

	var requestCount atomic.Int32
	appHTTPFetch = func(context.Context, *http.Request, string, string, map[string]string, []byte) (int, []byte, error) {
		requestCount.Add(1)
		return http.StatusUnauthorized, []byte(`{"error":"still unauthorized"}`), nil
	}

	credStore := &oauthRetryCredentialStore{
		staleToken: "bad-token",
		freshToken: "also-bad-token",
	}
	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(store.WithServices(r.Context(), &store.Services{
		Mode:        store.ModePlatform,
		Credentials: credStore,
	}))

	_, err := resolveHTTPSource(r, "GET:https://api.example.com/data@my-oauth-cred", nil)
	if err == nil {
		t.Fatal("expected error from persistent 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got: %v", err)
	}
	if got := requestCount.Load(); got != 2 {
		t.Errorf("expected 2 HTTP fetches (initial + retry), got %d", got)
	}
}

func TestResolveHTTPSource_NoRetryOn403(t *testing.T) {
	orig := appHTTPFetch
	defer func() { appHTTPFetch = orig }()

	var requestCount atomic.Int32
	appHTTPFetch = func(context.Context, *http.Request, string, string, map[string]string, []byte) (int, []byte, error) {
		requestCount.Add(1)
		return http.StatusForbidden, []byte(`{"error":"forbidden"}`), nil
	}

	credStore := &oauthRetryCredentialStore{
		staleToken: "valid-token",
		freshToken: "valid-token",
	}
	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(store.WithServices(r.Context(), &store.Services{
		Mode:        store.ModePlatform,
		Credentials: credStore,
	}))

	_, err := resolveHTTPSource(r, "GET:https://api.example.com/data@my-oauth-cred", nil)
	if err == nil {
		t.Fatal("expected error from 403 response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected 403 in error, got: %v", err)
	}
	if got := requestCount.Load(); got != 1 {
		t.Errorf("expected 1 HTTP fetch (no retry on 403), got %d", got)
	}
	if got := credStore.invalidateCount.Load(); got != 0 {
		t.Errorf("expected 0 InvalidateToken calls on 403, got %d", got)
	}
}
