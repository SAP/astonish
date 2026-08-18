package entstore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	entschema "entgo.io/ent/dialect/sql/schema"

	orgent "github.com/SAP/astonish/ent/org"
	"github.com/SAP/astonish/ent/org/orgencryptionkey"
	personalent "github.com/SAP/astonish/ent/personal"
	"github.com/SAP/astonish/ent/platform/organization"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/pgutil"
)

// --- store.TenantRouter implementation ---

func (s *Store) ForOrg(orgSlug string) (store.OrgDataStore, error) {
	// Fast path: check cache.
	if cached, ok := s.orgClients.Load(orgSlug); ok {
		return cached.(*orgDataStore), nil
	}

	// Serialize concurrent opens for the same org slug.
	result, err, _ := s.orgFlight.Do(orgSlug, func() (interface{}, error) {
		// Double-check cache (another goroutine may have populated it).
		if cached, ok := s.orgClients.Load(orgSlug); ok {
			return cached.(*orgDataStore), nil
		}

		// Open connection to the org database.
		orgClient, orgDB, err := s.openOrgDB(orgSlug)
		if err != nil {
			return nil, fmt.Errorf("entstore: ForOrg(%s): %w", orgSlug, err)
		}

		ds := &orgDataStore{
			orgSlug:     orgSlug,
			client:      orgClient,
			db:          orgDB,
			dialect:     s.dialect,
			embedFunc:   s.embedFunc,
			parentStore: s,
			teams:       make(map[string]*teamDataStore),
			users:       make(map[string]*personalDataStore),
		}

		s.orgClients.Store(orgSlug, ds)
		return ds, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*orgDataStore), nil
}

func (s *Store) ProvisionOrg(ctx context.Context, orgID, slug string) error {
	if err := validateSlug(slug); err != nil {
		return fmt.Errorf("provision org: %w", err)
	}
	switch s.dialect {
	case DialectPostgres:
		return s.provisionOrgPostgres(ctx, slug)
	case DialectSQLite:
		return s.provisionOrgSQLite(ctx, slug)
	default:
		return fmt.Errorf("entstore: unsupported dialect: %s", s.dialect)
	}
}

func (s *Store) DecommissionOrg(ctx context.Context, orgSlug string) error {
	if err := validateSlug(orgSlug); err != nil {
		return fmt.Errorf("decommission org: %w", err)
	}

	// Remove from cache and close.
	if cached, ok := s.orgClients.LoadAndDelete(orgSlug); ok {
		if ds, ok := cached.(*orgDataStore); ok {
			ds.Close()
		}
	}

	switch s.dialect {
	case DialectPostgres:
		return s.decommissionOrgPostgres(ctx, orgSlug)
	case DialectSQLite:
		return s.decommissionOrgSQLite(ctx, orgSlug)
	default:
		return fmt.Errorf("entstore: unsupported dialect: %s", s.dialect)
	}
}

// --- Postgres helpers ---

// sanitizeSlug replaces hyphens with underscores to match the old pgstore
// database naming convention (PG identifiers cannot contain hyphens unquoted).
func sanitizeSlug(slug string) string {
	return strings.ReplaceAll(slug, "-", "_")
}

// orgDBName returns the database name for an org in PG mode.
// Matches old pgstore convention: astonish_{suffix}_{slug_sanitized}
func (s *Store) orgDBName(slug string) string {
	safe := sanitizeSlug(slug)
	if s.instanceSuffix != "" {
		return fmt.Sprintf("astonish_%s_%s", s.instanceSuffix, safe)
	}
	return fmt.Sprintf("astonish_%s", safe)
}

// teamSchemaName returns the PG schema name for a team within the org database.
// Matches old pgstore convention: team_{teamSlug}
func teamSchemaName(teamSlug string) string {
	return "team_" + teamSlug
}

// personalSchemaName returns the PG schema name for a user's personal space
// within the org database. Matches old pgstore convention: personal_{uuid_underscored}
func personalSchemaName(userID string) string {
	return "personal_" + strings.ReplaceAll(userID, "-", "_")
}

// deriveDSN replaces the database name in the platform DSN.
func (s *Store) deriveDSN(dbName string) (string, error) {
	u, err := url.Parse(s.platformDSN)
	if err != nil {
		return "", fmt.Errorf("parse platform DSN: %w", err)
	}
	u.Path = "/" + dbName
	return u.String(), nil
}

// deriveSchemaAwareDSN returns a DSN targeting a specific PG schema within a database.
// It sets search_path=<schema>,public so the connection targets the given schema.
// The schema name is double-quoted as a PG identifier to handle names containing
// characters (e.g. hyphens) that are invalid in bare identifiers.
func (s *Store) deriveSchemaAwareDSN(dbName, schemaName string) (string, error) {
	u, err := url.Parse(s.platformDSN)
	if err != nil {
		return "", fmt.Errorf("parse platform DSN: %w", err)
	}
	u.Path = "/" + dbName
	q := u.Query()
	quoted := `"` + strings.ReplaceAll(schemaName, `"`, `""`) + `"`
	q.Set("search_path", quoted+",public")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *Store) openOrgDB(slug string) (*orgent.Client, *sql.DB, error) {
	if err := validateSlug(slug); err != nil {
		return nil, nil, fmt.Errorf("open org db: %w", err)
	}
	slug = filepath.Base(slug) // sanitize for path safety (also satisfies static analysis)
	switch s.dialect {
	case DialectPostgres:
		dbName := s.orgDBName(slug)
		dsn, err := s.deriveDSN(dbName)
		if err != nil {
			return nil, nil, err
		}
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, nil, fmt.Errorf("open org pg db %s: %w", dbName, err)
		}
		// Apply pool limits to prevent connection exhaustion.
		db.SetMaxOpenConns(s.maxOpenConns)
		db.SetMaxIdleConns(s.maxIdleConns)
		db.SetConnMaxLifetime(s.connMaxLifetime)
		drv := entsql.OpenDB(dialect.Postgres, db)
		client := orgent.NewClient(orgent.Driver(drv))

		// Ensure pgvector extension exists before auto-migration (needed for
		// vector(384) columns). Idempotent — harmless if already present.
		if _, err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
			slog.Warn("openOrgDB: could not ensure pgvector extension",
				"org", slug, "error", err)
		}

		// Migrate legacy pgstore table schemas to match Ent's expectations
		// (e.g. team_memberships composite PK → UUID id PK). Idempotent.
		migrateOrgLegacySchema(context.Background(), db)

		// Auto-migrate: create any missing tables (e.g. team_memberships added
		// after the org was originally provisioned). Skip ModifyColumn to
		// tolerate SERIAL-vs-IDENTITY differences on legacy tables.
		if err := client.Schema.Create(context.Background(),
			entschema.WithSkipChanges(entschema.ModifyColumn),
		); err != nil {
			db.Close()
			return nil, nil, fmt.Errorf("auto-migrate org pg db %s: %w", dbName, err)
		}

		return client, db, nil

	case DialectSQLite:
		dbPath := filepath.Join(s.dataDir, "orgs", slug, "org.db")
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("org database not found: %s", dbPath)
		}
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return nil, nil, fmt.Errorf("open org sqlite db %s: %w", dbPath, err)
		}
		// SQLite: single writer, serialize all access to prevent SQLITE_BUSY.
		db.SetMaxOpenConns(1)
		// Enable WAL and foreign keys.
		for _, pragma := range []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=10000",
		} {
			if _, err := db.Exec(pragma); err != nil {
				db.Close()
				return nil, nil, fmt.Errorf("pragma: %w", err)
			}
		}
		drv := entsql.OpenDB(dialect.SQLite, db)
		client := orgent.NewClient(orgent.Driver(drv))

		// Pre-migrate: fix legacy table schemas (add missing NOT NULL columns,
		// restructure composite PKs) so that Ent's Schema.Create succeeds.
		migrateOrgSQLiteLegacy(db)

		// Auto-migrate: create missing tables and add missing columns to existing
		// tables. SQLite doesn't have the SERIAL/IDENTITY issue that PostgreSQL has,
		// so we can let Ent fully manage the schema here.
		if err := client.Schema.Create(context.Background()); err != nil {
			db.Close()
			return nil, nil, fmt.Errorf("auto-migrate org sqlite db %s: %w", dbPath, err)
		}

		return client, db, nil

	default:
		return nil, nil, fmt.Errorf("unsupported dialect: %s", s.dialect)
	}
}

