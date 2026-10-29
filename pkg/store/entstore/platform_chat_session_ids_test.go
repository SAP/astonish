package entstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/store"
)

func TestAllChatSessionIDsAcrossSQLiteTeams(t *testing.T) {
	ctx := context.Background()
	s := newSQLitePlatformStoreForSessionIDs(t, ctx)

	orgID := uuid.NewString()
	if err := s.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Cleanup", Slug: "cleanup", Status: "active"}); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	if err := s.ProvisionOrg(ctx, orgID, "cleanup"); err != nil {
		t.Fatalf("provision organization: %v", err)
	}
	orgStore, err := s.ForOrg("cleanup")
	if err != nil {
		t.Fatalf("open organization: %v", err)
	}

	for _, slug := range []string{"alpha", "beta"} {
		team := &store.Team{ID: uuid.NewString(), Name: slug, Slug: slug, SchemaName: teamSchemaName(slug)}
		if err := orgStore.Teams().CreateTeam(ctx, team); err != nil {
			t.Fatalf("create team %s: %v", slug, err)
		}
		if err := orgStore.ProvisionTeam(ctx, slug); err != nil {
			t.Fatalf("provision team %s: %v", slug, err)
		}
	}

	for _, tc := range []struct {
		team string
		ids  []string
	}{
		{team: "alpha", ids: []string{"alpha-only", "shared"}},
		{team: "beta", ids: []string{"beta-only", "shared"}},
	} {
		for _, id := range tc.ids {
			if err := orgStore.ForTeam(tc.team).Sessions().AddSessionMeta(ctx, store.SessionMeta{ID: id, AppName: "astonish"}); err != nil {
				t.Fatalf("add session %s to %s: %v", id, tc.team, err)
			}
		}
	}

	ids, err := s.AllChatSessionIDs(ctx)
	if err != nil {
		t.Fatalf("AllChatSessionIDs() error = %v", err)
	}
	for _, id := range []string{"alpha-only", "beta-only", "shared"} {
		if !ids[id] {
			t.Errorf("AllChatSessionIDs() missing %q: %v", id, ids)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("AllChatSessionIDs() returned %d IDs, want 3: %v", len(ids), ids)
	}
}

func TestAllChatSessionIDsEmptySQLitePlatform(t *testing.T) {
	ctx := context.Background()
	s := newSQLitePlatformStoreForSessionIDs(t, ctx)

	ids, err := s.AllChatSessionIDs(ctx)
	if err != nil {
		t.Fatalf("AllChatSessionIDs() error = %v", err)
	}
	if ids == nil {
		t.Fatal("AllChatSessionIDs() returned nil snapshot, want authoritative empty map")
	}
	if len(ids) != 0 {
		t.Fatalf("AllChatSessionIDs() = %v, want empty", ids)
	}
}

func TestAllChatSessionIDsRejectsUnreadableTeam(t *testing.T) {
	ctx := context.Background()
	s := newSQLitePlatformStoreForSessionIDs(t, ctx)

	orgID := uuid.NewString()
	if err := s.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Partial", Slug: "partial", Status: "active"}); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	if err := s.ProvisionOrg(ctx, orgID, "partial"); err != nil {
		t.Fatalf("provision organization: %v", err)
	}
	orgStore, err := s.ForOrg("partial")
	if err != nil {
		t.Fatalf("open organization: %v", err)
	}
	for _, slug := range []string{"healthy", "broken"} {
		if err := orgStore.Teams().CreateTeam(ctx, &store.Team{ID: uuid.NewString(), Name: slug, Slug: slug, SchemaName: teamSchemaName(slug)}); err != nil {
			t.Fatalf("create team %s: %v", slug, err)
		}
		if err := orgStore.ProvisionTeam(ctx, slug); err != nil {
			t.Fatalf("provision team %s: %v", slug, err)
		}
	}
	if err := orgStore.ForTeam("healthy").Sessions().AddSessionMeta(ctx, store.SessionMeta{ID: "must-not-be-partial", AppName: "astonish"}); err != nil {
		t.Fatalf("add healthy session: %v", err)
	}
	brokenTeam, ok := orgStore.ForTeam("broken").(*teamDataStore)
	if !ok {
		t.Fatalf("broken team store type = %T", orgStore.ForTeam("broken"))
	}
	if _, err := brokenTeam.db.ExecContext(ctx, "DROP TABLE sessions"); err != nil {
		t.Fatalf("drop broken team sessions table: %v", err)
	}

	ids, err := s.AllChatSessionIDs(ctx)
	if err == nil {
		t.Fatalf("AllChatSessionIDs() = %v, nil error; want unreadable team error", ids)
	}
	if ids != nil {
		t.Fatalf("AllChatSessionIDs() returned partial snapshot %v on error", ids)
	}
}

