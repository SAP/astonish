package a2a

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// generateTestRSAKey creates a test RSA key pair and returns the private key and JWKS JSON.
func generateTestRSAKey(kid string) (*rsa.PrivateKey, []byte) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}

	nBytes := privateKey.PublicKey.N.Bytes()
	eBytes := big.NewInt(int64(privateKey.PublicKey.E)).Bytes()

	jwks := map[string]interface{}{
		"keys": []map[string]interface{}{
			{
				"kty": "RSA",
				"kid": kid,
				"n":   base64.RawURLEncoding.EncodeToString(nBytes),
				"e":   base64.RawURLEncoding.EncodeToString(eBytes),
			},
		},
	}

	data, err := json.Marshal(jwks)
	if err != nil {
		panic(err)
	}
	return privateKey, data
}

func TestJWKS_FetchAndParseRSAKey(t *testing.T) {
	privateKey, jwksData := generateTestRSAKey("test-key-1")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwksData)
	}))
	defer server.Close()

	client := NewJWKSClient()

	key, err := client.GetKey(server.URL, "test-key-1")
	if err != nil {
		t.Fatalf("GetKey failed: %v", err)
	}

	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("expected *rsa.PublicKey, got %T", key)
	}

	if rsaKey.N.Cmp(privateKey.PublicKey.N) != 0 {
		t.Error("RSA N mismatch")
	}
	if rsaKey.E != privateKey.PublicKey.E {
		t.Error("RSA E mismatch")
	}
}

func TestJWKS_CacheHit(t *testing.T) {
	_, jwksData := generateTestRSAKey("cached-key")

	var fetchCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwksData)
	}))
	defer server.Close()

	client := NewJWKSClient(WithCacheTTL(1 * time.Hour))

	// First call — should fetch
	_, err := client.GetKey(server.URL, "cached-key")
	if err != nil {
		t.Fatalf("first GetKey failed: %v", err)
	}

	// Second call — should use cache
	_, err = client.GetKey(server.URL, "cached-key")
	if err != nil {
		t.Fatalf("second GetKey failed: %v", err)
	}

	if count := fetchCount.Load(); count != 1 {
		t.Errorf("expected 1 HTTP fetch, got %d", count)
	}
}

func TestJWKS_ForcedRefreshOnUnknownKid(t *testing.T) {
	_, jwksData1 := generateTestRSAKey("key-1")

	// Build JWKS with both keys for the second response
	privateKey2, _ := generateTestRSAKey("key-2")
	nBytes2 := privateKey2.PublicKey.N.Bytes()
	eBytes2 := big.NewInt(int64(privateKey2.PublicKey.E)).Bytes()

	jwks2 := map[string]interface{}{
		"keys": []map[string]interface{}{
			{
				"kty": "RSA",
				"kid": "key-1",
				"n":   base64.RawURLEncoding.EncodeToString(privateKey2.PublicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey2.PublicKey.E)).Bytes()),
			},
			{
				"kty": "RSA",
				"kid": "key-2",
				"n":   base64.RawURLEncoding.EncodeToString(nBytes2),
				"e":   base64.RawURLEncoding.EncodeToString(eBytes2),
			},
		},
	}
	jwksData2, _ := json.Marshal(jwks2)

	var fetchCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := fetchCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if count == 1 {
			w.Write(jwksData1)
		} else {
			w.Write(jwksData2)
		}
	}))
	defer server.Close()

	// Use very short min refresh delay for testing
	client := NewJWKSClient(
		WithCacheTTL(1*time.Hour),
		WithMinRefreshDelay(0), // No rate limiting for test
	)

	// First call — fetches key-1
	_, err := client.GetKey(server.URL, "key-1")
	if err != nil {
		t.Fatalf("first GetKey failed: %v", err)
	}

	// Second call with unknown kid — should trigger refresh
	key, err := client.GetKey(server.URL, "key-2")
	if err != nil {
		t.Fatalf("GetKey for key-2 failed: %v", err)
	}

	if key == nil {
		t.Fatal("expected non-nil key for key-2")
	}

	if count := fetchCount.Load(); count != 2 {
		t.Errorf("expected 2 HTTP fetches, got %d", count)
	}
}

func TestJWKS_RateLimitedRefresh(t *testing.T) {
	_, jwksData := generateTestRSAKey("only-key")

	var fetchCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwksData)
	}))
	defer server.Close()

	client := NewJWKSClient(
		WithCacheTTL(1*time.Hour),
		WithMinRefreshDelay(1*time.Minute),
	)

	// First fetch
	_, err := client.GetKey(server.URL, "only-key")
	if err != nil {
		t.Fatalf("first GetKey failed: %v", err)
	}

	// Try to get unknown kid — should be rate limited after first refresh
	_, err = client.GetKey(server.URL, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}

	// Should only have done 1 fetch (initial fetch counts, second is rate limited)
	// Actually the initial fetch sets lastRefresh, so the second attempt is rate-limited
	if count := fetchCount.Load(); count != 1 {
		t.Errorf("expected 1 HTTP fetch (rate limited), got %d", count)
	}
}

func TestJWKS_UnreachableURL(t *testing.T) {
	client := NewJWKSClient(
		WithHTTPClient(&http.Client{Timeout: 100 * time.Millisecond}),
	)

	_, err := client.GetKey("http://127.0.0.1:1/jwks", "any-kid")
	if err == nil {
		t.Fatal("expected error for unreachable URL")
	}
}

func TestJWKS_MalformedJWKS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"keys": "not an array"}`))
	}))
	defer server.Close()

	client := NewJWKSClient()

	_, err := client.GetKey(server.URL, "any-kid")
	if err == nil {
		t.Fatal("expected error for malformed JWKS")
	}
}

func TestJWKS_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json at all`))
	}))
	defer server.Close()

	client := NewJWKSClient()

	_, err := client.GetKey(server.URL, "any-kid")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestJWKS_MultipleIssuers(t *testing.T) {
	_, jwksData1 := generateTestRSAKey("issuer1-key")
	_, jwksData2 := generateTestRSAKey("issuer2-key")

	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwksData1)
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwksData2)
	}))
	defer server2.Close()

	client := NewJWKSClient()

	key1, err := client.GetKey(server1.URL, "issuer1-key")
	if err != nil {
		t.Fatalf("GetKey from issuer1 failed: %v", err)
	}
	if key1 == nil {
		t.Fatal("expected non-nil key from issuer1")
	}

	key2, err := client.GetKey(server2.URL, "issuer2-key")
	if err != nil {
		t.Fatalf("GetKey from issuer2 failed: %v", err)
	}
	if key2 == nil {
		t.Fatal("expected non-nil key from issuer2")
	}

	// Ensure they are different keys
	rsaKey1 := key1.(*rsa.PublicKey)
	rsaKey2 := key2.(*rsa.PublicKey)
	if rsaKey1.N.Cmp(rsaKey2.N) == 0 {
		t.Error("expected different keys from different issuers")
	}
}

func TestJWKS_HTTPErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "internal error")
	}))
	defer server.Close()

	client := NewJWKSClient()

	_, err := client.GetKey(server.URL, "any-kid")
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}
