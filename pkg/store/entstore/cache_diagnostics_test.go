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
			Call: i, InvocationID: "inv", InputHash: fmt.Sprintf("request-%d", i), CreatedAt: time.Unix(int64(i), 0).UTC(),
		})
		if err != nil {
			t.Fatalf("append diagnostic %d: %v", i, err)
		}
	}

	got, err := ss.ListCacheDiagnostics(ctx, "personal-session")
	if err != nil {
		t.Fatalf("list diagnostics: %v", err)
	}
	if len(got) != store.MaxSessionCacheDiagnostics || got[0].Call != 6 || got[len(got)-1].Call != 105 {
		t.Fatalf("bounded diagnostics = %d calls %d..%d", len(got), got[0].Call, got[len(got)-1].Call)
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
		InvocationID: "invocation-3", Call: 3, CaptureLevel: "canonical-adk",
		InputHash: "request", StablePrefixElements: 7, StablePrefixBytes: 512,
		Payload: []byte(`{"model":"test"}`), Usage: store.CacheDiagnosticUsage{Reported: true, CachedTokens: 42},
	}
	if err := ss.AppendCacheDiagnostic(ctx, "team-session", want); err != nil {
		t.Fatalf("append diagnostic: %v", err)
	}
	got, err := ss.ListCacheDiagnostics(ctx, "team-session")
	if err != nil || len(got) != 1 {
		t.Fatalf("list diagnostics = %#v, err=%v", got, err)
	}
	if got[0].Call != want.Call || got[0].InvocationID != want.InvocationID || got[0].InputHash != want.InputHash || got[0].Usage.CachedTokens != want.Usage.CachedTokens {
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
