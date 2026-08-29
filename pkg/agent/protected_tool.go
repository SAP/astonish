package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/ui"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// RunnableTool defines an interface for tools that can be executed.
// Canonical definition lives in pkg/common; re-exported here for backward compatibility.
type RunnableTool = common.RunnableTool

// ToolWithDeclaration allows inspecting the tool's schema.
// Canonical definition lives in pkg/common; re-exported here for backward compatibility.
type ToolWithDeclaration = common.ToolWithDeclaration

// ProtectedToolset wraps a toolset and returns tools wrapped with ProtectedTool
type ProtectedToolset struct {
	underlying tool.Toolset
	state      session.State
	agent      *AstonishAgent
	yieldFunc  func(*session.Event, error) bool
}

// Name returns the name of the underlying toolset
func (p *ProtectedToolset) Name() string {
	return p.underlying.Name()
}

// Tools returns the underlying toolset's tools, wrapped with ProtectedTool
func (p *ProtectedToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	underlyingTools, err := p.underlying.Tools(ctx)
	if err != nil {
		return nil, err
	}

	wrappedTools := make([]tool.Tool, len(underlyingTools))
	for i, t := range underlyingTools {
		wrappedTools[i] = &ProtectedTool{
			Tool:      t,
			State:     p.state,
			Agent:     p.agent,
			YieldFunc: p.yieldFunc,
		}
	}

	return wrappedTools, nil
}

// FilteredToolset wraps a toolset and filters tools based on allowed list
type FilteredToolset struct {
	underlying   tool.Toolset
	allowedTools []string
}

// Name returns the name of the underlying toolset
func (f *FilteredToolset) Name() string {
	return f.underlying.Name()
}

// Tools returns only the tools that are in the allowed list
func (f *FilteredToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	underlyingTools, err := f.underlying.Tools(ctx)
	if err != nil {
		return nil, err
	}

	// Create a map for fast lookup
	allowedMap := make(map[string]bool)
	for _, name := range f.allowedTools {
		allowedMap[name] = true
	}

	// Filter tools
	var filteredTools []tool.Tool
	for _, t := range underlyingTools {
		if allowedMap[t.Name()] {
			filteredTools = append(filteredTools, t)
		}
	}

	return filteredTools, nil
}

// ProtectedTool wraps a standard tool and adds an approval gate.
type ProtectedTool struct {
	tool.Tool                                  // Embed the underlying tool
	State     session.State                    // Access to session state
	Agent     *AstonishAgent                   // Access to helper methods
	YieldFunc func(*session.Event, error) bool // For emitting events
}

// Declaration forwards the call to the underlying tool if it supports it.
func (p *ProtectedTool) Declaration() *genai.FunctionDeclaration {
	if declTool, ok := p.Tool.(ToolWithDeclaration); ok {
		return declTool.Declaration()
	}
	return nil
}

// ProcessRequest always delegates to underlying tool to register it with the LLM
// Approval is checked later during Run(), not during ProcessRequest()
func (p *ProtectedTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	// Always delegate to underlying tool so it gets registered with the LLM
	// Approval will be checked when the tool is actually executed (Run method)
	if processor, ok := p.Tool.(interface {
		ProcessRequest(tool.Context, *model.LLMRequest) error
	}); ok {
		return processor.ProcessRequest(ctx, req)
	}
	return nil // Tool doesn't implement ProcessRequest, that's okay
}

// Run intercepts the execution to check for approval
func (p *ProtectedTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	toolName := p.Tool.Name()

	// Get current node for node-scoped approval (prevents same tool in different nodes from sharing approval)
	currentNode := ""
	if nodeVal, err := p.State.Get("current_node"); err == nil && nodeVal != nil {
		if nodeName, ok := nodeVal.(string); ok {
			currentNode = nodeName
		}
	}
	approvalKey := fmt.Sprintf("approval:%s:%s", currentNode, toolName)

	// 1. Check if we already have approval OR if global auto-approve is enabled
	if p.Agent.AutoApprove {
		// Auto-approve enabled, bypass check
		// We use a broader interface check here to be safe
		if rt, ok := p.Tool.(interface {
			Run(tool.Context, any) (map[string]any, error)
		}); ok {
			return rt.Run(ctx, args)
		}
		return nil, fmt.Errorf("underlying tool does not implement Run")
	}

	if approved, _ := p.State.Get(approvalKey); approved == true {
		// Consume approval - each execution requires new approval
		p.State.Set(approvalKey, false)

		// We use a broader interface check here to be safe
		if rt, ok := p.Tool.(interface {
			Run(tool.Context, any) (map[string]any, error)
		}); ok {
			return rt.Run(ctx, args)
		}
		return nil, fmt.Errorf("underlying tool does not implement Run")
	}

	// 2. Format arguments for display
	var argsMap map[string]any
	if m, ok := args.(map[string]any); ok {
		argsMap = m
	} else {
		// If args is a struct (common in MCP), wrap it for display
		argsMap = map[string]any{"arguments": args}
	}

	// 3. Set the approval state
	p.State.Set("awaiting_approval", true)
	p.State.Set("approval_tool", toolName)
	p.State.Set("approval_args", argsMap)

	// 4. Emit the UI Event
	prompt := p.Agent.formatToolApprovalRequest(toolName, argsMap)

	p.YieldFunc(&session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Parts: []*genai.Part{{Text: prompt}},
				Role:  "model",
			},
		},
		Actions: session.EventActions{
			StateDelta: map[string]any{
				"awaiting_approval": true,
				"approval_options":  []string{"Yes", "No"},
			},
		},
	}, nil)

	// 5. Return error to stop execution and wait for approval
	// This prevents the LLM from seeing a tool result before approval
	return nil, ErrWaitingForApproval
}

// formatToolApprovalRequest formats a tool approval request
func (a *AstonishAgent) formatToolApprovalRequest(toolName string, args map[string]interface{}) string {
	if a.IsWebMode {
		// Return plain text / markdown for Web UI
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("**Requesting approval to execute tool: `%s`**\n\n", toolName))
		sb.WriteString("Arguments:\n")
		sb.WriteString("```json\n")
		enc := json.NewEncoder(&sb)
		enc.SetIndent("", "  ")
		enc.Encode(args)
		sb.WriteString("```\n")
		return sb.String()
	}
	// Return ANSI formatted box for CLI
	return ui.RenderToolBox(toolName, args)
}

// formatFolderApprovalRequest formats a folder-access authorization request for
// the code-mode gate: the tool wants to touch paths outside the project root.
func (a *AstonishAgent) formatFolderApprovalRequest(toolName string, paths []string, root string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("`%s` wants to access files outside the project directory:\n", toolName))
	for _, p := range paths {
		sb.WriteString("  • " + p + "\n")
	}
	if root != "" {
		sb.WriteString("\nProject directory: " + root + "\n")
	}
	return sb.String()
}
