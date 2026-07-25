package credentials

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// CredentialType defines the authentication mechanism.
type CredentialType string

const (
	// CredAPIKey uses a custom header with a static value.
	// Example: X-API-Key: my-secret-key
	CredAPIKey CredentialType = "api_key"

	// CredBearer uses Authorization: Bearer <token>.
	CredBearer CredentialType = "bearer"

	// CredBasic uses Authorization: Basic <base64(user:pass)>.
	CredBasic CredentialType = "basic"

	// CredOAuthClientCreds performs OAuth2 client_credentials flow
	// and produces Authorization: Bearer <access_token>.
	CredOAuthClientCreds CredentialType = "oauth_client_credentials"

	// CredPassword stores a plain username + password pair for non-HTTP use
	// cases: SSH, FTP, SMTP, databases, etc. Unlike basic, this type is not
	// an HTTP credential — Resolve() will return an error. Use Get() or the
	// resolve_credential LLM tool to access the raw fields.
	CredPassword CredentialType = "password"

	// CredOAuthAuthCode stores OAuth2 credentials obtained via the
	// authorization code flow (Google, GitHub, etc.). Unlike client_credentials,
	// this flow requires user consent and produces a refresh_token that can
	// acquire new access tokens without user interaction.
	// Resolve() auto-refreshes expired tokens and persists updated tokens to disk.
	CredOAuthAuthCode CredentialType = "oauth_authorization_code"

	// CredOpenStackKeystone performs OpenStack Keystone v3 token auth
	// (password or application_credential) and produces X-Auth-Token: <token>.
	// Resolve() auto-fetches and caches tokens until near expiry.
	CredOpenStackKeystone CredentialType = "openstack_keystone"

	// CredRawContent stores arbitrary encrypted file content for sandbox
	// materialization. It is not an HTTP credential; use the content field via
	// resolve_credential or fleet credential_injection files.
	CredRawContent CredentialType = "raw_content"
)

// Credential holds authentication data for a single named credential.
type Credential struct {
	Type CredentialType `json:"type"`

	// api_key fields
	Header string `json:"header,omitempty"` // e.g., "X-API-Key", "Authorization"
	Value  string `json:"value,omitempty"`  // the raw key/token value

	// bearer fields
	Token string `json:"token,omitempty"`

	// basic fields
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	// oauth_client_credentials fields
	AuthURL      string `json:"auth_url,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	Scope        string `json:"scope,omitempty"`

	// oauth_authorization_code fields (also uses ClientID, ClientSecret, Scope above)
	TokenURL     string `json:"token_url,omitempty"`     // Token endpoint (e.g., https://oauth2.googleapis.com/token)
	AccessToken  string `json:"access_token,omitempty"`  // Current access token
	RefreshToken string `json:"refresh_token,omitempty"` // Long-lived refresh token
	TokenExpiry  string `json:"token_expiry,omitempty"`  // RFC3339 timestamp of access token expiry

	// openstack_keystone fields (also uses AuthURL, Username, Password above)
	UserDomain                  string `json:"user_domain,omitempty"`
	ProjectID                   string `json:"project_id,omitempty"`
	ProjectName                 string `json:"project_name,omitempty"`
	ProjectDomain               string `json:"project_domain,omitempty"`
	ApplicationCredentialID     string `json:"application_credential_id,omitempty"`
	ApplicationCredentialSecret string `json:"application_credential_secret,omitempty"`

	// raw_content fields
	Content     string `json:"content,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// storeData is the JSON structure inside the encrypted file.
type storeData struct {
	Credentials   map[string]*Credential `json:"credentials"`
	Secrets       map[string]string      `json:"secrets,omitempty"`         // flat key-value store (provider keys, tokens, etc.)
	Migrated      bool                   `json:"migrated,omitempty"`        // true after config.yaml secrets have been migrated
	MasterKeyHash string                 `json:"master_key_hash,omitempty"` // argon2id hash (base64)
	MasterKeySalt string                 `json:"master_key_salt,omitempty"` // random 16-byte salt (base64)
}

// Store manages encrypted credential persistence with integrated redaction.
type Store struct {
	configDir string
	key       []byte
	mu        sync.RWMutex
	data      *storeData
	redactor  *Redactor
	tokens    *tokenCache
}

// Open loads (or creates) the credential store for the given config directory.
// The encryption key is loaded from .store_key (auto-generated on first use).
// The encrypted credentials are loaded from credentials.enc.
func Open(configDir string) (*Store, error) {
	key, err := loadOrCreateKey(configDir)
	if err != nil {
		return nil, fmt.Errorf("credential store key: %w", err)
	}

	s := &Store{
		configDir: configDir,
		key:       key,
		data: &storeData{
			Credentials: make(map[string]*Credential),
			Secrets:     make(map[string]string),
		},
		redactor: NewRedactor(),
		tokens:   newTokenCache(),
	}

	// Register the encryption key itself for redaction.
	// Both raw bytes (if somehow cat'd) and hex-encoded form.
	hexKey := hex.EncodeToString(key)
	s.redactor.AddSecret(storeKeyRedactName, hexKey)
	s.redactor.AddSecret(storeKeyRedactName, string(key))

	// Load existing store
	if err := s.load(); err != nil {
		return nil, err
	}

	// Sanitize any existing credentials that may have copy-paste artifacts
	// (e.g., trailing quotes/commas from JSON). This is idempotent — clean
	// values pass through unchanged. Only saves if something actually changed.
	if s.sanitizeExisting() {
		_ = s.save() // best-effort; don't fail Open() over this
	}

	// Build redaction signatures from loaded credentials
	s.redactor.UpdateFromCredentials(s.data.Credentials)

	// Register flat secrets for redaction
	for key, val := range s.data.Secrets {
		s.redactor.AddSecret("secret/"+key, val)
	}

	return s, nil
}

// Reload re-reads the credential store from disk, picking up changes made by
// other processes (e.g., the CLI running "astonish credential add" while the
// daemon is running). The in-memory state and redaction signatures are fully
// replaced. This is safe to call concurrently with other Store methods.
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.load(); err != nil {
		return fmt.Errorf("reload credential store: %w", err)
	}

	// Rebuild redaction signatures from refreshed data
	s.redactor.UpdateFromCredentials(s.data.Credentials)
	for key, val := range s.data.Secrets {
		s.redactor.AddSecret("secret/"+key, val)
	}

	return nil
}

