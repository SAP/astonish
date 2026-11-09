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
