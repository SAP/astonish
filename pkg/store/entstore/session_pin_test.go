package entstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	teament "github.com/SAP/astonish/ent/team"

	_ "modernc.org/sqlite"
)

func newTeamClient(t *testing.T) *teament.Client {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "team.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// Match the production SQLite router: concurrent Bot work and lease
	// renewal queue on one connection instead of failing with SQLITE_BUSY.
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatalf("pragma: %v", err)
		}
	}

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := teament.NewClient(teament.Driver(drv))
	t.Cleanup(func() { client.Close() })

	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("schema create: %v", err)
	}
	return client
}

func TestSessionPin_Personal_RoundTrip(t *testing.T) {
	// Given a personal store with a seeded session
	client := newPersonalClient(t)
	pds := &personalDataStore{client: client, userID: ""}
	ctx := context.Background()

	sessionID := "sess-personal-001"
	now := time.Now().UTC()
	if _, err := client.Session.Create().
		SetID(sessionID).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		Save(ctx); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// When: initial read returns empty (unpinned)
	got, err := pds.SessionPin(ctx, sessionID)
	if err != nil {
		t.Fatalf("initial SessionPin: %v", err)
	}
	if got.Provider != "" || got.Model != "" {
		t.Errorf("initial pin = %+v, want zero-value", got)
	}

	// When: SetSessionPin with values
	if err := pds.SetSessionPin(ctx, sessionID, "anthropic", "claude-sonnet-4.5"); err != nil {
		t.Fatalf("SetSessionPin: %v", err)
	}

	// Then: read returns the pin
	got, err = pds.SessionPin(ctx, sessionID)
	if err != nil {
		t.Fatalf("SessionPin after set: %v", err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-sonnet-4.5" {
		t.Errorf("after set = %+v, want {anthropic, claude-sonnet-4.5}", got)
	}

	// When: clear with empty strings
	if err := pds.SetSessionPin(ctx, sessionID, "", ""); err != nil {
		t.Fatalf("clear SetSessionPin: %v", err)
	}

	// Then: read returns empty again
	got, err = pds.SessionPin(ctx, sessionID)
	if err != nil {
		t.Fatalf("SessionPin after clear: %v", err)
	}
	if got.Provider != "" || got.Model != "" {
		t.Errorf("after clear = %+v, want zero-value", got)
	}
}

func TestSessionPin_Personal_NotFound(t *testing.T) {
	client := newPersonalClient(t)
	pds := &personalDataStore{client: client, userID: ""}
	ctx := context.Background()

	// Get on missing session
	if _, err := pds.SessionPin(ctx, "does-not-exist"); err == nil {
		t.Error("SessionPin on missing session: want error, got nil")
	}

	// Set on missing session
	if err := pds.SetSessionPin(ctx, "does-not-exist", "openai", "gpt-5"); err == nil {
		t.Error("SetSessionPin on missing session: want error, got nil")
	}
}

func TestSessionPin_Team_RoundTrip(t *testing.T) {
	client := newTeamClient(t)
	tds := &teamDataStore{client: client}
	ctx := context.Background()

	sessionID := "sess-team-001"
	now := time.Now().UTC()
	if _, err := client.Session.Create().
		SetID(sessionID).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		Save(ctx); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Initial: empty
	got, err := tds.SessionPin(ctx, sessionID)
	if err != nil {
		t.Fatalf("initial SessionPin: %v", err)
	}
	if got.Provider != "" || got.Model != "" {
		t.Errorf("initial pin = %+v, want zero-value", got)
	}

	// Set
	if err := tds.SetSessionPin(ctx, sessionID, "openai", "gpt-5"); err != nil {
		t.Fatalf("SetSessionPin: %v", err)
	}
	got, err = tds.SessionPin(ctx, sessionID)
	if err != nil {
		t.Fatalf("SessionPin after set: %v", err)
	}
	if got.Provider != "openai" || got.Model != "gpt-5" {
		t.Errorf("after set = %+v, want {openai, gpt-5}", got)
	}

	// Clear
	if err := tds.SetSessionPin(ctx, sessionID, "", ""); err != nil {
		t.Fatalf("clear SetSessionPin: %v", err)
	}
	got, err = tds.SessionPin(ctx, sessionID)
	if err != nil {
		t.Fatalf("SessionPin after clear: %v", err)
	}
	if got.Provider != "" || got.Model != "" {
		t.Errorf("after clear = %+v, want zero-value", got)
	}
}

func TestSessionPin_Team_NotFound(t *testing.T) {
	client := newTeamClient(t)
	tds := &teamDataStore{client: client}
	ctx := context.Background()

	if _, err := tds.SessionPin(ctx, "missing"); err == nil {
		t.Error("SessionPin missing: want error")
	}
	if err := tds.SetSessionPin(ctx, "missing", "x", "y"); err == nil {
		t.Error("SetSessionPin missing: want error")
	}
}

func TestAppPin_Personal_RoundTrip(t *testing.T) {
	client := newPersonalClient(t)
	pds := &personalDataStore{client: client, userID: ""}
	ctx := context.Background()

	// Seed a session first (App has SessionID FK)
	sessionID := "sess-for-app-p"
	now := time.Now().UTC()
	if _, err := client.Session.Create().
		SetID(sessionID).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		Save(ctx); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	slug := "my-app"
	if _, err := client.App.Create().
		SetSlug(slug).
		SetName("My App").
		SetDescription("d").
		SetCode("code").
		SetSessionID(sessionID).
		Save(ctx); err != nil {
		t.Fatalf("seed app: %v", err)
	}

	// Initial: empty
	got, err := pds.AppPin(ctx, slug)
	if err != nil {
		t.Fatalf("initial AppPin: %v", err)
	}
	if got.Provider != "" || got.Model != "" {
		t.Errorf("initial = %+v, want zero-value", got)
	}

	// Set
	if err := pds.SetAppPin(ctx, slug, "gemini", "gemini-2.5-pro"); err != nil {
		t.Fatalf("SetAppPin: %v", err)
	}
	got, err = pds.AppPin(ctx, slug)
	if err != nil {
		t.Fatalf("AppPin after set: %v", err)
	}
	if got.Provider != "gemini" || got.Model != "gemini-2.5-pro" {
		t.Errorf("after set = %+v, want {gemini, gemini-2.5-pro}", got)
	}

	// Clear
	if err := pds.SetAppPin(ctx, slug, "", ""); err != nil {
		t.Fatalf("clear SetAppPin: %v", err)
	}
	got, err = pds.AppPin(ctx, slug)
	if err != nil {
		t.Fatalf("AppPin after clear: %v", err)
	}
	if got.Provider != "" || got.Model != "" {
		t.Errorf("after clear = %+v, want zero-value", got)
	}
}

func TestAppPin_Personal_NotFound(t *testing.T) {
	client := newPersonalClient(t)
	pds := &personalDataStore{client: client, userID: ""}
	ctx := context.Background()

	if _, err := pds.AppPin(ctx, "no-such-app"); err == nil {
		t.Error("AppPin missing: want error")
	}
	if err := pds.SetAppPin(ctx, "no-such-app", "x", "y"); err == nil {
		t.Error("SetAppPin missing: want error")
	}
}

func TestAppPin_Team_RoundTrip(t *testing.T) {
	client := newTeamClient(t)
	tds := &teamDataStore{client: client}
	ctx := context.Background()

	sessionID := "sess-for-app-t"
	now := time.Now().UTC()
	if _, err := client.Session.Create().
		SetID(sessionID).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		Save(ctx); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	slug := "team-app"
	if _, err := client.App.Create().
		SetSlug(slug).
		SetName("Team App").
		SetDescription("d").
		SetCode("code").
		SetSessionID(sessionID).
		Save(ctx); err != nil {
		t.Fatalf("seed app: %v", err)
	}

	// Set & read
	if err := tds.SetAppPin(ctx, slug, "openai", "gpt-5"); err != nil {
		t.Fatalf("SetAppPin: %v", err)
	}
	got, err := tds.AppPin(ctx, slug)
	if err != nil {
		t.Fatalf("AppPin: %v", err)
	}
	if got.Provider != "openai" || got.Model != "gpt-5" {
		t.Errorf("after set = %+v, want {openai, gpt-5}", got)
	}

	// Clear
	if err := tds.SetAppPin(ctx, slug, "", ""); err != nil {
		t.Fatalf("clear SetAppPin: %v", err)
	}
	got, err = tds.AppPin(ctx, slug)
	if err != nil {
		t.Fatalf("AppPin after clear: %v", err)
	}
	if got.Provider != "" || got.Model != "" {
		t.Errorf("after clear = %+v, want zero-value", got)
	}
}

func TestAppPin_Team_NotFound(t *testing.T) {
	client := newTeamClient(t)
	tds := &teamDataStore{client: client}
	ctx := context.Background()

	if _, err := tds.AppPin(ctx, "missing"); err == nil {
		t.Error("AppPin missing: want error")
	}
	if err := tds.SetAppPin(ctx, "missing", "x", "y"); err == nil {
		t.Error("SetAppPin missing: want error")
	}
}
