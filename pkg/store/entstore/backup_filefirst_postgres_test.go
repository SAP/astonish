package entstore

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/pgutil"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

// postgresDatabaseBackupStore deliberately requires TWO opt-ins. Merely having
// a DSN in the environment must never cause database creation/drop in unit runs.
// ASTONISH_TEST_DSN must identify a disposable admin-capable instance, not production.
func postgresDatabaseBackupStore(t *testing.T) *Store {
	t.Helper()
	if os.Getenv("ASTONISH_TEST_POSTGRES_DATABASE_BACKUP") != "1" || os.Getenv("ASTONISH_TEST_DSN") == "" {
		t.Skip("live PostgreSQL unverified: requires ASTONISH_TEST_POSTGRES_DATABASE_BACKUP=1 and a safe ASTONISH_TEST_DSN")
	}
	base := os.Getenv("ASTONISH_TEST_DSN")
	suffix := "backup_db_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	platformName := "astonish_" + suffix + "_platform"
	dsn, err := pgutil.ReplaceDSNDatabase(base, platformName)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{DSN: dsn, InstanceSuffix: suffix, DataDir: t.TempDir()}
	if err = BootstrapPlatform(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	_, s, err := NewPlatformServices(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Include receipt-owned staging databases published by file-first restore.
		names := []string{platformName, s.orgDBName("restorepg")}
		rows, queryErr := s.platformDB.Query("SELECT db_name FROM organizations")
		if queryErr != nil {
			t.Error(queryErr)
		} else {
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					t.Error(err)
				} else {
					names = append(names, name)
				}
			}
			if err := rows.Err(); err != nil {
				t.Error(err)
			}
			_ = rows.Close()
		}
		s.orgClients.Range(func(key, value any) bool {
			if err := value.(*orgDataStore).Close(); err != nil {
				t.Error(err)
			}
			s.orgClients.Delete(key)
			return true
		})
		if err := s.Close(); err != nil {
			t.Error(err)
		}
		adminDSN, err := pgutil.ReplaceDSNDatabase(base, "postgres")
		if err != nil {
			t.Error(err)
			return
		}
		admin, err := sql.Open("pgx", adminDSN)
		if err != nil {
			t.Error(err)
			return
		}
		defer admin.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		seen := map[string]bool{}
		for _, name := range names {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			if _, err := admin.ExecContext(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()", name); err != nil {
				t.Error(err)
				continue
			}
			if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(DialectPostgres, name)); err != nil {
				t.Error(err)
			}
		}
	})
	return s
}

