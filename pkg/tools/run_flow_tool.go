package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/flowstore"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// FlowRunnerAccess provides the ability to execute flows from the chat agent.
// Implemented by the daemon/launcher layer which has access to the full
// tool and provider infrastructure.
type FlowRunnerAccess interface {
	// RunFlow starts or resumes a flow execution from a filesystem path.
	// - flowPath: absolute path to the flow YAML file
	// - parameters: input node values (node_name -> value)
	// - inputResponse: response to a mid-flow input node (empty on first call)
	// - sessionKey: unique key to track stateful execution across calls
	// Returns the execution result.
	RunFlow(ctx context.Context, flowPath string, parameters map[string]string, inputResponse string, sessionKey string) (*FlowRunResult, error)

	// RunFlowFromYAML starts or resumes a flow execution from raw YAML content.
	// Used in platform mode where flows are stored in the database, not on disk.
	// - yamlContent: raw YAML definition of the flow
	// - flowName: logical name of the flow (for logging/session tracking)
	// - parameters: input node values (node_name -> value)
	// - inputResponse: response to a mid-flow input node (empty on first call)
	// - sessionKey: unique key to track stateful execution across calls
	RunFlowFromYAML(ctx context.Context, yamlContent string, flowName string, parameters map[string]string, inputResponse string, sessionKey string) (*FlowRunResult, error)

	// GetPausedNode returns the name of the input node the flow is currently
	// paused on, or "" if no session exists or the flow is not paused.
	GetPausedNode(sessionKey string) string

	// GetPausedOptions returns the resolved options for the currently paused
	// input node, or nil if there are no options (free text input).
	GetPausedOptions(sessionKey string) []string

	// CleanupSession removes any state for a completed/abandoned flow session.
	CleanupSession(sessionKey string)
}

// FlowRunResult describes the outcome of a flow execution step.
type FlowRunResult struct {
	Status  string `json:"status"`  // "completed", "needs_parameters", "waiting_for_input", "error"
	Output  string `json:"output"`  // Collected output text so far
	Message string `json:"message"` // Human-readable status message

	// For "needs_parameters" status:
	Parameters []FlowParameter `json:"parameters,omitempty"`

	// For "waiting_for_input" status:
	InputNode    string   `json:"input_node,omitempty"`    // Name of the input node
	InputPrompt  string   `json:"input_prompt,omitempty"`  // Prompt text for the user
	InputOptions []string `json:"input_options,omitempty"` // Selection options (empty = free text)

	// Execution trace:
	NodesVisited []string `json:"nodes_visited,omitempty"` // Ordered list of nodes executed
}

// FlowParameter describes a required input parameter for a flow.
type FlowParameter struct {
	NodeName string   `json:"node_name"`         // Node name (used as parameter key)
	Prompt   string   `json:"prompt"`            // Prompt text describing what's needed
	Fields   []string `json:"fields,omitempty"`  // Output model field names
	Options  []string `json:"options,omitempty"` // Selection options (empty = free text)
}

// flowRunnerAccessVar holds the injected flow runner implementation.
var flowRunnerAccessVar FlowRunnerAccess

// SetFlowRunnerAccess wires the flow runner implementation.
func SetFlowRunnerAccess(fra FlowRunnerAccess) {
	flowRunnerAccessVar = fra
}

// flowRunnerContextKey carries presence separately from the runner: an explicitly
// absent scoped runner must never select the daemon-global runner.
type flowRunnerContextKey struct{}
type scopedFlowRunner struct{ runner FlowRunnerAccess }

// WithFlowRunnerContext binds execution authority for run_flow. The supplied
// runner must use only this runtime's policy/budget-bound model and tools.
// A nil runner explicitly disables flow execution, including host fallback.
func WithFlowRunnerContext(ctx context.Context, runner FlowRunnerAccess) context.Context {
	return context.WithValue(ctx, flowRunnerContextKey{}, scopedFlowRunner{runner: runner})
}

func flowRunnerFromContext(ctx context.Context) (FlowRunnerAccess, bool) {
	if ctx != nil {
		if scoped, ok := ctx.Value(flowRunnerContextKey{}).(scopedFlowRunner); ok {
			return scoped.runner, true
		}
	}
	if requiresScopedFlowDrillExecution(ctx) {
		return nil, true
	}
	return flowRunnerAccessVar, false
}

// Authenticated platform calls must explicitly supply execution authority. A
// missing binding is not permission to use a daemon runner or host executor.
// Legacy callers without an identity and local Code keep their host semantics.
func requiresScopedFlowDrillExecution(ctx context.Context) bool {
	principal, present := execution.PrincipalFromContext(ctx)
	return present && principal.Kind != execution.PrincipalKindLocal
}

// --- run_flow tool ---

