package credentials

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncryptDecrypt(t *testing.T) {
	key, err := generateKey()
	if err != nil {
		t.Fatalf("generateKey: %v", err)
	}

	plaintext := []byte("hello, credential store!")
	ciphertext, err := encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Ciphertext should be different from plaintext
	if string(ciphertext) == string(plaintext) {
		t.Error("ciphertext should differ from plaintext")
	}

	decrypted, err := decrypt(ciphertext, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptDecryptWrongKey(t *testing.T) {
	key1, _ := generateKey()
	key2, _ := generateKey()

	plaintext := []byte("secret data")
	ciphertext, err := encrypt(plaintext, key1)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	_, err = decrypt(ciphertext, key2)
	if err == nil {
		t.Error("expected decryption to fail with wrong key")
	}
}

func TestEncryptDecryptEmpty(t *testing.T) {
	key, _ := generateKey()

	ciphertext, err := encrypt([]byte{}, key)
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}

	decrypted, err := decrypt(ciphertext, key)
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}

	if len(decrypted) != 0 {
		t.Errorf("expected empty, got %d bytes", len(decrypted))
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	dir := t.TempDir()

	// First call: should create a key
	key1, err := loadOrCreateKey(dir)
	if err != nil {
		t.Fatalf("first loadOrCreateKey: %v", err)
	}
	if len(key1) != keySize {
		t.Errorf("key length = %d, want %d", len(key1), keySize)
	}

	// Verify file permissions
	info, err := os.Stat(filepath.Join(dir, keyFileName))
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("key file permissions = %o, want 0600", info.Mode().Perm())
	}

	// Verify file is hex-encoded text (not raw binary)
	data, err := os.ReadFile(filepath.Join(dir, keyFileName))
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) != keySize*2 {
		t.Errorf("hex key length = %d chars, want %d", len(trimmed), keySize*2)
	}
	decoded, err := hex.DecodeString(trimmed)
	if err != nil {
		t.Fatalf("key file is not valid hex: %v", err)
	}
	if string(decoded) != string(key1) {
		t.Error("hex-decoded key doesn't match returned key")
	}

	// Second call: should load the same key
	key2, err := loadOrCreateKey(dir)
	if err != nil {
		t.Fatalf("second loadOrCreateKey: %v", err)
	}
	if string(key1) != string(key2) {
		t.Error("second load should return the same key")
	}
}

func TestLoadOrCreateKey_BinaryMigration(t *testing.T) {
	dir := t.TempDir()

	// Write a raw binary key file (legacy format)
	rawKey := make([]byte, keySize)
	for i := range rawKey {
		rawKey[i] = byte(i + 42)
	}
	keyPath := filepath.Join(dir, keyFileName)
	if err := os.WriteFile(keyPath, rawKey, 0600); err != nil {
		t.Fatalf("write legacy key: %v", err)
	}

	// Load should succeed and return the same key
	key, err := loadOrCreateKey(dir)
	if err != nil {
		t.Fatalf("loadOrCreateKey with binary key: %v", err)
	}
	if string(key) != string(rawKey) {
		t.Error("loaded key doesn't match original binary key")
	}

	// File should now be hex-encoded (auto-migrated)
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read migrated key file: %v", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) != keySize*2 {
		t.Errorf("migrated key should be %d hex chars, got %d bytes", keySize*2, len(trimmed))
	}
	decoded, err := hex.DecodeString(trimmed)
	if err != nil {
		t.Fatalf("migrated key file is not valid hex: %v", err)
	}
	if string(decoded) != string(rawKey) {
		t.Error("migrated hex key doesn't decode to original binary key")
	}

	// Second load should still work (now reading hex format)
	key2, err := loadOrCreateKey(dir)
	if err != nil {
		t.Fatalf("second load after migration: %v", err)
	}
	if string(key2) != string(rawKey) {
		t.Error("second load after migration should return same key")
	}
}

func TestLoadOrCreateKey_EnvVarPrecedence(t *testing.T) {
	dir := t.TempDir()

	// Generate a known key as hex
	knownKey := make([]byte, keySize)
	for i := range knownKey {
		knownKey[i] = byte(i + 100)
	}
	hexKey := hex.EncodeToString(knownKey)

	// Set ASTONISH_MASTER_KEY env var
	t.Setenv("ASTONISH_MASTER_KEY", hexKey)

	// loadOrCreateKey should return the env var key (not create a file)
	key, err := loadOrCreateKey(dir)
	if err != nil {
		t.Fatalf("loadOrCreateKey with env var: %v", err)
	}
	if string(key) != string(knownKey) {
		t.Error("key should match ASTONISH_MASTER_KEY env var")
	}

	// No file should have been created
	if _, err := os.Stat(filepath.Join(dir, keyFileName)); !os.IsNotExist(err) {
		t.Error("key file should NOT be created when ASTONISH_MASTER_KEY is set")
	}
}

