package entstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/scheduledjob"
	teamsession "github.com/SAP/astonish/ent/team/session"
	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotRepositoryReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "durable-bot.db")
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	open := func() (*teament.Client, *teamBotStore) {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		c := teament.NewClient(teament.Driver(entsql.OpenDB(dialect.SQLite, db)))
		if err := migrateBotSchema(ctx, db, DialectSQLite, func() error { return c.Schema.Create(ctx) }); err != nil {
			_ = c.Close()
			t.Fatal(err)
		}
		return c, &teamBotStore{client: c, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "member", nil }}}
	}
	c, r := open()
	v := store.DefaultBotVersion()
	v.Charter = "Preserve this responsibility"
	req := store.BotCreateRequest{Name: "Persistent", OwnerID: owner, Version: v, IdempotencyKey: "reopen", Goals: []store.BotGoal{{Title: "Receipt", CompletionCriteria: "Confirmed receipt"}}}
	b, err := r.CreateDraft(ctx, req)
	if err != nil {
		_ = c.Close()
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, r = open()
	defer c.Close()
	restored, err := r.Get(ctx, b.ID)
	if err != nil || restored.HomeSessionID != b.HomeSessionID {
		t.Fatalf("restored bot: %+v %v", restored, err)
	}
	again, err := r.CreateDraft(ctx, req)
	if err != nil || again.ID != b.ID {
		t.Fatalf("reopen deduplication: %+v %v", again, err)
	}
	goals, err := r.ListGoals(ctx, b.ID)
	if err != nil || len(goals) != 1 || goals[0].Title != "Receipt" {
		t.Fatalf("restored goals: %+v %v", goals, err)
	}
	activity, err := r.ListActivity(ctx, b.ID, 0, 50)
	if err != nil || len(activity) != 1 || activity[0].Sequence != 1 {
		t.Fatalf("restored activity: %+v %v", activity, err)
	}
	correction := store.BotCorrection{ID: uuid.New(), BotID: b.ID, ExpectedRevision: restored.Revision, SupersededSource: "memory:receipt-date", Replacement: "Expected Friday", GoalIDs: []uuid.UUID{goals[0].ID}, Evidence: store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Correct date", Source: "owner-message", ActorID: owner, ObservedAt: time.Now().UTC()}}
	if err := r.Correct(ctx, correction); err != nil {
		t.Fatal(err)
	}
	internal := r.repository()
	internal.activationCheck = func(context.Context, store.Bot, store.BotVersion) error { return nil }
	active, err := internal.Activate(ctx, store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: restored.Revision + 1, ActorID: owner, Reason: "Enable manual runs"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, r = open()
	defer c.Close()
	if err := r.Correct(ctx, correction); err != nil {
		t.Fatalf("correction retry after reopen: %v", err)
	}
	restored, err = r.Get(ctx, b.ID)
	if err != nil || restored.Status != store.BotActive || restored.Revision != active.Revision {
		t.Fatalf("activation/correction lost: %+v %v", restored, err)
	}
	activity, err = r.ListActivity(ctx, b.ID, 1, 50)
	if err != nil || len(activity) != 2 || activity[0].Kind != store.BotActivityCorrection || activity[1].Kind != store.BotActivityLifecycle {
		t.Fatalf("restored correction/activation: %+v %v", activity, err)
	}
	var intent struct {
		Correction store.BotCorrection `json:"correction"`
		Intent     store.BotOutboxKind `json:"intent"`
	}
	if err := json.Unmarshal(activity[0].Payload, &intent); err != nil {
		t.Fatal(err)
	}
	if intent.Correction.ID != correction.ID || intent.Correction.Replacement != correction.Replacement || intent.Intent != store.BotOutboxReconcile {
		t.Fatalf("lost reconciliation: %+v", intent)
	}
}

func TestBotCommittedResultSurvivesReopen(t *testing.T) {
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "committed.db")
	open := func() (*teament.Client, *teamBotStore) {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		c := teament.NewClient(teament.Driver(entsql.OpenDB(dialect.SQLite, db)))
		if err := migrateBotSchema(ctx, db, DialectSQLite, func() error { return c.Schema.Create(ctx) }); err != nil {
			_ = c.Close()
			t.Fatal(err)
		}
		return c, &teamBotStore{client: c, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "member", nil }}}
	}
	c, r := open()
	v := store.DefaultBotVersion()
	v.Charter = "Preserve final outcome"
	b, err := r.CreateDraft(ctx, store.BotCreateRequest{Name: "Result", OwnerID: owner, Version: v, IdempotencyKey: "result"})
	if err != nil {
		_ = c.Close()
		t.Fatal(err)
	}
	if err := c.Bot.UpdateOneID(b.ID).SetStatus("active").Exec(ctx); err != nil {
		_ = c.Close()
		t.Fatal(err)
	}
	run, err := r.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: 1, Trigger: store.TriggerManual, Instructions: "Check", IdempotencyKey: "check"})
	if err != nil {
		_ = c.Close()
		t.Fatal(err)
	}
	worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: 1, ActorID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
	lease, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "worker", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		_ = c.Close()
		t.Fatal(err)
	}
	work, err := r.LoadWork(worker, lease)
	if err != nil {
		t.Fatal(err)
	}
	q := store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: store.BotReport{Work: store.BuildBotWorkProjection(work), Summary: "Source unavailable", Unverified: []string{"Live price"}}, Usage: store.BotUsage{InputTokens: 9007199254740993}}
	if err := r.CommitRun(worker, q); err != nil {
		_ = c.Close()
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, r = open()
	defer c.Close()
	if err := r.CommitRun(worker, q); err != nil {
		t.Fatalf("retry after reopen: %v", err)
	}
	saved, err := r.GetRun(ctx, b.ID, run.ID)
	if err != nil || saved.Status != store.RunPartial || saved.Usage.InputTokens != q.Usage.InputTokens || saved.Report.Summary != q.Report.Summary {
		t.Fatalf("restored outcome: %+v %v", saved, err)
	}
	activity, err := r.ListActivity(ctx, b.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	reports := 0
	for _, a := range activity {
		if a.Kind == store.BotActivityReport {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("duplicate restored report: %d", reports)
	}
}

func TestBotMigrationReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "team.db")
	open := func() (*sql.DB, *teament.Client) {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		c := teament.NewClient(teament.Driver(entsql.OpenDB(dialect.SQLite, db)))
		return db, c
	}
	db, c := open()
	if err := c.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Session.Create().SetID("existing-chat").SetTitle("Keep this conversation").Save(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := c.ScheduledJob.Create().SetName("existing-adaptive").SetSchedule("0 9 * * *").SetMode(scheduledjob.ModeAdaptive).SetPayload(map[string]any{"instructions": "Keep this job"}).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct the pre-Bot schema without changing the existing chat/job rows.
	for _, statement := range []string{
		"DROP TABLE bot_skill_revisions", "DROP TABLE bot_skills",
		"DROP TABLE bot_approvals", "DROP TABLE bot_runs", "DROP TABLE bot_tracked_items", "DROP TABLE bot_goals", "DROP TABLE bot_versions", "DROP TABLE bot_outbox", "DROP TABLE bot_templates", "DROP TABLE bots",
		"ALTER TABLE sessions DROP COLUMN bot_id", "ALTER TABLE sessions DROP COLUMN bot_run_id", "ALTER TABLE sessions DROP COLUMN purpose", "ALTER TABLE sessions DROP COLUMN retention",
		"ALTER TABLE sandbox_sessions DROP COLUMN bot_home_binding",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare legacy schema: %v", err)
		}
	}
	legacyScope := backup.Scope{Kind: "team", OrgSlug: "org", TeamSlug: "team"}
	legacyVersion, err := schemaVersionForScope(ctx, "sqlite", db, legacyScope)
	if err != nil {
		t.Fatal(err)
	}
	legacyManifest := backup.Manifest{SchemaVersions: map[string]backup.SchemaVersion{backupScopeKey(legacyScope): legacyVersion}}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	db, c = open()
	defer c.Close()
	create := func() error { return c.Schema.Create(ctx) }
	if err := migrateBotSchema(ctx, db, DialectSQLite, create); err != nil {
		t.Fatal(err)
	}
	if err := migrateBotSchema(ctx, db, DialectSQLite, create); err != nil {
		t.Fatalf("idempotency: %v", err)
	}
	if err := checkSQLiteScopeSchemaCompatible(ctx, legacyManifest, db, legacyScope); err != nil {
		t.Fatalf("pre-Bot backup upgrade rejected: %v", err)
	}
	legacyManifest.Entries = []backup.Entry{{Scope: legacyScope, Entity: "bot_runs"}}
	if err := checkSQLiteScopeSchemaCompatible(ctx, legacyManifest, db, legacyScope); err == nil {
		t.Fatal("Bot archive accepted as legacy")
	}
	legacyManifest.Entries = nil
	if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN unrelated TEXT`); err != nil {
		t.Fatal(err)
	}
	if err := checkSQLiteScopeSchemaCompatible(ctx, legacyManifest, db, legacyScope); err == nil {
		t.Fatal("unrelated schema mismatch accepted")
	}
	restored, err := c.Session.Get(ctx, "existing-chat")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Title != "Keep this conversation" || restored.Purpose != teamsession.PurposeChat || restored.Retention != teamsession.RetentionStandard || restored.BotID != nil {
		t.Fatalf("existing chat changed: %+v", restored)
	}
	saved, err := c.ScheduledJob.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Mode != scheduledjob.ModeAdaptive || saved.Payload["instructions"] != "Keep this job" {
		t.Fatalf("existing adaptive changed: %+v", saved)
	}
	var version int
	if err := db.QueryRowContext(ctx, "SELECT version FROM bot_schema_version WHERE id = 'bots'").Scan(&version); err != nil || version != botMigrationVersion {
		t.Fatalf("version %d: %v", version, err)
	}
	createBotSchemaFixture(t, c)
}

func TestBotMigrationRejectsNewerVersionBeforeChanges(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "future.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE bot_schema_version (id TEXT PRIMARY KEY, version INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO bot_schema_version (id,version) VALUES ('bots',999)"); err != nil {
		t.Fatal(err)
	}
	called := false
	err = migrateBotSchema(ctx, db, DialectSQLite, func() error { called = true; return nil })
	if !errors.Is(err, store.ErrBotUnsupported) || called {
		t.Fatalf("newer schema modified: called=%v error=%v", called, err)
	}
}

func TestBotMigrationDoesNotStampFailure(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "failed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	failure := errors.New("schema migration failed")
	err = migrateBotSchema(ctx, db, DialectSQLite, func() error { return failure })
	if !errors.Is(err, failure) {
		t.Fatalf("lost error: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'bot_schema_version'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("stamped failed migration: %d %v", count, err)
	}
}
