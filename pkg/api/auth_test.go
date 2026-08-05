package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// --- AuthStore Tests ---

func TestAuthStore_CreateAndValidate(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAuthStore(dir, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewAuthStore() error = %v", err)
	}

	token, err := store.CreateSession("test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	if len(token) != 64 { // 32 bytes hex-encoded
		t.Errorf("token length = %d, want 64", len(token))
	}

	if !store.ValidateToken(token) {
		t.Error("ValidateToken() = false for valid token")
	}

	if store.ValidateToken("invalid-token") {
		t.Error("ValidateToken() = true for invalid token")
	}
}

func TestAuthStore_Persistence(t *testing.T) {
	dir := t.TempDir()

	// Create store and add a session
	store1, err := NewAuthStore(dir, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewAuthStore() error = %v", err)
	}
	token, err := store1.CreateSession("test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	// Verify file was written
	storePath := filepath.Join(dir, sessionFileName)
	if _, err := os.Stat(storePath); err != nil {
		t.Fatalf("session file not created: %v", err)
	}

	// Create a new store from the same directory — session should persist
	store2, err := NewAuthStore(dir, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewAuthStore() on reload error = %v", err)
	}
	if !store2.ValidateToken(token) {
		t.Error("token not valid after reload from disk")
	}
}

func TestAuthStore_TTLExpiry(t *testing.T) {
	dir := t.TempDir()
	// Very short TTL
	store, err := NewAuthStore(dir, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("NewAuthStore() error = %v", err)
	}

	token, err := store.CreateSession("test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	// Wait for expiry
	time.Sleep(10 * time.Millisecond)

	if store.ValidateToken(token) {
		t.Error("ValidateToken() = true for expired token")
	}
}

func TestAuthStore_SessionCount(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAuthStore(dir, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewAuthStore() error = %v", err)
	}

	if store.SessionCount() != 0 {
		t.Errorf("SessionCount() = %d, want 0", store.SessionCount())
	}

	store.CreateSession("ua1", "1.1.1.1")
	store.CreateSession("ua2", "2.2.2.2")

	if store.SessionCount() != 2 {
		t.Errorf("SessionCount() = %d, want 2", store.SessionCount())
	}
}

func TestAuthStore_TokenHashing(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAuthStore(dir, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewAuthStore() error = %v", err)
	}

	store.CreateSession("test", "127.0.0.1")

	// Read the file — tokens should be stored as hashes, not raw
	data, err := os.ReadFile(filepath.Join(dir, sessionFileName))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var sessions []*AuthSession
	if err := json.Unmarshal(data, &sessions); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	// Token hash should be 64 hex chars (SHA-256)
	if len(sessions[0].TokenHash) != 64 {
		t.Errorf("token hash length = %d, want 64", len(sessions[0].TokenHash))
	}
}

// --- AuthManager Tests ---

func TestAuthManager_CodeGeneration(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	code := am.GenerateCode("test-agent", "127.0.0.1")

	if len(code) != codeLength {
		t.Errorf("code length = %d, want %d", len(code), codeLength)
	}

	// Code should only contain allowed characters
	for _, c := range code {
		if !strings.ContainsRune(string(codeAlphabet), c) {
			t.Errorf("code contains invalid character: %c", c)
		}
	}
}

func TestAuthManager_CodeMaxPending(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	// Generate maxPendingCodes + 2 codes
	for i := 0; i < maxPendingCodes+2; i++ {
		am.GenerateCode("test", "127.0.0.1")
	}

	// Oldest codes should be evicted
	am.mu.Lock()
	count := len(am.codes)
	am.mu.Unlock()

	if count > maxPendingCodes {
		t.Errorf("pending codes = %d, want <= %d", count, maxPendingCodes)
	}
}

func TestAuthManager_AuthorizeCode(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	code := am.GenerateCode("test-agent", "127.0.0.1")

	// Authorize with the correct code
	msg, ok := am.AuthorizeCode(code)
	if !ok {
		t.Fatalf("AuthorizeCode() failed: %s", msg)
	}
	if !strings.Contains(msg, "Authorized") {
		t.Errorf("expected success message, got: %s", msg)
	}

	// Check that the browser can now get the token
	token := am.CheckCodeStatus(code)
	if token == "" {
		t.Fatal("CheckCodeStatus() returned empty token after authorization")
	}

	// Validate the token works
	if !store.ValidateToken(token) {
		t.Error("token from authorization is not valid")
	}
}

func TestAuthManager_AuthorizeInvalidCode(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	am.GenerateCode("test-agent", "127.0.0.1")

	msg, ok := am.AuthorizeCode("WRONG1")
	if ok {
		t.Fatal("AuthorizeCode() should fail for wrong code")
	}
	if !strings.Contains(msg, "Invalid") {
		t.Errorf("expected 'Invalid' message, got: %s", msg)
	}
}

func TestAuthManager_AuthorizeEmpty(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	msg, ok := am.AuthorizeCode("")
	if ok {
		t.Fatal("AuthorizeCode() should fail for empty code")
	}
	if !strings.Contains(msg, "No code") {
		t.Errorf("expected 'No code' message, got: %s", msg)
	}
}

func TestAuthManager_DoubleAuthorize(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	code := am.GenerateCode("test-agent", "127.0.0.1")
	am.AuthorizeCode(code)

	// Second authorization of the same code should fail
	msg, ok := am.AuthorizeCode(code)
	if ok {
		t.Fatal("AuthorizeCode() should fail for already-used code")
	}
	if !strings.Contains(msg, "already been used") {
		t.Errorf("expected 'already been used' message, got: %s", msg)
	}
}

func TestAuthManager_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	code := am.GenerateCode("test-agent", "127.0.0.1")

	// Authorize with lowercase version
	_, ok := am.AuthorizeCode(strings.ToLower(code))
	if !ok {
		t.Fatal("AuthorizeCode() should accept lowercase codes")
	}
}

// --- HTTP Handler Tests ---

func TestAuthHandlers_GetCode(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	router := mux.NewRouter()
	RegisterAuthRoutes(router, am)

	req := httptest.NewRequest("GET", "/api/auth/code", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json unmarshal error: %v", err)
	}
	if resp["code"] == "" {
		t.Fatal("expected non-empty code in response")
	}
	if len(resp["code"]) != codeLength {
		t.Errorf("code length = %d, want %d", len(resp["code"]), codeLength)
	}
}

func TestAuthHandlers_StatusNotAuthorized(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	router := mux.NewRouter()
	RegisterAuthRoutes(router, am)

	am.GenerateCode("test", "127.0.0.1") // generate but don't authorize

	req := httptest.NewRequest("GET", "/api/auth/status?code=WRONG1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]bool
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["authorized"] {
		t.Error("expected authorized=false")
	}
}

func TestAuthHandlers_FullFlow(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	router := mux.NewRouter()
	RegisterAuthRoutes(router, am)

	// Step 1: Browser requests a code
	req1 := httptest.NewRequest("GET", "/api/auth/code", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	var codeResp map[string]string
	json.Unmarshal(w1.Body.Bytes(), &codeResp)
	code := codeResp["code"]

	// Step 2: Channel authorizes the code
	msg, ok := am.AuthorizeCode(code)
	if !ok {
		t.Fatalf("AuthorizeCode failed: %s", msg)
	}

	// Step 3: Browser polls and gets authorized + cookie
	req2 := httptest.NewRequest("GET", "/api/auth/status?code="+code, nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w2.Code)
	}

	var statusResp map[string]bool
	json.Unmarshal(w2.Body.Bytes(), &statusResp)
	if !statusResp["authorized"] {
		t.Fatal("expected authorized=true after code authorization")
	}

	// Check that a Set-Cookie header was returned
	cookies := w2.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == cookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected session cookie to be set")
		return
	}
	if !sessionCookie.HttpOnly {
		t.Error("session cookie should be HttpOnly")
	}

	// Step 4: Verify the cookie works for validation
	if !store.ValidateToken(sessionCookie.Value) {
		t.Error("session cookie token is not valid")
	}
}

// --- Middleware Tests ---

func TestAuthMiddleware_AllowsAuthEndpoints(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	handler := AuthMiddleware(am, inner)

	req := httptest.NewRequest("GET", "/api/auth/code", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("auth endpoint should be accessible, got status %d", w.Code)
	}
}

func TestAuthMiddleware_AllowsSlackWebhookEndpoints(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	handler := AuthMiddleware(am, inner)

	for _, path := range []string{"/api/slack/events", "/api/slack/commands"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("POST", path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Slack webhook endpoint should be accessible, got status %d", w.Code)
			}
		})
	}
}

