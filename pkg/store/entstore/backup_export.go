package entstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

const backupModeLogical = "logical"

type BotBackupBackend interface {
	sandbox.BotHomeFilesBackend
	sandbox.BotHomeStreamArchiveBackend
}

type PlatformBackupExportOptions struct {
	// FileBackend is lazy; SQLite owns the writer fence, not the caller.
	FileBackend      func(context.Context) (BotBackupBackend, error)
	contentDirectory string
	spoolDirectory   string
	fileInventory    *fileFirstSQLInventory
	Backend          string
	Compression      backup.Compression
	RedactSecrets    bool
	Passphrase       string
	OrgSlug          string
	TeamSlug         string
	UserID           string
	WorkspaceBackend sandbox.BotHomeWorkspaceBackend
	WorkspaceBinding func(backup.Scope, store.BotID, store.BotHomeBinding) (*sandbox.BotHomeBinding, error)
	// Trusted server callback holds all scope writers during SQL and file export.
	WorkspaceFence func(context.Context, backup.Scope, func() error) error
}

func (s *Store) ExportPlatformBackup(ctx context.Context, archivePath string, opts PlatformBackupExportOptions) error {
	// Never expose a partial archive or truncate the last good backup on refusal.
	// Keep plaintext and encrypted intermediates in a private sibling directory so
	// the final rename stays on the destination filesystem.
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, err := os.MkdirTemp(filepath.Dir(archivePath), ".platform-backup-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	staged := filepath.Join(directory, "archive")
	if err := s.exportPlatformBackup(ctx, staged, opts); err != nil {
		return err
	}
	if _, err := VerifyPlatformBackup(ctx, staged, backup.ReaderOptions{Passphrase: opts.Passphrase}); err != nil {
		return err
	}
	file, err := os.OpenFile(staged, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := file.Chmod(0600); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(staged, archivePath)
}

func (s *Store) exportPlatformBackup(ctx context.Context, archivePath string, opts PlatformBackupExportOptions) error {
	if opts.Passphrase != "" {
		plainPath := archivePath + ".plain"
		plainOpts := opts
		plainOpts.Passphrase = ""
		if err := s.exportPlatformBackup(ctx, plainPath, plainOpts); err != nil {
			return err
		}
		defer os.Remove(plainPath)
		return backup.EncryptArchiveFile(plainPath, archivePath, opts.Passphrase)
	}
	if s.dialect == DialectSQLite {
		return s.ExportSQLiteLogicalBackup(ctx, archivePath, opts)
	}
	if s.dialect == DialectPostgres {
		return s.ExportPostgresLogicalBackup(ctx, archivePath, opts)
	}
	backend := opts.Backend
	if backend == "" {
		backend = string(s.dialect)
	}
	manifest := backup.NewManifest(backend, backupModeLogical, []backup.Scope{{Kind: "platform"}})
	manifest.SchemaVersions = map[string]backup.SchemaVersion{
		"platform": {Scope: "platform"},
	}

	writer, err := backup.Create(archivePath, backup.WriterOptions{Compression: opts.Compression})
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = writer.Close()
		}
	}()

	if opts.RedactSecrets {
		manifest.Features = append(manifest.Features, "redacted-secrets")
	}

	if err := s.exportPlatformCollection(ctx, writer, &manifest, "platform/users.jsonl", "users", func(ctx context.Context) ([]backup.RecordValue, error) { return s.exportUsers(ctx, opts.RedactSecrets) }); err != nil {
		return err
	}
	if err := s.exportPlatformCollection(ctx, writer, &manifest, "platform/organizations.jsonl", "organizations", s.exportOrganizations); err != nil {
		return err
	}
	if err := s.exportPlatformCollection(ctx, writer, &manifest, "platform/org_memberships.jsonl", "org_memberships", s.exportOrgMemberships); err != nil {
		return err
	}
	if err := s.exportPlatformCollection(ctx, writer, &manifest, "platform/oidc_providers.jsonl", "oidc_providers", func(ctx context.Context) ([]backup.RecordValue, error) {
		return s.exportOIDCProviders(ctx, opts.RedactSecrets)
	}); err != nil {
		return err
	}

	if err := writer.CloseWithManifest(manifest); err != nil {
		return err
	}
	closed = true
	return nil
}

func (s *Store) exportPlatformCollection(ctx context.Context, writer *backup.Writer, manifest *backup.Manifest, path, entity string, load func(context.Context) ([]backup.RecordValue, error)) error {
	values, err := load(ctx)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	recordWriter := backup.NewRecordWriter(&buf, entity)
	for _, value := range values {
		if err := recordWriter.Write(value.ID, value.Value); err != nil {
			return err
		}
	}
	if _, err := writer.AddFile(path, &buf); err != nil {
		return err
	}
	manifest.Entries = append(manifest.Entries, backup.Entry{
		Path:    path,
		Kind:    "jsonl",
		Scope:   backup.Scope{Kind: "platform"},
		Entity:  entity,
		Records: recordWriter.Records(),
	})
	return nil
}

func (s *Store) exportUsers(ctx context.Context, redactSecrets bool) ([]backup.RecordValue, error) {
	users, err := s.Users().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("export users: %w", err)
	}
	values := make([]backup.RecordValue, 0, len(users))
	for _, user := range users {
		value := any(user)
		if redactSecrets {
			value = exportUser(*user)
		}
		values = append(values, backup.RecordValue{ID: user.ID, Value: value})
	}
	return values, nil
}

func (s *Store) exportOrganizations(ctx context.Context) ([]backup.RecordValue, error) {
	orgs, err := s.Organizations().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("export organizations: %w", err)
	}
	values := make([]backup.RecordValue, 0, len(orgs))
	for _, org := range orgs {
		values = append(values, backup.RecordValue{ID: org.ID, Value: org})
	}
	return values, nil
}

func (s *Store) exportOrgMemberships(ctx context.Context) ([]backup.RecordValue, error) {
	users, err := s.Users().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users for org memberships: %w", err)
	}
	var values []backup.RecordValue
	for _, user := range users {
		memberships, err := s.Organizations().GetUserOrgs(ctx, user.ID)
		if err != nil {
			return nil, fmt.Errorf("export org memberships for user %s: %w", user.ID, err)
		}
		for _, membership := range memberships {
			id := membership.UserID + ":" + membership.OrgID
			values = append(values, backup.RecordValue{ID: id, Value: membership})
		}
	}
	return values, nil
}

func (s *Store) exportOIDCProviders(ctx context.Context, redactSecrets bool) ([]backup.RecordValue, error) {
	providers, err := s.OIDCProviders().List(ctx, "*")
	if err != nil {
		return nil, fmt.Errorf("export oidc providers: %w", err)
	}
	values := make([]backup.RecordValue, 0, len(providers))
	for _, provider := range providers {
		value := any(provider)
		if redactSecrets {
			value = exportOIDCProvider(*provider)
		}
		values = append(values, backup.RecordValue{ID: provider.ID, Value: value})
	}
	return values, nil
}

type exportUser store.User

func (u exportUser) MarshalJSON() ([]byte, error) {
	type alias store.User
	value := alias(u)
	value.PasswordHash = ""
	return json.Marshal(value)
}

type exportOIDCProvider store.OIDCProvider

func (p exportOIDCProvider) MarshalJSON() ([]byte, error) {
	type alias store.OIDCProvider
	value := alias(p)
	if value.ClientSecret != "" {
		value.ClientSecret = "[REDACTED]"
	}
	return json.Marshal(value)
}
