package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type recoveryRestoreBackend struct {
	*fileFirstRestoreTestBackend
	fail         bool
	deleted      []sandbox.BotHomeBinding
	afterRestore func()
	failAt       int
	allocations  int
	deleteError  error
}

func (b *recoveryRestoreBackend) RestoreBotHomeArchive(ctx context.Context, h *sandbox.BotHomeBinding, m *sandbox.BotHomeArchiveManifest, r io.Reader, barrier sandbox.BotHomeArchiveBarrier) error {
	err := b.fileFirstRestoreTestBackend.RestoreBotHomeArchive(ctx, h, m, r, barrier)
	if err == nil && b.corrupt {
		// Simulate the backend's physical readback disagreeing after installation.
		err = verifyFileFirstHome(ctx, m, strings.NewReader("corrupt target readback"))
	}
	if b.afterRestore != nil {
		b.afterRestore()
	}
	return err
}

func (b *recoveryRestoreBackend) CreateBotHomeRestoreTargetJournaled(ctx context.Context, scope sandbox.BotHomeScope, reserve func(*sandbox.BotHomeBinding) error) (*sandbox.BotHomeBinding, error) {
	h, err := b.CreateBotHomeRestoreTarget(ctx, scope)
	if err != nil {
		return nil, err
	}
	if err = reserve(h); err != nil {
		return nil, err
	}
	b.allocations++
	if b.fail || b.failAt == b.allocations {
		return nil, errors.New("injected allocation failure after reservation")
	}
	return h, nil
}
func (b *recoveryRestoreBackend) DeleteBotHome(_ context.Context, h *sandbox.BotHomeBinding) error {
	if b.deleteError != nil {
		return b.deleteError
	}
	b.deleted = append(b.deleted, *h)
	return nil
}
func TestBackupFileFirstRecoveryInterruptions(t *testing.T) {
	for _, mode := range []string{"second-home", "readback", "cleanup-retry", "cleaned-marker-removed", "published-tenants", "private-partial-delete"} {
		t.Run(mode, func(t *testing.T) {
			src, _, db, b := fileFirstExportFixture(t, "personal")
			if mode == "second-home" {
				var raw string
				if err := db.QueryRow("SELECT data FROM bots LIMIT 1").Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var data map[string]json.RawMessage
				if err := json.Unmarshal([]byte(raw), &data); err != nil {
					t.Fatal(err)
				}
				var selection botContentHomeSelection
				if err := json.Unmarshal(data[botContentHomeSelectionKey], &selection); err != nil {
					t.Fatal(err)
				}
				id, session := uuid.New(), uuid.New()
				selection.Home.ID = uuid.New()
				selection.Home.BotID = id
				selection.Home.StorageKey = "second-source-home"
				selection.Binding.Ref = selection.Home.StorageKey
				selection.Binding.Scope.BotID = id.String()
				selected, _ := json.Marshal(selection)
				data[botContentHomeSelectionKey] = selected
				metadata, _ := json.Marshal(data)
				home, _ := json.Marshal(selection.Home)
				if _, err := db.Exec(`INSERT INTO bots (id,owner_id,name,status,revision,version,schema_version,home_session_id,home_binding,create_key,last_seq,fence,data,cleanup_complete,created_at,updated_at) SELECT ?,owner_id,'Second',status,revision,version,schema_version,?,?,?,last_seq,fence,?,cleanup_complete,created_at,updated_at FROM bots LIMIT 1`, id.String(), session.String(), string(home), uuid.NewString(), string(metadata)); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO sessions(id,user_id,bot_id,purpose,retention,title,metadata,created_at,updated_at) SELECT ?,user_id,?,'bot_home','bot','Second','{}',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP FROM sessions LIMIT 1`, session.String(), id.String()); err != nil {
					t.Fatal(err)
				}
				owner := uuid.MustParse(selection.Binding.Scope.UserID)
				codec := newBotContentCodec(filepath.Join(src.dataDir, "bot-content"), store.BotContentScope{OrgID: "archive", TeamID: personalSchemaName(owner.String()), OwnerID: owner.String(), BotID: id})
				version := store.DefaultBotVersion()
				version.BotID = id
				version.Version = 1
				version.CreatedBy = owner
				definition, err := codec.encodeVersion(t.Context(), version)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(definition)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(`INSERT INTO bot_versions(id,bot_id,version,schema_version,definition,content_hash,created_by,change_reason,created_at) VALUES (?,?,1,1,?,'hash',?,'',CURRENT_TIMESTAMP)`, uuid.NewString(), id.String(), string(encoded), owner.String()); err != nil {
					t.Fatal(err)
				}
			}
			archive := filepath.Join(t.TempDir(), "backup")
			if err := src.ExportPlatformBackup(t.Context(), archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return b, nil }}); err != nil {
				t.Fatal(err)
			}
			before, err := restoreArchiveDigest(t.Context(), archive)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			_, target, err := NewPlatformServices(t.Context(), Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			// Keep fake destination object writes isolated from the source backend.
			dest := &fileFirstTestBackend{contentHomeBackend: &contentHomeBackend{objects: map[string][]byte{}}, home: b.home}
			backend := &recoveryRestoreBackend{fileFirstRestoreTestBackend: &fileFirstRestoreTestBackend{fileFirstTestBackend: dest}, fail: mode != "readback" && mode != "second-home"}
			if mode == "second-home" {
				backend.failAt = 2
			}
			if mode == "readback" {
				dest.corrupt = true
			}
			opts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return backend, nil }}
			if _, err = target.RestorePlatformBackup(t.Context(), archive, opts); err == nil || !strings.Contains(err.Error(), "cleanup") {
				t.Fatalf("missing recovery: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, restoreJournalDir, "journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			var j restoreJournal
			if err = json.Unmarshal(raw, &j); err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == "second-home" {
				want = 2
			}
			if len(j.Homes) != want || j.Phase != restoreFailed {
				t.Fatalf("lost destinations: %+v", j)
			}
			if mode == "published-tenants" || mode == "private-partial-delete" {
				root, e := os.OpenRoot(dir)
				if e != nil {
					t.Fatal(e)
				}
				if e = root.Mkdir("orgs", 0700); e != nil {
					t.Fatal(e)
				}
				if e = restoreMarker(root, "orgs/.restore-owner", j.ID); e != nil {
					t.Fatal(e)
				}
				j.PublishTenants = true
				if e = writeRestoreJournal(root, &j); e != nil {
					t.Fatal(e)
				}
				if mode == "private-partial-delete" {
					if e = root.Rename("orgs", restoreJournalDir+"/removed-orgs"); e != nil {
						t.Fatal(e)
					}
					if e = root.Remove(restoreJournalDir + "/removed-orgs/.restore-owner"); e != nil {
						t.Fatal(e)
					}
				}
				root.Close()
			}
			if mode == "cleanup-retry" {
				backend.deleteError = store.ErrBotConflict
				if err = target.CleanupFileFirstRestore(t.Context(), archive, opts); err == nil {
					t.Fatal("attached destination removed")
				}
				backend.deleteError = nil
			}
			if mode == "cleaned-marker-removed" {
				root, e := os.OpenRoot(dir)
				if e != nil {
					t.Fatal(e)
				}
				j.Phase = restoreCleaned
				if e = writeRestoreJournal(root, &j); e != nil {
					t.Fatal(e)
				}
				root.Close()
				if e = os.Remove(filepath.Join(dir, "bot-content", ".restore-reservation")); e != nil {
					t.Fatal(e)
				}
			}
			if err = target.CleanupFileFirstRestore(t.Context(), archive, opts); err != nil {
				t.Fatal(err)
			}
			if len(backend.deleted) != want {
				t.Fatal("wrong cleanup count")
			}
			for i, h := range backend.deleted {
				if h != j.Homes[i].Target || h.Ref == j.Homes[i].Source.Ref {
					t.Fatal("cleanup escaped target journal")
				}
			}
			after, err := restoreArchiveDigest(t.Context(), archive)
			if err != nil || after != before {
				t.Fatal("archive changed", err)
			}
			if len(b.objects) != 0 {
				t.Fatal("source objects changed")
			}
			backend.fail = false
			backend.failAt = 0
			dest.corrupt = false
			if _, err = target.RestorePlatformBackup(t.Context(), archive, opts); err != nil {
				t.Fatal("retry failed", err)
			}
		})
	}
}

func TestBackupFileFirstRecoveryUnreservedContent(t *testing.T) {
	src, _, _, b := fileFirstExportFixture(t, "personal")
	archive := filepath.Join(t.TempDir(), "backup")
	if err := src.ExportPlatformBackup(t.Context(), archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return b, nil }}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, target, err := NewPlatformServices(t.Context(), Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	backend := &recoveryRestoreBackend{fileFirstRestoreTestBackend: &fileFirstRestoreTestBackend{fileFirstTestBackend: b}}
	opts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return backend, nil }}
	root, _, err := newRestoreJournal(t.Context(), target, archive, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// Interruption before reservation gives no ownership of later content.
	keep := filepath.Join(dir, "bot-content", "foreign")
	if err = os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = target.CleanupFileFirstRestore(t.Context(), archive, opts); err == nil {
		t.Fatal("cleanup accepted unreserved foreign content")
	}
	if raw, err := os.ReadFile(keep); err != nil || string(raw) != "keep" {
		t.Fatal("foreign content lost", err)
	}
	if err = os.Remove(keep); err != nil {
		t.Fatal(err)
	}
	if err = target.CleanupFileFirstRestore(t.Context(), archive, opts); err != nil {
		t.Fatal("empty prepared cleanup", err)
	}
}

func TestBackupFileFirstRecoveryPartialSQL(t *testing.T) {
	src, _, _, b := fileFirstExportFixture(t, "personal")
	archive := filepath.Join(t.TempDir(), "backup")
	if err := src.ExportPlatformBackup(t.Context(), archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return b, nil }}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, target, err := NewPlatformServices(t.Context(), Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	backend := &recoveryRestoreBackend{fileFirstRestoreTestBackend: &fileFirstRestoreTestBackend{fileFirstTestBackend: b}}
	opts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return backend, nil }}
	plan, err := target.PlanPlatformRestore(t.Context(), archive, opts)
	if err != nil {
		t.Fatal(err)
	}
	files, err := backup.ReadArchiveFiles(archive, backup.ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	entries := plan.Archive.Manifest.Entries[:0]
	for _, e := range plan.Archive.Manifest.Entries {
		if e.Kind != "jsonl" {
			continue
		}
		if e.Scope.Kind == "personal" {
			if e.Entity != "sessions" {
				continue
			}
			files[e.Path] = []byte("not-json\n")
		}
		entries = append(entries, e)
	}
	plan.Archive.Manifest.Entries = entries
	opts.fileFirst = &fileFirstRestoreState{files: files, mapping: newFileFirstMapping()}
	root, j, err := newRestoreJournal(t.Context(), target, archive, plan.Archive.Manifest.Scopes, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = restoreMarker(root, "bot-content/.restore-reservation", j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = target.importFileFirstStaged(t.Context(), archive, opts, *plan, root, j); err == nil || !strings.Contains(err.Error(), "restore personal") {
		t.Fatalf("expected later database failure: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, restoreJournalDir, "sql", "orgs", "archive", "teams", "original.db")); err != nil {
		t.Fatal("earlier tenant did not import", err)
	}
	var n int
	if err = target.platformDB.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial platform visible: %d %v", n, err)
	}
	if err = target.CleanupFileFirstRestore(t.Context(), archive, opts); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, restoreJournalDir)); !os.IsNotExist(err) {
		t.Fatal("SQL staging not removed")
	}
	if _, err = target.RestorePlatformBackup(t.Context(), archive, PlatformRestoreOptions{FileBackend: opts.FileBackend}); err != nil {
		t.Fatal(err)
	}
}

func TestBackupFileFirstRecoveryGuards(t *testing.T) {
	for _, mode := range []string{"wrong-archive", "platform-data", "wrong-dsn", "reservation", "content-symlink", "busy"} {
		t.Run(mode, func(t *testing.T) {
			src, _, _, b := fileFirstExportFixture(t, "team")
			archive := filepath.Join(t.TempDir(), "backup")
			if err := src.ExportPlatformBackup(t.Context(), archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return b, nil }}); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			_, target, err := NewPlatformServices(t.Context(), Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			backend := &recoveryRestoreBackend{fileFirstRestoreTestBackend: &fileFirstRestoreTestBackend{fileFirstTestBackend: b}, fail: true}
			opts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return backend, nil }}
			if _, err = target.RestorePlatformBackup(t.Context(), archive, opts); err == nil {
				t.Fatal("failure not injected")
			}
			switch mode {
			case "wrong-archive":
				archive = filepath.Join(t.TempDir(), "other")
				if err = os.WriteFile(archive, []byte("different"), 0600); err != nil {
					t.Fatal(err)
				}
			case "platform-data":
				_, err = target.platformDB.Exec("INSERT INTO platform_settings(key,value) VALUES ('keep','safe')")
				if err != nil {
					t.Fatal(err)
				}
			case "wrong-dsn":
				target.platformDSN += "?different"
			case "reservation":
				if err = os.WriteFile(filepath.Join(dir, "bot-content", ".restore-reservation"), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "content-symlink":
				if err = os.Rename(filepath.Join(dir, "bot-content"), filepath.Join(dir, "foreign-content")); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink("foreign-content", filepath.Join(dir, "bot-content")); err != nil {
					t.Fatal(err)
				}
			case "busy":
				lock, e := lockFileFirstRestore(dir)
				if e != nil {
					t.Fatal(e)
				}
				defer lock.Close()
			}
			if err = target.CleanupFileFirstRestore(t.Context(), archive, opts); err == nil {
				t.Fatal("unsafe cleanup accepted")
			}
			if len(backend.deleted) != 0 {
				t.Fatal("cleanup deleted a target before guards")
			}
			raw, err := os.ReadFile(filepath.Join(dir, restoreJournalDir, "journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			var j restoreJournal
			if err = json.Unmarshal(raw, &j); err != nil || len(j.Homes) != 1 {
				t.Fatal("journal lost", err)
			}
		})
	}
}
func TestBackupFileFirstRecoveryAllocation(t *testing.T) {
	src, _, _, b := fileFirstExportFixture(t, "personal")
	archive := filepath.Join(t.TempDir(), "backup")
	if err := src.ExportPlatformBackup(t.Context(), archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return b, nil }}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, target, err := NewPlatformServices(t.Context(), Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	backend := &recoveryRestoreBackend{fileFirstRestoreTestBackend: &fileFirstRestoreTestBackend{fileFirstTestBackend: b}, fail: true}
	opts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return backend, nil }}
	if _, err = target.RestorePlatformBackup(t.Context(), archive, opts); err == nil || !strings.Contains(err.Error(), "cleanup") {
		t.Fatalf("missing actionable recovery: %v", err)
	}
	if err = target.CleanupFileFirstRestore(t.Context(), archive, opts); err != nil {
		t.Fatal(err)
	}
	if len(backend.deleted) != 1 || backend.deleted[0] != *backend.target {
		t.Fatal("cleanup did not delete exact destination")
	}
	after, err := os.ReadFile(archive)
	if err != nil || string(before) != string(after) {
		t.Fatal("source archive changed")
	}
	backend.fail = false
	if _, err = target.RestorePlatformBackup(t.Context(), archive, opts); err != nil {
		t.Fatal(err)
	}
	if err = target.CleanupFileFirstRestore(t.Context(), archive, opts); err == nil {
		t.Fatal("cleanup accepted committed restore")
	}
}
