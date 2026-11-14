package launcher

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

func TestBotConfiguredSearchConcurrentFacets(t *testing.T) {
	f, runtime, episode := researchFixture(t)
	runtime.m.PublicResearch.MaxCalls = 4
	runtime.m.PublicResearch.Facets = []execution.BotResearchFacet{execution.BotResearchOverview, execution.BotResearchRecent, execution.BotResearchPrimary, execution.BotResearchContradictions}
	auth, err := execution.VerifyBotMandateAuthorization(f.ctx, runtime, runtime.m.Ref, runtime.m.Purpose)
	if err != nil {
		t.Fatal(err)
	}
	f.req.Mandate.Authorization = auth
	var active, maximum, calls atomic.Int32
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	factory := func(ctx context.Context, p, m string, cfg *config.AppConfig) (model.LLM, error) {
		if p != "search" || m != "sonar" || !provider.IsConfinedProviderSecrets(ctx) {
			return nil, store.ErrBotForbidden
		}
		current := active.Add(1)
		defer active.Add(-1)
		if current > maximum.Load() {
			maximum.Store(current)
		}
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &knowledgeModel{text: "Sources: https://example.com/decisive"}, nil
	}
	search, err := BotConfiguredResearchTool(f.ctx, f.req, knowledgeScope(f), factory, nil, episode)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 4)
	for _, facet := range runtime.m.PublicResearch.Facets {
		go func(facet execution.BotResearchFacet) {
			_, err := search.(botRunnableTool).Run(optionalKnowledgeContext{knowledgeCtx(f)}, map[string]any{"facet": string(facet)})
			results <- err
		}(facet)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no search dispatched")
	}
	if episode.AllowDetail(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String(), "https://example.com") == nil {
		t.Fatal("detail before search completion")
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	for range 4 {
		select {
		case err := <-results:
			if err != nil {
				t.Errorf("legitimate facet rejected: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("serialized searches deadlocked")
		}
	}
	if calls.Load() != 4 || maximum.Load() != 1 || runtime.calls != 4 || episode.State() != BotResearchSearched {
		t.Fatalf("dispatch=%d parallel=%d quota=%d state=%s", calls.Load(), maximum.Load(), runtime.calls, episode.State())
	}
	usage := f.req.Budget.(*bot.SharedRunBudget).Usage()
	if usage.InputTokens != 12 || usage.OutputTokens != 20 {
		t.Fatalf("model usage: %+v", usage)
	}
	if _, err := search.(botRunnableTool).Run(optionalKnowledgeContext{knowledgeCtx(f)}, map[string]any{"facet": "overview"}); err == nil || calls.Load() != 4 || runtime.calls != 4 {
		t.Fatal("finite research quota bypassed")
	}
}