func (s *Store) provisionOrgPostgres(ctx context.Context, slug string) error {
	dbName := s.orgDBName(slug)
	// Quote the database name for safety.
	quoted := fmt.Sprintf(`"%s"`, strings.ReplaceAll(dbName, `"`, `""`))

	// Create the database on the platform connection.
	if _, err := s.platformDB.ExecContext(ctx, "CREATE DATABASE "+quoted); err != nil { // CodeQL[go/sql-injection]: slug is validated by validateSlug allowlist, dbName is properly quoted
		// Ignore "already exists" errors.
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("create database %s: %w", dbName, err)
		}
	}

	// Open connection to the new database and run Ent auto-migration.
	client, db, err := s.openOrgDB(slug)
	if err != nil {
		return fmt.Errorf("open new org db: %w", err)
	}
	defer db.Close()
	defer client.Close()

	// Pre-create the pgvector extension so Schema.Create can use vector(384) columns.
	if _, err := db.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		return fmt.Errorf("create vector extension in org db: %w", err)
	}

	if err := client.Schema.Create(ctx); err != nil {
		return fmt.Errorf("migrate org schema: %w", err)
	}

	// Apply PG-specific extras (extensions, indexes, triggers, RLS).
	if err := s.applyPGExtras(ctx, ScopeOrg, db); err != nil {
		return fmt.Errorf("apply org pg extras: %w", err)
	}

	// Apply SQLite-specific extras (FTS5 virtual tables, triggers).
	if err := s.applySQLiteExtras(ctx, ScopeOrg, db); err != nil {
		return fmt.Errorf("apply org sqlite extras: %w", err)
	}

	// Apply grants for app role.
	if err := pgutil.ApplyGrants(ctx, db, "org"); err != nil {
		return fmt.Errorf("apply org grants: %w", err)
	}

	// Update the org's db_name field in the platform database.
	if _, err := s.platformClient.Organization.Update().
		Where(organization.SlugEQ(slug)).
		SetDbName(dbName).
		Save(ctx); err != nil {
		slog.Warn("failed to update org db_name", "slug", slug, "error", err)
	}

	slog.Info("provisioned org database", "slug", slug, "db", dbName)
	return nil
}

