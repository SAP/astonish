package entstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

// Never unlink this inode: restore and cleanup serialize across processes.
func lockFileFirstRestore(dir string) (*os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(".filefirst-restore.lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &stat); err == nil && (stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1) {
		err = store.ErrBotConflict
	}
	if err == nil {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("restore or cleanup busy: %w", err)
	}
	return f, nil
}

type restoreJournalPhase string

const (
	restorePrepared     restoreJournalPhase = "prepared"
	restoreFilesReady   restoreJournalPhase = "files-ready"
	restoreSQLImporting restoreJournalPhase = "sql-importing"
	restoreCommitted    restoreJournalPhase = "committed"
	restoreFailed       restoreJournalPhase = "failed"
	restoreCleaned      restoreJournalPhase = "cleaned"
	restoreJournalDir                       = ".filefirst-restore"
)

// The callback must complete durably BEFORE the backend makes any allocation.
// An error after reservation does not imply the physical destination is absent.
type journaledBotRestoreBackend interface {
	CreateBotHomeRestoreTargetJournaled(context.Context, sandbox.BotHomeScope, func(*sandbox.BotHomeBinding) error) (*sandbox.BotHomeBinding, error)
	sandbox.BotHomeDeleteBackend
}

type restoreJournalHome struct {
	Source    sandbox.BotHomeBinding `json:"source"`
	Target    sandbox.BotHomeBinding `json:"target"`
	Allocated bool                   `json:"allocated"`
}
type restoreJournal struct {
	Version           int                       `json:"version"`
	ID                string                    `json:"id"`
	Root              string                    `json:"root"`
	PlatformDSN       string                    `json:"platform_dsn"`
	ArchiveSHA256     string                    `json:"archive_sha256"`
	Scopes            []backup.Scope            `json:"scopes"`
	MapOrg            map[string]string         `json:"map_org,omitempty"`
	MapTeam           map[string]string         `json:"map_team,omitempty"`
	MapUser           map[string]string         `json:"map_user,omitempty"`
	Phase             restoreJournalPhase       `json:"phase"`
	Homes             []restoreJournalHome      `json:"homes"`
	PostgresDatabases []postgresRestoreDatabase `json:"postgres_databases,omitempty"`
	PublishTenants    bool                      `json:"publish_tenants"`
	Failure           string                    `json:"failure,omitempty"`
}

