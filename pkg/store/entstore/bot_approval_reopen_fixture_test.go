package entstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// ApprovalReopenFixture is test-only: controlled activation is never exported by
// the production store. Reopen closes every DB connection and recreates stores.
func ApprovalReopenFixture(t *testing.T) (context.Context, store.BotID, store.RunID, store.BotHomeBinding, func() (store.BotStore, store.SessionStore)) {
	return ApprovalScopeReopenFixture(t, store.BotScopePersonal)
}

func ApprovalScopeReopenFixture(t *testing.T, scope store.BotScope) (context.Context, store.BotID, store.RunID, store.BotHomeBinding, func() (store.BotStore, store.SessionStore)) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "approval.db")
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	auth := botRepositoryAuth{orgSlug: "org", teamSlug: "team", userID: owner.String(), scope: scope, check: func(context.Context, string) (string, error) { return "member", nil }}
	var client *personalent.Client
	var teamClient *teament.Client
	closeDB := func() {
		if client != nil {
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			client = nil
		}
		if teamClient != nil {
			if err := teamClient.Close(); err != nil {
				t.Fatal(err)
			}
			teamClient = nil
		}
	}
	reopen := func() (store.BotStore, store.SessionStore) {
		closeDB()
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		if scope == store.BotScopeTeam {
			teamClient = teament.NewClient(teament.Driver(entsql.OpenDB(dialect.SQLite, db)))
			if err := teamClient.Schema.Create(ctx); err != nil {
				t.Fatal(err)
			}
			return &teamBotStore{client: teamClient, auth: auth}, &teamSessionStore{client: teamClient}
		}
		client = personalent.NewClient(personalent.Driver(entsql.OpenDB(dialect.SQLite, db)))
		if err := client.Schema.Create(ctx); err != nil {
			t.Fatal(err)
		}
		return &personalBotStore{client: client, auth: auth}, &personalSessionStore{client: client}
	}
	scoped, _ := reopen()
	t.Cleanup(closeDB)
	version := store.DefaultBotVersion()
	version.Charter = "Read once, send exact approved action once, report."
	b, err := scoped.CreateDraft(ctx, store.BotCreateRequest{Name: "Resume", OwnerID: owner, Version: version, IdempotencyKey: "resume"})
	if err != nil {
		t.Fatal(err)
	}
	home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: b.Scope, Backend: "controlled", StorageKey: "opaque", Revision: 1}
	data, err := botJSONMap(home)
	if err != nil {
		t.Fatal(err)
	}
	// Only a test fixture bypasses unavailable production activation/readiness.
	if scope == store.BotScopeTeam {
		err = teamClient.Bot.UpdateOneID(b.ID).SetStatus("active").SetHomeBinding(data).Exec(ctx)
	} else {
		err = client.Bot.UpdateOneID(b.ID).SetStatus("active").SetHomeBinding(data).Exec(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	run, err := scoped.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: 1, Trigger: store.TriggerManual, IdempotencyKey: "run", Instructions: "Read once, then send body exact."})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, b.ID, run.ID, home, reopen
}
