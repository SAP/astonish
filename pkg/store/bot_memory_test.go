package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

type botMemoryBots struct {
	BotStore
	bot     Bot
	runs    []BotRun
	revoked bool
}

func (b *botMemoryBots) Get(context.Context, BotID) (*Bot, error) {
	if b.revoked {
		return nil, ErrBotForbidden
	}
	return &b.bot, nil
}
func (b *botMemoryBots) ListRuns(_ context.Context, _ BotID, p BotPage) ([]BotRun, error) {
	start := 0
	if p.After != "" {
		for i, r := range b.runs {
			if r.ID.String() == p.After {
				start = i + 1
				break
			}
		}
	}
	end := start + p.Limit
	if end > len(b.runs) {
		end = len(b.runs)
	}
	return b.runs[start:end], nil
}

type botMemoryWriter struct {
	*botMemoryBots
	actor  uuid.UUID
	denied bool
}

func (b *botMemoryWriter) MutateBotMemory(ctx context.Context, m MemoryStore, _ BotID, q BotMemoryMutation) error {
	switch q.Kind {
	case BotMemoryAdd:
		return m.Add(ctx, q.Entry)
	case BotMemoryUpdate:
		return m.Update(ctx, q.Previous.ID, q.Entry.Content, q.Entry.Category)
	case BotMemoryDelete:
		return m.Delete(ctx, q.Previous.ID)
	default:
		return ErrBotInvalid
	}
}

func (b *botMemoryWriter) AuthorizeBotMemoryWrite(context.Context, BotID) (uuid.UUID, error) {
	if b.denied {
		return uuid.Nil, ErrBotForbidden
	}
	return b.actor, nil
}

type botMemoryBackend struct {
	MemoryStore
	rows             []MemorySearchResult
	added            *MemoryEntry
	updates, deletes int
}

func (m *botMemoryBackend) Get(_ context.Context, id string) (*MemorySearchResult, error) {
	for _, r := range m.rows {
		if r.ID == id {
			return &r, nil
		}
	}
	return nil, nil
}
func (m *botMemoryBackend) ListBySession(_ context.Context, id string) ([]MemorySearchResult, error) {
	var rows []MemorySearchResult
	for _, r := range m.rows {
		if r.SessionID == id {
			rows = append(rows, r)
		}
	}
	return rows, nil
}
func (m *botMemoryBackend) Add(_ context.Context, e MemoryEntry) error { m.added = &e; return nil }
func (m *botMemoryBackend) Update(context.Context, string, string, string) error {
	m.updates++
	return nil
}
func (m *botMemoryBackend) Delete(context.Context, string) error { m.deletes++; return nil }

type botMemoryPrefilter struct {
	*botMemoryBackend
	leak     bool
	received []string
}

func (m *botMemoryPrefilter) SearchBySessions(_ context.Context, ids []string, _ string, n int, _ float64, category string) ([]MemorySearchResult, error) {
	m.received = ids
	if m.leak {
		return m.rows, nil
	}
	allowed := map[string]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	rows := []MemorySearchResult{}
	for _, r := range m.rows {
		if allowed[r.SessionID] && (category == "" || category == r.Category) {
			rows = append(rows, r)
			if len(rows) == n {
				break
			}
		}
	}
	return rows, nil
}
func (m *botMemoryPrefilter) ListBySessions(ctx context.Context, ids []string, category string, limit, offset int) ([]MemorySearchResult, error) {
	rows, err := m.SearchBySessions(ctx, ids, "", limit+offset, 0, category)
	if offset >= len(rows) {
		return nil, err
	}
	return rows[offset:], err
}

type botMemoryAuthorizerOnly struct {
	BotStore
	actor uuid.UUID
}

func (b *botMemoryAuthorizerOnly) AuthorizeBotMemoryWrite(context.Context, BotID) (uuid.UUID, error) {
	return b.actor, nil
}

func TestBotMemoryMissingMutationCapability(t *testing.T) {
	b := &botMemoryBots{bot: Bot{ID: uuid.New(), Scope: BotScopePersonal, HomeSessionID: uuid.New()}}
	backend := &botMemoryBackend{rows: []MemorySearchResult{{ID: "home", SessionID: b.bot.HomeSessionID.String()}}}
	svc, _ := NewBotMemoryService(&botMemoryAuthorizerOnly{BotStore: b, actor: uuid.New()}, backend, b.bot.ID, BotScopePersonal)
	ctx := context.Background()
	for _, op := range []func() error{
		func() error { return svc.Add(ctx, MemoryEntry{SessionID: b.bot.HomeSessionID.String()}) },
		func() error { return svc.Update(ctx, "home", "changed", "") },
		func() error { return svc.Delete(ctx, "home") },
	} {
		if err := op(); !errors.Is(err, ErrBotUnsupported) {
			t.Fatalf("unsafe fallback: %v", err)
		}
	}
	if backend.added != nil || backend.updates != 0 || backend.deletes != 0 {
		t.Fatal("generic memory mutated")
	}
}

