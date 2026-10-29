package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/common"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// FlowValidationResult mirrors api.FlowValidationResult to avoid import cycles.
type FlowValidationResult struct {
	Valid  bool
	Errors []string
}

// DistillerToolInfo mirrors api.ToolInfo to avoid import cycles.
type DistillerToolInfo struct {
	Name        string
	Description string
	Source      string
	Schema      json.RawMessage // JSON schema of tool parameters (nil if unavailable)
}

// FlowDryRunResult mirrors api.FlowDryRunResult to avoid import cycles.
type FlowDryRunResult struct {
	Valid    bool
	Warnings []string
	Errors   []string
}

// FlowDistiller converts execution traces into YAML flow definitions.
type FlowDistiller struct {
	LLM          func(ctx context.Context, prompt string) (string, error)
	Tools        []tool.Tool    // For building available tools list
	Toolsets     []tool.Toolset // For building available tools list
	MaxRetries   int            // Default: 3
	GetSchema    func() string  // Returns the flow schema string
	ValidateYAML func(yamlStr string, tools []DistillerToolInfo) FlowValidationResult
	DryRunYAML   func(yamlStr string, toolSchemas map[string]json.RawMessage) FlowDryRunResult
}

// internalOnlyTools lists tools that exist only for the chat agent's
// internal use and must never appear in distilled flows.
var internalOnlyTools = map[string]bool{
	"memory_save":    true,
	"delegate_tasks": true,
	"skill_lookup":   true,
}

// NewFlowDistiller creates a FlowDistiller that uses the provided ADK model.LLM.
// The getSchema and validateYAML functions break the import cycle with pkg/api.
func NewFlowDistiller(
	llm model.LLM,
	tools []tool.Tool,
	toolsets []tool.Toolset,
	getSchema func() string,
	validateYAML func(yamlStr string, tools []DistillerToolInfo) FlowValidationResult,
	dryRunYAML func(yamlStr string, toolSchemas map[string]json.RawMessage) FlowDryRunResult,
) *FlowDistiller {
	return &FlowDistiller{
		LLM: func(ctx context.Context, prompt string) (string, error) {
			// Prefer per-request LLM override (team-specific model) over factory default.
			effectiveLLM := LLMFromContext(ctx)
			if effectiveLLM == nil {
				effectiveLLM = llm
			}

			req := &model.LLMRequest{
				Contents: []*genai.Content{
					{
						Parts: []*genai.Part{{Text: prompt}},
						Role:  "user",
					},
				},
			}
			var text string
			for resp, err := range effectiveLLM.GenerateContent(ctx, req, false) {
				if err != nil {
					return text, err
				}
				if resp.Content != nil {
					for _, p := range resp.Content.Parts {
						if p.Text != "" {
							text += p.Text
						}
					}
				}
			}
			if text == "" {
				return "", fmt.Errorf("empty response from LLM")
			}
			return text, nil
		},
		Tools:        tools,
		Toolsets:     toolsets,
		MaxRetries:   3,
		GetSchema:    getSchema,
		ValidateYAML: validateYAML,
		DryRunYAML:   dryRunYAML,
	}
}

// DistillRequest holds the inputs for flow distillation.
type DistillRequest struct {
	UserRequest string
	Trace       *ExecutionTrace
}

// DistillResult holds the output of flow distillation.
type DistillResult struct {
	YAML        string   // The generated YAML flow
	FlowName    string   // Suggested filename (snake_case, no extension)
	Description string   // Human-readable description for registry
	Tags        []string // For registry indexing
	Explanation string   // Human-readable explanation of the flow structure
}

// DistillModifyRequest holds the inputs for modifying a distilled flow.
type DistillModifyRequest struct {
	CurrentYAML    string // The current YAML to modify
	ChangeRequest  string // What the user wants changed
	OriginalTrace  *ExecutionTrace
	History        []string // Previous modification requests for context
	DryRunOutput   string   // Output from last test run (for context)
	DryRunError    string   // Error from last test run (for diagnosis)
}

