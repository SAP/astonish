package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

type legacyToolDiscoveryState struct {
	dynamicMatches     []ToolMatch
	searchToolsResults []string
	mu                 sync.Mutex
}

func (c *ChatAgent) legacyToolState(invocationID string) *legacyToolDiscoveryState {
	state, _ := c.legacyToolStates.LoadOrStore(invocationID, &legacyToolDiscoveryState{})
	return state.(*legacyToolDiscoveryState)
}

// RegisterSearchToolsResults records tools for the next legacy model round.
func (c *ChatAgent) RegisterSearchToolsResults(ctx context.Context, toolNames []string) {
	invocationContext, ok := ctx.(interface{ InvocationID() string })
	if !ok {
		return
	}
	state := c.legacyToolState(invocationContext.InvocationID())
	state.mu.Lock()
	defer state.mu.Unlock()
	state.searchToolsResults = append(state.searchToolsResults, toolNames...)
}

// AutoInjectMissingToolCallback enables legacy missing-tool recovery.
func (c *ChatAgent) AutoInjectMissingToolCallback(state *legacyToolDiscoveryState) llmagent.OnToolErrorCallback {
	return autoInjectMissingToolCallback(c.ToolIndex, func(names []string) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.searchToolsResults = append(state.searchToolsResults, names...)
	}, nil)
}

// autoInjectMissingToolCallback builds the shared OnToolErrorCallback used by
// sub-agents with their child-scoped injection pipeline.
// exclude skips tools that must not be injected
// (e.g. excludedChildTools).
func autoInjectMissingToolCallback(
	toolIndex *ToolIndex,
	register func([]string),
	exclude map[string]bool,
) llmagent.OnToolErrorCallback {
	return func(ctx agent.ToolContext, t tool.Tool, _ map[string]any, err error) (map[string]any, error) {
		if toolIndex == nil || register == nil || t == nil || !isToolNotFoundError(err, t.Name()) {
			return nil, nil // let ADK keep its default not-found response
		}

		name := t.Name()
		// Normalize app-style refs (mcp:email/send_email → send_email).
		resolved := resolveIndexedToolName(toolIndex, name)
		if exclude != nil && (exclude[name] || exclude[resolved]) {
			return nil, nil
		}
		if !canAutoInjectTool(ctx, toolIndex, resolved) {
			return nil, nil
		}

		register([]string{resolved})
		slog.Debug("auto-injected missing tool for next LLM call",
			"component", "chat", "tool", resolved, "requested", name)

		hint := resolved
		if resolved != name {
			hint = fmt.Sprintf("%s (not %q — use the bare tool name)", resolved, name)
		}
		return map[string]any{
			"error": fmt.Sprintf(
				"Tool %s exists but was not loaded for this turn. "+
					"It has been injected into the session — call %q again with the same arguments.",
				hint, resolved,
			),
		}, nil
	}
}

// isToolNotFoundError reports whether err is ADK's tool-not-found error for toolName.
// Matches ADK 1.5's "tool 'X' not found" FunctionResponse path and the legacy
// hard-error form "unknown tool:".
func isToolNotFoundError(err error, toolName string) bool {
	if err == nil || toolName == "" {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, fmt.Sprintf("tool '%s' not found", toolName)) {
		return true
	}
	if strings.Contains(msg, "unknown tool:") && strings.Contains(msg, toolName) {
		return true
	}
	return false
}

// canAutoInjectTool reports whether toolName may be injected from ToolIndex
// or request-scoped MCP groups (MCP access + team disabled-tool list).
// Accepts bare names and mcp:server/tool aliases.
func canAutoInjectTool(ctx context.Context, toolIndex *ToolIndex, toolName string) bool {
	if toolName == "" {
		return false
	}
	if toolIndex != nil {
		resolved := resolveIndexedToolName(toolIndex, toolName)
		if fp := toolIndex.FirstPartyToolEntry(resolved); fp != nil {
			for _, disabled := range store.DisabledToolsFromContext(ctx) {
				if disabled == resolved || disabled == toolName || disabled == fp.Name {
					return false
				}
			}
			return true
		}
	}
	// Request-scoped MCP tools (team catalog) take precedence over stale MCP
	// index entries, but not over first-party tools (checked above).
	if t, gName, lookupErr := LookupRequestMCPTool(ctx, toolName); lookupErr == nil && t != nil {
		for _, disabled := range store.DisabledToolsFromContext(ctx) {
			if disabled == t.Name() || disabled == toolName {
				return false
			}
		}
		if serverName, isMCP := mcpServerNameFromGroup(gName); isMCP {
			if !isMCPServerAccessible(ctx, serverName) {
				return false
			}
		}
		return true
	}
	if toolIndex == nil {
		return false
	}
	resolved := resolveIndexedToolName(toolIndex, toolName)
	entry := toolIndex.GetToolEntry(resolved)
	if entry == nil || entry.Tool == nil {
		return false
	}
	for _, disabled := range store.DisabledToolsFromContext(ctx) {
		if disabled == resolved || disabled == toolName {
			return false
		}
	}
	if serverName, isMCP := mcpServerNameFromGroup(entry.GroupName); isMCP {
		if !isMCPServerAccessible(ctx, serverName) {
			return false
		}
	}
	return true
}

