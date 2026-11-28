package launcher

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
)

func TestBotStartupReadsBorrowedThroughConstructionAndDeclarations(t *testing.T) {
	f := newBotFactoryFixture(t)
	ctx, end := WithBotStartupReads(f.ctx)
	defer end()
	f.ctx = ctx
	var retained context.Context
	load := f.cfg.LoadScope
	f.cfg.LoadScope = func(ctx context.Context, req bot.RunRequest) (*BotChatScope, error) {
		retained = ctx
		return load(ctx, req)
	}
	resources, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer resources.Cleanup()
	if entstore.WithBotContentReadOperationFromContext(context.Background(), retained) == context.Background() {
		t.Fatal("factory closed caller-owned startup operation")
	}
	b := &botFactoryBinding{owner: store.WithServices(retained, f.services), req: f.req, declarations: &botDeclarationReads{}, verifyIsolation: func(context.Context) error { return nil }}
	request := &model.LLMRequest{}
	decl := b.declarations.bind(b, request)
	if decl.owner != b.owner {
		t.Fatal("declarations failed to borrow startup operation")
	}
	provider := &declarationBoundaryModel{t: t}
	llm := &botFactoryLLM{LLM: provider, binding: b, researchOnly: true}
	for _, err := range llm.GenerateContent(ctx, request, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if provider.calls != 1 || entstore.WithBotContentReadOperationFromContext(context.Background(), retained) != context.Background() || entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) != context.Background() {
		t.Fatal("startup operation survived first provider boundary")
	}
}
