package entstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	personalent "github.com/SAP/astonish/ent/personal"
	"github.com/SAP/astonish/ent/personal/credential"
	teament "github.com/SAP/astonish/ent/team"
	teamcredential "github.com/SAP/astonish/ent/team/credential"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"

	_ "modernc.org/sqlite"
)

// testMasterKeyHex is a fixed 32-byte key (64 hex chars) used in tests.
const testMasterKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// setupPersonalStore creates a fresh SQLite-backed personal Ent client with
// migrated schema. Returns the credential store and the raw Ent client (for
// inspecting stored bytes directly).
func setupPersonalStore(t *testing.T) (*personalCredentialStore, *personalent.Client) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "personal.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatalf("pragma: %v", err)
		}
	}

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := personalent.NewClient(personalent.Driver(drv))
	t.Cleanup(func() { client.Close() })

	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("schema create: %v", err)
	}

	return &personalCredentialStore{client: client}, client
}

// setupTeamStore creates a fresh SQLite-backed team Ent client with
// migrated schema.
func setupTeamStore(t *testing.T) (*teamCredentialStore, *teament.Client) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "team.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatalf("pragma: %v", err)
		}
	}

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := teament.NewClient(teament.Driver(drv))
	t.Cleanup(func() { client.Close() })

	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("schema create: %v", err)
	}

	return &teamCredentialStore{client: client}, client
}

// TestPersonalCredential_SetGet_Encrypted verifies that Set() encrypts data
// in the DB and Get() decrypts it correctly.
func TestPersonalCredential_SetGet_Encrypted(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	cs, client := setupPersonalStore(t)
	ctx := context.Background()

	cred := &store.Credential{
		Type:  store.CredBearer,
		Token: "ghp_test_token_12345",
	}

	// Set the credential.
	if err := cs.Set(ctx, "github", cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Verify the raw stored bytes are NOT plain JSON.
	raw, err := client.Credential.Query().
		Where(credential.NameEQ("github")).
		Only(ctx)
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}

	// Encrypted data should NOT be valid JSON.
	var probe store.Credential
	if json.Unmarshal(raw.Encrypted, &probe) == nil && probe.Token == cred.Token {
		t.Error("stored data is plain JSON — encryption not applied")
	}

	// Encrypted data should NOT start with '{'.
	if len(raw.Encrypted) > 0 && raw.Encrypted[0] == '{' {
		t.Error("stored data starts with '{' — looks like unencrypted JSON")
	}

	// Get should decrypt and return the original credential.
	got := cs.Get(ctx, "github")
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.Token != cred.Token {
		t.Errorf("Token = %q, want %q", got.Token, cred.Token)
	}
	if got.Type != store.CredBearer {
		t.Errorf("Type = %q, want %q", got.Type, store.CredBearer)
	}
}

// TestTeamCredential_SetGet_Encrypted verifies the team store also encrypts.
func TestTeamCredential_SetGet_Encrypted(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	cs, client := setupTeamStore(t)
	ctx := context.Background()

	cred := &store.Credential{
		Type:     store.CredBasic,
		Username: "admin",
		Password: "s3cr3t",
	}

	if err := cs.Set(ctx, "jira", cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Verify raw bytes are encrypted.
	raw, err := client.Credential.Query().
		Where(teamcredential.NameEQ("jira")).
		Only(ctx)
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}

	var probe store.Credential
	if json.Unmarshal(raw.Encrypted, &probe) == nil && probe.Password == "s3cr3t" {
		t.Error("stored data is plain JSON — encryption not applied")
	}

	// Get should decrypt correctly.
	got := cs.Get(ctx, "jira")
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.Username != "admin" || got.Password != "s3cr3t" {
		t.Errorf("got username=%q password=%q, want admin/s3cr3t", got.Username, got.Password)
	}
}

