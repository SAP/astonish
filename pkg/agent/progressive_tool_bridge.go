package agent

import (
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

const executeToolName = common.ExecuteToolName

// isBridgeOwnToolName reports whether name is one of the progressive bridge's
// own fixed dispatcher/discovery tools. These are model-visible by declaration
// and must never be looked up as deferred catalog entries.
func isBridgeOwnToolName(name string) bool {
	switch name {
	case executeToolName, "describe_tools", "search_tools":
		return true
	default:
		return false
	}
}

type DescribeToolsArgs struct {
	Names []string `json:"names" jsonschema:"Tool names returned by search_tools"`
}

type DescribedTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	GroupName   string `json:"group_name"`
	InputSchema any    `json:"input_schema,omitempty"`
}

type DescribeToolsResult struct {
	Tools  []DescribedTool `json:"tools"`
	Errors []string        `json:"errors,omitempty"`
}

type ExecuteToolArgs struct {
	Name      string         `json:"name" jsonschema:"Exact tool reference returned by describe_tools"`
	Arguments map[string]any `json:"arguments" jsonschema:"Arguments matching the schema returned by describe_tools"`
}

type deferredToolResolver struct {
	index *ToolIndex
	scope *ChildToolScope
}

func (r deferredToolResolver) resolve(ctx agent.ReadonlyContext, name string) (tool.Tool, string, error) {
	name = strings.TrimSpace(name)
	if r.scope != nil {
		return r.scope.resolve(ctx, name)
	}
	if name == "" {
		return nil, "", fmt.Errorf("tool name is required")
	}
	// The progressive bridge's own fixed names must never be resolved as deferred
	// catalog tools. If one reaches here it means an execute_tool envelope was not
	// unwrapped to its inner tool (the model nested execute_tool inside itself, or
	// a provider echoed the dispatcher name as the inner name). Returning the
	// generic "not found in the available catalog" here is actively misleading —
	// it implies the dispatcher is missing. Instead, guide the model to supply the
	// real inner tool name and arguments.
	if isBridgeOwnToolName(name) {
		return nil, "", fmt.Errorf("%q is the tool dispatcher, not a target tool; call it with a %q field naming the deferred tool to run (from describe_tools) and its parameters under %q", name, "name", "arguments")
	}
	resolved := resolveIndexedToolName(r.index, name)
	qualified := strings.Contains(name, "/")
	if !qualified && r.index != nil {
		if entry := r.index.FirstPartyToolEntry(resolved); entry != nil {
			if isToolDisabled(ctx, name, entry.Name) {
				return nil, "", fmt.Errorf("tool %q is disabled", entry.Name)
			}
			return entry.Tool, entry.GroupName, nil
		}
	}
	if !qualified {
		if overrides := PromptOverridesFromContext(ctx); overrides != nil {
			var matched tool.Tool
			for _, candidate := range overrides.AdditionalTools {
				if candidate != nil && candidate.Name() == resolved {
					if matched != nil {
						return nil, "", fmt.Errorf("tool %q is ambiguous across request tools", name)
					}
					matched = candidate
				}
			}
			if matched != nil {
				if isToolDisabled(ctx, name, matched.Name()) {
					return nil, "", fmt.Errorf("tool %q is disabled", matched.Name())
				}
				return matched, "request", nil
			}
		}
	}
	if t, group, lookupErr := LookupRequestMCPTool(ctx, name); lookupErr != nil {
		return nil, "", lookupErr
	} else if t != nil {
		if isToolDisabled(ctx, name, t.Name()) {
			return nil, "", fmt.Errorf("tool %q is disabled", t.Name())
		}
		if server, mcp := mcpServerNameFromGroup(group); mcp && !isMCPServerAccessible(ctx, server) {
			return nil, "", fmt.Errorf("access denied: MCP server %q is not available for your team", server)
		}
		return t, group, nil
	}
	if r.index != nil {
		if entry := r.index.GetToolEntry(resolved); entry != nil && entry.Tool != nil {
			if qualified && name != entry.GroupName+"/"+entry.Name {
				return nil, "", fmt.Errorf("tool %q was not found in the available catalog", name)
			}
			if isToolDisabled(ctx, name, entry.Name) {
				return nil, "", fmt.Errorf("tool %q is disabled", entry.Name)
			}
			if server, mcp := mcpServerNameFromGroup(entry.GroupName); mcp && !isMCPServerAccessible(ctx, server) {
				return nil, "", fmt.Errorf("access denied: MCP server %q is not available for your team", server)
			}
			return entry.Tool, entry.GroupName, nil
		}
	}
	return nil, "", fmt.Errorf("tool %q was not found in the available catalog", name)
}

func isToolDisabled(ctx agent.ReadonlyContext, names ...string) bool {
	disabled := store.DisabledToolsFromContext(ctx)
	for _, candidate := range names {
		for _, name := range disabled {
			if candidate == name {
				return true
			}
		}
	}
	return false
}

type runnableDeferredTool interface {
	tool.Tool
	Run(agent.ToolContext, any) (map[string]any, error)
}

type executeToolBridge struct {
	resolver deferredToolResolver
}

