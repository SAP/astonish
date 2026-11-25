package entstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

func TestBotDatabaseBackupRestore(t *testing.T) {
	for _, kind := range []string{"personal", "team"} {
		t.Run(kind, func(t *testing.T) {
			src, path, db, backend := fileFirstExportFixture(t, kind)
			var bot, owner, session string
			if err := db.QueryRow("SELECT id,owner_id,home_session_id FROM bots").Scan(&bot, &owner, &session); err != nil {
				t.Fatal(err)
			}
			scope := store.BotContentScope{OrgID: "archive", TeamID: "original", OwnerID: owner, BotID: uuid.MustParse(bot)}
			if kind == "personal" {
				scope.TeamID = personalSchemaName(owner)
			}
			codec := newBotContentCodec(filepath.Join(src.dataDir, "bot-content"), scope)
			artifactBytes := []byte{'P', 'D', 'F', 0, 255, 1, 2, 3}
			disk, err := newBotDiskContent(filepath.Join(src.dataDir, "bot-content"), scope)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := disk.Put(t.Context(), store.BotContentArtifact, 1, int64(len(artifactBytes)), bytes.NewReader(artifactBytes), contentHomeCommit)
			if err != nil {
				t.Fatal(err)
			}
			_ = disk.Close()
			run := botStoredRun{Run: store.BotRun{ID: uuid.New(), BotID: scope.BotID, Version: 1, SessionID: uuid.MustParse(session), Status: store.RunSucceeded, Report: store.BotReport{Summary: "saved deliverable", Artifacts: []store.BotArtifact{{ContentRef: ref, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}}}, Checkpoint: json.RawMessage(`{ "arguments": "opaque" }`)}, ExternalAdmissionValidated: true}
			data, err := codec.encodeRun(t.Context(), run)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(data)
			if _, err = db.Exec(`INSERT INTO bot_runs (id,bot_id,version,session_id,idempotency_key,request_hash,status,fence,worker_id,data) VALUES (?,?,1,?,'backup','hash','succeeded',0,'',?)`, run.Run.ID.String(), bot, session, string(raw)); err != nil {
				t.Fatal(err)
			}
			initial := adksession.NewEvent("initial-state")
			initial.ID = "initial-state"
			initial.Actions.StateDelta = map[string]any{"state": "retained"}
			state, err := codec.encodeSessionEvent(t.Context(), scope.BotID, session, initial)
			if err != nil {
				t.Fatal(err)
			}
			metadata, _ := json.Marshal(map[string]any{"bot_initial_state": state})
			if _, err = db.Exec("UPDATE sessions SET metadata=? WHERE id=?", string(metadata), session); err != nil {
				t.Fatal(err)
			}
			// Fail closed at the real export boundary, not merely a helper decoder.
			var invalid botRunRecord
			if err = decodeBotRecord(data, &invalid); err != nil {
				t.Fatal(err)
			}
			t.Run("corrupt-inline", func(t *testing.T) {
				bad := invalid
				bad.Inline = append([]byte(nil), invalid.Inline...)
				bad.Inline[0] ^= 1
				badData, marshalErr := encodeRuntimeRecord(&bad)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				badRaw, _ := json.Marshal(badData)
				if _, err := db.Exec("UPDATE bot_runs SET data=? WHERE id=?", string(badRaw), run.Run.ID.String()); err != nil {
					t.Fatal(err)
				}
				exportErr := src.ExportPlatformBackup(t.Context(), filepath.Join(t.TempDir(), "rejected"), PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return backend, nil }})
				if _, err := db.Exec("UPDATE bot_runs SET data=? WHERE id=?", string(raw), run.Run.ID.String()); err != nil {
					t.Fatal(err)
				}
				if exportErr == nil {
					t.Fatal("invalid database runtime exported")
				}
			})
			archive := filepath.Join(t.TempDir(), "archive")
			if err = src.ExportPlatformBackup(t.Context(), archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return backend, nil }}); err != nil {
				t.Fatal(err)
			}
			if _, err = VerifyPlatformBackup(t.Context(), archive, backup.ReaderOptions{}); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			_, target, err := NewPlatformServices(t.Context(), Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			restore := &fileFirstRestoreTestBackend{fileFirstTestBackend: backend}
			opts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return restore, nil }}
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err = target.RestorePlatformBackup(canceled, archive, opts); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled restore: %v", err)
			}
			interrupted := &recoveryRestoreBackend{fileFirstRestoreTestBackend: restore, fail: true}
			interruptedOpts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return interrupted, nil }}
			if _, err = target.RestorePlatformBackup(t.Context(), archive, interruptedOpts); err == nil {
				t.Fatal("interrupted restore succeeded")
			}
			orgs, err := target.Organizations().List(t.Context())
			if err != nil || len(orgs) != 0 {
				t.Fatalf("interrupted restore published tenants: %v %v", orgs, err)
			}
			if err = target.CleanupFileFirstRestore(t.Context(), archive, interruptedOpts); err != nil {
				t.Fatal(err)
			}
			if _, err = target.RestorePlatformBackup(t.Context(), archive, opts); err != nil {
				t.Fatal(err)
			}
			rel, _ := filepath.Rel(src.dataDir, path)
			targetDB, err := sql.Open("sqlite", filepath.Join(dir, rel))
			if err != nil {
				t.Fatal(err)
			}
			defer targetDB.Close()
			var status, restoredRun, restoredState string
			if err = targetDB.QueryRow("SELECT status FROM bots").Scan(&status); err != nil || status != "paused" {
				t.Fatalf("paused restore: %s %v", status, err)
			}
			if err = targetDB.QueryRow("SELECT data FROM bot_runs WHERE id=?", run.Run.ID.String()).Scan(&restoredRun); err != nil {
				t.Fatal(err)
			}
			var mapped map[string]any
			if err = json.Unmarshal([]byte(restoredRun), &mapped); err != nil {
				t.Fatal(err)
			}
			got, err := newBotContentCodec(filepath.Join(dir, "bot-content"), scope).decodeRun(t.Context(), scope.BotID, mapped)
			if err != nil {
				t.Fatal(err)
			}
			if got.ExternalAdmissionValidated || got.Run.Lease.WorkerID != "" || !bytes.Equal(got.Run.Checkpoint, run.Run.Checkpoint) {
				t.Fatal("authority or opaque checkpoint changed")
			}
			targetDisk, err := newBotDiskContent(filepath.Join(dir, "bot-content"), scope)
			if err != nil {
				t.Fatal(err)
			}
			defer targetDisk.Close()
			stream, err := targetDisk.Open(t.Context(), *got.Run.Report.Artifacts[0].ContentRef, store.BotFileRange{})
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(stream)
			if err != nil {
				t.Fatal(err)
			}
			_ = stream.Close()
			if !bytes.Equal(body, artifactBytes) {
				t.Fatal("artifact bytes changed")
			}
			if err = targetDB.QueryRow("SELECT metadata FROM sessions WHERE id=?", session).Scan(&restoredState); err != nil {
				t.Fatal(err)
			}
			var meta map[string]any
			if err = json.Unmarshal([]byte(restoredState), &meta); err != nil {
				t.Fatal(err)
			}
			stateData, ok := meta["bot_initial_state"].(map[string]any)
			if !ok {
				t.Fatal("restored initial state missing")
			}
			restoredInitial, err := newBotContentCodec(filepath.Join(dir, "bot-content"), scope).decodeSessionEvent(t.Context(), scope.BotID, session, stateData)
			if err != nil || restoredInitial.ID != initial.ID || restoredInitial.Actions.StateDelta["state"] != "retained" {
				t.Fatalf("restored initial state changed: %v", err)
			}
			inv, _ := newBotBackupInventory([]store.BotContentScope{scope})
			if err = inv.rootsFromSession(scope.BotID, session, meta); err != nil || len(inv.objects) != 0 {
				t.Fatalf("inline initial state: %v", err)
			}
			foreign := scope
			foreign.OwnerID = uuid.NewString()
			if _, err = newBotContentCodec(t.TempDir(), foreign).decodeRun(t.Context(), scope.BotID, mapped); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("cross-scope runtime visible: %v", err)
			}
		})
	}
}

