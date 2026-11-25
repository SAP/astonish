package launcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

type BotConfiguredSearchRoute = execution.BotConfiguredSearchRoute

// BotEffectiveConfiguredSearchRoute resolves exactly Chat's effective selection.
// Fingerprints include configuration/credential changes but expose no values.
func BotEffectiveConfiguredSearchRoute(ctx context.Context, svc *store.Services) (BotConfiguredSearchRoute, error) {
	cfg, err := botKnowledgeSearchConfig(ctx, svc)
	if err != nil {
		return BotConfiguredSearchRoute{}, err
	}
	return botConfiguredSearchRoute(ctx, svc, cfg)
}
func botConfiguredSearchRoute(ctx context.Context, svc *store.Services, cfg *config.AppConfig) (BotConfiguredSearchRoute, error) {
	if cfg == nil || svc == nil {
		return BotConfiguredSearchRoute{}, store.ErrBotUnsupported
	}
	selected := cfg.General.WebSearchTool
	if selected == "perplexity:perplexity_web_search" {
		p, m := cfg.PerplexityWebSearch.Provider, cfg.PerplexityWebSearch.Model
		pc := cfg.Providers[p]
		if p == "" || m == "" || len(pc) == 0 {
			return BotConfiguredSearchRoute{}, fmt.Errorf("configured search unavailable: configure scoped Perplexity provider/model and credentials")
		}
		raw, _ := json.Marshal(struct {
			Provider string
			Model    string
			Config   config.ProviderConfig
			Search   any
		}{p, m, pc, cfg.PerplexityWebSearch})
		sum := sha256.Sum256(raw)
		keys := make([]string, 0, len(pc))
		for k := range pc {
			keys = append(keys, "provider:"+p+":"+k)
		}
		sort.Strings(keys)
		return BotConfiguredSearchRoute{Engine: execution.BotSearchPerplexity, Provider: p, Model: m, Tool: BotConfiguredSearchToolName, Fingerprint: "provider-config-v1:" + hex.EncodeToString(sum[:]), CredentialHandles: keys}, nil
	}
	parts := strings.Split(selected, ":")
	if len(parts) != 2 || parts[0] == "" {
		return BotConfiguredSearchRoute{}, fmt.Errorf("configured search unavailable: select a supported scoped search executable")
	}
	engine, err := execution.BotSearchEngineForTool(parts[1])
	if err != nil || !engine.IsMCP() {
		return BotConfiguredSearchRoute{}, fmt.Errorf("configured search unavailable: unsupported selected executable")
	}
	var current *store.MCPServer
	for _, s := range []store.MCPServerStore{svc.MCPServers, svc.TeamMCPServers} {
		if s == nil {
			continue
		}
		rows, e := s.List(ctx)
		if e != nil {
			return BotConfiguredSearchRoute{}, e
		}
		for _, row := range rows {
			if row.Name == parts[0] {
				v := row
				current = &v
			}
		}
	}
	if current == nil {
		current, _, err = botSelectedStandardSearch(ctx, svc, cfg)
		if err != nil {
			return BotConfiguredSearchRoute{}, err
		}
	}
	if current == nil || !current.IsEnabled() {
		return BotConfiguredSearchRoute{}, fmt.Errorf("configured MCP search executable unavailable")
	}
	handles := []string{}
	for _, v := range current.Env {
		if botSymbolicSearchCredential(v) {
			handles = append(handles, v)
		} else if v != "" {
			return BotConfiguredSearchRoute{}, fmt.Errorf("configured MCP search requires symbolic credential environment")
		}
	}
	sort.Strings(handles)
	if len(handles) == 0 {
		return BotConfiguredSearchRoute{}, fmt.Errorf("configured MCP search credentials unavailable")
	}
	return BotConfiguredSearchRoute{Engine: engine, Tool: "mcp:" + parts[0] + "/" + parts[1], Fingerprint: botMCPFingerprint(botMCPConfig(*current)), CredentialHandles: handles}, nil
}

