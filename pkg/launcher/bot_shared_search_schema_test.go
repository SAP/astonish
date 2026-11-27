package launcher

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
)

// Verbatim argument payloads reported for live Bot 495... / run 52daa...,
// SQL event IDs 36263 and 36268. IDs are provenance only: no live DB is read.
const historicalAppleEightQuery = "Latest Apple Inc news as of November 23 2026. Find recent specialist reporting (MacRumors, 9to5Mac, AppleInsider and other technology outlets) on main distinct Apple developments in the past week: products, software, business. Give substantive facts, exact publication dates and source links; separate rumors from confirmed announcements. Do not substitute older stories as current if November coverage is unavailable."
const historicalAppleSixQuery = "What is the latest news about Apple Inc? Include publication dates and source links, and distinguish confirmed news from rumors."

func historicalAppleQuery(mode string) string {
	switch mode {
	case "live-eight":
		return historicalAppleEightQuery
	case "live-six":
		return historicalAppleSixQuery
	default:
		return "Stock Market News overview"
	}
}

type schemaSearchModel struct {
	inner *knowledgeModel
	t     *testing.T
	mode  string
}

func (m schemaSearchModel) Name() string { return m.inner.Name() }
func (m schemaSearchModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	body, _ := json.Marshal(req)
	for _, hint := range []string{"{{", "team-secret"} {
		if strings.Contains(string(body), hint) {
			m.t.Fatalf("optional hint escaped disclosure normalization: %s", body)
		}
	}
	if !strings.Contains(string(body), historicalAppleQuery(m.mode)) {
		m.t.Fatalf("admitted query lost: %s", body)
	}
	want := []string{"Maximum sources: 5"}
	switch m.mode {
	case "complete":
		want = []string{"Maximum sources: 2", "Freshness/recency hint: recent", "Domain preference/restriction: example.com", "Detail level: detailed", "Search mode: news"}
	case "live-eight":
		want = append(want, "Freshness/recency hint: week", "Detail level: detailed", "Search mode: news")
	case "live-six":
		want = append(want, "Detail level: detailed")
	}
	for _, hint := range want {
		if !strings.Contains(string(body), hint) {
			m.t.Fatalf("bounded effective hint lost (%s): %s", hint, body)
		}
	}
	return m.inner.GenerateContent(ctx, req, stream)
}

