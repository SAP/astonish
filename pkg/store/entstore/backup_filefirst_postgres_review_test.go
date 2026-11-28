package entstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// reviewReachableSQL deliberately does not use the existing fake's unconditional
// zero result for db_name probes: exercise the real published-route rejection.
type reviewReachableSQL struct{ *postgresReceiptSQL }

func (m reviewReachableSQL) QueryContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, "WHERE db_name = $1") {
		m.events = append(m.events, q)
		return &postgresReceiptRows{values: []driver.Value{int64(1)}}, nil
	}
	return m.postgresReceiptSQL.QueryContext(ctx, q, a)
}
func (m reviewReachableSQL) Connect(context.Context) (driver.Conn, error) { return m, nil }

func TestPostgresFileFirstReviewRejectPublishedReceipt(t *testing.T) {
	for _, op := range []string{"open", "cleanup"} {
		t.Run(op, func(t *testing.T) {
			s, root, j, m := postgresReceiptFixture(t)
			j.Phase = restoreSQLImporting
			g := uuid.NewString()
			d := postgresRestoreDatabase{OrgSlug: "owned", Generation: g, Name: postgresRestoreDatabaseName(g), Instance: s.instanceSuffix, Created: true, OID: 123}
			j.PostgresDatabases = []postgresRestoreDatabase{d}
			m.query = func(string, []driver.NamedValue) ([]driver.Value, error) {
				return []driver.Value{int64(123), postgresRestoreReceipt(j, d)}, nil
			}
			db := sql.OpenDB(reviewReachableSQL{m})
			defer db.Close()
			s.platformDB = db
			var err error
			if op == "open" {
				err = s.checkStagedPostgresOrg(t.Context(), j, d)
			} else {
				err = s.cleanupPostgresRestoreDatabases(t.Context(), root, j)
			}
			if !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("published receipt accepted", err)
			}
			for _, q := range m.events {
				if strings.HasPrefix(q, "DROP") {
					t.Fatal("published database dropped", q)
				}
			}
			if j.PostgresDatabases[0].Cleaned {
				t.Fatal("published database marked cleaned")
			}
		})
	}
}

// Shared user IDs and team slugs in another organization must not determine
// ownership or routing inside a private restored tenant database.
func TestPostgresFileFirstReviewTenantMappingIsolation(t *testing.T) {
	for _, mode := range []string{"foreign-only", "qualified-local"} {
		t.Run(mode, func(t *testing.T) {
			const oldUser = "00000000-0000-0000-0000-000000000001"
			const localUser = "00000000-0000-0000-0000-000000000002"
			const foreignUser = "00000000-0000-0000-0000-000000000003"
			source := backup.Scope{Kind: "team", OrgSlug: "local", TeamSlug: "shared"}
			target := backup.Scope{Kind: "team", OrgSlug: "restored", TeamSlug: "shared"}
			opts := PlatformRestoreOptions{MapOrg: map[string]string{"local": "restored", "foreign": "other"}, MapTeam: map[string]string{"foreign/shared": "other/foreign-team"}, MapUser: map[string]string{"foreign/" + oldUser: "other/" + foreignUser}, fileFirst: &fileFirstRestoreState{mapping: newFileFirstMapping(), files: map[string][]byte{"apps": []byte(`{"entity":"apps","data":{"id":"00000000-0000-0000-0000-000000000004","owner_id":"` + oldUser + `","name":"shared"}}` + "\n")}}}
			wantUser, wantName := oldUser, "shared"
			if mode == "qualified-local" {
				opts.MapUser["local/"+oldUser] = "restored/" + localUser
				opts.MapTeam["local/shared"] = "restored/local-team"
				target.TeamSlug = "local-team"
				wantUser, wantName = localUser, "local-team"
			}
			m := &postgresReceiptSQL{}
			db := sql.OpenDB(m)
			defer db.Close()
			inserted := false
			m.exec = func(q string, a []driver.NamedValue) error {
				if !strings.HasPrefix(q, "INSERT") {
					return nil
				}
				inserted = true
				if !strings.Contains(q, quoteIdent(DialectPostgres, postgresSchemaForScope(target))+`."apps"`) {
					t.Fatalf("wrong tenant schema: %s", q)
				}
				columns := []string{"id", "name", "owner_id"}
				for i, col := range columns {
					want := map[string]string{"id": "00000000-0000-0000-0000-000000000004", "name": wantName, "owner_id": wantUser}[col]
					if a[i].Value != want {
						t.Errorf("%s: got %v want %s", col, a[i].Value, want)
					}
				}
				return nil
			}
			plan := backup.RestorePlan{Archive: backup.Summary{Manifest: backup.Manifest{Entries: []backup.Entry{{Path: "apps", Kind: "jsonl", Entity: "apps", Scope: source}}}}}
			if err := restoreFileFirstPostgresTenantRows(t.Context(), db, []backup.Scope{target}, opts, plan, &backup.RestoreResult{}); err != nil {
				t.Fatal(err)
			}
			if !inserted {
				t.Fatal("no row imported")
			}
		})
	}
}
