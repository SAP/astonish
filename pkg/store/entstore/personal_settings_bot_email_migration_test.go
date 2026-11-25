package entstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/schema/field"
	personalent "github.com/SAP/astonish/ent/personal"
	personalmigrate "github.com/SAP/astonish/ent/personal/migrate"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Exercise a real pre-mailbox SQLite table, rather than creating the current
// schema and treating an unset value as migration evidence.
func TestPersonalSettingsBotEmailLegacySQLiteUpgrade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-personal.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.ExecContext(ctx, `CREATE TABLE personal_settings (
		id integer NOT NULL PRIMARY KEY AUTOINCREMENT,
		user_id uuid NOT NULL,
		default_provider text NOT NULL DEFAULT '',
		default_model text NOT NULL DEFAULT '',
		brand_theme text NOT NULL DEFAULT '',
		created_at datetime NOT NULL DEFAULT (datetime('now')),
		updated_at datetime NOT NULL DEFAULT (datetime('now'))
	);
	CREATE UNIQUE INDEX personalsettings_user_id ON personal_settings(user_id);`)
	if err != nil {
		t.Fatal(err)
	}
	otherID := uuid.NewString()
	for _, id := range []string{uuid.Nil.String(), otherID} {
		if _, err := db.ExecContext(ctx, `INSERT INTO personal_settings(user_id, default_provider, default_model, brand_theme) VALUES (?, 'controlled', 'legacy-model', 'nova')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.QueryContext(ctx, "SELECT bot_email FROM personal_settings"); err == nil {
		t.Fatal("legacy fixture unexpectedly includes bot_email")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	open := func() (*personalent.Client, *sql.DB) {
		t.Helper()
		db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatal(err)
		}
		client := personalent.NewClient(personalent.Driver(entsql.OpenDB(dialect.SQLite, db)))
		t.Cleanup(func() { client.Close() })
		if err := migrateBotSchema(ctx, db, DialectSQLite, func() error { return client.Schema.Create(ctx) }); err != nil {
			t.Fatal(err)
		}
		return client, db
	}
	client, db := open()
	owner := (&personalDataStore{client: client}).PersonalSettings() // local-user sentinel
	other := (&personalDataStore{client: client, userID: otherID}).PersonalSettings()
	legacy := &store.PersonalSettings{DefaultProvider: "controlled", DefaultModel: "legacy-model", BrandTheme: "nova"}
	for _, scoped := range []store.PersonalSettingsStore{owner, other} {
		got, err := scoped.Get(ctx)
		if err != nil || !reflect.DeepEqual(got, legacy) {
			t.Fatalf("legacy values/mailbox absence changed: %+v %v", got, err)
		}
	}
	want := *legacy
	want.BotEmail = &store.BotEmailSettings{Provider: "imap", Address: "owner@example.com", IMAPServer: "127.0.0.1:1143", SMTPServer: "127.0.0.1:1025", Credential: "personal-mail", Folder: "INBOX"}
	if err := owner.Save(ctx, &want); err != nil {
		t.Fatal(err)
	}
	// Update every mailbox field through the existing-row path.
	want.BotEmail = &store.BotEmailSettings{Provider: "graph", Address: "updated@example.com", Credential: "graph-mail", Folder: "Archive"}
	if err := owner.Save(ctx, &want); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRowContext(ctx, "SELECT bot_email FROM personal_settings WHERE user_id = ?", uuid.Nil.String()).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"credential":"graph-mail"`) {
		t.Fatalf("missing stored credential reference: %s", raw)
	}
	for _, secret := range []string{`"password"`, `"token"`, `"access_token"`, `"refresh_token"`} {
		if strings.Contains(raw, secret) {
			t.Fatalf("secret field persisted: %s", secret)
		}
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	client, db = open() // idempotent additive reconciliation + durable reopen
	owner = (&personalDataStore{client: client}).PersonalSettings()
	other = (&personalDataStore{client: client, userID: otherID}).PersonalSettings()
	got, err := owner.Get(ctx)
	if err != nil || !reflect.DeepEqual(got, &want) {
		t.Fatalf("reopened mailbox: %+v %v", got, err)
	}
	foreign, err := other.Get(ctx)
	if err != nil || !reflect.DeepEqual(foreign, legacy) {
		t.Fatalf("mailbox inherited across owner scope: %+v %v", foreign, err)
	}
	want.BotEmail = nil
	if err := owner.Save(ctx, &want); err != nil {
		t.Fatal(err)
	}
	got, err = owner.Get(ctx)
	if err != nil || !reflect.DeepEqual(got, legacy) {
		t.Fatalf("clear changed other preferences: %+v %v", got, err)
	}
	var cleared sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT bot_email FROM personal_settings WHERE user_id = ?", uuid.Nil.String()).Scan(&cleared); err != nil || cleared.Valid {
		t.Fatalf("clear must persist SQL NULL: %+v %v", cleared, err)
	}
	if count, err := client.PersonalSettings.Query().Count(ctx); err != nil || count != 2 {
		t.Fatalf("migration/upsert changed legacy row identities: %d %v", count, err)
	}
}

// Metadata contract applies to both supported dialects. This is deliberately
// not labelled live PostgreSQL migration evidence.
func TestPersonalSettingsBotEmailMigrationColumnContract(t *testing.T) {
	for _, column := range personalmigrate.PersonalSettingsColumns {
		if column.Name == "bot_email" {
			if column.Type != field.TypeJSON || !column.Nullable || column.Default != nil {
				t.Fatalf("mailbox must be additive nullable JSON without inheritance default: %+v", column)
			}
			return
		}
	}
	t.Fatal("generated migration metadata lacks bot_email")
}
