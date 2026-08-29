package entstore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	platforment "github.com/SAP/astonish/ent/platform"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
)

const (
	// platformSettingsKey is the key used to store platform-wide settings.
	platformSettingsKey = "providers"

	// orgSettingsKeyPrefix is the prefix for org-level settings keys.
	orgSettingsKeyPrefix = "org_settings:"
)

// --- PlatformSettingsStore ---

// platformSettingsStore implements store.PlatformSettingsStore using the Ent platform client.
type platformSettingsStore struct {
	client *platforment.Client
}

func (s *Store) PlatformSettings() store.PlatformSettingsStore {
	return &platformSettingsStore{client: s.platformClient}
}

func (ps *platformSettingsStore) Get(ctx context.Context) (*store.PlatformSettings, error) {
	row, err := ps.client.PlatformSetting.Get(ctx, platformSettingsKey)
	if err != nil {
		if platforment.IsNotFound(err) {
			return &store.PlatformSettings{}, nil
		}
		return nil, fmt.Errorf("get platform settings: %w", err)
	}

	// The Value field is map[string]interface{} — re-marshal and unmarshal into typed struct.
	data, err := json.Marshal(row.Value)
	if err != nil {
		return nil, fmt.Errorf("marshal platform settings value: %w", err)
	}

	var settings store.PlatformSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("unmarshal platform settings: %w", err)
	}

	// Inject secrets from platform_secrets into provider configs.
	secretStore := &platformSecretStore{client: ps.client}
	getSecret := secretStore.getter()
	injectProviderSecrets(settings.Providers, "platform", getSecret)

	return &settings, nil
}

func (ps *platformSettingsStore) Save(ctx context.Context, settings *store.PlatformSettings) error {
	// Marshal settings to JSON, then unmarshal into map for storage.
	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal platform settings: %w", err)
	}

	var valueMap map[string]interface{}
	if err := json.Unmarshal(data, &valueMap); err != nil {
		return fmt.Errorf("unmarshal platform settings to map: %w", err)
	}

	// Upsert: try to update, create if not found.
	row, err := ps.client.PlatformSetting.Get(ctx, platformSettingsKey)
	if err != nil {
		if platforment.IsNotFound(err) {
			// Create new row.
			_, err = ps.client.PlatformSetting.Create().
				SetID(platformSettingsKey).
				SetValue(valueMap).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("create platform settings: %w", err)
			}
			return nil
		}
		return fmt.Errorf("get platform settings for upsert: %w", err)
	}

	// Update existing row.
	_, err = row.Update().
		SetValue(valueMap).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("update platform settings: %w", err)
	}
	return nil
}

// Compile-time assertion.
var _ store.PlatformSettingsStore = (*platformSettingsStore)(nil)

// --- OrgSettingsStore ---

// orgSettingsStore implements store.OrgSettingsStore using the Ent platform client.
type orgSettingsStore struct {
	client  *platforment.Client
	orgSlug string
}

func (s *Store) OrgSettings(orgSlug string) store.OrgSettingsStore {
	return &orgSettingsStore{client: s.platformClient, orgSlug: orgSlug}
}

func (os *orgSettingsStore) storageKey() string {
	return orgSettingsKeyPrefix + os.orgSlug
}

func (os *orgSettingsStore) Get(ctx context.Context) (*store.OrgSettings, error) {
	key := os.storageKey()
	row, err := os.client.PlatformSetting.Get(ctx, key)
	if err != nil {
		if platforment.IsNotFound(err) {
			return &store.OrgSettings{}, nil
		}
		return nil, fmt.Errorf("get org settings: %w", err)
	}

	data, err := json.Marshal(row.Value)
	if err != nil {
		return nil, fmt.Errorf("marshal org settings value: %w", err)
	}

	var settings store.OrgSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("unmarshal org settings: %w", err)
	}
	return &settings, nil
}

