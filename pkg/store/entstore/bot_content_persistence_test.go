package entstore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotContentPersistenceVersionGoal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}
	_, backend, err := NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	org := &store.Organization{ID: uuid.NewString(), Slug: "content-org", Name: "Content", Status: "active"}
	if err := backend.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := backend.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := backend.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "content-team", Name: "Content", SchemaName: "team_content_team"}
	if err := ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner, viewer := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, viewer} {
		if err := backend.Users().Create(ctx, &store.User{ID: id.String(), Email: id.String() + "@example.test", DisplayName: "Test", Status: "active"}); err != nil {
			t.Fatal(err)
		}
		if err := backend.Organizations().AddMember(ctx, id.String(), org.ID, "member"); err != nil {
			t.Fatal(err)
		}
		if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: id.String(), TeamID: team.ID, Role: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	principal := func(id uuid.UUID, selectedTeam string) context.Context {
		c, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: id.String(), OrgSlug: org.Slug, TeamSlug: selectedTeam, Authenticated: true})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	ownerCtx := principal(owner, team.Slug)
	sentinel := "DISK_ONLY_SENTINEL_\"quoted\"_\\_雪"
	type savedContent struct {
		bot     *store.Bot
		version *store.BotVersion
		goal    *store.BotGoal
	}
	saved := map[store.BotScope]savedContent{}
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		var bots store.BotStore
		var raw *sql.DB
		if scope == store.BotScopeTeam {
			ds := ods.ForTeam(team.Slug).(*teamDataStore)
			bots, raw = ds.Bots(), ds.db
		} else {
			ds := ods.ForUser(owner.String()).(*personalDataStore)
			bots, raw = ds.Bots(), ds.db
		}
		version := store.DefaultBotVersion()
		version.Charter = sentinel + " charter"
		version.Persona.Soul = sentinel + " soul"
		version.Persona.Identity = sentinel + " identity"
		version.Persona.User = sentinel + " user"
		version.Delivery.Preferences = sentinel + " preferences"
		version.ChangeReason = sentinel + " change"
		b, err := bots.Create(ownerCtx, store.BotCreateRequest{Name: "Disk content", OwnerID: owner, Version: version, IdempotencyKey: "disk-content"})
		if err != nil {
			t.Fatal(scope, err)
		}
		version.BotID = b.ID
		version.Charter += " updated"
		b, err = bots.ApplyVersion(ownerCtx, store.BotVersionRequest{BotID: b.ID, ExpectedRevision: b.Revision, Version: version})
		if err != nil {
			t.Fatal(scope, err)
		}
		goal, err := bots.SaveGoal(ownerCtx, store.BotGoalUpdate{BotID: b.ID, Goal: store.BotGoal{BotID: b.ID, Title: "Goal", Description: sentinel + " description", CompletionCriteria: sentinel + " criteria"}})
		if err != nil {
			t.Fatal(scope, err)
		}
		goal.Description += " updated"
		goal, err = bots.SaveGoal(ownerCtx, store.BotGoalUpdate{BotID: b.ID, ExpectedRevision: goal.Revision, Goal: *goal})
		if err != nil {
			t.Fatal(scope, err)
		}
		got, err := bots.GetVersion(ownerCtx, b.ID, b.Version)
		if err != nil || got.Charter != version.Charter || got.ChangeReason != version.ChangeReason {
			t.Fatalf("%s version hydration: %+v %v", scope, got, err)
		}
		goals, err := bots.ListGoals(ownerCtx, b.ID)
		if err != nil || len(goals) != 1 || !reflect.DeepEqual(goals[0], *goal) {
			t.Fatalf("%s goal hydration: %+v %v", scope, goals, err)
		}
		assertBotContentSQLAbsent(t, raw, sentinel)
		saved[scope] = savedContent{b, got, goal}
	}
	// Closing/reopening the actual router proves refs survive adapter reconstruction.
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened, err := NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	ods, err = reopened.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	for scope, want := range saved {
		var bots store.BotStore
		var readCtx context.Context
		if scope == store.BotScopeTeam {
			bots = ods.ForTeam(team.Slug).Bots()
			readCtx = principal(viewer, team.Slug) // Scope must use persisted owner, not viewer.
		} else {
			bots = ods.ForUser(owner.String()).Bots()
			readCtx = principal(owner, "different-team") // Personal refs cannot depend on selected team.
		}
		got, err := bots.GetVersion(readCtx, want.bot.ID, want.bot.Version)
		if err != nil || !reflect.DeepEqual(got, want.version) {
			t.Fatalf("%s reopened version: %+v %v", scope, got, err)
		}
		goals, err := bots.ListGoals(readCtx, want.bot.ID)
		if err != nil || len(goals) != 1 || !reflect.DeepEqual(goals[0], *want.goal) {
			t.Fatalf("%s reopened goals: %+v %v", scope, goals, err)
		}
	}
}

