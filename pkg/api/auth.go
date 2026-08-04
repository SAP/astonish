// Package api — device authorization flow for Astonish Studio.
//
// When auth is enabled (daemon mode), unauthenticated browsers see a short
// code. The user types /authorize in a channel (Telegram, CLI) and enters
// the code. The backend then issues a session cookie to the browser.
package api

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/gorilla/mux"
)

const (
	// codeLength is the number of characters in an authorization code.
	codeLength = 6
	// codeExpiry is how long a pending code is valid.
	codeExpiry = 5 * time.Minute
	// maxPendingCodes limits the number of pending authorization codes.
	maxPendingCodes = 5
	// cookieName is the HTTP cookie name for authorized sessions.
	cookieName = "astonish_session"
	// cookieMaxAge is the cookie max-age in seconds (90 days).
	cookieMaxAge = 90 * 24 * 60 * 60
)

// codeAlphabet excludes ambiguous characters: 0/O, 1/I/L
var codeAlphabet = []byte("ABCDEFGHJKMNPQRSTUVWXYZ23456789")

// pendingCode represents an authorization code waiting for channel confirmation.
type pendingCode struct {
	Code      string
	CreatedAt time.Time
	UserAgent string
	IP        string
	// authorized is set to true when the code is confirmed via /authorize.
	// The token is the session token to issue to the browser.
	authorized bool
	token      string
}

// AuthManager handles the device authorization flow.
// It tracks pending codes (in-memory) and delegates session persistence
// to the AuthStore.
type AuthManager struct {
	mu      sync.Mutex
	codes   []*pendingCode
	store   *AuthStore
	enabled bool
}

// NewAuthManager creates an AuthManager backed by the given session store.
func NewAuthManager(store *AuthStore) *AuthManager {
	return &AuthManager{
		store:   store,
		enabled: true,
	}
}

// GenerateCode creates a new pending authorization code.
func (am *AuthManager) GenerateCode(userAgent, ip string) string {
	code := randomCode()

	am.mu.Lock()
	defer am.mu.Unlock()

	// Evict expired codes
	am.evictExpiredLocked()

	// Evict oldest if at capacity
	for len(am.codes) >= maxPendingCodes {
		am.codes = am.codes[1:]
	}

	am.codes = append(am.codes, &pendingCode{
		Code:      code,
		CreatedAt: time.Now(),
		UserAgent: userAgent,
		IP:        ip,
	})
	return code
}

// AuthorizeCode validates a code from a channel command and creates a session.
// Returns a human-readable result message and whether the authorization succeeded.
func (am *AuthManager) AuthorizeCode(code string) (string, bool) {
	code = strings.TrimSpace(strings.ToUpper(code))
	if code == "" {
		return "No code provided. Please enter the code shown on the Studio page.", false
	}

	am.mu.Lock()
	defer am.mu.Unlock()

	am.evictExpiredLocked()

	for _, pc := range am.codes {
		if pc.Code == code {
			if pc.authorized {
				return "This code has already been used.", false
			}
			// Create session
			token, err := am.store.CreateSession(pc.UserAgent, pc.IP)
			if err != nil {
				slog.Error("failed to create session", "component", "auth", "error", err)
				return "Failed to authorize. Please try again.", false
			}
			pc.authorized = true
			pc.token = token
			return "Authorized! The Studio page will refresh automatically.", true
		}
	}

	return "Invalid or expired code. Please refresh the Studio page for a new code.", false
}

// CheckCodeStatus checks if a pending code has been authorized.
// Returns the session token if authorized, empty string otherwise.
func (am *AuthManager) CheckCodeStatus(code string) string {
	code = strings.TrimSpace(strings.ToUpper(code))

	am.mu.Lock()
	defer am.mu.Unlock()

	for _, pc := range am.codes {
		if pc.Code == code && pc.authorized {
			return pc.token
		}
	}
	return ""
}

// ValidateRequest checks if the request has a valid session cookie.
func (am *AuthManager) ValidateRequest(r *http.Request) bool {
	if !am.enabled {
		return true
	}
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return am.store.ValidateToken(cookie.Value)
}

// evictExpiredLocked removes expired pending codes. Caller must hold am.mu.
func (am *AuthManager) evictExpiredLocked() {
	now := time.Now()
	alive := am.codes[:0]
	for _, pc := range am.codes {
		if now.Sub(pc.CreatedAt) < codeExpiry {
			alive = append(alive, pc)
		}
	}
	am.codes = alive
}

// randomCode generates a random code of codeLength from the code alphabet.
func randomCode() string {
	b := make([]byte, codeLength)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		b[i] = codeAlphabet[idx.Int64()]
	}
	return string(b)
}

// --- HTTP Handlers ---

