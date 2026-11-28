package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type fileFirstRestoreTestBackend struct {
	*fileFirstTestBackend
	restored []byte
	target   *sandbox.BotHomeBinding
}

func (b *fileFirstRestoreTestBackend) CreateBotHomeRestoreTarget(_ context.Context, s sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	b.target = &sandbox.BotHomeBinding{Scope: s, Backend: sandbox.BackendKindDocker, Ref: "fresh-" + uuid.NewString()}
	return b.target, nil
}
func (b *fileFirstRestoreTestBackend) CreateBotHomeRestoreTargetJournaled(ctx context.Context, s sandbox.BotHomeScope, reserve func(*sandbox.BotHomeBinding) error) (*sandbox.BotHomeBinding, error) {
	h, err := b.CreateBotHomeRestoreTarget(ctx, s)
	if err != nil {
		return nil, err
	}
	if err = reserve(h); err != nil {
		return nil, err
	}
	return h, nil
}
func (b *fileFirstRestoreTestBackend) DeleteBotHome(context.Context, *sandbox.BotHomeBinding) error {
	return nil
}

func (b *fileFirstRestoreTestBackend) RestoreBotHomeArchive(ctx context.Context, h *sandbox.BotHomeBinding, m *sandbox.BotHomeArchiveManifest, r io.Reader, barrier sandbox.BotHomeArchiveBarrier) error {
	return barrier(ctx, h, func(context.Context) error {
		var err error
		b.restored, err = io.ReadAll(r)
		if err != nil {
			return err
		}
		return verifyFileFirstHome(ctx, m, bytes.NewReader(b.restored))
	})
}
func TestBackupFileFirstRestoreGuards(t *testing.T) {
	src, _, _, b := fileFirstExportFixture(t, "personal")
	archive := filepath.Join(t.TempDir(), "backup")
	if err := src.ExportPlatformBackup(t.Context(), archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return b, nil }}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"mapping", "reset", "missing-backend", "populated", "leftover-tenant"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			_, target, err := NewPlatformServices(t.Context(), Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			called := false
			opts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) {
				called = true
				return &fileFirstRestoreTestBackend{fileFirstTestBackend: b}, nil
			}}
			switch mode {
			case "leftover-tenant":
				if err := os.MkdirAll(filepath.Join(dir, "orgs", "orphan"), 0700); err != nil {
					t.Fatal(err)
				}
			case "mapping":
				opts.MapOrg = map[string]string{"irrelevant": "new"}
			case "reset":
				opts.ResetTarget = true
			case "missing-backend":
				opts.FileBackend = nil
			case "populated":
				err = target.Users().Create(t.Context(), &store.User{ID: uuid.NewString(), Email: "existing@example.test", DisplayName: "Existing", Status: "active"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = target.RestorePlatformBackup(t.Context(), archive, opts); err == nil {
				t.Fatal("unsupported restore accepted")
			}
			if called {
				t.Fatal("backend touched on failed preflight")
			}
			children, err := os.ReadDir(filepath.Join(dir, "bot-content"))
			if err != nil || len(children) != 0 {
				t.Fatalf("content changed before validation: %v entries=%d", err, len(children))
			}
		})
	}
}

func TestBackupFileFirstStreamEOF(t *testing.T) {
	for _, test := range []struct {
		a, b string
		size int64
		fail bool
	}{
		{"abc", "abc", 3, false}, {"abcX", "abc", 3, true}, {"abc", "abcX", 3, true}, {"abc", "ab", 3, true}, {"abc", "abd", 3, true},
	} {
		err := compareFileFirstStreams(t.Context(), strings.NewReader(test.a), strings.NewReader(test.b), test.size)
		if (err != nil) != test.fail {
			t.Fatalf("%+v: %v", test, err)
		}
	}
}

// fileFirstOrderingBackend keeps source and destination bytes separate. Its
// synchronous checkpoints are barriers: restore cannot advance until the SQL
// absence assertion returns, including the final stream Close acknowledgement.
type fileFirstOrderingBackend struct {
	*fileFirstRestoreTestBackend
	check          func(string)
	ref            store.BotContentRef
	installed      map[string][]byte
	readback       string
	opened, closed int
}