func assertBotContentSQLAbsent(t *testing.T, db *sql.DB, sentinel string) {
	t.Helper()
	encoded, err := json.Marshal(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	markers := []string{sentinel, string(encoded[1 : len(encoded)-1]), base64.StdEncoding.EncodeToString([]byte(sentinel)), "DISK_ONLY_SENTINEL"}
	for table, query := range map[string]string{"bot_versions": "SELECT * FROM bot_versions", "bot_goals": "SELECT * FROM bot_goals"} {
		rows, err := db.QueryContext(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		count := 0
		for rows.Next() {
			count++
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for i, value := range values {
				var text string
				switch v := value.(type) {
				case string:
					text = v
				case []byte:
					text = string(v)
				}
				for _, marker := range markers {
					if strings.Contains(text, marker) {
						rows.Close()
						t.Fatalf("content leaked to %s.%s", table, columns[i])
					}
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		if count == 0 {
			t.Fatalf("no rows inspected in %s", table)
		}
	}
}

func TestBotContentPersistenceRollbackAndMissing(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, _ := botRunFixture(t, scope)
			version, err := repo.db.ReadVersion(ctx, run.BotID, 1)
			if err != nil {
				t.Fatal(err)
			}
			version.Version = 2
			injected := errors.New("rollback after staging")
			err = repo.db.Transaction(ctx, func(tx botDB) error {
				if err := tx.InsertVersion(ctx, *version, "staged-hash"); err != nil {
					return err
				}
				if _, err := tx.ReadVersion(ctx, run.BotID, 2); err != nil {
					return err
				}
				return injected
			})
			if !errors.Is(err, injected) {
				t.Fatal(err)
			}
			if _, err := repo.db.ReadVersion(ctx, run.BotID, 2); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("rolled-back version visible: %v", err)
			}
			var codec *botContentCodec
			var data map[string]any
			switch d := repo.db.(type) {
			case teamBotDB:
				row, err := d.client.BotVersion.Query().Only(ctx)
				if err != nil {
					t.Fatal(err)
				}
				data = row.Definition
				codec = d.contentCodec()
			case personalBotDB:
				row, err := d.client.BotVersion.Query().Only(ctx)
				if err != nil {
					t.Fatal(err)
				}
				data = row.Definition
				codec = d.contentCodec()
			}
			var record botVersionRecord
			if err := decodeBotRecord(data, &record); err != nil {
				t.Fatal(err)
			}
			disk, err := codec.Open(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			if err := disk.Remove(ctx, record.Content, func(commit func() error) error { return commit() }); err != nil {
				t.Fatal(err)
			}
			if err := disk.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.db.ReadVersion(ctx, run.BotID, 1); err == nil {
				t.Fatal("missing content silently accepted")
			}
			if _, err := repo.Get(ctx, run.BotID); err != nil {
				t.Fatalf("metadata lookup needs body: %v", err)
			}
		})
	}
}

func TestBotContentPersistenceRequiresExplicitCodec(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, _ := botRunFixture(t, scope)
			var unconfigured botDB
			switch d := repo.db.(type) {
			case teamBotDB:
				d.content = nil
				unconfigured = d
			case personalBotDB:
				d.content = nil
				unconfigured = d
			}
			if _, err := unconfigured.ReadVersion(ctx, run.BotID, 1); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("nil codec read: %v", err)
			}
			v := store.DefaultBotVersion()
			v.BotID = run.BotID
			v.Version = 2
			if err := unconfigured.InsertVersion(ctx, v, "hash"); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("nil codec write: %v", err)
			}
		})
	}
}