// TestAllChatSessionIDsIncludesPersonalSessions proves the authoritative
// snapshot enumerates personal (per-user) chat sessions, not only team
// sessions. Regular Studio chat in platform mode stores sessions in the
// personal store (chat_handlers.go: "regular chat uses personal sessions"),
// so a snapshot that omits them would classify every live Studio session as
// an orphan and the reaper would destroy its sandbox — the exact data-loss
// incident reported for session 81e8352a.
func TestAllChatSessionIDsIncludesPersonalSessions(t *testing.T) {
	ctx := context.Background()
	s := newSQLitePlatformStoreForSessionIDs(t, ctx)

	orgID := uuid.NewString()
	if err := s.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Personal", Slug: "personal", Status: "active"}); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	if err := s.ProvisionOrg(ctx, orgID, "personal"); err != nil {
		t.Fatalf("provision organization: %v", err)
	}
	orgStore, err := s.ForOrg("personal")
	if err != nil {
		t.Fatalf("open organization: %v", err)
	}

	// A provisioned team session (existing path) plus a personal session for a
	// real org member (the path the reaper was missing).
	team := &store.Team{ID: uuid.NewString(), Name: "shared", Slug: "shared", SchemaName: teamSchemaName("shared")}
	if err := orgStore.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatalf("create team: %v", err)
	}
	if err := orgStore.ProvisionTeam(ctx, "shared"); err != nil {
		t.Fatalf("provision team: %v", err)
	}
	if err := orgStore.ForTeam("shared").Sessions().AddSessionMeta(ctx, store.SessionMeta{ID: "fleet-team-session", AppName: "astonish"}); err != nil {
		t.Fatalf("add team session: %v", err)
	}

	userID := uuid.NewString()
	if err := s.Users().Create(ctx, &store.User{ID: userID, Email: "owner@example.com", DisplayName: "Owner", PasswordHash: "hash", Status: "active"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.Organizations().AddMember(ctx, userID, orgID, "owner"); err != nil {
		t.Fatalf("add member: %v", err)
	}
	personal := orgStore.ForUser(userID)
	if personal == nil {
		t.Fatal("ForUser returned nil personal store")
	}
	if err := personal.Sessions().AddSessionMeta(ctx, store.SessionMeta{ID: "live-studio-session", AppName: "astonish"}); err != nil {
		t.Fatalf("add personal session: %v", err)
	}

	ids, err := s.AllChatSessionIDs(ctx)
	if err != nil {
		t.Fatalf("AllChatSessionIDs() error = %v", err)
	}
	if !ids["live-studio-session"] {
		t.Errorf("AllChatSessionIDs() missing personal chat session %q: %v", "live-studio-session", ids)
	}
	if !ids["fleet-team-session"] {
		t.Errorf("AllChatSessionIDs() missing team session %q: %v", "fleet-team-session", ids)
	}
}

func newSQLitePlatformStoreForSessionIDs(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	dataDir := t.TempDir()
	cfg := Config{DSN: "file:" + filepath.Join(dataDir, "platform.db"), DataDir: dataDir}
	if err := BootstrapPlatform(ctx, cfg, nil); err != nil {
		t.Fatalf("BootstrapPlatform() error = %v", err)
	}
	s, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