// TestIntegrationPostgresBotDatabaseBackupRestore exercises the actual SQL
// export/verify/restore boundary, not the connector-local receipt mocks below.
func TestIntegrationPostgresBotDatabaseBackupRestore(t *testing.T) {
	for _, kind := range []string{"personal", "team"} {
		t.Run(kind, func(t *testing.T) {
			source := postgresDatabaseBackupStore(t)
			ctx := t.Context()
			owner, orgID, bot, session := uuid.NewString(), uuid.NewString(), uuid.New(), uuid.NewString()
			if err := source.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@example.test", DisplayName: "Backup", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := source.Organizations().Create(ctx, &store.Organization{ID: orgID, Slug: "restorepg", Name: "Backup", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := source.Organizations().AddMember(ctx, owner, orgID, "owner"); err != nil {
				t.Fatal(err)
			}
			if err := source.ProvisionOrg(ctx, orgID, "restorepg"); err != nil {
				t.Fatal(err)
			}
			org, err := source.ForOrg("restorepg")
			if err != nil {
				t.Fatal(err)
			}
			if err := org.Teams().CreateTeam(ctx, &store.Team{ID: uuid.NewString(), Slug: "ops", Name: "Ops", SchemaName: teamSchemaName("ops")}); err != nil {
				t.Fatal(err)
			}
			if err := org.ProvisionTeam(ctx, "ops"); err != nil {
				t.Fatal(err)
			}
			if err := org.ProvisionPersonalSchema(ctx, owner); err != nil {
				t.Fatal(err)
			}
			schema := teamSchemaName("ops")
			if kind == "personal" {
				if err := org.ProvisionPersonalSchema(ctx, owner); err != nil {
					t.Fatal(err)
				}
				schema = personalSchemaName(owner)
			}
			db, err := source.openRestorePostgresDB("restorepg", "")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			// Dedicated connection ensures every seeded row uses the selected scope.
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err = conn.ExecContext(ctx, "SET search_path TO "+quoteIdent(DialectPostgres, schema)); err != nil {
				t.Fatal(err)
			}
			home := store.BotHomeBinding{ID: uuid.New(), BotID: bot, Scope: store.BotScope(kind), Backend: "docker", StorageKey: "test-owned-home", Revision: 1}
			binding := sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: "restorepg", TeamID: "ops", UserID: owner, BotID: bot.String()}, Backend: sandbox.BackendKindDocker, Ref: home.StorageKey}
			metadata, _ := json.Marshal(map[string]any{botContentHomeSelectionKey: botContentHomeSelection{Home: home, Binding: binding}})
			homeRaw, _ := json.Marshal(home)
			if _, err = conn.ExecContext(ctx, `INSERT INTO bots(id,owner_id,name,status,revision,version,schema_version,home_session_id,home_binding,create_key,last_seq,fence,data,cleanup_complete,created_at,updated_at) VALUES($1,$2,'Backup','paused',1,1,1,$3,$4,$5,0,0,$6,false,now(),now())`, bot.String(), owner, session, string(homeRaw), uuid.NewString(), string(metadata)); err != nil {
				t.Fatal(err)
			}
			if _, err = conn.ExecContext(ctx, `INSERT INTO sessions(id,user_id,bot_id,purpose,retention,title,metadata,created_at,updated_at) VALUES($1,$2,$3,'bot_home','bot','Backup','{}',now(),now())`, session, owner, bot.String()); err != nil {
				t.Fatal(err)
			}
			scope := store.BotContentScope{OrgID: "restorepg", TeamID: "ops", OwnerID: owner, BotID: bot}
			if kind == "personal" {
				scope.TeamID = schema
			}
			codec := newBotContentCodec(filepath.Join(source.dataDir, "bot-content"), scope)
			version := store.DefaultBotVersion()
			version.BotID = bot
			version.Version = 1
			version.Charter = "retained version"
			version.CreatedBy = uuid.MustParse(owner)
			data, err := codec.encodeVersion(ctx, version)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(data)
			if _, err = conn.ExecContext(ctx, `INSERT INTO bot_versions(id,bot_id,version,schema_version,definition,content_hash,created_by,change_reason,created_at) VALUES($1,$2,1,1,$3,'hash',$4,'',now())`, uuid.NewString(), bot.String(), string(raw), owner); err != nil {
				t.Fatal(err)
			}
			disk, err := newBotDiskContent(filepath.Join(source.dataDir, "bot-content"), scope)
			if err != nil {
				t.Fatal(err)
			}
			defer disk.Close()
			artifactBytes := []byte{'P', 'D', 'F', 0, 255, 1, 2, 3}
			ref, err := disk.Put(ctx, store.BotContentArtifact, 1, int64(len(artifactBytes)), bytes.NewReader(artifactBytes), contentHomeCommit)
			if err != nil {
				t.Fatal(err)
			}
			candidateRef, err := disk.Put(ctx, store.BotContentArtifact, 1, 4, strings.NewReader("work"), contentHomeCommit)
			if err != nil {
				t.Fatal(err)
			}
			run := botStoredRun{Run: store.BotRun{ID: uuid.New(), BotID: bot, Version: 1, SessionID: uuid.MustParse(session), Status: store.RunSucceeded, Lease: store.BotLease{WorkerID: "expired-source-worker", Fence: 7, ExpiresAt: time.Now().Add(-time.Hour)}, Report: store.BotReport{Summary: "deliverable", Artifacts: []store.BotArtifact{{ContentRef: ref, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}}}, Checkpoint: json.RawMessage(`{ "opaque": 9007199254740993 }`)}, ExternalAdmissionValidated: true, Work: &store.BotWorkState{Candidate: &store.BotWorkCandidate{Artifacts: []store.BotArtifact{{ContentRef: candidateRef, SHA256: candidateRef.SHA256, SizeBytes: candidateRef.SizeBytes}}}}}
			data, err = codec.encodeRun(ctx, run)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ = json.Marshal(data)
			if _, err = conn.ExecContext(ctx, `INSERT INTO bot_runs(id,bot_id,version,session_id,idempotency_key,request_hash,status,fence,worker_id,data) VALUES($1,$2,1,$3,'backup','hash','succeeded',7,'expired-source-worker',$4)`, run.Run.ID.String(), bot.String(), session, string(raw)); err != nil {
				t.Fatal(err)
			}
			initial := adksession.NewEvent("initial-state")
			initial.ID = "initial-state"
			initial.Actions.StateDelta = map[string]any{"state": "retained"}
			state, err := codec.encodeSessionEvent(ctx, bot, session, initial)
			if err != nil {
				t.Fatal(err)
			}
			stateRaw, _ := json.Marshal(map[string]any{"bot_initial_state": state})
			if _, err = conn.ExecContext(ctx, "UPDATE sessions SET metadata=$1 WHERE id=$2", string(stateRaw), session); err != nil {
				t.Fatal(err)
			}
			backend := &fileFirstTestBackend{contentHomeBackend: &contentHomeBackend{objects: map[string][]byte{}}, home: []byte("retained home bytes")}
			exportOpts := PlatformBackupExportOptions{Backend: "postgres", FileBackend: func(context.Context) (BotBackupBackend, error) { return backend, nil }}
			var record botRunRecord
			if err := decodeBotRecord(data, &record); err != nil {
				t.Fatal(err)
			}
			t.Run("corrupt-inline", func(t *testing.T) {
				bad := record
				bad.Inline = append([]byte(nil), record.Inline...)
				bad.Inline[0] ^= 1
				badData, err := encodeRuntimeRecord(&bad)
				if err != nil {
					t.Fatal(err)
				}
				badRaw, _ := json.Marshal(badData)
				if _, err = conn.ExecContext(ctx, "UPDATE bot_runs SET data=$1 WHERE id=$2", string(badRaw), run.Run.ID.String()); err != nil {
					t.Fatal(err)
				}
				exportErr := source.ExportPlatformBackup(ctx, filepath.Join(t.TempDir(), "rejected"), exportOpts)
				if _, err = conn.ExecContext(ctx, "UPDATE bot_runs SET data=$1 WHERE id=$2", string(raw), run.Run.ID.String()); err != nil {
					t.Fatal(err)
				}
				if exportErr == nil {
					t.Fatal("invalid database runtime exported")
				}
			})
			archive := filepath.Join(t.TempDir(), "archive")
			if err = source.ExportPlatformBackup(ctx, archive, exportOpts); err != nil {
				t.Fatal(err)
			}
			if _, err = VerifyPlatformBackup(ctx, archive, backup.ReaderOptions{}); err != nil {
				t.Fatal(err)
			}
			target := postgresDatabaseBackupStore(t)
			restore := &fileFirstRestoreTestBackend{fileFirstTestBackend: backend}
			opts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return restore, nil }}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err = target.RestorePlatformBackup(canceled, archive, opts); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled restore: %v", err)
			}
			interrupted := &recoveryRestoreBackend{fileFirstRestoreTestBackend: restore, fail: true}
			interruptedOpts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return interrupted, nil }}
			if _, err = target.RestorePlatformBackup(ctx, archive, interruptedOpts); err == nil {
				t.Fatal("interrupted restore succeeded")
			}
			orgs, err := target.Organizations().List(ctx)
			if err != nil || len(orgs) != 0 {
				t.Fatalf("partial publication: %v %v", orgs, err)
			}
			if err = target.CleanupFileFirstRestore(ctx, archive, interruptedOpts); err != nil {
				t.Fatal(err)
			}
			if _, err = target.RestorePlatformBackup(ctx, archive, opts); err != nil {
				t.Fatal(err)
			}
			// File-first publishes a receipt-owned generation, not the old org name.
			var targetName string
			if err = target.platformDB.QueryRowContext(ctx, "SELECT db_name FROM organizations WHERE slug=$1", "restorepg").Scan(&targetName); err != nil {
				t.Fatal(err)
			}
			targetDB, err := target.openRestorePostgresDB("restorepg", targetName)
			if err != nil {
				t.Fatal(err)
			}
			defer targetDB.Close()
			q := func(table string) string {
				return quoteIdent(DialectPostgres, schema) + "." + quoteIdent(DialectPostgres, table)
			}
			var status, restoredRun, restoredState, worker string
			if err = targetDB.QueryRowContext(ctx, "SELECT status FROM "+q("bots")).Scan(&status); err != nil || status != "paused" {
				t.Fatalf("paused restore: %s %v", status, err)
			}
			if err = targetDB.QueryRowContext(ctx, "SELECT data,worker_id FROM "+q("bot_runs")+" WHERE id=$1", run.Run.ID.String()).Scan(&restoredRun, &worker); err != nil {
				t.Fatal(err)
			}
			var mapped map[string]any
			if err = json.Unmarshal([]byte(restoredRun), &mapped); err != nil {
				t.Fatal(err)
			}
			targetCodec := newBotContentCodec(filepath.Join(target.dataDir, "bot-content"), scope)
			got, err := targetCodec.decodeRun(ctx, bot, mapped)
			if err != nil {
				t.Fatal(err)
			}
			if worker != "" || got.ExternalAdmissionValidated || got.Run.Lease.WorkerID != "" || !got.Run.Lease.ExpiresAt.IsZero() || !bytes.Equal(got.Run.Checkpoint, run.Run.Checkpoint) {
				t.Fatal("restored authority or opaque checkpoint changed")
			}
			if len(got.Run.Report.Artifacts) != 1 || got.Work == nil || got.Work.Candidate == nil || len(got.Work.Candidate.Artifacts) != 1 {
				t.Fatal("report/candidate closure missing")
			}
			targetDisk, err := newBotDiskContent(filepath.Join(target.dataDir, "bot-content"), scope)
			if err != nil {
				t.Fatal(err)
			}
			defer targetDisk.Close()
			for _, expected := range []struct {
				ref  *store.BotContentRef
				body []byte
			}{{got.Run.Report.Artifacts[0].ContentRef, artifactBytes}, {got.Work.Candidate.Artifacts[0].ContentRef, []byte("work")}} {
				if expected.ref == nil {
					t.Fatal("artifact reference missing")
				}
				stream, err := targetDisk.Open(ctx, *expected.ref, store.BotFileRange{})
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(stream)
				closeErr := stream.Close()
				if readErr != nil || closeErr != nil || !bytes.Equal(body, expected.body) {
					t.Fatalf("artifact bytes changed: %v %v", readErr, closeErr)
				}
			}
			if err = targetDB.QueryRowContext(ctx, "SELECT metadata FROM "+q("sessions")+" WHERE id=$1", session).Scan(&restoredState); err != nil {
				t.Fatal(err)
			}
			var meta map[string]any
			if err = json.Unmarshal([]byte(restoredState), &meta); err != nil {
				t.Fatal(err)
			}
			stateData, ok := meta["bot_initial_state"].(map[string]any)
			if !ok {
				t.Fatal("initial state missing")
			}
			gotInitial, err := targetCodec.decodeSessionEvent(ctx, bot, session, stateData)
			if err != nil || gotInitial.ID != initial.ID || gotInitial.Actions.StateDelta["state"] != "retained" {
				t.Fatalf("initial state changed: %v", err)
			}
			foreign := scope
			foreign.OwnerID = uuid.NewString()
			if _, err = newBotContentCodec(t.TempDir(), foreign).decodeRun(ctx, bot, mapped); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("cross-scope runtime visible: %v", err)
			}
			otherSchema := teamSchemaName("ops")
			if kind == "team" {
				otherSchema = personalSchemaName(owner)
			}
			var count int
			//nolint:gosec // G201: schema name is from quoteIdent (validated identifier), bot_id is parameterized; test-only query.
			query := fmt.Sprintf("SELECT count(*) FROM %s.bot_runs WHERE bot_id=$1", quoteIdent(DialectPostgres, otherSchema))
			if err = targetDB.QueryRowContext(ctx, query, bot.String()).Scan(&count); err != nil || count != 0 {
				t.Fatalf("restored cross-scope row leakage: %d %v", count, err)
			}
		})
	}
}

