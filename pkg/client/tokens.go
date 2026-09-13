package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
)

const tokenFileName = "remote_tokens.enc"

const (
	TokenKindPlatform = "platform"
	TokenKindOAuth    = "oauth"
)

// Tokens holds the JWT access and refresh tokens for the remote connection.
type Tokens struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	Kind             string    `json:"kind,omitempty"`
	ClientID         string    `json:"client_id,omitempty"`
}

func (t *Tokens) isOAuth() bool {
	return t != nil && t.Kind == TokenKindOAuth
}

// IsAccessExpired returns true if the access token has expired or will expire
// within the next 30 seconds (to avoid edge-case failures).
func (t *Tokens) IsAccessExpired() bool {
	return time.Now().Add(30 * time.Second).After(t.AccessExpiresAt)
}

// IsRefreshExpired returns true if the refresh token has expired.
func (t *Tokens) IsRefreshExpired() bool {
	return time.Now().After(t.RefreshExpiresAt)
}

// TokenStore manages encrypted storage of JWT tokens on disk.
type TokenStore struct {
	path string
	key  []byte
}

// NewTokenStore creates a token store using the shared .store_key for encryption.
func NewTokenStore() (*TokenStore, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return nil, err
	}

	key, err := loadStoreKey(dir)
	if err != nil {
		return nil, fmt.Errorf("load encryption key: %w", err)
	}

	return &TokenStore{
		path: filepath.Join(dir, tokenFileName),
		key:  key,
	}, nil
}

// Load reads and decrypts the stored tokens.
// Returns nil, nil if no token file exists.
func (ts *TokenStore) Load() (*Tokens, error) {
	data, err := os.ReadFile(ts.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read token file: %w", err)
	}

	plaintext, err := credentials.Decrypt(data, ts.key)
	if err != nil {
		return nil, fmt.Errorf("decrypt tokens: %w", err)
	}

	var tokens Tokens
	if err := json.Unmarshal(plaintext, &tokens); err != nil {
		return nil, fmt.Errorf("parse tokens: %w", err)
	}
	return &tokens, nil
}

// Save encrypts and writes tokens to disk.
func (ts *TokenStore) Save(tokens *Tokens) error {
	plaintext, err := json.Marshal(tokens)
	if err != nil {
		return fmt.Errorf("marshal tokens: %w", err)
	}

	ciphertext, err := credentials.Encrypt(plaintext, ts.key)
	if err != nil {
		return fmt.Errorf("encrypt tokens: %w", err)
	}

	// Atomic write: write to temp file, then rename
	tmpPath := ts.path + ".tmp"
	if err := os.WriteFile(tmpPath, ciphertext, 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	return os.Rename(tmpPath, ts.path)
}

// Remove deletes the token file from disk.
func (ts *TokenStore) Remove() error {
	err := os.Remove(ts.path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove token file: %w", err)
	}
	return nil
}

// loadStoreKey loads the existing .store_key (shared with credential store).
// Unlike the credential store, we do NOT auto-create a key here — if no key
// exists, the user hasn't set up any credentials yet, and login will create one.
func loadStoreKey(configDir string) ([]byte, error) {
	keyPath := filepath.Join(configDir, ".store_key")

	data, err := os.ReadFile(keyPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No key exists yet — create one for the token store
			key, genErr := credentials.GenerateKey()
			if genErr != nil {
				return nil, genErr
			}
			if mkErr := os.MkdirAll(configDir, 0755); mkErr != nil {
				return nil, mkErr
			}
			hexKey := fmt.Sprintf("%x\n", key)
			if wErr := os.WriteFile(keyPath, []byte(hexKey), 0600); wErr != nil {
				return nil, wErr
			}
			return key, nil
		}
		return nil, err
	}

	// Parse key (same logic as credentials package)
	key, err := parseHexKey(data)
	if err != nil {
		return nil, err
	}
	return key, nil
}

// parseHexKey parses a hex-encoded key file (64 hex chars + optional newline).
func parseHexKey(data []byte) ([]byte, error) {
	trimmed := string(data)
	// Trim trailing whitespace/newline
	for len(trimmed) > 0 && (trimmed[len(trimmed)-1] == '\n' || trimmed[len(trimmed)-1] == '\r' || trimmed[len(trimmed)-1] == ' ') {
		trimmed = trimmed[:len(trimmed)-1]
	}

	if len(trimmed) != 64 {
		// Try raw binary (legacy)
		if len(data) == 32 {
			return data, nil
		}
		return nil, fmt.Errorf("invalid key file: expected 64 hex chars, got %d chars", len(trimmed))
	}

	key := make([]byte, 32)
	for i := 0; i < 32; i++ {
		_, err := fmt.Sscanf(trimmed[i*2:i*2+2], "%02x", &key[i])
		if err != nil {
			return nil, fmt.Errorf("invalid hex in key file at position %d: %w", i*2, err)
		}
	}
	return key, nil
}