// Distill converts an execution trace into a YAML flow definition.
func (d *FlowDistiller) Distill(ctx context.Context, req DistillRequest) (*DistillResult, error) {
	maxRetries := d.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	// Build available tools list for validation
	availableTools := d.buildToolInfoList()

	// Build tool schema map for dry-run validation
	toolSchemas := d.buildToolSchemaMap(availableTools)

	var lastYAML string
	var lastErrors []string

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Build prompt (include validation errors on retry)
		prompt := d.buildDistillationPrompt(req, lastYAML, lastErrors)

		// Call LLM
		response, err := d.LLM(ctx, prompt)
		if err != nil {
			return nil, fmt.Errorf("LLM call failed on attempt %d: %w", attempt+1, err)
		}

		// Extract YAML from response
		yamlStr := extractYAMLBlock(response)
		if yamlStr == "" {
			lastErrors = []string{"No YAML code block found in response. Please wrap the flow in ```yaml ... ``` markers."}
			lastYAML = ""
			continue
		}

		// Schema validation
		if d.ValidateYAML != nil {
			validation := d.ValidateYAML(yamlStr, availableTools)
			if !validation.Valid {
				lastYAML = yamlStr
				lastErrors = validation.Errors
				continue
			}
		}

		// Semantic dry-run validation
		if d.DryRunYAML != nil {
			dryRun := d.DryRunYAML(yamlStr, toolSchemas)
			if !dryRun.Valid {
				lastYAML = yamlStr
				// Combine errors with context about what dry-run checks
				var dryRunErrors []string
				for _, e := range dryRun.Errors {
					dryRunErrors = append(dryRunErrors, "[dry-run] "+e)
				}
				for _, w := range dryRun.Warnings {
					dryRunErrors = append(dryRunErrors, "[dry-run warning] "+w)
				}
				lastErrors = dryRunErrors
				continue
			}
		}

		// Extract metadata and explanation from response
		result := &DistillResult{YAML: yamlStr}
		d.extractMetadata(response, result)
		result.Explanation = extractExplanationBlock(response)

		return result, nil
	}

	// All retries exhausted
	if lastYAML != "" {
		// Return the last attempt with a warning
		result := &DistillResult{YAML: lastYAML}
		d.extractMetadata("", result)
		if result.Description == "" {
			result.Description = "Generated flow (may have validation warnings)"
		}
		return result, fmt.Errorf("flow generated but has validation errors after %d attempts: %s",
			maxRetries, strings.Join(lastErrors, "; "))
	}

	return nil, fmt.Errorf("failed to generate valid YAML flow after %d attempts", maxRetries)
}

// buildToolSchemaMap creates a map of tool name -> JSON parameter schema
// for use in dry-run validation.
func (d *FlowDistiller) buildToolSchemaMap(tools []DistillerToolInfo) map[string]json.RawMessage {
	schemas := make(map[string]json.RawMessage)
	for _, t := range tools {
		if t.Schema != nil {
			schemas[t.Name] = t.Schema
		}
	}
	return schemas
}