// A connector-local driver: no network, no shared registered driver, no DSN.
type postgresReceiptSQL struct {
	exec        func(string, []driver.NamedValue) error
	query       func(string, []driver.NamedValue) ([]driver.Value, error)
	events      []string
	commitError error
}

func (m *postgresReceiptSQL) Connect(context.Context) (driver.Conn, error) { return m, nil }
func (m *postgresReceiptSQL) Driver() driver.Driver                        { return postgresReceiptDriver{} }

type postgresReceiptDriver struct{}

func (postgresReceiptDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("network prohibited")
}
func (m *postgresReceiptSQL) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (m *postgresReceiptSQL) Close() error { return nil }
func (m *postgresReceiptSQL) Begin() (driver.Tx, error) {
	m.events = append(m.events, "begin")
	return m, nil
}
func (m *postgresReceiptSQL) Commit() error {
	m.events = append(m.events, "commit")
	return m.commitError
}
func (m *postgresReceiptSQL) Rollback() error { m.events = append(m.events, "rollback"); return nil }
func (m *postgresReceiptSQL) ExecContext(_ context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	m.events = append(m.events, q)
	if m.exec != nil {
		if err := m.exec(q, a); err != nil {
			return nil, err
		}
	}
	return driver.RowsAffected(1), nil
}
func (m *postgresReceiptSQL) QueryContext(_ context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	m.events = append(m.events, q)
	if strings.Contains(q, "WHERE db_name = $1") {
		return &postgresReceiptRows{values: []driver.Value{int64(0)}}, nil
	}
	if m.query == nil {
		return &postgresReceiptRows{values: []driver.Value{int64(0)}}, nil
	}
	values, err := m.query(q, a)
	if err != nil {
		return nil, err
	}
	return &postgresReceiptRows{values: values}, nil
}