func TestBotSharedResearchAdvertisedSchema(t *testing.T) {
	for _, mode := range []string{"complete", "omitted", "null", "live-eight", "live-six"} {
		t.Run(mode, func(t *testing.T) {
			f, quota, episode := researchFixture(t)
			if mode == "live-eight" || mode == "live-six" {
				quota.m.PublicResearch.Subject = "Apple News"
				quota.m.PublicResearch.SourceEventID = "reported-live-495-run-52daa-sql-36263-36268"
				auth, err := execution.VerifyBotMandateAuthorization(f.ctx, quota, quota.m.Ref, quota.m.Purpose)
				if err != nil {
					t.Fatal(err)
				}
				f.req.Mandate.Authorization = auth
			}
			f.req.Run.IdempotencyKey = "owner-confirmed-fixture"
			ref := quota.m.Ref
			f.req.Run.MandateRef = &ref
			f.req.Principal = f.req.Mandate.Authorization.Principal(f.req.Principal.Surface)
			f.ctx, _ = execution.WithPrincipal(context.Background(), f.req.Principal)
			budget := &sharedSearchBudget{}
			f.req.Budget = budget
			var err error
			f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomyScoped, AllowedTools: []string{"perplexity_web_search", "web_fetch"}})
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := BotScopedChatConfig(f.ctx, knowledgeScope(f))
			if err != nil {
				t.Fatal(err)
			}
			search := &knowledgeModel{text: "Controlled sourced answer https://evidence.example/source"}
			factoryCalls := 0
			factory := func(ctx context.Context, p, m string, selected *config.AppConfig) (model.LLM, error) {
				factoryCalls++
				if !provider.IsConfinedProviderSecrets(ctx) || p != "search" || m != "sonar" || selected.Providers[p]["api_key"] != "team-secret" {
					t.Fatal("lost configured scoped provider")
				}
				return schemaSearchModel{inner: search, t: t, mode: mode}, nil
			}
			prepare := f.cfg.Prepare
			f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
				safe, err := prepare(ctx, req, scope)
				if err == nil {
					safe.ChatConfig = cfg
					safe.Catalog = &ChatCatalogResources{Pool: &catalogTestPool{}}
					safe.ProviderFactory = factory
					safe.Research = episode
					safe.PolicyRuntime.Classifier = BotCatalogueClassifier{}
					safe.PolicyRuntime.Mandate = req.Mandate
				}
				return safe, err
			}
			runtime, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Cleanup()
			// Search now runs inside the private tool-enabled worker, not on the
			// tools-less Employee. Resolve the shared search tool from a worker.
			worker, err := runtime.WorkerFactory(f.ctx, api.BotEmployeeWorkerRequest{Request: f.req, ConversationID: f.req.Run.SessionID.String(), Sessions: runtime.SessionService})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Cleanup()
			entry := worker.Agent.ToolIndex.GetToolEntry("perplexity_web_search")
			if entry == nil {
				t.Fatal("missing shared search")
			}
			actual, err := tools.NewPerplexityWebSearchTool(cfg, factory)
			if err != nil {
				t.Fatal(err)
			}
			schema, _ := json.Marshal(actual.(common.ToolWithDeclaration).Declaration())
			for _, field := range []string{"query", "max_results", "recency", "domains", "search_mode", "detail_level"} {
				if !strings.Contains(string(schema), `"`+field+`"`) {
					t.Fatalf("actual schema missing %s: %s", field, schema)
				}
			}
			args := map[string]any{"query": "Stock Market News overview"}
			if mode == "complete" {
				args = map[string]any{"query": "Stock Market News overview", "max_results": float64(2), "recency": "recent", "domains": []any{"example.com"}, "search_mode": "news", "detail_level": "detailed"}
			} else if mode == "live-eight" {
				args = map[string]any{"query": historicalAppleEightQuery, "max_results": 8, "recency": "week", "search_mode": "news", "detail_level": "detailed"}
			} else if mode == "live-six" {
				args = map[string]any{"query": historicalAppleSixQuery, "max_results": 6, "detail_level": "detailed"}
			} else if mode == "null" {
				for _, field := range []string{"max_results", "recency", "domains", "search_mode", "detail_level"} {
					args[field] = nil
				}
			}
			if mode == "live-eight" || mode == "live-six" {
				if !strings.Contains(args["query"].(string), "Apple Inc") {
					t.Fatal("historical live envelope was replaced by a Stock Market News reconstruction")
				}
			}
			invoke := func(a map[string]any) (map[string]any, error) {
				return entry.Tool.(chatRuntimeRunnable).Run(runtimeCapabilityToolContext{sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: agent.WithBotPrivateWorkerExecution(execution.WithBotRunIdentity(f.ctx, execution.BotRunIdentity{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Version.Version, ActorID: f.req.Bot.OwnerID.String(), OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}))}, sid: f.req.Run.SessionID.String()}}}, a)
			}
			originalBytes, _ := json.Marshal(args)
			originalAction := execution.ActionDescriptor{Tool: "perplexity_web_search", Effect: execution.EffectRead, Arguments: originalBytes, ConstraintsVerified: true}
			originalDigest, err := originalAction.Digest()
			if err != nil {
				t.Fatal(err)
			}
			out, err := invoke(args)
			afterBytes, _ := json.Marshal(args)
			afterAction := originalAction
			afterAction.Arguments = afterBytes
			afterDigest, digestErr := afterAction.Digest()
			if digestErr != nil || string(afterBytes) != string(originalBytes) || afterDigest != originalDigest {
				t.Fatal("effective dispatch mutated original action arguments/digest")
			}
			if mode == "live-eight" || mode == "live-six" {
				var effective map[string]any
				_ = json.Unmarshal(originalBytes, &effective)
				effective["max_results"] = 5
				boundedBytes, _ := json.Marshal(effective)
				boundedAction := originalAction
				boundedAction.Arguments = boundedBytes
				boundedDigest, _ := boundedAction.Digest()
				if boundedDigest == originalDigest || quota.m.PublicResearch.MaxResults != 5 || quota.m.PublicResearch.Subject != "Apple News" {
					t.Fatal("normalization changed saved grant or lost original over-ceiling action identity")
				}
				t.Logf("exact-historical-envelope=true bridge=configured_search/perplexity_web_search provenance=495.../52daa... SQL=36263/36268 requested=%v effective=5 original-digest-preserved=true", args["max_results"])
			}
			encoded, _ := json.Marshal(out)
			var observed tools.PerplexityWebSearchResult
			if decodeErr := json.Unmarshal(encoded, &observed); decodeErr != nil || len(observed.Citations) > 5 || len(observed.SearchResults) > 5 {
				t.Fatalf("returned evidence exceeds saved result grant: %+v decode=%v", observed, decodeErr)
			}
			if err != nil || search.calls != 1 || factoryCalls != 1 || quota.calls != 1 || budget.reserved != 1 || budget.settled != 1 || !strings.Contains(string(encoded), "Controlled sourced answer") {
				t.Fatalf("authorized actual dispatch: %s err=%v provider=%d factory=%d quota=%d budget=%d/%d", encoded, err, search.calls, factoryCalls, quota.calls, budget.reserved, budget.settled)
			}
			for _, bad := range []map[string]any{
				{"query": "private assignment"},
				{"query": "{{secret}}"},
				{"query": "Stock Market News", "max_results": 1.5},
				{"query": "Stock Market News", "max_results": "6"},
				{"query": "Stock Market News", "domains": []any{"{{credential}}"}},
				{"query": "Stock Market News", "domains": []any{"localhost"}},
				{"query": "Stock Market News", "max_results": -1},
				{"query": "Stock Market News", "external_url": "http://localhost"},
				{"query": "Stock Market News", "recency": []any{"recent"}},
				{"query": "Stock Market News", "domains": "example.com"},
				{"query": "Stock Market News", "facet": "unadmitted"},
			} {
				if _, err := invoke(bad); err == nil {
					t.Fatalf("unsafe arguments admitted: %v", bad)
				}
				if search.calls != 1 || factoryCalls != 1 || quota.calls != 1 {
					t.Fatal("rejection dispatched or spent research quota")
				}
			}
			quota.revoked = true
			if _, err := invoke(args); err == nil {
				t.Fatal("revocation bypass")
			}
			if search.calls != 1 {
				t.Fatal("revoked dispatch")
			}
		})
	}
}