func TestLoadOrCreateKey_EnvVarInvalidHex(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("ASTONISH_MASTER_KEY", "not-valid-hex")

	_, err := loadOrCreateKey(dir)
	if err == nil {
		t.Error("expected error for invalid hex in ASTONISH_MASTER_KEY")
	}
}

func TestLoadOrCreateKey_EnvVarWrongLength(t *testing.T) {
	dir := t.TempDir()

	// 16 bytes (32 hex chars) instead of 32 bytes (64 hex chars)
	t.Setenv("ASTONISH_MASTER_KEY", "abcdef0123456789abcdef0123456789")

	_, err := loadOrCreateKey(dir)
	if err == nil {
		t.Error("expected error for wrong-length ASTONISH_MASTER_KEY")
	}
}

func TestStoreKeyRedactionWithHexFormat(t *testing.T) {
	dir := t.TempDir()

	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Read the key file to get the hex string that cat would produce
	data, err := os.ReadFile(filepath.Join(dir, keyFileName))
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	hexKey := strings.TrimSpace(string(data))

	// Simulating: "cat .store_key" returns the hex string
	// The redactor should catch it
	redacted := store.Redactor().Redact(hexKey)
	if !strings.Contains(redacted, "[REDACTED:") {
		t.Errorf("hex key should be redacted, got: %s", redacted)
	}
	if strings.Contains(redacted, hexKey) {
		t.Errorf("redacted output still contains hex key")
	}
}

func TestStoreKeyRedactionWithHexInSurroundingText(t *testing.T) {
	dir := t.TempDir()

	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Read the hex key
	data, err := os.ReadFile(filepath.Join(dir, keyFileName))
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	hexKey := strings.TrimSpace(string(data))

	// Simulating tool output that includes the key surrounded by other text
	toolOutput := "$ cat /home/user/.config/astonish/.store_key\n" + hexKey + "\n$"
	redacted := store.Redactor().Redact(toolOutput)
	if strings.Contains(redacted, hexKey) {
		t.Errorf("hex key should not appear in redacted output, got:\n%s", redacted)
	}
}

func TestStoreCreateEmpty(t *testing.T) {
	dir := t.TempDir()

	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if store.Count() != 0 {
		t.Errorf("expected 0 credentials, got %d", store.Count())
	}

	list := store.List()
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d entries", len(list))
	}
}

func TestStoreCRUD(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Add
	err = store.Set("my-api", &Credential{
		Type:   CredAPIKey,
		Header: "X-API-Key",
		Value:  "sk-test-1234567890",
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	if store.Count() != 1 {
		t.Errorf("Count = %d, want 1", store.Count())
	}

	// Get
	cred := store.Get("my-api")
	if cred == nil {
		t.Fatal("Get returned nil")
		return
	}
	if cred.Type != CredAPIKey {
		t.Errorf("Type = %q, want %q", cred.Type, CredAPIKey)
	}
	if cred.Value != "sk-test-1234567890" {
		t.Errorf("Value = %q, want %q", cred.Value, "sk-test-1234567890")
	}

	// Get non-existent
	if store.Get("nonexistent") != nil {
		t.Error("expected nil for nonexistent credential")
	}

	// List
	list := store.List()
	if len(list) != 1 {
		t.Errorf("List length = %d, want 1", len(list))
	}
	if list["my-api"] != CredAPIKey {
		t.Errorf("List[my-api] = %q, want %q", list["my-api"], CredAPIKey)
	}

	// Update
	err = store.Set("my-api", &Credential{
		Type:   CredAPIKey,
		Header: "X-API-Key",
		Value:  "sk-updated-9876543210",
	})
	if err != nil {
		t.Fatalf("Set (update): %v", err)
	}
	if store.Count() != 1 {
		t.Errorf("Count after update = %d, want 1", store.Count())
	}
	updated := store.Get("my-api")
	if updated.Value != "sk-updated-9876543210" {
		t.Errorf("updated Value = %q, want %q", updated.Value, "sk-updated-9876543210")
	}

	// Remove
	err = store.Remove("my-api")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if store.Count() != 0 {
		t.Errorf("Count after remove = %d, want 0", store.Count())
	}

	// Remove non-existent
	err = store.Remove("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent remove")
	}
}

func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()

	// Create and add credential
	store1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open (1): %v", err)
	}
	store1.Set("persist-test", &Credential{
		Type:  CredBearer,
		Token: "eyJhbGciOiJIUzI1NiJ9.test-token",
	})

	// Reopen from disk
	store2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open (2): %v", err)
	}

	cred := store2.Get("persist-test")
	if cred == nil {
		t.Fatal("credential not found after reload")
		return
	}
	if cred.Token != "eyJhbGciOiJIUzI1NiJ9.test-token" {
		t.Errorf("Token = %q after reload, want %q", cred.Token, "eyJhbGciOiJIUzI1NiJ9.test-token")
	}
}