func (b *fileFirstOrderingBackend) RestoreBotHomeArchive(ctx context.Context, h *sandbox.BotHomeBinding, m *sandbox.BotHomeArchiveManifest, r io.Reader, barrier sandbox.BotHomeArchiveBarrier) error {
	return barrier(ctx, h, func(live context.Context) error {
		b.check("before-install")
		var err error
		b.restored, err = io.ReadAll(r)
		if err != nil {
			return err
		}
		if err = verifyFileFirstHome(live, m, bytes.NewReader(b.restored)); err != nil {
			return err
		}
		// This fixture has one retained object, serialized into the Home archive.
		b.installed = map[string][]byte{b.ref.Key: bytes.Clone(b.restored)}
		b.check("after-install")
		return nil
	})
}

func (b *fileFirstOrderingBackend) OpenBotContent(_ context.Context, h *sandbox.BotHomeBinding, ref store.BotContentRef, r store.BotFileRange) (*store.BotFileStream, error) {
	if h == nil || b.target == nil || *h != *b.target || ref != b.ref || r != (store.BotFileRange{}) {
		return nil, store.ErrBotForbidden
	}
	data, ok := b.installed[ref.Key]
	if !ok {
		return nil, os.ErrNotExist
	}
	b.opened++
	b.check("before-readback")
	data = bytes.Clone(data)
	switch b.readback {
	case "corrupt":
		data[len(data)/2] ^= 1
	case "truncated":
		data = data[:len(data)-1]
	case "trailing":
		data = append(data, '!')
	}
	return &store.BotFileStream{ReadCloser: &fileFirstOrderingReadback{Reader: bytes.NewReader(data), backend: b}}, nil
}

type fileFirstOrderingReadback struct {
	*bytes.Reader
	backend *fileFirstOrderingBackend
}

func (r *fileFirstOrderingReadback) Close() error {
	r.backend.closed++
	r.backend.check("readback-close")
	if r.backend.readback == "close-error" {
		return errFileFirstReadbackClose
	}
	return nil
}

var errFileFirstReadbackClose = errors.New("test retained readback close failure")