func (b *executeToolBridge) Name() string { return executeToolName }
func (b *executeToolBridge) Description() string {
	return "Execute one deferred catalog tool by the exact reference returned from describe_tools, with its required arguments."
}
func (b *executeToolBridge) IsLongRunning() bool { return false }
func (b *executeToolBridge) ProcessRequest(_ agent.ToolContext, req *model.LLMRequest) error {
	packToolIntoRequest(req, b)
	return nil
}
func (b *executeToolBridge) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        b.Name(),
		Description: b.Description(),
		ParametersJsonSchema: map[string]any{
			"type":     "object",
			"required": []string{"name", "arguments"},
			"properties": map[string]any{
				"name":      map[string]any{"type": "string"},
				"arguments": map[string]any{"type": "object", "additionalProperties": true},
			},
		},
	}
}
func (b *executeToolBridge) Run(ctx agent.ToolContext, raw any) (map[string]any, error) {
	args, err := decodeExecuteToolArgs(raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Name) == "" {
		// The provider delivered an execute_tool envelope without a
		// resolvable inner tool name (e.g. the arguments object was
		// fragmented or streamed truncated). Returning a precise,
		// actionable message lets the model re-issue the call correctly
		// instead of seeing the misleading generic "tool name is required".
		return nil, fmt.Errorf("execute_tool requires a %q field naming the tool to run (from describe_tools), with its parameters under %q", "name", "arguments")
	}
	resolved, _, err := b.resolver.resolve(ctx, args.Name)
	if err != nil {
		return nil, err
	}
	runner, ok := resolved.(runnableDeferredTool)
	if !ok {
		return nil, fmt.Errorf("tool %q is not executable", resolved.Name())
	}
	if err := common.ValidateToolArguments(resolved, args.Arguments); err != nil {
		return nil, err
	}
	return runner.Run(ctx, args.Arguments)
}

func decodeExecuteToolArgs(raw any) (ExecuteToolArgs, error) {
	switch args := raw.(type) {
	case ExecuteToolArgs:
		raw = map[string]any{"name": args.Name, "arguments": args.Arguments}
	case *ExecuteToolArgs:
		if args != nil {
			raw = map[string]any{"name": args.Name, "arguments": args.Arguments}
		}
	}
	name, arguments, err := common.NormalizeExecuteToolArgs(raw)
	if err == nil {
		// Canonicalize the invocation once so callback substitutions and the
		// runner share an argument object, including provider JSON strings.
		// The common decoder stays pure for persisted-event projection.
		if envelope, ok := raw.(map[string]any); ok {
			envelope["name"] = name
			envelope["arguments"] = arguments
		}
	}
	return ExecuteToolArgs{Name: name, Arguments: arguments}, err
}

// NewProgressiveToolBridge creates the fixed declarations used to inspect and
// execute deferred tools without changing the model-visible tool set.
func NewProgressiveToolBridge(index *ToolIndex) ([]tool.Tool, error) {
	return newProgressiveToolBridge(deferredToolResolver{index: index})
}

// NewScopedProgressiveToolBridge never resolves outside the immutable child scope.
func NewScopedProgressiveToolBridge(scope *ChildToolScope) ([]tool.Tool, error) {
	if scope == nil {
		return nil, fmt.Errorf("child tool scope is required")
	}
	return newProgressiveToolBridge(deferredToolResolver{scope: scope})
}

func newProgressiveToolBridge(resolver deferredToolResolver) ([]tool.Tool, error) {
	describe, err := functiontool.New(functiontool.Config{
		Name:        "describe_tools",
		Description: "Return descriptions and input schemas for deferred tools from search_tools. This does not add tool declarations.",
	}, func(ctx agent.ToolContext, args DescribeToolsArgs) (DescribeToolsResult, error) {
		result := DescribeToolsResult{Tools: []DescribedTool{}}
		for _, name := range args.Names {
			t, group, resolveErr := resolver.resolve(ctx, name)
			if resolveErr != nil {
				result.Errors = append(result.Errors, resolveErr.Error())
				continue
			}
			resolvedName := t.Name()
			if RequestMCPGroupsFromContext(ctx)[group] != nil {
				resolvedName = group + "/" + t.Name()
			}
			described := DescribedTool{Name: resolvedName, Description: t.Description(), GroupName: group}
			if declared, ok := t.(interface {
				Declaration() *genai.FunctionDeclaration
			}); ok {
				if declaration := declared.Declaration(); declaration != nil {
					described.InputSchema = declaration.ParametersJsonSchema
				}
			}
			result.Tools = append(result.Tools, described)
		}
		return result, nil
	})
	if err != nil {
		return nil, fmt.Errorf("create describe_tools: %w", err)
	}
	return []tool.Tool{describe, &executeToolBridge{resolver: resolver}}, nil
}

func (c *ChatAgent) effectiveToolCall(ctx agent.ReadonlyContext, called tool.Tool, args map[string]any) (tool.Tool, map[string]any) {
	if called == nil || called.Name() != executeToolName {
		return called, args
	}
	executeArgs, err := decodeExecuteToolArgs(args)
	if err != nil {
		return called, args
	}
	bridge, ok := called.(*executeToolBridge)
	if !ok {
		return called, args
	}
	resolved, _, err := bridge.resolver.resolve(ctx, executeArgs.Name)
	if err != nil {
		return called, args
	}
	return resolved, executeArgs.Arguments
}
