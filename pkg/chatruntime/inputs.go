// Package chatruntime assembles request-local Chat capabilities independently of transport.
package chatruntime

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/a2aclient"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/skills"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"reflect"
	"sync"
)

type FallbackPolicy uint8

const (
	FallbackForbidden FallbackPolicy = iota
	FallbackAllowed
)

type ResourceScope string

const (
	ResourceScopePersonal ResourceScope = "personal"
	ResourceScopeTeam     ResourceScope = "team"
)

// BuildOptions contains already-authenticated identity and explicitly scoped dependencies.
// FallbackForbidden never consults host credentials, config, or tool caches.
type BuildOptions struct {
	Principal              execution.Principal
	Services               *store.Services
	ResourceScope          ResourceScope
	MemoryScope            store.MemoryScope
	AppConfig              *config.AppConfig
	MCPConfig              *config.MCPConfig
	SessionService         store.SessionStore
	LLM                    model.LLM
	Pool                   sandbox.ToolNodePool
	FilesystemSkills       []skills.Skill
	CredentialResolver     credentials.CredentialResolver
	Redactor               *credentials.Redactor
	MemoryDeleteAuthorizer store.MemoryDeleteAuthorizer
	MemorySaveOrMerge      store.MemorySaveOrMergeFunc
	RunJob                 store.RunJobFunc
	Templates              SandboxSelectors
	FallbackPolicy         FallbackPolicy
	Debug                  bool
}

// Inputs owns a resolved snapshot. Bind never detaches cancellation or replaces a
// caller's Bot run policy/budget; shared stores are interfaces, not copied databases.
type Inputs struct {
	options     BuildOptions
	services    *store.Services
	memory      store.MemoryStore
	memoryScope store.MemoryScope
	memories    store.MemoryStoresByScope
	credential  store.CredentialStore
	flow        store.FlowStore
	groups      map[string]*agent.ToolGroup
	skillIndex  string
	sandbox     sandboxSelection
	disabled    []string
	once        sync.Once
}

// Build is the compatibility name for BuildScopedChatRuntimeInputs.
func Build(ctx context.Context, o BuildOptions) (*Inputs, error) {
	return BuildScopedChatRuntimeInputs(ctx, o)
}