// botSelectedStandardSearch mirrors Chat's selected standard-server construction,
// but uses only injected scoped stores. Raw scoped keys become run-local named
// credentials; no global getter, persistent writes, or credential-bearing metadata.
func botSelectedStandardSearch(ctx context.Context, svc *store.Services, cfg *config.AppConfig) (*store.MCPServer, store.CredentialStore, error) {
	if svc == nil || cfg == nil {
		return nil, nil, store.ErrBotUnsupported
	}
	parts := strings.Split(cfg.General.WebSearchTool, ":")
	if len(parts) != 2 {
		return nil, nil, store.ErrBotUnsupported
	}
	standard := config.GetStandardServer(parts[0])
	if standard == nil || standard.Kind == "model" || standard.Category != "web" || standard.WebSearchTool != cfg.General.WebSearchTool {
		return nil, nil, store.ErrBotUnsupported
	}
	cs := store.CredentialStoreFromContext(ctx)
	if cs == nil {
		cs = svc.Credentials
	}
	if cs == nil {
		return nil, nil, store.ErrBotForbidden
	}
	key := cs.GetSecret(ctx, "web_servers."+standard.ID+".api_key")
	if key == "" {
		key = cfg.WebServers[standard.ID].APIKey
	}
	// Provider resolution intentionally omits web-server keys. Read the same
	// scoped settings resource Chat uses for standard-server installation.
	if key == "" && svc.Settings != nil {
		settings, err := svc.Settings.Get(ctx)
		if err != nil {
			return nil, nil, store.ErrBotForbidden
		}
		if settings != nil {
			key = settings.WebServers[standard.ID]["api_key"]
		}
	}
	if key == "" || strings.ContainsAny(key, "\r\n\x00") {
		return nil, nil, fmt.Errorf("configured standard search credentials unavailable")
	}
	if botSymbolicSearchCredential(key) {
		inner := strings.TrimSuffix(strings.TrimPrefix(key, "{{CREDENTIAL:"), "}}")
		if cs.Get(ctx, strings.Split(inner, ":")[0]) == nil {
			return nil, nil, fmt.Errorf("configured standard search credentials unavailable")
		}
	} else {
		sum := sha256.Sum256([]byte(standard.ID + "\x00" + key))
		name := "bot-standard-" + hex.EncodeToString(sum[:])
		cs = &botStandardSearchCredentials{CredentialStore: cs, named: sessioncreds.NewStore(map[string]*store.Credential{name: {Type: store.CredAPIKey, Value: key}})}
		key = "{{CREDENTIAL:" + name + ":value}}"
	}
	built := config.BuildMCPServerConfig(standard, key)
	return &store.MCPServer{Name: standard.ID, Command: built.Command, Args: append([]string(nil), built.Args...), Env: built.Env, Transport: built.Transport}, cs, nil
}

type botStandardSearchCredentials struct {
	store.CredentialStore
	named *sessioncreds.Store
}

func (s *botStandardSearchCredentials) Get(ctx context.Context, name string) *store.Credential {
	if c := s.named.Get(ctx, name); c != nil {
		return c
	}
	return s.CredentialStore.Get(ctx, name)
}
func (s *botStandardSearchCredentials) List(ctx context.Context) map[string]store.CredentialType {
	out := map[string]store.CredentialType{}
	for n, typ := range s.CredentialStore.List(ctx) {
		out[n] = typ
	}
	for n, typ := range s.named.List(ctx) {
		out[n] = typ
	}
	return out
}

type botStandardSearchStore struct {
	store.MCPServerStore
	services *store.Services
}

func (s botStandardSearchStore) List(ctx context.Context) ([]store.MCPServer, error) {
	cfg, err := botKnowledgeSearchConfig(ctx, s.services)
	if err != nil {
		return nil, err
	}
	row, _, err := botSelectedStandardSearch(ctx, s.services, cfg)
	if err != nil {
		return nil, err
	}
	return []store.MCPServer{*row}, nil
}

