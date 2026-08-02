package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/netpolicy"
)

// appHTTPFetch performs an HTTP request via the App sandbox (curl Exec).
// Overridable in tests.
var appHTTPFetch = fetchHTTPViaSandbox

func fetchHTTPViaSandbox(ctx context.Context, r *http.Request, method, rawURL string, headers map[string]string, body []byte) (status int, respBody []byte, err error) {
	userID := "anonymous"
	if r != nil {
		userID = effectiveUserID(r)
	}

	backend, sessionID, appCfg, cleanup, err := ensureAppSandboxSession(ctx, r, userID)
	if err != nil {
		return 0, nil, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	status, respBody, err = execAppHTTPCurl(ctx, backend, sessionID, method, rawURL, headers, body)
	if err != nil || status == 0 {
		// Transport failure (curl 000 / Exec error): clear sticky PreSeed and retry once.
		slog.Warn("app HTTP sandbox transport failure; clearing PreSeed and retrying once",
			"session", sessionID, "status", status, "error", err)
		netpolicy.ClearSessionSeeded(sessionID)
		seedCtx := withRuntimeNetworkPolicyContext(ctx, r, appCfg)
		netpolicy.EnsurePreSeedFromContext(seedCtx, sessionID)
		status, respBody, err = execAppHTTPCurl(ctx, backend, sessionID, method, rawURL, headers, body)
	}
	if err != nil {
		return 0, nil, err
	}
	if status == 0 {
		return 0, nil, fmt.Errorf("HTTP request failed: no response from server (curl status 000)")
	}
	return status, respBody, nil
}

func execAppHTTPCurl(ctx context.Context, backend sandbox.Backend, sessionID, method, rawURL string, headers map[string]string, body []byte) (status int, respBody []byte, err error) {
	marker := newAppHTTPStatusMarker()
	cmd := []string{
		"curl", "-sS",
		"-X", method,
		"--max-redirs", "5",
		"--max-time", "30",
		"-w", marker + "%{http_code}",
	}
	// Header values (including credentials) appear in sandbox process argv;
	// App sessions are per-user. Reject CR/LF to prevent header injection.
	for k, v := range headers {
		if err := validateCurlHeader(k, v); err != nil {
			return 0, nil, err
		}
		cmd = append(cmd, "-H", k+": "+v)
	}

	var stdin io.Reader
	if len(body) > 0 {
		cmd = append(cmd, "--data-binary", "@-")
		stdin = bytes.NewReader(body)
	}
	cmd = append(cmd, rawURL)
	backendKind := sandbox.BackendKind("")
	if backend != nil {
		backendKind = backend.Kind()
	}
	execCmd := appHTTPCurlCommand(backendKind, cmd)

	slog.Debug("app HTTP sandbox fetch", "method", method, "url", rawURL, "session", sessionID)
	result, err := backend.Exec(ctx, sessionID, sandbox.ExecSpec{
		Command: execCmd,
		Stdin:   stdin,
	})
	// Refresh idle deadline even on Exec error — the session was still used.
	appMCPIdleTracker.touch(sessionID)
	if err != nil {
		return 0, nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	// curl non-zero exit often still includes status/body (e.g. connection errors).
	respBody, status, parseErr := parseCurlHTTPOutput(result.Stdout, marker)
	if parseErr != nil {
		stderr := strings.TrimSpace(string(result.Stderr))
		if stderr == "" {
			stderr = strings.TrimSpace(string(result.Stdout))
		}
		if result.ExitCode != 0 {
			return 0, nil, fmt.Errorf("HTTP request failed: curl exit %d: %s", result.ExitCode, stderr)
		}
		return 0, nil, fmt.Errorf("HTTP request failed: %w", parseErr)
	}
	if len(respBody) > maxResponseBodySize {
		respBody = respBody[:maxResponseBodySize]
	}
	return status, respBody, nil
}

func appHTTPCurlCommand(kind sandbox.BackendKind, cmd []string) []string {
	if kind == sandbox.BackendKindK8s {
		return append([]string{"/usr/local/bin/astonish-shell"}, cmd...)
	}
	return cmd
}

// newAppHTTPStatusMarker returns a per-request marker so response bodies that
// contain a fixed literal cannot collide with curl -w status parsing.
func newAppHTTPStatusMarker() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely; fall back to a fixed unique-enough prefix.
		return "\nASTONISH_HTTP_STATUS_fallback:"
	}
	return fmt.Sprintf("\nASTONISH_HTTP_STATUS_%x:", b)
}

// validateCurlHeader rejects empty keys and CR/LF in keys or values so a
// credential/header value cannot inject additional HTTP headers via curl -H.
func validateCurlHeader(key, value string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("HTTP request failed: empty header name")
	}
	if strings.ContainsAny(key, "\r\n") {
		return fmt.Errorf("HTTP request failed: header name %q contains CR/LF", key)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("HTTP request failed: header %q value contains CR/LF", key)
	}
	return nil
}

func parseCurlHTTPOutput(stdout []byte, marker string) (body []byte, status int, err error) {
	if marker == "" {
		return nil, 0, fmt.Errorf("curl status marker is empty")
	}
	idx := bytes.LastIndex(stdout, []byte(marker))
	if idx < 0 {
		return nil, 0, fmt.Errorf("curl did not report HTTP status")
	}
	body = stdout[:idx]
	statusStr := strings.TrimSpace(string(stdout[idx+len(marker):]))
	status, err = strconv.Atoi(statusStr)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid HTTP status %q from curl: %w", statusStr, err)
	}
	return body, status, nil
}

// validateAppHTTPURL rejects non-http(s) schemes and hard-blocked IP literals
// (loopback / link-local / metadata). Soft-private hosts are allowed here —
// sandbox L7 + Network Policy PreSeed enforce egress. Hostnames are not
// resolved on the Studio host.
func validateAppHTTPURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q (only http and https are allowed)", parsed.Scheme)
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isHardBlockedIP(ip) {
			return fmt.Errorf("requests to private/internal networks are not allowed (%s)", ip)
		}
	}
	return nil
}