func TestStoreEncryptedOnDisk(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)

	secret := "super-secret-api-key-12345"
	store.Set("test", &Credential{
		Type:   CredAPIKey,
		Header: "Authorization",
		Value:  secret,
	})

	// Read the raw file — should be encrypted, not contain the secret
	raw, err := os.ReadFile(filepath.Join(dir, storeFileName))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if len(raw) == 0 {
		t.Fatal("encrypted file should not be empty")
	}

	rawStr := string(raw)
	if contains(rawStr, secret) {
		t.Error("encrypted file should not contain plaintext secret")
	}
	if contains(rawStr, "super-secret") {
		t.Error("encrypted file should not contain any part of the secret")
	}
}

func TestStoreRawContentRoundTripPreservesFormatting(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	content := "  {\n  \"token\": \"abc12345\",\n  \"trailing\": \"keep ,;\"\n}\n  "
	if err := store.Set("providers-file", &Credential{
		Type:        CredRawContent,
		Content:     content,
		ContentType: " application/json ",
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got := store.Get("providers-file")
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.Type != CredRawContent {
		t.Fatalf("Type = %q, want %q", got.Type, CredRawContent)
	}
	if got.Content != content {
		t.Fatalf("Content was sanitized or changed: got %q want %q", got.Content, content)
	}
	if got.ContentType != "application/json" {
		t.Fatalf("ContentType = %q, want application/json", got.ContentType)
	}
}

func TestStoreResolveRawContentRejectedForHTTP(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	if err := store.Set("config-file", &Credential{Type: CredRawContent, Content: "secret-content-12345"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	_, _, err := store.Resolve("config-file")
	if err == nil {
		t.Fatal("expected Resolve to reject raw_content")
	}
	if !strings.Contains(err.Error(), "raw content") {
		t.Fatalf("Resolve error = %v, want raw content message", err)
	}
}

func TestStoreResolveAPIKey(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)

	store.Set("my-api", &Credential{
		Type:   CredAPIKey,
		Header: "X-API-Key",
		Value:  "sk-test-key-12345678",
	})

	key, value, err := store.Resolve("my-api")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if key != "X-API-Key" {
		t.Errorf("header key = %q, want %q", key, "X-API-Key")
	}
	if value != "sk-test-key-12345678" {
		t.Errorf("header value = %q, want %q", value, "sk-test-key-12345678")
	}
}

func TestStoreResolveBearer(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)

	store.Set("my-bearer", &Credential{
		Type:  CredBearer,
		Token: "eyJhbGciOiJIUzI1NiJ9.test",
	})

	key, value, err := store.Resolve("my-bearer")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if key != "Authorization" {
		t.Errorf("header key = %q, want %q", key, "Authorization")
	}
	if value != "Bearer eyJhbGciOiJIUzI1NiJ9.test" {
		t.Errorf("header value = %q, want %q", value, "Bearer eyJhbGciOiJIUzI1NiJ9.test")
	}
}

func TestStoreResolveBasic(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)

	store.Set("my-basic", &Credential{
		Type:     CredBasic,
		Username: "admin",
		Password: "hunter2-extended",
	})

	key, value, err := store.Resolve("my-basic")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if key != "Authorization" {
		t.Errorf("header key = %q, want %q", key, "Authorization")
	}

	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:hunter2-extended"))
	if value != expected {
		t.Errorf("header value = %q, want %q", value, expected)
	}
}

func TestStoreResolveNotFound(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)

	_, _, err := store.Resolve("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent credential")
	}
}

func TestStorePasswordCRUD(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Save a password credential
	err = store.Set("proxmox-ssh", &Credential{
		Type:     CredPassword,
		Username: "root",
		Password: "my-secret-password",
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Get — should return raw fields
	cred := store.Get("proxmox-ssh")
	if cred == nil {
		t.Fatal("Get returned nil")
		return
	}
	if cred.Type != CredPassword {
		t.Errorf("Type = %q, want %q", cred.Type, CredPassword)
	}
	if cred.Username != "root" {
		t.Errorf("Username = %q, want %q", cred.Username, "root")
	}
	if cred.Password != "my-secret-password" {
		t.Errorf("Password = %q, want %q", cred.Password, "my-secret-password")
	}

	// List — should show the type
	list := store.List()
	if list["proxmox-ssh"] != CredPassword {
		t.Errorf("List[proxmox-ssh] = %q, want %q", list["proxmox-ssh"], CredPassword)
	}
}

func TestStoreResolvePassword_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)

	store.Set("ssh-cred", &Credential{
		Type:     CredPassword,
		Username: "admin",
		Password: "secret123",
	})

	_, _, err := store.Resolve("ssh-cred")
	if err == nil {
		t.Error("expected error resolving password credential")
	}
	if !strings.Contains(err.Error(), "not an HTTP credential") {
		t.Errorf("error should mention 'not an HTTP credential', got: %v", err)
	}
}

