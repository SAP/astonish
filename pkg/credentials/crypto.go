// Package credentials provides an encrypted credential store for Astonish.
// Secrets are encrypted at rest with AES-256-GCM. The encryption key is
// auto-generated on first use and stored in a separate key file. A redaction
// engine prevents credential values from leaking through tool outputs,
// channel messages, or session transcripts.
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const (
	// keySize is the AES-256 key length in bytes.
	keySize = 32
	// nonceSize is the GCM nonce length in bytes.
	nonceSize = 12
	// keyFileName is the name of the encryption key file.
	keyFileName = ".store_key"
	// storeFileName is the name of the encrypted credential file.
	storeFileName = "credentials.enc"
)

// generateKey creates a cryptographically random 256-bit key.
func generateKey() ([]byte, error) {
	key := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate random key: %w", err)
	}
	return key, nil
}

// GenerateKey creates a cryptographically random 256-bit (32-byte) AES key.
// This is the exported variant for use by the PG credential store's envelope encryption.
func GenerateKey() ([]byte, error) {
	return generateKey()
}

// encrypt encrypts plaintext using AES-256-GCM with a random nonce.
// Output format: [12-byte nonce][ciphertext+GCM tag]
func encrypt(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// Encrypt encrypts plaintext using AES-256-GCM with a random nonce.
// Output format: [12-byte nonce][ciphertext+GCM tag].
// This is the exported variant for use by the PG credential store.
func Encrypt(plaintext, key []byte) ([]byte, error) {
	return encrypt(plaintext, key)
}

// decrypt decrypts data produced by encrypt.
func decrypt(ciphertext, key []byte) ([]byte, error) {
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonce := ciphertext[:nonceSize]
	data := ciphertext[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

// Decrypt decrypts data produced by Encrypt.
// This is the exported variant for use by the PG credential store.
func Decrypt(ciphertext, key []byte) ([]byte, error) {
	return decrypt(ciphertext, key)
}

// loadOrCreateKey reads the encryption key from disk, or generates a new one.
// If the ASTONISH_MASTER_KEY environment variable is set, it takes precedence
// over the file-based key (designed for containerized deployments where no
// persistent volume is available).
// The key file stores the key as hex-encoded text (64 hex chars = 32 bytes).
// For backward compatibility, raw binary key files (exactly 32 bytes of
// non-hex content) are auto-migrated to hex format on first load.
// The key file is created with 0600 permissions (owner read/write only).
func loadOrCreateKey(configDir string) ([]byte, error) {
	// Priority 1: environment variable (for K8s / stateless containers)
	if envKey := os.Getenv("ASTONISH_MASTER_KEY"); envKey != "" {
		decoded, err := hex.DecodeString(strings.TrimSpace(envKey))
		if err != nil || len(decoded) != keySize {
			return nil, fmt.Errorf("ASTONISH_MASTER_KEY must be %d hex characters (got %d chars)", keySize*2, len(strings.TrimSpace(envKey)))
		}
		return decoded, nil
	}

	// Priority 2: file-based key
	keyPath := filepath.Join(configDir, keyFileName)

	data, err := os.ReadFile(keyPath)
	if err == nil {
		key, migrated, parseErr := parseKeyFile(data)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid key file: %w", parseErr)
		}
		// Auto-migrate raw binary to hex format so the redactor can match it
		if migrated {
			hexKey := hex.EncodeToString(key) + "\n"
			if writeErr := os.WriteFile(keyPath, []byte(hexKey), 0600); writeErr != nil {
				// Non-fatal: key works fine, just can't upgrade the file format
				slog.Debug("failed to upgrade key format to hex", "error", writeErr)
			}
		}
		return key, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read key file: %w", err)
	}

	// Generate new key
	key, err := generateKey()
	if err != nil {
		return nil, err
	}

	// Ensure directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	// Write key file as hex text with trailing newline
	hexKey := hex.EncodeToString(key) + "\n"
	if err := os.WriteFile(keyPath, []byte(hexKey), 0600); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}

	return key, nil
}

// LoadOrCreateMasterKey returns the configured AES-256 master key. It is shared
// by platform-owned encrypted records such as OAuth signing keys; callers must
// never log or return it to an API client.
func LoadOrCreateMasterKey(configDir string) ([]byte, error) {
	return loadOrCreateKey(configDir)
}

// parseKeyFile interprets key file data in either hex-text or raw-binary format.
// Returns the decoded key, whether a migration from binary occurred, and any error.
func parseKeyFile(data []byte) (key []byte, migrated bool, err error) {
	// Try hex-encoded text first (new format): 64 hex chars, optionally with trailing newline
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) == keySize*2 {
		decoded, hexErr := hex.DecodeString(trimmed)
		if hexErr == nil && len(decoded) == keySize {
			return decoded, false, nil
		}
	}

	// Fall back to raw binary (legacy format): exactly 32 bytes
	if len(data) == keySize {
		return data, true, nil
	}

	return nil, false, fmt.Errorf("expected %d hex chars or %d raw bytes, got %d bytes", keySize*2, keySize, len(data))
}

// storeFilePath returns the full path to the encrypted store file.
func storeFilePath(configDir string) string {
	return filepath.Join(configDir, storeFileName)
}