type botPublicResearchArgs struct {
	Facet       execution.BotResearchFacet `json:"facet,omitempty" jsonschema:"Admitted public research facet: overview, recent, primary, contradictions."`
	Query       string                     `json:"query,omitempty" jsonschema:"Optional specific public search query (for example, a specific company, ticker, or topic within the assignment)."`
	MaxResults  int                        `json:"max_results,omitempty"`
	Recency     string                     `json:"recency,omitempty"`
	Domains     []string                   `json:"domains,omitempty"`
	SearchMode  string                     `json:"search_mode,omitempty"`
	DetailLevel string                     `json:"detail_level,omitempty"`
}

// effectiveSearchArgs is recomputed under the current grant; original invocation
// bytes remain untouched for action digest and exact-approval matching.
func (a botPublicResearchArgs) effectiveSearchArgs(ceiling int) (tools.PerplexityWebSearchArgs, error) {
	out := tools.PerplexityWebSearchArgs{Query: a.Query, MaxResults: a.MaxResults, Recency: a.Recency, Domains: append([]string(nil), a.Domains...), SearchMode: a.SearchMode, DetailLevel: a.DetailLevel}
	if ceiling <= 0 || out.MaxResults < 0 {
		return out, store.ErrBotInvalid
	}
	if out.MaxResults == 0 || out.MaxResults > ceiling {
		out.MaxResults = ceiling
	}
	allowed := func(v string, values ...string) bool {
		if v == "" {
			return true
		}
		for _, value := range values {
			if v == value {
				return true
			}
		}
		return false
	}
	if !allowed(out.Recency, "day", "week", "month", "year", "recent") || !allowed(out.SearchMode, "general", "academic", "news", "finance") || !allowed(out.DetailLevel, "concise", "normal", "detailed") || len(out.Domains) > 10 {
		return out, store.ErrBotInvalid
	}
	for _, domain := range out.Domains {
		// Preferences are public hostnames, not URLs, origins or credentials.
		if len(domain) > 253 || !strings.Contains(domain, ".") || net.ParseIP(domain) != nil || strings.ContainsAny(domain, "/:@{}\\\\ \r\n\x00") || strings.HasSuffix(strings.ToLower(domain), ".local") || strings.HasSuffix(strings.ToLower(domain), ".internal") {
			return out, store.ErrBotInvalid
		}
		for _, label := range strings.Split(domain, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return out, store.ErrBotInvalid
			}
			for _, r := range label {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
					return out, store.ErrBotInvalid
				}
			}
		}
	}
	return out, nil
}

type botPublicResearchQuota interface {
	ReserveBotPublicResearch(context.Context, execution.BotMandateRef, string) error
}
type botConfiguredResearchTool struct {
	botRunnableTool
	request  bot.RunRequest
	services *store.Services
	factory  tools.LLMFactory
	episode  *BotResearchEpisode
	mcp      *botMCPTool
	// shared executes Chat's already budgeted concrete search, never another catalog.
	sharedResearchAdapter bool
	shared                func(tool.Context, any) (map[string]any, error)
}