func TestBackupFileFirstRestoreFilesBeforeSQL(t *testing.T) {
	for _, kind := range []string{"personal", "team"} {
		t.Run(kind, func(t *testing.T) {
			src, _, sourceDB, source := fileFirstExportFixture(t, kind)
			// Seed a current-format retained reference (not an old-format migration).
			// The fixture's SQL connection belongs only to its disposable scoped DB.
			var definition string
			if err := sourceDB.QueryRow("SELECT definition FROM bot_versions").Scan(&definition); err != nil {
				t.Fatal(err)
			}
			var envelope map[string]any
			if err := json.Unmarshal([]byte(definition), &envelope); err != nil {
				t.Fatal(err)
			}
			var version botVersionRecord
			if err := decodeBotRecord(envelope, &version); err != nil {
				t.Fatal(err)
			}
			disk, err := newBotDiskContent(filepath.Join(src.dataDir, "bot-content"), version.Content.Scope)
			if err != nil {
				t.Fatal(err)
			}
			stream, err := disk.Open(t.Context(), version.Content, store.BotFileRange{})
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(stream)
			if err = errors.Join(err, stream.Close(), disk.Close()); err != nil {
				t.Fatal(err)
			}
			version.Content.Scope.TeamID = "original"
			version.Content.Key = "content-v1/document-1-" + strings.ReplaceAll(uuid.NewString(), "-", "")
			ref := version.Content
			if kind == "team" {
				// Routed reads require live team membership as well as Bot ownership.
				org, err := src.ForOrg(ref.Scope.OrgID)
				if err != nil {
					t.Fatal(err)
				}
				team, err := org.Teams().GetTeamBySlug(t.Context(), "original")
				if err != nil || team == nil {
					t.Fatalf("fixture team: %v %v", team, err)
				}
				if err = org.Teams().AddMember(t.Context(), &store.TeamMembership{UserID: ref.Scope.OwnerID, TeamID: team.ID, Role: "member"}); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := json.Marshal(version)
			if err != nil {
				t.Fatal(err)
			}
			envelope = map[string]any{"bot_record_v2": string(raw)}
			raw, err = json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = sourceDB.Exec("UPDATE bot_versions SET definition=?", string(raw)); err != nil {
				t.Fatal(err)
			}
			source.objects[ref.Key] = bytes.Clone(body)
			// The same bytes are physically installed and subsequently reopened; the
			// destination must never satisfy readback from the source object map.
			source.home = bytes.Clone(body)
			archive := filepath.Join(t.TempDir(), "backup")
			if err = src.ExportPlatformBackup(t.Context(), archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return source, nil }}); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"exact", "corrupt", "truncated", "trailing", "close-error", "interrupt-sql"} {
				t.Run(mode, func(t *testing.T) {
					dir := t.TempDir()
					_, target, err := NewPlatformServices(t.Context(), Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
					if err != nil {
						t.Fatal(err)
					}
					defer target.Close()
					// Restore holds the target's sole SQLite connection through file
					// installation. Observe committed SQL using a second store-managed
					// connection to this disposable platform DB, not that locked pool.
					observer, err := New(t.Context(), Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
					if err != nil {
						t.Fatal(err)
					}
					defer observer.Close()
					var checkpoints []string
					assertNoSQL := func(checkpoint string) {
						t.Helper()
						checkpoints = append(checkpoints, checkpoint)
						for _, table := range []string{"users", "organizations", "org_memberships"} {
							var n int
							if err := observer.platformDB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+quoteSQLiteIdent(table)).Scan(&n); err != nil || n != 0 {
								t.Fatalf("%s: imported platform %s rows=%d: %v", checkpoint, table, n, err)
							}
						}
						// No tenant DB (public OR private import staging) may exist.
						// Checking only platform visibility would miss early private Bot SQL.
						for _, path := range []string{filepath.Join(dir, "orgs", "archive"), filepath.Join(dir, restoreJournalDir, "sql")} {
							if _, err := os.Stat(path); !os.IsNotExist(err) {
								t.Fatalf("%s: Bot SQL tree exists before exact readback: %s (%v)", checkpoint, path, err)
							}
						}
					}
					backend := &fileFirstOrderingBackend{fileFirstRestoreTestBackend: &fileFirstRestoreTestBackend{fileFirstTestBackend: &fileFirstTestBackend{contentHomeBackend: &contentHomeBackend{objects: map[string][]byte{}}}}, check: assertNoSQL, ref: ref, readback: mode}
					opts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return backend, nil }, interruptAfterFiles: mode == "interrupt-sql"}
					_, err = target.RestorePlatformBackup(t.Context(), archive, opts)
					if got := strings.Join(checkpoints, ","); got != "before-install,after-install,before-readback,readback-close" {
						t.Fatalf("restore checkpoints=%s: %v", got, err)
					}
					if backend.opened != 1 || backend.closed != 1 {
						t.Fatalf("readback lifetime opens=%d closes=%d", backend.opened, backend.closed)
					}
					if !bytes.Equal(backend.restored, body) || !bytes.Equal(source.objects[ref.Key], body) {
						t.Fatal("installation or source bytes changed")
					}
					if mode != "exact" {
						want := "install content"
						if mode == "interrupt-sql" {
							want = "test SQL import interrupted after files"
						}
						if err == nil || !strings.Contains(err.Error(), want) {
							t.Fatalf("failed restore accepted or wrong failure: %v", err)
						}
						if mode == "interrupt-sql" && !errors.Is(err, errFileFirstSQLInterrupted) {
							t.Fatalf("SQL interruption lost: %v", err)
						}
						if mode == "close-error" && !errors.Is(err, errFileFirstReadbackClose) {
							t.Fatalf("close acknowledgement lost: %v", err)
						}
						if mode == "interrupt-sql" {
							// Private staging may exist, but the destination is not authorized.
							for _, table := range []string{"users", "organizations", "org_memberships"} {
								var n int
								if err := observer.platformDB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+quoteSQLiteIdent(table)).Scan(&n); err != nil || n != 0 {
									t.Fatalf("rolled-back platform %s rows=%d: %v", table, n, err)
								}
							}
							if _, err := os.Stat(filepath.Join(dir, "orgs", "archive")); !os.IsNotExist(err) {
								t.Fatalf("interrupted restore published tenant SQL: %v", err)
							}
							if _, err := target.ForOrg(ref.Scope.OrgID); err == nil {
								t.Fatal("interrupted restore authorized an organization")
							}
						} else {
							assertNoSQL("failed-readback")
						}
						data, e := os.ReadFile(filepath.Join(dir, restoreJournalDir, "journal.json"))
						if e != nil {
							t.Fatal(e)
						}
						var journal restoreJournal
						if e = json.Unmarshal(data, &journal); e != nil {
							t.Fatal(e)
						}
						if journal.Phase != restoreFailed || len(journal.Homes) != 1 || journal.Homes[0].Target != *backend.target {
							t.Fatalf("failed destination not journaled: %+v", journal)
						}
						if e = target.CleanupFileFirstRestore(t.Context(), archive, opts); e != nil {
							t.Fatal(e)
						}
						assertNoSQL("after-cleanup")
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					// Reopen through the tenant router, with fresh owner authorization.
					ctx, err := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: ref.Scope.OwnerID, OrgSlug: ref.Scope.OrgID, TeamSlug: "original", Authenticated: true})
					if err != nil {
						t.Fatal(err)
					}
					if err = target.InstallBotContentHomeBackend(backend); err != nil {
						t.Fatal(err)
					}
					org, err := target.ForOrg(ref.Scope.OrgID)
					if err != nil {
						t.Fatal(err)
					}
					var bots store.BotStore
					if kind == "personal" {
						bots = org.ForUser(ref.Scope.OwnerID).Bots()
					} else {
						bots = org.ForTeam("original").Bots()
					}
					got, err := bots.Get(ctx, ref.Scope.BotID)
					if err != nil {
						t.Fatal(err)
					}
					if got.Status != store.BotPaused || got.Home.StorageKey != backend.target.Ref {
						t.Fatalf("restored Bot not paused on fresh Home: %+v", got)
					}
					// Further routed reads are after SQL visibility, outside the barriers.
					backend.check = func(string) {}
					hydrated, err := bots.GetVersion(ctx, ref.Scope.BotID, version.Version)
					if err != nil || hydrated.Charter != "disk-only-charter-sentinel" {
						t.Fatalf("restored retained version: %+v %v", hydrated, err)
					}
					sum := sha256.Sum256(backend.installed[ref.Key])
					if hex.EncodeToString(sum[:]) != ref.SHA256 || int64(len(body)) != ref.SizeBytes {
						t.Fatal("restored content not byte-exact")
					}
					t.Log(fmt.Sprintf("%s: installation and exact retained readback precede scoped Bot SQL visibility", kind))
				})
			}
		})
	}
}