func TestBotMemoryBoundaries(t *testing.T) {
	for _, scope := range []BotScope{BotScopePersonal, BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			b := &botMemoryBots{bot: Bot{ID: uuid.New(), Scope: scope, HomeSessionID: uuid.New()}}
			runSession := uuid.New()
			b.runs = []BotRun{{ID: uuid.New(), BotID: b.bot.ID, SessionID: runSession}}
			backend := &botMemoryBackend{rows: []MemorySearchResult{
				{ID: "foreign", SessionID: uuid.NewString(), Scope: string(scope), Score: 1},
				{ID: "home", SessionID: b.bot.HomeSessionID.String(), Scope: string(scope), CreatedBy: uuid.NewString(), CreatedAt: "2026-11-04T00:00:00Z", Path: "notes", Category: "facts", Score: .5},
				{ID: "run", SessionID: runSession.String()}, {ID: "unattributed"},
				{ID: "wrong-scope", SessionID: runSession.String(), Scope: "org"},
			}}
			service, err := NewBotMemoryService(b, backend, b.bot.ID, scope)
			if err != nil {
				t.Fatal(err)
			}
			row, err := service.Get(ctx, "home")
			if err != nil || !reflect.DeepEqual(*row, backend.rows[1]) {
				t.Fatalf("lost attribution: %+v %v", row, err)
			}
			if _, err = service.Get(ctx, "run"); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"foreign", "unattributed", "wrong-scope"} {
				if row, err = service.Get(ctx, id); !errors.Is(err, ErrBotForbidden) || row != nil {
					t.Fatalf("%s leaked: %+v %v", id, row, err)
				}
			}
			if _, err = service.Search(ctx, "query", 1, 0); !errors.Is(err, ErrBotUnsupported) {
				t.Fatalf("search: %v", err)
			}
			if _, err = service.List(ctx, "", 1, 0); !errors.Is(err, ErrBotUnsupported) {
				t.Fatalf("list: %v", err)
			}
			if _, err = service.ListBySession(ctx, backend.rows[0].SessionID); !errors.Is(err, ErrBotForbidden) {
				t.Fatal(err)
			}
			if rows, err := service.ListBySession(ctx, b.bot.HomeSessionID.String()); err != nil || len(rows) != 1 {
				t.Fatalf("session: %v %v", rows, err)
			}
			if err = service.Add(ctx, MemoryEntry{SessionID: b.bot.HomeSessionID.String()}); !errors.Is(err, ErrBotUnsupported) {
				t.Fatalf("missing write authority: %v", err)
			}
			writer := &botMemoryWriter{botMemoryBots: b, actor: uuid.New()}
			service.bots = writer
			original := MemoryEntry{SessionID: runSession.String(), Content: "observation", SourcePath: "report.md", Category: "facts", Metadata: map[string]any{"source": "primary"}}
			if err = service.Add(ctx, original); err != nil {
				t.Fatal(err)
			}
			expected := original
			expected.CreatedBy = writer.actor.String()
			if !reflect.DeepEqual(*backend.added, expected) {
				t.Fatalf("provenance: %+v", backend.added)
			}
			original.CreatedBy = uuid.NewString()
			if err = service.Add(ctx, original); !errors.Is(err, ErrBotForbidden) {
				t.Fatal(err)
			}
			if err = service.Update(ctx, "foreign", "new", ""); !errors.Is(err, ErrBotForbidden) {
				t.Fatal(err)
			}
			if err = service.Delete(ctx, "foreign"); !errors.Is(err, ErrBotForbidden) {
				t.Fatal(err)
			}
			if err = service.Update(ctx, "home", "new", ""); err != nil {
				t.Fatal(err)
			}
			if err = service.Delete(ctx, "home"); err != nil {
				t.Fatal(err)
			}
			writer.denied = true
			if err = service.Delete(ctx, "home"); !errors.Is(err, ErrBotForbidden) {
				t.Fatal(err)
			}
			if backend.updates != 1 || backend.deletes != 1 {
				t.Fatal("unauthorized mutations")
			}
			b.revoked = true
			if _, err = service.Get(ctx, "home"); !errors.Is(err, ErrBotForbidden) {
				t.Fatal(err)
			}
		})
	}
}

func TestBotMemoryPrefilterAndPagination(t *testing.T) {
	b := &botMemoryBots{bot: Bot{ID: uuid.New(), Scope: BotScopePersonal, HomeSessionID: uuid.New()}}
	for i := 0; i < 201; i++ {
		b.runs = append(b.runs, BotRun{ID: uuid.New(), BotID: b.bot.ID, SessionID: uuid.New()})
	}
	own := MemorySearchResult{ID: "own", SessionID: b.runs[200].SessionID.String(), Score: .1, CreatedBy: uuid.NewString()}
	backend := &botMemoryPrefilter{botMemoryBackend: &botMemoryBackend{rows: []MemorySearchResult{{ID: "foreign-best", SessionID: uuid.NewString(), Score: 1}, own}}}
	s, _ := NewBotMemoryService(b, backend, b.bot.ID, BotScopePersonal)
	rows, err := s.Search(context.Background(), "query", 1, 0)
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], own) || len(backend.received) != 202 {
		t.Fatalf("prefilter lost owned result: %+v %v", rows, err)
	}
	rows, err = s.List(context.Background(), "", 1, 0)
	if err != nil || len(rows) != 1 || rows[0].ID != "own" {
		t.Fatalf("list: %+v %v", rows, err)
	}
	backend.leak = true
	if rows, err = s.Search(context.Background(), "query", 1, 0); !errors.Is(err, ErrBotForbidden) || rows != nil {
		t.Fatalf("bad backend not rejected: %+v %v", rows, err)
	}
	if rows, err = s.List(context.Background(), "", 10, 0); !errors.Is(err, ErrBotForbidden) || rows != nil {
		t.Fatalf("bad list: %+v %v", rows, err)
	}
	b.runs[0].BotID = uuid.New()
	if _, err = s.Get(context.Background(), "own"); !errors.Is(err, ErrBotForbidden) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Get(ctx, "own"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
