package entstore

import (
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestSchemaChangeSafePostgresDSN(t *testing.T) {
	t.Parallel()

	dsn := "postgres://user:pass@db.example:5432/astonish?sslmode=require&application_name=astonish"
	got, err := schemaChangeSafePostgresDSN(dsn)
	if err != nil {
		t.Fatalf("schemaChangeSafePostgresDSN() error = %v", err)
	}

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if got := u.Query().Get("default_query_exec_mode"); got != postgresSchemaChangeSafeQueryMode {
		t.Errorf("default_query_exec_mode = %q, want %q", got, postgresSchemaChangeSafeQueryMode)
	}
	if got := u.Query().Get("sslmode"); got != "require" {
		t.Errorf("sslmode = %q, want require", got)
	}
	if got := u.Query().Get("application_name"); got != "astonish" {
		t.Errorf("application_name = %q, want astonish", got)
	}
}

func TestSchemaChangeSafePostgresDSNParsesWithPGX(t *testing.T) {
	t.Parallel()

	got, err := schemaChangeSafePostgresDSN("postgres://user:pass@localhost/db?sslmode=disable")
	if err != nil {
		t.Fatalf("schemaChangeSafePostgresDSN() error = %v", err)
	}
	cfg, err := pgx.ParseConfig(got)
	if err != nil {
		t.Fatalf("pgx.ParseConfig() error = %v", err)
	}
	if cfg.DefaultQueryExecMode != pgx.QueryExecModeExec {
		t.Errorf("DefaultQueryExecMode = %v, want %v", cfg.DefaultQueryExecMode, pgx.QueryExecModeExec)
	}
}

func TestSchemaChangeSafePostgresDSNOverridesCachedMode(t *testing.T) {
	t.Parallel()

	dsn := "postgres://user@localhost/db?default_query_exec_mode=cache_statement"
	got, err := schemaChangeSafePostgresDSN(dsn)
	if err != nil {
		t.Fatalf("schemaChangeSafePostgresDSN() error = %v", err)
	}

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if got := u.Query()["default_query_exec_mode"]; len(got) != 1 || got[0] != postgresSchemaChangeSafeQueryMode {
		t.Errorf("default_query_exec_mode values = %q, want [%q]", got, postgresSchemaChangeSafeQueryMode)
	}
}

func TestDeriveSchemaAwareDSNPreservesSafeModeAndSearchPath(t *testing.T) {
	t.Parallel()

	platformDSN, err := schemaChangeSafePostgresDSN("postgres://user:pass@localhost/platform?sslmode=disable")
	if err != nil {
		t.Fatalf("schemaChangeSafePostgresDSN() error = %v", err)
	}
	s := &Store{platformDSN: platformDSN}

	got, err := s.deriveSchemaAwareDSN("astonish_acme", `team_ops-west`)
	if err != nil {
		t.Fatalf("deriveSchemaAwareDSN() error = %v", err)
	}

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if u.Path != "/astonish_acme" {
		t.Errorf("path = %q, want /astonish_acme", u.Path)
	}
	if got := u.Query().Get("search_path"); got != `"team_ops-west",public` {
		t.Errorf("search_path = %q, want %q", got, `"team_ops-west",public`)
	}
	if got := u.Query().Get("default_query_exec_mode"); got != postgresSchemaChangeSafeQueryMode {
		t.Errorf("default_query_exec_mode = %q, want %q", got, postgresSchemaChangeSafeQueryMode)
	}
	if got := u.Query().Get("sslmode"); got != "disable" {
		t.Errorf("sslmode = %q, want disable", got)
	}
}
