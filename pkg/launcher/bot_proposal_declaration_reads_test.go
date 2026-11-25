package launcher

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
)

func TestBotProposalDeclarationSharesRequestReads(t *testing.T) {
	f := newBotFactoryFixture(t)
	repo := &proposalTestRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
	f.services.Bots, f.services.PersonalBots = repo, repo
	var checked context.Context
	b := &botFactoryBinding{owner: store.WithServices(f.ctx, f.services), req: f.req, declarations: &botDeclarationReads{}, verifyIsolation: func(ctx context.Context) error { checked = ctx; return nil }}
	defer b.declarations.close()
	raw, err := newBotProposalTool(b)
	if err != nil {
		t.Fatal(err)
	}
	proposal := raw.(*botProposalTool)
	ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: f.ctx}, sid: f.req.Run.SessionID.String()}
	request := &model.LLMRequest{}
	first := b.declarations.bind(b, request)
	if err := proposal.ProcessRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if entstore.WithBotContentReadOperationFromContext(context.Background(), checked) == context.Background() {
		t.Fatal("proposal guard opened and closed independent immutable-read scope")
	}
	if b.declarations.owner != first.owner || request.Tools[botProposalToolName] != proposal || repo.calls != 0 {
		t.Fatal("proposal changed request scope, unwrapped dispatch, or executed during declaration")
	}
	retained := checked
	next := &model.LLMRequest{}
	if err := proposal.ProcessRequest(ctx, next); err != nil {
		t.Fatal(err)
	}
	if b.declarations.owner == first.owner || entstore.WithBotContentReadOperationFromContext(context.Background(), retained) != context.Background() {
		t.Fatal("later request retained prior immutable reads")
	}
	retained = checked
	provider := &declarationBoundaryModel{t: t}
	llm := &botFactoryLLM{LLM: provider, binding: b, researchOnly: true}
	for _, err := range llm.GenerateContent(f.ctx, next, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if provider.calls != 1 || b.declarations.request != nil || entstore.WithBotContentReadOperationFromContext(context.Background(), retained) != context.Background() {
		t.Fatal("proposal read scope survived provider entry")
	}
	request = &model.LLMRequest{}
	if err := proposal.ProcessRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	retained = checked
	f.req.Run.Lease.Fence++
	if err := proposal.ProcessRequest(ctx, request); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("cached authority: %v", err)
	}
	if b.declarations.request != nil || entstore.WithBotContentReadOperationFromContext(context.Background(), retained) != context.Background() {
		t.Fatal("failed proposal declaration retained immutable reads")
	}
}