type postgresReceiptRows struct {
	values []driver.Value
	done   bool
}

func (r *postgresReceiptRows) Columns() []string {
	if len(r.values) == 2 {
		return []string{"oid", "comment"}
	}
	return []string{"count"}
}
func (r *postgresReceiptRows) Close() error { return nil }
func (r *postgresReceiptRows) Next(v []driver.Value) error {
	if r.done || r.values == nil {
		return io.EOF
	}
	copy(v, r.values)
	r.done = true
	return nil
}

func postgresReceiptFixture(t *testing.T) (*Store, *os.Root, *restoreJournal, *postgresReceiptSQL) {
	t.Helper()
	m := &postgresReceiptSQL{}
	db := sql.OpenDB(m)
	t.Cleanup(func() { db.Close() })
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	if err = root.Mkdir(restoreJournalDir, 0700); err != nil {
		t.Fatal(err)
	}
	s := &Store{dialect: DialectPostgres, platformDB: db, platformDSN: "mock-instance", instanceSuffix: "test"}
	j := &restoreJournal{Version: 1, ID: uuid.NewString(), PlatformDSN: s.platformDSN, ArchiveSHA256: strings.Repeat("a", 64), Phase: restoreFilesReady}
	if err = writeRestoreJournal(root, j); err != nil {
		t.Fatal(err)
	}
	return s, root, j, m
}
func readPostgresReceiptJournal(t *testing.T, root *os.Root) restoreJournal {
	t.Helper()
	raw, err := root.ReadFile(restoreJournalDir + "/journal.json")
	if err != nil {
		t.Fatal(err)
	}
	var j restoreJournal
	if err = json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	return j
}
func acknowledgePostgresCreate(t *testing.T, root *os.Root, j *restoreJournal, m *postgresReceiptSQL) {
	t.Helper()
	m.exec = func(q string, _ []driver.NamedValue) error {
		if strings.HasPrefix(q, "CREATE DATABASE") {
			saved := readPostgresReceiptJournal(t, root)
			if len(saved.PostgresDatabases) == 0 || saved.PostgresDatabases[len(saved.PostgresDatabases)-1].Created {
				t.Fatal("CREATE before durable reservation")
			}
		}
		return nil
	}
	m.query = func(string, []driver.NamedValue) ([]driver.Value, error) {
		d := j.PostgresDatabases[len(j.PostgresDatabases)-1]
		return []driver.Value{int64(123), postgresRestoreReceipt(j, d)}, nil
	}
}
func TestPostgresFileFirstReceiptBinding(t *testing.T) {
	s, _, j, _ := postgresReceiptFixture(t)
	generation := uuid.NewString()
	good := postgresRestoreDatabase{OrgSlug: "demo", Generation: generation, Name: postgresRestoreDatabaseName(generation), Instance: s.instanceSuffix}
	if err := validatePostgresRestoreDatabase(s, j, good); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*postgresRestoreDatabase){
		func(d *postgresRestoreDatabase) { d.Instance = "foreign" },
		func(d *postgresRestoreDatabase) { d.Name = s.orgDBName("demo") },
		func(d *postgresRestoreDatabase) { d.Generation = "../outside" },
	} {
		bad := good
		change(&bad)
		if err := validatePostgresRestoreDatabase(s, j, bad); !errors.Is(err, store.ErrBotConflict) {
			t.Fatal(err)
		}
	}
	j.PlatformDSN = "foreign"
	if err := validatePostgresRestoreDatabase(s, j, good); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
}

