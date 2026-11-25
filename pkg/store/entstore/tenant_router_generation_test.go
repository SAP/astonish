package entstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestTenantGenerationRoute(t *testing.T) {
	generation := uuid.NewString()
	generated := postgresRestoreDatabaseName(generation)
	receipt := "astonish-filefirst:journal:" + strings.Repeat("a", 64) + ":local:" + generation
	for _, tc := range []struct {
		name, route, comment string
		absent, denied       bool
	}{
		{name: "legacy", route: "astonish_local_acme"},
		{name: "absent canonical row", absent: true},
		{name: "published generation", route: generated, comment: receipt},
		{name: "empty canonical route", denied: true},
		{name: "foreign org", route: "astonish_local_other", denied: true},
		{name: "foreign instance legacy", route: "astonish_remote_acme", denied: true},
		{name: "foreign generation instance", route: generated, comment: strings.Replace(receipt, ":local:", ":remote:", 1), denied: true},
		{name: "unreceipted generation", route: generated, denied: true},
		{name: "wrong generation", route: generated, comment: "astonish-filefirst:journal:" + strings.Repeat("a", 64) + ":local:" + uuid.NewString(), denied: true},
		{name: "invalid archive digest", route: generated, comment: strings.Replace(receipt, strings.Repeat("a", 64), "hash", 1), denied: true},
		{name: "unsafe route", route: "../astonish_local_acme", denied: true},
		{name: "truncated route", route: strings.Repeat("a", 64), denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &postgresReceiptSQL{query: func(q string, a []driver.NamedValue) ([]driver.Value, error) {
				if a[0].Value != "acme" && a[0].Value != generated {
					t.Fatalf("unexpected argument: %v", a)
				}
				if strings.Contains(q, "SELECT db_name") {
					if tc.absent {
						return nil, nil
					}
					return []driver.Value{tc.route}, nil
				}
				return []driver.Value{tc.comment}, nil
			}}
			db := sql.OpenDB(fake)
			t.Cleanup(func() { db.Close() })
			s := &Store{dialect: DialectPostgres, instanceSuffix: "local", platformDB: db, platformDSN: "postgres://localhost/platform"}
			got, err := s.resolveOrgPostgresDBName(context.Background(), "acme")
			if tc.denied {
				if !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("expected conflict, got %q %v", got, err)
				}
				if _, _, err := s.openOrgDB("acme"); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("org opener accepted denied route: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := tc.route
			if tc.absent {
				want = "astonish_local_acme"
			}
			if got != want {
				t.Fatalf("got %q want %q", got, want)
			}
			o := &orgDataStore{orgSlug: "acme", dbName: got, parentStore: s}
			// Both child openers must use the pinned route rather than a second lookup.
			before := len(fake.events)
			for _, schema := range []string{teamSchemaName("engineering"), personalSchemaName(uuid.NewString())} {
				name, err := o.postgresDBName(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				dsn, err := s.deriveSchemaAwareDSN(name, schema)
				if err != nil {
					t.Fatal(err)
				}
				u, _ := url.Parse(dsn)
				if u.Path != "/"+want {
					t.Fatalf("foreign child DSN %s", u.Path)
				}
			}
			team, tdb, err := o.openTeamDBBySchema(teamSchemaName("engineering"))
			if err != nil {
				t.Fatal(err)
			}
			team.Close()
			tdb.Close()
			personal, pdb, err := o.openPersonalDBBySchema(personalSchemaName(uuid.NewString()))
			if err != nil {
				t.Fatal(err)
			}
			personal.Close()
			pdb.Close()
			if len(fake.events) != before {
				t.Fatal("child connection re-resolved pinned database")
			}
		})
	}
}

func TestTenantGenerationRouteTeamSchema(t *testing.T) {
	for _, tc := range []struct {
		name           string
		absent, denied bool
	}{
		{name: "team_engineering"}, {absent: true}, {name: "team_other", denied: true}, {name: "public", denied: true}, {name: "team_engineering,public", denied: true}, {denied: true},
	} {
		fake := &postgresReceiptSQL{query: func(q string, a []driver.NamedValue) ([]driver.Value, error) {
			if tc.absent {
				return nil, nil
			}
			return []driver.Value{tc.name}, nil
		}}
		db := sql.OpenDB(fake)
		o := &orgDataStore{db: db}
		got, err := o.resolveTeamPostgresSchema(context.Background(), "engineering")
		db.Close()
		if tc.denied {
			if !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("accepted %q: %v", tc.name, err)
			}
		} else if err != nil || got != "team_engineering" {
			t.Fatalf("got %q %v", got, err)
		}
	}
}

type stagedRouteSQL struct {
	*postgresReceiptSQL
	published bool
}

func (m *stagedRouteSQL) Connect(context.Context) (driver.Conn, error) { return m, nil }
func (m *stagedRouteSQL) QueryContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	if m.published && strings.Contains(q, "WHERE db_name = $1") {
		m.events = append(m.events, q)
		return &postgresReceiptRows{values: []driver.Value{int64(1)}}, nil
	}
	return m.postgresReceiptSQL.QueryContext(ctx, q, a)
}

func TestStagedPostgresOrgReceiptPreflight(t *testing.T) {
	for _, scenario := range []string{"valid", "nil journal", "foreign instance", "not created", "cleaned", "ready", "zero oid", "unrecorded", "wrong oid", "wrong comment", "published", "wrong phase"} {
		t.Run(scenario, func(t *testing.T) {
			s := &Store{dialect: DialectPostgres, instanceSuffix: "local", platformDSN: "postgres://localhost/platform"}
			d := postgresRestoreDatabase{OrgSlug: "acme", Generation: uuid.NewString(), Instance: "local", Created: true, OID: 42}
			d.Name = postgresRestoreDatabaseName(d.Generation)
			j := &restoreJournal{ID: "journal", ArchiveSHA256: "hash", PlatformDSN: s.platformDSN, Phase: restoreSQLImporting, PostgresDatabases: []postgresRestoreDatabase{d}}
			fake := &postgresReceiptSQL{query: func(q string, a []driver.NamedValue) ([]driver.Value, error) {
				oid := d.OID
				comment := postgresRestoreReceipt(j, d)
				if scenario == "wrong oid" {
					oid++
				}
				if scenario == "wrong comment" {
					comment = "foreign"
				}
				return []driver.Value{oid, comment}, nil
			}}
			db := sql.OpenDB(&stagedRouteSQL{postgresReceiptSQL: fake, published: scenario == "published"})
			defer db.Close()
			s.platformDB = db
			switch scenario {
			case "foreign instance":
				d.Instance = "remote"
			case "not created":
				d.Created = false
			case "cleaned":
				d.Cleaned = true
			case "ready":
				d.Ready = true
			case "zero oid":
				d.OID = 0
			case "unrecorded":
				j.PostgresDatabases = nil
			case "wrong phase":
				j.Phase = restoreCommitted
			}
			if scenario == "nil journal" {
				j = nil
			}
			err := s.checkStagedPostgresOrg(context.Background(), j, d)
			if scenario == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("expected conflict: %v", err)
			}
			_, err = s.openStagedPostgresOrg(context.Background(), j, d)
			if !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("opener accepted invalid receipt: %v", err)
			}
			for _, q := range fake.events {
				if !strings.HasPrefix(q, "SELECT ") {
					t.Fatalf("mutated platform: %s", q)
				}
			}
		})
	}
}