// RegisterAuthRoutes registers the authentication endpoints on the router.
// These endpoints are always accessible (not behind auth middleware).
func RegisterAuthRoutes(router *mux.Router, am *AuthManager) {
	router.HandleFunc("/api/auth/code", am.handleGetCode).Methods("GET")
	router.HandleFunc("/api/auth/status", am.handleCheckStatus).Methods("GET")
	router.HandleFunc("/api/auth/authorize", am.handleAuthorize).Methods("POST")
}

// handleGetCode generates a new authorization code and returns it.
// GET /api/auth/code
func (am *AuthManager) handleGetCode(w http.ResponseWriter, r *http.Request) {
	code := am.GenerateCode(r.UserAgent(), r.RemoteAddr)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"code": code})
}

// handleCheckStatus checks if a pending code has been authorized.
// GET /api/auth/status?code=XXXXXX
// Returns {"authorized": true} with a Set-Cookie if authorized.
func (am *AuthManager) handleCheckStatus(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		respondError(w, http.StatusBadRequest, `{"error":"missing code parameter"}`)
		return
	}

	token := am.CheckCodeStatus(code)
	if token == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"authorized": false})
		return
	}

	// Set the session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"authorized": true})
}

// handleAuthorize validates an authorization code submitted from CLI or channel.
// POST /api/auth/authorize  {"code": "XXXXXX"}
func (am *AuthManager) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, `{"error":"invalid request body"}`)
		return
	}
	msg, ok := am.AuthorizeCode(req.Code)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"message": msg, "authorized": ok})
}

// AuthMiddleware returns HTTP middleware that enforces device authorization.
// Requests from loopback addresses (CLI, in-process calls) are always allowed.
// Requests to /api/auth/* are always allowed (they are the auth flow).
// API requests get 401. UI requests get the embedded auth page.
func AuthMiddleware(am *AuthManager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Loopback bypass — CLI commands and in-process calls always come
		// from localhost. Auth is for remote browser sessions, not local tools.
		if isLoopback(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}

		path := r.URL.Path

		// Health endpoints are always accessible (K8s probes)
		if path == "/api/healthz" || path == "/api/readyz" {
			next.ServeHTTP(w, r)
			return
		}

		// Auth endpoints are always accessible
		if strings.HasPrefix(path, "/api/auth/") {
			next.ServeHTTP(w, r)
			return
		}

		// Slack webhooks are authenticated by Slack request signatures in the channel handler.
		if path == "/api/slack/events" || path == "/api/slack/commands" {
			next.ServeHTTP(w, r)
			return
		}

		// Browser VNC proxy is accessed via iframe where the session cookie
		// may not be sent (SameSite=Strict blocks cookies on iframe navigations).
		// Instead of a blanket bypass, we validate that the container has an
		// active handoff token. The initial page load carries the token in the
		// query string (?token=...). Sub-resource requests (JS, CSS, images)
		// don't have the token in the URL, but we allow them if the container
		// has any active token registered (the handoff is in progress).
		if strings.HasPrefix(path, "/api/browser/vnc/") {
			if validateVNCAccess(r) {
				next.ServeHTTP(w, r)
				return
			}
			// Fall through to 401 below
		}

		// Check session cookie
		if am.ValidateRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Unauthorized
		if strings.HasPrefix(path, "/api/") {
			// API requests get 401 JSON
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"error":   "unauthorized",
				"message": "Device not authorized. Open the Studio in a browser and use /authorize in a channel to authenticate.",
			})
			return
		}

		// UI requests get the auth page
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, authPageHTML)
	})
}

// isLoopback returns true if the remote address is a loopback address
// (127.0.0.1, [::1], or any 127.x.x.x). The addr is typically in
// "host:port" format from http.Request.RemoteAddr.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr // no port, use as-is
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validateVNCAccess checks if the request is authorized to access the
// VNC proxy. It extracts the container name from the URL path and
// checks two things:
//
//  1. If the request has a ?token= query param, validate it directly.
//     This is the initial iframe page load.
//
//  2. Otherwise, check if the container has any active handoff token.
//     This covers sub-resource requests (JS, CSS, images) that the
//     KasmVNC page loads and which don't carry the token.
func validateVNCAccess(r *http.Request) bool {
	// Extract container name from path: /api/browser/vnc/{container}/...
	path := r.URL.Path
	const prefix = "/api/browser/vnc/"
	rest := path[len(prefix):]
	container := rest
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		container = rest[:idx]
	}
	if container == "" {
		return false
	}

	registry := browser.GetHandoffTokenRegistry()

	// Check explicit token in query string (initial page load)
	if token := r.URL.Query().Get("token"); token != "" {
		if tokenContainer, ok := registry.ValidateToken(token); ok && tokenContainer == container {
			return true
		}
		return false
	}

	// Sub-resource request — validate that container has an active handoff
	return registry.ValidateContainer(container)
}

