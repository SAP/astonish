package entstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	personalent "github.com/SAP/astonish/ent/personal"
	personalcachediagnostic "github.com/SAP/astonish/ent/personal/cachediagnostic"
	teament "github.com/SAP/astonish/ent/team"
	teamcachediagnostic "github.com/SAP/astonish/ent/team/cachediagnostic"
	"github.com/SAP/astonish/pkg/store"
	adksession "google.golang.org/adk/session"
)

func TestPersonalSessionCacheDiagnosticsBoundedAndCascade(t *testing.T) {
	ctx := context.Background()
	client := newPersonalClient(t)
	ss := &personalSessionStore{client: client}
	seedPersonalDiagnosticSession(t, client, "personal-session")

	for i := 1; i <= store.MaxSessionCacheDiagnostics+5; i++ {
		err := ss.AppendCacheDiagnostic(ctx, "personal-session", store.CacheDiagnostic{
			Round: i, SystemHash: fmt.Sprintf("system-%d", i), ToolHash: "tools", CreatedAt: time.Unix(int64(i), 0).UTC(),
		})
		if err != nil {
			t.Fatalf("append diagnostic %d: %v", i, err)
		}
	}

	got, err := ss.ListCacheDiagnostics(ctx, "personal-session")
	if err != nil {
		t.Fatalf("list diagnostics: %v", err)
	}
	if len(got) != store.MaxSessionCacheDiagnostics || got[0].Round != 6 || got[len(got)-1].Round != 105 {
		t.Fatalf("bounded diagnostics = %d rounds %d..%d", len(got), got[0].Round, got[len(got)-1].Round)
	}
	if err := ss.Delete(ctx, &adksession.DeleteRequest{SessionID: "personal-session"}); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	count, err := client.CacheDiagnostic.Query().Where(personalcachediagnostic.SessionIDEQ("personal-session")).Count(ctx)
	if err != nil || count != 0 {
		t.Fatalf("diagnostics after cascade = %d, err=%v", count, err)
	}
}

func TestTeamSessionCacheDiagnosticsRoundTripAndCascade(t *testing.T) {
	ctx := context.Background()
	client := newTeamClient(t)
	ss := &teamSessionStore{client: client}
	seedTeamDiagnosticSession(t, client, "team-session")

	want := store.CacheDiagnostic{
		Round: 3, CacheStablePath: true, SystemHash: "system", SystemChanged: true,
		SystemChangedSession: true, ToolHash: "tools", ToolCount: 7,
		ToolsChanged: true, ToolsChangedSession: true,
	}
	if err := ss.AppendCacheDiagnostic(ctx, "team-session", want); err != nil {
		t.Fatalf("append diagnostic: %v", err)
	}
	got, err := ss.ListCacheDiagnostics(ctx, "team-session")
	if err != nil || len(got) != 1 {
		t.Fatalf("list diagnostics = %#v, err=%v", got, err)
	}
	if got[0].Round != want.Round || got[0].SystemHash != want.SystemHash || got[0].ToolHash != want.ToolHash || !got[0].ToolsChangedSession {
		t.Fatalf("diagnostic = %#v, want %#v", got[0], want)
	}
	if err := ss.RemoveSessionMeta(ctx, "team-session"); err != nil {
		t.Fatalf("remove session: %v", err)
	}
	count, err := client.CacheDiagnostic.Query().Where(teamcachediagnostic.SessionIDEQ("team-session")).Count(ctx)
	if err != nil || count != 0 {
		t.Fatalf("diagnostics after cascade = %d, err=%v", count, err)
	}
}

func seedPersonalDiagnosticSession(t *testing.T, client *personalent.Client, id string) {
	t.Helper()
	if _, err := client.Session.Create().SetID(id).Save(context.Background()); err != nil {
		t.Fatalf("seed personal session: %v", err)
	}
}

func seedTeamDiagnosticSession(t *testing.T, client *teament.Client, id string) {
	t.Helper()
	if _, err := client.Session.Create().SetID(id).Save(context.Background()); err != nil {
		t.Fatalf("seed team session: %v", err)
	}
}
