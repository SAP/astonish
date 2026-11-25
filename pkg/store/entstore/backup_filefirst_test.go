package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type fileFirstTestBackend struct {
	*contentHomeBackend
	check func()
	home  []byte
	calls int
}

func (b *fileFirstTestBackend) ExportBotHomeArchive(ctx context.Context, home *sandbox.BotHomeBinding, w io.Writer, barrier sandbox.BotHomeArchiveBarrier) (*sandbox.BotHomeArchiveManifest, error) {
	b.calls++
	sum := sha256.Sum256(b.home)
	m := &sandbox.BotHomeArchiveManifest{Version: 1, Binding: home.Scope.Key(), Entries: []sandbox.BotHomeArchiveEntry{{Root: sandbox.BotHomeArchiveHome, Path: "MEMORY.md", Size: int64(len(b.home)), SHA256: hex.EncodeToString(sum[:]), Mode: 0600}}}
	err := barrier(ctx, home, func(context.Context) error {
		if b.check != nil {
			b.check()
		}
		_, err := w.Write(b.home)
		return err
	})
	return m, err
}
func (*fileFirstTestBackend) RestoreBotHomeArchive(context.Context, *sandbox.BotHomeBinding, *sandbox.BotHomeArchiveManifest, io.Reader, sandbox.BotHomeArchiveBarrier) error {
	return store.ErrBotUnsupported
}

func fileFirstExportFixture(t *testing.T, kind string) (*Store, string, *sql.DB, *fileFirstTestBackend) {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	_, s, err := NewPlatformServices(ctx, Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	owner := uuid.New()
	org := &store.Organization{ID: uuid.NewString(), Slug: "archive", Name: "Archive", Status: "active"}
	if err = s.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@example.test", DisplayName: "Archive", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err = s.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err = s.Organizations().AddMember(ctx, owner.String(), org.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err = s.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := s.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Name: "Original Team", Slug: "original", SchemaName: "original"}
	if err = ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	if err = ods.ProvisionTeam(ctx, team.Slug); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "orgs", org.Slug, "teams", team.Slug+".db")
	if kind == "personal" {
		if err = ods.ProvisionPersonalSchema(ctx, owner.String()); err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(dir, "orgs", org.Slug, "personal", owner.String()+".db")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	id, session := uuid.New(), uuid.New()
	home := store.BotHomeBinding{ID: uuid.New(), BotID: id, Scope: store.BotScope(kind), Backend: "docker", StorageKey: "test-owned-home", Revision: 1}
	binding := sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: org.Slug, TeamID: team.Slug, UserID: owner.String(), BotID: id.String()}, Backend: sandbox.BackendKindDocker, Ref: home.StorageKey}
	metadata := map[string]any{botContentHomeSelectionKey: botContentHomeSelection{Home: home, Binding: binding}}
	raw, _ := json.Marshal(metadata)
	homeRaw, _ := json.Marshal(home)
	_, err = db.Exec(`INSERT INTO bots (id,owner_id,name,status,revision,version,schema_version,home_session_id,home_binding,create_key,last_seq,fence,data,cleanup_complete,created_at,updated_at) VALUES (?,?,?,'paused',1,1,1,?,?,?,0,0,?,false,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, id.String(), owner.String(), "Backup", session.String(), string(homeRaw), uuid.NewString(), string(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO sessions (id,user_id,bot_id,purpose,retention,title,metadata,created_at,updated_at) VALUES (?,?,?,'bot_home','bot','Backup','{}',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, session.String(), owner.String(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	stage := store.BotContentScope{OrgID: org.Slug, TeamID: team.Slug, OwnerID: owner.String(), BotID: id}
	if kind == "personal" {
		stage.TeamID = personalSchemaName(owner.String())
	}
	codec := newBotContentCodec(filepath.Join(dir, "bot-content"), stage)
	version := store.DefaultBotVersion()
	version.BotID = id
	version.Version = 1
	version.Charter = "disk-only-charter-sentinel"
	version.CreatedBy = owner
	data, err := codec.encodeVersion(ctx, version)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := json.Marshal(data)
	_, err = db.Exec(`INSERT INTO bot_versions (id,bot_id,version,schema_version,definition,content_hash,created_by,change_reason,created_at) VALUES (?,?,1,1,?,?,?,'',CURRENT_TIMESTAMP)`, uuid.NewString(), id.String(), string(definition), "hash", owner.String())
	if err != nil {
		t.Fatal(err)
	}
	backend := &fileFirstTestBackend{contentHomeBackend: &contentHomeBackend{objects: map[string][]byte{}}, home: bytes.Repeat([]byte("retained-file-sentinel\n"), (17<<20)/23+1)}
	return s, path, db, backend
}

func TestBackupSQLiteWriterFence(t *testing.T) {
	if path := os.Getenv("ASTONISH_TEST_BACKUP_WRITER_DB"); path != "" {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		_, err = db.Exec("UPDATE bots SET revision=revision+1")
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "locked") {
			t.Fatalf("expected cross-process lock refusal, got %v", err)
		}
		return
	}
	s, path, db, backend := fileFirstExportFixture(t, "personal")
	backend.check = func() {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBackupSQLiteWriterFence$", "-test.v")
		cmd.Env = append(os.Environ(), "ASTONISH_TEST_BACKUP_WRITER_DB="+path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("writer subprocess: %v\n%s", err, out)
		}
	}
	archive := filepath.Join(t.TempDir(), "backup.archive")
	err := s.ExportPlatformBackup(t.Context(), archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return backend, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if backend.calls != 1 {
		t.Fatalf("Home calls=%d", backend.calls)
	}
	if _, err = db.Exec("UPDATE bots SET revision=revision+1"); err != nil {
		t.Fatalf("lock leaked: %v", err)
	}
	if _, err = VerifyPlatformBackup(t.Context(), archive, backup.ReaderOptions{}); err != nil {
		t.Fatal(err)
	}
}
func TestBackupFileFirstExport(t *testing.T) {
	for _, kind := range []string{"personal", "team"} {
		t.Run(kind, func(t *testing.T) {
			s, _, db, backend := fileFirstExportFixture(t, kind)
			archive := filepath.Join(t.TempDir(), "backup.archive")
			opts := PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return backend, nil }}
			if err := s.ExportPlatformBackup(t.Context(), archive, opts); err != nil {
				t.Fatal(err)
			}
			summary, err := VerifyPlatformBackup(t.Context(), archive, backup.ReaderOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err = validateBotContentArchive(t.Context(), archive, backup.ReaderOptions{}, summary); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("restore gate opened: %v", err)
			}
			before, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec("UPDATE bots SET status='active'"); err != nil {
				t.Fatal(err)
			}
			if err = s.ExportPlatformBackup(t.Context(), archive, opts); err == nil {
				t.Fatal("active Bot exported")
			}
			after, _ := os.ReadFile(archive)
			if !bytes.Equal(before, after) {
				t.Fatal("failed export replaced prior archive")
			}
			if _, err = db.Exec("UPDATE bots SET status='paused'"); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`UPDATE bot_versions SET definition='{"charter":"legacy"}'`); err != nil {
				t.Fatal(err)
			}
			if err = s.ExportPlatformBackup(t.Context(), archive, opts); err == nil {
				t.Fatal("inline version exported")
			}
		})
	}
}