// RunFlowArgs defines the arguments for the run_flow tool.
type RunFlowArgs struct {
	FlowName   string            `json:"flow_name" jsonschema:"Name of the flow to execute (as returned by search_flows)"`
	Parameters map[string]string `json:"parameters,omitempty" jsonschema:"Input values keyed by node_name. For initial parameters and mid-flow inputs alike, use the node_name returned in the response as the key."`
}

// RunFlowResult is returned from run_flow.
type RunFlowResult struct {
	Status       string          `json:"status"`                  // "completed", "needs_parameters", "waiting_for_input", "error"
	Output       string          `json:"output,omitempty"`        // Flow output text
	Message      string          `json:"message,omitempty"`       // Human-readable status message
	Parameters   []FlowParameter `json:"parameters,omitempty"`    // Required parameters (when status=needs_parameters)
	InputNode    string          `json:"input_node,omitempty"`    // Current input node (when status=waiting_for_input)
	InputPrompt  string          `json:"input_prompt,omitempty"`  // Prompt for user (when status=waiting_for_input)
	InputOptions []string        `json:"input_options,omitempty"` // Options for selection (when status=waiting_for_input)
}

func runFlow(ctx tool.Context, args RunFlowArgs) (RunFlowResult, error) {
	runner, scoped := flowRunnerFromContext(ctx)
	if scoped {
		if err := ctx.Err(); err != nil {
			return RunFlowResult{}, err
		}
		if runner == nil {
			return RunFlowResult{}, fmt.Errorf("scoped flow execution is unavailable")
		}
		if getEffectiveFlowStore(ctx) == nil {
			return RunFlowResult{}, fmt.Errorf("scoped flow store is unavailable; filesystem fallback disabled")
		}
	}
	if runner == nil {
		return RunFlowResult{
			Status:  "error",
			Message: "Flow execution is not available — the daemon must be running.",
		}, nil
	}

	if args.FlowName == "" {
		return RunFlowResult{
			Status:  "error",
			Message: "flow_name is required. Use search_flows to find available flows.",
		}, nil
	}

	// Platform mode: load and run flow from the database store.
	if fs := getEffectiveFlowStore(ctx); fs != nil {
		return runFlowFromStore(ctx, fs, args)
	}

	// Personal mode: resolve from filesystem.
	// Resolve the flow YAML path
	flowPath, err := resolveFlowFilePath(args.FlowName)
	if err != nil {
		return RunFlowResult{
			Status:  "error",
			Message: fmt.Sprintf("Flow %q not found: %v", args.FlowName, err),
		}, nil
	}

	// Reject drills — they have different execution semantics
	if agentCfg, loadErr := config.LoadAgent(flowPath); loadErr == nil {
		if agentCfg.Type == "drill" || agentCfg.Type == "drill_suite" {
			return RunFlowResult{
				Status:  "error",
				Message: fmt.Sprintf("%q is a drill, not a flow. Use the drill commands to run drills.", args.FlowName),
			}, nil
		}
	}

	// Build a session key for stateful tracking
	sessionKey := fmt.Sprintf("flow:%s:%s", args.FlowName, ctx.SessionID())

	// Check if there's a paused session waiting for input.
	// If so, extract the value from parameters and resume.
	var inputResponse string
	if pausedNode := runner.GetPausedNode(sessionKey); pausedNode != "" {
		if val, ok := args.Parameters[pausedNode]; ok {
			inputResponse = val
		}

		// Validate against available options when the paused node has
		// a fixed set of choices (selection-type input).
		if pausedOptions := runner.GetPausedOptions(sessionKey); len(pausedOptions) > 0 {
			if inputResponse == "" {
				return RunFlowResult{
					Status:       "waiting_for_input",
					Message:      fmt.Sprintf("Missing required parameter %q. The user must choose one of the available options.", pausedNode),
					InputNode:    pausedNode,
					InputOptions: pausedOptions,
				}, nil
			}
			validChoice := false
			for _, opt := range pausedOptions {
				if strings.EqualFold(inputResponse, opt) {
					inputResponse = opt // normalize to exact option text
					validChoice = true
					break
				}
			}
			if !validChoice {
				return RunFlowResult{
					Status:       "waiting_for_input",
					Message:      fmt.Sprintf("Invalid selection %q. The user must choose one of the available options. Ask them to pick one.", inputResponse),
					InputNode:    pausedNode,
					InputOptions: pausedOptions,
				}, nil
			}
		}

		if inputResponse == "" {
			return RunFlowResult{
				Status:  "waiting_for_input",
				Message: fmt.Sprintf("The flow is waiting for input on node %q. Provide the value in parameters[\"%s\"].", pausedNode, pausedNode),
			}, nil
		}
	}

	// No paused session — this is a new/initial execution.
	// Check if the flow needs parameters that weren't provided.
	if inputResponse == "" && len(args.Parameters) == 0 {
		params, scanErr := scanFlowParameters(flowPath)
		if scanErr == nil && len(params) > 0 {
			return RunFlowResult{
				Status:     "needs_parameters",
				Message:    fmt.Sprintf("Flow %q requires the following parameters. Ask the user for each one, then call run_flow again with all parameters filled in.", args.FlowName),
				Parameters: params,
			}, nil
		}
	}

	// Execute the flow
	result, err := runner.RunFlow(
		ctx,
		flowPath,
		args.Parameters,
		inputResponse,
		sessionKey,
	)
	if err != nil {
		if scoped {
			return RunFlowResult{}, err
		}
		return RunFlowResult{
			Status:  "error",
			Message: fmt.Sprintf("Flow execution failed: %v", err),
		}, nil
	}

	// Map the internal result to the tool result
	toolResult := RunFlowResult{
		Status:       result.Status,
		Output:       result.Output,
		Message:      result.Message,
		InputNode:    result.InputNode,
		InputPrompt:  result.InputPrompt,
		InputOptions: result.InputOptions,
		Parameters:   result.Parameters,
	}

	// Clean up completed/errored sessions
	if result.Status == "completed" || result.Status == "error" {
		runner.CleanupSession(sessionKey)
	}

	return toolResult, nil
}

