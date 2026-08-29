package agent

import (
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

const executeToolName = "execute_tool"

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
}

func (r deferredToolResolver) resolve(ctx agent.ReadonlyContext, name string) (tool.Tool, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", fmt.Errorf("tool name is required")
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
	resolved, _, err := b.resolver.resolve(ctx, args.Name)
	if err != nil {
		return nil, err
	}
	runner, ok := resolved.(runnableDeferredTool)
	if !ok {
		return nil, fmt.Errorf("tool %q is not executable", resolved.Name())
	}
	return runner.Run(ctx, args.Arguments)
}

func decodeExecuteToolArgs(raw any) (ExecuteToolArgs, error) {
	switch args := raw.(type) {
	case ExecuteToolArgs:
		return args, nil
	case *ExecuteToolArgs:
		if args != nil {
			return *args, nil
		}
	case map[string]any:
		name, _ := args["name"].(string)
		arguments, _ := args["arguments"].(map[string]any)
		if arguments == nil {
			arguments = map[string]any{}
		}
		return ExecuteToolArgs{Name: name, Arguments: arguments}, nil
	}
	return ExecuteToolArgs{}, fmt.Errorf("invalid execute_tool arguments")
}

// NewProgressiveToolBridge creates the fixed declarations used to inspect and
// execute deferred tools without changing the model-visible tool set.
func NewProgressiveToolBridge(index *ToolIndex) ([]tool.Tool, error) {
	resolver := deferredToolResolver{index: index}
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