func (s *Store) provisionOrgSQLite(ctx context.Context, slug string) error {
	slug = filepath.Base(slug) // sanitize for path safety (also satisfies static analysis)
	orgDir := filepath.Join(s.dataDir, "orgs", slug)
	if err := os.MkdirAll(orgDir, 0750); err != nil {
		return fmt.Errorf("create org directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(orgDir, "teams"), 0750); err != nil {
		return fmt.Errorf("create teams directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(orgDir, "personal"), 0750); err != nil {
		return fmt.Errorf("create personal directory: %w", err)
	}

	dbPath := filepath.Join(orgDir, "org.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("create org database: %w", err)
	}
	defer db.Close()

	// Enable WAL and foreign keys.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("pragma: %w", err)
		}
	}

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := orgent.NewClient(orgent.Driver(drv))
	defer client.Close()

	if err := client.Schema.Create(ctx); err != nil {
		return fmt.Errorf("migrate org schema: %w", err)
	}

	slog.Info("provisioned org database (sqlite)", "slug", slug, "path", dbPath)
	return nil
}

func (s *Store) decommissionOrgPostgres(ctx context.Context, orgSlug string) error {
	dbName := s.orgDBName(orgSlug)
	quoted := fmt.Sprintf(`"%s"`, strings.ReplaceAll(dbName, `"`, `""`))

	// Terminate existing connections.
	_, _ = s.platformDB.ExecContext(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, dbName)

	// Drop the database.
	if _, err := s.platformDB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoted); err != nil { // CodeQL[go/sql-injection]: slug is validated by validateSlug allowlist, dbName is properly quoted
		return fmt.Errorf("drop database %s: %w", dbName, err)
	}

	slog.Info("decommissioned org database", "slug", orgSlug, "db", dbName)
	return nil
}

func (s *Store) decommissionOrgSQLite(_ context.Context, orgSlug string) error {
	orgDir := filepath.Join(s.dataDir, "orgs", orgSlug)
	if err := os.RemoveAll(orgDir); err != nil {
		return fmt.Errorf("remove org directory: %w", err)
	}
	slog.Info("decommissioned org (sqlite)", "slug", orgSlug)
	return nil
}

// ===========================================================================
// orgDataStore implements store.OrgDataStore
// ===========================================================================

type orgDataStore struct {
	orgSlug   string
	client    *orgent.Client
	db        *sql.DB
	dialect   Dialect
	embedFunc store.EmbedFunc

	parentStore *Store

	mu    sync.RWMutex
	teams map[string]*teamDataStore
	users map[string]*personalDataStore

	credKeyOnce sync.Once
	credKey     []byte // cached per-org credential DEK
}

// getOrCreateCredentialKey returns the per-org Data Encryption Key (DEK) used
// for credential encryption. The DEK is stored encrypted (by the master key)
// in the org_encryption_keys table. This replicates the old pgstore's envelope
// encryption scheme: master key encrypts the DEK, DEK encrypts credential data.
//
// If no DEK exists yet (new org), one is generated and stored.
// The result is cached for the lifetime of the orgDataStore.
// Returns nil if no master key is configured (encryption disabled).
func (o *orgDataStore) getOrCreateCredentialKey() []byte {
	o.credKeyOnce.Do(func() {
		masterKey := loadMasterKey()
		if masterKey == nil {
			return // encryption disabled
		}

		ctx := context.Background()

		// Try to load existing DEK via Ent (fast path).
		ek, err := o.client.OrgEncryptionKey.Query().
			Where(orgencryptionkey.KeyNameEQ("credential_key")).
			Only(ctx)
		if err == nil {
			// Decrypt the DEK with the master key.
			dek, decErr := credentials.Decrypt(ek.KeyData, masterKey)
			if decErr != nil {
				slog.Error("failed to decrypt org credential key", "org", o.orgSlug, "error", decErr)
				return
			}
			o.credKey = dek
			return
		}

		// Ent query failed — could be "not found" or a schema mismatch (e.g.,
		// legacy table with NULL/invalid id column that Ent can't scan).
		// Try raw SQL as fallback to check if the key actually exists.
		var keyData []byte
		rawErr := o.db.QueryRowContext(ctx,
			`SELECT key_data FROM org_encryption_keys WHERE key_name = 'credential_key' LIMIT 1`,
		).Scan(&keyData)
		if rawErr == nil && len(keyData) > 0 {
			// Key exists in DB but Ent couldn't read it (schema mismatch).
			dek, decErr := credentials.Decrypt(keyData, masterKey)
			if decErr != nil {
				slog.Error("failed to decrypt org credential key (raw fallback)", "org", o.orgSlug, "error", decErr)
				return
			}
			o.credKey = dek
			return
		}

		// DEK truly doesn't exist — generate one and store it.
		dek, genErr := credentials.GenerateKey()
		if genErr != nil {
			slog.Error("failed to generate org credential key", "org", o.orgSlug, "error", genErr)
			return
		}

		encryptedDEK, encErr := credentials.Encrypt(dek, masterKey)
		if encErr != nil {
			slog.Error("failed to encrypt org credential key", "org", o.orgSlug, "error", encErr)
			return
		}

		_, storeErr := o.client.OrgEncryptionKey.Create().
			SetKeyName("credential_key").
			SetKeyData(encryptedDEK).
			Save(ctx)
		if storeErr != nil {
			// Handle race condition: another instance may have inserted concurrently.
			// Retry loading via raw SQL.
			var keyData2 []byte
			if rawErr2 := o.db.QueryRowContext(ctx,
				`SELECT key_data FROM org_encryption_keys WHERE key_name = 'credential_key' LIMIT 1`,
			).Scan(&keyData2); rawErr2 == nil && len(keyData2) > 0 {
				if dek2, decErr := credentials.Decrypt(keyData2, masterKey); decErr == nil {
					o.credKey = dek2
					return
				}
			}
			slog.Error("failed to store org credential key", "org", o.orgSlug, "error", storeErr)
			return
		}

		o.credKey = dek
	})
	return o.credKey
}