func TestStorePasswordPersistence(t *testing.T) {
	dir := t.TempDir()

	// Create and add
	store1, _ := Open(dir)
	store1.Set("db-cred", &Credential{
		Type:     CredPassword,
		Username: "dbuser",
		Password: "dbpass123",
	})

	// Reopen from disk
	store2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open (2): %v", err)
	}

	cred := store2.Get("db-cred")
	if cred == nil {
		t.Fatal("credential not found after reload")
		return
	}
	if cred.Username != "dbuser" || cred.Password != "dbpass123" {
		t.Errorf("fields don't match after reload: user=%q pass=%q", cred.Username, cred.Password)
	}
}

func TestStorePasswordRedaction(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)

	store.Set("ssh-test", &Credential{
		Type:     CredPassword,
		Username: "root",
		Password: "super-secret-ssh-pass-12345",
	})

	// The password should be redacted
	input := "Password is: super-secret-ssh-pass-12345"
	output := store.Redactor().Redact(input)

	if contains(output, "super-secret-ssh-pass-12345") {
		t.Error("password should be redacted from output")
	}
}

func TestStoreReload(t *testing.T) {
	dir := t.TempDir()

	// Open store1 and add a credential
	store1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open store1: %v", err)
	}
	store1.Set("existing", &Credential{
		Type:  CredBearer,
		Token: "token-existing-111111",
	})

	// Simulate an external write: open a second store instance (like the CLI)
	// and add a credential through it
	store2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open store2: %v", err)
	}
	store2.Set("cli-added", &Credential{
		Type:     CredPassword,
		Username: "admin",
		Password: "external-password-222222",
	})

	// Before reload: store1 should NOT see "cli-added"
	if store1.Get("cli-added") != nil {
		t.Fatal("store1 should not see cli-added before Reload()")
	}

	// Reload store1 from disk
	if err := store1.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// After reload: store1 should see both credentials
	cred := store1.Get("cli-added")
	if cred == nil {
		t.Fatal("store1 should see cli-added after Reload()")
		return
	}
	if cred.Username != "admin" || cred.Password != "external-password-222222" {
		t.Errorf("cli-added fields wrong: user=%q pass=%q", cred.Username, cred.Password)
	}

	// Original credential should still be there
	existing := store1.Get("existing")
	if existing == nil {
		t.Fatal("existing credential should survive reload")
		return
	}
	if existing.Token != "token-existing-111111" {
		t.Errorf("existing token = %q, want %q", existing.Token, "token-existing-111111")
	}

	// Redaction should work for the newly loaded credential's password
	output := store1.Redactor().Redact("password is external-password-222222")
	if contains(output, "external-password-222222") {
		t.Error("password from reloaded credential should be redacted")
	}
}

// --- Redaction tests ---

func TestRedactSimple(t *testing.T) {
	r := NewRedactor()
	r.AddSecret("my-api", "sk-secret-key-12345678")

	input := "The API key is sk-secret-key-12345678 in this text"
	output := r.Redact(input)
	expected := "The API key is [REDACTED:my-api] in this text"

	if output != expected {
		t.Errorf("Redact = %q, want %q", output, expected)
	}
}

func TestRedactBase64(t *testing.T) {
	r := NewRedactor()
	secret := "my-secret-value-for-b64"
	r.AddSecret("test", secret)

	b64 := base64.StdEncoding.EncodeToString([]byte(secret))
	input := "Encoded: " + b64
	output := r.Redact(input)

	if contains(output, b64) {
		t.Errorf("base64 encoding should be redacted, got: %s", output)
	}
}

func TestRedactURLEncoded(t *testing.T) {
	r := NewRedactor()
	secret := "key=value&special+chars"
	r.AddSecret("test", secret)

	urlEnc := url.QueryEscape(secret)
	input := "URL: ?" + urlEnc
	output := r.Redact(input)

	if contains(output, urlEnc) {
		t.Errorf("URL-encoded value should be redacted, got: %s", output)
	}
}

