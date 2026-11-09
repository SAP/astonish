package entstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestRestorePlatformBackupSQLiteFreshTarget(t *testing.T) {
	for _, enable := range []bool{false, true} {
		name := "default"
		if enable {
			name = "explicit-runtime-options"
		}
		t.Run(name, func(t *testing.T) {
			testRestorePlatformBackupSQLiteFreshTarget(t, PlatformRestoreOptions{EnableScheduledJobs: enable, IncludeTransient: enable})
		})
	}
}

func testRestorePlatformBackupSQLiteFreshTarget(t *testing.T, opts PlatformRestoreOptions) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	sourceDSN := "file:" + filepath.Join(sourceDir, "platform.db")
	if err := BootstrapPlatform(ctx, Config{DSN: sourceDSN, DataDir: sourceDir}, nil); err != nil {
		t.Fatalf("BootstrapPlatform(source) error = %v", err)
	}
	source, err := New(ctx, Config{DSN: sourceDSN, DataDir: sourceDir})
	if err != nil {
		t.Fatalf("New(source) error = %v", err)
	}
	defer source.Close()

	userID := uuid.NewString()
	orgID := uuid.NewString()
	if err := source.Users().Create(ctx, &store.User{ID: userID, Email: "restore@example.com", DisplayName: "Restore User", PasswordHash: "hash", Status: "active"}); err != nil {
		t.Fatalf("Create user error = %v", err)
	}
	if err := source.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Restore Org", Slug: "restore", Status: "active"}); err != nil {
		t.Fatalf("Create org error = %v", err)
	}
	if err := source.Organizations().AddMember(ctx, userID, orgID, "owner"); err != nil {
		t.Fatalf("AddMember error = %v", err)
	}
	if err := source.ProvisionOrg(ctx, orgID, "restore"); err != nil {
		t.Fatalf("ProvisionOrg error = %v", err)
	}
	orgStore, err := source.ForOrg("restore")
	if err != nil {
		t.Fatalf("ForOrg error = %v", err)
	}
	team := &store.Team{ID: uuid.NewString(), Name: "Ops", Slug: "ops", SchemaName: "ops"}
	if err := orgStore.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatalf("CreateTeam error = %v", err)
	}
	if err := orgStore.ProvisionTeam(ctx, "ops"); err != nil {
		t.Fatalf("ProvisionTeam error = %v", err)
	}
	if err := orgStore.ProvisionPersonalSchema(ctx, userID); err != nil {
		t.Fatalf("ProvisionPersonalSchema error = %v", err)
	}
	seedTeamBackupRows(t, filepath.Join(sourceDir, "orgs", "restore", "teams", "ops.db"))
	seedSessionBackupRows(t, filepath.Join(sourceDir, "orgs", "restore", "teams", "ops.db"))
	seedScheduledJob(t, filepath.Join(sourceDir, "orgs", "restore", "teams", "ops.db"))

	if err := orgStore.Teams().AddMember(ctx, &store.TeamMembership{UserID: userID, TeamID: team.ID, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	botCtx, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: userID, OrgSlug: "restore", TeamSlug: "ops", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.MustParse(userID)
	botIDs := map[store.BotScope]uuid.UUID{}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		var repo store.BotStore
		if scope == store.BotScopePersonal {
			repo = orgStore.ForUser(userID).Bots()
		} else {
			repo = orgStore.ForTeam("ops").Bots()
		}
		v := store.DefaultBotVersion()
		v.Charter = "Retain this responsibility"
		b, err := repo.CreateDraft(botCtx, store.BotCreateRequest{Name: "Backup Bot", OwnerID: owner, Version: v, IdempotencyKey: "backup-bot", Goals: []store.BotGoal{{Title: "Delivery", CompletionCriteria: "Received"}}})
		if err != nil {
			t.Fatal(err)
		}
		botIDs[scope] = b.ID
		goals, err := repo.ListGoals(botCtx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.SaveTrackedItem(botCtx, b.ID, store.BotTrackedItem{ID: uuid.New(), GoalID: goals[0].ID, Title: "Confirm delivery", Status: store.BotTrackedOpen}, 1); err != nil {
			t.Fatal(err)
		}
		if err := repo.Correct(botCtx, store.BotCorrection{ID: uuid.New(), BotID: b.ID, ExpectedRevision: 1, SupersededSource: "memory:date", Replacement: "Friday", Evidence: store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, ActorID: owner, Source: "owner", Summary: "Date corrected", ObservedAt: time.Now().UTC()}}); err != nil {
			t.Fatal(err)
		}
	}
	deletedIDs := map[store.BotScope]uuid.UUID{}
	deletedUntil := map[store.BotScope]time.Time{}
	for scope := range botIDs {
		var repo store.BotStore
		if scope == store.BotScopePersonal {
			repo = orgStore.ForUser(userID).Bots()
		} else {
			repo = orgStore.ForTeam("ops").Bots()
		}
		v := store.DefaultBotVersion()
		v.Charter = "Retained deletion"
		b, err := repo.CreateDraft(botCtx, store.BotCreateRequest{Name: "Deleted Bot", OwnerID: owner, Version: v, IdempotencyKey: "deleted"})
		if err != nil {
			t.Fatal(err)
		}
		b, err = repo.BeginDelete(botCtx, store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: owner, Reason: "Retain for restore"})
		if err != nil {
			t.Fatal(err)
		}
		deletedIDs[scope] = b.ID
		deletedUntil[scope] = *b.RestoreUntil
	}
	for scope, id := range botIDs {
		dbPath := filepath.Join(sourceDir, "orgs", "restore", "teams", "ops.db")
		if scope == store.BotScopePersonal {
			dbPath = filepath.Join(sourceDir, "orgs", "restore", "personal", userID+".db")
		}
		seedBotRecoveryRows(t, dbPath, id)
	}
	template, err := orgStore.ForTeam("ops").BotTemplates().Publish(botCtx, store.BotTemplateDefinition{SchemaVersion: store.BotSchemaVersion, Name: "Backup template", Charter: "Reviewed public observation"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "restore.astonish-backup")
	if err := source.ExportPlatformBackup(ctx, archivePath, PlatformBackupExportOptions{Backend: "sqlite"}); err != nil {
		t.Fatalf("ExportPlatformBackup() error = %v", err)
	}

	targetDir := t.TempDir()
	targetDSN := "file:" + filepath.Join(targetDir, "platform.db")
	if err := BootstrapPlatform(ctx, Config{DSN: targetDSN, DataDir: targetDir}, nil); err != nil {
		t.Fatalf("BootstrapPlatform(target) error = %v", err)
	}
	target, err := New(ctx, Config{DSN: targetDSN, DataDir: targetDir})
	if err != nil {
		t.Fatalf("New(target) error = %v", err)
	}
	defer target.Close()

	plan, err := target.PlanPlatformRestore(ctx, archivePath, PlatformRestoreOptions{DryRun: true})
	if err != nil {
		t.Fatalf("PlanPlatformRestore() error = %v", err)
	}
	if len(plan.Blockers) != 0 {
		t.Fatalf("plan blockers = %v, want none", plan.Blockers)
	}
	result, err := target.RestorePlatformBackup(ctx, archivePath, opts)
	if err != nil {
		t.Fatalf("RestorePlatformBackup() error = %v", err)
	}
	if result.RestoredRecords == 0 {
		t.Fatal("RestoredRecords = 0, want records")
	}

	files, err := backup.ReadArchiveFiles(archivePath)
	if err != nil {
		t.Fatalf("ReadArchiveFiles() error = %v", err)
	}
	if !strings.Contains(string(files["orgs/restore/teams/ops/apps.jsonl"]), "backup-app") {
		t.Fatal("source archive missing seeded app")
	}
	assertRestoredTableContains(t, filepath.Join(targetDir, "platform.db"), "users", "password_hash", "hash")
	assertRestoredTableContains(t, filepath.Join(targetDir, "orgs", "restore", "teams", "ops.db"), "apps", "slug", "backup-app")
	assertRestoredTableContains(t, filepath.Join(targetDir, "orgs", "restore", "teams", "ops.db"), "flows", "name", "backup-flow")
	assertRestoredTableContains(t, filepath.Join(targetDir, "orgs", "restore", "teams", "ops.db"), "sessions", "title", "backup-session")
	assertRestoredTableContains(t, filepath.Join(targetDir, "orgs", "restore", "teams", "ops.db"), "session_events", "session_id", "backup-session-id")
	if !opts.EnableScheduledJobs {
		assertRestoredScheduledJobPaused(t, filepath.Join(targetDir, "orgs", "restore", "teams", "ops.db"))
	} else {
		assertRestoredTableContains(t, filepath.Join(targetDir, "orgs", "restore", "teams", "ops.db"), "scheduled_jobs", "status", "active")
	}
	for scope := range botIDs {
		dbPath := filepath.Join(targetDir, "orgs", "restore", "teams", "ops.db")
		if scope == store.BotScopePersonal {
			dbPath = filepath.Join(targetDir, "orgs", "restore", "personal", userID+".db")
		}
		assertBotRecoveryRows(t, dbPath)
	}
	restoredOrg, err := target.ForOrg("restore")
	if err != nil {
		t.Fatal(err)
	}
	restoredTemplate, err := restoredOrg.ForTeam("ops").BotTemplates().Get(botCtx, template.ID)
	if err != nil || restoredTemplate.Revision != 1 || restoredTemplate.Definition.Charter != template.Definition.Charter {
		t.Fatalf("template restore: %+v %v", restoredTemplate, err)
	}
	for scope, id := range botIDs {
		var repo store.BotStore
		var sessions store.SessionStore
		if scope == store.BotScopePersonal {
			ds := restoredOrg.ForUser(userID)
			repo = ds.Bots()
			sessions = ds.Sessions()
		} else {
			ds := restoredOrg.ForTeam("ops")
			repo = ds.Bots()
			sessions = ds.Sessions()
		}
		retained, err := repo.Get(botCtx, deletedIDs[scope])
		if err != nil || retained.Status != store.BotDeleted || retained.RestoreUntil == nil || !retained.RestoreUntil.Equal(deletedUntil[scope]) {
			t.Fatalf("retained deletion: %+v %v", retained, err)
		}
		restored, err := repo.Restore(botCtx, store.BotLifecycleRequest{BotID: retained.ID, ExpectedRevision: retained.Revision, ActorID: owner, Reason: "Restore after backup"})
		if err != nil || restored.Status != store.BotPaused {
			t.Fatalf("restore retained Bot: %+v %v", restored, err)
		}
		b, err := repo.Get(botCtx, id)
		if err != nil {
			t.Fatalf("restored %s Bot: %v", scope, err)
		}
		if b.Status != store.BotPaused || b.Revision != 3 || b.OwnerID != owner {
			t.Fatalf("restored Bot state: %+v", b)
		}
		v, err := repo.GetVersion(botCtx, id, 1)
		if err != nil || v.Charter != "Retain this responsibility" {
			t.Fatalf("version: %+v %v", v, err)
		}
		goals, err := repo.ListGoals(botCtx, id)
		if err != nil || len(goals) != 1 || goals[0].Revision != 2 {
			t.Fatalf("goals: %+v %v", goals, err)
		}
		items, err := repo.ListTrackedItems(botCtx, id, goals[0].ID)
		if err != nil || len(items) != 1 {
			t.Fatalf("commitments: %+v %v", items, err)
		}
		activity, err := repo.ListActivity(botCtx, id, 0, 50)
		if err != nil || len(activity) != 3 {
			t.Fatalf("activity: %+v %v", activity, err)
		}
		meta, err := sessions.GetSessionMeta(botCtx, b.HomeSessionID.String())
		if err != nil || meta == nil || meta.BotID != id.String() || meta.Retention != store.SessionRetentionBot {
			t.Fatalf("home linkage: %+v %v", meta, err)
		}
		other := restoredOrg.ForTeam("ops").Bots()
		if scope == store.BotScopeTeam {
			other = restoredOrg.ForUser(userID).Bots()
		}
		if _, err := other.Get(botCtx, id); !errors.Is(err, store.ErrBotNotFound) {
			t.Fatalf("restored cross-scope leak: %v", err)
		}
	}
}

