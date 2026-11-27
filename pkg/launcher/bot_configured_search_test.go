package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

type researchTestRuntime struct {
	store.BotMandateRuntime
	m               execution.BotMandate
	calls           int64
	revoked         bool
	denyReservation bool
}

func (r *researchTestRuntime) VerifyBotMandate(ctx context.Context, _ execution.BotMandateRef, _ execution.BotMandatePurpose) (execution.BotMandate, error) {
	if ctx.Err() != nil {
		return execution.BotMandate{}, ctx.Err()
	}
	if r.revoked {
		return execution.BotMandate{}, store.ErrBotForbidden
	}
	return r.m.Clone(), nil
}
func (r *researchTestRuntime) ReserveBotPublicResearch(_ context.Context, _ execution.BotMandateRef, _ string) error {
	if r.revoked || r.denyReservation {
		return store.ErrBotForbidden
	}
	r.calls++
	return nil
}
func researchFixture(t *testing.T) (*botFactoryFixture, *researchTestRuntime, *BotResearchEpisode) {
	t.Helper()
	f := newBotFactoryFixture(t)
	knowledgeSearchSettings(f)
	route, err := BotEffectiveConfiguredSearchRoute(f.ctx, f.services)
	if err != nil {
		t.Fatal(err)
	}
	identity := f.req.Policy.Identity()
	m := execution.BotMandate{Ref: execution.BotMandateRef{ID: uuid.New(), BotID: identity.BotID, OrgSlug: identity.OrgSlug, TeamSlug: identity.TeamSlug, OwnerID: identity.ActorID, Revision: 1}, Purpose: execution.BotMandatePurposeTask, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{string(execution.CapabilityToolExecute)}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{BotConfiguredSearchToolName, "web_fetch"}}}, Limits: execution.BotMandateLimits{MinScheduleIntervalSeconds: 1, MaxSchedules: 1, MaxDailyRuns: 1, MaxConcurrentRuns: 1, MaxRuntimeSeconds: 60, MaxToolCalls: 10, MaxTokens: 10000, MaxTotalToolCalls: 100, MaxTotalTokens: 100000}, PublicResearch: &execution.BotPublicResearchScope{Version: 1, Subject: "Stock Market News", SourceEventID: "owner-event", Route: route, Facets: []execution.BotResearchFacet{execution.BotResearchOverview, execution.BotResearchRecent}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 2}}
	runtime := &researchTestRuntime{m: m}
	auth, err := execution.VerifyBotMandateAuthorization(f.ctx, runtime, m.Ref, m.Purpose)
	if err != nil {
		t.Fatal(err)
	}
	f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: runtime, Identity: identity}
	budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.req.Budget = budget
	episode, err := NewBotResearchEpisode(f.req.Run.ID, f.req.Run.SessionID.String(), BotResearchOpenWeb, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f, runtime, episode
}
func TestBotConfiguredSearch(t *testing.T) {
	for _, mode := range []string{"success", "empty", "429", "credential-change", "revoked", "spoofed"} {
		t.Run(mode, func(t *testing.T) {
			f, runtime, episode := researchFixture(t)
			f.ctx, _ = bot.WithToolOutcomes(f.ctx)
			searchModel := &knowledgeModel{text: "Sources: https://example.com/decisive"}
			if mode == "empty" {
				searchModel.text = "no sources"
			}
			if mode == "429" {
				searchModel.err = errors.New("429 team-secret")
			}
			factory := func(ctx context.Context, p, m string, cfg *config.AppConfig) (model.LLM, error) {
				if !provider.IsConfinedProviderSecrets(ctx) || p != "search" || m != "sonar" || cfg.Providers[p]["api_key"] != "team-secret" {
					t.Fatal("search engine not scoped")
				}
				return searchModel, nil
			}
			tool, err := BotConfiguredResearchTool(f.ctx, f.req, knowledgeScope(f), factory, nil, episode)
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"facet": "overview"}
			switch mode {
			case "credential-change":
				f.services.Settings = botKnowledgeTeamSettings{row: &store.TeamSettings{Providers: map[string]store.ProviderConfig{"search": {"api_key": "changed"}}}}
			case "revoked":
				runtime.revoked = true
			case "spoofed":
				args["query"] = "private assignment"
			}
			if episode.AllowDetail(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String(), "https://example.com/guessed") == nil {
				t.Fatal("pre-search detail allowed")
			}
			toolContext := optionalKnowledgeContext{knowledgeCtx(f)}
			result, err := tool.(botRunnableTool).Run(toolContext, args)
			if outcome, ok := bot.TakeToolOutcome(toolContext, toolContext.FunctionCallID()); ok && (outcome.Verified || len(outcome.SourceAccess) != 0) {
				t.Fatal("search snippets became primary source/effect attestation")
			}
			if mode != "success" && episode.AllowDetail(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String(), "https://example.com/decisive") == nil {
				t.Fatal("empty/error/denied search unlocked detail")
			}
			if mode == "success" {
				if err != nil || episode.State() != BotResearchSearched || searchModel.calls != 1 || runtime.calls != 1 {
					t.Fatalf("search: %v %s %d %d", err, episode.State(), searchModel.calls, runtime.calls)
				}
				raw, _ := json.Marshal(result)
				if strings.Contains(string(raw), "team-secret") {
					t.Fatal("secret leak")
				}
				usage := f.req.Budget.(*bot.SharedRunBudget).Usage()
				if usage.InputTokens != 3 || usage.OutputTokens != 5 {
					t.Fatalf("double or missing model charge: %+v", usage)
				}
				if _, err := tool.(botRunnableTool).Run(optionalKnowledgeContext{knowledgeCtx(f)}, args); err != nil {
					t.Fatal(err)
				}
				if _, err := tool.(botRunnableTool).Run(optionalKnowledgeContext{knowledgeCtx(f)}, args); err != nil || searchModel.calls != 3 {
					t.Fatal("historical search cap enforced")
				}
			} else if mode == "empty" {
				if err != nil || episode.State() != BotResearchEmpty {
					t.Fatalf("empty: %v %s", err, episode.State())
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "team-secret") {
					t.Fatalf("failed search: %v", err)
				}
			}
		})
	}
}