// Get returns a copy of the named credential, or nil if not found.
func (s *Store) Get(name string) *Credential {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cred, ok := s.data.Credentials[name]
	if !ok {
		return nil
	}
	cp := *cred
	return &cp
}

// sanitizeCredential strips common copy-paste artifacts from credential fields.
// Users often paste values from JSON, YAML, or terminal output that include
// trailing quotes, commas, semicolons, or surrounding whitespace.
func sanitizeCredential(cred *Credential) {
	cred.Header = cleanFieldValue(cred.Header)
	cred.Value = cleanFieldValue(cred.Value)
	cred.Token = cleanFieldValue(cred.Token)
	cred.Username = cleanFieldValue(cred.Username)
	cred.Password = cleanFieldValue(cred.Password)
	cred.AuthURL = cleanFieldValue(cred.AuthURL)
	cred.ClientID = cleanFieldValue(cred.ClientID)
	cred.ClientSecret = cleanFieldValue(cred.ClientSecret)
	cred.Scope = cleanFieldValue(cred.Scope)
	cred.TokenURL = cleanFieldValue(cred.TokenURL)
	cred.AccessToken = cleanFieldValue(cred.AccessToken)
	cred.RefreshToken = cleanFieldValue(cred.RefreshToken)
	cred.ContentType = cleanFieldValue(cred.ContentType)
	// Note: TokenExpiry is an RFC3339 timestamp, skip sanitization.
	// Note: Content is arbitrary file payload and must remain byte-for-byte intact.
}

// sanitizeSecretValue strips common copy-paste artifacts from a secret value.
func sanitizeSecretValue(s string) string {
	return cleanFieldValue(s)
}

// cleanFieldValue removes copy-paste artifacts: surrounding whitespace,
// trailing commas/semicolons, and matched surrounding quotes.
func cleanFieldValue(s string) string {
	s = strings.TrimSpace(s)
	// Strip trailing JSON/YAML artifacts first (comma, semicolon)
	s = strings.TrimRight(s, ",; \t")
	// Strip matched surrounding quotes: "value" or 'value'
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			s = s[1 : len(s)-1]
		}
	}
	// One more pass for any remaining trailing artifacts after unquoting
	s = strings.TrimRight(s, "\",;' \t")
	return strings.TrimSpace(s)
}

