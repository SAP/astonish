package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/a2aclient"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

// MCPProgressiveRuntime is an immutable, authenticated snapshot of the tools
// and guidance available for one externally-owned MCP turn.
type MCPProgressiveRuntime struct {
	ctx         context.Context
	principal   execution.Principal
	chat        *agent.ChatAgent
	prompt      *agent.SystemPromptBuilder
	catalog     *agent.ToolIndex
	catalogList []agent.ToolMatch
	sessionID   string
}

func prepareMCPProgressiveRuntime(request *http.Request, principal execution.Principal, sessionID string) (*MCPProgressiveRuntime, error) {
	if request == nil {
		return nil, fmt.Errorf("MCP request is required")
	}
	// The MCP server processes later tool calls through distinct HTTP requests.
	// Preserve request-scoped tenant/store values without retaining cancellation
	// from the context-retrieval request.
	ctx := store.WithSessionID(context.WithoutCancel(request.Context()), sessionID)
	cm := GetChatManager()
	if err := cm.ensureReady(ctx); err != nil {
		return nil, fmt.Errorf("initialize MCP tool runtime: %w", err)
	}
	cm.mu.Lock()
	components := cm.components
	cm.mu.Unlock()
	if components == nil || components.ChatAgent == nil {
		return nil, fmt.Errorf("MCP tool runtime is unavailable")
	}

	base := components.ChatAgent
	chatCopy := &agent.ChatAgent{
		ToolIndex:       base.ToolIndex,
		CredentialStore: base.CredentialStore,
		PendingSecrets:  base.PendingSecrets,
	}
	prompt := base.SystemPrompt.Clone()
	if prompt == nil {
		prompt = &agent.SystemPromptBuilder{}
	}
	ctx = hydrateMCPProgressiveContext(request, ctx, components, chatCopy, prompt)
	runtimeRequest := request.WithContext(ctx)
	// A local lexical index includes the authenticated request's deferred MCP
	// and A2A tools without mutating the singleton Studio agent catalog.
	groups := buildRequestMCPToolGroups(runtimeRequest, components.SandboxPool, base.DebugMode)
	if groups == nil {
		groups = map[string]*agent.ToolGroup{}
	}
	if services := store.FromContext(ctx); services != nil {
		a2aTools := a2aclient.GetA2AToolsFromStores(ctx, &store.A2AAgentStores{
			Platform: services.PlatformA2AAgents,
			Org:      services.A2AAgents,
			Team:     services.TeamA2AAgents,
		})
		if len(a2aTools) > 0 {
			groups["a2a"] = &agent.ToolGroup{Name: "a2a", Description: "Configured remote A2A agents", Tools: a2aTools}
		}
	}
	ctx = agent.WithRequestMCPGroups(ctx, groups)
	catalog := agent.NewLexicalToolIndex()
	if err := catalog.PrimeTools(ctx, base.Tools, append(base.SystemPrompt.Catalog, agent.SortedGroups(groups)...)); err != nil {
		return nil, fmt.Errorf("build MCP tool catalog: %w", err)
	}
	chatCopy.ToolIndex = catalog
	all := flattenToolCatalog(catalog.ListAll())
	return &MCPProgressiveRuntime{ctx: ctx, principal: principal, chat: chatCopy, prompt: prompt, catalog: catalog, catalogList: all, sessionID: sessionID}, nil
}

func hydrateMCPProgressiveContext(request *http.Request, ctx context.Context, components *StudioChatComponents, chatCopy *agent.ChatAgent, prompt *agent.SystemPromptBuilder) context.Context {
	ctx = withRequestCredentialStore(ctx, request)
	if credentialStore := store.CredentialStoreFromContext(ctx); credentialStore != nil {
		redactor := credentials.NewRedactor()
		redactor.HydrateFromStore(credentialStore)
		chatCopy.Redactor = redactor
		ctx = credentials.WithRedactor(ctx, redactor)
	}
	if services := store.FromContext(ctx); services != nil && services.Mode == store.ModePlatform {
		ctx = store.WithSkillStores(ctx, &store.SkillStores{
			Platform: services.PlatformSkills,
			Org:      services.Skills,
			Team:     services.TeamSkills,
		})
		prompt.SkillIndex = buildMergedSkillIndex(ctx, components.FilesystemSkills, services.PlatformSkills, services.Skills, services.TeamSkills)
	}
	return ctx
}

func flattenToolCatalog(grouped map[string][]agent.ToolMatch) []agent.ToolMatch {
	var out []agent.ToolMatch
	for _, matches := range grouped {
		out = append(out, matches...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ToolName == out[j].ToolName {
			return out[i].GroupName < out[j].GroupName
		}
		return out[i].ToolName < out[j].ToolName
	})
	return out
}

