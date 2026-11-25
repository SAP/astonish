package launcher

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
)

func TestBotGuardChainSharesReadOperation(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "borrowed"}[existing], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			owner := store.WithServices(f.ctx, f.services)
			if existing {
				var end func()
				owner, end = entstore.WithBotContentReadOperation(owner)
				defer end()
			}
			var budgetCtx, isolationCtx context.Context
			b := &botFactoryBinding{owner: owner, req: f.req, verifyIsolation: func(ctx context.Context) error {
				isolationCtx = ctx
				if entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) == context.Background() {
					t.Fatal("isolation lost guard operation")
				}
				return nil
			}}
			budget := func(ctx context.Context) error {
				budgetCtx = ctx
				if entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) == context.Background() {
					t.Fatal("budget lost guard operation")
				}
				return nil
			}
			if err := b.checkWithBudget(f.ctx, budget); err != nil {
				t.Fatal(err)
			}
			if budgetCtx != isolationCtx {
				t.Fatal("budget and isolation did not share bound context")
			}
			active := entstore.WithBotContentReadOperationFromContext(context.Background(), budgetCtx) != context.Background()
			if active != existing {
				t.Fatalf("operation lifetime: active=%v borrowed=%v", active, existing)
			}
			f.req.Run.Lease.Fence++
			if err := b.checkWithBudget(f.ctx, budget); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("lease revocation bypassed: %v", err)
			}
		})
	}
}

func TestBotDeclarationReadsRequestLifetime(t *testing.T) {
	f := newBotFactoryFixture(t)
	b := &botFactoryBinding{owner: store.WithServices(f.ctx, f.services), req: f.req, verifyIsolation: func(context.Context) error { return nil }}
	reads := &botDeclarationReads{}
	defer reads.close()
	req := &model.LLMRequest{}
	first := reads.bind(b, req)
	second := reads.bind(b, req)
	if first.owner != second.owner {
		t.Fatal("one request did not share read operation")
	}
	if first.owner == b.owner {
		t.Fatal("request scope not isolated")
	}
	// Nested adapters bind again through the original binding. Only the read
	// operation may survive that rebinding, never ambient owner/service values.
	rebound := b.bind(first.owner)
	if entstore.WithBotContentReadOperationFromContext(context.Background(), rebound) == context.Background() {
		t.Fatal("nested owner rebinding dropped request read operation")
	}
	if err := first.check(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.req.Run.Lease.Fence++
	if err := second.check(f.ctx); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("cached authority: %v", err)
	}
	f.req.Run.Lease.Fence--
	ended := 0
	oldEnd := reads.end
	reads.end = func() { ended++; oldEnd() }
	next := reads.bind(b, &model.LLMRequest{})
	if ended != 1 || next.owner == first.owner {
		t.Fatal("new request reused prior request")
	}
	reads.close()
	if reads.request != nil || reads.owner != nil || reads.end != nil {
		t.Fatal("cleanup retained request")
	}
	again := reads.bind(b, req)
	if again.owner == first.owner {
		t.Fatal("recycled request reused ended operation")
	}
	if (*botDeclarationReads)(nil).bind(b, req) != b || reads.bind(b, nil) != b {
		t.Fatal("nil request manufactured cache")
	}
}

type declarationBoundaryModel struct {
	model.LLM
	t     *testing.T
	calls int
}

func (m *declarationBoundaryModel) GenerateContent(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) != context.Background() {
			m.t.Error("request read operation survived into provider")
		}
		yield(&model.LLMResponse{}, nil)
	}
}

func TestBotDeclarationReadsEndBeforeProvider(t *testing.T) {
	f := newBotFactoryFixture(t)
	b := &botFactoryBinding{owner: store.WithServices(f.ctx, f.services), req: f.req, declarations: &botDeclarationReads{}, verifyIsolation: func(context.Context) error { return nil }}
	request := &model.LLMRequest{}
	declaration := b.declarations.bind(b, request)
	provider := &declarationBoundaryModel{t: t}
	llm := &botFactoryLLM{LLM: provider, binding: b, researchOnly: true}
	for _, err := range llm.GenerateContent(f.ctx, request, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if provider.calls != 1 || b.declarations.request != nil {
		t.Fatal("provider not called or declarations retained")
	}
	if entstore.WithBotContentReadOperationFromContext(context.Background(), declaration.owner) != context.Background() {
		t.Fatal("retained declaration context survived provider entry")
	}
}