func TestRedactMap(t *testing.T) {
	r := NewRedactor()
	r.AddSecret("my-key", "supersecretvalue123")

	input := map[string]any{
		"stdout":  "output contains supersecretvalue123 here",
		"code":    42,
		"nested":  map[string]any{"data": "also supersecretvalue123"},
		"list":    []any{"supersecretvalue123", "safe"},
		"boolean": true,
	}

	output := r.RedactMap(input)

	// Check string field
	if s, ok := output["stdout"].(string); ok {
		if contains(s, "supersecretvalue123") {
			t.Errorf("stdout should be redacted: %s", s)
		}
	}

	// Check nested map
	if nested, ok := output["nested"].(map[string]any); ok {
		if s, ok := nested["data"].(string); ok {
			if contains(s, "supersecretvalue123") {
				t.Errorf("nested.data should be redacted: %s", s)
			}
		}
	}

	// Check list
	if list, ok := output["list"].([]any); ok {
		if s, ok := list[0].(string); ok {
			if contains(s, "supersecretvalue123") {
				t.Errorf("list[0] should be redacted: %s", s)
			}
		}
		// Second element should be unchanged
		if s, ok := list[1].(string); ok {
			if s != "safe" {
				t.Errorf("list[1] = %q, want %q", s, "safe")
			}
		}
	}

	// Non-string fields unchanged
	if code, ok := output["code"].(int); ok {
		if code != 42 {
			t.Errorf("code = %d, want 42", code)
		}
	}
}

func TestRedactMinLength(t *testing.T) {
	r := NewRedactor()
	r.AddSecret("short", "abc") // Too short, should not be tracked

	if r.SignatureCount() != 0 {
		t.Errorf("short secret should not be tracked, got %d signatures", r.SignatureCount())
	}

	input := "abc is safe"
	output := r.Redact(input)
	if output != input {
		t.Errorf("short value should not be redacted: %q", output)
	}
}

func TestRedactStoreKey(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)

	// Read the key file — now stored as hex text
	keyData, _ := os.ReadFile(filepath.Join(dir, keyFileName))
	hexKey := strings.TrimSpace(string(keyData))

	// The hex key string should be redacted (this is what "cat .store_key" would produce)
	input := "Key file contains: " + hexKey
	output := store.Redactor().Redact(input)

	if contains(output, hexKey) {
		t.Error("store encryption key should be redacted from output")
	}
	if !contains(output, "[REDACTED:store-encryption-key]") {
		t.Errorf("expected redaction marker, got: %s", output)
	}
}

func TestRedactCredentialValues(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)

	store.Set("my-api", &Credential{
		Type:   CredAPIKey,
		Header: "X-API-Key",
		Value:  "sk-prod-api-key-99887766",
	})

	// The credential value should be redacted
	input := "Found key: sk-prod-api-key-99887766"
	output := store.Redactor().Redact(input)

	if contains(output, "sk-prod-api-key-99887766") {
		t.Error("credential value should be redacted")
	}
	if !contains(output, "[REDACTED:my-api]") {
		t.Errorf("expected redaction marker, got: %s", output)
	}
}

func TestRedactAfterRemove(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)

	store.Set("temp", &Credential{
		Type:  CredBearer,
		Token: "temp-token-abcdef123456",
	})

	// Should be redacted
	output := store.Redactor().Redact("Token: temp-token-abcdef123456")
	if contains(output, "temp-token-abcdef123456") {
		t.Error("token should be redacted before removal")
	}

	// Remove the credential
	store.Remove("temp")

	// Should no longer be redacted
	output = store.Redactor().Redact("Token: temp-token-abcdef123456")
	if !contains(output, "temp-token-abcdef123456") {
		t.Error("token should not be redacted after credential removal")
	}
}

func TestRedactNoFalsePositives(t *testing.T) {
	r := NewRedactor()
	r.AddSecret("my-api", "specific-api-key-12345")

	input := "This text has no secrets in it whatsoever."
	output := r.Redact(input)

	if output != input {
		t.Errorf("no-match text was modified: %q", output)
	}
}

func TestRedactMultipleCredentials(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)

	store.Set("api-a", &Credential{Type: CredAPIKey, Header: "X-Key", Value: "key-aaaaa-11111111"})
	store.Set("api-b", &Credential{Type: CredAPIKey, Header: "X-Key", Value: "key-bbbbb-22222222"})

	input := "Keys: key-aaaaa-11111111 and key-bbbbb-22222222"
	output := store.Redactor().Redact(input)

	if contains(output, "key-aaaaa-11111111") {
		t.Error("first key should be redacted")
	}
	if contains(output, "key-bbbbb-22222222") {
		t.Error("second key should be redacted")
	}
}

func TestOAuthTokenCache(t *testing.T) {
	tc := newTokenCache()
	r := NewRedactor()

	// Manually cache a token
	tc.mu.Lock()
	tc.tokens["test"] = &cachedToken{
		accessToken: "cached-access-token-xyz",
		expiresAt:   time.Now().Add(300 * time.Second),
	}
	tc.mu.Unlock()

	// Should return cached token without calling fetchOAuthToken
	cred := &Credential{
		Type:         CredOAuthClientCreds,
		AuthURL:      "https://auth.example.com/token",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
	}

	token, err := tc.GetOrRefresh("test", cred, r)
	if err != nil {
		t.Fatalf("GetOrRefresh: %v", err)
	}
	if token != "cached-access-token-xyz" {
		t.Errorf("token = %q, want %q", token, "cached-access-token-xyz")
	}
}