// sanitizeExisting runs sanitization on all stored credentials and secrets.
// Returns true if any value was actually changed (indicating a save is needed).
func (s *Store) sanitizeExisting() bool {
	changed := false
	for _, cred := range s.data.Credentials {
		before := *cred
		sanitizeCredential(cred)
		if *cred != before {
			changed = true
		}
	}
	for key, val := range s.data.Secrets {
		cleaned := sanitizeSecretValue(val)
		if cleaned != val {
			s.data.Secrets[key] = cleaned
			changed = true
		}
	}
	return changed
}

// Set adds or updates a credential and persists the store.
func (s *Store) Set(name string, cred *Credential) error {
	sanitizeCredential(cred)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.Credentials[name] = cred
	if err := s.save(); err != nil {
		return err
	}

	s.redactor.UpdateFromCredentials(s.data.Credentials)
	return nil
}

// Remove deletes a credential and persists the store.
func (s *Store) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.data.Credentials[name]; !ok {
		return fmt.Errorf("credential %q not found", name)
	}

	delete(s.data.Credentials, name)
	if err := s.save(); err != nil {
		return err
	}

	s.redactor.RemoveByName(name)
	s.redactor.UpdateFromCredentials(s.data.Credentials)
	return nil
}

// List returns credential names and their types. No secret values are exposed.
func (s *Store) List() map[string]CredentialType {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]CredentialType, len(s.data.Credentials))
	for name, cred := range s.data.Credentials {
		result[name] = cred.Type
	}
	return result
}

// Count returns the number of stored credentials.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data.Credentials)
}

// Resolve produces the HTTP header key and value for a named credential.
// For OAuth credentials, it handles token acquisition and caching automatically.
// The resolved credential values are never exposed to the LLM — they are
// injected directly into HTTP requests by the http_request tool.
func (s *Store) Resolve(name string) (headerKey, headerValue string, err error) {
	s.mu.RLock()
	cred, ok := s.data.Credentials[name]
	if !ok {
		s.mu.RUnlock()
		return "", "", fmt.Errorf("credential %q not found", name)
	}
	// Copy to avoid holding the lock during OAuth calls
	credCopy := *cred
	s.mu.RUnlock()

	switch credCopy.Type {
	case CredAPIKey:
		if credCopy.Header == "" {
			return "", "", fmt.Errorf("credential %q: api_key type requires a header name", name)
		}
		return credCopy.Header, credCopy.Value, nil

	case CredBearer:
		return "Authorization", "Bearer " + credCopy.Token, nil

	case CredBasic:
		encoded := BasicAuthValue(credCopy.Username, credCopy.Password)
		return "Authorization", "Basic " + encoded, nil

	case CredOAuthClientCreds:
		token, err := s.tokens.GetOrRefresh(name, &credCopy, s.redactor)
		if err != nil {
			return "", "", fmt.Errorf("credential %q OAuth: %w", name, err)
		}
		return "Authorization", "Bearer " + token, nil

	case CredOpenStackKeystone:
		token, err := s.tokens.GetOrRefreshKeystone(name, &credCopy, s.redactor)
		if err != nil {
			return "", "", fmt.Errorf("credential %q Keystone: %w", name, err)
		}
		return "X-Auth-Token", token, nil

	case CredPassword:
		return "", "", fmt.Errorf("credential %q is a password credential (for SSH/FTP/etc.), not an HTTP credential — use resolve_credential to access its fields", name)

	case CredRawContent:
		return "", "", fmt.Errorf("credential %q is raw content for file materialization, not an HTTP credential — use resolve_credential to access its content field", name)

	case CredOAuthAuthCode:
		token, err := s.resolveAuthCode(name, &credCopy)
		if err != nil {
			return "", "", fmt.Errorf("credential %q OAuth: %w", name, err)
		}
		return "Authorization", "Bearer " + token, nil

	default:
		return "", "", fmt.Errorf("credential %q: unknown type %q", name, credCopy.Type)
	}
}

// InvalidateToken evicts the cached OAuth token for the named credential,
// forcing the next Resolve call to fetch a fresh token from the OAuth provider.
// This is called when a downstream API returns 401, indicating the token is no
// longer valid despite not having reached its expected expiry time.
func (s *Store) InvalidateToken(name string) {
	s.tokens.Invalidate(name)
}