func (o *orgDataStore) ForTeam(teamSlug string) store.TeamDataStore {
	o.mu.RLock()
	if ts, ok := o.teams[teamSlug]; ok {
		o.mu.RUnlock()
		return ts
	}
	o.mu.RUnlock()

	// Acquire write lock and double-check to prevent duplicate opens.
	o.mu.Lock()
	if ts, ok := o.teams[teamSlug]; ok {
		o.mu.Unlock()
		return ts
	}
	o.mu.Unlock()

	ts, err := o.openTeamDB(teamSlug)
	if err != nil {
		slog.Error("open team database", "team", teamSlug, "org", o.orgSlug, "error", err)
		return nil
	}

	o.mu.Lock()
	// Final check: another goroutine may have inserted while we opened.
	if existing, ok := o.teams[teamSlug]; ok {
		o.mu.Unlock()
		// Close the duplicate we just opened.
		if ts.db != nil {
			ts.db.Close()
		}
		return existing
	}
	o.teams[teamSlug] = ts
	o.mu.Unlock()
	return ts
}

func (o *orgDataStore) ForUser(userID string) store.PersonalDataStore {
	o.mu.RLock()
	if ps, ok := o.users[userID]; ok {
		o.mu.RUnlock()
		return ps
	}
	o.mu.RUnlock()

	// Acquire write lock and double-check to prevent duplicate opens.
	o.mu.Lock()
	if ps, ok := o.users[userID]; ok {
		o.mu.Unlock()
		return ps
	}
	o.mu.Unlock()

	ps, err := o.openPersonalDB(userID)
	if err != nil {
		slog.Error("open personal database", "user", userID, "org", o.orgSlug, "error", err)
		return nil
	}

	o.mu.Lock()
	// Final check: another goroutine may have inserted while we opened.
	if existing, ok := o.users[userID]; ok {
		o.mu.Unlock()
		// Close the duplicate we just opened.
		if ps.db != nil {
			ps.db.Close()
		}
		return existing
	}
	o.users[userID] = ps
	o.mu.Unlock()
	return ps
}

func (o *orgDataStore) OrgMemories() store.MemoryStore {
	ms := &orgMemoryStore{
		client:    o.client,
		db:        o.db,
		dialect:   o.dialect,
		embedFunc: o.embedFunc,
		table:     "org_memories",
	}
	if ms.dialect == DialectSQLite {
		ms.vecIndex = newVectorIndex()
		ms.ftsTable = "org_memories_fts"
	}
	return ms
}

func (o *orgDataStore) OrgSkills() store.SkillStore {
	return &orgSkillStore{client: o.client}
}

func (o *orgDataStore) OrgMCPServers() store.MCPServerStore {
	return &orgMCPServerStore{client: o.client}
}

func (o *orgDataStore) OrgA2AAgents() store.A2AAgentStore {
	return &orgA2AAgentStore{client: o.client}
}

func (o *orgDataStore) OrgNetworkPolicies() store.NetworkPolicyStore {
	return &orgNetworkPolicyStore{client: o.client}
}

func (o *orgDataStore) OrgApps() store.AppStore {
	return &orgAppStore{client: o.client}
}

func (o *orgDataStore) OrgAudit() store.AuditStore {
	return &orgAuditStore{client: o.client}
}

func (o *orgDataStore) Teams() store.TeamManagementStore {
	return &orgTeamStore{client: o.client}
}

func (o *orgDataStore) ProvisionTeam(ctx context.Context, slug string) error {
	if err := validateSlug(slug); err != nil {
		return fmt.Errorf("provision team: %w", err)
	}
	switch o.dialect {
	case DialectPostgres:
		schemaName := teamSchemaName(slug)
		quoted := fmt.Sprintf(`"%s"`, strings.ReplaceAll(schemaName, `"`, `""`))

		// Create the PG schema within the org database.
		if _, err := o.db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+quoted); err != nil { // CodeQL[go/sql-injection]: slug is validated by validateSlug allowlist, schemaName is properly quoted
			return fmt.Errorf("create team schema %s: %w", schemaName, err)
		}

		client, db, err := o.openTeamDBBySchema(schemaName)
		if err != nil {
			return fmt.Errorf("open new team schema: %w", err)
		}
		defer db.Close()
		defer client.Close()

		// Ensure pgvector extension exists (database-wide, idempotent).
		// Required for vector(384) columns in memories table.
		if _, err := db.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
			slog.Warn("ProvisionTeam: could not ensure pgvector extension", "team", slug, "error", err)
		}

		if err := client.Schema.Create(ctx); err != nil {
			return fmt.Errorf("migrate team schema: %w", err)
		}

		// Apply PG-specific extras (indexes, triggers) — non-fatal.
		if err := o.parentStore.applyPGExtras(ctx, ScopeTeam, db); err != nil {
			slog.Warn("ProvisionTeam: pg extras failed (non-fatal)", "team", slug, "error", err)
		}

		// Apply SQLite-specific extras (FTS5 virtual tables, triggers).
		if err := o.parentStore.applySQLiteExtras(ctx, ScopeTeam, db); err != nil {
			slog.Warn("ProvisionTeam: sqlite extras failed (non-fatal)", "team", slug, "error", err)
		}

		// Apply grants for app role.
		if err := pgutil.ApplyGrants(ctx, db, "team"); err != nil {
			slog.Warn("ProvisionTeam: grants failed (non-fatal)", "team", slug, "error", err)
		}

		slog.Info("provisioned team schema", "org", o.orgSlug, "team", slug, "schema", schemaName)

	case DialectSQLite:
		dbPath := filepath.Join(o.parentStore.dataDir, "orgs", o.orgSlug, "teams", slug+".db")
		if err := os.MkdirAll(filepath.Dir(dbPath), 0750); err != nil {
			return fmt.Errorf("create team directory: %w", err)
		}
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return fmt.Errorf("create team database: %w", err)
		}
		defer db.Close()

		for _, pragma := range []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=5000",
		} {
			if _, err := db.ExecContext(ctx, pragma); err != nil {
				return fmt.Errorf("pragma: %w", err)
			}
		}

		drv := entsql.OpenDB(dialect.SQLite, db)
		client := teament.NewClient(teament.Driver(drv))
		defer client.Close()

		if err := client.Schema.Create(ctx); err != nil {
			return fmt.Errorf("migrate team schema: %w", err)
		}
		slog.Info("provisioned team database (sqlite)", "org", o.orgSlug, "team", slug)
	}
	return nil
}