func (os *orgSettingsStore) Save(ctx context.Context, settings *store.OrgSettings) error {
	key := os.storageKey()

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal org settings: %w", err)
	}

	var valueMap map[string]interface{}
	if err := json.Unmarshal(data, &valueMap); err != nil {
		return fmt.Errorf("unmarshal org settings to map: %w", err)
	}

	// Upsert: try to update, create if not found.
	row, err := os.client.PlatformSetting.Get(ctx, key)
	if err != nil {
		if platforment.IsNotFound(err) {
			_, err = os.client.PlatformSetting.Create().
				SetID(key).
				SetValue(valueMap).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("create org settings: %w", err)
			}
			return nil
		}
		return fmt.Errorf("get org settings for upsert: %w", err)
	}

	_, err = row.Update().
		SetValue(valueMap).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("update org settings: %w", err)
	}
	return nil
}

// Compile-time assertion.
var _ store.OrgSettingsStore = (*orgSettingsStore)(nil)

// --- platformSecretStore (SecretGetter) ---

// platformSecretStore provides secret lookup from the platform_secrets table.
type platformSecretStore struct {
	client *platforment.Client
}

func (s *Store) SecretGetter() func(string) string {
	ps := &platformSecretStore{client: s.platformClient}
	return ps.getter()
}

// getter returns a closure that queries the platform_secrets table live on
// each call. This ensures callers always see the current DB state — critical
// for IsStandardServerInstalled() which must reflect secrets written by the
// install handler without requiring a daemon restart.
func (ps *platformSecretStore) getter() func(string) string {
	masterKey := loadMasterKey()

	return func(key string) string {
		row, err := ps.client.PlatformSecret.Get(context.Background(), key)
		if err != nil {
			return ""
		}
		plaintext, err := decryptSecret(row.Value, masterKey)
		if err != nil {
			slog.Warn("failed to decrypt platform secret", "key", key, "error", err)
			return ""
		}
		return string(plaintext)
	}
}

// --- PlatformSecretsStore (context-free interface for API layer) ---

// PlatformSecretsStore provides context-free GetSecret/SetSecret/RemoveSecret.
// Used by api.SetPlatformSecrets.
type PlatformSecretsStore struct {
	client *platforment.Client
}

// Secrets returns a PlatformSecretsStore for the platform secrets table.
func (s *Store) Secrets() *PlatformSecretsStore {
	return &PlatformSecretsStore{client: s.platformClient}
}

// GetSecret returns the decrypted secret value for the given key, or "" if not found.
func (ps *PlatformSecretsStore) GetSecret(key string) string {
	ctx := context.Background()
	row, err := ps.client.PlatformSecret.Get(ctx, key)
	if err != nil {
		return ""
	}
	masterKey := loadMasterKey()
	plaintext, err := decryptSecret(row.Value, masterKey)
	if err != nil {
		slog.Warn("failed to decrypt platform secret", "key", key, "error", err)
		return ""
	}
	return string(plaintext)
}

// SetSecret stores a secret value. Creates or updates the entry.
func (ps *PlatformSecretsStore) SetSecret(key, value string) error {
	ctx := context.Background()
	masterKey := loadMasterKey()
	encrypted, err := encryptSecret([]byte(value), masterKey)
	if err != nil {
		return fmt.Errorf("encrypt secret: %w", err)
	}

	// Check if it exists first.
	existing, err := ps.client.PlatformSecret.Get(ctx, key)
	if err != nil && !platforment.IsNotFound(err) {
		return err
	}
	if existing != nil {
		// Update existing.
		return ps.client.PlatformSecret.UpdateOneID(key).
			SetValue(encrypted).
			Exec(ctx)
	}
	// Create new.
	_, err = ps.client.PlatformSecret.Create().
		SetID(key).
		SetValue(encrypted).
		Save(ctx)
	return err
}

// RemoveSecret deletes a secret by key.
func (ps *PlatformSecretsStore) RemoveSecret(key string) error {
	ctx := context.Background()
	return ps.client.PlatformSecret.DeleteOneID(key).Exec(ctx)
}

// --- Encryption helpers ---

