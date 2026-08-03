package entstore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/SAP/astonish/ent/platform/loginsession"
	"github.com/SAP/astonish/ent/platform/pendinglinkcode"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/store"
)

// cachedSandboxSessionStore holds a cached *sql.DB and Ent client for a
// specific team schema. Used by sandboxSessionsForSchema to avoid leaking
// connection pools on repeated calls from background workers.
type cachedSandboxSessionStore struct {
	db     *sql.DB
	client *teament.Client
	store  *teamSandboxSessionStore
}

// --- store.TenantRouter ---
// ForOrg, ProvisionOrg, DecommissionOrg are implemented in tenant_router.go.

// --- store.PlatformBackend additional methods ---

// PlatformSettings is implemented in platform_settings.go.
// OrgSettings is implemented in platform_settings.go.
// PlatformMCPServers is implemented in mcp_servers.go.
// SandboxLayers is implemented in sandbox.go.
// SandboxTemplates is implemented in sandbox.go.
// SecretGetter is implemented in platform_settings.go.

func (s *Store) SandboxLayers() store.LayerStore {
	return &layerStore{client: s.platformClient}
}

func (s *Store) SandboxTemplates() store.SandboxTemplateStore {
	return s.sandboxTemplates
}

func (s *Store) MigrateAll(ctx context.Context) error {
	// TODO: implement migration runner
	return fmt.Errorf("entstore: MigrateAll not yet implemented")
}

func (s *Store) CleanupExpired(ctx context.Context) error {
	now := time.Now()

	// Delete expired login sessions.
	if _, err := s.platformClient.LoginSession.Delete().
		Where(loginsession.ExpiresAtLT(now)).
		Exec(ctx); err != nil {
		return fmt.Errorf("cleanup login sessions: %w", err)
	}

	// Delete expired link codes.
	if _, err := s.platformClient.PendingLinkCode.Delete().
		Where(pendinglinkcode.ExpiresAtLT(now)).
		Exec(ctx); err != nil {
		return fmt.Errorf("cleanup link codes: %w", err)
	}

	return nil
}

// AllSandboxSessionIDs returns all known sandbox session IDs across all teams.
// Used by the K8s GC reconciler to detect orphan pods.
func (s *Store) AllSandboxSessionIDs(ctx context.Context) (map[string]bool, error) {
	sessions, err := s.AllSandboxSessions(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]bool, len(sessions))
	for _, sess := range sessions {
		result[sess.SessionID] = true
	}
	return result, nil
}

// AllSandboxSessions returns metadata for all sandbox sessions across all team
// schemas. This is a platform-admin/background-worker surface for GC and audit;
// user request paths must continue to use tenant-scoped stores.
func (s *Store) AllSandboxSessions(ctx context.Context) ([]store.SandboxSessionGCInfo, error) {
	teamSchemas, err := s.ListTeamSchemas(ctx)
	if err != nil {
		return nil, fmt.Errorf("list team schemas: %w", err)
	}

	var result []store.SandboxSessionGCInfo
	for _, ts := range teamSchemas {
		sessStore := s.sandboxSessionsForSchema(ts.orgDBName, ts.schemaName)
		if sessStore == nil {
			continue
		}
		sessions, err := sessStore.List(ctx, store.SandboxSessionFilter{})
		if err != nil {
			// Non-fatal: schema might not have sandbox_sessions table yet.
			slog.Debug("AllSandboxSessions: list failed", "db", ts.orgDBName, "schema", ts.schemaName, "error", err)
			continue
		}
		for _, sess := range sessions {
			if sess == nil {
				continue
			}
			result = append(result, store.SandboxSessionGCInfo{
				SandboxSession: *sess,
				OrgDBName:      ts.orgDBName,
				TeamSchema:     ts.schemaName,
			})
		}
	}
	return result, nil
}

// DeleteSandboxSessionInTeam removes a sandbox session row from the exact team
// schema that owns it. It is used by GC only after the corresponding persisted
// upper directory has been reclaimed.
func (s *Store) DeleteSandboxSessionInTeam(ctx context.Context, orgDBName, teamSchema, sessionID string) error {
	if orgDBName == "" || teamSchema == "" {
		return fmt.Errorf("DeleteSandboxSessionInTeam: org database and team schema are required")
	}
	sessStore := s.sandboxSessionsForSchema(orgDBName, teamSchema)
	if sessStore == nil {
		return fmt.Errorf("DeleteSandboxSessionInTeam: sandbox session store unavailable for %s.%s", orgDBName, teamSchema)
	}
	return sessStore.Delete(ctx, sessionID)
}