func (o *orgDataStore) DropTeamSchema(ctx context.Context, slug string) error {
	if err := validateSlug(slug); err != nil {
		return fmt.Errorf("drop team schema: %w", err)
	}
	switch o.dialect {
	case DialectPostgres:
		schemaName := teamSchemaName(slug)
		quoted := fmt.Sprintf(`"%s"`, strings.ReplaceAll(schemaName, `"`, `""`))

		// Close any cached client for this team schema.
		o.mu.Lock()
		if cached, ok := o.teams[slug]; ok {
			cached.client.Close()
			cached.db.Close()
			delete(o.teams, slug)
		}
		o.mu.Unlock()

		if _, err := o.db.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE"); err != nil { // CodeQL[go/sql-injection]: slug is validated by validateSlug allowlist, schemaName is properly quoted
			return fmt.Errorf("drop team schema %s: %w", schemaName, err)
		}
		slog.Info("dropped team schema", "org", o.orgSlug, "team", slug, "schema", schemaName)

	case DialectSQLite:
		// Close any cached client for this team.
		o.mu.Lock()
		if cached, ok := o.teams[slug]; ok {
			cached.client.Close()
			cached.db.Close()
			delete(o.teams, slug)
		}
		o.mu.Unlock()

		dbPath := filepath.Join(o.parentStore.dataDir, "orgs", o.orgSlug, "teams", slug+".db")
		if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove team database file: %w", err)
		}
		// Also remove WAL/SHM files if present.
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")
		slog.Info("dropped team database (sqlite)", "org", o.orgSlug, "team", slug)
	}
	return nil
}

func (o *orgDataStore) ProvisionPersonalSchema(ctx context.Context, userID string) error {
	if err := validateUserID(userID); err != nil {
		return fmt.Errorf("provision personal schema: %w", err)
	}
	switch o.dialect {
	case DialectPostgres:
		schemaName := personalSchemaName(userID)
		quoted := fmt.Sprintf(`"%s"`, strings.ReplaceAll(schemaName, `"`, `""`))

		// Create the PG schema within the org database.
		if _, err := o.db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+quoted); err != nil { // CodeQL[go/sql-injection]: userID is validated by validateUserID allowlist, schemaName is properly quoted
			return fmt.Errorf("create personal schema %s: %w", schemaName, err)
		}

		client, db, err := o.openPersonalDBBySchema(schemaName)
		if err != nil {
			return fmt.Errorf("open new personal schema: %w", err)
		}
		defer db.Close()
		defer client.Close()

		// Ensure pgvector extension exists (database-wide, idempotent).
		// Required for vector(384) columns in memories table.
		if _, err := db.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
			slog.Warn("ProvisionPersonalSchema: could not ensure pgvector extension", "user", userID, "error", err)
		}

		if err := client.Schema.Create(ctx); err != nil {
			return fmt.Errorf("migrate personal schema: %w", err)
		}

		// Apply PG-specific extras (indexes, triggers) — non-fatal.
		if err := o.parentStore.applyPGExtras(ctx, ScopePersonal, db); err != nil {
			slog.Warn("ProvisionPersonalSchema: pg extras failed (non-fatal)", "user", userID, "error", err)
		}

		// Apply SQLite-specific extras (FTS5 virtual tables, triggers).
		if err := o.parentStore.applySQLiteExtras(ctx, ScopePersonal, db); err != nil {
			slog.Warn("ProvisionPersonalSchema: sqlite extras failed (non-fatal)", "user", userID, "error", err)
		}

		// Apply grants for app role.
		if err := pgutil.ApplyGrants(ctx, db, "personal"); err != nil {
			slog.Warn("ProvisionPersonalSchema: grants failed (non-fatal)", "user", userID, "error", err)
		}

		slog.Info("provisioned personal schema", "org", o.orgSlug, "user", userID, "schema", schemaName)

	case DialectSQLite:
		safeID := strings.ReplaceAll(userID, "/", "_")
		dbPath := filepath.Join(o.parentStore.dataDir, "orgs", o.orgSlug, "personal", safeID+".db")
		if err := os.MkdirAll(filepath.Dir(dbPath), 0750); err != nil {
			return fmt.Errorf("create personal directory: %w", err)
		}
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return fmt.Errorf("create personal database: %w", err)
		}
		defer db.Close()

		for _, pragma := range []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=5000",
		} {
			if _, err := db.ExecContext(ctx, pragma); err != nil {
				return fmt.Errorf("pragma: %w", err)
			}
		}

		drv := entsql.OpenDB(dialect.SQLite, db)
		client := personalent.NewClient(personalent.Driver(drv))
		defer client.Close()

		if err := client.Schema.Create(ctx); err != nil {
			return fmt.Errorf("migrate personal schema: %w", err)
		}
		slog.Info("provisioned personal database (sqlite)", "org", o.orgSlug, "user", userID)
	}
	return nil
}