// BotConfiguredResearchTool shares the strict BotKnowledge search implementation,
// and dispatches Tavily only through its concrete sandbox MCP adapter.
func BotConfiguredResearchTool(ctx context.Context, r bot.RunRequest, scope *BotChatScope, factory tools.LLMFactory, mcpTools []tool.Tool, episode *BotResearchEpisode) (tool.Tool, error) {
	if r.Mandate == nil || scope == nil || scope.Services == nil || episode == nil || r.Mandate.Authorization.Snapshot().PublicResearch == nil {
		return nil, store.ErrBotForbidden
	}
	t := &botConfiguredResearchTool{request: r, services: scope.Services, factory: factory, episode: episode}
	route, err := BotEffectiveConfiguredSearchRoute(ctx, scope.Services)
	if err != nil {
		return nil, err
	}
	grant := r.Mandate.Authorization.Snapshot().PublicResearch
	ceiling := grant.Clone()
	ceiling.Route = route
	if err = grant.ValidateWithin(&ceiling); err != nil {
		return nil, err
	}
	if route.Engine.IsMCP() {
		for _, candidate := range mcpTools {
			if m, ok := candidate.(*botMCPTool); ok && m.Name() == route.Tool && botMCPFingerprint(m.config) == route.Fingerprint {
				t.mcp = m
			}
		}
		if t.mcp == nil {
			return nil, fmt.Errorf("configured MCP search metadata unavailable; discover the exact executable with approval first")
		}
	}
	native, err := functiontool.New(functiontool.Config{Name: BotConfiguredSearchToolName, Description: fmt.Sprintf("Search the owner-admitted public subject through the exact configured Perplexity, Tavily, Brave or Firecrawl executable. Pass a typed facet (overview, recent, primary, contradictions) or an optional specific public query (such as an individual company or topic). Never send private instructions. Search snippets are discovery only; inspect decisive sources before asserting actionable facts. This run admits at most %d search calls. Use returned remaining episode ceiling; stop broad facet retries when exhausted and inspect discovered sources.", grant.MaxCalls)}, func(c tool.Context, a botPublicResearchArgs) (tools.PerplexityWebSearchResult, error) {
		return t.search(c, a)
	})
	if err != nil {
		return nil, err
	}
	t.botRunnableTool = native.(botRunnableTool)
	episode.setDetailValidator(func(ctx context.Context) error {
		raw, _ := json.Marshal(botPublicResearchArgs{Facet: grant.Facets[0]})
		_, _, err := t.prepare(ctx, raw)
		return err
	})
	return t, nil
}
func (t *botConfiguredResearchTool) prepare(ctx context.Context, raw json.RawMessage) (*execution.BotPublicResearchScope, string, error) {
	if err := t.request.Mandate.Check(ctx); err != nil {
		return nil, "", err
	}
	var a botPublicResearchArgs
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil {
		return nil, "", &bot.ToolArgumentError{Cause: err}
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, "", &bot.ToolArgumentError{Cause: store.ErrBotInvalid}
	}
	grant := t.request.Mandate.Authorization.Snapshot().PublicResearch
	if grant == nil {
		return nil, "", store.ErrBotForbidden
	}
	route, err := BotEffectiveConfiguredSearchRoute(ctx, t.services)
	if err != nil {
		return nil, "", err
	}
	ceiling := grant.Clone()
	ceiling.Route = route
	if err := grant.ValidateWithin(&ceiling); err != nil {
		return nil, "", err
	}
	if _, err := a.effectiveSearchArgs(grant.MaxResults); err != nil {
		return nil, "", &bot.ToolArgumentError{Cause: err}
	}
	customQuery := strings.TrimSpace(a.Query)
	if customQuery != "" {
		if t.request.Run.IdempotencyKey == "" || strings.Contains(strings.ToLower(customQuery), "private") || strings.Contains(customQuery, "{{") || strings.ContainsAny(customQuery, "\r\n\x00") || len(customQuery) > grant.MaxQueryBytes {
			return nil, "", store.ErrBotForbidden
		}
		if a.Facet == "" && len(grant.Facets) > 0 {
			a.Facet = grant.Facets[0]
		}
		if _, err := grant.Query(a.Facet); err != nil {
			return nil, "", err
		}
		return grant, customQuery, nil
	}
	if a.Facet == "" && t.request.Run.IdempotencyKey != "" && len(grant.Facets) > 0 {
		a.Facet = grant.Facets[0]
	}
	q, err := grant.Query(a.Facet)
	return grant, q, err
}
func (t *botConfiguredResearchTool) describe(ctx context.Context, raw json.RawMessage) (execution.ActionDescriptor, error) {
	if _, _, err := t.prepare(ctx, raw); err != nil {
		return execution.ActionDescriptor{}, err
	}
	return execution.ActionDescriptor{Tool: t.Name(), Arguments: raw, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
}
func (t *botConfiguredResearchTool) search(c tool.Context, a botPublicResearchArgs) (result tools.PerplexityWebSearchResult, err error) {
	original := c
	c, endReads := botToolReadOperation(original)
	defer func() { endReads() }()
	raw, _ := json.Marshal(a)
	if _, _, err = t.prepare(c, raw); err != nil {
		return result, err
	}
	endReads() // Episode serialization may wait on another network call.
	generation, err := t.episode.Begin(original, t.request.Run.ID, original.SessionID())
	c, endReads = botToolReadOperation(original)
	if err != nil {
		return result, err
	}
	defer func() { t.episode.observe(c, generation, len(result.SearchResults)+len(result.Citations), err) }()
	// Waiting for another facet must not retain stale configuration, grants or
	// lease authority. Revalidate before quota reservation and provider dispatch.
	grant, q, err := t.prepare(c, raw)
	if err != nil {
		return result, err
	}
	effective, err := a.effectiveSearchArgs(grant.MaxResults)
	if err != nil {
		return result, err
	}
	effective.Query = q
	quota, ok := t.request.Mandate.Runtime.(botPublicResearchQuota)
	if !ok {
		return result, store.ErrBotUnsupported
	}
	// The episode counts only successful durable reservations by this exact
	// executable. An unknown durable rejection is never inferred to be quota.
	if t.episode.remainingSearchCalls(grant.MaxCalls) == 0 {
		bot.RecordToolOutcome(c, c.FunctionCallID(), bot.NewToolNonexecutionOutcome(c, c.FunctionCallID(), t.Name(), a, bot.ToolFeedbackTerminal))
		return result, errBotResearchEpisodeQuota
	}
	// Durable research ceiling is independent of the dispatch's once-only tool
	// reservation and the actual Perplexity engine's shared model reservation.
	if err = quota.ReserveBotPublicResearch(c, grantRef(t), c.SessionID()); err != nil {
		// Reservation rejected before any provider/MCP dispatch. This receipt
		// attests nonexecution only, not quota classification or discovery success.
		bot.RecordToolOutcome(c, c.FunctionCallID(), bot.NewToolNonexecutionOutcome(c, c.FunctionCallID(), t.Name(), a, bot.ToolFeedbackTerminal))
		return result, err
	}
	t.episode.recordSearchReservation()
	if t.shared != nil {
		endReads()
		encoded, _ := json.Marshal(effective)
		var effectiveValues map[string]any
		_ = json.Unmarshal(encoded, &effectiveValues)
		values, callErr := t.shared(original, effectiveValues)
		if callErr != nil {
			return result, callErr
		}
		body, _ := json.Marshal(values)
		if err = json.Unmarshal(body, &result); err != nil {
			return result, err
		}
	} else if grant.Route.Engine.IsMCP() {
		if t.mcp == nil {
			return result, store.ErrBotUnsupported
		}
		arguments, argumentErr := botConfiguredSearchMCPArguments(grant.Route.Engine, q, grant.MaxResults)
		if argumentErr != nil {
			return result, argumentErr
		}
		auth := botMCPAuthorization{target: t.mcp, fingerprint: grant.Route.Fingerprint, check: t.request.Mandate.Check}
		endReads()
		invocation := botBoundToolContext{Context: original, owner: context.WithValue(original, botMCPAuthorizedKey{}, auth)}
		values, callErr := t.mcp.Run(invocation, arguments)
		if callErr != nil {
			return result, callErr
		}
		result, err = botNormalizeConfiguredSearchMCP(grant.Route.Engine, q, grant.MaxResults, values)
		if err != nil {
			return result, err
		}
	} else if grant.Route.Engine == execution.BotSearchPerplexity {
		var cfg *config.AppConfig
		cfg, err = botKnowledgeSearchConfig(c, t.services)
		if err != nil {
			return result, err
		}
		if t.factory == nil {
			return result, store.ErrBotUnsupported
		}
		redactor := credentials.NewRedactor()
		for _, v := range cfg.Providers[grant.Route.Provider] {
			redactor.AddSecret("bot-search-provider", v)
		}
		result, err = tools.RunPerplexityWebSearch(c, effective, cfg, func(ctx context.Context, p, m string, app *config.AppConfig) (model.LLM, error) {
			if p != grant.Route.Provider || m != grant.Route.Model {
				return nil, store.ErrBotForbidden
			}
			// Initialization is read-only discovery/authentication, before any model
			// generation. Retry it once, never RunPerplexityWebSearch or its LLM.
			// Keep the existing run deadline and shared meter, including the wait.
			if timeout, ok := t.request.Budget.(interface{ ModelCallTimeout() time.Duration }); ok && timeout.ModelCallTimeout() > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout.ModelCallTimeout())
				defer cancel()
			}
			if deadline, ok := t.request.Budget.(interface{ Deadline() time.Time }); ok && !deadline.Deadline().IsZero() {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, deadline.Deadline())
				defer cancel()
			}
			var inner model.LLM
			for attempt := 0; ; attempt++ {
				if e := ctx.Err(); e != nil {
					return nil, e
				}
				if t.request.Budget == nil {
					return nil, store.ErrBotUnsupported
				}
				preflight, closePreflight := entstore.WithBotContentReadOperationIfAbsent(ctx)
				e := func() error {
					defer closePreflight()
					if e := t.request.Budget.Check(preflight); e != nil {
						return e
					}
					_, _, e := t.prepare(preflight, raw)
					return e
				}()
				if e != nil {
					return nil, e
				}
				endReads() // Never retain bytes through provider initialization or retry waits.
				inner, e = t.factory(provider.WithConfinedProviderSecrets(ctx), p, m, app)
				if e == nil {
					break
				}
				if attempt != 0 || !botSearchInitializationTransient(ctx, e) {
					// Preserve the concrete diagnostic; redact at the outer boundary.
					return nil, e
				}
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-timer.C:
				}
			}
			return provider.NewBudgetLLM(inner, t.request.Budget, provider.BudgetLLMOptions{MaxOutputTokens: 4096, SupportsOutputLimit: true})
		})
		if err != nil {
			return tools.PerplexityWebSearchResult{}, fmt.Errorf("configured search failed: %s", redactor.Redact(err.Error()))
		}
		data, _ := json.Marshal(result)
		_ = json.Unmarshal([]byte(redactor.Redact(string(data))), &result)
	} else {
		return tools.PerplexityWebSearchResult{}, store.ErrBotUnsupported
	}
	c, endReads = botToolReadOperation(original)
	if err = t.request.Mandate.Check(c); err != nil {
		return tools.PerplexityWebSearchResult{}, err
	}
	if result.Metadata == nil {
		result.Metadata = map[string]interface{}{}
	}
	result.Metadata["remaining_episode_search_ceiling"] = t.episode.remainingSearchCalls(grant.MaxCalls)
	result.Metadata["search_call_limit"] = grant.MaxCalls
	projection, _ := json.Marshal(result)
	var observation map[string]any
	_ = json.Unmarshal(projection, &observation)
	recordBuiltinToolObservationWithCapture(c, c.SessionID(), t.Name(), map[string]any{"facet": a.Facet}, observation, observation)
	return result, nil
}

