package entstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/pgutil"
	"github.com/google/uuid"
)

func beginFileFirstPlatformExclusion(ctx context.Context, conn *sql.Conn, dialect Dialect) error {
	begin := "BEGIN IMMEDIATE"
	if dialect == DialectPostgres {
		begin = "BEGIN"
	}
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		return err
	}
	fail := func(err error) error { _, _ = conn.ExecContext(context.Background(), "ROLLBACK"); return err }
	if dialect == DialectPostgres {
		var tables []string
		for _, table := range []string{"users", "organizations", "org_memberships", "oidc_providers", "platform_settings", "platform_secrets"} {
			tables = append(tables, quoteIdent(DialectPostgres, "public")+"."+quoteIdent(DialectPostgres, table))
		}
		// Excludes writers and competing publication, but allows receipt readback
		// through another connection during offline cleanup.
		if _, err := conn.ExecContext(ctx, "LOCK TABLE "+strings.Join(tables, ", ")+" IN SHARE ROW EXCLUSIVE MODE"); err != nil {
			return fail(err)
		}
	}
	for _, table := range []string{"users", "organizations", "org_memberships", "oidc_providers", "platform_settings", "platform_secrets"} {
		var n int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent(dialect, table)).Scan(&n); err != nil {
			return fail(err)
		}
		if n != 0 {
			return fail(store.ErrBotConflict)
		}
	}
	return nil
}

// receiptPostgresStage owns only private, uncached handles. ImportTenant closes
// all of them before the coordinator records Ready or calls PublishPlatform.
type receiptPostgresStage struct {
	s       *Store
	root    *os.Root
	journal *restoreJournal
	plan    backup.RestorePlan
}

func (s *Store) preflightFileFirstPostgres(ctx context.Context, manifest backup.Manifest, opts PlatformRestoreOptions) error {
	if s.platformDB == nil || s.platformDSN == "" || s.dataDir == "" {
		return fmt.Errorf("file-first PostgreSQL restore requires a durable local journal and platform connection")
	}
	var capable bool
	// Read-only capability probe. Do not create roles or databases during planning.
	err := s.platformDB.QueryRowContext(ctx, `SELECT (r.rolsuper OR r.rolcreatedb) AND EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector') AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'astonish_app') FROM pg_roles r WHERE r.rolname = current_user`).Scan(&capable)
	if err != nil {
		return fmt.Errorf("file-first PostgreSQL capability preflight: %w", err)
	}
	if !capable {
		return fmt.Errorf("file-first PostgreSQL restore requires CREATEDB, available vector extension and existing astonish_app role")
	}
	return checkPostgresScopeSchemaCompatible(ctx, mappedManifestForRestore(manifest, opts), s.platformDB, backup.Scope{Kind: "platform"})
}

func (p *receiptPostgresStage) ImportTenant(ctx context.Context, d postgresRestoreDatabase, scopes []backup.Scope, opts PlatformRestoreOptions, result *backup.RestoreResult) (retErr error) {
	if opts.fileFirst == nil {
		return store.ErrBotUnsupported
	}
	o, err := p.s.openStagedPostgresOrg(ctx, p.journal, d)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, o.Close()) }()
	// The only database here is the newly created, receipt-checked generation.
	// Existing schema constructors initialize it; ProvisionOrg/ForOrg never run.
	ordered := append([]backup.Scope(nil), scopes...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Kind == "org" && ordered[j].Kind != "org" })
	for _, scope := range ordered {
		if scope.OrgSlug != d.OrgSlug {
			return store.ErrBotConflict
		}
		switch scope.Kind {
		case "org":
		case "team":
			if err := o.ProvisionTeam(ctx, scope.TeamSlug); err != nil {
				return err
			}
			client, db, err := o.openTeamDBBySchema(teamSchemaName(scope.TeamSlug))
			if err != nil {
				return err
			}
			err = p.s.applyPGExtras(ctx, ScopeTeam, db)
			err = errors.Join(err, grantStagedPostgresSchema(ctx, db, teamSchemaName(scope.TeamSlug), true), client.Close())
			if err != nil {
				return err
			}
		case "personal":
			if err := o.ProvisionPersonalSchema(ctx, scope.UserID); err != nil {
				return err
			}
			client, db, err := o.openPersonalDBBySchema(personalSchemaName(scope.UserID))
			if err != nil {
				return err
			}
			err = p.s.applyPGExtras(ctx, ScopePersonal, db)
			err = errors.Join(err, grantStagedPostgresSchema(ctx, db, personalSchemaName(scope.UserID), false), client.Close())
			if err != nil {
				return err
			}
		default:
			return store.ErrBotConflict
		}
		if err := checkPostgresScopeSchemaCompatible(ctx, mappedManifestForRestore(p.plan.Archive.Manifest, opts), o.db, scope); err != nil {
			return err
		}
	}
	if err := grantStagedPostgresSchema(ctx, o.db, "public", false); err != nil {
		return err
	}
	// RoleApp is a fixed identifier constant, so this statement has no dynamic SQL inputs.
	const revokeAuditWrites = `REVOKE UPDATE, DELETE ON public.org_audit_log FROM "` + pgutil.RoleApp + `"`
	if _, err := o.db.ExecContext(ctx, revokeAuditWrites); err != nil {
		return err
	}
	return restoreFileFirstPostgresTenantRows(ctx, o.db, ordered, opts, p.plan, result)
}