func (o *orgDataStore) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Close all team stores.
	for _, ts := range o.teams {
		if ts.db != nil {
			ts.db.Close()
		}
		if ts.client != nil {
			ts.client.Close()
		}
	}
	o.teams = make(map[string]*teamDataStore)

	// Close all personal stores.
	for _, ps := range o.users {
		if ps.db != nil {
			ps.db.Close()
		}
		if ps.client != nil {
			ps.client.Close()
		}
	}
	o.users = make(map[string]*personalDataStore)

	// Close org client/db.
	if o.client != nil {
		o.client.Close()
	}
	if o.db != nil {
		return o.db.Close()
	}
	return nil
}

// --- Team/Personal DB helpers ---

func (o *orgDataStore) openTeamDB(teamSlug string) (*teamDataStore, error) {
	switch o.dialect {
	case DialectPostgres:
		schemaName := teamSchemaName(teamSlug)
		quoted := fmt.Sprintf(`"%s"`, strings.ReplaceAll(schemaName, `"`, `""`))

		// Ensure the PG schema exists (idempotent). Handles the case where
		// a team record was created but ProvisionTeam failed or was skipped.
		if _, err := o.db.ExecContext(context.Background(), "CREATE SCHEMA IF NOT EXISTS "+quoted); err != nil { // CodeQL[go/sql-injection]: teamSlug validated by caller, schemaName is quoted
			return nil, fmt.Errorf("ensure team schema %s: %w", schemaName, err)
		}

		client, db, err := o.openTeamDBBySchema(schemaName)
		if err != nil {
			return nil, err
		}

		// Ensure pgvector extension exists (database-wide, idempotent).
		// Required for vector(384) columns in memories table.
		if _, err := db.ExecContext(context.Background(), "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
			slog.Warn("openTeamDB: could not ensure pgvector extension", "team", teamSlug, "error", err)
		}

		// Auto-migrate: create any missing tables/columns. Skip ModifyColumn
		// to tolerate SERIAL-vs-IDENTITY differences on legacy tables; allow
		// ModifyTable so Ent can ADD COLUMN to existing tables on deploy.
		if err := client.Schema.Create(context.Background(),
			entschema.WithSkipChanges(entschema.ModifyColumn),
		); err != nil {
			db.Close()
			client.Close()
			return nil, fmt.Errorf("auto-migrate team schema %s: %w", schemaName, err)
		}

		// Apply PG extras (idempotent — skips already-existing objects).
		if err := o.parentStore.applyPGExtras(context.Background(), ScopeTeam, db); err != nil {
			slog.Warn("openTeamDB: pg extras failed", "schema", schemaName, "error", err)
		}

		return &teamDataStore{
			teamSlug:  teamSlug,
			orgSlug:   o.orgSlug,
			client:    client,
			db:        db,
			parentOrg: o,
		}, nil

	case DialectSQLite:
		dbPath := filepath.Join(o.parentStore.dataDir, "orgs", o.orgSlug, "teams", teamSlug+".db")
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return nil, fmt.Errorf("open team sqlite: %w", err)
		}
		db.SetMaxOpenConns(1)
		for _, pragma := range []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=10000",
		} {
			if _, err := db.Exec(pragma); err != nil {
				db.Close()
				return nil, fmt.Errorf("pragma: %w", err)
			}
		}
		drv := entsql.OpenDB(dialect.SQLite, db)
		client := teament.NewClient(teament.Driver(drv))

		// Auto-migrate: create missing tables/columns. If this fails, the DB
		// has an incompatible schema from a previous version and must be removed.
		if err := client.Schema.Create(context.Background()); err != nil {
			client.Close()
			db.Close()
			return nil, fmt.Errorf("team database %q has an incompatible schema from a previous version — "+
				"please delete or move the file and restart: %w", dbPath, err)
		}

		return &teamDataStore{
			teamSlug:  teamSlug,
			orgSlug:   o.orgSlug,
			client:    client,
			db:        db,
			parentOrg: o,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported dialect: %s", o.dialect)
	}
}

// openTeamDBBySchema opens a connection to the org database with search_path
// targeting the given team schema.
func (o *orgDataStore) openTeamDBBySchema(schemaName string) (*teament.Client, *sql.DB, error) {
	orgDBName := o.parentStore.orgDBName(o.orgSlug)
	dsn, err := o.parentStore.deriveSchemaAwareDSN(orgDBName, schemaName)
	if err != nil {
		return nil, nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open team schema %s: %w", schemaName, err)
	}
	// Apply pool limits to prevent connection exhaustion.
	db.SetMaxOpenConns(o.parentStore.maxOpenConns)
	db.SetMaxIdleConns(o.parentStore.maxIdleConns)
	db.SetConnMaxLifetime(o.parentStore.connMaxLifetime)
	drv := entsql.OpenDB(dialect.Postgres, db)
	client := teament.NewClient(teament.Driver(drv))
	return client, db, nil
}