func TestBotConfiguredSearchAdmittedCustomQueries(t *testing.T) {
	f, runtime, episode := researchFixture(t)
	f.req.Run.IdempotencyKey = "admitted-followup-turn"
	runtime.m.PublicResearch.MaxCalls = 5
	f.ctx, _ = bot.WithToolOutcomes(f.ctx)
	searchModel := &researchCoverageProbeModel{knowledgeModel: knowledgeModel{text: "Sources: https://example.com/decisive"}}
	factory := func(context.Context, string, string, *config.AppConfig) (model.LLM, error) { return searchModel, nil }
	tool, err := BotConfiguredResearchTool(f.ctx, f.req, knowledgeScope(f), factory, nil, episode)
	if err != nil {
		t.Fatal(err)
	}
	native := tool.(botRunnableTool)
	ctx := optionalKnowledgeContext{knowledgeCtx(f)}
	if _, err := native.Run(ctx, map[string]any{"query": "Meta Platforms latest news"}); err != nil {
		t.Fatalf("custom company query rejected on admitted run: %v", err)
	}
	if _, err := native.Run(ctx, map[string]any{"facet": "recent", "query": "Apple Inc latest news"}); err != nil {
		t.Fatalf("facet + custom company query rejected on admitted run: %v", err)
	}
	if len(searchModel.requests) != 2 || !strings.Contains(searchModel.requests[0], "Meta Platforms latest news") || !strings.Contains(searchModel.requests[1], "Apple Inc latest news") {
		t.Fatalf("custom queries not dispatched: %v", searchModel.requests)
	}
}
