package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// autoInjectMissingToolCallback builds the shared OnToolErrorCallback used by
// Sub-agents use this callback with their child-scoped injection pipeline.
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