func (o *orgDataStore) openPersonalDB(userID string) (*personalDataStore, error) {
	if err := validateUserID(userID); err != nil {
		return nil, fmt.Errorf("open personal db: %w", err)
	}
	credKey := o.getOrCreateCredentialKey()

	switch o.dialect {
	case DialectPostgres:
		schemaName := personalSchemaName(userID)
		quoted := fmt.Sprintf(`"%s"`, strings.ReplaceAll(schemaName, `"`, `""`))

		// Ensure the PG schema exists (idempotent). This handles the case
		// where a user switches to an org before ProvisionPersonalSchema was
		// explicitly called for them.
		if _, err := o.db.ExecContext(context.Background(), "CREATE SCHEMA IF NOT EXISTS "+quoted); err != nil { // CodeQL[go/sql-injection]: userID is validated, schemaName is quoted
			return nil, fmt.Errorf("ensure personal schema %s: %w", schemaName, err)
		}

		client, db, err := o.openPersonalDBBySchema(schemaName)
		if err != nil {
			return nil, err
		}

		// Ensure pgvector extension exists (database-wide, idempotent).
		// Required for vector(384) columns in memories table.
		if _, err := db.ExecContext(context.Background(), "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
			slog.Warn("openPersonalDB: could not ensure pgvector extension", "user", userID, "error", err)
		}

		// Auto-migrate: create any missing tables/columns.
		if err := client.Schema.Create(context.Background(),
			entschema.WithSkipChanges(entschema.ModifyColumn),
		); err != nil {
			db.Close()
			client.Close()
			return nil, fmt.Errorf("auto-migrate personal schema %s: %w", schemaName, err)
		}

		// Apply PG extras (idempotent — skips already-existing objects).
		if err := o.parentStore.applyPGExtras(context.Background(), ScopePersonal, db); err != nil {
			slog.Warn("openPersonalDB: pg extras failed", "schema", schemaName, "error", err)
		}

		return &personalDataStore{
			userID:    userID,
			client:    client,
			db:        db,
			dialect:   o.dialect,
			embedFunc: o.embedFunc,
			credKey:   credKey,
		}, nil

	case DialectSQLite:
		safeID := strings.ReplaceAll(userID, "/", "_")
		dbPath := filepath.Join(o.parentStore.dataDir, "orgs", o.orgSlug, "personal", safeID+".db")
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return nil, fmt.Errorf("open personal sqlite: %w", err)
		}
		db.SetMaxOpenConns(1)
		for _, pragma := range []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=10000",
		} {
			if _, err := db.Exec(pragma); err != nil {
				db.Close()
				return nil, fmt.Errorf("pragma: %w", err)
			}
		}
		drv := entsql.OpenDB(dialect.SQLite, db)
		client := personalent.NewClient(personalent.Driver(drv))

		// Auto-migrate: create missing tables/columns. If this fails, the DB
		// has an incompatible schema from a previous version and must be removed.
		if err := client.Schema.Create(context.Background()); err != nil {
			client.Close()
			db.Close()
			return nil, fmt.Errorf("personal database %q has an incompatible schema from a previous version — "+
				"please delete or move the file and restart: %w", dbPath, err)
		}

		return &personalDataStore{
			userID:    userID,
			client:    client,
			db:        db,
			dialect:   o.dialect,
			embedFunc: o.embedFunc,
			credKey:   credKey,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported dialect: %s", o.dialect)
	}
}

// openPersonalDBBySchema opens a connection to the org database with search_path
// targeting the given personal schema.
func (o *orgDataStore) openPersonalDBBySchema(schemaName string) (*personalent.Client, *sql.DB, error) {
	orgDBName := o.parentStore.orgDBName(o.orgSlug)
	dsn, err := o.parentStore.deriveSchemaAwareDSN(orgDBName, schemaName)
	if err != nil {
		return nil, nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open personal schema %s: %w", schemaName, err)
	}
	// Apply pool limits to prevent connection exhaustion.
	db.SetMaxOpenConns(o.parentStore.maxOpenConns)
	db.SetMaxIdleConns(o.parentStore.maxIdleConns)
	db.SetConnMaxLifetime(o.parentStore.connMaxLifetime)
	drv := entsql.OpenDB(dialect.Postgres, db)
	client := personalent.NewClient(personalent.Driver(drv))
	return client, db, nil
}

// ===========================================================================
// teamDataStore implements store.TeamDataStore
// ===========================================================================

type teamDataStore struct {
	teamSlug  string
	orgSlug   string
	client    *teament.Client
	db        *sql.DB
	parentOrg *orgDataStore
}