// Unlike ordinary best-effort provisioning grants, offline import must fail
// closed if the published application role cannot access an exact child schema.
func grantStagedPostgresSchema(ctx context.Context, db *sql.DB, schema string, team bool) error {
	q, role := quoteIdent(DialectPostgres, schema), quoteIdent(DialectPostgres, pgutil.RoleApp)
	stmts := []string{
		"GRANT USAGE ON SCHEMA " + q + " TO " + role,
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA " + q + " TO " + role,
		"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA " + q + " TO " + role,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA " + q + " GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO " + role,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA " + q + " GRANT USAGE, SELECT ON SEQUENCES TO " + role,
	}
	if team {
		stmts = append(stmts, "REVOKE UPDATE, DELETE ON "+q+".team_audit_log FROM "+role)
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// All scoped SQL rows commit together within the private database. Search path
// is transaction-local: shared pools cannot retain another scope's authority.
func restoreFileFirstPostgresTenantRows(ctx context.Context, db *sql.DB, scopes []backup.Scope, opts PlatformRestoreOptions, plan backup.RestorePlan, result *backup.RestoreResult) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var imported backup.RestoreResult
	for _, scope := range scopes {
		schema := postgresSchemaForScope(scope)
		if _, err := tx.ExecContext(ctx, "SET LOCAL search_path TO "+quoteIdent(DialectPostgres, schema)); err != nil {
			return err
		}
		entries := entriesForScopeForRestore(plan.Archive.Manifest.Entries, scope, opts)
		sort.SliceStable(entries, func(i, j int) bool {
			return restoreTablePriority(entries[i].Entity) < restoreTablePriority(entries[j].Entity)
		})
		for _, e := range entries {
			action, _ := restoreActionForEntry(e, opts)
			if action == "skip" {
				imported.SkippedEntries++
				continue
			}
			data, ok := opts.fileFirst.files[e.Path]
			if !ok {
				return fmt.Errorf("archive missing %s", e.Path)
			}
			n, err := restoreLogicalEntry(ctx, tx, DialectPostgres, schema, scope, e, data, action, postgresTenantRestoreOptions(e.Scope.OrgSlug, opts))
			if err != nil {
				return err
			}
			imported.RestoredEntries++
			imported.RestoredRecords += n
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	result.RestoredEntries += imported.RestoredEntries
	result.RestoredRecords += imported.RestoredRecords
	result.SkippedEntries += imported.SkippedEntries
	return nil
}

// postgresTenantRestoreOptions limits generic row remapping to the source
// organization. User UUIDs and team slugs are not globally unique mapping keys:
// another tenant's mapping must never rewrite ownership in this database.
func postgresTenantRestoreOptions(org string, opts PlatformRestoreOptions) PlatformRestoreOptions {
	local := opts
	local.MapOrg = map[string]string{}
	if to := opts.MapOrg[org]; to != "" {
		local.MapOrg[org] = to
	}
	local.MapTeam, local.MapUser = map[string]string{}, map[string]string{}
	for from, to := range opts.MapTeam {
		if source, _, ok := strings.Cut(from, "/"); ok && source == org {
			local.MapTeam[from] = to
		}
	}
	for from, to := range opts.MapUser {
		if source, _, ok := strings.Cut(from, "/"); ok && source == org {
			local.MapUser[from] = to
		}
	}
	return local
}

func (p *receiptPostgresStage) PublishPlatform(ctx context.Context, ds []postgresRestoreDatabase, opts PlatformRestoreOptions, result *backup.RestoreResult, j *restoreJournal) error {
	if j != p.journal || len(ds) != len(j.PostgresDatabases) {
		return store.ErrBotConflict
	}
	for i := range ds {
		if ds[i] != j.PostgresDatabases[i] {
			return store.ErrBotConflict
		}
	}
	return p.s.publishFileFirstPostgresPlatform(ctx, p.root, j, opts, p.plan, result)
}

// postgresRestoreDatabase is a receipt, not permission to adopt a named database.
// Reserved is persisted before CREATE; Created is set only after an acknowledged
// CREATE. An ambiguous CREATE is deliberately leaked for operator reconciliation.
type postgresRestoreDatabase struct {
	OrgSlug    string `json:"org_slug"`
	Name       string `json:"name"`
	Generation string `json:"generation"`
	Instance   string `json:"instance"`
	OID        int64  `json:"oid,omitempty"`
	Created    bool   `json:"created"`
	Ready      bool   `json:"ready"`
	Cleaned    bool   `json:"cleaned"`
}

func postgresRestoreDatabaseName(generation string) string {
	return "astonish_restore_" + strings.ReplaceAll(generation, "-", "")
}
func postgresRestoreReceipt(j *restoreJournal, d postgresRestoreDatabase) string {
	return "astonish-filefirst:" + j.ID + ":" + j.ArchiveSHA256 + ":" + d.Instance + ":" + d.Generation
}
func validatePostgresRestoreDatabase(s *Store, j *restoreJournal, d postgresRestoreDatabase) error {
	g, err := uuid.Parse(d.Generation)
	if err != nil || g.String() != d.Generation || d.Name != postgresRestoreDatabaseName(d.Generation) || d.Instance != s.instanceSuffix || d.OrgSlug == "" || j.ID == "" || j.PlatformDSN != s.platformDSN {
		return store.ErrBotConflict
	}
	return nil
}

// allocatePostgresRestoreDatabase never calls ProvisionOrg: that API adopts an
// existing database and updates routing before tenant import. No target is opened
// until CREATE succeeds and its exact identity has been durably recorded.
func (s *Store) allocatePostgresRestoreDatabase(ctx context.Context, root *os.Root, j *restoreJournal, org string) (int, error) {
	if s.dialect != DialectPostgres || validateSlug(org) != nil {
		return -1, store.ErrBotUnsupported
	}
	for _, d := range j.PostgresDatabases {
		if d.OrgSlug == org {
			return -1, store.ErrBotConflict
		}
	}
	d := postgresRestoreDatabase{OrgSlug: org, Generation: uuid.NewString(), Instance: s.instanceSuffix}
	d.Name = postgresRestoreDatabaseName(d.Generation)
	if err := validatePostgresRestoreDatabase(s, j, d); err != nil {
		return -1, err
	}
	index := len(j.PostgresDatabases)
	j.PostgresDatabases = append(j.PostgresDatabases, d)
	if err := writeRestoreJournal(root, j); err != nil {
		return -1, err
	}
	if _, err := s.platformDB.ExecContext(ctx, "CREATE DATABASE "+quoteIdent(DialectPostgres, d.Name)+" TEMPLATE template0"); err != nil {
		return -1, fmt.Errorf("fresh database creation not acknowledged; reservation retained, never adopt %s: %w", d.Name, err)
	}
	j.PostgresDatabases[index].Created = true
	if err := writeRestoreJournal(root, j); err != nil {
		return -1, err
	}
	receipt := postgresRestoreReceipt(j, d)
	// Receipt text is trusted generated metadata; escape literals independently of identifiers.
	if _, err := s.platformDB.ExecContext(ctx, "COMMENT ON DATABASE "+quoteIdent(DialectPostgres, d.Name)+" IS '"+strings.ReplaceAll(receipt, "'", "''")+"'"); err != nil {
		return -1, err
	}
	var oid int64
	var comment string
	if err := s.platformDB.QueryRowContext(ctx, `SELECT oid, COALESCE(shobj_description(oid, 'pg_database'), '') FROM pg_database WHERE datname = $1`, d.Name).Scan(&oid, &comment); err != nil {
		return -1, err
	}
	if oid <= 0 || comment != receipt {
		return -1, store.ErrBotConflict
	}
	j.PostgresDatabases[index].OID = oid
	if err := writeRestoreJournal(root, j); err != nil {
		return -1, err
	}
	return index, nil
}

// cleanupPostgresRestoreDatabases requires stopped cluster/target writers, just
// like offline restore cleanup. It never force-terminates connections. Missing
// ownership evidence (including interrupted CREATE acknowledgement) fails closed.
func (s *Store) cleanupPostgresRestoreDatabases(ctx context.Context, root *os.Root, j *restoreJournal) error {
	if s.dialect != DialectPostgres || j.Phase == restoreCommitted {
		return store.ErrBotConflict
	}
	for i := range j.PostgresDatabases {
		d := j.PostgresDatabases[i]
		if err := validatePostgresRestoreDatabase(s, j, d); err != nil {
			return err
		}
		if d.Cleaned {
			continue
		}
		var reachable int
		if err := s.platformDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM public.organizations WHERE db_name = $1`, d.Name).Scan(&reachable); err != nil {
			return err
		}
		if reachable != 0 {
			return store.ErrBotConflict
		}
		var oid int64
		var comment string
		err := s.platformDB.QueryRowContext(ctx, `SELECT oid, COALESCE(shobj_description(oid, 'pg_database'), '') FROM pg_database WHERE datname = $1`, d.Name).Scan(&oid, &comment)
		if errors.Is(err, sql.ErrNoRows) {
			j.PostgresDatabases[i].Cleaned = true
			if err := writeRestoreJournal(root, j); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !d.Created || d.OID <= 0 || oid != d.OID || comment != postgresRestoreReceipt(j, d) {
			return store.ErrBotConflict
		}
		if _, err := s.platformDB.ExecContext(ctx, "DROP DATABASE "+quoteIdent(DialectPostgres, d.Name)); err != nil {
			return err
		}
		j.PostgresDatabases[i].Cleaned = true
		if err := writeRestoreJournal(root, j); err != nil {
			return err
		}
	}
	return nil
}

// publishFileFirstPostgresPlatform imports routing and platform data atomically.
// All database receipts must be ready and every organization row must match one
// exact receipt. An uncertain COMMIT keeps the committed journal barrier: cleanup
// must never drop databases that may now be reachable through platform routing.
func (s *Store) publishFileFirstPostgresPlatform(ctx context.Context, root *os.Root, j *restoreJournal, opts PlatformRestoreOptions, plan backup.RestorePlan, result *backup.RestoreResult) error {
	if s.dialect != DialectPostgres || opts.fileFirst == nil || j.Phase != restoreSQLImporting {
		return store.ErrBotUnsupported
	}
	routes := map[string]string{}
	for _, d := range j.PostgresDatabases {
		if err := validatePostgresRestoreDatabase(s, j, d); err != nil {
			return err
		}
		if !d.Created || !d.Ready || d.OID <= 0 || d.Cleaned || routes[d.OrgSlug] != "" {
			return store.ErrBotConflict
		}
		var oid int64
		var comment string
		if err := s.platformDB.QueryRowContext(ctx, `SELECT oid, COALESCE(shobj_description(oid, 'pg_database'), '') FROM pg_database WHERE datname = $1`, d.Name).Scan(&oid, &comment); err != nil {
			return err
		}
		if oid != d.OID || comment != postgresRestoreReceipt(j, d) {
			return store.ErrBotConflict
		}
		routes[d.OrgSlug] = d.Name
	}
	files := make(map[string][]byte, len(opts.fileFirst.files))
	for p, data := range opts.fileFirst.files {
		files[p] = data
	}
	seen := map[string]bool{}
	scope := backup.Scope{Kind: "platform"}
	entries := entriesForScopeForRestore(plan.Archive.Manifest.Entries, scope, opts)
	for _, e := range entries {
		if e.Entity != "organizations" {
			continue
		}
		scan := backup.NewRecordScanner(bytes.NewReader(files[e.Path]), e.Entity)
		var out bytes.Buffer
		for scan.Next() {
			rec, err := scan.Record()
			if err != nil {
				return err
			}
			row, err := backup.DecodeRecordData(rec)
			if err != nil {
				return err
			}
			slug := backupString(row, "slug")
			target := slug
			if mapped := opts.MapOrg[slug]; mapped != "" {
				target = mapped
			}
			if routes[target] == "" || seen[target] {
				return store.ErrBotConflict
			}
			seen[target] = true
			row["db_name"] = routes[target]
			raw, err := json.Marshal(row)
			if err != nil {
				return err
			}
			rec.Data = raw
			raw, err = json.Marshal(rec)
			if err != nil {
				return err
			}
			out.Write(raw)
			out.WriteByte('\n')
		}
		if err := scan.Err(); err != nil {
			return err
		}
		files[e.Path] = out.Bytes()
	}
	if len(seen) != len(routes) {
		return store.ErrBotConflict
	}
	tx, err := s.platformDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tables := []string{"users", "organizations", "org_memberships", "oidc_providers", "platform_settings", "platform_secrets"}
	// ACCESS EXCLUSIVE also excludes ordinary writers not using restore's disk lock.
	var quoted []string
	for _, table := range tables {
		quoted = append(quoted, quoteIdent(DialectPostgres, "public")+"."+quoteIdent(DialectPostgres, table))
	}
	if _, err := tx.ExecContext(ctx, "LOCK TABLE "+strings.Join(quoted, ", ")+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		return err
	}
	for _, table := range quoted {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return store.ErrBotConflict
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return restoreTablePriority(entries[i].Entity) < restoreTablePriority(entries[j].Entity)
	})
	for _, e := range entries {
		action, _ := restoreActionForEntry(e, opts)
		if action == "skip" {
			result.SkippedEntries++
			continue
		}
		data, ok := files[e.Path]
		if !ok {
			return fmt.Errorf("archive missing %s", e.Path)
		}
		n, err := restoreLogicalEntry(ctx, tx, DialectPostgres, "public", scope, e, data, action, opts)
		if err != nil {
			return err
		}
		result.RestoredEntries++
		result.RestoredRecords += n
	}
	// Persist the no-cleanup barrier before COMMIT, not after a possibly lost ack.
	j.Phase = restoreCommitted
	if err := writeRestoreJournal(root, j); err != nil {
		j.Phase = restoreSQLImporting
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platform commit outcome uncertain; retain journal and reconcile routing: %w", err)
	}
	return nil
}

// postgresFileFirstStage separates tenant import from the final publication.
// Production uses receiptPostgresStage; no ordinary router or provisioning path
// can publish a database before its private tenant import has succeeded.
type postgresFileFirstStage interface {
	ImportTenant(context.Context, postgresRestoreDatabase, []backup.Scope, PlatformRestoreOptions, *backup.RestoreResult) error
	PublishPlatform(context.Context, []postgresRestoreDatabase, PlatformRestoreOptions, *backup.RestoreResult, *restoreJournal) error
}

// importFileFirstPostgresStaged is the final SQL boundary AFTER Home/content
// readback. Nothing is published until every private database is ready.
func (s *Store) importFileFirstPostgresStaged(ctx context.Context, opts PlatformRestoreOptions, plan backup.RestorePlan, root *os.Root, j *restoreJournal, stage postgresFileFirstStage) (result *backup.RestoreResult, retErr error) {
	if s.dialect != DialectPostgres || stage == nil || opts.fileFirst == nil || j.Phase != restoreFilesReady {
		return nil, store.ErrBotUnsupported
	}
	defer func() {
		if retErr != nil && j.Phase != restoreCommitted {
			j.Phase = restoreFailed
			j.Failure = retErr.Error()
			retErr = errors.Join(retErr, writeRestoreJournal(root, j))
		}
	}()
	j.Phase = restoreSQLImporting
	if err := writeRestoreJournal(root, j); err != nil {
		return nil, err
	}
	result = &backup.RestoreResult{Plan: plan, Warnings: append([]string(nil), plan.Warnings...)}
	manifest := mappedManifestForRestore(plan.Archive.Manifest, opts)
	orgs := map[string]bool{}
	for _, scope := range scopesOfKind(manifest.Scopes, "org") {
		orgs[scope.OrgSlug] = true
	}
	for _, scope := range manifest.Scopes {
		if scope.Kind != "platform" && !orgs[scope.OrgSlug] {
			return nil, fmt.Errorf("scope has no staged organization: %w", store.ErrBotConflict)
		}
	}
	for _, scope := range scopesOfKind(manifest.Scopes, "org") {
		index, err := s.allocatePostgresRestoreDatabase(ctx, root, j, scope.OrgSlug)
		if err != nil {
			return nil, err
		}
		d := j.PostgresDatabases[index]
		var scopes []backup.Scope
		for _, child := range manifest.Scopes {
			if child.OrgSlug == scope.OrgSlug {
				scopes = append(scopes, child)
			}
		}
		if err := stage.ImportTenant(ctx, d, scopes, opts, result); err != nil {
			return nil, err
		}
		j.PostgresDatabases[index].Ready = true
		if err := writeRestoreJournal(root, j); err != nil {
			return nil, err
		}
	}
	if opts.fileFirst.interruptSQL {
		return nil, errFileFirstSQLInterrupted
	}
	if err := stage.PublishPlatform(ctx, j.PostgresDatabases, opts, result, j); err != nil {
		return nil, err
	}
	if j.Phase != restoreCommitted {
		return nil, fmt.Errorf("publication did not acknowledge commit: %w", store.ErrBotConflict)
	}
	if err := writeRestoreJournal(root, j); err != nil {
		return nil, err
	}
	return result, nil
}
