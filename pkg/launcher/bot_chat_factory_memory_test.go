package launcher

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type botPromptMemoryRepo struct{ botFactoryTestRepo }

func (r botPromptMemoryRepo) ListRuns(context.Context, store.BotID, store.BotPage) ([]store.BotRun, error) {
	return []store.BotRun{r.fixture.req.Run}, nil
}

type botPromptMemoryStore struct {
	store.MemoryStore
	rows  []store.MemorySearchResult
	after func()
	leak  bool
}

func (m *botPromptMemoryStore) SearchBySessions(context.Context, []string, string, int, float64, string) ([]store.MemorySearchResult, error) {
	panic("unexpected search")
}
func (m *botPromptMemoryStore) ListBySessions(ctx context.Context, ids []string, category string, limit, offset int) ([]store.MemorySearchResult, error) {
	if limit != 20 || offset != 0 {
		panic("unbounded read")
	}
	if m.after != nil {
		m.after()
	}
	var out []store.MemorySearchResult
	for _, row := range m.rows {
		for _, id := range ids {
			if row.SessionID == id || m.leak {
				out = append(out, row)
				break
			}
		}
	}
	return out, nil
}
func TestBotChatCurrentMemoryScopeAndFence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.req.Bot.Scope = scope
			repo := botPromptMemoryRepo{botFactoryTestRepo{fixture: f}}
			f.services.Bots, f.services.PersonalBots = repo, repo
			row := store.MemorySearchResult{Snippet: "Delivery is Friday", SessionID: f.req.Run.SessionID.String(), Scope: string(scope), Attribution: &store.MemoryAttribution{BotID: f.req.Bot.ID, Source: "notice", ObservedAt: time.Now(), Status: store.MemoryKnowledgeInferred}}
			m := &botPromptMemoryStore{rows: []store.MemorySearchResult{row, {Snippet: "another Bot private memory", SessionID: uuid.NewString(), Scope: string(scope)}}}
			f.services.Memory = m
			owner := store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services)
			b := &botFactoryBinding{owner: owner, req: f.req, verifyIsolation: func(context.Context) error { return nil }}
			viewer := store.WithServices(context.Background(), &store.Services{Memory: &botPromptMemoryStore{rows: []store.MemorySearchResult{{Snippet: "viewer secret"}}}})
			text, err := b.currentMemory(viewer)
			if err != nil || !strings.Contains(text, "Delivery is Friday") || strings.Contains(text, "another Bot") || strings.Contains(text, "viewer secret") {
				t.Fatalf("scope: %q %v", text, err)
			}
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			prompt := resources.Agent.SystemPrompt.Build()
			resources.Cleanup()
			if !strings.Contains(prompt, "Delivery is Friday") || strings.Contains(prompt, "another Bot private memory") {
				t.Fatal("factory prompt lost scope")
			}
			m.leak = true
			if text, err = b.currentMemory(viewer); !errors.Is(err, store.ErrBotForbidden) || text != "" {
				t.Fatalf("foreign row accepted: %q %v", text, err)
			}
			m.leak = false
			for _, mutate := range []func(){
				func() { f.req.Bot.OwnerID = uuid.New() },
				func() { f.req.Bot.Version++ },
			} {
				original := f.req.Bot
				mutate()
				if text, err = b.currentMemory(viewer); !errors.Is(err, store.ErrBotConflict) || text != "" {
					t.Fatalf("changed owner/version accepted: %q %v", text, err)
				}
				f.req.Bot = original
			}
			m.after = func() { f.req.Run.Lease.Fence++ }
			if text, err = b.currentMemory(viewer); !errors.Is(err, store.ErrBotConflict) || text != "" {
				t.Fatalf("stale fence accepted: %q %v", text, err)
			}
		})
	}
}