// TestCredential_Publish_PersonalToTeam verifies the publish flow:
// set in personal → get from personal → set in team → get from team.
func TestCredential_Publish_PersonalToTeam(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	personalCS, _ := setupPersonalStore(t)
	teamCS, _ := setupTeamStore(t)
	ctx := context.Background()

	original := &store.Credential{
		Type:  store.CredBearer,
		Token: "ghp_publish_test_token",
	}

	// 1. Save to personal store.
	if err := personalCS.Set(ctx, "github", original); err != nil {
		t.Fatalf("personal Set: %v", err)
	}

	// 2. Read from personal store (simulates what PublishCredentialHandler does).
	fromPersonal := personalCS.Get(ctx, "github")
	if fromPersonal == nil {
		t.Fatal("personal Get returned nil")
	}

	// 3. Write to team store (the "publish" action).
	if err := teamCS.Set(ctx, "github", fromPersonal); err != nil {
		t.Fatalf("team Set: %v", err)
	}

	// 4. Read from team store — should match original.
	fromTeam := teamCS.Get(ctx, "github")
	if fromTeam == nil {
		t.Fatal("team Get returned nil")
	}
	if fromTeam.Token != original.Token {
		t.Errorf("team Token = %q, want %q", fromTeam.Token, original.Token)
	}
	if fromTeam.Type != store.CredBearer {
		t.Errorf("team Type = %q, want %q", fromTeam.Type, store.CredBearer)
	}
}

// TestCredential_BackwardCompat_PlainJSON verifies that credentials stored
// as plain JSON (by the old entstore before encryption was added) are still
// readable.
func TestCredential_BackwardCompat_PlainJSON(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	cs, client := setupPersonalStore(t)
	ctx := context.Background()

	// Directly insert plain JSON (simulating old entstore behavior).
	plainJSON := []byte(`{"type":"bearer","token":"legacy_plain_token"}`)
	_, err := client.Credential.Create().
		SetName("legacy-cred").
		SetCredType("bearer").
		SetEncrypted(plainJSON).
		Save(ctx)
	if err != nil {
		t.Fatalf("insert plain JSON: %v", err)
	}

	// Get should still work (decryptSecret falls back to plaintext).
	got := cs.Get(ctx, "legacy-cred")
	if got == nil {
		t.Fatal("Get returned nil for plain JSON credential")
	}
	if got.Token != "legacy_plain_token" {
		t.Errorf("Token = %q, want %q", got.Token, "legacy_plain_token")
	}
}