// botSearchInitializationTransient deliberately uses typed transport errors, not
// error-message matching. HTTP auth/status failures, bad configuration, TLS and
// permanent DNS errors are not retryable. A client timeout may wrap a deadline
// error even though the caller's own deadline has not expired.
func botSearchInitializationTransient(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return false
	}
	var network net.Error
	if errors.As(err, &network) && (network.Timeout() || network.Temporary()) {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE)
}

func botSymbolicSearchCredential(v string) bool {
	if len(v) > 256 || !strings.HasPrefix(v, "{{CREDENTIAL:") || !strings.HasSuffix(v, "}}") {
		return false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(v, "{{CREDENTIAL:"), "}}")
	parts := strings.Split(inner, ":")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.ContainsAny(inner, "{}\r\n\x00")
}

func botConfiguredSearchMCPArguments(engine execution.BotSearchEngine, query string, limit int) (map[string]any, error) {
	if query == "" || len(query) > 2048 || limit < 1 || limit > 20 {
		return nil, store.ErrBotInvalid
	}
	args := map[string]any{"query": query}
	switch engine {
	case execution.BotSearchTavily:
		args["max_results"] = limit
	case execution.BotSearchBrave:
		args["count"] = limit
	case execution.BotSearchFirecrawl:
		args["limit"] = limit
	default:
		return nil, store.ErrBotUnsupported
	}
	return args, nil
}