func BuildScopedChatRuntimeInputs(ctx context.Context, o BuildOptions) (*Inputs, error) {
	if err := o.Principal.Validate(); err != nil {
		return nil, err
	}
	if o.ResourceScope != ResourceScopePersonal && o.ResourceScope != ResourceScopeTeam {
		return nil, fmt.Errorf("explicit resource scope is required")
	}
	if p, ok := execution.PrincipalFromContext(ctx); ok && !reflect.DeepEqual(p, o.Principal) {
		return nil, fmt.Errorf("runtime principal mismatch")
	}
	if tc := store.TenantContextFrom(ctx); tc != nil && (tc.OrgSlug != o.Principal.OrgSlug || tc.TeamSlug != o.Principal.TeamSlug) {
		return nil, fmt.Errorf("runtime tenant mismatch")
	}
	if o.FallbackPolicy != FallbackAllowed && o.FallbackPolicy != FallbackForbidden {
		return nil, fmt.Errorf("invalid fallback policy")
	}
	if o.Services == nil && o.FallbackPolicy == FallbackForbidden {
		return nil, fmt.Errorf("scoped services required")
	}
	o.Principal.Roles = append([]string(nil), o.Principal.Roles...)
	o.Principal.Scopes = append([]string(nil), o.Principal.Scopes...)
	// JSON config contains only serializable configuration. Snapshot maps/slices so
	// late changes to shared configuration cannot retarget this runtime.
	var err error
	o.AppConfig, err = cloneConfig(o.AppConfig)
	if err != nil {
		return nil, err
	}
	o.MCPConfig, err = cloneConfig(o.MCPConfig)
	if err != nil {
		return nil, err
	}
	if o.FallbackPolicy == FallbackForbidden && o.AppConfig != nil {
		_, server, _ := parseWebTool(o.AppConfig.General.WebSearchTool)
		if server == "perplexity" {
			return nil, fmt.Errorf("strict Perplexity web search requires a scoped provider factory")
		}
	}
	in := &Inputs{options: o, memoryScope: o.MemoryScope}
	if in.memoryScope == "" {
		in.memoryScope = store.MemoryScopeTeam
	}
	if in.memoryScope != store.MemoryScopeTeam && in.memoryScope != store.MemoryScopePersonal {
		return nil, fmt.Errorf("unsupported memory scope")
	}
	if o.ResourceScope == ResourceScopeTeam && in.memoryScope == store.MemoryScopePersonal {
		return nil, fmt.Errorf("personal memory is outside team resource scope")
	}
	if o.Services != nil {
		s := *o.Services
		if o.ResourceScope == ResourceScopeTeam {
			// Tools also consume Services directly (docs/apps, for example).
			// A team runtime must not retain the caller's private capabilities.
			s.PersonalCredentials = nil
			s.PersonalFlows = nil
			s.PersonalDocs = nil
			s.PersonalApps = nil
			s.PersonalSessions = nil
			s.PersonalScheduler = nil
			s.PersonalSettings = nil
			s.PersonalBots = nil
			s.MemorySearcher = nil // may contain a personal tier
			s.TenantRouter = nil   // team tools must not route to arbitrary users
		}
		in.services = &s
		in.options.Services = nil
		in.memory = s.Memory
		if o.ResourceScope == ResourceScopePersonal {
			if s.PersonalCredentials != nil || s.Credentials != nil {
				in.credential = store.NewMergedCredentialStore(s.PersonalCredentials, s.Credentials)
			}
			if s.PersonalFlows != nil || s.Flows != nil {
				in.flow = store.NewCompositeFlowStore(s.PersonalFlows, s.Flows)
			}
		} else {
			in.credential = s.Credentials
			in.flow = s.Flows
		}
		if o.ResourceScope == ResourceScopePersonal && s.TenantRouter != nil && o.Principal.Subject != "" && o.Principal.Kind != execution.PrincipalKindService {
			org, e := s.TenantRouter.ForOrg(o.Principal.OrgSlug)
			if e != nil && o.FallbackPolicy == FallbackForbidden {
				return nil, fmt.Errorf("resolve scoped memory: %w", e)
			}
			if e == nil && org != nil {
				in.memories = store.MemoryStoresByScope{Personal: org.ForUser(o.Principal.Subject).Memories(), Team: s.Memory, Org: org.OrgMemories()}
				if in.memoryScope == store.MemoryScopePersonal {
					in.memory = in.memories.Personal
				}
			}
		}
		if in.memoryScope == store.MemoryScopePersonal && in.memories.Personal == nil {
			if o.FallbackPolicy == FallbackForbidden {
				return nil, fmt.Errorf("personal memory unavailable")
			}
			in.memoryScope = store.MemoryScopeTeam
		}
		if s.Mode == store.ModePlatform || o.FallbackPolicy == FallbackForbidden {
			in.skillIndex, err = buildMergedSkillIndex(ctx, o.FilesystemSkills, s.PlatformSkills, s.Skills, s.TeamSkills, o.FallbackPolicy)
			if err != nil {
				return nil, fmt.Errorf("scoped skills: %w", err)
			}
		}
		if s.Settings != nil {
			settings, e := s.Settings.Get(ctx)
			if e != nil && o.FallbackPolicy == FallbackForbidden {
				return nil, e
			}
			if settings != nil {
				in.disabled = append([]string(nil), settings.DisabledTools...)
				in.sandbox.template = settings.TemplateName
			}
		}
	}
	if in.credential == nil && o.FallbackPolicy == FallbackForbidden {
		in.credential = unavailableCredentials{}
		in.services.Credentials = in.credential
	}
	if in.credential != nil {
		o.CredentialResolver = credentials.NewStoreAdapter(in.credential)
		if o.Redactor != nil {
			o.Redactor.HydrateFromStore(in.credential)
		}
	} else if o.FallbackPolicy == FallbackForbidden {
		o.CredentialResolver = nil
	}
	if o.FallbackPolicy == FallbackForbidden {
		ctx = capabilityContext{ctx}
		ctx = store.WithServices(ctx, in.services)
		ctx = store.WithCredentialStore(ctx, in.credential)
		ctx, err = execution.WithPrincipal(ctx, o.Principal)
		if err != nil {
			return nil, err
		}
	}
	if o.MCPConfig == nil {
		o.MCPConfig, err = LoadMCPConfigChecked(ctx, in.services, o.AppConfig, o.FallbackPolicy)
		if err != nil {
			return nil, err
		}
	}
	if o.FallbackPolicy == FallbackForbidden {
		for name, server := range o.MCPConfig.MCPServers {
			if server.IsEnabled() && server.Command != "" && o.Pool == nil {
				return nil, fmt.Errorf("MCP server %q requires scoped sandbox pool", name)
			}
		}
	}
	in.groups, err = BuildMCPToolGroupsChecked(ctx, in.services, o.MCPConfig, o.CredentialResolver, o.Pool, o.Debug, o.FallbackPolicy)
	if err != nil {
		return nil, err
	}
	if in.groups == nil {
		in.groups = map[string]*agent.ToolGroup{}
	}
	if s := in.services; s != nil {
		var ts []tool.Tool
		if o.FallbackPolicy == FallbackForbidden {
			ts, err = strictA2ATools(ctx, s, o.CredentialResolver)
			if err != nil {
				return nil, err
			}
		} else {
			ts = a2aclient.GetA2AToolsFromStores(ctx, &store.A2AAgentStores{Platform: s.PlatformA2AAgents, Org: s.A2AAgents, Team: s.TeamA2AAgents})
		}
		if len(ts) > 0 {
			in.groups["a2a"] = &agent.ToolGroup{Name: "a2a", Description: "Remote A2A agent skills (invoke external agents via the A2A protocol)", Tools: ts}
		}
	}
	in.sandbox = resolveSandbox(ctx, in.sandbox.template, o.Templates)
	return in, nil
}