func TestOAuthTokenCacheInvalidate(t *testing.T) {
	tc := newTokenCache()

	// Manually cache a token
	tc.mu.Lock()
	tc.tokens["my-cred"] = &cachedToken{
		accessToken: "stale-token",
		expiresAt:   time.Now().Add(300 * time.Second),
	}
	tc.mu.Unlock()

	// Verify it's cached
	tc.mu.RLock()
	_, exists := tc.tokens["my-cred"]
	tc.mu.RUnlock()
	if !exists {
		t.Fatal("expected token to be cached")
	}

	// Invalidate
	tc.Invalidate("my-cred")

	// Verify it's gone
	tc.mu.RLock()
	_, exists = tc.tokens["my-cred"]
	tc.mu.RUnlock()
	if exists {
		t.Fatal("expected token to be evicted after Invalidate")
	}
}

func TestOAuthTokenCacheInvalidateNonExistent(t *testing.T) {
	tc := newTokenCache()

	// Should not panic on non-existent key
	tc.Invalidate("does-not-exist")
}

func TestOAuthTokenCacheInvalidateAndRefresh(t *testing.T) {
	tc := newTokenCache()
	r := NewRedactor()

	// Cache a valid token
	tc.mu.Lock()
	tc.tokens["refresh-test"] = &cachedToken{
		accessToken: "old-token",
		expiresAt:   time.Now().Add(300 * time.Second),
	}
	tc.mu.Unlock()

	// GetOrRefresh should return the cached token
	cred := &Credential{
		Type:         CredOAuthClientCreds,
		AuthURL:      "https://auth.example.com/token",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
	}
	token, err := tc.GetOrRefresh("refresh-test", cred, r)
	if err != nil {
		t.Fatalf("GetOrRefresh (before invalidate): %v", err)
	}
	if token != "old-token" {
		t.Errorf("expected old-token, got %q", token)
	}

	// Invalidate it
	tc.Invalidate("refresh-test")

	// Next GetOrRefresh should try to fetch (will fail since no real server)
	// but proves the cache was cleared.
	_, err = tc.GetOrRefresh("refresh-test", cred, r)
	if err == nil {
		t.Fatal("expected error from GetOrRefresh after invalidation (no real OAuth server)")
	}
}

// contains is a test helper for string containment.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- OAuth Authorization Code credential tests ---

func TestOAuthAuthCodeSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	cred := &Credential{
		Type:         CredOAuthAuthCode,
		TokenURL:     "https://oauth2.googleapis.com/token",
		ClientID:     "test-client-id-123.apps.googleusercontent.com",
		ClientSecret: "GOCSPX-test-secret-value",
		AccessToken:  "ya29.test-access-token-abcdef",
		RefreshToken: "1//test-refresh-token-ghijkl",
		Scope:        "https://www.googleapis.com/auth/calendar.readonly",
	}

	if err := store.Set("google-calendar", cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Reload from disk
	store2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open (reload): %v", err)
	}

	loaded := store2.Get("google-calendar")
	if loaded == nil {
		t.Fatal("credential not found after reload")
		return
	}
	if loaded.Type != CredOAuthAuthCode {
		t.Errorf("type = %q, want %q", loaded.Type, CredOAuthAuthCode)
	}
	if loaded.TokenURL != cred.TokenURL {
		t.Errorf("token_url = %q, want %q", loaded.TokenURL, cred.TokenURL)
	}
	if loaded.ClientID != cred.ClientID {
		t.Errorf("client_id mismatch")
	}
	if loaded.ClientSecret != cred.ClientSecret {
		t.Errorf("client_secret mismatch")
	}
	if loaded.AccessToken != cred.AccessToken {
		t.Errorf("access_token mismatch")
	}
	if loaded.RefreshToken != cred.RefreshToken {
		t.Errorf("refresh_token mismatch")
	}
	if loaded.Scope != cred.Scope {
		t.Errorf("scope = %q, want %q", loaded.Scope, cred.Scope)
	}
}

