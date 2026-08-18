package a2a

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// JWKSClient fetches and caches JWKS (JSON Web Key Sets) from external IdP endpoints.
// It supports multiple issuers and provides automatic cache refresh with rate limiting.
type JWKSClient struct {
	mu              sync.RWMutex
	httpClient      *http.Client
	cacheTTL        time.Duration
	minRefreshDelay time.Duration
	entries         map[string]*jwksEntry // URL → cached entry
}

// jwksEntry holds the cached keyset for a single JWKS URL.
type jwksEntry struct {
	keys        map[string]crypto.PublicKey // kid → public key
	fetchedAt   time.Time
	lastRefresh time.Time
}

// jwksResponse represents the JSON response from a JWKS endpoint.
type jwksResponse struct {
	Keys []jwkKey `json:"keys"`
}

// jwkKey represents a single JWK in the JWKS response.
type jwkKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	// RSA fields
	N string `json:"n"`
	E string `json:"e"`
	// EC fields
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// JWKSClientOption configures a JWKSClient.
type JWKSClientOption func(*JWKSClient)

// WithCacheTTL sets the cache time-to-live for fetched keysets.
func WithCacheTTL(ttl time.Duration) JWKSClientOption {
	return func(c *JWKSClient) {
		c.cacheTTL = ttl
	}
}

// WithHTTPClient sets a custom HTTP client for JWKS fetching.
func WithHTTPClient(client *http.Client) JWKSClientOption {
	return func(c *JWKSClient) {
		c.httpClient = client
	}
}

// WithMinRefreshDelay sets the minimum delay between forced refreshes (rate limiting).
func WithMinRefreshDelay(d time.Duration) JWKSClientOption {
	return func(c *JWKSClient) {
		c.minRefreshDelay = d
	}
}

// NewJWKSClient creates a new JWKS client with the given options.
// Default TTL is 1 hour, default min refresh delay is 1 minute.
func NewJWKSClient(opts ...JWKSClientOption) *JWKSClient {
	c := &JWKSClient{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		cacheTTL:        1 * time.Hour,
		minRefreshDelay: 1 * time.Minute,
		entries:         make(map[string]*jwksEntry),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// GetKey retrieves the public key for the given kid from the JWKS at jwksURL.
// It uses a cached keyset if available and not expired. On cache miss or unknown kid,
// it performs a forced refresh (rate-limited to once per minRefreshDelay).
func (c *JWKSClient) GetKey(jwksURL, kid string) (crypto.PublicKey, error) {
	// Try cache first
	c.mu.RLock()
	entry, exists := c.entries[jwksURL]
	c.mu.RUnlock()

	if exists && time.Since(entry.fetchedAt) < c.cacheTTL {
		if key, ok := entry.keys[kid]; ok {
			return key, nil
		}
		// kid not found in cache — try forced refresh
		return c.refreshAndGet(jwksURL, kid)
	}

	// Cache miss or expired — fetch
	return c.refreshAndGet(jwksURL, kid)
}

// refreshAndGet fetches the JWKS from the URL (rate-limited) and returns the key for kid.
func (c *JWKSClient) refreshAndGet(jwksURL, kid string) (crypto.PublicKey, error) {
	c.mu.Lock()

	// Check if we already have a recent entry (another goroutine may have refreshed)
	entry, exists := c.entries[jwksURL]
	if exists && time.Since(entry.fetchedAt) < c.cacheTTL {
		if key, ok := entry.keys[kid]; ok {
			c.mu.Unlock()
			return key, nil
		}
		// Check rate limit for forced refresh
		if time.Since(entry.lastRefresh) < c.minRefreshDelay {
			c.mu.Unlock()
			return nil, fmt.Errorf("jwks: key %q not found at %s (refresh rate limited)", kid, jwksURL)
		}
	}

	c.mu.Unlock()

	// Fetch JWKS outside the lock
	keys, err := c.fetchJWKS(jwksURL)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	c.mu.Lock()
	c.entries[jwksURL] = &jwksEntry{
		keys:        keys,
		fetchedAt:   now,
		lastRefresh: now,
	}
	c.mu.Unlock()

	key, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("jwks: key %q not found at %s", kid, jwksURL)
	}
	return key, nil
}

// fetchJWKS performs an HTTP GET to the JWKS URL and parses the response.
func (c *JWKSClient) fetchJWKS(jwksURL string) (map[string]crypto.PublicKey, error) {
	resp, err := c.httpClient.Get(jwksURL)
	if err != nil {
		return nil, fmt.Errorf("jwks: failed to fetch %s: %w", jwksURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: unexpected status %d from %s", resp.StatusCode, jwksURL)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("jwks: failed to read response from %s: %w", jwksURL, err)
	}

	var jwks jwksResponse
	if err := json.Unmarshal(body, &jwks); err != nil {
		return nil, fmt.Errorf("jwks: failed to parse JWKS from %s: %w", jwksURL, err)
	}

	keys := make(map[string]crypto.PublicKey)
	for _, k := range jwks.Keys {
		pubKey, err := parseJWK(k)
		if err != nil {
			// Skip keys we can't parse (e.g., unsupported key types)
			continue
		}
		keys[k.Kid] = pubKey
	}

	return keys, nil
}

// parseJWK converts a JWK JSON object to a crypto.PublicKey.
func parseJWK(k jwkKey) (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		return parseRSAJWK(k)
	case "EC":
		return parseECJWK(k)
	default:
		return nil, fmt.Errorf("jwks: unsupported key type %q", k.Kty)
	}
}

// parseRSAJWK parses an RSA JWK into an *rsa.PublicKey.
func parseRSAJWK(k jwkKey) (*rsa.PublicKey, error) {
	if k.N == "" || k.E == "" {
		return nil, fmt.Errorf("jwks: RSA key missing n or e")
	}

	nBytes, err := base64URLDecode(k.N)
	if err != nil {
		return nil, fmt.Errorf("jwks: failed to decode RSA n: %w", err)
	}

	eBytes, err := base64URLDecode(k.E)
	if err != nil {
		return nil, fmt.Errorf("jwks: failed to decode RSA e: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)

	if !e.IsInt64() {
		return nil, fmt.Errorf("jwks: RSA exponent too large")
	}

	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}

// parseECJWK parses an EC JWK into an *ecdsa.PublicKey.
func parseECJWK(k jwkKey) (*ecdsa.PublicKey, error) {
	if k.X == "" || k.Y == "" || k.Crv == "" {
		return nil, fmt.Errorf("jwks: EC key missing x, y, or crv")
	}

	var curve elliptic.Curve
	switch k.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("jwks: unsupported EC curve %q", k.Crv)
	}

	xBytes, err := base64URLDecode(k.X)
	if err != nil {
		return nil, fmt.Errorf("jwks: failed to decode EC x: %w", err)
	}

	yBytes, err := base64URLDecode(k.Y)
	if err != nil {
		return nil, fmt.Errorf("jwks: failed to decode EC y: %w", err)
	}

	x := new(big.Int).SetBytes(xBytes)
	y := new(big.Int).SetBytes(yBytes)

	return &ecdsa.PublicKey{
		Curve: curve,
		X:     x,
		Y:     y,
	}, nil
}

// base64URLDecode decodes a base64url-encoded string (without padding).
func base64URLDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