func TestAuthMiddleware_BlocksAPIWithout(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := AuthMiddleware(am, inner)

	req := httptest.NewRequest("GET", "/api/agents", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("API without cookie should be 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_ServesAuthPageForUI(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := AuthMiddleware(am, inner)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("UI request should get 200 with auth page, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Authorize this device") {
		t.Error("expected auth page HTML in response")
	}
}

func TestAuthMiddleware_AllowsWithValidCookie(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	token, _ := store.CreateSession("test", "127.0.0.1")

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("authenticated"))
	})
	handler := AuthMiddleware(am, inner)

	req := httptest.NewRequest("GET", "/api/agents", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("request with valid cookie should be 200, got %d", w.Code)
	}
	if w.Body.String() != "authenticated" {
		t.Errorf("expected 'authenticated', got %q", w.Body.String())
	}
}

func TestAuthMiddleware_RejectsExpiredCookie(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 1*time.Millisecond)
	am := NewAuthManager(store)

	token, _ := store.CreateSession("test", "127.0.0.1")
	time.Sleep(10 * time.Millisecond)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := AuthMiddleware(am, inner)

	req := httptest.NewRequest("GET", "/api/agents", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expired cookie should be 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_AllowsLoopback(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	handler := AuthMiddleware(am, inner)

	tests := []struct {
		name       string
		remoteAddr string
	}{
		{"IPv4 loopback", "127.0.0.1:12345"},
		{"IPv6 loopback", "[::1]:12345"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/scheduler/jobs", nil)
			req.RemoteAddr = tt.remoteAddr
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("loopback request from %s should be allowed, got status %d", tt.remoteAddr, w.Code)
			}
		})
	}
}

func TestAuthMiddleware_BlocksNonLoopback(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewAuthStore(dir, 24*time.Hour)
	am := NewAuthManager(store)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := AuthMiddleware(am, inner)

	// Non-loopback address without auth should still be blocked
	req := httptest.NewRequest("GET", "/api/scheduler/jobs", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("non-loopback request without auth should be 401, got %d", w.Code)
	}
}

func TestIsLoopback(t *testing.T) {
	tests := []struct {
		addr     string
		expected bool
	}{
		{"127.0.0.1:8080", true},
		{"127.0.0.1:0", true},
		{"[::1]:8080", true},
		{"192.168.1.1:8080", false},
		{"10.0.0.1:8080", false},
		{"0.0.0.0:8080", false},
		{"", false},
		{"invalid", false},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got := isLoopback(tt.addr)
			if got != tt.expected {
				t.Errorf("isLoopback(%q) = %v, want %v", tt.addr, got, tt.expected)
			}
		})
	}
}