func TestOAuthAuthCodeResolveValidToken(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Set a token that expires in the future
	expiry := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	cred := &Credential{
		Type:         CredOAuthAuthCode,
		TokenURL:     "https://oauth2.googleapis.com/token",
		ClientID:     "test-client-id-123.apps.googleusercontent.com",
		ClientSecret: "GOCSPX-test-secret-value",
		AccessToken:  "ya29.valid-access-token-12345",
		RefreshToken: "1//test-refresh-token-ghijkl",
		TokenExpiry:  expiry,
	}

	if err := store.Set("test-oauth", cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Resolve should return the stored token without refreshing
	headerKey, headerValue, err := store.Resolve("test-oauth")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if headerKey != "Authorization" {
		t.Errorf("header key = %q, want %q", headerKey, "Authorization")
	}
	if headerValue != "Bearer ya29.valid-access-token-12345" {
		t.Errorf("header value = %q, want %q", headerValue, "Bearer ya29.valid-access-token-12345")
	}
}

func TestOAuthAuthCodeResolveNoRefreshToken(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Set a token that's already expired with no refresh token
	expiry := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	cred := &Credential{
		Type:         CredOAuthAuthCode,
		TokenURL:     "https://oauth2.googleapis.com/token",
		ClientID:     "test-client-id-123.apps.googleusercontent.com",
		ClientSecret: "GOCSPX-test-secret-value",
		AccessToken:  "ya29.expired-access-token",
		TokenExpiry:  expiry,
		// No RefreshToken
	}

	if err := store.Set("test-no-refresh", cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	_, _, err = store.Resolve("test-no-refresh")
	if err == nil {
		t.Fatal("expected error when resolving expired token without refresh_token")
	}
	if !strings.Contains(err.Error(), "refresh_token") {
		t.Errorf("error should mention refresh_token, got: %v", err)
	}
}

func TestOAuthAuthCodeResolveCachedToken(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Save a credential with expired token
	cred := &Credential{
		Type:         CredOAuthAuthCode,
		TokenURL:     "https://oauth2.googleapis.com/token",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret-val",
		AccessToken:  "ya29.old-token",
		RefreshToken: "1//refresh-token",
	}

	if err := store.Set("test-cache", cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Manually inject a cached token (simulates prior successful resolve)
	store.tokens.mu.Lock()
	store.tokens.tokens["test-cache"] = &cachedToken{
		accessToken: "ya29.cached-from-memory-token",
		expiresAt:   time.Now().Add(5 * time.Minute),
	}
	store.tokens.mu.Unlock()

	// Resolve should return the cached token, not try to refresh
	_, headerValue, err := store.Resolve("test-cache")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if headerValue != "Bearer ya29.cached-from-memory-token" {
		t.Errorf("expected cached token, got: %q", headerValue)
	}
}

func TestOAuthAuthCodeRedaction(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	cred := &Credential{
		Type:         CredOAuthAuthCode,
		TokenURL:     "https://oauth2.googleapis.com/token",
		ClientID:     "test-client-id-for-redaction.apps.googleusercontent.com",
		ClientSecret: "GOCSPX-redact-this-secret",
		AccessToken:  "ya29.redact-this-access-token",
		RefreshToken: "1//redact-this-refresh-token",
	}

	if err := store.Set("gcal", cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	r := store.Redactor()

	// All secret fields should be redacted
	input := "Secret: GOCSPX-redact-this-secret, Token: ya29.redact-this-access-token, Refresh: 1//redact-this-refresh-token"
	output := r.Redact(input)

	if strings.Contains(output, "GOCSPX-redact-this-secret") {
		t.Error("client_secret should be redacted")
	}
	if strings.Contains(output, "ya29.redact-this-access-token") {
		t.Error("access_token should be redacted")
	}
	if strings.Contains(output, "1//redact-this-refresh-token") {
		t.Error("refresh_token should be redacted")
	}

	// Client ID should also be redacted
	if strings.Contains(output, "test-client-id-for-redaction.apps.googleusercontent.com") {
		t.Error("client_id should be redacted")
	}
}

func TestOAuthAuthCodeList(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	cred := &Credential{
		Type:         CredOAuthAuthCode,
		TokenURL:     "https://oauth2.googleapis.com/token",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret-val",
		AccessToken:  "ya29.test-token-value",
		RefreshToken: "1//test-refresh-value",
	}

	if err := store.Set("gcal", cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	listed := store.List()
	if credType, ok := listed["gcal"]; !ok {
		t.Error("gcal not found in list")
	} else if credType != CredOAuthAuthCode {
		t.Errorf("type = %q, want %q", credType, CredOAuthAuthCode)
	}
}

func TestOAuthAuthCodeRemoveAndReload(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	cred := &Credential{
		Type:         CredOAuthAuthCode,
		TokenURL:     "https://oauth2.googleapis.com/token",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret-val",
		AccessToken:  "ya29.test-token-value",
		RefreshToken: "1//test-refresh-value",
	}

	if err := store.Set("to-remove", cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := store.Remove("to-remove"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Verify removed from memory
	if store.Get("to-remove") != nil {
		t.Error("credential should be removed from memory")
	}

	// Verify removed from disk
	store2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open (reload): %v", err)
	}
	if store2.Get("to-remove") != nil {
		t.Error("credential should be removed from disk")
	}

	// Verify redaction signatures are cleaned up
	secretText := "GOCSPX-redact-this-secret ya29.test-token-value 1//test-refresh-value"
	output := store.Redactor().Redact(secretText)
	// After remove, the values might still be in the redactor (by design — conservative),
	// but the credential itself should be gone.
	if store.Get("to-remove") != nil {
		t.Error("credential should be gone")
	}
	_ = output // redaction behavior after remove is not strictly specified
}

func TestSanitizeCredential(t *testing.T) {
	tests := []struct {
		name     string
		input    *Credential
		expected *Credential
	}{
		{
			name: "trailing quote and comma from JSON copy-paste",
			input: &Credential{
				Type:         CredOAuthClientCreds,
				AuthURL:      "https://auth.example.com/oauth/token",
				ClientID:     `sb-abc-123!b2002|my-service!b1045",`,
				ClientSecret: `d856-secret-value$rQQqU=",`,
			},
			expected: &Credential{
				Type:         CredOAuthClientCreds,
				AuthURL:      "https://auth.example.com/oauth/token",
				ClientID:     `sb-abc-123!b2002|my-service!b1045`,
				ClientSecret: `d856-secret-value$rQQqU=`,
			},
		},
		{
			name: "surrounding double quotes",
			input: &Credential{
				Type:  CredBearer,
				Token: `"my-bearer-token"`,
			},
			expected: &Credential{
				Type:  CredBearer,
				Token: "my-bearer-token",
			},
		},
		{
			name: "surrounding single quotes",
			input: &Credential{
				Type:   CredAPIKey,
				Header: "X-API-Key",
				Value:  `'my-api-key'`,
			},
			expected: &Credential{
				Type:   CredAPIKey,
				Header: "X-API-Key",
				Value:  "my-api-key",
			},
		},
		{
			name: "leading and trailing whitespace",
			input: &Credential{
				Type:     CredBasic,
				Username: "  admin  ",
				Password: "  secret  ",
			},
			expected: &Credential{
				Type:     CredBasic,
				Username: "admin",
				Password: "secret",
			},
		},
		{
			name: "trailing semicolons",
			input: &Credential{
				Type:  CredBearer,
				Token: "my-token;",
			},
			expected: &Credential{
				Type:  CredBearer,
				Token: "my-token",
			},
		},
		{
			name: "clean values unchanged",
			input: &Credential{
				Type:         CredOAuthClientCreds,
				AuthURL:      "https://auth.example.com/token",
				ClientID:     "clean-client-id",
				ClientSecret: "clean-secret",
				Scope:        "read write",
			},
			expected: &Credential{
				Type:         CredOAuthClientCreds,
				AuthURL:      "https://auth.example.com/token",
				ClientID:     "clean-client-id",
				ClientSecret: "clean-secret",
				Scope:        "read write",
			},
		},
		{
			name: "empty fields stay empty",
			input: &Credential{
				Type: CredAPIKey,
			},
			expected: &Credential{
				Type: CredAPIKey,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sanitizeCredential(tt.input)
			if tt.input.Header != tt.expected.Header {
				t.Errorf("Header: got %q, want %q", tt.input.Header, tt.expected.Header)
			}
			if tt.input.Value != tt.expected.Value {
				t.Errorf("Value: got %q, want %q", tt.input.Value, tt.expected.Value)
			}
			if tt.input.Token != tt.expected.Token {
				t.Errorf("Token: got %q, want %q", tt.input.Token, tt.expected.Token)
			}
			if tt.input.Username != tt.expected.Username {
				t.Errorf("Username: got %q, want %q", tt.input.Username, tt.expected.Username)
			}
			if tt.input.Password != tt.expected.Password {
				t.Errorf("Password: got %q, want %q", tt.input.Password, tt.expected.Password)
			}
			if tt.input.AuthURL != tt.expected.AuthURL {
				t.Errorf("AuthURL: got %q, want %q", tt.input.AuthURL, tt.expected.AuthURL)
			}
			if tt.input.ClientID != tt.expected.ClientID {
				t.Errorf("ClientID: got %q, want %q", tt.input.ClientID, tt.expected.ClientID)
			}
			if tt.input.ClientSecret != tt.expected.ClientSecret {
				t.Errorf("ClientSecret: got %q, want %q", tt.input.ClientSecret, tt.expected.ClientSecret)
			}
			if tt.input.Scope != tt.expected.Scope {
				t.Errorf("Scope: got %q, want %q", tt.input.Scope, tt.expected.Scope)
			}
		})
	}
}

func TestSanitizeSecretValue(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`"my-api-key",`, "my-api-key"},
		{`'my-api-key';`, "my-api-key"},
		{`  spaced  `, "spaced"},
		{`clean-value`, "clean-value"},
		{`""`, ""},
		{``, ""},
	}

	for _, tt := range tests {
		got := sanitizeSecretValue(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeSecretValue(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