func (r *MCPProgressiveRuntime) contextForTurn(userMessage string) (string, []store.MemorySearchResult, string) {
	var memories []store.MemorySearchResult
	memoryState := "unavailable"
	if searcher := store.ThreeTierSearcherFromContext(r.ctx); searcher != nil {
		results, err := searcher.SearchAllTiers(r.ctx, userMessage, 8, 0.3)
		if err == nil {
			memories, memoryState = results, "ready"
		} else {
			memoryState = "error"
		}
	} else if memory := store.MemoryStoreFromContext(r.ctx); memory != nil {
		results, err := memory.Search(r.ctx, userMessage, 8, 0.3)
		if err == nil {
			memories, memoryState = results, "ready"
		} else {
			memoryState = "error"
		}
	}
	knowledge := formatMCPMemories(memories)
	return agent.RenderExternalTurnContext(nil, agent.FormatToolMatchesForPrompt(r.catalogList), knowledge), memories, memoryState
}

func formatMCPMemories(memories []store.MemorySearchResult) string {
	if len(memories) == 0 {
		return ""
	}
	var b strings.Builder
	for _, memory := range memories {
		fmt.Fprintf(&b, "- %s\n", memory.Snippet)
	}
	return b.String()
}

func (r *MCPProgressiveRuntime) search(query string, maxResults int) []agent.ToolMatch {
	if maxResults <= 0 || maxResults > 50 {
		maxResults = 10
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return r.catalogList[:min(maxResults, len(r.catalogList))]
	}
	var matches []agent.ToolMatch
	for _, candidate := range r.catalogList {
		if strings.Contains(strings.ToLower(candidate.ToolName), query) || strings.Contains(strings.ToLower(candidate.Description), query) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) > maxResults {
		matches = matches[:maxResults]
	}
	return matches
}

func (r *MCPProgressiveRuntime) describe(names []string) ([]map[string]any, []string) {
	var described []map[string]any
	var unavailable []string
	for _, name := range names {
		entry := r.catalog.GetToolEntry(name)
		if entry == nil || entry.Tool == nil {
			unavailable = append(unavailable, name)
			continue
		}
		declared, ok := entry.Tool.(interface {
			Declaration() *genai.FunctionDeclaration
		})
		if !ok {
			unavailable = append(unavailable, name)
			continue
		}
		decl := declared.Declaration()
		described = append(described, map[string]any{"name": entry.Name, "description": entry.Description, "group": entry.GroupName, "input_schema": declarationSchema(decl)})
	}
	return described, unavailable
}

func declarationSchema(decl any) any {
	// FunctionDeclaration is intentionally returned as JSON-compatible data by
	// the MCP SDK's encoder. Keeping it opaque avoids losing provider-specific
	// schema fields while never exposing a runnable implementation.
	return decl
}

func (r *MCPProgressiveRuntime) snapshotKey() string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(r.sessionID)
	b.WriteString("\n")
	b.WriteString(r.prompt.BuildExternal())
	for _, tool := range r.catalogList {
		fmt.Fprintf(&b, "\n%s|%s|%s", tool.GroupName, tool.ToolName, tool.Description)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func (r *MCPProgressiveRuntime) execute(name string, arguments map[string]any) agent.ProgressiveToolResult {
	return agent.NewProgressiveToolExecutor(r.chat).Execute(&mcpToolContext{Context: r.ctx, sessionID: r.sessionID}, name, arguments)
}

type mcpToolContext struct {
	context.Context
	sessionID string
}

func (m *mcpToolContext) Actions() *session.EventActions       { return &session.EventActions{} }
func (m *mcpToolContext) Branch() string                       { return "" }
func (m *mcpToolContext) AgentName() string                    { return "MCPProgressiveRuntime" }
func (m *mcpToolContext) AppName() string                      { return "astonish" }
func (m *mcpToolContext) Artifacts() adkagent.Artifacts        { return nil }
func (m *mcpToolContext) FunctionCallID() string               { return "" }
func (m *mcpToolContext) InvocationID() string                 { return "" }
func (m *mcpToolContext) SessionID() string                    { return m.sessionID }
func (m *mcpToolContext) UserID() string                       { return "" }
func (m *mcpToolContext) UserContent() *genai.Content          { return nil }
func (m *mcpToolContext) ReadonlyState() session.ReadonlyState { return nil }
func (m *mcpToolContext) SearchMemory(context.Context, string) (*memory.SearchResponse, error) {
	return nil, nil
}
func (m *mcpToolContext) State() session.State                                 { return nil }
func (m *mcpToolContext) RequestConfirmation(string, any) error                { return nil }
func (m *mcpToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