// buildDistillationPrompt constructs the prompt for the LLM.
func (d *FlowDistiller) buildDistillationPrompt(req DistillRequest, previousYAML string, validationErrors []string) string {
	var sb strings.Builder

	sb.WriteString("# Task\n\n")
	sb.WriteString("Convert this successful execution trace into a reusable Astonish YAML flow.\n\n")

	// Include the flow schema
	if d.GetSchema != nil {
		sb.WriteString("# Astonish Flow Schema\n\n")
		sb.WriteString(d.GetSchema())
	}

	// Collect tool names used in the trace
	tracedTools := make(map[string]bool)
	for _, step := range req.Trace.SuccessfulSteps() {
		if !internalOnlyTools[step.ToolName] {
			tracedTools[step.ToolName] = true
		}
	}

	// Available tools — full list for reference, schemas for traced tools only
	toolInfos := d.buildToolInfoList()
	if len(toolInfos) > 0 {
		sb.WriteString("\n\n# Available Tools\n\nONLY use tools from this list:\n")
		for _, t := range toolInfos {
			if internalOnlyTools[t.Name] {
				continue
			}
			sb.WriteString(fmt.Sprintf("- %s: %s (source: %s)\n", t.Name, t.Description, t.Source))
		}

		// Include parameter schemas for tools that were actually used in the trace
		sb.WriteString("\n## Parameter Schemas (for tools used in this execution)\n\n")
		sb.WriteString("When generating `tool` type nodes, the `args` keys MUST exactly match the parameter names below.\n")
		sb.WriteString("When generating `llm` type nodes with `tools: true`, the LLM will receive these schemas at runtime.\n\n")
		for _, t := range toolInfos {
			if !tracedTools[t.Name] {
				continue
			}
			if t.Schema != nil {
				// Pretty-print the schema
				var prettySchema json.RawMessage
				if err := json.Unmarshal(t.Schema, &prettySchema); err == nil {
					formatted, _ := json.MarshalIndent(prettySchema, "", "  ")
					sb.WriteString(fmt.Sprintf("### %s\n```json\n%s\n```\n\n", t.Name, string(formatted)))
				}
			} else {
				sb.WriteString(fmt.Sprintf("### %s\n(no schema available — use the arg names from the trace below)\n\n", t.Name))
			}
		}
	}

	// Original user request
	sb.WriteString("\n\n# Original User Request\n\n")
	sb.WriteString(req.UserRequest)

	// Execution trace (successful steps only, excluding internal-only tools)
	steps := req.Trace.SuccessfulSteps()
	sb.WriteString("\n\n# Execution Trace (successful steps)\n\n")
	stepNum := 0
	for _, step := range steps {
		if internalOnlyTools[step.ToolName] {
			continue
		}
		stepNum++
		argsJSON, _ := json.MarshalIndent(step.ToolArgs, "", "  ")
		sb.WriteString(fmt.Sprintf("Step %d: Called **%s**\n", stepNum, step.ToolName))
		sb.WriteString(fmt.Sprintf("  Args: %s\n", string(argsJSON)))
		if step.DurationMs > 0 {
			sb.WriteString(fmt.Sprintf("  Duration: %dms\n", step.DurationMs))
		}
		// Include truncated result to show the approach worked
		if step.ToolResult != nil {
			resultJSON, err := json.Marshal(step.ToolResult)
			if err == nil {
				resultStr := string(resultJSON)
				if len(resultStr) > 500 {
					resultStr = resultStr[:500] + "... (truncated)"
				}
				sb.WriteString(fmt.Sprintf("  Result (truncated): %s\n", resultStr))
			}
		}
		sb.WriteString("\n")
	}

	// Faithful Replication Instructions (most important section)
	sb.WriteString("# CRITICAL: Faithful Replication Rules\n\n")
	sb.WriteString("The distilled flow MUST faithfully replicate the exact sequence of tool calls from the execution trace above.\n\n")
	sb.WriteString("**DO:**\n")
	sb.WriteString("- Replicate the EXACT same tool calls that succeeded, in the SAME order\n")
	sb.WriteString("- Parameterize only literal values that would change between runs (e.g., server names, URLs, regions)\n")
	sb.WriteString("- Use the same tool for each step as was used in the original execution\n")
	sb.WriteString("- Preserve the approach that worked — if the original used `{{CREDENTIAL:name:field}}` placeholders directly in commands, keep that pattern (the system substitutes real values automatically at execution time)\n\n")
	sb.WriteString("**DO NOT:**\n")
	sb.WriteString("- Add steps that weren't in the original execution (no extra helper/preparatory/validation steps)\n")
	sb.WriteString("- Remove or skip steps that were in the original execution\n")
	sb.WriteString("- Reorder the steps\n")
	sb.WriteString("- Replace a single tool call with multiple calls\n")
	sb.WriteString("- Add error-handling or retry logic that wasn't in the original\n")
	sb.WriteString("- \"Improve\" upon the original approach — it worked as-is, replicate it\n\n")

	// Shell command guidance
	sb.WriteString("# Shell Command Handling\n\n")
	sb.WriteString("For steps that call `shell_command`:\n")
	sb.WriteString("- ALWAYS use an `llm` node with `tools: true` and `tools_selection: [shell_command]`\n")
	sb.WriteString("- NEVER use a `tool` node with hardcoded command args for shell scripts\n")
	sb.WriteString("- For SIMPLE commands (single-line: `ls`, `cat`, `grep`, `find`, `mkdir`, `cp`):\n")
	sb.WriteString("  - Do NOT use `raw_context` — just describe the task in `prompt`\n")
	sb.WriteString("  - The LLM can easily generate simple one-line commands without a reference script\n")
	sb.WriteString("- For COMPLEX scripts (multi-line, pipes, awk, jq, variable expansion `${VAR}`, curl with JSON bodies, loops):\n")
	sb.WriteString("  - Include the EXACT working script from the trace in the `raw_context` field\n")
	sb.WriteString("  - `raw_context` is appended to the system instruction WITHOUT state variable interpolation\n")
	sb.WriteString("  - This preserves shell syntax like `${VAR}`, `{print $2}`, jq `{...}` intact\n")
	sb.WriteString("  - In `raw_context`, preface the script with: \"Execute EXACTLY this proven script.\"\n")
	sb.WriteString("- In the `prompt` field, always describe the task with `{parameter}` references for state variables\n\n")
	sb.WriteString("Example (complex script needing raw_context):\n")
	sb.WriteString("```yaml\n")
	sb.WriteString("- name: fetch_data\n")
	sb.WriteString("  type: llm\n")
	sb.WriteString("  system: \"You are an infrastructure automation assistant.\"\n")
	sb.WriteString("  prompt: \"Authenticate with credential {credential_name} in region {region} and list all resources\"\n")
	sb.WriteString("  raw_context: |\n")
	sb.WriteString("    Execute EXACTLY this proven script. Do NOT modify the approach or use alternatives.\n")
	sb.WriteString("    APP_CRED_ID=\"{{CREDENTIAL:<credential_name>:username}}\"\n")
	sb.WriteString("    APP_CRED_SECRET=\"{{CREDENTIAL:<credential_name>:password}}\"\n")
	sb.WriteString("    TOKEN=$(curl -s -X POST \"$AUTH_URL/auth/tokens\" -d @payload.json | jq -r '.token')\n")
	sb.WriteString("    curl -s -H \"X-Auth-Token: $TOKEN\" \"${ENDPOINT}/v2/resources\" | jq '.resources[]'\n")
	sb.WriteString("  tools: true\n")
	sb.WriteString("  tools_selection:\n")
	sb.WriteString("    - shell_command\n")
	sb.WriteString("```\n\n")

	// Additional instructions
	sb.WriteString("# CRITICAL: Flow Structure Requirements\n\n")
	sb.WriteString("Every generated flow MUST contain BOTH a `nodes:` section AND a `flow:` section.\n")
	sb.WriteString("The `flow:` section defines edges connecting nodes. Without it, the flow is INVALID.\n\n")
	sb.WriteString("```yaml\n")
	sb.WriteString("flow:\n")
	sb.WriteString("  - from: START\n")
	sb.WriteString("    to: first_node\n")
	sb.WriteString("  - from: first_node\n")
	sb.WriteString("    to: second_node\n")
	sb.WriteString("  - from: last_node\n")
	sb.WriteString("    to: END\n")
	sb.WriteString("```\n\n")
	sb.WriteString("# Additional Instructions\n\n")
	sb.WriteString("1. Create a valid YAML flow with `nodes:` AND `flow:` sections\n")
	sb.WriteString("2. Replace literal values (IPs, paths, credentials, region names, IDs, URLs) with {parameter} variables\n")
	sb.WriteString("3. Create ONE input node PER parameter. Each input node collects exactly ONE value and has exactly ONE field in output_model. Do NOT combine multiple parameters into a single input node.\n")
	sb.WriteString("4. Each tool-calling step should be an LLM node with tools: true\n")
	sb.WriteString("5. Use tools_selection to restrict to only the tools that were actually used\n")
	sb.WriteString("6. After tool execution, add an LLM processing node that formats the output\n")
	sb.WriteString("7. Add an output node to display the formatted result\n")
	sb.WriteString("8. The `flow:` section MUST connect START -> all nodes in order -> END\n")
	sb.WriteString("9. NEVER include memory_save, delegate_tasks, or skill_lookup in the flow — these are internal chat-mode tools\n")
	sb.WriteString("10. CREDENTIAL HANDLING: If the original execution used `{{CREDENTIAL:name:field}}` placeholders directly in commands, preserve that exact pattern.\n")
	sb.WriteString("    - Add an input node asking for the CREDENTIAL NAME (a reference to an already-saved credential in the encrypted store)\n")
	sb.WriteString("    - In shell commands, use the {{CREDENTIAL:{credential_var}:password}} pattern — the inner {credential_var} is resolved from state at prompt time, then the outer {{CREDENTIAL:...}} placeholder is substituted with the real secret at tool execution time\n")
	sb.WriteString("    - NEVER hardcode credential names in the YAML — always use a {variable} from an input node\n")
	sb.WriteString("    - NEVER add resolve_credential tool calls unless the original execution explicitly used resolve_credential\n\n")

	// Include actual output if captured, so the distiller can replicate the format
	if req.Trace.FinalOutput != "" {
		sb.WriteString("# Actual Output Produced During Execution\n\n")
		sb.WriteString("The formatting LLM node MUST replicate this exact output format.\n")
		sb.WriteString("Include ALL columns, sections, emojis, and formatting shown below in the system prompt of the formatting node.\n\n")
		sb.WriteString("```markdown\n")
		sb.WriteString(req.Trace.FinalOutput)
		sb.WriteString("\n```\n\n")
	}

	// If retrying, include previous errors
	if previousYAML != "" && len(validationErrors) > 0 {
		sb.WriteString("# IMPORTANT: Previous Attempt Failed Validation\n\n")
		sb.WriteString("Your previous YAML had these errors:\n")
		for _, e := range validationErrors {
			sb.WriteString(fmt.Sprintf("- %s\n", e))
		}
		sb.WriteString("\nPrevious YAML:\n```yaml\n")
		sb.WriteString(previousYAML)
		sb.WriteString("\n```\n\n")
		sb.WriteString("Fix ALL the errors above and generate a corrected YAML flow.\n\n")
	}

	// Response format
	sb.WriteString("# Response Format\n\n")
	sb.WriteString("First, provide metadata on separate lines:\n")
	sb.WriteString("flow_name: suggested_filename_in_snake_case\n")
	sb.WriteString("description: one-line human description of what this flow does\n")
	sb.WriteString("tags: comma, separated, tags\n\n")
	sb.WriteString("Then provide the complete YAML flow in a ```yaml code block.\n\n")
	sb.WriteString("After the YAML block, provide a brief explanation in this exact format:\n\n")
	sb.WriteString("```explanation\n")
	sb.WriteString("## Summary\n")
	sb.WriteString("<one sentence describing what this flow does>\n\n")
	sb.WriteString("## Nodes\n")
	sb.WriteString("- **<node_name>** (<type>): <what it does>\n")
	sb.WriteString("- ...\n\n")
	sb.WriteString("## Input Parameters\n")
	sb.WriteString("- **<param_name>**: <description> (example: `<example_value>`)\n")
	sb.WriteString("- ...\n\n")
	sb.WriteString("## Notes\n")
	sb.WriteString("<any important notes about the flow behavior, if applicable>\n")
	sb.WriteString("```\n")

	return sb.String()
}