func (c *ChatAgent) DynamicToolInjectionCallback(state *legacyToolDiscoveryState) llmagent.BeforeModelCallback {
	return func(cbCtx agent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
		if c.ToolIndex == nil {
			return nil, nil
		}

		// Collect tool names to inject from both sources.
		toolsToInject := make(map[string]bool)

		// Sources 1 and 2 are scoped to this invocation so concurrent sessions
		// cannot exchange automatic or explicit search results.
		state.mu.Lock()
		for _, m := range state.dynamicMatches {
			if !m.IsMainTool {
				toolsToInject[m.ToolName] = true
			}
		}
		for _, name := range state.searchToolsResults {
			toolsToInject[name] = true
		}
		state.mu.Unlock()

		// Source 3: pinned tool groups from PromptOverrides (wizard sessions).
		// These ensure critical tools remain available across all turns of a
		// multi-turn guided conversation regardless of ToolIndex scoring.
		if po := PromptOverridesFromContext(cbCtx); po != nil && len(po.PinnedToolGroups) > 0 {
			for _, groupName := range po.PinnedToolGroups {
				entries := c.ToolIndex.GetToolsByGroup(groupName)
				for _, entry := range entries {
					if !entry.IsMainTool {
						toolsToInject[entry.Name] = true
					}
				}
				// Also check request-scoped groups (e.g., per-request A2A tools)
				// that are not in the singleton ToolIndex.
				if len(entries) == 0 {
					if reqGroups := RequestMCPGroupsFromContext(cbCtx); reqGroups != nil {
						if g := reqGroups[groupName]; g != nil {
							for _, t := range g.Tools {
								if t != nil {
									toolsToInject[t.Name()] = true
								}
							}
							readCtx := &minimalReadonlyContext{Context: cbCtx}
							for _, ts := range g.Toolsets {
								if ts == nil {
									continue
								}
								tools, err := ts.Tools(readCtx)
								if err != nil {
									continue
								}
								for _, t := range tools {
									if t != nil {
										toolsToInject[t.Name()] = true
									}
								}
							}
						}
					}
				}
			}
		}

		if len(toolsToInject) == 0 {
			return nil, nil
		}

		// Inject each tool into the request.
		injected := 0
		for toolName := range toolsToInject {
			resolved := resolveIndexedToolName(c.ToolIndex, toolName)
			if _, exists := req.Tools[resolved]; exists {
				continue // already registered (static main-thread tool)
			}

			// First-party tools (slides, email, core, …) win over MCP servers that
			// reuse the same bare name (email-mcp list_templates vs slides).
			if fp := c.ToolIndex.FirstPartyToolEntry(resolved); fp != nil {
				if _, exists := req.Tools[fp.Tool.Name()]; exists {
					continue
				}
				packToolIntoRequest(req, fp.Tool)
				injected++
				continue
			}

			// Prefer request-scoped MCP tools (team catalog) over stale index entries.
			t, gName, lookupErr := LookupRequestMCPTool(cbCtx, toolName)
			if lookupErr != nil {
				continue
			}
			if t != nil {
				if serverName, isMCP := mcpServerNameFromGroup(gName); isMCP {
					if !isMCPServerAccessible(cbCtx, serverName) {
						continue
					}
				}
				if _, exists := req.Tools[t.Name()]; exists {
					continue
				}
				packToolIntoRequest(req, t)
				injected++
				continue
			}

			entry := c.ToolIndex.GetToolEntry(resolved)
			if entry == nil || entry.Tool == nil {
				// If the LLM/search asked for a whole MCP group (mcp:email),
				// inject every tool in that group (index + request-scoped).
				if group, bare, isRef := parseMCPToolRef(toolName); isRef && bare == "" {
					if reqGroups := RequestMCPGroupsFromContext(cbCtx); reqGroups != nil {
						if g := reqGroups[group]; g != nil {
							if serverName, isMCP := mcpServerNameFromGroup(group); isMCP {
								if !isMCPServerAccessible(cbCtx, serverName) {
									continue
								}
							}
							readCtx := &minimalReadonlyContext{Context: cbCtx}
							for _, ts := range g.Toolsets {
								tools, err := ts.Tools(readCtx)
								if err != nil {
									continue
								}
								for _, gt := range tools {
									if gt == nil {
										continue
									}
									if _, exists := req.Tools[gt.Name()]; exists {
										continue
									}
									packToolIntoRequest(req, gt)
									injected++
								}
							}
						}
					}
					if c.ToolIndex != nil {
						for _, ge := range c.ToolIndex.GetToolsByGroup(group) {
							if ge.Tool == nil {
								continue
							}
							if serverName, isMCP := mcpServerNameFromGroup(ge.GroupName); isMCP {
								if !isMCPServerAccessible(cbCtx, serverName) {
									continue
								}
							}
							if _, exists := req.Tools[ge.Name]; exists {
								continue
							}
							packToolIntoRequest(req, ge.Tool)
							injected++
						}
					}
				}
				continue
			}

			// MCP tool access control: in platform mode, only inject tools
			// from MCP servers the user's team/org has access to.
			if serverName, isMCP := mcpServerNameFromGroup(entry.GroupName); isMCP {
				if !isMCPServerAccessible(cbCtx, serverName) {
					continue
				}
			}

			packToolIntoRequest(req, entry.Tool)
			injected++
		}

		if c.DebugMode && injected > 0 {
			slog.Debug("dynamic tool injection", "component", "chat", "injected", injected)
		}

		return nil, nil
	}
}

