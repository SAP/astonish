// Package pgutil provides PostgreSQL utility functions for DSN construction,
// manipulation, and one-time infrastructure setup (role creation, database
// existence checks). Uses pgx for raw PG connections and database/sql for
// role management.
//
// This package has no dependency on pgstore or entstore. It provides the thin
// infrastructure layer that runs *before* Ent can connect (CREATE DATABASE,
// CREATE ROLE).
package pgutil

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/SAP/astonish/pkg/config"
	"github.com/jackc/pgx/v5"
)

// BuildDSN constructs a PostgreSQL connection string from individual components.
func BuildDSN(host string, port int, user, password, dbname, sslmode string) string {
	u := &url.URL{
		Scheme: "postgres",
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   "/" + dbname,
	}
	if password != "" {
		u.User = url.UserPassword(user, password)
	} else {
		u.User = url.User(user)
	}
	if sslmode != "" {
		q := u.Query()
		q.Set("sslmode", sslmode)
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// ReplaceDSNDatabase takes a PostgreSQL DSN and replaces the database name.
// Supports both URL-style (postgres://...) and keyword-style (host=... dbname=...) DSNs.
func ReplaceDSNDatabase(dsn, newDB string) (string, error) {
	// Try URL-style first
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("failed to parse DSN as URL: %w", err)
		}
		u.Path = "/" + newDB
		return u.String(), nil
	}

	// Keyword-style DSN (e.g., "host=localhost dbname=foo")
	parts := strings.Fields(dsn)
	found := false
	for i, part := range parts {
		if strings.HasPrefix(part, "dbname=") {
			parts[i] = "dbname=" + newDB
			found = true
			break
		}
	}
	if !found {
		parts = append(parts, "dbname="+newDB)
	}
	return strings.Join(parts, " "), nil
}

// PlatformDBExists checks whether a platform database with the given suffix
// already exists on the PostgreSQL host. Uses a single pgx connection to the
// admin database.
func PlatformDBExists(ctx context.Context, anyDSN, suffix string) (bool, error) {
	adminDSN, err := ReplaceDSNDatabase(anyDSN, "postgres")
	if err != nil {
		return false, err
	}
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return false, err
	}
	defer conn.Close(ctx)

	return PlatformDBExistsConn(ctx, conn, suffix)
}

// PlatformDBExistsConn checks whether a platform database with the given suffix
// already exists, using an existing connection. This avoids opening multiple
// connections which can be problematic with kubectl port-forward tunnels.
func PlatformDBExistsConn(ctx context.Context, conn *pgx.Conn, suffix string) (bool, error) {
	dbName := config.PlatformDBName(suffix)
	var exists bool
	err := conn.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`,
		dbName,
	).Scan(&exists)
	return exists, err
}

// PlatformDBExistsDB checks whether a platform database with the given suffix
// already exists, using an existing *sql.DB connection pool. Preferred over
// PlatformDBExists when the caller needs to keep the connection alive for
// subsequent operations (e.g., through kubectl port-forward tunnels).
func PlatformDBExistsDB(ctx context.Context, db *sql.DB, suffix string) (bool, error) {
	dbName := config.PlatformDBName(suffix)
	var exists bool
	err := db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`,
		dbName,
	).Scan(&exists)
	return exists, err
}

// PostgreSQL roles used by the platform.
const (
	// RolePlatformAdmin owns all databases and runs migrations.
	RolePlatformAdmin = "astonish_platform_admin"

	// RoleApp is the application connection role with restricted privileges.
	RoleApp = "astonish_app"
)

// EnsureRoles creates the platform roles if they don't already exist.
// Uses database/sql (compatible with entstore's *sql.DB). Must be called
// from a connection with CREATEROLE privilege (typically the admin DSN
// pointing to the "postgres" database).
func EnsureRoles(ctx context.Context, db *sql.DB) error {
	for _, role := range []string{RolePlatformAdmin, RoleApp} {
		stmt := fmt.Sprintf(`DO $$ BEGIN CREATE ROLE %q; EXCEPTION WHEN duplicate_object THEN NULL; END $$`, role)
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure role %s: %w", role, err)
		}
	}

	// Grant LOGIN to the app role.
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`ALTER ROLE %q LOGIN`, RoleApp)); err != nil {
		return fmt.Errorf("grant LOGIN to %s: %w", RoleApp, err)
	}

	return nil
}

// ApplyGrants applies standard grants for the astonish_app role on the
// given database. Call this after Schema.Create() on each newly provisioned
// database (platform, org, team, personal).
func ApplyGrants(ctx context.Context, db *sql.DB, scope string) error {
	appRole := fmt.Sprintf("%q", RoleApp)
	grants := []string{
		fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, appRole),
		fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %s`, appRole),
		fmt.Sprintf(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %s`, appRole),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s`, appRole),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO %s`, appRole),
	}

	// Platform: more restrictive (SELECT-only by default, specific writes on login_sessions)
	if scope == "platform" {
		grants = []string{
			fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, appRole),
			fmt.Sprintf(`GRANT SELECT ON ALL TABLES IN SCHEMA public TO %s`, appRole),
			fmt.Sprintf(`GRANT INSERT, UPDATE, DELETE ON login_sessions TO %s`, appRole),
			fmt.Sprintf(`GRANT INSERT, UPDATE, DELETE ON device_sessions TO %s`, appRole),
			fmt.Sprintf(`GRANT INSERT, UPDATE, DELETE ON pending_link_codes TO %s`, appRole),
			fmt.Sprintf(`GRANT INSERT, UPDATE, DELETE ON platform_settings TO %s`, appRole),
			fmt.Sprintf(`GRANT INSERT, UPDATE, DELETE ON platform_secrets TO %s`, appRole),
			fmt.Sprintf(`GRANT INSERT, UPDATE, DELETE ON platform_mcp_servers TO %s`, appRole),
			fmt.Sprintf(`GRANT INSERT, UPDATE, DELETE ON user_channels TO %s`, appRole),
		}
	}

	// Org/Team: append audit-log append-only enforcement
	if scope == "org" {
		grants = append(grants, fmt.Sprintf(`REVOKE UPDATE, DELETE ON org_audit_log FROM %s`, appRole))
	}
	if scope == "team" {
		grants = append(grants, fmt.Sprintf(`REVOKE UPDATE, DELETE ON team_audit_log FROM %s`, appRole))
	}

	for _, g := range grants {
		if _, err := db.ExecContext(ctx, g); err != nil {
			// Non-fatal: some grants may fail if table doesn't exist yet (optional tables).
			// Log and continue.
			_ = err
		}
	}
	return nil
}