func (t *teamDataStore) Sessions() store.SessionStore {
	return &teamSessionStore{client: t.client}
}
func (t *teamDataStore) Memories() store.MemoryStore {
	ms := &teamMemoryStore{
		client:    t.client,
		db:        t.db,
		dialect:   t.parentOrg.dialect,
		embedFunc: t.parentOrg.embedFunc,
		table:     "memories",
	}
	if ms.dialect == DialectSQLite {
		ms.vecIndex = newVectorIndex()
		ms.ftsTable = "memories_fts"
	}
	return ms
}
func (t *teamDataStore) Credentials() store.CredentialStore {
	return &teamCredentialStore{
		client:  t.client,
		credKey: t.parentOrg.getOrCreateCredentialKey(),
		repairSchema: func(ctx context.Context) error {
			return t.client.Schema.Create(ctx, entschema.WithSkipChanges(entschema.ModifyColumn))
		},
	}
}
func (t *teamDataStore) Apps() store.AppStore          { return &teamAppStore{client: t.client} }
func (t *teamDataStore) AppState() store.AppStateStore { return &teamAppStateStore{client: t.client} }
func (t *teamDataStore) AppStateSQL() store.AppStateSQLStore {
	if t.parentOrg.dialect == DialectPostgres {
		return &pgAppStateSQLStore{db: t.db, teamSchema: teamSchemaName(t.teamSlug)}
	}
	return newSQLiteAppStateSQLStore(filepath.Join(t.parentOrg.parentStore.dataDir, "orgs", t.parentOrg.orgSlug, "teams", t.teamSlug))
}
func (t *teamDataStore) Flows() store.FlowStore   { return &teamFlowStore{client: t.client} }
func (t *teamDataStore) Skills() store.SkillStore { return &teamSkillStore{client: t.client} }
func (t *teamDataStore) MCPServers() store.MCPServerStore {
	return &teamMCPServerStore{client: t.client}
}
func (t *teamDataStore) A2AAgents() store.A2AAgentStore {
	return &teamA2AAgentStore{client: t.client}
}
func (t *teamDataStore) NetworkPolicies() store.NetworkPolicyStore {
	return &teamNetworkPolicyStore{client: t.client}
}
func (t *teamDataStore) ScheduledJobs() store.SchedulerStore {
	return &teamSchedulerStore{client: t.client}
}
func (t *teamDataStore) FleetTemplates() store.FleetTemplateStore {
	return &teamFleetTemplateStore{client: t.client}
}
func (t *teamDataStore) FleetPlans() store.FleetPlanStore {
	return &teamFleetPlanStore{client: t.client}
}
func (t *teamDataStore) FleetSetupProfiles() store.FleetSetupProfileStore {
	return &teamFleetSetupProfileStore{client: t.client}
}
func (t *teamDataStore) FleetSetupDrafts() store.FleetSetupDraftStore {
	return &teamFleetSetupDraftStore{client: t.client}
}
func (t *teamDataStore) FleetRunStates() store.FleetRunStateStore {
	return &teamFleetRunStateStore{client: t.client}
}
func (t *teamDataStore) FleetMailbox() store.FleetMailboxStore {
	return &teamFleetMailboxStore{client: t.client}
}
func (t *teamDataStore) FleetTaskBoard() store.FleetTaskBoardStore {
	return &teamFleetTaskBoardStore{client: t.client}
}
func (t *teamDataStore) DrillReports() store.DrillReportStore {
	return &teamDrillReportStore{client: t.client}
}
func (t *teamDataStore) Settings() store.SettingsStore { return &teamSettingsStore{client: t.client} }
func (t *teamDataStore) Audit() store.AuditStore       { return &teamAuditStore{client: t.client} }

func (t *teamDataStore) SessionPin(ctx context.Context, sessionID string) (*store.SessionPin, error) {
	return getSessionPinTeam(ctx, t.client, sessionID)
}
func (t *teamDataStore) SetSessionPin(ctx context.Context, sessionID, provider, model string) error {
	return setSessionPinTeam(ctx, t.client, sessionID, provider, model)
}
func (t *teamDataStore) AppPin(ctx context.Context, appSlug string) (*store.AppPin, error) {
	return getAppPinTeam(ctx, t.client, appSlug)
}
func (t *teamDataStore) SetAppPin(ctx context.Context, appSlug, provider, model string) error {
	return setAppPinTeam(ctx, t.client, appSlug, provider, model)
}

// ===========================================================================
// personalDataStore implements store.PersonalDataStore
// ===========================================================================

type personalDataStore struct {
	userID    string
	client    *personalent.Client
	db        *sql.DB
	dialect   Dialect
	embedFunc store.EmbedFunc
	credKey   []byte // per-org credential DEK (nil = encryption disabled)
}

func (p *personalDataStore) Memories() store.MemoryStore {
	ms := &personalMemoryStore{
		client:    p.client,
		db:        p.db,
		dialect:   p.dialect,
		embedFunc: p.embedFunc,
		table:     "memories",
	}
	if ms.dialect == DialectSQLite {
		ms.vecIndex = newVectorIndex()
		ms.ftsTable = "memories_fts"
	}
	return ms
}
func (p *personalDataStore) Apps() store.AppStore { return &personalAppStore{client: p.client} }
func (p *personalDataStore) Sessions() store.SessionStore {
	return &personalSessionStore{client: p.client}
}
func (p *personalDataStore) AppState() store.AppStateStore {
	return &personalAppStateStore{client: p.client}
}
func (p *personalDataStore) Flows() store.FlowStore { return &personalFlowStore{client: p.client} }
func (p *personalDataStore) Credentials() store.CredentialStore {
	return &personalCredentialStore{
		client:  p.client,
		credKey: p.credKey,
		repairSchema: func(ctx context.Context) error {
			return p.client.Schema.Create(ctx, entschema.WithSkipChanges(entschema.ModifyColumn))
		},
	}
}
func (p *personalDataStore) ScheduledJobs() store.SchedulerStore {
	return &personalSchedulerStore{client: p.client}
}

func (p *personalDataStore) PersonalSettings() store.PersonalSettingsStore {
	return &personalSettingsStore{client: p.client, userID: p.userID}
}
func (p *personalDataStore) SessionPin(ctx context.Context, sessionID string) (*store.SessionPin, error) {
	return getSessionPinPersonal(ctx, p.client, sessionID)
}
func (p *personalDataStore) SetSessionPin(ctx context.Context, sessionID, provider, model string) error {
	return setSessionPinPersonal(ctx, p.client, sessionID, provider, model)
}
func (p *personalDataStore) AppPin(ctx context.Context, appSlug string) (*store.AppPin, error) {
	return getAppPinPersonal(ctx, p.client, appSlug)
}
func (p *personalDataStore) SetAppPin(ctx context.Context, appSlug, provider, model string) error {
	return setAppPinPersonal(ctx, p.client, appSlug, provider, model)
}