// buildToolInfoList creates a DistillerToolInfo list from the available tools.
func (d *FlowDistiller) buildToolInfoList() []DistillerToolInfo {
	var infos []DistillerToolInfo

	for _, t := range d.Tools {
		infos = append(infos, DistillerToolInfo{
			Name:        t.Name(),
			Description: t.Description(),
			Source:      "internal",
			Schema:      common.ExtractToolInputSchema(t),
		})
	}

	if len(d.Toolsets) > 0 {
		ctx := &minimalReadonlyContext{Context: context.Background()}
		for _, ts := range d.Toolsets {
			mcpTools, err := ts.Tools(ctx)
			if err != nil {
				continue
			}
			for _, t := range mcpTools {
				infos = append(infos, DistillerToolInfo{
					Name:        t.Name(),
					Description: t.Description(),
					Source:      ts.Name(),
					Schema:      common.ExtractToolInputSchema(t),
				})
			}
		}
	}

	return infos
}

// extractMetadata parses flow_name, description, and tags from the LLM response text.
func (d *FlowDistiller) extractMetadata(response string, result *DistillResult) {
	lines := strings.Split(response, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "flow_name:") {
			result.FlowName = strings.TrimSpace(strings.TrimPrefix(line, "flow_name:"))
		} else if strings.HasPrefix(line, "description:") {
			result.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		} else if strings.HasPrefix(line, "tags:") {
			tagsStr := strings.TrimSpace(strings.TrimPrefix(line, "tags:"))
			for _, tag := range strings.Split(tagsStr, ",") {
				tag = strings.TrimSpace(tag)
				if tag != "" {
					result.Tags = append(result.Tags, tag)
				}
			}
		}
	}

	// Defaults
	if result.FlowName == "" {
		result.FlowName = "distilled_flow"
	}
	if result.Description == "" {
		result.Description = "Auto-generated flow from chat execution"
	}
}