// resolveFlowFilePath resolves a flow name to its filesystem path.
func resolveFlowFilePath(name string) (string, error) {
	// Try with .yaml extension
	flowFile := name
	if !strings.HasSuffix(flowFile, ".yaml") {
		flowFile = name + ".yaml"
	}

	// Check flows directory
	flowsDir, err := flowstore.GetFlowsDir()
	if err != nil {
		return "", fmt.Errorf("could not determine flows directory: %w", err)
	}

	path := flowsDir + "/" + flowFile
	// Verify the file exists by trying to load it
	if _, loadErr := config.LoadAgent(path); loadErr == nil {
		return path, nil
	}

	// Try legacy agents directory
	configDir, cdErr := config.GetConfigDir()
	if cdErr == nil {
		legacyPath := configDir + "/agents/" + flowFile
		if _, loadErr := config.LoadAgent(legacyPath); loadErr == nil {
			return legacyPath, nil
		}
	}

	return "", fmt.Errorf("flow file %q not found in flows directory %q", flowFile, flowsDir)
}

// scanFlowParameters loads a flow YAML and extracts initial input node parameters.
// Only returns input nodes reachable before any LLM/tool node (the "initial" inputs).
func scanFlowParameters(flowPath string) ([]FlowParameter, error) {
	agentCfg, err := config.LoadAgent(flowPath)
	if err != nil {
		return nil, err
	}
	return scanFlowParametersFromConfig(agentCfg), nil
}

// scanFlowParametersFromConfig extracts initial input node parameters from a
// parsed agent config. Shared by the filesystem and platform-mode paths.
func scanFlowParametersFromConfig(agentCfg *config.AgentConfig) []FlowParameter {
	// Build flow graph adjacency
	adj := make(map[string]string)
	for _, fi := range agentCfg.Flow {
		if fi.To != "" {
			adj[fi.From] = fi.To
		} else if len(fi.Edges) > 0 {
			// For conditional edges, take the first one as default path
			adj[fi.From] = fi.Edges[0].To
		}
	}

	// Build node lookup
	nodeMap := make(map[string]*config.Node)
	for i := range agentCfg.Nodes {
		nodeMap[agentCfg.Nodes[i].Name] = &agentCfg.Nodes[i]
	}

	// Walk from START, collecting consecutive input nodes
	var params []FlowParameter
	current := "START"
	visited := make(map[string]bool)

	for i := 0; i < 50; i++ { // safety limit
		next, ok := adj[current]
		if !ok || next == "END" || next == "" {
			break
		}
		if visited[next] {
			break
		}
		visited[next] = true

		node, exists := nodeMap[next]
		if !exists {
			break
		}

		// Stop at non-input nodes — these are the "initial" parameters
		if node.Type != "input" {
			break
		}

		param := FlowParameter{
			NodeName: node.Name,
			Prompt:   node.Prompt,
		}

		// Extract field names from output_model
		for field := range node.OutputModel {
			param.Fields = append(param.Fields, field)
		}

		// Extract options
		param.Options = node.Options

		params = append(params, param)
		current = next
	}

	return params
}