// TestCredential_BackwardCompat_OldPgstoreEncrypted verifies that credentials
// stored by the old pgstore (AES-GCM encrypted with the master key) are
// readable by the new entstore.
func TestCredential_BackwardCompat_OldPgstoreEncrypted(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	cs, client := setupPersonalStore(t)
	ctx := context.Background()

	// Produce encrypted bytes the same way the old pgstore did.
	masterKey := loadMasterKey()
	if masterKey == nil {
		t.Fatal("loadMasterKey returned nil despite ASTONISH_MASTER_KEY being set")
	}

	originalJSON := []byte(`{"type":"bearer","token":"old_pgstore_token"}`)
	ciphertext, err := credentials.Encrypt(originalJSON, masterKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Directly insert the encrypted blob (simulating old pgstore data).
	_, err = client.Credential.Create().
		SetName("old-pgstore-cred").
		SetCredType("bearer").
		SetEncrypted(ciphertext).
		Save(ctx)
	if err != nil {
		t.Fatalf("insert encrypted: %v", err)
	}

	// Get should decrypt and return the credential.
	got := cs.Get(ctx, "old-pgstore-cred")
	if got == nil {
		t.Fatal("Get returned nil for old pgstore encrypted credential")
	}
	if got.Token != "old_pgstore_token" {
		t.Errorf("Token = %q, want %q", got.Token, "old_pgstore_token")
	}
}

// TestCredential_NoMasterKey verifies graceful degradation when no master key
// is configured: data is stored as plain JSON and can be read back.
func TestCredential_RawContentRoundTrip(t *testing.T) {
	cs, _ := setupPersonalStore(t)
	ctx := context.Background()

	content := "providers:\n  alpaca:\n    key: raw-secret-12345\n"
	cred := &store.Credential{
		Type:        store.CredRawContent,
		Content:     content,
		ContentType: "application/yaml",
	}
	if err := cs.Set(ctx, "providers-file", cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got := cs.Get(ctx, "providers-file")
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.Type != store.CredRawContent || got.Content != content || got.ContentType != "application/yaml" {
		t.Fatalf("Get = %#v", got)
	}
	_, _, err := cs.Resolve(ctx, "providers-file")
	if err == nil || !strings.Contains(err.Error(), "raw content") {
		t.Fatalf("Resolve error = %v, want raw content rejection", err)
	}
}

func TestCredential_NoMasterKey(t *testing.T) {
	// Ensure no master key is available via env or file.
	t.Setenv("ASTONISH_MASTER_KEY", "")
	// Point config dir to a temp dir with no .store_key file.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cs, client := setupPersonalStore(t)
	ctx := context.Background()

	cred := &store.Credential{
		Type:  store.CredAPIKey,
		Value: "sk-test-api-key",
	}

	if err := cs.Set(ctx, "openai", cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Without a master key, data should be stored as plain JSON.
	raw, err := client.Credential.Query().
		Where(credential.NameEQ("openai")).
		Only(ctx)
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}

	var probe store.Credential
	if err := json.Unmarshal(raw.Encrypted, &probe); err != nil {
		t.Errorf("stored data is not valid JSON (expected plain when no master key): %v", err)
	}
	if probe.Value != "sk-test-api-key" {
		t.Errorf("stored plain Value = %q, want %q", probe.Value, "sk-test-api-key")
	}

	// Get should still work.
	got := cs.Get(ctx, "openai")
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.Value != "sk-test-api-key" {
		t.Errorf("Value = %q, want %q", got.Value, "sk-test-api-key")
	}
}

// TestCredential_Secret_RoundTrip verifies SetSecret/GetSecret round-trip
// through the encrypted store.
func TestCredential_Secret_RoundTrip(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	cs, _ := setupPersonalStore(t)
	ctx := context.Background()

	if err := cs.SetSecret(ctx, "GH_TOKEN", "ghp_secret_value"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	got := cs.GetSecret(ctx, "GH_TOKEN")
	if got != "ghp_secret_value" {
		t.Errorf("GetSecret = %q, want %q", got, "ghp_secret_value")
	}
}

// TestCredential_Secret_Encrypted verifies that secrets are actually encrypted
// in the database.
func TestCredential_Secret_Encrypted(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	cs, client := setupPersonalStore(t)
	ctx := context.Background()

	if err := cs.SetSecret(ctx, "API_KEY", "super-secret-key"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	// The secret is stored with name "_secret:API_KEY".
	raw, err := client.Credential.Query().
		Where(credential.NameEQ("_secret:API_KEY")).
		Only(ctx)
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}

	// Should NOT be readable as plain JSON containing the secret.
	var probe store.Credential
	if json.Unmarshal(raw.Encrypted, &probe) == nil && probe.Value == "super-secret-key" {
		t.Error("secret stored as plain JSON — encryption not applied")
	}

	// But GetSecret should return it correctly.
	got := cs.GetSecret(ctx, "API_KEY")
	if got != "super-secret-key" {
		t.Errorf("GetSecret = %q, want %q", got, "super-secret-key")
	}
}

// TestCredential_List verifies that List() works correctly (it reads name/type
// columns, not the encrypted blob).
func TestCredential_List(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	cs, _ := setupPersonalStore(t)
	ctx := context.Background()

	creds := map[string]*store.Credential{
		"github": {Type: store.CredBearer, Token: "gh-token"},
		"jira":   {Type: store.CredBasic, Username: "u", Password: "p"},
	}
	for name, cred := range creds {
		if err := cs.Set(ctx, name, cred); err != nil {
			t.Fatalf("Set(%s): %v", name, err)
		}
	}

	list := cs.List(ctx)
	if len(list) != 2 {
		t.Fatalf("List returned %d items, want 2", len(list))
	}
	if list["github"] != store.CredBearer {
		t.Errorf("github type = %q, want %q", list["github"], store.CredBearer)
	}
	if list["jira"] != store.CredBasic {
		t.Errorf("jira type = %q, want %q", list["jira"], store.CredBasic)
	}
}

// TestCredential_Update verifies that updating an existing credential
// re-encrypts with fresh ciphertext.
func TestCredential_Update(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	cs, client := setupPersonalStore(t)
	ctx := context.Background()

	// Create initial credential.
	cred1 := &store.Credential{Type: store.CredBearer, Token: "token-v1"}
	if err := cs.Set(ctx, "github", cred1); err != nil {
		t.Fatalf("Set v1: %v", err)
	}

	// Read the raw ciphertext.
	raw1, _ := client.Credential.Query().Where(credential.NameEQ("github")).Only(ctx)
	cipher1 := make([]byte, len(raw1.Encrypted))
	copy(cipher1, raw1.Encrypted)

	// Update the credential.
	cred2 := &store.Credential{Type: store.CredBearer, Token: "token-v2"}
	if err := cs.Set(ctx, "github", cred2); err != nil {
		t.Fatalf("Set v2: %v", err)
	}

	// Raw ciphertext should be different (different nonce).
	raw2, _ := client.Credential.Query().Where(credential.NameEQ("github")).Only(ctx)
	if string(raw2.Encrypted) == string(cipher1) {
		t.Error("ciphertext unchanged after update — nonce should differ")
	}

	// Get should return v2.
	got := cs.Get(ctx, "github")
	if got == nil {
		t.Fatal("Get returned nil after update")
	}
	if got.Token != "token-v2" {
		t.Errorf("Token = %q, want %q", got.Token, "token-v2")
	}
}

// TestCredential_DEK_EnvelopeEncryption verifies the envelope encryption path:
// data encrypted with a per-org DEK is decrypted correctly when credKey is set.
func TestCredential_DEK_EnvelopeEncryption(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	cs, client := setupPersonalStore(t)
	ctx := context.Background()

	// Generate a DEK (like getOrCreateCredentialKey does).
	dek, err := credentials.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Set credKey on the store (simulating what tenant_router does).
	cs.credKey = dek

	// Set a credential — should encrypt with DEK.
	cred := &store.Credential{Type: store.CredBearer, Token: "dek-encrypted-token"}
	if err := cs.Set(ctx, "test-dek", cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Verify stored data is NOT decryptable with master key alone.
	raw, err := client.Credential.Query().Where(credential.NameEQ("test-dek")).Only(ctx)
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}

	masterKey := loadMasterKey()
	_, decErr := credentials.Decrypt(raw.Encrypted, masterKey)
	if decErr == nil {
		t.Error("data decrypted with master key — should only decrypt with DEK")
	}

	// But should decrypt fine with DEK.
	plaintext, err := credentials.Decrypt(raw.Encrypted, dek)
	if err != nil {
		t.Fatalf("decrypt with DEK failed: %v", err)
	}
	var got store.Credential
	if err := json.Unmarshal(plaintext, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Token != "dek-encrypted-token" {
		t.Errorf("Token = %q, want %q", got.Token, "dek-encrypted-token")
	}

	// And Get() should work (it uses credKey internally).
	gotCred := cs.Get(ctx, "test-dek")
	if gotCred == nil {
		t.Fatal("Get returned nil")
	}
	if gotCred.Token != "dek-encrypted-token" {
		t.Errorf("Get Token = %q, want %q", gotCred.Token, "dek-encrypted-token")
	}
}

// TestCredential_DEK_FallbackToMasterKey verifies that data encrypted with
// the master key directly (not DEK) is still readable when credKey is set
// (the DEK attempt fails, then master key fallback succeeds).
func TestCredential_DEK_FallbackToMasterKey(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	cs, client := setupPersonalStore(t)
	ctx := context.Background()

	// Generate a DEK.
	dek, err := credentials.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cs.credKey = dek

	// Insert data encrypted with master key directly (simulating old code path).
	masterKey := loadMasterKey()
	originalJSON := []byte(`{"type":"bearer","token":"master-key-encrypted"}`)
	ciphertext, err := credentials.Encrypt(originalJSON, masterKey)
	if err != nil {
		t.Fatalf("Encrypt with master key: %v", err)
	}

	_, err = client.Credential.Create().
		SetName("old-master-key-cred").
		SetCredType("bearer").
		SetEncrypted(ciphertext).
		Save(ctx)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Get should succeed via fallback (DEK fails → master key succeeds).
	got := cs.Get(ctx, "old-master-key-cred")
	if got == nil {
		t.Fatal("Get returned nil — fallback to master key failed")
	}
	if got.Token != "master-key-encrypted" {
		t.Errorf("Token = %q, want %q", got.Token, "master-key-encrypted")
	}
}

// TestCredential_DEK_OldPgstoreData verifies the exact production scenario:
// data encrypted with a per-org DEK (by old pgstore) is decrypted correctly.
func TestCredential_DEK_OldPgstoreData(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	cs, client := setupPersonalStore(t)
	ctx := context.Background()

	// Simulate old pgstore's envelope encryption:
	// 1. Generate a DEK
	dek, err := credentials.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// 2. Encrypt credential data with the DEK (what old pgstore did).
	originalJSON := []byte(`{"type":"api_key","value":"sk-old-pgstore-key-12345"}`)
	ciphertext, err := credentials.Encrypt(originalJSON, dek)
	if err != nil {
		t.Fatalf("Encrypt with DEK: %v", err)
	}

	// 3. Insert the DEK-encrypted data into the DB.
	_, err = client.Credential.Create().
		SetName("old-pgstore-dek").
		SetCredType("api_key").
		SetEncrypted(ciphertext).
		Save(ctx)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// 4. Set the credKey on our store (simulating DEK loaded from org_encryption_keys).
	cs.credKey = dek

	// Get should decrypt using the DEK.
	got := cs.Get(ctx, "old-pgstore-dek")
	if got == nil {
		t.Fatal("Get returned nil — DEK decryption failed for old pgstore data")
	}
	if got.Value != "sk-old-pgstore-key-12345" {
		t.Errorf("Value = %q, want %q", got.Value, "sk-old-pgstore-key-12345")
	}
}

// TestPersonalCredential_Resolve_OAuthFetcherNotNil verifies that Resolve()
// passes a non-nil OAuthTokenFetcher for OAuth credential types (so they no
// longer produce "requires a token fetcher" errors).
func TestPersonalCredential_Resolve_OAuthFetcherNotNil(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	cs, _ := setupPersonalStore(t)
	ctx := context.Background()

	// Store a bearer credential (simpler, doesn't need network access).
	bearerCred := &store.Credential{
		Type:  store.CredBearer,
		Token: "test-token-123",
	}
	if err := cs.Set(ctx, "my-bearer", bearerCred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Resolve should return the correct Authorization header.
	headerKey, headerValue, err := cs.Resolve(ctx, "my-bearer")
	if err != nil {
		t.Fatalf("Resolve bearer: %v", err)
	}
	if headerKey != "Authorization" {
		t.Errorf("headerKey = %q, want %q", headerKey, "Authorization")
	}
	if headerValue != "Bearer test-token-123" {
		t.Errorf("headerValue = %q, want %q", headerValue, "Bearer test-token-123")
	}

	// Store an OAuth client_credentials credential.
	// We can't test actual token fetching without a server, but we can verify
	// that Resolve() returns a proper OAuth error (not "requires a token fetcher").
	oauthCred := &store.Credential{
		Type:         store.CredOAuthClientCreds,
		AuthURL:      "http://127.0.0.1:1/oauth/token", // intentionally unreachable
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
	}
	if err := cs.Set(ctx, "my-oauth", oauthCred); err != nil {
		t.Fatalf("Set oauth: %v", err)
	}

	// Resolve should attempt the OAuth flow (and fail with a network error),
	// NOT with "requires a token fetcher".
	_, _, err = cs.Resolve(ctx, "my-oauth")
	if err == nil {
		t.Fatal("expected error from unreachable OAuth server")
	}
	errMsg := err.Error()
	if contains(errMsg, "requires a token fetcher") {
		t.Errorf("Resolve should not return 'requires a token fetcher' error, got: %v", err)
	}
	// Should be a network/connection error instead.
	if !contains(errMsg, "token request") && !contains(errMsg, "OAuth") {
		t.Errorf("expected network/OAuth error, got: %v", err)
	}
}

// TestTeamCredential_Resolve_OAuthFetcherNotNil verifies the same for team store.
func TestTeamCredential_Resolve_OAuthFetcherNotNil(t *testing.T) {
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)

	cs, _ := setupTeamStore(t)
	ctx := context.Background()

	// Store an OAuth client_credentials credential.
	oauthCred := &store.Credential{
		Type:         store.CredOAuthClientCreds,
		AuthURL:      "http://127.0.0.1:1/oauth/token",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
	}
	if err := cs.Set(ctx, "my-oauth", oauthCred); err != nil {
		t.Fatalf("Set oauth: %v", err)
	}

	_, _, err := cs.Resolve(ctx, "my-oauth")
	if err == nil {
		t.Fatal("expected error from unreachable OAuth server")
	}
	errMsg := err.Error()
	if contains(errMsg, "requires a token fetcher") {
		t.Errorf("Resolve should not return 'requires a token fetcher' error, got: %v", err)
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
