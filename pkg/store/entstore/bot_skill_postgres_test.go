//go:build integration

package entstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Normal integration selection can skip infrastructure; explicit acceptance
// MUST set ASTONISH_REQUIRE_SKILL_POSTGRES=1 so an absent DSN is a failure.
func requireSkillPostgres(t *testing.T) {
	t.Helper()
	if os.Getenv("ASTONISH_TEST_DSN") != "" {
		if os.Getenv("ASTONISH_BOTS_TEST_DB_ISOLATED") != "1" {
			t.Fatal("ASTONISH_BOTS_TEST_DB_ISOLATED=1 required: refuse unacknowledged PostgreSQL target")
		}
		return
	}
	if os.Getenv("ASTONISH_REQUIRE_SKILL_POSTGRES") == "1" {
		t.Fatal("ASTONISH_TEST_DSN required: isolated disposable PostgreSQL only")
	}
	t.Skip("requires disposable ASTONISH_TEST_DSN; acceptance sets ASTONISH_REQUIRE_SKILL_POSTGRES=1")
}

func TestBotSkillPostgresPreSkillArchive(t *testing.T) {
	requireSkillPostgres(t)
	ctx := context.Background()
	f := newPostgresBotServicesFixture(t, store.BotScopePersonal, 100, 100)
	p, _ := execution.PrincipalFromContext(f.owner)
	db, err := f.platform.openRestorePostgresDB("restorepg", "")
	if err != nil {
		t.Fatal(err)
	}
	scopes := []backup.Scope{{Kind: "team", OrgSlug: "restorepg", TeamSlug: "ops"}, {Kind: "personal", OrgSlug: "restorepg", UserID: p.Subject}}
	for _, scope := range scopes {
		schema := quoteIdent(DialectPostgres, postgresSchemaForScope(scope)) + "."
		for _, statement := range []string{"DROP TABLE " + schema + "bot_skill_revisions", "DROP TABLE " + schema + "bot_skills", "UPDATE " + schema + "bot_schema_version SET version=1"} {
			execPreBotPostgres(t, db, statement)
		}
	}
	_ = db.Close()
	archive := filepath.Join(t.TempDir(), "pre-skill.astonish-backup")
	if err := f.platform.ExportPlatformBackup(ctx, archive, PlatformBackupExportOptions{Backend: "postgres"}); err != nil {
		t.Fatal(err)
	}
	summary, err := backup.Verify(archive)
	if err != nil {
		t.Fatal(err)
	}
	target := setupPostgresBackupRestoreStore(t, "skill_legacy")
	if _, err := target.RestorePlatformBackup(ctx, archive, PlatformRestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	target = reopenPreBotPostgres(t, target)
	org, err := target.ForOrg("restorepg")
	if err != nil {
		t.Fatal(err)
	}
	bots := org.ForUser(p.Subject).Bots()
	b, err := bots.Get(f.owner, f.run.BotID)
	if err != nil || b.Status != store.BotPaused {
		t.Fatalf("legacy Bot lost: %+v %v", b, err)
	}
	list, err := bots.(store.BotSkillStore).ListSkills(f.owner, b.ID, store.BotSkillPage{})
	if err != nil || len(list.Skills) != 0 {
		t.Fatal("invented legacy skill")
	}
	db, err = target.openRestorePostgresDB("restorepg", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, scope := range scopes {
		if err := checkPostgresScopeSchemaCompatible(ctx, summary.Manifest, db, scope); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"bot_skills", "bot_skill_revisions"} {
			bad := summary.Manifest
			bad.Entries = append(append([]backup.Entry(nil), bad.Entries...), backup.Entry{Scope: scope, Entity: table})
			if err := checkPostgresScopeSchemaCompatible(ctx, bad, db, scope); err == nil {
				t.Fatal("skill-bearing old archive accepted")
			}
		}
		schema := quoteIdent(DialectPostgres, postgresSchemaForScope(scope)) + "."
		execPreBotPostgres(t, db, "ALTER TABLE "+schema+"sessions ADD COLUMN unrelated TEXT")
		if err := checkPostgresScopeSchemaCompatible(ctx, summary.Manifest, db, scope); err == nil {
			t.Fatal("arbitrary drift accepted")
		}
		execPreBotPostgres(t, db, "ALTER TABLE "+schema+"sessions DROP COLUMN unrelated")
	}
}

func TestBotSkillPostgresRecovery(t *testing.T) {
	requireSkillPostgres(t)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newPostgresBotServicesFixture(t, scope, 100, 100)
			r := f.repo
			v := skillTestRevision(t, r, f.owner, f.run.BotID)
			work, err := r.LoadWork(f.worker, f.lease)
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotSkillOrientation{Kind: store.BotSkillCreateDraft, Key: "recovery", Title: "Recovery", Intent: "Observe ledger", Draft: &v, Mutation: store.BotSkillMutation{BotID: f.run.BotID, Lease: f.lease, ExpectedRevision: work.Revision, IdempotencyKey: "pg-skill"}}
			foreign := v.Clone()
			foreign.Expectations[0].Provenance.BotID = uuid.New()
			foreign.Digest = foreign.ContentDigest()
			q.Draft = foreign
			if _, err = r.OrientSkill(f.worker, q); err == nil {
				t.Fatal("foreign evidence accepted")
			}
			rows, err := r.ListSkills(f.owner, f.run.BotID, store.BotSkillPage{})
			if err != nil || len(rows.Skills) != 0 {
				t.Fatal("failed provenance write leaked rows")
			}
			q.Draft = &v
			created, err := r.OrientSkill(f.worker, q)
			if err != nil {
				t.Fatal(err)
			}
			retry, err := r.OrientSkill(f.worker, q)
			if err != nil || retry.Work.Revision != created.Work.Revision {
				t.Fatalf("replay: %v", err)
			}
			// Root/work CAS: both requests use the same expected work revision.
			next := v.Clone()
			next.ID = uuid.New()
			next.Revision = 2
			next.ParentRevision = 1
			next.ParentDigest = v.Digest
			next.ChangeReason = "Owner clarification"
			next.Digest = next.ContentDigest()
			rollback := errors.New("injected skill transaction failure")
			err = r.db.Transaction(f.owner, func(tx botDraftDB) error {
				db := tx.(botSkillDB)
				root, err := db.readSkill(f.owner, v.BotID, v.SkillID)
				if err != nil {
					return err
				}
				root.Skill.Title = "must roll back"
				root.Skill.Revision++
				if err := db.writeSkill(f.owner, *root, 1); err != nil {
					return err
				}
				if err := db.insertSkillRevision(f.owner, *next); err != nil {
					return err
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				t.Fatalf("rollback injection: %v", err)
			}
			root, err := r.GetSkill(f.owner, v.BotID, v.SkillID)
			if err != nil || root.Revision != 1 || root.Title != q.Title {
				t.Fatal("root update escaped rollback")
			}
			if _, err := r.GetSkillRevision(f.owner, v.BotID, v.SkillID, 2); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("revision escaped rollback: %v", err)
			}
			update := q
			update.Kind = store.BotSkillUpdateOwnerExpectations
			update.SkillID = &v.SkillID
			update.Draft = next
			update.Mutation.ExpectedRevision = created.Work.Revision
			update.Mutation.Source = v.Expectations[0].Provenance
			results := make(chan error, 2)
			for _, key := range []string{"cas-a", "cas-b"} {
				go func(key string) {
					request := update
					request.Mutation.IdempotencyKey = key
					_, err := r.OrientSkill(f.worker, request)
					results <- err
				}(key)
			}
			wins, conflicts := 0, 0
			for i := 0; i < 2; i++ {
				err := <-results
				if err == nil {
					wins++
				} else if errors.Is(err, store.ErrBotConflict) {
					conflicts++
				} else {
					t.Fatalf("CAS: %v", err)
				}
			}
			if wins != 1 || conflicts != 1 {
				t.Fatalf("CAS winners=%d conflicts=%d", wins, conflicts)
			}
			p, _ := execution.PrincipalFromContext(f.owner)
			archive := filepath.Join(t.TempDir(), "skills.astonish-backup")
			if err := f.platform.ExportPlatformBackup(context.Background(), archive, PlatformBackupExportOptions{Backend: "postgres"}); err != nil {
				t.Fatal(err)
			}
			target := setupPostgresBackupRestoreStore(t, "skill_recovery")
			if _, err := target.RestorePlatformBackup(context.Background(), archive, PlatformRestoreOptions{IncludeTransient: true, EnableScheduledJobs: true}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				target = reopenPreBotPostgres(t, target)
				org, err := target.ForOrg("restorepg")
				if err != nil {
					t.Fatal(err)
				}
				bots := org.ForTeam("ops").Bots()
				if scope == store.BotScopePersonal {
					bots = org.ForUser(p.Subject).Bots()
				}
				skills := bots.(store.BotSkillStore)
				for _, want := range []*store.BotSkillRevision{&v, next} {
					got, err := skills.GetSkillRevision(f.owner, f.run.BotID, v.SkillID, want.Revision)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("reopened history changed: %+v %v", got, err)
					}
				}
				b, err := bots.Get(f.owner, f.run.BotID)
				if err != nil || b.Status != store.BotPaused {
					t.Fatalf("unsafe root: %+v %v", b, err)
				}
				runs, err := bots.ListRuns(f.owner, b.ID, store.BotPage{})
				if err != nil || len(runs) != 1 || runs[0].Status != store.RunInterrupted {
					t.Fatalf("unsafe runs: %+v %v", runs, err)
				}
				if _, err := skills.GetSkill(f.owner, uuid.New(), v.SkillID); !errors.Is(err, store.ErrBotNotFound) {
					t.Fatalf("cross Bot read: %v", err)
				}
				db, err := target.openRestorePostgresDB("restorepg", "")
				if err != nil {
					t.Fatal(err)
				}
				bs := backup.Scope{Kind: "team", OrgSlug: "restorepg", TeamSlug: "ops"}
				if scope == store.BotScopePersonal {
					bs = backup.Scope{Kind: "personal", OrgSlug: "restorepg", UserID: p.Subject}
				}
				var version int
				err = db.QueryRow("SELECT version FROM " + quoteIdent(DialectPostgres, postgresSchemaForScope(bs)) + ".bot_schema_version WHERE id='bots'").Scan(&version)
				_ = db.Close()
				if err != nil || version != botMigrationVersion {
					t.Fatalf("schema version %d: %v", version, err)
				}
			}
		})
	}
}