// Redactor returns the redaction engine for wiring into tool outputs,
// channel delivery, and session storage.
func (s *Store) Redactor() *Redactor {
	return s.redactor
}

// --- Flat secret storage (for provider keys, bot tokens, etc.) ---

// SetSecret stores a single key-value secret in the encrypted store.
// The key uses dot notation (e.g., "provider.anthropic.api_key").
// The value is automatically registered for redaction.
func (s *Store) SetSecret(key, value string) error {
	value = sanitizeSecretValue(value)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.Secrets[key] = value
	if err := s.save(); err != nil {
		return err
	}
	s.redactor.AddSecret("secret/"+key, value)
	return nil
}

// SetSecretBatch stores multiple key-value secrets in a single encrypted write.
// More efficient than calling SetSecret in a loop.
func (s *Store) SetSecretBatch(secrets map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, value := range secrets {
		s.data.Secrets[key] = sanitizeSecretValue(value)
	}
	if err := s.save(); err != nil {
		return err
	}
	for key, value := range secrets {
		s.redactor.AddSecret("secret/"+key, value)
	}
	return nil
}

// GetSecret retrieves a flat secret value by key.
// Returns empty string if not found.
func (s *Store) GetSecret(key string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data == nil || s.data.Secrets == nil {
		return ""
	}
	return s.data.Secrets[key]
}

// RemoveSecret deletes a flat secret and persists the store.
func (s *Store) RemoveSecret(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.data.Secrets[key]; !ok {
		return nil // not an error, just a no-op
	}
	delete(s.data.Secrets, key)
	if err := s.save(); err != nil {
		return err
	}
	s.redactor.RemoveByName("secret/" + key)
	return nil
}

// HasSecrets returns true if any flat secrets are stored.
func (s *Store) HasSecrets() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data.Secrets) > 0
}

// SecretCount returns the number of flat secrets stored.
func (s *Store) SecretCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data.Secrets)
}

// ListSecrets returns the keys of all flat secrets (no values).
// Keys use dot notation, e.g. "provider.anthropic.api_key".
func (s *Store) ListSecrets() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := make([]string, 0, len(s.data.Secrets))
	for key := range s.data.Secrets {
		keys = append(keys, key)
	}
	return keys
}

// --- Migration tracking ---

// HasMigrated returns true if config.yaml secrets have been migrated
// to the credential store. Once true, migration is skipped on future startups.
func (s *Store) HasMigrated() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Migrated
}

// SetMigrated marks the credential store as having completed config migration.
func (s *Store) SetMigrated() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.Migrated = true
	return s.save()
}

// --- Master key for gating human-facing credential reveals ---

// argon2id parameters (standard recommendations for interactive use).
const (
	argon2Time    = 1
	argon2Memory  = 64 * 1024 // 64 MB
	argon2Threads = 4
	argon2KeyLen  = 32
	masterSaltLen = 16
)

// SetMasterKey sets (or changes) the master key used to gate credential reveals.
// Pass an empty password to remove the master key.
func (s *Store) SetMasterKey(password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if password == "" {
		s.data.MasterKeyHash = ""
		s.data.MasterKeySalt = ""
		return s.save()
	}

	salt := make([]byte, masterSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	s.data.MasterKeyHash = base64.StdEncoding.EncodeToString(hash)
	s.data.MasterKeySalt = base64.StdEncoding.EncodeToString(salt)
	return s.save()
}

// HasMasterKey returns true if a master key has been configured.
func (s *Store) HasMasterKey() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.MasterKeyHash != "" && s.data.MasterKeySalt != ""
}

// VerifyMasterKey checks a password against the stored master key hash.
// Returns false if no master key is set or if the password does not match.
func (s *Store) VerifyMasterKey(password string) bool {
	s.mu.RLock()
	storedHash := s.data.MasterKeyHash
	storedSalt := s.data.MasterKeySalt
	s.mu.RUnlock()

	if storedHash == "" || storedSalt == "" {
		return false
	}

	salt, err := base64.StdEncoding.DecodeString(storedSalt)
	if err != nil {
		return false
	}
	expected, err := base64.StdEncoding.DecodeString(storedHash)
	if err != nil {
		return false
	}

	hash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return subtle.ConstantTimeCompare(hash, expected) == 1
}