func removeRequestToolsCallback(names ...string) llmagent.BeforeModelCallback {
	return func(_ agent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
		if req == nil {
			return nil, nil
		}
		for _, name := range names {
			delete(req.Tools, name)
		}
		if req.Config == nil {
			return nil, nil
		}
		for _, packed := range req.Config.Tools {
			if packed == nil {
				continue
			}
			kept := packed.FunctionDeclarations[:0]
			for _, declaration := range packed.FunctionDeclarations {
				remove := false
				for _, name := range names {
					remove = remove || declaration != nil && declaration.Name == name
				}
				if !remove {
					kept = append(kept, declaration)
				}
			}
			packed.FunctionDeclarations = kept
		}
		return nil, nil
	}
}

// toolWithDeclaration matches ADK's internal FunctionTool interface for tools
// that can declare their JSON schema. All function-based tools implement this.
type toolWithDeclaration interface {
	Declaration() *genai.FunctionDeclaration
}

// packToolIntoRequest adds a tool to an LLM request for both dispatch and
// schema declaration. This replicates the logic from ADK's internal PackTool
// (toolutils.go) and Astonish's NodeTool.ProcessRequest.
func packToolIntoRequest(req *model.LLMRequest, t tool.Tool) {
	if req.Tools == nil {
		req.Tools = make(map[string]any)
	}
	name := t.Name()
	if _, ok := req.Tools[name]; ok {
		return // already registered
	}
	req.Tools[name] = t

	// Get the function declaration via type assertion — tool.Tool doesn't
	// include Declaration(), but all function-based tools implement it.
	dt, ok := t.(toolWithDeclaration)
	if !ok {
		return
	}
	decl := dt.Declaration()
	if decl == nil {
		return
	}
	if req.Config == nil {
		req.Config = &genai.GenerateContentConfig{}
	}
	// Find existing FunctionDeclarations block (all function tools share one).
	var funcTool *genai.Tool
	for _, gt := range req.Config.Tools {
		if gt != nil && gt.FunctionDeclarations != nil {
			funcTool = gt
			break
		}
	}
	if funcTool == nil {
		req.Config.Tools = append(req.Config.Tools, &genai.Tool{
			FunctionDeclarations: []*genai.FunctionDeclaration{decl},
		})
	} else {
		funcTool.FunctionDeclarations = append(funcTool.FunctionDeclarations, decl)
	}
}
