package launcher

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
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
func TestBotMemoryUnavailableHasNoGenericFallback(t *testing.T) {
	for _, mode := range []execution.BotExecutionMode{execution.BotExecutionModeLegacy, execution.BotExecutionModeChatInteractive} {
		t.Run(string(mode), func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.services.Memory = nil
			f.req.Run.ExecutionMode = mode
			if mode == execution.BotExecutionModeChatInteractive {
				f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
				f.req.Authorization = interactiveFactoryAuthorization{}
				f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
					return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &catalogTestPool{}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
				}
			}
			r, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Cleanup()
			for _, candidate := range append(r.Agent.Tools, r.Agent.SubAgentManager.AllTools()...) {
				switch candidate.Name() {
				case "memory_save", "memory_search", "memory_get", "memory_delete":
					t.Errorf("unavailable Bot memory exposed generic %s", candidate.Name())
				}
			}
		})
	}
}

func TestBotChatCurrentMemoryScopeAndFence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []execution.BotExecutionMode{execution.BotExecutionModeLegacy, execution.BotExecutionModeChatInteractive} {
			t.Run(string(scope)+"/"+string(mode), func(t *testing.T) {
				f := newBotFactoryFixture(t)
				f.req.Run.ExecutionMode = mode
				if mode == execution.BotExecutionModeChatInteractive {
					f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
					f.req.Authorization = interactiveFactoryAuthorization{}
					f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
						return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &catalogTestPool{}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
					}
				}
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
				for _, name := range []string{"memory_save", "memory_search", "memory_get", "memory_delete"} {
					count := 0
					for _, candidate := range resources.Agent.Tools {
						if candidate.Name() != name {
							continue
						}
						count++
						var bound *botFactoryBinding
						switch tool := candidate.(type) {
						case *botBoundTool:
							bound = tool.binding
						case *interactiveBotTool:
							bound = tool.binding
						}
						if bound == nil || store.MemoryStoreFromContext(bound.owner) != nil || store.ThreeTierSearcherFromContext(bound.owner) != nil {
							t.Fatalf("%s has unbound or owner-wide memory fallback", name)
						}
					}
					if count != 1 {
						t.Fatalf("%s count=%d", name, count)
					}
				}
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
}
