package entstore

import (
	"testing"

	platformmigrate "github.com/SAP/astonish/ent/platform/migrate"
)

func TestPostgresPlatformAutoMigrateTablesIncludesOAuth(t *testing.T) {
	got := map[string]bool{}
	for _, table := range postgresPlatformAutoMigrateTables() {
		if table == nil || table.Name == "" {
			t.Fatal("auto-migrate table list contains a nil or unnamed table")
		}
		got[table.Name] = true
	}
	for _, name := range []string{
		platformmigrate.OauthAuthorizationsTable.Name,
		platformmigrate.OauthClientsTable.Name,
		platformmigrate.OauthConsentsTable.Name,
		platformmigrate.OauthSigningKeysTable.Name,
		platformmigrate.OauthTokensTable.Name,
		platformmigrate.PlatformSkillsTable.Name,
		platformmigrate.SandboxTemplatesTable.Name,
	} {
		if !got[name] {
			t.Errorf("postgres auto-migrate omitted table %q", name)
		}
	}
}