// extractYAMLBlock extracts YAML content from a ```yaml code block.
func extractYAMLBlock(text string) string {
	startMarker := "```yaml"
	endMarker := "```"

	startIdx := strings.Index(text, startMarker)
	if startIdx == -1 {
		return ""
	}

	startIdx += len(startMarker)
	remaining := text[startIdx:]

	endIdx := strings.Index(remaining, endMarker)
	if endIdx == -1 {
		return ""
	}

	return strings.TrimSpace(remaining[:endIdx])
}

// extractExplanationBlock extracts the explanation content from a ```explanation code block.
func extractExplanationBlock(text string) string {
	startMarker := "```explanation"
	endMarker := "```"

	startIdx := strings.Index(text, startMarker)
	if startIdx == -1 {
		return ""
	}

	startIdx += len(startMarker)
	remaining := text[startIdx:]

	endIdx := strings.Index(remaining, endMarker)
	if endIdx == -1 {
		return ""
	}

	return strings.TrimSpace(remaining[:endIdx])
}

// ModifyFlow takes an existing flow YAML and modifies it based on the user's request.
// It returns the modified DistillResult with updated YAML and explanation.
func (d *FlowDistiller) ModifyFlow(ctx context.Context, req DistillModifyRequest) (*DistillResult, error) {
	maxRetries := d.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	availableTools := d.buildToolInfoList()
	toolSchemas := d.buildToolSchemaMap(availableTools)
	var lastYAML string
	var lastErrors []string

	for attempt := 0; attempt < maxRetries; attempt++ {
		prompt := d.buildModificationPrompt(req, lastYAML, lastErrors)

		response, err := d.LLM(ctx, prompt)
		if err != nil {
			return nil, fmt.Errorf("LLM call failed on attempt %d: %w", attempt+1, err)
		}

		yamlStr := extractYAMLBlock(response)
		if yamlStr == "" {
			lastErrors = []string{"No YAML code block found in response. Please wrap the flow in ```yaml ... ``` markers."}
			lastYAML = ""
			continue
		}

		if d.ValidateYAML != nil {
			validation := d.ValidateYAML(yamlStr, availableTools)
			if !validation.Valid {
				lastYAML = yamlStr
				lastErrors = validation.Errors
				continue
			}
		}

		// Semantic dry-run validation
		if d.DryRunYAML != nil {
			dryRun := d.DryRunYAML(yamlStr, toolSchemas)
			if !dryRun.Valid {
				lastYAML = yamlStr
				var dryRunErrors []string
				for _, e := range dryRun.Errors {
					dryRunErrors = append(dryRunErrors, "[dry-run] "+e)
				}
				for _, w := range dryRun.Warnings {
					dryRunErrors = append(dryRunErrors, "[dry-run warning] "+w)
				}
				lastErrors = dryRunErrors
				continue
			}
		}

		result := &DistillResult{YAML: yamlStr}
		d.extractMetadata(response, result)
		result.Explanation = extractExplanationBlock(response)

		return result, nil
	}

	if lastYAML != "" {
		result := &DistillResult{YAML: lastYAML}
		d.extractMetadata("", result)
		if result.Description == "" {
			result.Description = "Modified flow (may have validation warnings)"
		}
		return result, fmt.Errorf("flow modified but has validation errors after %d attempts: %s",
			maxRetries, strings.Join(lastErrors, "; "))
	}

	return nil, fmt.Errorf("failed to generate valid modified YAML after %d attempts", maxRetries)
}