func TestPostgresFileFirstReceiptReservedBeforeCreate(t *testing.T) {
	s, root, j, m := postgresReceiptFixture(t)
	acknowledgePostgresCreate(t, root, j, m)
	i, err := s.allocatePostgresRestoreDatabase(t.Context(), root, j, "demo")
	if err != nil {
		t.Fatal(err)
	}
	d := readPostgresReceiptJournal(t, root).PostgresDatabases[i]
	if !d.Created || d.OID != 123 || d.Name == s.orgDBName("demo") {
		t.Fatalf("invalid receipt: %+v", d)
	}
	before := len(m.events)
	if _, err = s.allocatePostgresRestoreDatabase(t.Context(), root, j, "demo"); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
	if len(m.events) != before {
		t.Fatal("duplicate allocation touched SQL")
	}
}
func TestPostgresFileFirstNeverAdoptsExistingOrAmbiguousCreate(t *testing.T) {
	for _, failure := range []string{"already exists", "connection lost after CREATE"} {
		t.Run(failure, func(t *testing.T) {
			s, root, j, m := postgresReceiptFixture(t)
			m.exec = func(string, []driver.NamedValue) error { return errors.New(failure) }
			if _, err := s.allocatePostgresRestoreDatabase(t.Context(), root, j, "demo"); err == nil {
				t.Fatal("adopted target")
			}
			saved := readPostgresReceiptJournal(t, root)
			if len(saved.PostgresDatabases) != 1 || saved.PostgresDatabases[0].Created {
				t.Fatalf("receipt %+v", saved)
			}
			m.query = func(string, []driver.NamedValue) ([]driver.Value, error) {
				return []driver.Value{int64(99), "someone else"}, nil
			}
			if err := s.cleanupPostgresRestoreDatabases(t.Context(), root, j); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal(err)
			}
			for _, q := range m.events {
				if strings.HasPrefix(q, "DROP") || strings.HasPrefix(q, "COMMENT") {
					t.Fatal(q)
				}
			}
		})
	}
}
func TestPostgresFileFirstCleanupExactReceiptAndRetry(t *testing.T) {
	s, root, j, m := postgresReceiptFixture(t)
	acknowledgePostgresCreate(t, root, j, m)
	if _, err := s.allocatePostgresRestoreDatabase(t.Context(), root, j, "demo"); err != nil {
		t.Fatal(err)
	}
	d := j.PostgresDatabases[0]
	for _, values := range [][]driver.Value{{int64(124), postgresRestoreReceipt(j, d)}, {int64(123), "foreign receipt"}} {
		m.query = func(string, []driver.NamedValue) ([]driver.Value, error) { return values, nil }
		if err := s.cleanupPostgresRestoreDatabases(t.Context(), root, j); !errors.Is(err, store.ErrBotConflict) {
			t.Fatal(err)
		}
	}
	m.query = func(string, []driver.NamedValue) ([]driver.Value, error) {
		return []driver.Value{int64(123), postgresRestoreReceipt(j, d)}, nil
	}
	m.exec = func(q string, _ []driver.NamedValue) error {
		if strings.HasPrefix(q, "DROP") {
			return errors.New("attached")
		}
		return nil
	}
	if err := s.cleanupPostgresRestoreDatabases(t.Context(), root, j); err == nil {
		t.Fatal("ignored attached database")
	}
	if readPostgresReceiptJournal(t, root).PostgresDatabases[0].Cleaned {
		t.Fatal("receipt lost")
	}
	m.exec = nil
	if err := s.cleanupPostgresRestoreDatabases(t.Context(), root, j); err != nil {
		t.Fatal(err)
	}
	before := len(m.events)
	if err := s.cleanupPostgresRestoreDatabases(t.Context(), root, j); err != nil || len(m.events) != before {
		t.Fatal("cleanup not idempotent", err)
	}
	j.Phase = restoreCommitted
	if err := s.cleanupPostgresRestoreDatabases(t.Context(), root, j); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
}

