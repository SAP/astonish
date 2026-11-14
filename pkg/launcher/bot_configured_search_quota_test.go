package launcher

import (
	"context"
	"strings"
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
	r.calls = 2
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
func TestBotConfiguredSearchQuotaPreservesDiscovery(t *testing.T) {
	for _, denial := range []string{"quota", "revoked", "route-missing", "route-changed", "cancelled", "provider-error"} {
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
			for i := 0; i < 2; i++ {
				result, err := search.(botRunnableTool).Run(c, args)
				if err != nil {
					t.Fatal(err)
				}
				metadata, ok := result["metadata"].(map[string]any)
				if !ok || metadata["remaining_episode_search_ceiling"] != float64(1-i) {
					t.Fatalf("remaining ceiling: %v", result)
				}
			}
			_, _ = bot.TakeToolOutcome(c, c.FunctionCallID())
			if _, err := search.(botRunnableTool).Run(c, args); err == nil {
				t.Fatal("quota bypass")
			}
			if searchModel.calls != 2 || runtime.calls != 2 {
				t.Fatal("quota denial dispatched")
			}
			if episode.State() != BotResearchFailed {
				t.Fatal("negative quota observation lost")
			}
			o, ok := bot.TakeToolOutcome(c, c.FunctionCallID())
			if !ok || !o.Verified || o.Nonexecution == nil || o.Nonexecution.CallID != c.FunctionCallID() || o.Nonexecution.ToolName != search.Name() || len(o.SourceAccess) != 0 || len(o.Changed) != 0 {
				t.Fatalf("missing exact trusted nonexecution: %+v", o)
			}
			if denial == "revoked" {
				runtime.revoked = true
			}
			if denial == "route-missing" {
				f.services.Settings = nil
				f.services.PersonalSettings = nil
			}
			if denial == "route-changed" {
				f.services.Settings = botKnowledgeTeamSettings{}
			}
			if denial == "cancelled" {
				ctx, cancel := context.WithCancel(f.ctx)
				cancel()
				f.ctx = ctx
			}
			if denial == "provider-error" {
				// A non-quota failure must not inherit the narrow quota exception.
				generation, err := episode.Begin(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String())
				if err != nil {
					t.Fatal(err)
				}
				episode.observe(f.ctx, generation, 0, context.DeadlineExceeded)
			}
			err = episode.AllowDetail(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String(), "https://example.com/decisive")
			if denial == "quota" && err != nil {
				t.Fatalf("quota erased discovery: %v", err)
			}
			if denial != "quota" && err == nil {
				t.Fatalf("%s unlocked detail", denial)
			}
			if denial == "quota" && !strings.Contains(search.(botRunnableTool).Declaration().Description, "2") {
				t.Fatal("search ceiling missing from producer description")
			}
		})
	}
}
