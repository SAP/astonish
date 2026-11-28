package bot

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type healthMemory struct {
	store.MemoryStore
	rows     []store.MemorySearchResult
	sessions []string
	limit    int
}

func (m *healthMemory) ListBySessions(_ context.Context, ids []string, _ string, limit, _ int) ([]store.MemorySearchResult, error) {
	m.sessions = ids
	m.limit = limit
	return m.rows, nil
}
func (m *healthMemory) SearchBySessions(context.Context, []string, string, int, float64, string) ([]store.MemorySearchResult, error) {
	panic("health must not rank")
}
func (r *documentRepository) ListRuns(context.Context, store.BotID, store.BotPage) ([]store.BotRun, error) {
	return nil, nil
}

func TestMemoryHealthSnapshot(t *testing.T) {
	_, repo := documentFixture(store.BotScopePersonal)
	repo.bot.HomeSessionID = uuid.New()
	source := store.MemorySearchResult{ID: uuid.NewString(), SessionID: repo.bot.HomeSessionID.String(), Scope: "personal", CreatedBy: uuid.NewString(), Snippet: "Evidence", Path: "source", CreatedAt: "2026-11-04T15:00:00Z"}
	secret := source
	secret.ID = uuid.NewString()
	secret.Category = "credentials"
	secret.Snippet = "do not project"
	backend := &healthMemory{rows: []store.MemorySearchResult{source, secret}}
	service, _ := store.NewBotMemoryService(repo, backend, repo.bot.ID, repo.bot.Scope)
	health, _ := NewMemoryHealth(service)
	got, err := health.Snapshot(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Records, []store.MemorySearchResult{source}) || got.Fingerprint == "" || got.Truncated {
		t.Fatalf("snapshot %+v", got)
	}
	if len(got.Fingerprints) != 1 || got.Fingerprints[source.ID] != store.MemoryContentFingerprint(source) {
		t.Fatal("missing reviewed CAS mapping")
	}
	if backend.limit != 21 || !reflect.DeepEqual(backend.sessions, []string{source.SessionID}) {
		t.Fatal("missing prefilter")
	}
	again, _ := health.Snapshot(context.Background(), 20)
	if again.Fingerprint != got.Fingerprint {
		t.Fatal("unstable version")
	}
	backend.rows = []store.MemorySearchResult{source, source}
	if _, err = health.Snapshot(context.Background(), 20); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
	backend.rows = []store.MemorySearchResult{source, secret}
	limited, err := health.Snapshot(context.Background(), 1)
	if err != nil || !limited.Truncated {
		t.Fatalf("truncation %+v %v", limited, err)
	}
	backend.rows[0].SessionID = uuid.NewString()
	if _, err = health.Snapshot(context.Background(), 1); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	repo.revoked = true
	if _, err = health.Snapshot(context.Background(), 1); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	if _, err = health.Snapshot(context.Background(), 2001); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatal(err)
	}
}