// SandboxSessionsForTeam returns a SandboxSessionStore for the given org and
// team. For PostgreSQL mode, opens a connection to the team schema within the
// org database and returns an Ent-backed store. For SQLite mode, returns nil.
func (s *Store) SandboxSessionsForTeam(_ context.Context, orgSlug, teamSlug string) store.SandboxSessionStore {
	if s.dialect != DialectPostgres {
		return nil
	}

	orgDB := s.orgDBName(orgSlug)
	schema := teamSchemaName(teamSlug)
	return s.sandboxSessionsForSchema(orgDB, schema)
}

// teamSchemaRef represents a team schema within an org database.
type teamSchemaRef struct {
	orgDBName  string
	schemaName string
}

// ListTeamSchemas returns all team schemas across all active orgs.
// Used by sandbox audit and GC reconciler to iterate all sandbox sessions.
func (s *Store) ListTeamSchemas(ctx context.Context) ([]teamSchemaRef, error) {
	if s.dialect != DialectPostgres {
		return nil, fmt.Errorf("ListTeamSchemas only supported on PostgreSQL")
	}

	// Get all active orgs to find their databases.
	orgs, err := s.platformClient.Organization.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query orgs: %w", err)
	}

	var refs []teamSchemaRef
	for _, org := range orgs {
		orgDB := org.DbName
		if orgDB == "" {
			orgDB = s.orgDBName(org.Slug)
		}

		// Connect to the org database and list team schemas.
		dsn, err := s.deriveDSN(orgDB)
		if err != nil {
			slog.Debug("ListTeamSchemas: derive DSN failed", "org", org.Slug, "error", err)
			continue
		}
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			slog.Debug("ListTeamSchemas: open failed", "org", org.Slug, "error", err)
			continue
		}

		rows, err := db.QueryContext(ctx,
			`SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE 'team_%' ORDER BY schema_name`)
		if err != nil {
			db.Close()
			slog.Debug("ListTeamSchemas: query failed", "org", org.Slug, "error", err)
			continue
		}
		for rows.Next() {
			var schema string
			if err := rows.Scan(&schema); err != nil {
				continue
			}
			refs = append(refs, teamSchemaRef{orgDBName: orgDB, schemaName: schema})
		}
		rows.Close()
		db.Close()
	}

	return refs, nil
}

// sandboxSessionsForSchema returns a cached SandboxSessionStore for the given
// team schema within the given org database. The underlying *sql.DB is opened
// once and reused across calls — preventing the connection leak that previously
// occurred when background workers called this every 5 minutes.
//
// Returns nil if the connection fails or if sandbox_sessions table doesn't exist.
func (s *Store) sandboxSessionsForSchema(orgDBName, schemaName string) store.SandboxSessionStore {
	cacheKey := orgDBName + "|" + schemaName

	// Fast path: check cache.
	if cached, ok := s.sandboxSessClients.Load(cacheKey); ok {
		return cached.(*cachedSandboxSessionStore).store
	}

	// Serialize concurrent opens for the same key.
	result, err, _ := s.sandboxSessFlight.Do(cacheKey, func() (interface{}, error) {
		// Double-check cache.
		if cached, ok := s.sandboxSessClients.Load(cacheKey); ok {
			return cached, nil
		}

		dsn, err := s.deriveSchemaAwareDSN(orgDBName, schemaName)
		if err != nil {
			return nil, fmt.Errorf("derive DSN: %w", err)
		}

		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, fmt.Errorf("open: %w", err)
		}

		// Apply pool limits to prevent unbounded connection growth.
		db.SetMaxOpenConns(s.maxOpenConns)
		db.SetMaxIdleConns(s.maxIdleConns)
		db.SetConnMaxLifetime(s.connMaxLifetime)

		// Check if sandbox_sessions table exists in this schema.
		var exists bool
		err = db.QueryRow(`SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = $1 AND table_name = 'sandbox_sessions'
		)`, schemaName).Scan(&exists)
		if err != nil || !exists {
			db.Close()
			return nil, fmt.Errorf("table not found")
		}

		drv := entsql.OpenDB(dialect.Postgres, db)
		client := teament.NewClient(teament.Driver(drv))
		entry := &cachedSandboxSessionStore{
			db:     db,
			client: client,
			store:  &teamSandboxSessionStore{client: client},
		}

		s.sandboxSessClients.Store(cacheKey, entry)
		return entry, nil
	})
	if err != nil {
		slog.Debug("sandboxSessionsForSchema: open failed",
			"db", orgDBName, "schema", schemaName, "error", err)
		return nil
	}
	return result.(*cachedSandboxSessionStore).store
}