func TestRestorePlatformBackupSQLiteMappedOrgAndTeam(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	sourceDSN := "file:" + filepath.Join(sourceDir, "platform.db")
	if err := BootstrapPlatform(ctx, Config{DSN: sourceDSN, DataDir: sourceDir}, nil); err != nil {
		t.Fatalf("BootstrapPlatform(source) error = %v", err)
	}
	source, err := New(ctx, Config{DSN: sourceDSN, DataDir: sourceDir})
	if err != nil {
		t.Fatalf("New(source) error = %v", err)
	}
	defer source.Close()

	userID := uuid.NewString()
	orgID := uuid.NewString()
	if err := source.Users().Create(ctx, &store.User{ID: userID, Email: "mapped@example.com", DisplayName: "Mapped User", PasswordHash: "hash", Status: "active"}); err != nil {
		t.Fatalf("Create user error = %v", err)
	}
	if err := source.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Original Org", Slug: "oldorg", Status: "active"}); err != nil {
		t.Fatalf("Create org error = %v", err)
	}
	if err := source.ProvisionOrg(ctx, orgID, "oldorg"); err != nil {
		t.Fatalf("ProvisionOrg error = %v", err)
	}
	orgStore, err := source.ForOrg("oldorg")
	if err != nil {
		t.Fatalf("ForOrg error = %v", err)
	}
	team := &store.Team{ID: uuid.NewString(), Name: "Ops", Slug: "ops", SchemaName: "ops"}
	if err := orgStore.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatalf("CreateTeam error = %v", err)
	}
	if err := orgStore.ProvisionTeam(ctx, "ops"); err != nil {
		t.Fatalf("ProvisionTeam error = %v", err)
	}
	seedTeamBackupRows(t, filepath.Join(sourceDir, "orgs", "oldorg", "teams", "ops.db"))
	if err := source.Organizations().AddMember(ctx, userID, orgID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := orgStore.Teams().AddMember(ctx, &store.TeamMembership{UserID: userID, TeamID: team.ID, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	botCtx, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: userID, OrgSlug: "oldorg", TeamSlug: "ops", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[store.BotScope]uuid.UUID{}
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		var repo store.BotStore = orgStore.ForTeam("ops").Bots()
		if scope == store.BotScopePersonal {
			repo = orgStore.ForUser(userID).Bots()
		}
		v := store.DefaultBotVersion()
		v.Charter = "oldorg"
		b, err := repo.CreateDraft(botCtx, store.BotCreateRequest{Name: "ops", OwnerID: uuid.MustParse(userID), Version: v, IdempotencyKey: "mapped", Goals: []store.BotGoal{{Title: "oldorg", CompletionCriteria: userID}}})
		if err != nil {
			t.Fatal(err)
		}
		ids[scope] = b.ID
		if err := repo.MarkRead(botCtx, store.BotReadCursor{BotID: b.ID, ViewerID: uuid.MustParse(userID), Sequence: 1}); err != nil {
			t.Fatal(err)
		}
	}
	newUserID := uuid.NewString()

	archivePath := filepath.Join(t.TempDir(), "mapped.astonish-backup")
	if err := source.ExportPlatformBackup(ctx, archivePath, PlatformBackupExportOptions{Backend: "sqlite"}); err != nil {
		t.Fatalf("ExportPlatformBackup() error = %v", err)
	}

	targetDir := t.TempDir()
	targetDSN := "file:" + filepath.Join(targetDir, "platform.db")
	if err := BootstrapPlatform(ctx, Config{DSN: targetDSN, DataDir: targetDir}, nil); err != nil {
		t.Fatalf("BootstrapPlatform(target) error = %v", err)
	}
	target, err := New(ctx, Config{DSN: targetDSN, DataDir: targetDir})
	if err != nil {
		t.Fatalf("New(target) error = %v", err)
	}
	defer target.Close()

	result, err := target.RestorePlatformBackup(ctx, archivePath, PlatformRestoreOptions{
		MapOrg:  map[string]string{"oldorg": "neworg"},
		MapTeam: map[string]string{"oldorg/ops": "neworg/platform"},
		MapUser: map[string]string{"oldorg/" + userID: "neworg/" + newUserID},
	})
	if err != nil {
		t.Fatalf("RestorePlatformBackup(mapped) error = %v", err)
	}
	if result.RestoredRecords == 0 {
		t.Fatal("RestoredRecords = 0, want records")
	}
	assertRestoredTableContains(t, filepath.Join(targetDir, "platform.db"), "organizations", "slug", "neworg")
	assertRestoredTableNotContains(t, filepath.Join(targetDir, "platform.db"), "organizations", "slug", "oldorg")
	assertRestoredTableContains(t, filepath.Join(targetDir, "orgs", "neworg", "org.db"), "teams", "slug", "platform")
	assertRestoredTableContains(t, filepath.Join(targetDir, "orgs", "neworg", "org.db"), "teams", "schema_name", "team_platform")
	assertRestoredTableContains(t, filepath.Join(targetDir, "orgs", "neworg", "teams", "platform.db"), "apps", "slug", "backup-app")
	newCtx, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: newUserID, OrgSlug: "neworg", TeamSlug: "platform", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ods, err := target.ForOrg("neworg")
	if err != nil {
		t.Fatal(err)
	}
	for scope, id := range ids {
		var repo store.BotStore = ods.ForTeam("platform").Bots()
		var sessions store.SessionStore = ods.ForTeam("platform").Sessions()
		if scope == store.BotScopePersonal {
			repo = ods.ForUser(newUserID).Bots()
			sessions = ods.ForUser(newUserID).Sessions()
		}
		b, err := repo.Get(newCtx, id)
		if err != nil {
			t.Fatal(err)
		}
		if b.Name != "ops" || b.OwnerID.String() != newUserID || b.Status != store.BotDraft {
			t.Fatalf("mapped Bot: %+v", b)
		}
		v, err := repo.GetVersion(newCtx, id, 1)
		if err != nil || v.Charter != "oldorg" || v.CreatedBy.String() != userID {
			t.Fatalf("changed reviewed version: %+v %v", v, err)
		}
		goals, err := repo.ListGoals(newCtx, id)
		if err != nil || len(goals) != 1 || goals[0].Title != "oldorg" || goals[0].CompletionCriteria != userID {
			t.Fatalf("changed evidence: %+v %v", goals, err)
		}
		meta, err := sessions.GetSessionMeta(newCtx, b.HomeSessionID.String())
		if err != nil || meta.UserID != newUserID || meta.BotID != id.String() {
			t.Fatalf("mapped home: %+v %v", meta, err)
		}
		if err := repo.MarkRead(newCtx, store.BotReadCursor{BotID: id, ViewerID: uuid.MustParse(newUserID), Sequence: 1}); err != nil {
			t.Fatal(err)
		}
		after, err := repo.Get(newCtx, id)
		if err != nil || after.Revision != b.Revision {
			t.Fatal("mapped read cursor lost")
		}
		if _, err := repo.Get(botCtx, id); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("old principal accepted: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(targetDir, "orgs", "oldorg")); !os.IsNotExist(err) {
		t.Fatalf("old org directory exists after mapped restore, stat error = %v", err)
	}
}

func TestRestorePlatformBackupSQLiteRestoreCredentialPreflightValidKey(t *testing.T) {
	ctx := context.Background()
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	archivePath := createSQLiteBackupWithPersonalCredential(t, ctx)

	targetDir := t.TempDir()
	targetDSN := "file:" + filepath.Join(targetDir, "platform.db")
	if err := BootstrapPlatform(ctx, Config{DSN: targetDSN, DataDir: targetDir}, nil); err != nil {
		t.Fatalf("BootstrapPlatform(target) error = %v", err)
	}
	target, err := New(ctx, Config{DSN: targetDSN, DataDir: targetDir})
	if err != nil {
		t.Fatalf("New(target) error = %v", err)
	}
	defer target.Close()

	plan, err := target.PlanPlatformRestore(ctx, archivePath, PlatformRestoreOptions{DryRun: true})
	if err != nil {
		t.Fatalf("PlanPlatformRestore() error = %v", err)
	}
	if len(plan.Blockers) != 0 {
		t.Fatalf("plan blockers = %v, want none", plan.Blockers)
	}
}

func TestRestorePlatformBackupSQLiteRestoreCredentialPreflightWrongKey(t *testing.T) {
	ctx := context.Background()
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	archivePath := createSQLiteBackupWithPersonalCredential(t, ctx)

	t.Setenv("ASTONISH_MASTER_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	targetDir := t.TempDir()
	targetDSN := "file:" + filepath.Join(targetDir, "platform.db")
	if err := BootstrapPlatform(ctx, Config{DSN: targetDSN, DataDir: targetDir}, nil); err != nil {
		t.Fatalf("BootstrapPlatform(target) error = %v", err)
	}
	target, err := New(ctx, Config{DSN: targetDSN, DataDir: targetDir})
	if err != nil {
		t.Fatalf("New(target) error = %v", err)
	}
	defer target.Close()

	plan, err := target.PlanPlatformRestore(ctx, archivePath, PlatformRestoreOptions{DryRun: true})
	if err != nil {
		t.Fatalf("PlanPlatformRestore() error = %v", err)
	}
	if !restorePlanHasBlocker(plan, "cannot decrypt its credential key") {
		t.Fatalf("plan blockers = %v, want credential key mismatch blocker", plan.Blockers)
	}
	if _, err := target.RestorePlatformBackup(ctx, archivePath, PlatformRestoreOptions{}); err == nil || !strings.Contains(err.Error(), "cannot decrypt its credential key") {
		t.Fatalf("RestorePlatformBackup() error = %v, want credential key blocker", err)
	}
}

func TestRestorePlatformBackupSQLiteRestoreCredentialPreflightMissingKey(t *testing.T) {
	ctx := context.Background()
	t.Setenv("ASTONISH_MASTER_KEY", testMasterKeyHex)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	archivePath := createSQLiteBackupWithPersonalCredential(t, ctx)

	t.Setenv("ASTONISH_MASTER_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	targetDir := t.TempDir()
	targetDSN := "file:" + filepath.Join(targetDir, "platform.db")
	if err := BootstrapPlatform(ctx, Config{DSN: targetDSN, DataDir: targetDir}, nil); err != nil {
		t.Fatalf("BootstrapPlatform(target) error = %v", err)
	}
	target, err := New(ctx, Config{DSN: targetDSN, DataDir: targetDir})
	if err != nil {
		t.Fatalf("New(target) error = %v", err)
	}
	defer target.Close()

	plan, err := target.PlanPlatformRestore(ctx, archivePath, PlatformRestoreOptions{DryRun: true})
	if err != nil {
		t.Fatalf("PlanPlatformRestore() error = %v", err)
	}
	if !restorePlanHasBlocker(plan, "no ASTONISH_MASTER_KEY or ~/.config/astonish/.store_key is configured") {
		t.Fatalf("plan blockers = %v, want missing key blocker", plan.Blockers)
	}
}

func TestLogicalSQLQuotesSQLiteIdentifiers(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "quoted.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite error = %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE "odd "" table" ("select" TEXT, "quote "" col" BLOB)`); err != nil {
		t.Fatalf("create quoted table error = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO "odd "" table" ("select", "quote "" col") VALUES ('value', x'0102')`); err != nil {
		t.Fatalf("insert quoted table error = %v", err)
	}
	data, records, _, err := exportLogicalTable(ctx, db, DialectSQLite, "", `odd " table`, false)
	if err != nil {
		t.Fatalf("exportLogicalTable() error = %v", err)
	}
	if records != 1 {
		t.Fatalf("records = %d, want 1", records)
	}
	if !strings.Contains(string(data), `"select":"value"`) {
		t.Fatalf("exported data = %s, want quoted identifier row", data)
	}
}

func TestRestorePlatformBackupSQLiteResetTarget(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	sourceDSN := "file:" + filepath.Join(sourceDir, "platform.db")
	if err := BootstrapPlatform(ctx, Config{DSN: sourceDSN, DataDir: sourceDir}, nil); err != nil {
		t.Fatalf("BootstrapPlatform(source) error = %v", err)
	}
	source, err := New(ctx, Config{DSN: sourceDSN, DataDir: sourceDir})
	if err != nil {
		t.Fatalf("New(source) error = %v", err)
	}
	defer source.Close()

	userID := uuid.NewString()
	orgID := uuid.NewString()
	if err := source.Users().Create(ctx, &store.User{ID: userID, Email: "reset-restore@example.com", DisplayName: "Reset Restore User", PasswordHash: "hash", Status: "active"}); err != nil {
		t.Fatalf("Create source user error = %v", err)
	}
	if err := source.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Reset Restore Org", Slug: "reset-restore", Status: "active"}); err != nil {
		t.Fatalf("Create source org error = %v", err)
	}
	if err := source.Organizations().AddMember(ctx, userID, orgID, "owner"); err != nil {
		t.Fatalf("AddMember error = %v", err)
	}
	if err := source.ProvisionOrg(ctx, orgID, "reset-restore"); err != nil {
		t.Fatalf("ProvisionOrg error = %v", err)
	}

	archivePath := filepath.Join(t.TempDir(), "reset-restore.astonish-backup")
	if err := source.ExportPlatformBackup(ctx, archivePath, PlatformBackupExportOptions{Backend: "sqlite"}); err != nil {
		t.Fatalf("ExportPlatformBackup() error = %v", err)
	}

	targetDir := t.TempDir()
	targetDSN := "file:" + filepath.Join(targetDir, "platform.db")
	if err := BootstrapPlatform(ctx, Config{DSN: targetDSN, DataDir: targetDir}, nil); err != nil {
		t.Fatalf("BootstrapPlatform(target) error = %v", err)
	}
	target, err := New(ctx, Config{DSN: targetDSN, DataDir: targetDir})
	if err != nil {
		t.Fatalf("New(target) error = %v", err)
	}
	defer target.Close()
	if err := target.Users().Create(ctx, &store.User{ID: uuid.NewString(), Email: "junk@example.com", DisplayName: "Junk User", PasswordHash: "junk", Status: "active"}); err != nil {
		t.Fatalf("Create target junk user error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(targetDir, "orgs", "junk"), 0o750); err != nil {
		t.Fatalf("create target junk org dir error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(targetDir, "fleet_state", "junk"), 0o750); err != nil {
		t.Fatalf("create target junk fleet state dir error = %v", err)
	}

	blockedPlan, err := target.PlanPlatformRestore(ctx, archivePath, PlatformRestoreOptions{})
	if err != nil {
		t.Fatalf("PlanPlatformRestore() error = %v", err)
	}
	if len(blockedPlan.Blockers) == 0 {
		t.Fatal("PlanPlatformRestore() blockers = none, want non-empty target blocker")
	}

	result, err := target.RestorePlatformBackup(ctx, archivePath, PlatformRestoreOptions{ResetTarget: true})
	if err != nil {
		t.Fatalf("RestorePlatformBackup(ResetTarget) error = %v", err)
	}
	if !result.Plan.TargetEmpty {
		t.Fatal("result.Plan.TargetEmpty = false, want true after reset")
	}
	assertRestoredTableContains(t, filepath.Join(targetDir, "platform.db"), "users", "email", "reset-restore@example.com")
	assertRestoredTableNotContains(t, filepath.Join(targetDir, "platform.db"), "users", "email", "junk@example.com")
	if _, err := os.Stat(filepath.Join(targetDir, "orgs", "junk")); !os.IsNotExist(err) {
		t.Fatalf("junk org directory still exists after reset, stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "fleet_state", "junk")); !os.IsNotExist(err) {
		t.Fatalf("junk fleet state directory still exists after reset, stat error = %v", err)
	}
}

func createSQLiteBackupWithPersonalCredential(t *testing.T, ctx context.Context) string {
	t.Helper()
	sourceDir := t.TempDir()
	sourceDSN := "file:" + filepath.Join(sourceDir, "platform.db")
	if err := BootstrapPlatform(ctx, Config{DSN: sourceDSN, DataDir: sourceDir}, nil); err != nil {
		t.Fatalf("BootstrapPlatform(source) error = %v", err)
	}
	source, err := New(ctx, Config{DSN: sourceDSN, DataDir: sourceDir})
	if err != nil {
		t.Fatalf("New(source) error = %v", err)
	}
	defer source.Close()

	userID := uuid.NewString()
	orgID := uuid.NewString()
	if err := source.Users().Create(ctx, &store.User{ID: userID, Email: "cred-restore@example.com", DisplayName: "Cred Restore User", PasswordHash: "hash", Status: "active"}); err != nil {
		t.Fatalf("Create source user error = %v", err)
	}
	if err := source.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Cred Restore Org", Slug: "cred-restore", Status: "active"}); err != nil {
		t.Fatalf("Create source org error = %v", err)
	}
	if err := source.Organizations().AddMember(ctx, userID, orgID, "owner"); err != nil {
		t.Fatalf("AddMember error = %v", err)
	}
	if err := source.ProvisionOrg(ctx, orgID, "cred-restore"); err != nil {
		t.Fatalf("ProvisionOrg error = %v", err)
	}
	orgStore, err := source.ForOrg("cred-restore")
	if err != nil {
		t.Fatalf("ForOrg error = %v", err)
	}
	if err := orgStore.ProvisionPersonalSchema(ctx, userID); err != nil {
		t.Fatalf("ProvisionPersonalSchema error = %v", err)
	}
	personalStore := orgStore.ForUser(userID)
	if err := personalStore.Credentials().Set(ctx, "sap-github-bearer", &store.Credential{Type: store.CredBearer, Token: "restored-token"}); err != nil {
		t.Fatalf("Set personal credential error = %v", err)
	}

	archivePath := filepath.Join(t.TempDir(), "credential-restore.astonish-backup")
	if err := source.ExportPlatformBackup(ctx, archivePath, PlatformBackupExportOptions{Backend: "sqlite"}); err != nil {
		t.Fatalf("ExportPlatformBackup() error = %v", err)
	}
	return archivePath
}

func restorePlanHasBlocker(plan *backup.RestorePlan, want string) bool {
	for _, blocker := range plan.Blockers {
		if strings.Contains(blocker, want) {
			return true
		}
	}
	return false
}

func TestBotRestoreMappingPreservesContent(t *testing.T) {
	oldUser, newUser := uuid.NewString(), uuid.NewString()
	scope := backup.Scope{Kind: "personal", OrgSlug: "oldorg", UserID: oldUser}
	target := backup.Scope{Kind: "personal", OrgSlug: "neworg", UserID: newUser}
	opts := PlatformRestoreOptions{MapOrg: map[string]string{"oldorg": "neworg"}, MapTeam: map[string]string{"oldorg/ops": "neworg/platform"}, MapUser: map[string]string{"oldorg/" + oldUser: "neworg/" + newUser}}
	for _, table := range []string{"bots", "bot_versions", "bot_goals", "bot_tracked_items", "bot_runs", "bot_approvals", "bot_outbox", "bot_templates", "sessions"} {
		t.Run(table, func(t *testing.T) {
			row := map[string]any{"id": "stable-id", "owner_id": oldUser, "user_id": oldUser, "created_by": oldUser, "name": "ops", "title": "oldorg", "bot_id": "bot-id", "definition": map[string]any{"charter": "oldorg", "created_by": oldUser}, "data": map[string]any{"read_cursors": map[string]any{oldUser: "9007199254740993"}, "evidence": oldUser}}
			got := remapRestoreRow(scope, target, table, row, opts)
			if got["name"] != "ops" || got["title"] != "oldorg" || got["definition"].(map[string]any)["charter"] != "oldorg" || got["data"].(map[string]any)["evidence"] != oldUser {
				t.Fatalf("rewrote content: %+v", got)
			}
			if got["created_by"] != oldUser {
				t.Fatal("rewrote historical authorship")
			}
			if table == "bots" && (got["owner_id"] != newUser || got["data"].(map[string]any)["read_cursors"].(map[string]any)[newUser] != "9007199254740993") {
				t.Fatalf("ownership/cursor not mapped: %+v", got)
			}
			if table == "sessions" && got["user_id"] != newUser {
				t.Fatal("session owner not mapped")
			}
		})
	}
	// A mapping for another organization must never move this Bot's owner.
	scope.Kind, scope.UserID, scope.TeamSlug = "team", "", "ops"
	target.Kind, target.UserID, target.TeamSlug = "team", "", "platform"
	opts.MapUser = map[string]string{"otherorg/" + oldUser: "neworg/" + newUser}
	row := remapRestoreRow(scope, target, "bots", map[string]any{"owner_id": oldUser}, opts)
	if row["owner_id"] != oldUser {
		t.Fatal("cross-org owner mapping")
	}
}

func TestBotRestoreApprovalEnvelope(t *testing.T) {
	for _, textJSON := range []bool{false, true} {
		for _, wrapped := range []bool{false, true} {
			for _, state := range []store.ApprovalStatus{store.ApprovalPending, store.ApprovalApproved, store.ApprovalConsumed, store.ApprovalDenied} {
				doc := map[string]any{"status": string(state), "action": map[string]any{"arguments": map[string]any{"amount": json.Number("9007199254740993"), "recipient": "oldorg"}}, "action_digest": "unchanged"}
				var value any = doc
				if wrapped {
					data, err := json.Marshal(doc)
					if err != nil {
						t.Fatal(err)
					}
					value = map[string]any{"bot_document": string(data)}
				}
				if textJSON {
					data, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					value = string(data)
				}
				row := map[string]any{"status": string(state), "data": value}
				if err := quiesceRestoredBotRow("bot_approvals", row); err != nil {
					t.Fatal(err)
				}
				outer := row["data"].(map[string]any)
				if _, ok := outer["bot_document"]; ok != wrapped {
					t.Fatal("changed envelope representation")
				}
				var decoded struct {
					Status store.ApprovalStatus `json:"status"`
					Action struct {
						Arguments struct {
							Amount    int64  `json:"amount"`
							Recipient string `json:"recipient"`
						} `json:"arguments"`
					} `json:"action"`
					Digest string `json:"action_digest"`
				}
				if err := botDecode(outer, &decoded); err != nil {
					t.Fatal(err)
				}
				want := state
				if state == store.ApprovalPending || state == store.ApprovalApproved {
					want = store.ApprovalRevoked
				}
				if decoded.Status != want || decoded.Action.Arguments.Amount != 9007199254740993 || decoded.Action.Arguments.Recipient != "oldorg" || decoded.Digest != "unchanged" {
					t.Fatalf("changed approval: %+v", decoded)
				}
			}
		}
	}
	for _, invalid := range []any{nil, 42, "null", "[]", "broken"} {
		row := map[string]any{"status": "approved", "data": map[string]any{"bot_document": invalid}}
		if err := quiesceRestoredBotRow("bot_approvals", row); err == nil {
			t.Fatalf("accepted malformed envelope %v", invalid)
		}
	}
}

func TestBotRestoreQuiescenceDocuments(t *testing.T) {
	for _, textJSON := range []bool{false, true} {
		for _, state := range []store.BotStatus{store.BotDraft, store.BotActive, store.BotPaused, store.BotStopped, store.BotDeleted} {
			doc := any(map[string]any{"create_hash": "keep", "read_cursors": map[string]any{"viewer": json.Number("9007199254740993")}})
			if textJSON {
				data, _ := json.Marshal(doc)
				doc = string(data)
			}
			row := map[string]any{"status": string(state), "fence": json.Number("9007199254740993"), "revision": json.Number("7"), "data": doc, "active_run_id": "teardown-barrier", "purge_after": "original-deadline"}
			if err := quiesceRestoredBotRow("bots", row); err != nil {
				t.Fatal(err)
			}
			want := state
			if want == store.BotActive {
				want = store.BotPaused
			}
			if row["status"] != string(want) || row["fence"] != int64(9007199254740994) || row["active_run_id"] != "teardown-barrier" || row["purge_after"] != "original-deadline" {
				t.Fatalf("restore lost state: %+v", row)
			}
			data := row["data"].(map[string]any)
			if data["read_cursors"].(map[string]any)["viewer"] != json.Number("9007199254740993") {
				t.Fatal("cursor precision lost")
			}
		}
	}
	for _, delivered := range []any{nil, "2026-01-01T00:00:00Z"} {
		row := map[string]any{"fence": json.Number("2"), "delivered_at": delivered, "available_at": "original-time", "payload": "unchanged", "attempts": 3}
		if err := quiesceRestoredBotRow("bot_outbox", row); err != nil {
			t.Fatal(err)
		}
		if row["delivered_at"] != delivered || row["payload"] != "unchanged" || row["attempts"] != 3 {
			t.Fatal("receipt/evidence changed")
		}
		if delivered != nil && row["available_at"] != "original-time" {
			t.Fatal("delivered receipt rescheduled")
		}
	}
	for _, entity := range []string{"bots", "bot_runs", "bot_approvals", "bot_outbox"} {
		action, reason := restoreActionForEntry(backup.Entry{Entity: entity}, PlatformRestoreOptions{EnableScheduledJobs: true, IncludeTransient: true})
		if action != "restore_quiesced" || reason == "" {
			t.Fatalf("restore plan hides quiescence: %s", entity)
		}
	}
	for _, counter := range []any{nil, -1, "invalid", json.Number("9223372036854775807")} {
		if err := quiesceRestoredBotRow("bots", map[string]any{"fence": counter}); err == nil {
			t.Fatalf("accepted invalid fence %v", counter)
		}
	}
	for _, doc := range []any{nil, "null", "[]", "broken"} {
		if _, err := restoredBotDocument(doc); err == nil {
			t.Fatalf("accepted invalid document %v", doc)
		}
	}
}

func TestBotRestoreMalformedDocumentRollsBackScope(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "restore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE bots (id TEXT PRIMARY KEY, status TEXT, fence INTEGER, revision INTEGER, data TEXT, lease_expires_at TEXT, cleanup_complete BOOLEAN)`); err != nil {
		t.Fatal(err)
	}
	var payload strings.Builder
	writer := backup.NewRecordWriter(&payload, "bots")
	for _, row := range []map[string]any{
		{"id": "valid", "status": "active", "fence": 1, "revision": 1, "data": map[string]any{}},
		{"id": "broken", "status": "active", "fence": 1, "revision": 1, "data": "not-json"},
	} {
		if err := writer.Write(row["id"].(string), row); err != nil {
			t.Fatal(err)
		}
	}
	entry := backup.Entry{Entity: "bots", Path: "bots.jsonl", Kind: "jsonl"}
	err = restoreLogicalEntries(context.Background(), db, DialectSQLite, "", backup.Scope{Kind: "team", OrgSlug: "org", TeamSlug: "team"}, map[string][]byte{entry.Path: []byte(payload.String())}, []backup.Entry{entry}, PlatformRestoreOptions{}, &backup.RestoreResult{})
	if err == nil {
		t.Fatal("malformed archive accepted")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bots`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("partial scope restore committed")
	}
}

// seedBotRecoveryRows models an archive taken while work and external effects
// were outstanding. Raw rows deliberately cover every persisted execution state.
func seedBotRecoveryRows(t *testing.T, path string, id uuid.UUID) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE bots SET status='active', fence=8, active_run_id=?, lease_expires_at=?, cleanup_complete=true WHERE id=?`, uuid.NewString(), time.Now().Add(time.Hour), id.String())
	for _, state := range []store.RunStatus{store.RunQueued, store.RunRunning, store.RunWaitingApproval, store.RunInterrupted, store.RunSucceeded, store.RunPartial, store.RunFailed, store.RunSkipped, store.RunCancelled} {
		run := store.BotRun{ID: uuid.New(), BotID: id, Version: 1, SessionID: uuid.New(), Status: state, Report: store.BotReport{Summary: "Retained evidence"}}
		document, err := botJSONMap(botStoredRun{Run: run})
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO bot_runs (id,bot_id,version,session_id,idempotency_key,request_hash,status,fence,worker_id,lease_expires_at,data) VALUES (?,?,1,?,?,?,?,8,'old-worker',?,?)`, run.ID.String(), id.String(), run.SessionID.String(), string(state), "hash", string(state), time.Now().Add(time.Hour), string(data))
		if state == store.RunRunning {
			for _, status := range []store.ApprovalStatus{store.ApprovalPending, store.ApprovalApproved, store.ApprovalConsumed, store.ApprovalDenied, store.ApprovalExpired, store.ApprovalRevoked} {
				exec(`INSERT INTO bot_approvals (id,bot_id,run_id,version,action_digest,status,data,expires_at) VALUES (?,?,?,1,?,?,?,?)`, uuid.NewString(), id.String(), run.ID.String(), string(status), string(status), `{"status":"`+string(status)+`"}`, time.Now().Add(time.Hour))
			}
		}
	}
	exec(`INSERT INTO scheduled_jobs (id,name,schedule,mode,payload,status,next_run_at) VALUES (?,'bot-job','* * * * *','bot_trigger',?,'active',?)`, uuid.NewString(), `{"bot_id":"`+id.String()+`"}`, time.Now())
}

func assertBotRecoveryRows(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`SELECT COUNT(*) FROM bots WHERE status='active' OR lease_expires_at IS NOT NULL OR cleanup_complete=true`,
		`SELECT COUNT(*) FROM bot_runs WHERE status IN ('queued','running','waiting_approval') OR worker_id!='' OR lease_expires_at IS NOT NULL OR fence!=9`,
		`SELECT COUNT(*) FROM bot_approvals WHERE status IN ('pending','approved')`,
		`SELECT COUNT(*) FROM scheduled_jobs WHERE mode='bot_trigger' AND (status!='paused' OR next_run_at IS NOT NULL)`,
		`SELECT COUNT(*) FROM bot_outbox WHERE delivered_at IS NULL AND (available_at < '9999-01-01' OR last_error='')`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("unsafe restored rows (%d): %s", n, q)
		}
	}
	var interrupted, terminal, approvals int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bot_runs WHERE status='interrupted'`).Scan(&interrupted); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM bot_runs WHERE status IN ('succeeded','partial','failed','skipped','cancelled')`).Scan(&terminal); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM bot_approvals WHERE status='revoked'`).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if interrupted != 4 || terminal != 5 || approvals != 3 {
		t.Fatalf("history states lost: %d %d %d", interrupted, terminal, approvals)
	}
	rows, err := db.Query(`SELECT status,data FROM bot_runs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var status, data string
		if err := rows.Scan(&status, &data); err != nil {
			t.Fatal(err)
		}
		var saved botStoredRun
		var document map[string]any
		if err := json.Unmarshal([]byte(data), &document); err != nil {
			t.Fatal(err)
		}
		if err := botDecode(document, &saved); err != nil {
			t.Fatal(err)
		}
		if string(saved.Run.Status) != status || saved.Run.Lease.WorkerID != "" || saved.Run.Report.Summary != "Retained evidence" {
			t.Fatalf("inconsistent run document: %+v", saved)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func seedSessionBackupRows(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open team db error = %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO sessions (id, title, message_count, metadata, last_seq, created_at, updated_at) VALUES ('backup-session-id', 'backup-session', 1, '{}', 1, datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("seed session failed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO session_events (id, session_id, event_data, created_at) VALUES (137, 'backup-session-id', '{}', datetime('now'))`); err != nil {
		t.Fatalf("seed session event failed: %v", err)
	}
}

func seedScheduledJob(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open team db error = %v", err)
	}
	defer db.Close()
	stmt := `INSERT INTO scheduled_jobs (id, name, schedule, mode, payload, status, last_status, last_error, consecutive_failures, created_at, updated_at) VALUES (?, 'backup-job', '* * * * *', 'routine', '{}', 'active', 'pending', '', 0, datetime('now'), datetime('now'))`
	if _, err := db.Exec(stmt, uuid.NewString()); err != nil {
		t.Fatalf("seed scheduled job failed: %v", err)
	}
}

func assertRestoredTableContains(t *testing.T, dbPath, table, column, want string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open restored db error = %v", err)
	}
	defer db.Close()
	var count int
	query := "SELECT COUNT(*) FROM " + quoteSQLiteIdent(table) + " WHERE " + quoteSQLiteIdent(column) + " = ?"
	if err := db.QueryRow(query, want).Scan(&count); err != nil {
		t.Fatalf("query restored table error = %v", err)
	}
	if count != 1 {
		t.Fatalf("%s.%s=%q count = %d, want 1", table, column, want, count)
	}
}

func assertRestoredTableNotContains(t *testing.T, dbPath, table, column, want string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open restored db error = %v", err)
	}
	defer db.Close()
	var count int
	query := "SELECT COUNT(*) FROM " + quoteSQLiteIdent(table) + " WHERE " + quoteSQLiteIdent(column) + " = ?"
	if err := db.QueryRow(query, want).Scan(&count); err != nil {
		t.Fatalf("query restored table error = %v", err)
	}
	if count != 0 {
		t.Fatalf("%s.%s=%q count = %d, want 0", table, column, want, count)
	}
}

func assertRestoredScheduledJobPaused(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open restored db error = %v", err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRow("SELECT status FROM scheduled_jobs WHERE name = 'backup-job'").Scan(&status); err != nil {
		t.Fatalf("query scheduled job error = %v", err)
	}
	if status != "paused" {
		t.Fatalf("scheduled job status = %q, want paused", status)
	}
}