// loadMasterKey reads the master key from env or .store_key file.
// Returns nil if no valid key is found (encryption disabled — values stored/read as-is).
func loadMasterKey() []byte {
	// Priority 1: environment variable
	masterKeyHex := os.Getenv("ASTONISH_MASTER_KEY")

	// Priority 2: .store_key file
	if masterKeyHex == "" {
		if configDir, err := config.GetConfigDir(); err == nil {
			keyPath := filepath.Join(configDir, ".store_key")
			if data, err := os.ReadFile(keyPath); err == nil {
				masterKeyHex = strings.TrimSpace(string(data))
			}
		}
	}

	if masterKeyHex == "" {
		return nil
	}

	key, err := hex.DecodeString(masterKeyHex)
	if err != nil || len(key) != 32 {
		slog.Warn("master key is invalid, platform secret encryption disabled")
		return nil
	}
	return key
}

// decryptSecret decrypts a secret value using the master key.
// If no master key is configured, returns the raw bytes (unencrypted mode).
func decryptSecret(data, masterKey []byte) ([]byte, error) {
	if len(masterKey) == 0 {
		return data, nil
	}
	plaintext, err := credentials.Decrypt(data, masterKey)
	if err != nil {
		// Fallback: data might be unencrypted (stored before encryption was enabled).
		if isPlainText(data) {
			return data, nil
		}
		return nil, err
	}
	return plaintext, nil
}

// encryptSecret encrypts a secret value using the master key.
// If no master key is configured, returns the raw bytes (unencrypted mode).
func encryptSecret(plaintext, masterKey []byte) ([]byte, error) {
	if len(masterKey) == 0 {
		return plaintext, nil
	}
	return credentials.Encrypt(plaintext, masterKey)
}

// isPlainText checks if data looks like unencrypted text (ASCII/UTF-8 printable).
func isPlainText(data []byte) bool {
	for _, b := range data {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			return false
		}
	}
	return len(data) > 0
}

// decryptCredentialData decrypts credential data using the per-org DEK (envelope
// encryption). Fallback order: DEK → master key → plaintext.
// This supports credentials encrypted by the old pgstore (which used DEK),
// any that might have been encrypted directly with master key, and legacy
// plaintext data.
func decryptCredentialData(data, credKey []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}

	// Try DEK first (correct envelope encryption path).
	if len(credKey) > 0 {
		plaintext, err := credentials.Decrypt(data, credKey)
		if err == nil {
			return plaintext, nil
		}
	}

	// Fallback: try master key directly (in case data was encrypted that way).
	masterKey := loadMasterKey()
	if len(masterKey) > 0 {
		plaintext, err := credentials.Decrypt(data, masterKey)
		if err == nil {
			return plaintext, nil
		}
	}

	// Fallback: data might be unencrypted (legacy plaintext).
	if isPlainText(data) {
		return data, nil
	}

	return nil, fmt.Errorf("decrypt: unable to decrypt credential data (tried DEK, master key, plaintext)")
}

// encryptCredentialData encrypts credential data using the per-org DEK.
// Falls back to master key if DEK is not available.
// If neither key is available, returns plaintext (unencrypted mode).
func encryptCredentialData(plaintext, credKey []byte) ([]byte, error) {
	if len(credKey) > 0 {
		return credentials.Encrypt(plaintext, credKey)
	}
	// Fallback to master key if no DEK available.
	masterKey := loadMasterKey()
	if len(masterKey) > 0 {
		return credentials.Encrypt(plaintext, masterKey)
	}
	return plaintext, nil
}

// --- Provider secret injection ---

// injectProviderSecrets re-injects secret values from the encrypted store
// into provider configs for runtime use.
func injectProviderSecrets(providers map[string]store.ProviderConfig, level string, getSecret func(string) string) {
	for instanceName, pCfg := range providers {
		for _, key := range providerSecretFields(pCfg) {
			storeKey := "provider." + level + "." + instanceName + "." + key
			if val := getSecret(storeKey); val != "" {
				if pCfg == nil {
					pCfg = make(store.ProviderConfig)
					providers[instanceName] = pCfg
				}
				pCfg[key] = val
			}
		}
	}
}

// providerSecretFields returns the field names that should be treated as secrets
// for a given provider config. Based on provider type.
func providerSecretFields(pCfg store.ProviderConfig) []string {
	switch pCfg["type"] {
	case "sap_ai_core":
		return []string{"client_id", "client_secret", "auth_url"}
	case "xai_oauth":
		return []string{"access_token", "refresh_token", "expires_at"}
	default:
		return []string{"api_key"}
	}
}