func (in *Inputs) Bind(ctx context.Context) (context.Context, error) {
	if p, ok := execution.PrincipalFromContext(ctx); ok && !reflect.DeepEqual(p, in.options.Principal) {
		return nil, fmt.Errorf("runtime principal mismatch")
	}
	if tc := store.TenantContextFrom(ctx); tc != nil && (tc.OrgSlug != in.options.Principal.OrgSlug || tc.TeamSlug != in.options.Principal.TeamSlug) {
		return nil, fmt.Errorf("runtime tenant mismatch")
	}
	var err error
	if in.options.FallbackPolicy == FallbackForbidden {
		ctx = capabilityContext{ctx}
	}
	ctx, err = execution.WithPrincipal(ctx, in.options.Principal)
	if err != nil {
		return nil, err
	}
	o := in.options
	if s := in.services; s != nil {
		copy := *s
		ctx = store.WithServices(ctx, &copy)
		if in.credential != nil {
			ctx = store.WithCredentialStore(ctx, in.credential)
		}
		if in.memory != nil {
			ctx = store.WithMemoryStore(ctx, in.memory)
		}
		ctx = store.WithMemoryScope(ctx, in.memoryScope)
		if s.MemorySearcher != nil {
			ctx = store.WithThreeTierSearcher(ctx, s.MemorySearcher)
		}
		if in.memories.Personal != nil {
			ctx = store.WithMemoryStoresByScope(ctx, in.memories)
		}
		if in.flow != nil {
			ctx = store.WithFlowStore(ctx, in.flow)
		}
		if s.Flows != nil {
			ctx = store.WithTeamFlowStore(ctx, s.Flows)
		}
		if s.DrillReports != nil {
			ctx = store.WithDrillReportStore(ctx, s.DrillReports)
		}
		if s.Mode == store.ModePlatform {
			ctx = store.WithSkillStores(ctx, &store.SkillStores{Platform: s.PlatformSkills, Org: s.Skills, Team: s.TeamSkills})
		}
		ctx = store.WithMCPServerStores(ctx, &store.MCPServerStores{Platform: s.PlatformMCPServers, Org: s.MCPServers, Team: s.TeamMCPServers})
		ctx = store.WithA2AAgentStores(ctx, &store.A2AAgentStores{Platform: s.PlatformA2AAgents, Org: s.A2AAgents, Team: s.TeamA2AAgents})
		ctx = store.WithNetworkPolicyStores(ctx, &store.NetworkPolicyStores{Platform: s.PlatformNetworkPolicies, Org: s.NetworkPolicies, Team: s.TeamNetworkPolicies})
		if s.Scheduler != nil {
			ctx = store.WithSchedulerStore(ctx, s.Scheduler)
		}
		if o.ResourceScope == ResourceScopePersonal && s.PersonalScheduler != nil {
			ctx = store.WithPersonalSchedulerStore(ctx, s.PersonalScheduler)
		}
		if s.FleetTemplates != nil {
			ctx = store.WithFleetTemplateStore(ctx, s.FleetTemplates)
		}
		if s.FleetPlans != nil {
			ctx = store.WithFleetPlanStore(ctx, s.FleetPlans)
		}
		if s.FleetSetupProfiles != nil {
			ctx = store.WithFleetSetupProfileStore(ctx, s.FleetSetupProfiles)
		}
		if s.FleetSetupDrafts != nil {
			ctx = store.WithFleetSetupDraftStore(ctx, s.FleetSetupDrafts)
		}
		if o.ResourceScope == ResourceScopePersonal && s.PersonalSessions != nil {
			ctx = store.WithSessionService(ctx, s.PersonalSessions)
		}
	}
	if o.SessionService != nil {
		ctx = store.WithSessionService(ctx, o.SessionService)
	}
	if o.LLM != nil {
		ctx = agent.WithLLM(ctx, o.LLM)
	}
	if o.Redactor != nil {
		ctx = credentials.WithRedactor(ctx, o.Redactor)
	}
	if o.MemoryDeleteAuthorizer != nil {
		ctx = store.WithMemoryDeleteAuthorizer(ctx, o.MemoryDeleteAuthorizer)
	}
	if o.MemorySaveOrMerge != nil {
		ctx = store.WithMemorySaveOrMerge(ctx, o.MemorySaveOrMerge)
	}
	if o.RunJob != nil {
		ctx = store.WithRunJobFunc(ctx, o.RunJob)
	}
	ctx = store.WithUserID(ctx, o.Principal.Subject)
	ctx = store.WithOrgSlug(ctx, o.Principal.OrgSlug)
	ctx = store.WithTeamSlug(ctx, o.Principal.TeamSlug)
	if len(in.disabled) > 0 {
		ctx = store.WithDisabledTools(ctx, append([]string(nil), in.disabled...))
	}
	ctx = in.sandbox.bind(ctx)
	ctx = agent.WithRequestMCPGroups(ctx, in.RequestToolGroups())
	var po agent.PromptOverrides
	if p := agent.PromptOverridesFromContext(ctx); p != nil {
		po = *p
		po.PinnedToolGroups = append([]string(nil), p.PinnedToolGroups...)
		po.AdditionalTools = append([]tool.Tool(nil), p.AdditionalTools...)
	}
	if in.skillIndex != "" {
		po.SkillIndex = in.skillIndex
	}
	if g := in.groups["a2a"]; g != nil {
		po.SessionContext += "\n\n## A2A Agents (deferred tools)\n\nThe following remote A2A agent tools are configured. Inspect them with `describe_tools`, then invoke them with `execute_tool` using the returned reference:\n"
		for _, t := range g.Tools {
			po.SessionContext += "- `" + t.Name() + "`\n"
		}
		po.SessionContext += "\nCall them with a `message` argument describing what you need. They connect to external AI agents via the A2A protocol.\n"
		po.PinnedToolGroups = append(po.PinnedToolGroups, "a2a")
	}
	ctx = agent.WithPromptOverrides(ctx, &po)
	return WithWebSearchContext(ctx, o.AppConfig), nil
}

func (in *Inputs) RequestToolGroups() map[string]*agent.ToolGroup {
	out := make(map[string]*agent.ToolGroup, len(in.groups))
	for k, g := range in.groups {
		c := *g
		c.Tools = append([]tool.Tool(nil), g.Tools...)
		c.Toolsets = append([]tool.Toolset(nil), g.Toolsets...)
		out[k] = &c
	}
	return out
}

// Close is idempotent. Inputs does not own shared pools, LLMs, or stores.
func (in *Inputs) Close() error { in.once.Do(func() {}); return nil }