func TestBackupFileFirstRestore(t *testing.T) {
	for _, kind := range []string{"personal", "team"} {
		t.Run(kind, func(t *testing.T) {
			src, path, sourceDB, b := fileFirstExportFixture(t, kind)
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
			backend := &fileFirstRestoreTestBackend{fileFirstTestBackend: b}
			opts := PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) { return backend, nil }}
			if _, err = target.RestorePlatformBackup(t.Context(), archive, opts); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(b.home, backend.restored) {
				t.Fatal("ordinary Home changed")
			}
			relative, err := filepath.Rel(src.dataDir, path)
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", filepath.Join(dir, relative))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var before, after string
			if err = sourceDB.QueryRow("SELECT definition FROM bot_versions").Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow("SELECT definition FROM bot_versions").Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("immutable SQL reference changed")
			}
			var homeRaw, status, meta string
			if err = db.QueryRow("SELECT home_binding,status,data FROM bots").Scan(&homeRaw, &status, &meta); err != nil {
				t.Fatal(err)
			}
			var home store.BotHomeBinding
			if err = json.Unmarshal([]byte(homeRaw), &home); err != nil {
				t.Fatal(err)
			}
			if status != string(store.BotPaused) || home.StorageKey != backend.target.Ref {
				t.Fatal("target not selected paused")
			}
			var sourceHomeRaw string
			if err = sourceDB.QueryRow("SELECT home_binding FROM bots").Scan(&sourceHomeRaw); err != nil {
				t.Fatal(err)
			}
			var sourceHome store.BotHomeBinding
			if err = json.Unmarshal([]byte(sourceHomeRaw), &sourceHome); err != nil {
				t.Fatal(err)
			}
			if home.ID != sourceHome.ID || home.Revision != sourceHome.Revision+1 || home.StorageKey == sourceHome.StorageKey {
				t.Fatal("logical identity changed or physical generation was reused")
			}
			var record map[string]any
			if err = json.Unmarshal([]byte(after), &record); err != nil {
				t.Fatal(err)
			}
			var version botVersionRecord
			if err = decodeBotRecord(record, &version); err != nil {
				t.Fatal(err)
			}
			codec := newBotContentCodec(filepath.Join(dir, "bot-content"), version.Content.Scope)
			hydrated, err := codec.decodeVersion(t.Context(), version.BotID, version.Version, record)
			if err != nil {
				t.Fatal(err)
			}
			if hydrated.Charter != "disk-only-charter-sentinel" {
				t.Fatal("staged bytes not reopened")
			}
		})
	}
}