func TestBotDatabaseArtifactClosure(t *testing.T) {
	scope := botFileTestScope()
	disk, err := newBotDiskContent(t.TempDir(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	ref, err := disk.Put(t.Context(), store.BotContentArtifact, 1, 4, strings.NewReader("real"), contentHomeCommit)
	if err != nil {
		t.Fatal(err)
	}
	codec := newBotContentCodec(t.TempDir(), scope)
	artifact := store.BotArtifact{ContentRef: ref, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}
	candidateRef, err := disk.Put(t.Context(), store.BotContentArtifact, 1, 4, strings.NewReader("work"), contentHomeCommit)
	if err != nil {
		t.Fatal(err)
	}
	candidate := store.BotArtifact{ContentRef: candidateRef, SHA256: candidateRef.SHA256, SizeBytes: candidateRef.SizeBytes}
	run := botStoredRun{Run: store.BotRun{ID: uuid.New(), BotID: scope.BotID, Version: 1, Report: store.BotReport{Artifacts: []store.BotArtifact{artifact}}}, Work: &store.BotWorkState{Candidate: &store.BotWorkCandidate{Artifacts: []store.BotArtifact{candidate}}}}
	data, err := codec.encodeRun(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	inv, _ := newBotBackupInventory([]store.BotContentScope{scope})
	row := botHomeConfigurationRecord{kind: botHomeRun, id: run.Run.ID, sessionID: run.Run.SessionID.String(), data: data}
	if err = inv.rootsFromHome(scope.BotID, []botHomeConfigurationRecord{row}); err != nil {
		t.Fatal(err)
	}
	if len(inv.objects) != 2 || inv.objects[0].Ref != *ref || inv.objects[1].Ref != *candidateRef {
		t.Fatal("inline report/candidate artifact lost")
	}
	decoy := *ref
	decoy.Scope.OwnerID = uuid.NewString()
	payload, _ := json.Marshal(map[string]any{"report": run.Run.Report, "artifacts": []store.BotArtifact{artifact}, "arguments": map[string]any{"content_ref": decoy}})
	activity := store.BotActivity{BotID: scope.BotID, Sequence: 1, Kind: store.BotActivityReport, Payload: payload, CreatedAt: time.Now()}
	activityData, err := codec.encodeActivity(t.Context(), activity)
	if err != nil {
		t.Fatal(err)
	}
	if err = inv.rootsFromHome(scope.BotID, []botHomeConfigurationRecord{{kind: botHomeActivity, revision: activity.Sequence, activityKind: activity.Kind, createdAt: activity.CreatedAt, data: activityData}}); err != nil || len(inv.objects) != 2 {
		t.Fatalf("named activity projection closure: %v", err)
	}
	archive := filepath.Join(t.TempDir(), "closure.tar")
	exportOpts := botFileBackupExportOptions{Scopes: []store.BotContentScope{scope}, Compression: backup.CompressionNone, Fence: func(_ context.Context, f func() error) error { return f() }, Collect: func(_ context.Context, i *botBackupInventory) error {
		return i.rootsFromHome(scope.BotID, []botHomeConfigurationRecord{row, {kind: botHomeActivity, revision: activity.Sequence, activityKind: activity.Kind, createdAt: activity.CreatedAt, data: activityData}})
	}, Open: func(ctx context.Context, r store.BotContentRef) (*store.BotFileStream, error) {
		return disk.Open(ctx, r, store.BotFileRange{})
	}}
	if err = exportBotFileContent(t.Context(), archive, exportOpts); err != nil {
		t.Fatal(err)
	}
	if err = validateBotFileContentArchive(t.Context(), archive, backup.ReaderOptions{}, []store.BotContentScope{scope}, inv.objects); err != nil {
		t.Fatal(err)
	}
	target, err := newBotDiskContent(t.TempDir(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	mapping, err := restoreBotFileContent(t.Context(), archive, botFileBackupRestoreOptions{SpoolDirectory: t.TempDir(), Scopes: []store.BotContentScope{scope}, ExpectedRoots: inv.objects, Targets: map[store.BotContentScope]*botDiskContent{scope: target}})
	if err != nil || mapping[*ref] != *ref {
		t.Fatalf("database artifact closure restore: %v", err)
	}
	for _, expected := range []struct {
		ref  store.BotContentRef
		body string
	}{{*ref, "real"}, {*candidateRef, "work"}} {
		if mapping[expected.ref] != expected.ref {
			t.Fatal("restored artifact identity changed")
		}
		stream, err := target.Open(t.Context(), mapping[expected.ref], store.BotFileRange{})
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(stream)
		closeErr := stream.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(body, []byte(expected.body)) {
			t.Fatalf("restored closure bytes changed: %v %v", readErr, closeErr)
		}
	}
	if _, err = stagingGCClosure(t.Context(), disk, scope, inv); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = stagingGCClosure(ctx, disk, scope, inv); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	name, _ := diskContentName(*ref)
	root, err := disk.scopeRoot(false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.WriteFile(name, []byte("evil"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = stagingGCClosure(t.Context(), disk, scope, inv); err == nil {
		t.Fatal("corrupt artifact accepted by GC")
	}
	if err = exportBotFileContent(t.Context(), filepath.Join(t.TempDir(), "corrupt.tar"), exportOpts); err == nil {
		t.Fatal("corrupt reachable artifact exported")
	}
	var record botRunRecord
	_ = decodeBotRecord(data, &record)
	record.Inline[0] ^= 1
	corrupt, _ := encodeRuntimeRecord(&record)
	row.data = corrupt
	if err = inv.rootsFromHome(scope.BotID, []botHomeConfigurationRecord{row}); err == nil {
		t.Fatal("corrupt inline runtime accepted")
	}
}

func TestBotLegacyContentArchiveRejectedBeforeWrites(t *testing.T) {
	for _, fixture := range []struct {
		name, table string
		row         map[string]any
	}{
		{"inline-version", "bot_versions", map[string]any{"id": "old-version", "data": map[string]any{"bot_document": `{"charter":"legacy body"}`}}},
		{"inline-task", "bot_task_revisions", map[string]any{"id": "old-task", "data": map[string]any{"instructions": "legacy body"}}},
		{"inline-skill", "bot_skill_revisions", map[string]any{"id": "old-skill", "data": map[string]any{"body": "legacy body"}}},
		{"reference-only", "bot_versions", map[string]any{"id": "new-version", "data": map[string]any{"bot_record_v2": `{"format":2,"content":{"path":"missing"}}`}}},
		{"bound-transcript", "sessions", map[string]any{"id": "bot-chat", "bot_id": "old-bot"}},
		{"bound-job", "scheduled_jobs", map[string]any{"id": "bot-job", "mode": "bot_trigger"}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			archive := filepath.Join(t.TempDir(), "development.astonish-backup")
			writer, err := backup.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			manifest := backup.NewManifest("sqlite", backupModeLogical, []backup.Scope{{Kind: "platform"}, {Kind: "team", OrgSlug: "old", TeamSlug: "ops"}})
			for _, entry := range []struct {
				table string
				row   map[string]any
				scope backup.Scope
			}{
				{"users", map[string]any{"id": "must-not-import", "email": "old@example.test"}, manifest.Scopes[0]},
				{fixture.table, fixture.row, manifest.Scopes[1]},
			} {
				var data bytes.Buffer
				if err := backup.NewRecordWriter(&data, entry.table).Write("fixture", entry.row); err != nil {
					t.Fatal(err)
				}
				path := entry.table + ".jsonl"
				if _, err := writer.AddFile(path, &data); err != nil {
					t.Fatal(err)
				}
				// Deliberately lie about Records to test inspection of real bytes.
				manifest.Entries = append(manifest.Entries, backup.Entry{Path: path, Kind: "jsonl", Entity: entry.table, Scope: entry.scope})
			}
			if err := writer.CloseWithManifest(manifest); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			cfg := Config{DSN: "file:" + filepath.Join(dir, "platform.db"), DataDir: dir}
			if err := BootstrapPlatform(ctx, cfg, nil); err != nil {
				t.Fatal(err)
			}
			target, err := New(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			preserveID := uuid.NewString()
			if err := target.Users().Create(ctx, &store.User{ID: preserveID, Email: "preserve@example.test", DisplayName: "Keep", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			for _, opts := range []PlatformRestoreOptions{{}, {DryRun: true}, {ResetTarget: true, IncludeTransient: true, EnableScheduledJobs: true}} {
				_, err := target.RestorePlatformBackup(ctx, archive, opts)
				if !errors.Is(err, store.ErrBotUnsupported) || !strings.Contains(err.Error(), "unsupported development Bot backup format") {
					t.Fatalf("expected explicit refusal: %v", err)
				}
				users, err := target.Users().List(ctx)
				if err != nil || len(users) != 1 || users[0].ID != preserveID {
					t.Fatalf("target mutated: %+v %v", users, err)
				}
				orgs, err := target.Organizations().List(ctx)
				if err != nil || len(orgs) != 0 {
					t.Fatalf("partial scope import: %+v %v", orgs, err)
				}
			}
		})
	}
}

func TestBotLegacyContentLogicalExportAndImportRejected(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`CREATE TABLE bot_versions (id TEXT, data TEXT); INSERT INTO bot_versions VALUES ('old', '{"charter":"inline"}')`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := exportLogicalTable(ctx, db, DialectSQLite, "", "bot_versions", false); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("export succeeded without content archive: %v", err)
	}
	var data bytes.Buffer
	if err := backup.NewRecordWriter(&data, "bot_versions").Write("import", map[string]any{"id": "import", "data": "inline"}); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := restoreLogicalEntry(ctx, tx, DialectSQLite, "", backup.Scope{Kind: "team", OrgSlug: "org", TeamSlug: "ops"}, backup.Entry{Entity: "bot_versions"}, data.Bytes(), "restore", PlatformRestoreOptions{}); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("direct import allowed: %v", err)
	}
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM bot_versions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial import: %d %v", count, err)
	}
}

func TestBotLegacyMarkerFourDoesNotConvertContent(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE bot_schema_version (id TEXT PRIMARY KEY, version INTEGER NOT NULL); INSERT INTO bot_schema_version VALUES ('bots',4); CREATE TABLE bot_versions (id TEXT, data TEXT); INSERT INTO bot_versions VALUES ('old','inline'); CREATE TABLE sessions (id TEXT); INSERT INTO sessions VALUES ('chat')`); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := migrateBotSchema(context.Background(), db, DialectSQLite, func() error { called = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("shared database reconciliation was blocked")
	}
	var body, chat string
	if err := db.QueryRow("SELECT data FROM bot_versions").Scan(&body); err != nil || body != "inline" {
		t.Fatalf("content was migrated/deleted: %q %v", body, err)
	}
	if err := db.QueryRow("SELECT id FROM sessions").Scan(&chat); err != nil || chat != "chat" {
		t.Fatalf("ordinary Chat changed: %q %v", chat, err)
	}
}