// PruneStaleSandboxSessions removes sandbox_sessions records whose
// chat_session_id does not correspond to an existing chat session in the same
// team schema. These stale records accumulate when session deletion previously
// failed to clean up the sandbox registry (before the PG-backed
// TryDestroySession fix).
//
// Returns the total number of records deleted across all team schemas.
func (s *Store) PruneStaleSandboxSessions(ctx context.Context) (int, error) {
	teamSchemas, err := s.ListTeamSchemas(ctx)
	if err != nil {
		return 0, fmt.Errorf("PruneStaleSandboxSessions: list team schemas: %w", err)
	}

	total := 0
	for _, ts := range teamSchemas {
		dsn, err := s.deriveSchemaAwareDSN(ts.orgDBName, ts.schemaName)
		if err != nil {
			slog.Debug("PruneStaleSandboxSessions: derive DSN failed", "db", ts.orgDBName, "schema", ts.schemaName, "error", err)
			continue
		}

		db, err := sql.Open("pgx", dsn)
		if err != nil {
			slog.Debug("PruneStaleSandboxSessions: open failed", "db", ts.orgDBName, "schema", ts.schemaName, "error", err)
			continue
		}

		// Delete sandbox_sessions whose chat_session_id has no matching row in sessions.
		// The special 'team-template-*' rows (used for template builds) have
		// chat_session_id = id, and there's never a matching chat session for them.
		// They are intentionally pruned here — they represent build artifacts, not
		// live sessions.
		//
		// Safety guards:
		//   - Skip records younger than 10 minutes to avoid a race where the
		//     sandbox_session is inserted before the chat sessions row is committed
		//     (or when the chat session lives in a different org database).
		//   - For active states (creating/running/resuming), use a longer grace
		//     period of 1 hour. This handles the case where a chat session is
		//     deleted while the sandbox is still running (e.g., user clears chat
		//     history, or daemon restart loses in-memory state). Without this,
		//     running-state orphans are never pruned and pods accumulate forever.
		res, err := db.ExecContext(ctx, `
			DELETE FROM sandbox_sessions ss
			WHERE NOT EXISTS (
				SELECT 1 FROM sessions s WHERE s.id = ss.chat_session_id
			)
			AND (
				(ss.state NOT IN ('creating', 'running', 'resuming')
				 AND ss.created_at < NOW() - INTERVAL '10 minutes')
				OR
				(ss.state IN ('creating', 'running', 'resuming')
				 AND ss.created_at < NOW() - INTERVAL '1 hour')
			)
		`)
		db.Close()
		if err != nil {
			slog.Warn("PruneStaleSandboxSessions: delete failed", "db", ts.orgDBName, "schema", ts.schemaName, "error", err)
			continue
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			slog.Info("PruneStaleSandboxSessions: pruned stale records",
				"db", ts.orgDBName, "schema", ts.schemaName, "pruned", n)
		}
		total += int(n)
	}
	return total, nil
}

// ListIdleSandboxSessions returns all running (non-pinned) sandbox sessions
// across all team schemas whose last_active_at is before the given cutoff.
// Used by the idle watchdog to find sessions that should be evicted.
func (s *Store) ListIdleSandboxSessions(ctx context.Context, cutoff time.Time) ([]store.SandboxSession, error) {
	teamSchemas, err := s.ListTeamSchemas(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListIdleSandboxSessions: list team schemas: %w", err)
	}

	var result []store.SandboxSession
	for _, ts := range teamSchemas {
		sessStore := s.sandboxSessionsForSchema(ts.orgDBName, ts.schemaName)
		if sessStore == nil {
			continue
		}
		sessions, lErr := sessStore.List(ctx, store.SandboxSessionFilter{State: store.SandboxSessionStateRunning})
		if lErr != nil {
			slog.Debug("ListIdleSandboxSessions: list failed", "db", ts.orgDBName, "schema", ts.schemaName, "error", lErr)
			continue
		}
		for _, sess := range sessions {
			if sess.Pinned {
				continue
			}
			if sess.LastActiveAt.IsZero() || sess.LastActiveAt.Before(cutoff) {
				result = append(result, *sess)
			}
		}
	}
	return result, nil
}

// MarkSandboxSessionEvicted finds the given sandbox session (by SessionID)
// across all team schemas and sets its state to Evicted, clearing container/pod
// names. Used by the idle watchdog after pod deletion.
func (s *Store) MarkSandboxSessionEvicted(ctx context.Context, sessionID string) error {
	teamSchemas, err := s.ListTeamSchemas(ctx)
	if err != nil {
		return fmt.Errorf("MarkSandboxSessionEvicted: list team schemas: %w", err)
	}

	for _, ts := range teamSchemas {
		sessStore := s.sandboxSessionsForSchema(ts.orgDBName, ts.schemaName)
		if sessStore == nil {
			continue
		}
		sess, gErr := sessStore.Get(ctx, sessionID)
		if gErr != nil || sess == nil {
			continue
		}
		sess.State = store.SandboxSessionStateEvicted
		sess.ContainerName = ""
		sess.PodName = ""
		return sessStore.Put(ctx, sess)
	}
	return fmt.Errorf("MarkSandboxSessionEvicted: session %q not found", sessionID)
}