type postgresStageFixture struct {
	t         *testing.T
	imported  []string
	fail      bool
	published bool
}

func (f *postgresStageFixture) ImportTenant(_ context.Context, d postgresRestoreDatabase, scopes []backup.Scope, _ PlatformRestoreOptions, _ *backup.RestoreResult) error {
	if d.Ready || !d.Created || d.OID == 0 || len(scopes) == 0 {
		f.t.Fatal("invalid tenant admission")
	}
	f.imported = append(f.imported, d.OrgSlug)
	if f.fail {
		return errors.New("tenant failed")
	}
	return nil
}
func (f *postgresStageFixture) PublishPlatform(_ context.Context, ds []postgresRestoreDatabase, _ PlatformRestoreOptions, _ *backup.RestoreResult, j *restoreJournal) error {
	if len(f.imported) != len(ds) {
		f.t.Fatal("published too early")
	}
	for _, d := range ds {
		if !d.Ready {
			f.t.Fatal("unready published")
		}
	}
	f.published = true
	j.Phase = restoreCommitted
	return nil
}
func TestPostgresFileFirstTenantBeforePublication(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "tenant rollback"}[failure], func(t *testing.T) {
			s, root, j, m := postgresReceiptFixture(t)
			acknowledgePostgresCreate(t, root, j, m)
			stage := &postgresStageFixture{t: t, fail: failure}
			opts := PlatformRestoreOptions{fileFirst: &fileFirstRestoreState{}}
			plan := backup.RestorePlan{Archive: backup.Summary{Manifest: backup.Manifest{Scopes: []backup.Scope{{Kind: "platform"}, {Kind: "org", OrgSlug: "first"}, {Kind: "org", OrgSlug: "second"}}}}}
			_, err := s.importFileFirstPostgresStaged(t.Context(), opts, plan, root, j, stage)
			if failure {
				if err == nil || stage.published {
					t.Fatal("partial publication")
				}
				saved := readPostgresReceiptJournal(t, root)
				if saved.Phase != restoreFailed || len(saved.PostgresDatabases) != 1 || !saved.PostgresDatabases[0].Created {
					t.Fatal("failure discarded recovery receipt")
				}
			} else {
				if err != nil || !stage.published || readPostgresReceiptJournal(t, root).Phase != restoreCommitted {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestPostgresFileFirstPublicationRollbackAndAmbiguousCommit(t *testing.T) {
	for _, mode := range []string{"success", "insert-error", "commit-error", "target-populated"} {
		t.Run(mode, func(t *testing.T) {
			s, root, j, m := postgresReceiptFixture(t)
			j.Phase = restoreSQLImporting
			generation := uuid.NewString()
			j.PostgresDatabases = []postgresRestoreDatabase{{OrgSlug: "demo", Generation: generation, Name: postgresRestoreDatabaseName(generation), Instance: s.instanceSuffix, Created: true, Ready: true, OID: 123}}
			opts := PlatformRestoreOptions{fileFirst: &fileFirstRestoreState{mapping: newFileFirstMapping(), files: map[string][]byte{"platform/organizations.jsonl": []byte(`{"entity":"organizations","data":{"id":"00000000-0000-0000-0000-000000000001","slug":"demo","db_name":"source"}}` + "\n")}}}
			plan := backup.RestorePlan{Archive: backup.Summary{Manifest: backup.Manifest{Entries: []backup.Entry{{Path: "platform/organizations.jsonl", Kind: "jsonl", Entity: "organizations", Scope: backup.Scope{Kind: "platform"}}}}}}
			m.exec = func(q string, a []driver.NamedValue) error {
				if strings.HasPrefix(q, "INSERT") {
					found := false
					for _, v := range a {
						if v.Value == j.PostgresDatabases[0].Name {
							found = true
						}
					}
					if !found {
						t.Fatal("original database route published")
					}
					if mode == "insert-error" {
						return errors.New("insert failure")
					}
				}
				return nil
			}
			m.query = func(q string, _ []driver.NamedValue) ([]driver.Value, error) {
				if strings.Contains(q, "FROM pg_database") {
					return []driver.Value{int64(123), postgresRestoreReceipt(j, j.PostgresDatabases[0])}, nil
				}
				if mode == "target-populated" {
					return []driver.Value{int64(1)}, nil
				}
				return []driver.Value{int64(0)}, nil
			}
			if mode == "commit-error" {
				m.commitError = errors.New("lost commit ack")
			}
			err := s.publishFileFirstPostgresPlatform(t.Context(), root, j, opts, plan, &backup.RestoreResult{})
			events := strings.Join(m.events, ";")
			if !strings.Contains(events, "LOCK TABLE") {
				t.Fatal("no write exclusion")
			}
			if mode == "insert-error" || mode == "target-populated" {
				if err == nil || strings.Contains(events, "commit") || !strings.Contains(events, "rollback") {
					t.Fatal(err, events)
				}
			} else {
				if (err != nil) != (mode == "commit-error") || readPostgresReceiptJournal(t, root).Phase != restoreCommitted {
					t.Fatal(err)
				}
				if err := s.cleanupPostgresRestoreDatabases(t.Context(), root, j); !errors.Is(err, store.ErrBotConflict) {
					t.Fatal("ambiguous commit cleanup allowed", err)
				}
			}
		})
	}
}
