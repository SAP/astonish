package launcher

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"google.golang.org/adk/model"
)

func TestBotConfiguredSearchUnknownReservationDenialStaysBlocked(t *testing.T) {
	f, r, e := researchFixture(t)
	f.ctx, _ = bot.WithToolOutcomes(f.ctx)
	m := &knowledgeModel{text: "Sources: https://example.com/decisive"}
	search, err := BotConfiguredResearchTool(f.ctx, f.req, knowledgeScope(f), func(context.Context, string, string, *config.AppConfig) (model.LLM, error) { return m, nil }, nil, e)
	if err != nil {
		t.Fatal(err)
	}
	c := optionalKnowledgeContext{knowledgeCtx(f)}
	args := map[string]any{"facet": "overview"}
	if _, err := search.(botRunnableTool).Run(c, args); err != nil {
		t.Fatal(err)
	}
	_, _ = bot.TakeToolOutcome(c, c.FunctionCallID())
	// Simulate durable denial not proven by this episode's reservation count.
	r.denyReservation = true
	if _, err := search.(botRunnableTool).Run(c, args); err == nil {
		t.Fatal("denial bypass")
	}
	if m.calls != 1 || e.AllowDetail(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String(), "https://example.com/decisive") == nil {
		t.Fatal("unknown denial recovered")
	}
	o, ok := bot.TakeToolOutcome(c, c.FunctionCallID())
	if !ok || o.Nonexecution == nil || !o.Verified {
		t.Fatal("predispatch denial lacks receipt")
	}
}
func TestBotConfiguredSearchBeyondHistoricalCapsKeepsAuthority(t *testing.T) {
	for _, denial := range []string{"revoked", "route-missing", "route-changed", "cancelled", "provider-error"} {
		t.Run(denial, func(t *testing.T) {
			f, runtime, episode := researchFixture(t)
			f.ctx, _ = bot.WithToolOutcomes(f.ctx)
			searchModel := &knowledgeModel{text: "Sources: https://example.com/decisive"}
			search, err := BotConfiguredResearchTool(f.ctx, f.req, knowledgeScope(f), func(context.Context, string, string, *config.AppConfig) (model.LLM, error) { return searchModel, nil }, nil, episode)
			if err != nil {
				t.Fatal(err)
			}
			c := optionalKnowledgeContext{knowledgeCtx(f)}
			args := map[string]any{"facet": "overview"}
			for i := 0; i < 13; i++ {
				result, err := search.(botRunnableTool).Run(c, args)
				if err != nil {
					t.Fatalf("search %d: %v", i+1, err)
				}
				metadata, ok := result["metadata"].(map[string]any)
				if !ok || metadata["episode_search_calls"] != float64(i+1) {
					t.Fatalf("usage: %v", result)
				}
				if _, exists := metadata["remaining_episode_search_ceiling"]; exists {
					t.Fatal("execution ceiling still advertised")
				}
			}
			if searchModel.calls != 13 || runtime.calls != 13 {
				t.Fatal("usage lost")
			}
			if err := episode.AllowDetail(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String(), "https://example.com/decisive"); err != nil {
				t.Fatal(err)
			}
			switch denial {
			case "revoked":
				runtime.revoked = true
			case "route-missing":
				f.services.Settings = nil
				f.services.PersonalSettings = nil
			case "route-changed":
				f.services.Settings = botKnowledgeTeamSettings{}
			case "cancelled":
				ctx, cancel := context.WithCancel(f.ctx)
				cancel()
				f.ctx = ctx
			case "provider-error":
				generation, err := episode.Begin(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String())
				if err != nil {
					t.Fatal(err)
				}
				episode.observe(f.ctx, generation, 0, context.DeadlineExceeded)
			}
			if episode.AllowDetail(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String(), "https://example.com/decisive") == nil {
				t.Fatalf("%s unlocked detail", denial)
			}
			if denial != "provider-error" {
				c = optionalKnowledgeContext{knowledgeCtx(f)}
				if _, err := search.(botRunnableTool).Run(c, args); err == nil {
					t.Fatalf("%s dispatched", denial)
				}
				if searchModel.calls != 13 {
					t.Fatal("authority denial dispatched")
				}
			}
		})
	}
}
