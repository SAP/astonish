package launcher

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
)

type sharedHydrationRepo struct {
	botFactoryTestRepo
	revoked bool
}

func (r *sharedHydrationRepo) Get(ctx context.Context, id store.BotID) (*store.Bot, error) {
	if r.revoked {
		return nil, store.ErrBotForbidden
	}
	return r.botFactoryTestRepo.Get(ctx, id)
}

// ADK declaration callbacks receive the original invocation context, not the
// context returned to InstructionProvider. Shared binding must explicitly borrow
// that preparation's immutable bytes, but never its authorization result.
func TestBotSharedBindingBorrowsPreparationReads(t *testing.T) {
	f := newBotFactoryFixture(t)
	repo := &sharedHydrationRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
	f.services.PersonalBots, f.services.Bots = repo, repo
	var checked context.Context
	checks := 0
	b := &botFactoryBinding{owner: store.WithServices(f.ctx, f.services), req: f.req, declarations: &botDeclarationReads{}, verifyIsolation: func(ctx context.Context) error { checked = ctx; checks++; return nil }}
	prep := botSharedModelPreparation{b}
	prepared := prep.Begin(f.ctx)
	defer prep.End()
	hook := botSharedBinding{b}
	for i := 0; i < 12; i++ {
		bound, err := hook.Bind(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if entstore.WithBotContentReadOperationFromContext(context.Background(), bound) == context.Background() || entstore.WithBotContentReadOperationFromContext(context.Background(), checked) == context.Background() {
			t.Fatal("shared declaration lost the active preparation operation")
		}
	}
	if checks != 12 {
		t.Fatalf("authorization checks=%d, want every binding", checks)
	}
	f.req.Run.Lease.Fence++
	if _, err := hook.Bind(f.ctx); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("stale lease accepted: %v", err)
	}
	f.req.Run.Lease.Fence--
	repo.revoked = true
	if _, err := hook.Bind(f.ctx); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("revoked membership accepted: %v", err)
	}
	repo.revoked = false
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := hook.Bind(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled binding accepted: %v", err)
	}
	provider := &declarationBoundaryModel{t: t}
	llm := &botFactoryLLM{LLM: provider, binding: b, researchOnly: true}
	for _, err := range llm.GenerateContent(f.ctx, &model.LLMRequest{}, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if provider.calls != 1 || entstore.WithBotContentReadOperationFromContext(context.Background(), prepared) != context.Background() {
		t.Fatal("preparation survived provider entry")
	}
	if _, err := hook.Bind(f.ctx); err != nil {
		t.Fatal(err)
	}
	if entstore.WithBotContentReadOperationFromContext(context.Background(), checked) != context.Background() {
		t.Fatal("standalone binding retained its reads")
	}
}