func restoreArchiveDigest(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, botBackupContextReader{ctx, f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func writeRestoreJournal(root *os.Root, j *restoreJournal) error {
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	f, err := root.OpenFile(restoreJournalDir+"/journal.tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err = root.Rename(restoreJournalDir+"/journal.tmp", restoreJournalDir+"/journal.json"); err != nil {
		return err
	}
	d, err := root.Open(restoreJournalDir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func newRestoreJournal(ctx context.Context, s *Store, archive string, scopes []backup.Scope, opts PlatformRestoreOptions) (*os.Root, *restoreJournal, error) {
	canonical, err := filepath.EvalSymlinks(s.dataDir)
	if err != nil {
		return nil, nil, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*os.Root, *restoreJournal, error) { root.Close(); return nil, nil, err }
	digest, err := restoreArchiveDigest(ctx, archive)
	if err != nil {
		return fail(err)
	}
	if err = root.Mkdir(restoreJournalDir, 0700); err != nil {
		return fail(fmt.Errorf("restore journal exists or cannot be reserved; use platform restore cleanup: %w", err))
	}
	j := &restoreJournal{Version: 1, ID: uuid.NewString(), Root: canonical, PlatformDSN: s.platformDSN, ArchiveSHA256: digest, Scopes: scopes, MapOrg: opts.MapOrg, MapTeam: opts.MapTeam, MapUser: opts.MapUser, Phase: restorePrepared}
	if err = writeRestoreJournal(root, j); err != nil {
		return fail(err)
	}
	if err = syncBotRestoreRoot(root); err != nil {
		return fail(err)
	}
	return root, j, nil
}
func restoreMarker(root *os.Root, path, id string) error {
	f, err := root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(id)
	if err = errors.Join(err, f.Sync(), f.Close()); err != nil {
		return err
	}
	d, err := root.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
func checkRestoreMarker(root *os.Root, path, id string) error {
	info, err := root.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return store.ErrBotConflict
	}
	raw, err := root.ReadFile(path)
	if err != nil {
		return err
	}
	if string(raw) != id {
		return store.ErrBotConflict
	}
	return nil
}

// CleanupFileFirstRestore discards ONLY an uncommitted restore's fresh physical
// generations and owned staging/tenant tree. It never resets the platform DB,
// deletes the archive, adopts legacy reservations, or detaches any container.
// The daemon and every other target writer must be stopped before invoking it.
func (s *Store) CleanupFileFirstRestore(ctx context.Context, archive string, opts PlatformRestoreOptions) error {
	if (s.dialect != DialectSQLite && s.dialect != DialectPostgres) || opts.FileBackend == nil {
		return store.ErrBotUnsupported
	}
	lock, err := lockFileFirstRestore(s.dataDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	canonical, err := filepath.EvalSymlinks(s.dataDir)
	if err != nil {
		return err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(restoreJournalDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return store.ErrBotConflict
	}
	info, err = root.Lstat(restoreJournalDir + "/journal.json")
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return store.ErrBotConflict
	}
	raw, err := root.ReadFile(restoreJournalDir + "/journal.json")
	if err != nil {
		return err
	}
	var j restoreJournal
	if err = json.Unmarshal(raw, &j); err != nil {
		return err
	}
	if j.Version != 1 || j.PlatformDSN != s.platformDSN || j.Root != canonical || j.ID == "" || j.Phase == restoreCommitted || (s.dialect == DialectSQLite && len(j.PostgresDatabases) != 0) || (s.dialect == DialectPostgres && j.PublishTenants) {
		return store.ErrBotConflict
	}
	digest, err := restoreArchiveDigest(ctx, archive)
	if err != nil {
		return err
	}
	if digest != j.ArchiveSHA256 {
		return fmt.Errorf("archive does not match recovery journal: %w", store.ErrBotConflict)
	}
	// Hold the same SQLite write exclusion as restore. Never erase a platform that
	// has committed, or an ordinary target populated since the failed attempt.
	conn, err := s.platformDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = beginFileFirstPlatformExclusion(ctx, conn, s.dialect); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK") //nolint:errcheck
	for _, table := range []string{"users", "organizations", "org_memberships", "oidc_providers", "platform_settings", "platform_secrets"} {
		var n int
		if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteSQLiteIdent(table)).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return fmt.Errorf("target platform has data; cleanup refused: %w", store.ErrBotConflict)
		}
	}
	info, err = root.Lstat("bot-content")
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return store.ErrBotConflict
	}
	if err = checkRestoreMarker(root, "bot-content/.restore-reservation", j.ID); err != nil {
		if !errors.Is(err, os.ErrNotExist) || (j.Phase != restoreCleaned && len(j.Homes) != 0) {
			return err
		}
		// Before reservation (or after completed removal) we own no content.
		// Never interpret a missing marker as permission to erase later files.
		d, openErr := root.Open("bot-content")
		if openErr != nil {
			return openErr
		}
		children, readErr := d.ReadDir(-1)
		closeErr := d.Close()
		if err = errors.Join(readErr, closeErr); err != nil {
			return err
		}
		if len(children) != 0 {
			return fmt.Errorf("unreserved content retained: %w", store.ErrBotConflict)
		}
	}
	if j.PublishTenants {
		if _, err = root.Lstat("orgs"); err == nil {
			if err = checkRestoreMarker(root, "orgs/.restore-owner", j.ID); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, h := range j.Homes {
		if h.Target.Validate() != nil || h.Target.Ref == h.Source.Ref {
			return store.ErrBotConflict
		}
	}
	backend, err := opts.FileBackend(ctx)
	if err != nil {
		return err
	}
	cleanup, ok := backend.(journaledBotRestoreBackend)
	if !ok {
		return store.ErrBotUnsupported
	}
	for _, h := range j.Homes {
		if !restoreBackendAcceptsBinding(backend, &h.Target) {
			return store.ErrBotConflict
		}
	}
	if s.dialect == DialectPostgres {
		if err = s.cleanupPostgresRestoreDatabases(ctx, root, &j); err != nil {
			return err
		}
	}
	for _, h := range j.Homes {
		if err = cleanup.DeleteBotHome(ctx, &h.Target); err != nil {
			return fmt.Errorf("cleanup Home %s retained; retry cleanup after resolving attachments: %w", h.Target.Ref, err)
		}
	}
	if j.PublishTenants {
		// First move the owned public tree back under the private journal. A
		// recursive delete may remove its marker before failing; keeping a half
		// deleted tree public would make a safe retry impossible.
		if err = root.Rename("orgs", restoreJournalDir+"/removed-orgs"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = root.RemoveAll(restoreJournalDir + "/removed-orgs"); err != nil {
			return err
		}
	}
	// Keep the reservation until contents are removed, so interrupted cleanup can
	// be repeated. os.Root refuses symlink escapes at every removal boundary.
	content, err := root.OpenRoot("bot-content")
	if err != nil {
		return err
	}
	d, err := content.Open(".")
	if err != nil {
		content.Close()
		return err
	}
	children, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		content.Close()
		return err
	}
	for _, child := range children {
		if child.Name() != ".restore-reservation" {
			if err = content.RemoveAll(child.Name()); err != nil {
				content.Close()
				return err
			}
		}
	}
	content.Close()
	if err = root.RemoveAll(restoreJournalDir + "/sql"); err != nil {
		return err
	}
	j.Phase = restoreCleaned
	if err = writeRestoreJournal(root, &j); err != nil {
		return err
	}
	if err = root.Remove("bot-content/.restore-reservation"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return root.RemoveAll(restoreJournalDir)
}

// Import every database privately first. Publish tenant files only after every
// import succeeds; the platform transaction is the final visibility boundary.
func (s *Store) importFileFirstStaged(ctx context.Context, archive string, opts PlatformRestoreOptions, plan backup.RestorePlan, root *os.Root, j *restoreJournal) (*backup.RestoreResult, error) {
	j.Phase = restoreSQLImporting
	if err := writeRestoreJournal(root, j); err != nil {
		return nil, err
	}
	if err := root.Mkdir(restoreJournalDir+"/sql", 0700); err != nil {
		return nil, err
	}
	dir := filepath.Join(j.Root, restoreJournalDir, "sql")
	_, stage, err := NewPlatformServices(ctx, Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		return nil, err
	}
	result, err := stage.restoreSQLiteLogicalBackup(ctx, archive, opts, plan)
	stage.orgClients.Range(func(key, value any) bool {
		if ds, ok := value.(*orgDataStore); ok {
			err = errors.Join(err, ds.Close())
		}
		stage.orgClients.Delete(key)
		return true
	})
	err = errors.Join(err, stage.Close())
	if err != nil {
		return nil, err
	}
	if _, err = root.Lstat(restoreJournalDir + "/sql/orgs"); err == nil {
		if err = restoreMarker(root, restoreJournalDir+"/sql/orgs/.restore-owner", j.ID); err != nil {
			return nil, err
		}
		j.PublishTenants = true
		if err = writeRestoreJournal(root, j); err != nil {
			return nil, err
		}
		// Remove only an empty pre-existing tenant directory. Nonempty roots fail.
		if err = root.Remove("orgs"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err = root.Rename(restoreJournalDir+"/sql/orgs", "orgs"); err != nil {
			return nil, err
		}
		if err = syncBotRestoreRoot(root); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// Counts in result already include this platform import from the private pass.
	var ignored backup.RestoreResult
	err = restoreLogicalEntries(ctx, s.platformDB, DialectSQLite, "", backup.Scope{Kind: "platform"}, opts.fileFirst.files, entriesForScopeForRestore(plan.Archive.Manifest.Entries, backup.Scope{Kind: "platform"}, opts), opts, &ignored)
	if err != nil {
		return nil, err
	}
	j.Phase = restoreCommitted
	if err = writeRestoreJournal(root, j); err != nil {
		return nil, fmt.Errorf("SQL committed but recovery receipt could not be written; do not cleanup: %w", err)
	}
	if err = root.RemoveAll(restoreJournalDir + "/sql"); err != nil {
		return nil, err
	}
	return result, nil
}
