package entstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
)

func TestTenantGenerationRouteLookupFailureDoesNotFallback(t *testing.T) {
	failure := errors.New("canonical routing unavailable")
	fake := &postgresReceiptSQL{query: func(string, []driver.NamedValue) ([]driver.Value, error) { return nil, failure }}
	db := sql.OpenDB(fake)
	defer db.Close()
	s := &Store{dialect: DialectPostgres, instanceSuffix: "local", platformDB: db}
	if _, err := s.resolveOrgPostgresDBName(context.Background(), "acme"); !errors.Is(err, failure) {
		t.Fatalf("lookup error hidden: %v", err)
	}
	if err := s.decommissionOrgPostgres(context.Background(), "acme"); !errors.Is(err, failure) {
		t.Fatalf("decommission ignored failed canonical route: %v", err)
	}
	for _, q := range fake.events {
		if q != `SELECT db_name FROM public.organizations WHERE slug = $1` {
			t.Fatalf("mutated unresolved database: %s", q)
		}
	}
	before := len(fake.events)
	if _, err := s.resolveOrgPostgresDBName(context.Background(), "../acme"); err == nil {
		t.Fatal("unsafe slug accepted")
	}
	if len(fake.events) != before {
		t.Fatal("queried unsafe slug")
	}
}