// buildModificationPrompt constructs the prompt for modifying an existing flow.
func (d *FlowDistiller) buildModificationPrompt(req DistillModifyRequest, previousYAML string, validationErrors []string) string {
	var sb strings.Builder

	sb.WriteString("# Task\n\n")
	sb.WriteString("Modify this existing Astonish YAML flow based on the user's request.\n\n")

	// Include the flow schema
	if d.GetSchema != nil {
		sb.WriteString("# Astonish Flow Schema\n\n")
		sb.WriteString(d.GetSchema())
	}

	// Available tools
	toolInfos := d.buildToolInfoList()
	if len(toolInfos) > 0 {
		sb.WriteString("\n\n# Available Tools\n\nONLY use tools from this list:\n")
		for _, t := range toolInfos {
			if internalOnlyTools[t.Name] {
				continue
			}
			sb.WriteString(fmt.Sprintf("- %s: %s (source: %s)\n", t.Name, t.Description, t.Source))
		}
	}

	// Current YAML
	sb.WriteString("\n\n# Current Flow YAML\n\n```yaml\n")
	sb.WriteString(req.CurrentYAML)
	sb.WriteString("\n```\n\n")

	// Modification history for context
	if len(req.History) > 0 {
		sb.WriteString("# Previous Modification Requests\n\n")
		for i, h := range req.History {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, h))
		}
		sb.WriteString("\n")
	}

	// Last dry-run results for context (helps the LLM diagnose issues)
	if req.DryRunError != "" || req.DryRunOutput != "" {
		sb.WriteString("# Last Test Run Result\n\n")
		if req.DryRunError != "" {
			sb.WriteString("**Status: FAILED**\n")
			sb.WriteString(fmt.Sprintf("Error: %s\n\n", req.DryRunError))
		} else {
			sb.WriteString("**Status: SUCCESS**\n\n")
		}
		if req.DryRunOutput != "" {
			output := req.DryRunOutput
			if len(output) > 2000 {
				output = output[:2000] + "\n... (truncated)"
			}
			sb.WriteString("Output:\n```\n")
			sb.WriteString(output)
			sb.WriteString("\n```\n\n")
		}
		sb.WriteString("Use this information to understand what works and what fails in the current flow.\n\n")
	}

	// User's change request
	sb.WriteString("# User's Change Request\n\n")
	sb.WriteString(req.ChangeRequest)
	sb.WriteString("\n\n")

	// Instructions
	sb.WriteString("# Instructions\n\n")
	sb.WriteString("1. Modify the flow YAML based on the user's request\n")
	sb.WriteString("2. Preserve the existing flow structure unless the change requires restructuring\n")
	sb.WriteString("3. Only modify what the user requested — keep all other nodes and connections intact\n")
	sb.WriteString("4. Ensure all edges are valid (every from/to references an existing node or START/END)\n")
	sb.WriteString("5. Follow the same rules as flow creation: one input node per parameter, tools: true for tool-calling steps, etc.\n")
	sb.WriteString("6. NEVER include memory_save or other internal-only tools\n\n")

	// If retrying, include previous errors
	if previousYAML != "" && len(validationErrors) > 0 {
		sb.WriteString("# IMPORTANT: Previous Attempt Failed Validation\n\n")
		sb.WriteString("Your previous YAML had these errors:\n")
		for _, e := range validationErrors {
			sb.WriteString(fmt.Sprintf("- %s\n", e))
		}
		sb.WriteString("\nPrevious YAML:\n```yaml\n")
		sb.WriteString(previousYAML)
		sb.WriteString("\n```\n\n")
		sb.WriteString("Fix ALL the errors above and generate a corrected YAML flow.\n\n")
	}

	// Response format
	sb.WriteString("# Response Format\n\n")
	sb.WriteString("First, provide metadata on separate lines:\n")
	sb.WriteString("flow_name: suggested_filename_in_snake_case\n")
	sb.WriteString("description: one-line human description of what this flow does\n")
	sb.WriteString("tags: comma, separated, tags\n\n")
	sb.WriteString("Then provide the complete modified YAML flow in a ```yaml code block.\n\n")
	sb.WriteString("After the YAML block, provide a brief explanation in this exact format:\n\n")
	sb.WriteString("```explanation\n")
	sb.WriteString("## Summary\n")
	sb.WriteString("<one sentence describing what this flow does>\n\n")
	sb.WriteString("## Nodes\n")
	sb.WriteString("- **<node_name>** (<type>): <what it does>\n")
	sb.WriteString("- ...\n\n")
	sb.WriteString("## Input Parameters\n")
	sb.WriteString("- **<param_name>**: <description> (example: `<example_value>`)\n")
	sb.WriteString("- ...\n\n")
	sb.WriteString("## Changes Made\n")
	sb.WriteString("- <brief description of what was changed>\n")
	sb.WriteString("- ...\n\n")
	sb.WriteString("## Notes\n")
	sb.WriteString("<any important notes about the flow behavior, if applicable>\n")
	sb.WriteString("```\n")

	return sb.String()
}

// flattenTraces replaces delegate_tasks trace steps with the actual tool calls
// from child agent traces. This ensures distilled flows contain real tool calls
// (read_file, shell_command, etc.) rather than opaque delegate_tasks calls.
// The flattening is done in-place on the trace.
func flattenTraces(trace *ExecutionTrace) *ExecutionTrace {
	if trace == nil {
		return trace
	}

	trace.mu.Lock()
	defer trace.mu.Unlock()

	var flattened []TraceStep
	for _, step := range trace.Steps {
		if step.ToolName != "delegate_tasks" || len(step.SubAgentTraces) == 0 {
			flattened = append(flattened, step)
			continue
		}

		// Replace the delegate_tasks step with the children's actual tool calls
		for _, childTrace := range step.SubAgentTraces {
			if childTrace == nil {
				continue
			}
			childTrace.mu.Lock()
			for _, childStep := range childTrace.Steps {
				// Skip internal-only tools from children too
				if internalOnlyTools[childStep.ToolName] {
					continue
				}
				// Tag the step with the sub-agent name for context
				childStep.SubAgentName = step.SubAgentName
				flattened = append(flattened, childStep)
			}
			childTrace.mu.Unlock()
		}
	}

	trace.Steps = flattened
	return trace
}