// runFlowFromStore handles flow execution in platform mode, where flows are
// stored in the database rather than on the filesystem.
func runFlowFromStore(ctx tool.Context, fs store.FlowStore, args RunFlowArgs) (RunFlowResult, error) {
	runner, scoped := flowRunnerFromContext(ctx)
	if runner == nil {
		return RunFlowResult{}, fmt.Errorf("flow execution is unavailable")
	}
	// Load flow YAML from the database.
	yamlContent, err := fs.GetFlow(ctx, args.FlowName)
	if err != nil {
		return RunFlowResult{
			Status:  "error",
			Message: fmt.Sprintf("Flow %q not found: %v", args.FlowName, err),
		}, nil
	}

	// Parse to check type and extract parameters.
	agentCfg, parseErr := config.LoadAgentFromBytes([]byte(yamlContent))
	if parseErr != nil {
		return RunFlowResult{
			Status:  "error",
			Message: fmt.Sprintf("Failed to parse flow %q: %v", args.FlowName, parseErr),
		}, nil
	}

	// Reject drills — they have different execution semantics.
	if agentCfg.Type == "drill" || agentCfg.Type == "drill_suite" {
		return RunFlowResult{
			Status:  "error",
			Message: fmt.Sprintf("%q is a drill, not a flow. Use the drill commands to run drills.", args.FlowName),
		}, nil
	}

	// Build a session key for stateful tracking.
	sessionKey := fmt.Sprintf("flow:%s:%s", args.FlowName, ctx.SessionID())

	// Check if there's a paused session waiting for input.
	var inputResponse string
	if pausedNode := runner.GetPausedNode(sessionKey); pausedNode != "" {
		if val, ok := args.Parameters[pausedNode]; ok {
			inputResponse = val
		}

		// Validate against available options.
		if pausedOptions := runner.GetPausedOptions(sessionKey); len(pausedOptions) > 0 {
			if inputResponse == "" {
				return RunFlowResult{
					Status:       "waiting_for_input",
					Message:      fmt.Sprintf("Missing required parameter %q. The user must choose one of the available options.", pausedNode),
					InputNode:    pausedNode,
					InputOptions: pausedOptions,
				}, nil
			}
			validChoice := false
			for _, opt := range pausedOptions {
				if strings.EqualFold(inputResponse, opt) {
					inputResponse = opt
					validChoice = true
					break
				}
			}
			if !validChoice {
				return RunFlowResult{
					Status:       "waiting_for_input",
					Message:      fmt.Sprintf("Invalid selection %q. The user must choose one of the available options. Ask them to pick one.", inputResponse),
					InputNode:    pausedNode,
					InputOptions: pausedOptions,
				}, nil
			}
		}

		if inputResponse == "" {
			return RunFlowResult{
				Status:  "waiting_for_input",
				Message: fmt.Sprintf("The flow is waiting for input on node %q. Provide the value in parameters[\"%s\"].", pausedNode, pausedNode),
			}, nil
		}
	}

	// No paused session — check if the flow needs parameters that weren't provided.
	if inputResponse == "" && len(args.Parameters) == 0 {
		params := scanFlowParametersFromConfig(agentCfg)
		if len(params) > 0 {
			return RunFlowResult{
				Status:     "needs_parameters",
				Message:    fmt.Sprintf("Flow %q requires the following parameters. Ask the user for each one, then call run_flow again with all parameters filled in.", args.FlowName),
				Parameters: params,
			}, nil
		}
	}

	// Execute the flow from YAML content.
	result, err := runner.RunFlowFromYAML(
		ctx,
		yamlContent,
		args.FlowName,
		args.Parameters,
		inputResponse,
		sessionKey,
	)
	if err != nil {
		if scoped {
			return RunFlowResult{}, err
		}
		return RunFlowResult{
			Status:  "error",
			Message: fmt.Sprintf("Flow execution failed: %v", err),
		}, nil
	}

	// Map the internal result to the tool result.
	toolResult := RunFlowResult{
		Status:       result.Status,
		Output:       result.Output,
		Message:      result.Message,
		InputNode:    result.InputNode,
		InputPrompt:  result.InputPrompt,
		InputOptions: result.InputOptions,
		Parameters:   result.Parameters,
	}

	// Clean up completed/errored sessions.
	if result.Status == "completed" || result.Status == "error" {
		runner.CleanupSession(sessionKey)
	}

	return toolResult, nil
}

// NewRunFlowTool creates the run_flow tool.
func NewRunFlowTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "run_flow",
		Description: "Execute a saved flow/workflow by name. Call search_flows first to find available flows. " +
			"On first call without parameters, returns needed parameters with their node_name — ask the user for each one, then call again with parameters filled in. " +
			"When a mid-flow input is needed, returns input_node and input_options — present them to the user, then call again with parameters: {input_node: user_choice}. " +
			"When input_options are present, the user MUST pick one of the listed options exactly. If they don't, ask them to choose from the list. " +
			"When input_options are empty, it's free text — pass the user's exact words. " +
			"The output field is delivered directly to the user's screen — do NOT reproduce, summarize, or paraphrase it. " +
			"Just present any input_options/input_prompt for the next step, or confirm the flow completed.",
	}, runFlow)
}
