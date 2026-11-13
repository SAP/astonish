package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// BotKnowledgeTools composes Chat's knowledge implementations with owner-only
// stores. Callers must still apply the normal Bot catalogue/policy wrappers.
// No local skills, process provider credentials, or filesystem flow stores are
// discovered here. The model factory is trusted dependency injection, not input.
func BotKnowledgeTools(r bot.RunRequest, scope *BotChatScope, factory tools.LLMFactory) ([]tool.Tool, error) {
	if scope == nil || scope.Services == nil || scope.Sessions == nil || r.Budget == nil {
		return nil, store.ErrBotUnsupported
	}
	svc := *scope.Services
	repo := svc.Bots
	flowStore := svc.Flows
	if r.Bot.Scope == store.BotScopePersonal {
		repo, flowStore = svc.PersonalBots, svc.PersonalFlows
	} else if r.Bot.Scope != store.BotScopeTeam {
		return nil, store.ErrBotInvalid
	}
	if repo == nil {
		return nil, store.ErrBotUnsupported
	}
	check := func(ctx context.Context) error {
		if ctx == nil {
			return store.ErrBotForbidden
		}
		if err := r.Budget.Check(ctx); err != nil {
			return err
		}
		id, ok := execution.BotRunIdentityFromContext(ctx)
		if !ok || id.BotID != r.Bot.ID || id.RunID != r.Run.ID || id.ActorID != r.Bot.OwnerID.String() || id.OrgSlug != r.Principal.OrgSlug || id.TeamSlug != r.Principal.TeamSlug || id.Version != r.Version.Version {
			return store.ErrBotForbidden
		}
		b, err := repo.Get(ctx, r.Bot.ID)
		if err != nil {
			return store.ErrBotForbidden
		}
		if b == nil || b.Status != store.BotActive || b.Version != r.Version.Version || b.OwnerID != r.Bot.OwnerID {
			return store.ErrBotForbidden
		}
		run, err := repo.GetRun(ctx, r.Bot.ID, r.Run.ID)
		if err != nil {
			return store.ErrBotForbidden
		}
		if run == nil || run.Status != store.RunRunning || run.SessionID != r.Run.SessionID || run.Lease.Fence != r.Run.Lease.Fence || run.Lease.WorkerID != r.Run.Lease.WorkerID || !time.Now().Before(run.Lease.ExpiresAt) {
			return store.ErrBotConflict
		}
		return nil
	}
	lookup := tools.SkillLookup(nil, tools.SkillLookupModePlatform)
	skill, err := functiontool.New(functiontool.Config{Name: "skill_lookup", Description: "Load a skill or bundled file from the Bot owner's team, organization, or platform skills."}, func(ctx tool.Context, a tools.SkillLookupArgs) (tools.SkillLookupResult, error) {
		if err := check(ctx); err != nil {
			return tools.SkillLookupResult{}, err
		}
		// Chat normally tolerates failed reads and tries inherited skills. Bots must
		// not cross a failed scope read. Pin the selected row before reusing Chat's
		// validation, file-path handling, content parser and manifest implementation.
		selected := &store.SkillStores{}
		for _, s := range []store.SkillStore{svc.TeamSkills, svc.Skills, svc.PlatformSkills} {
			if s == nil {
				continue
			}
			row, err := s.Get(ctx, strings.ToLower(strings.TrimSpace(a.Name)))
			if err != nil {
				return tools.SkillLookupResult{}, store.ErrBotForbidden
			}
			if row != nil {
				selected.Team = botKnowledgeSkillSnapshot{SkillStore: s, row: row}
				break
			}
		}
		// Do not enter Chat's static/filesystem fallback for unscoped names.
		if selected.Team == nil {
			return tools.SkillLookupResult{Error: "skill not found in Bot scope"}, nil
		}
		c := botKnowledgeContext{Context: ctx, values: store.WithSkillStores(ctx, selected)}
		result, lookupErr := lookup(c, a)
		if lookupErr != nil {
			return result, lookupErr
		}
		if err := check(ctx); err != nil {
			return tools.SkillLookupResult{}, err
		}
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	search, err := functiontool.New(functiontool.Config{Name: "perplexity_web_search", Description: "Search the web using the configured scoped Perplexity/Sonar provider and return sources."}, func(ctx tool.Context, a tools.PerplexityWebSearchArgs) (tools.PerplexityWebSearchResult, error) {
		if err := check(ctx); err != nil {
			return tools.PerplexityWebSearchResult{}, err
		}
		cfg, err := botKnowledgeSearchConfig(ctx, &svc)
		if err != nil {
			return tools.PerplexityWebSearchResult{}, err
		}
		name := cfg.PerplexityWebSearch.Provider
		pc, ok := cfg.Providers[name]
		if !ok || name == "" || cfg.PerplexityWebSearch.Model == "" || factory == nil || cfg.General.WebSearchTool != "perplexity:perplexity_web_search" {
			return tools.PerplexityWebSearchResult{}, store.ErrBotUnsupported
		}
		redactor := credentials.NewRedactor()
		// Register all configured values: provider-specific secret field names vary.
		// This over-redacts rather than allowing a provider error/echo to leak keys.
		for _, v := range pc {
			redactor.AddSecret("bot-search-provider", v)
		}
		result, err := tools.RunPerplexityWebSearch(ctx, a, cfg, func(c context.Context, p, m string, cfg *config.AppConfig) (model.LLM, error) {
			if err := check(c); err != nil {
				return nil, err
			}
			inner, err := factory(provider.WithConfinedProviderSecrets(c), p, m, cfg)
			if err != nil {
				return nil, store.ErrBotUnsupported
			}
			return provider.NewBudgetLLM(inner, r.Budget, provider.BudgetLLMOptions{MaxOutputTokens: 4096, SupportsOutputLimit: true})
		})
		if err != nil {
			return tools.PerplexityWebSearchResult{}, fmt.Errorf("Bot web search failed: %s", redactor.Redact(err.Error()))
		}
		if err := check(ctx); err != nil {
			return tools.PerplexityWebSearchResult{}, err
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return tools.PerplexityWebSearchResult{}, err
		}
		err = json.Unmarshal([]byte(redactor.Redact(string(raw))), &result)
		return result, err
	})
	if err != nil {
		return nil, err
	}
	distill, err := functiontool.New(functiontool.Config{Name: "distill_flow", Description: "Distill this Bot session into a reusable flow saved in the same owner scope."}, func(ctx tool.Context, _ tools.DistillFlowArgs) (tools.DistillFlowResult, error) {
		if err := check(ctx); err != nil {
			return tools.DistillFlowResult{}, err
		}
		if flowStore == nil || ctx.SessionID() != r.Run.SessionID.String() || ctx.UserID() != r.Bot.OwnerID.String() || ctx.AppName() != "astonish" {
			return tools.DistillFlowResult{}, store.ErrBotUnsupported
		}
		fs := &botKnowledgeFlowStore{FlowStore: flowStore, check: check}
		c := store.WithFlowStore(store.WithSessionService(ctx, nil), fs)
		chat := agent.NewChatAgent(nil, nil, nil, scope.Sessions, nil, false, false)
		chat.FlowDistiller = agent.NewFlowDistiller(nil, nil, nil, api.GetFlowSchema, botKnowledgeValidateFlow, func(y string, s map[string]json.RawMessage) agent.FlowDryRunResult {
			v := api.DryRunFlowYAML(y, s)
			return agent.FlowDryRunResult{Valid: v.Valid, Errors: v.Errors, Warnings: v.Warnings}
		})
		ds := agent.DistillSession{SessionID: r.Run.SessionID.String(), AppName: "astonish", UserID: r.Bot.OwnerID.String()}
		if _, err := chat.PreviewDistill(c, ds); err != nil {
			return tools.DistillFlowResult{}, err
		}
		var output strings.Builder
		if err := chat.ConfirmAndDistill(c, ds, func(s string) { output.WriteString(s) }); err != nil {
			return tools.DistillFlowResult{}, err
		}
		return tools.DistillFlowResult{ToolResult: tools.ToolResult{Status: "success", Message: output.String()}, FlowName: fs.saved}, nil
	})
	if err != nil {
		return nil, err
	}
	return []tool.Tool{skill, search, distill}, nil
}

type botSkillToolArgs struct {
	After                 string                    `json:"after,omitempty"`
	Limit                 int                       `json:"limit,omitempty"`
	SkillID               string                    `json:"skill_id,omitempty"`
	ExpectedRevision      int64                     `json:"expected_revision,omitempty"`
	Title                 string                    `json:"title,omitempty"`
	Method                []string                  `json:"method,omitempty"`
	OutputFormat          string                    `json:"output_format,omitempty"`
	EvaluationRules       []string                  `json:"evaluation_rules,omitempty"`
	EvidenceRefs          []string                  `json:"evidence_refs,omitempty"`
	OwnerQuotes           []string                  `json:"owner_quotes,omitempty"`
	OwnerFeedbackScope    bot.BotOwnerFeedbackScope `json:"owner_feedback_scope,omitempty"`
	ReplaceExpectationIDs []string                  `json:"replace_expectation_ids,omitempty"`
}

// Bot-owned methods never shadow ordinary skill_lookup or expand authority.
func newBotSkillTools(binding *botFactoryBinding, scope *BotChatScope) ([]tool.Tool, error) {
	if binding == nil || bot.BotWorkServicesFromContext(binding.owner) == nil {
		return nil, nil
	}
	repo := scope.Services.Bots
	if binding.req.Bot.Scope == store.BotScopePersonal {
		repo = scope.Services.PersonalBots
	}
	skills, ok := repo.(store.BotSkillStore)
	if !ok {
		return nil, nil
	}
	var out []tool.Tool
	for _, name := range []string{"bot_skill_list", "bot_skill_read", "bot_skill_use", "bot_skill_save"} {
		name := name
		t, err := functiontool.New(functiontool.Config{Name: name, Description: "Optional Bot-owned reusable methods. list/read inspect; list accepts limit (default 20, maximum 100) and after (the previous result's next cursor) to discover later pages; use pins a method; save proposes advisory learning, not permission or verified truth. expected_revision comes from bot_work_read. Evidence refs come from captured tools. No skill is required to answer. Owner corrections require skill_id before selecting a method, owner_feedback_scope=skill, exact current owner_quotes including the owner's durable intent, and replace_expectation_ids from bot_skill_read. scope=run is one-off and is never saved. Do not combine corrections with method changes."}, func(c tool.Context, a botSkillToolArgs) (map[string]any, error) {
			if c.SessionID() != binding.req.Run.SessionID.String() && (name == "bot_skill_use" || name == "bot_skill_save") {
				return nil, store.ErrBotForbidden
			}
			ctx := binding.bind(c)
			if err := binding.check(ctx); err != nil {
				return nil, err
			}
			svc := bot.BotWorkServicesFromContext(ctx)
			if svc == nil {
				return botWorkToolResult(c, nil, store.ErrBotUnsupported)
			}
			switch name {
			case "bot_skill_list":
				limit := a.Limit
				if limit == 0 {
					limit = 20
				}
				if limit < 1 || limit > store.BotSkillMaxPageSize {
					return botWorkToolResult(c, nil, store.ErrBotInvalid)
				}
				rows, err := skills.ListSkills(ctx, binding.req.Bot.ID, store.BotSkillPage{Limit: limit, After: a.After})
				return botWorkToolResult(c, rows, err)
			case "bot_skill_read":
				id, err := uuid.Parse(a.SkillID)
				if err != nil {
					return botWorkToolResult(c, nil, store.ErrBotInvalid)
				}
				head, err := skills.GetSkill(ctx, binding.req.Bot.ID, id)
				if err != nil {
					return botWorkToolResult(c, nil, err)
				}
				rev, err := skills.GetSkillRevision(ctx, binding.req.Bot.ID, id, head.HeadRevision)
				return botWorkToolResult(c, rev, err)
			case "bot_skill_use":
				id, err := uuid.Parse(a.SkillID)
				if err != nil {
					return botWorkToolResult(c, nil, store.ErrBotInvalid)
				}
				inv, err := svc.SelectSkill(ctx, bot.BotWorkSkillSelection{SkillID: id, ExpectedRevision: a.ExpectedRevision, CallID: c.FunctionCallID()})
				return botWorkToolResult(c, inv, err)
			default:
				var id uuid.UUID
				if a.SkillID != "" {
					var err error
					id, err = uuid.Parse(a.SkillID)
					if err != nil {
						return botWorkToolResult(c, nil, store.ErrBotInvalid)
					}
				}
				err := svc.StageLearning(ctx, bot.BotWorkLearningProposal{SkillID: id, OwnerFeedbackScope: a.OwnerFeedbackScope, OwnerQuotes: a.OwnerQuotes, ReplaceExpectationIDs: a.ReplaceExpectationIDs, CallID: c.FunctionCallID(), ExpectedRevision: a.ExpectedRevision, Title: a.Title, Method: a.Method, OutputFormat: a.OutputFormat, EvaluationRules: a.EvaluationRules, EvidenceRefs: a.EvidenceRefs})
				if len(a.OwnerQuotes) > 0 || a.OwnerFeedbackScope != "" || len(a.ReplaceExpectationIDs) > 0 {
					return botWorkToolResult(c, map[string]any{"staged": false, "saved": err == nil, "message": "Owner expectation correction only; saved=true means the scoped transaction committed. Method and original authority are unchanged."}, err)
				}
				return botWorkToolResult(c, map[string]any{"staged": err == nil, "message": "Proposal only; terminal commit determines the saved outcome."}, err)
			}
		})
		if err != nil {
			return nil, err
		}
		t, err = newBotOptionalSchemaTool(t, binding, name == "bot_skill_use" || name == "bot_skill_save")
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Validate only before executing the optional handler. Never classify an error
// returned by execution as a schema error: it could be authorization, cancellation
// or budget exhaustion. The normal outer policy/budget wrapper is unchanged.
type botOptionalSchemaTool struct {
	botRunnableTool
	binding  *botFactoryBinding
	mutation bool
	schema   *jsonschema.Resolved
}

func newBotOptionalSchemaTool(t tool.Tool, binding *botFactoryBinding, mutation bool) (tool.Tool, error) {
	runnable, ok := t.(botRunnableTool)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	schema, ok := runnable.Declaration().ParametersJsonSchema.(*jsonschema.Schema)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, err
	}
	return &botOptionalSchemaTool{botRunnableTool: runnable, binding: binding, mutation: mutation, schema: resolved}, nil
}

func (t *botOptionalSchemaTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil || t.mutation && ctx.SessionID() != t.binding.req.Run.SessionID.String() {
		return nil, store.ErrBotForbidden
	}
	if err := t.binding.check(t.binding.bind(ctx)); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(args)
	var normalized any
	if err == nil {
		err = json.Unmarshal(raw, &normalized)
	}
	if err == nil {
		err = t.schema.Validate(normalized)
	}
	if err == nil {
		// Schema-valid strings/numbers can still fail Go decoding (UUIDs or
		// integer overflow). Predecode only the optional tool's declared input;
		// never recover errors returned by an executing handler.
		var input any
		switch t.Name() {
		case botWorkReadTool:
			input = &botWorkReadArgs{}
		case botWorkUpdateTool:
			input = &botWorkUpdateArgs{}
		case botEvidenceReadTool:
			input = &botEvidenceReadArgs{}
		case botCheckWorkTool:
			input = &botCheckWorkArgs{}
		case "bot_skill_list", "bot_skill_read", "bot_skill_use", "bot_skill_save":
			input = &botSkillToolArgs{}
		}
		if input != nil {
			err = json.Unmarshal(raw, input)
		}
	}
	if err != nil {
		return botWorkToolResult(ctx, map[string]any{"staged": false, "message": "Optional request was not executed. Use the declared argument schema and retry only if useful; otherwise continue the assignment."}, store.ErrBotInvalid)
	}
	return t.botRunnableTool.Run(ctx, args)
}

type botKnowledgeContext struct {
	tool.Context
	values context.Context
}

func (c botKnowledgeContext) Value(k any) any { return c.values.Value(k) }

type botKnowledgeSkillSnapshot struct {
	store.SkillStore
	row *store.Skill
}

func (s botKnowledgeSkillSnapshot) Get(context.Context, string) (*store.Skill, error) {
	return s.row, nil
}

type botKnowledgeFlowStore struct {
	store.FlowStore
	check func(context.Context) error
	saved string
}

func (s *botKnowledgeFlowStore) SaveFlow(ctx context.Context, name, y string) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	if v := botKnowledgeValidateFlow(y, nil); !v.Valid {
		return fmt.Errorf("%w: invalid distilled flow", store.ErrBotInvalid)
	}
	if v := api.DryRunFlowYAML(y, nil); !v.Valid {
		return fmt.Errorf("%w: distilled flow failed validation", store.ErrBotInvalid)
	}
	if r := credentials.RedactorFromContext(ctx); r != nil {
		y, _ = r.Placeholderize(y)
	}
	if err := s.FlowStore.SaveFlow(ctx, name, y); err != nil {
		return err
	}
	s.saved = name
	return nil
}
func botKnowledgeValidateFlow(y string, _ []agent.DistillerToolInfo) agent.FlowValidationResult {
	var infos []api.ToolInfo
	for _, d := range tools.GetAllFlowToolDeclarations() {
		infos = append(infos, api.ToolInfo{Name: d.Name, Description: d.Description})
	}
	v := api.ValidateFlowYAML(y, infos)
	return agent.FlowValidationResult{Valid: v.Valid, Errors: v.Errors}
}

// Read every settings layer once and fail closed; reuse the Chat cascade over
// immutable snapshots so its tolerant-error behavior cannot change credentials.
func botKnowledgeSearchConfig(ctx context.Context, svc *store.Services) (*config.AppConfig, error) {
	if svc.PlatformSettings == nil || svc.OrgSettings == nil || svc.Settings == nil {
		return nil, store.ErrBotUnsupported
	}
	p, e := svc.PlatformSettings.Get(ctx)
	if e != nil {
		return nil, store.ErrBotForbidden
	}
	o, e := svc.OrgSettings.Get(ctx)
	if e != nil {
		return nil, store.ErrBotForbidden
	}
	t, e := svc.Settings.Get(ctx)
	if e != nil {
		return nil, store.ErrBotForbidden
	}
	cfg := provider.ResolveEffectiveConfig(ctx, botKnowledgePlatformSettings{row: p}, botKnowledgeOrgSettings{row: o}, botKnowledgeTeamSettings{row: t})
	for n, pc := range cfg.Providers {
		clone := config.ProviderConfig{}
		for k, v := range pc {
			clone[k] = v
		}
		cfg.Providers[n] = clone
	}
	return cfg, nil
}

type botKnowledgePlatformSettings struct {
	store.PlatformSettingsStore
	row *store.PlatformSettings
}

func (s botKnowledgePlatformSettings) Get(context.Context) (*store.PlatformSettings, error) {
	return s.row, nil
}

type botKnowledgeOrgSettings struct {
	store.OrgSettingsStore
	row *store.OrgSettings
}

func (s botKnowledgeOrgSettings) Get(context.Context) (*store.OrgSettings, error) { return s.row, nil }

type botKnowledgeTeamSettings struct {
	store.SettingsStore
	row *store.TeamSettings
}

func (s botKnowledgeTeamSettings) Get(context.Context) (*store.TeamSettings, error) {
	return s.row, nil
}