// load reads and decrypts the credential store from disk.
func (s *Store) load() error {
	path := storeFilePath(s.configDir)

	ciphertext, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // Empty store, first run
	}
	if err != nil {
		return fmt.Errorf("read credential store: %w", err)
	}
	if len(ciphertext) == 0 {
		return nil
	}

	plaintext, err := decrypt(ciphertext, s.key)
	if err != nil {
		return fmt.Errorf("decrypt credential store: %w", err)
	}

	var data storeData
	if err := json.Unmarshal(plaintext, &data); err != nil {
		return fmt.Errorf("parse credential store: %w", err)
	}
	if data.Credentials == nil {
		data.Credentials = make(map[string]*Credential)
	}
	if data.Secrets == nil {
		data.Secrets = make(map[string]string)
	}

	s.data = &data
	return nil
}

// save encrypts and writes the credential store to disk atomically.
func (s *Store) save() error {
	plaintext, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credential store: %w", err)
	}

	ciphertext, err := encrypt(plaintext, s.key)
	if err != nil {
		return fmt.Errorf("encrypt credential store: %w", err)
	}

	path := storeFilePath(s.configDir)
	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, ciphertext, 0600); err != nil {
		return fmt.Errorf("write temp credential store: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("rename credential store: %w", err)
	}

	return nil
}

// BasicAuthValue encodes username:password as base64 for HTTP Basic auth.
func BasicAuthValue(username, password string) string {
	auth := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(auth))
}

// resolveAuthCode returns a valid access token for an oauth_authorization_code
// credential. If the token is expired, it refreshes using the refresh_token
// and persists the updated tokens to disk.
func (s *Store) resolveAuthCode(name string, cred *Credential) (string, error) {
	// Check in-memory cache first
	s.tokens.mu.RLock()
	if cached, ok := s.tokens.tokens[name]; ok {
		if time.Now().Before(cached.expiresAt) {
			s.tokens.mu.RUnlock()
			return cached.accessToken, nil
		}
	}
	s.tokens.mu.RUnlock()

	// Check if stored token is still valid
	if cred.AccessToken != "" && cred.TokenExpiry != "" {
		if expiry, err := time.Parse(time.RFC3339, cred.TokenExpiry); err == nil {
			if time.Now().Before(expiry.Add(-tokenExpiryBuffer)) {
				// Cache it and return
				s.tokens.mu.Lock()
				s.tokens.tokens[name] = &cachedToken{
					accessToken: cred.AccessToken,
					expiresAt:   expiry.Add(-tokenExpiryBuffer),
				}
				s.tokens.mu.Unlock()
				if s.redactor != nil {
					s.redactor.AddSecret(name+"/token", cred.AccessToken)
				}
				return cred.AccessToken, nil
			}
		}
	}

	// Token expired or missing — refresh
	if cred.RefreshToken == "" {
		return "", fmt.Errorf("access token expired and no refresh_token available — the user needs to re-authorize")
	}

	accessToken, newRefreshToken, expiresIn, err := refreshAuthCodeToken(cred)
	if err != nil {
		return "", err
	}

	// Calculate expiry
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	// Update in-memory cache
	s.tokens.mu.Lock()
	s.tokens.tokens[name] = &cachedToken{
		accessToken: accessToken,
		expiresAt:   expiresAt.Add(-tokenExpiryBuffer),
	}
	s.tokens.mu.Unlock()

	// Register for redaction
	if s.redactor != nil {
		s.redactor.AddSecret(name+"/token", accessToken)
	}

	// Persist updated tokens to disk — critical for auth code flow since
	// the refresh token may rotate (new refresh_token returned on each use).
	s.mu.Lock()
	if stored, ok := s.data.Credentials[name]; ok {
		stored.AccessToken = accessToken
		stored.TokenExpiry = expiresAt.Format(time.RFC3339)
		if newRefreshToken != "" {
			stored.RefreshToken = newRefreshToken
			// Update redaction for the new refresh token
			if s.redactor != nil {
				s.redactor.AddSecret(name, newRefreshToken)
			}
		}
		if err := s.save(); err != nil {
			s.mu.Unlock()
			// Non-fatal: token works but won't survive restart
			return accessToken, nil
		}
	}
	s.mu.Unlock()

	return accessToken, nil
}

// Compile-time check: *Store satisfies CredentialResolver.
var _ CredentialResolver = (*Store)(nil)