// authPageHTML is a self-contained HTML page that displays the authorization code
// and polls for completion. No external dependencies.
var authPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Astonish Studio — Authorize</title>
<style>
  :root {
    --bg: #fafbfe; --surface: #ffffff; --text: #0b1222;
    --muted: #6b7280; --border: #e5e8f0; --accent: #7c3aed;
    --accent-soft: #eeebff; --shadow: 0 8px 28px rgba(15, 23, 42, 0.1);
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0b1222; --surface: #0f172a; --text: #f6f7fb;
      --muted: #9ca3af; --border: rgba(255,255,255,0.08); --accent: #8d7ae0;
      --accent-soft: rgba(141, 122, 224, 0.12); --shadow: 0 10px 32px rgba(0, 0, 0, 0.42);
    }
  }
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
    background: var(--bg); color: var(--text);
    display: flex; align-items: center; justify-content: center;
    min-height: 100vh; padding: 24px;
  }
  .card {
    background: var(--surface); border: 1px solid var(--border);
    border-radius: 16px; padding: 48px; max-width: 440px; width: 100%;
    text-align: center; box-shadow: var(--shadow);
  }
  h1 { font-size: 20px; font-weight: 600; color: var(--text); margin-bottom: 8px; }
  .subtitle { font-size: 14px; color: var(--muted); margin-bottom: 32px; line-height: 1.5; }
  .code-display {
    font-family: ui-monospace, 'SF Mono', 'Fira Code', 'Cascadia Code', Consolas, monospace;
    font-size: 36px; font-weight: 700; letter-spacing: 8px;
    color: var(--accent); background: var(--bg); border: 1px solid var(--border);
    border-radius: 12px; padding: 20px; margin-bottom: 32px;
    user-select: all;
  }
  .steps { text-align: left; font-size: 13px; line-height: 2; color: var(--muted); }
  .steps strong { color: var(--text); font-weight: 500; }
  .steps code {
    background: var(--accent-soft); padding: 2px 8px; border-radius: 6px;
    font-family: ui-monospace, 'SF Mono', monospace; font-size: 12px; color: var(--accent);
  }
  .status { margin-top: 24px; padding-top: 20px; border-top: 1px solid var(--border); font-size: 13px; color: var(--muted); }
  .status.ok { color: #16a34a; font-weight: 500; }
  .status.err { color: #ef4444; }
  .spinner {
    display: inline-block; width: 12px; height: 12px;
    border: 2px solid var(--border); border-top-color: var(--accent);
    border-radius: 50%; animation: spin 0.8s linear infinite;
    vertical-align: middle; margin-right: 6px;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
</head>
<body>
<div class="card">
  <h1>Authorize this device</h1>
  <p class="subtitle">Enter this code in one of your connected channels to grant access.</p>
  <div class="code-display" id="code">------</div>
  <div class="steps">
    <p><strong>1.</strong> Open <strong>Telegram</strong> or <strong>CLI chat</strong></p>
    <p><strong>2.</strong> Type <code>/authorize</code></p>
    <p><strong>3.</strong> Enter the code shown above</p>
  </div>
  <div class="status" id="status"><span class="spinner"></span>Waiting for authorization...</div>
</div>
<script>
(function() {
  var code = '';
  var statusEl = document.getElementById('status');
  var codeEl = document.getElementById('code');
  var polling = null;

  function fetchCode() {
    fetch('/api/auth/code')
      .then(function(r) { return r.json(); })
      .then(function(d) {
        code = d.code;
        codeEl.textContent = code;
        startPolling();
      })
      .catch(function() {
        statusEl.className = 'status err';
        statusEl.textContent = 'Failed to get authorization code. Retrying...';
        setTimeout(fetchCode, 3000);
      });
  }

  function startPolling() {
    if (polling) clearInterval(polling);
    polling = setInterval(checkStatus, 3000);
  }

  function checkStatus() {
    if (!code) return;
    fetch('/api/auth/status?code=' + encodeURIComponent(code))
      .then(function(r) { return r.json(); })
      .then(function(d) {
        if (d.authorized) {
          clearInterval(polling);
          statusEl.className = 'status ok';
          statusEl.textContent = 'Authorized! Redirecting...';
          setTimeout(function() { window.location.reload(); }, 500);
        }
      })
      .catch(function() {});
  }

  fetchCode();

  // Refresh code if it expires (5 minutes)
  setTimeout(function() {
    clearInterval(polling);
    statusEl.className = 'status err';
    statusEl.textContent = 'Code expired. Refreshing...';
    setTimeout(fetchCode, 1000);
  }, 5 * 60 * 1000);
})();
</script>
</body>
</html>`