// Decode the actual MCP text envelope, not just synthetic top-level results.
// Failed/unknown envelopes must not turn error text or sibling results into sources.
func botNormalizeConfiguredSearchMCP(engine execution.BotSearchEngine, query string, limit int, values map[string]any) (tools.PerplexityWebSearchResult, error) {
	var empty tools.PerplexityWebSearchResult
	if !engine.IsMCP() || limit < 1 || limit > 20 {
		return empty, store.ErrBotInvalid
	}
	raw, err := json.Marshal(values)
	if err != nil || len(raw) > 8<<20 {
		return empty, fmt.Errorf("invalid configured search response")
	}
	result := tools.PerplexityWebSearchResult{Query: query, Provider: string(engine), Warning: "Search snippets are discovery evidence, not verification of actionable values."}
	seen := map[string]bool{}
	var decode func(json.RawMessage, int) error
	decode = func(raw json.RawMessage, depth int) error {
		if depth > 1 {
			return fmt.Errorf("nested configured search envelope")
		}
		var payload struct {
			IsError bool            `json:"isError"`
			Error   json.RawMessage `json:"error"`
			Success *bool           `json:"success"`
			Results json.RawMessage `json:"results"`
			Web     struct {
				Results json.RawMessage `json:"results"`
			} `json:"web"`
			Data    json.RawMessage `json:"data"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return fmt.Errorf("invalid configured search payload")
		}
		if payload.IsError || (payload.Success != nil && !*payload.Success) || (len(payload.Error) > 0 && string(payload.Error) != "null" && string(payload.Error) != `""`) {
			return fmt.Errorf("configured search returned an error")
		}
		if len(payload.Content) > 0 {
			for _, block := range payload.Content {
				if block.Type != "" && block.Type != "text" {
					continue
				}
				if err := decode(json.RawMessage(block.Text), depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		var hitsRaw json.RawMessage
		switch engine {
		case execution.BotSearchTavily:
			hitsRaw = payload.Results
		case execution.BotSearchBrave:
			hitsRaw = payload.Web.Results
		case execution.BotSearchFirecrawl:
			hitsRaw = payload.Data
			if len(hitsRaw) > 0 && bytes.HasPrefix(bytes.TrimSpace(hitsRaw), []byte("{")) {
				var data struct {
					Web json.RawMessage `json:"web"`
				}
				if json.Unmarshal(hitsRaw, &data) != nil {
					return fmt.Errorf("invalid Firecrawl data")
				}
				hitsRaw = data.Web
			}
		}
		var hits []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		}
		if len(hitsRaw) == 0 || json.Unmarshal(hitsRaw, &hits) != nil {
			return fmt.Errorf("unknown configured search result shape")
		}
		for _, hit := range hits {
			if len(result.SearchResults) >= limit {
				break
			}
			if len(hit.URL) > 4096 || !botPublicSearchURL(hit.URL) || seen[hit.URL] {
				continue
			}
			seen[hit.URL] = true
			title := hit.Title
			if len(title) > 1024 {
				title = ""
			}
			result.SearchResults = append(result.SearchResults, tools.PerplexitySearchHit{Title: title, URL: hit.URL})
		}
		return nil
	}
	if err := decode(raw, 0); err != nil {
		return empty, err
	}
	if len(result.SearchResults) == 0 {
		return empty, fmt.Errorf("configured search returned no usable public sources")
	}
	return result, nil
}

func botPublicSearchURL(raw string) bool {
	if _, err := tools.RoutineReadOrigin(raw); err != nil {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || !strings.Contains(host, ".") && net.ParseIP(host) == nil {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && (!ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

func grantRef(t *botConfiguredResearchTool) execution.BotMandateRef {
	return t.request.Mandate.Authorization.Ref()
}
