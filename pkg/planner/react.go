package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/execution"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

// ReActPlanner implements a manual ReAct (Reasoning + Acting) loop
// for models that do not support native tool calling.
// ApprovalCallback is called when a tool needs approval
type ApprovalCallback func(toolName string, args map[string]any) (bool, error)

type ReActPlanner struct {
	// BotDispatch is a trusted execution adapter, not an interactive approval.
	// Bot execution fails closed when it is absent.
	BotDispatch      func(tool.Context, tool.Tool, map[string]any) (map[string]any, error)
	LLM              model.LLM
	Tools            []tool.Tool
	ApprovalCallback ApprovalCallback
	State            session.State
	DebugMode        bool
}

// NewReActPlanner creates a new ReActPlanner.
func NewReActPlanner(llm model.LLM, tools []tool.Tool) *ReActPlanner {
	return &ReActPlanner{
		LLM:   llm,
		Tools: tools,
	}
}

func NewReActPlannerWithApproval(llm model.LLM, tools []tool.Tool, approvalCallback ApprovalCallback, state session.State, debugMode bool) *ReActPlanner {
	return &ReActPlanner{
		LLM:              llm,
		Tools:            tools,
		ApprovalCallback: approvalCallback,
		State:            state,
		DebugMode:        debugMode,
	}
}

// Run executes the ReAct loop to answer the input question.
// systemInstruction is optional additional context from the agent configuration.
func (p *ReActPlanner) Run(ctx context.Context, input string, systemInstruction string) (string, error) {
	// Check if we're resuming from a paused state
	var history string
	var startStep int

	if p.State != nil {
		if savedHistory, err := p.State.Get("_react_history"); err == nil {
			if h, ok := savedHistory.(string); ok {
				history = h
				if p.DebugMode {
					slog.Debug("resuming from saved state", "component", "react")
				}
			}
		}
		if savedStep, err := p.State.Get("_react_step"); err == nil {
			if s, ok := savedStep.(int); ok {
				startStep = s
			}
		}
	}

	// If not resuming, construct initial system prompt
	var currentSystemPrompt string
	if history == "" {
		// 1. Construct System Prompt (First Run)
		toolDescriptions := p.getToolDescriptions()
		toolNames := p.getToolNames()

		// Incorporate the agent's system instruction if provided
		var systemContext string
		if systemInstruction != "" {
			systemContext = fmt.Sprintf("%s\n\n", systemInstruction)
		}

		currentSystemPrompt = fmt.Sprintf(`%sAnswer the following questions as best you can. You have access to the following tools:

%s

IMPORTANT: You must ONLY use the tools listed above. Do not invent new tools or actions.

Use the following format STRICTLY for this FIRST RUN ONLY:

Question: the input question you must answer
Thought: Analyze the question and available tools. Do I have the answer, or do I need a tool? If I need a tool, which one is best, and what arguments does it need?
Action: the action to take, should be one of [%s]
Action Input: a JSON object with the tool parameters, e.g. {"owner": "user", "repo": "myrepo"}

IMPORTANT: For this FIRST RUN, DO NOT include Observation or Final Answer. The system will execute your chosen Action and provide the Observation in the next run.

Begin!

Question: %s
Thought:`, systemContext, toolDescriptions, toolNames, input)

		history = currentSystemPrompt
	}

	maxSteps := 10

	// Helper for float32 pointer
	temp := float32(0.0)

	for i := startStep; i < maxSteps; i++ {
		// If we are past the first step (and not resuming from a state where we already switched),
		// switch to the full system prompt.
		// We do this at the start of i=1 (after first tool execution).
		if i == 1 && startStep == 0 {
			// Construct Full System Prompt
			toolDescriptions := p.getToolDescriptions()
			toolNames := p.getToolNames()

			var systemContext string
			if systemInstruction != "" {
				systemContext = fmt.Sprintf("%s\n\n", systemInstruction)
			}

			fullSystemPrompt := fmt.Sprintf(`%sAnswer the following questions as best you can. You have access to the following tools:

%s

IMPORTANT: You must ONLY use the tools listed above. Do not invent new tools or actions.

Use the following format:

Question: the input question you must answer
Thought: you should always think about what to do
Action: the action to take, should be one of [%s]
Action Input: a JSON object with the tool parameters, e.g. {"owner": "user", "repo": "myrepo"}
Observation: the result of the action
... (this Thought/Action/Action Input/Observation cycle can repeat N times)

When you have enough information:
Thought: I now know the final answer
Final Answer: the final answer to the original input question

Begin!

Question: %s
Thought:`, systemContext, toolDescriptions, toolNames, input)

			// Replace the first run prompt with the full prompt in history
			// We assume history starts with currentSystemPrompt
			if strings.HasPrefix(history, currentSystemPrompt) {
				history = strings.Replace(history, currentSystemPrompt, fullSystemPrompt, 1)
				if p.DebugMode {
					slog.Debug("switched to full system prompt", "component", "react")
				}
			}
		}
		var action, actionInput string
		var skipLLM bool

		// CHECK: Do we have a pending action from a resume?
		if p.State != nil {
			if pendingAction, _ := p.State.Get("_react_pending_action"); pendingAction != nil {
				if act, ok := pendingAction.(string); ok {
					action = act
					if pendingInput, _ := p.State.Get("_react_pending_input"); pendingInput != nil {
						if inp, ok := pendingInput.(string); ok {
							actionInput = inp
						}
					}

					// Clear the pending state so we don't loop forever
					if err := p.State.Set("_react_pending_action", nil); err != nil {
						slog.Warn("failed to clear pending action", "component", "react", "error", err)
					}
					if err := p.State.Set("_react_pending_input", nil); err != nil {
						slog.Warn("failed to clear pending input", "component", "react", "error", err)
					}

					skipLLM = true
					if p.DebugMode {
						slog.Debug("resuming execution of pending tool", "component", "react", "tool", action)
					}
				}
			}
		}

		if !skipLLM {
			// 2. Call LLM
			req := &model.LLMRequest{
				Contents: []*genai.Content{
					{
						Role: "user",
						Parts: []*genai.Part{
							{Text: history},
						},
					},
				},
				Config: &genai.GenerateContentConfig{
					Temperature:   &temp,                    // Deterministic for planning
					StopSequences: []string{"Observation:"}, // Stop at observation to let us execute tool
				},
			}

			var responseText string
			// We use non-streaming for simplicity in the loop, or we could stream and buffer.
			// Let's consume the stream to get the full text.
			for resp, err := range p.LLM.GenerateContent(ctx, req, false) {
				if err != nil {
					return "", fmt.Errorf("LLM generation failed: %w", err)
				}
				if resp.Content != nil {
					for _, part := range resp.Content.Parts {
						responseText += part.Text
					}
				}
			}

			// CRITICAL: Truncate everything after "STOP HERE" to prevent the model from hallucinating
			// the observation and final answer. The model should ONLY generate up to the Action Input,
			// then we execute the tool and provide the real observation.
			if stopIdx := strings.Index(responseText, "STOP HERE"); stopIdx != -1 {
				// Keep everything up to and including "STOP HERE"
				responseText = responseText[:stopIdx+9] // 9 = len("STOP HERE")
				if p.DebugMode {
					slog.Debug("truncated output after STOP HERE", "component", "react")
				}
			}

			// Sanitize response: Remove repeated "Question:" if present
			// The model sometimes repeats the question at the start of its response.
			// We want to keep the history clean.
			if strings.HasPrefix(strings.TrimSpace(responseText), "Question:") {
				// Find the first "Thought:" or "Action:"
				thoughtIdx := strings.Index(responseText, "Thought:")
				if thoughtIdx != -1 {
					responseText = responseText[thoughtIdx:]
				} else {
					actionIdx := strings.Index(responseText, "Action:")
					if actionIdx != -1 {
						responseText = responseText[actionIdx:]
					}
				}
			}

			if p.DebugMode {
				slog.Debug("llm output", "component", "react", "step", i+1, "output", responseText)
			}

			// Update history with the LLM's response
			history += responseText

			// 3. Check for Final Answer
			if strings.Contains(responseText, "Final Answer:") {
				// Extract final answer
				parts := strings.Split(responseText, "Final Answer:")
				if len(parts) >= 2 {
					answer := strings.TrimSpace(parts[1])
					// Clear saved state
					if p.State != nil {
						if err := p.State.Set("_react_history", nil); err != nil {
							slog.Warn("failed to clear react history", "component", "react", "error", err)
						}
						if err := p.State.Set("_react_step", nil); err != nil {
							slog.Warn("failed to clear react step", "component", "react", "error", err)
						}
					}
					return answer, nil
				}
			}

			// Parse Action
			// Allow hyphens, dots, or other safe chars in tool names (non-whitespace)
			cleanedResponse := removeThinkTags(responseText)
			actionRegex := regexp.MustCompile(`Action:\s*([^\s]+)`)
			actionMatch := actionRegex.FindStringSubmatch(cleanedResponse)
			if len(actionMatch) < 2 {
				// Heuristic: If it wrote "Action Input" but missed "Action", or if the text is very long, it failed.
				if strings.Contains(cleanedResponse, "Action Input:") {
					history += "\n\nObservation: Error: Invalid Format. You provided an Input but no Action. Please use the 'Action: <ToolName>' format.\n\nThought: "
					continue
				}
				// No action found - might be just thinking, continue
				// The responseText is already added to history, so just continue.
				continue
			}
			action = strings.Trim(actionMatch[1], "`\"'")

			// Parse Action Input - it might be multiline or contain code blocks
			// Look for "Action Input:" and capture everything until "STOP HERE", "Observation:", or end
			actionInputRegex := regexp.MustCompile(`(?s)Action Input:\s*(.*?)(?:\n\nSTOP HERE|\n\nObservation:|$)`)
			inputMatch := actionInputRegex.FindStringSubmatch(cleanedResponse)
			if len(inputMatch) >= 2 {
				actionInput = strings.TrimSpace(inputMatch[1])

				// Strip markdown code blocks if present
				// Handle ```python\ncode\n``` or ```\ncode\n```
				if strings.HasPrefix(actionInput, "```") {
					// Find the first newline after ```
					lines := strings.Split(actionInput, "\n")
					if len(lines) > 1 {
						// Skip first line (```python or ```)
						codeLines := lines[1:]
						// Remove last line if it's ```
						if len(codeLines) > 0 && strings.TrimSpace(codeLines[len(codeLines)-1]) == "```" {
							codeLines = codeLines[:len(codeLines)-1]
						}
						actionInput = strings.Join(codeLines, "\n")
					}
				}
				actionInput = strings.TrimSpace(actionInput)
			}
		} // End of !skipLLM block

		// Execute tool
		if p.DebugMode {
			slog.Debug("executing tool", "component", "react", "tool", action, "input", actionInput)
		}
		observation, err := p.executeTool(ctx, action, actionInput)
		if err != nil {
			// Check if this is an approval required error
			if err.Error() == "tool approval required" {
				// Save current state before pausing
				if p.State != nil {
					if err := p.State.Set("_react_history", history); err != nil {
						slog.Warn("failed to save react history", "component", "react", "error", err)
					}
					if err := p.State.Set("_react_step", i); err != nil {
						slog.Warn("failed to save react step", "component", "react", "error", err)
					}

					// NEW: Save the pending action details
					if err := p.State.Set("_react_pending_action", action); err != nil {
						slog.Warn("failed to save pending action", "component", "react", "error", err)
					}
					if err := p.State.Set("_react_pending_input", actionInput); err != nil {
						slog.Warn("failed to save pending input", "component", "react", "error", err)
					}

					if p.DebugMode {
						slog.Debug("saving state", "component", "react", "step", i)
					}
				}
				// Return a special result indicating approval is needed
				// The caller will handle pausing and requesting approval
				return "", fmt.Errorf("APPROVAL_REQUIRED:%s:%s", action, actionInput)
			}
			// Other errors - treat as observation
			observation = fmt.Sprintf("Error: %v", err)
		}

		if p.DebugMode {
			slog.Debug("observation", "component", "react", "result", observation)
		}

		// Append observation to history
		history += fmt.Sprintf("\n\nObservation: %s\n\nThought: ", observation)
	}

	return "", fmt.Errorf("max ReAct steps (%d) reached without final answer", maxSteps)
}

// FormatOutput takes the ReAct result and formats it according to the output schema.
// This is called after the ReAct loop completes to ensure the output matches the expected structure.
func (p *ReActPlanner) FormatOutput(ctx context.Context, reactResult string, outputSchema map[string]string, systemInstruction string) (string, error) {
	if len(outputSchema) == 0 {
		// No schema, return as-is
		return reactResult, nil
	}

	// Build schema description
	var schemaDesc strings.Builder
	schemaDesc.WriteString("{\n")
	for key, typeName := range outputSchema {
		schemaDesc.WriteString(fmt.Sprintf("  \"%s\": <%s>,\n", key, typeName))
	}
	schemaDesc.WriteString("}")

	// Create formatting prompt
	var systemContext string
	if systemInstruction != "" {
		systemContext = fmt.Sprintf("Context: %s\n\n", systemInstruction)
	}

	formatPrompt := fmt.Sprintf(`%sYou are a data formatter. Take the following result and format it as a JSON object matching this schema:

%s

Result to format:
%s

Return ONLY the JSON object, no other text or markdown.`, systemContext, schemaDesc.String(), reactResult)

	// Call LLM to format
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role: "user",
				Parts: []*genai.Part{
					{Text: formatPrompt},
				},
			},
		},
		Config: &genai.GenerateContentConfig{
			Temperature:      func() *float32 { t := float32(0.0); return &t }(),
			ResponseMIMEType: "application/json",
		},
	}

	var responseText string
	for resp, err := range p.LLM.GenerateContent(ctx, req, false) {
		if err != nil {
			return "", fmt.Errorf("formatting LLM call failed: %w", err)
		}
		if resp.Content != nil {
			for _, part := range resp.Content.Parts {
				responseText += part.Text
			}
		}
	}

	responseText = removeThinkTags(responseText)

	// Clean up markdown if present
	responseText = strings.TrimSpace(responseText)
	responseText = strings.TrimPrefix(responseText, "```json")
	responseText = strings.TrimPrefix(responseText, "```")
	responseText = strings.TrimSuffix(responseText, "```")
	responseText = strings.TrimSpace(responseText)

	return responseText, nil
}

func (p *ReActPlanner) getToolDescriptions() string {
	var sb strings.Builder
	for _, t := range p.Tools {
		sb.WriteString(fmt.Sprintf("%s: %s", t.Name(), t.Description()))

		// Try to get parameter information from the tool's declaration
		if declTool, ok := t.(common.ToolWithDeclaration); ok {
			decl := declTool.Declaration()
			if decl != nil && decl.ParametersJsonSchema != nil {
				// Try to extract parameter details
				if schema, ok := decl.ParametersJsonSchema.(*genai.Schema); ok {
					if schema.Type == genai.TypeObject && len(schema.Properties) > 0 {
						sb.WriteString("\n  Parameters:")
						for propName, propSchema := range schema.Properties {
							required := ""
							for _, req := range schema.Required {
								if req == propName {
									required = " (required)"
									break
								}
							}
							propType := string(propSchema.Type)
							desc := propSchema.Description
							if desc != "" {
								sb.WriteString(fmt.Sprintf("\n    - %s: %s - %s%s", propName, propType, desc, required))
							} else {
								sb.WriteString(fmt.Sprintf("\n    - %s: %s%s", propName, propType, required))
							}
						}
					}
				} else if schemaMap, ok := decl.ParametersJsonSchema.(map[string]interface{}); ok {
					// Handle map-based schema
					if props, ok := schemaMap["properties"].(map[string]interface{}); ok && len(props) > 0 {
						var required []string
						if reqList, ok := schemaMap["required"].([]interface{}); ok {
							for _, r := range reqList {
								if rs, ok := r.(string); ok {
									required = append(required, rs)
								}
							}
						}
						sb.WriteString("\n  Parameters:")
						for propName, propVal := range props {
							isReq := ""
							for _, r := range required {
								if r == propName {
									isReq = " (required)"
									break
								}
							}
							if propMap, ok := propVal.(map[string]interface{}); ok {
								propType, _ := propMap["type"].(string)
								desc, _ := propMap["description"].(string)
								if desc != "" {
									sb.WriteString(fmt.Sprintf("\n    - %s: %s - %s%s", propName, propType, desc, isReq))
								} else {
									sb.WriteString(fmt.Sprintf("\n    - %s: %s%s", propName, propType, isReq))
								}
							}
						}
					}
				}
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func (p *ReActPlanner) getToolNames() string {
	var names []string
	for _, t := range p.Tools {
		names = append(names, t.Name())
	}
	return strings.Join(names, ", ")
}

func (p *ReActPlanner) executeTool(ctx context.Context, name string, inputJSON string) (string, error) {
	// Find the tool
	var selectedTool tool.Tool
	for _, t := range p.Tools {
		if t.Name() == name {
			selectedTool = t
			break
		}
	}

	if selectedTool == nil {
		return fmt.Sprintf("Error: Tool '%s' not found. Available tools: %s", name, p.getToolNames()), nil
	}

	// Parse input
	// Parse input
	var args map[string]any
	// Try parsing as JSON first
	if err := json.Unmarshal([]byte(inputJSON), &args); err != nil {
		// If not JSON, try to determine the single argument name from the schema
		argName := "input" // Default fallback

		if declTool, ok := selectedTool.(common.ToolWithDeclaration); ok {
			if p.DebugMode {
				slog.Debug("tool implements ToolWithDeclaration", "component", "react", "tool", name)
			}
			decl := declTool.Declaration()
			if decl != nil && decl.ParametersJsonSchema != nil {
				if schema, ok := decl.ParametersJsonSchema.(*genai.Schema); ok {
					// Check if there is exactly one required property
					if schema.Type == genai.TypeObject {
						if len(schema.Required) == 1 {
							argName = schema.Required[0]
						} else if len(schema.Properties) == 1 {
							for k := range schema.Properties {
								argName = k
								break
							}
						}
					}
				} else if schemaMap, ok := decl.ParametersJsonSchema.(map[string]interface{}); ok {
					// Check required list in map
					if required, ok := schemaMap["required"].([]interface{}); ok && len(required) == 1 {
						if reqStr, ok := required[0].(string); ok {
							argName = reqStr
						}
					} else if props, ok := schemaMap["properties"].(map[string]interface{}); ok && len(props) == 1 {
						for k := range props {
							argName = k
							break
						}
					}
				}
			}
		} else {
			if p.DebugMode {
				slog.Debug("tool does not implement ToolWithDeclaration", "component", "react", "tool", name, "type", fmt.Sprintf("%T", selectedTool))
			}
		}
		args = map[string]any{argName: inputJSON}
		if p.DebugMode {
			slog.Debug("wrapped input for tool", "component", "react", "tool", name, "args", args)
		}
	}

	// WORKAROUND: Sanitize arguments for common model hallucinations
	// Some models (e.g. phi-4) output "map[]" string for empty object fields
	if val, ok := args["global_variables"]; ok {
		if strVal, ok := val.(string); ok && (strVal == "map[]" || strVal == "") {
			args["global_variables"] = map[string]any{}
		}
	}

	if _, bot := execution.BotRunIdentityFromContext(ctx); bot || execution.RunPolicyFromContext(ctx) != nil {
		if p.BotDispatch == nil {
			return "", fmt.Errorf("bot policy: missing ReAct execution adapter")
		}
		result, err := p.BotDispatch(&minimalToolContext{Context: ctx, state: p.State}, selectedTool, args)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(result)
		return string(raw), err
	}

	// Check for approval if callback is set
	if p.ApprovalCallback != nil {
		approved, err := p.ApprovalCallback(name, args)
		if err != nil {
			return fmt.Sprintf("Error checking approval: %v", err), nil
		}
		if !approved {
			// Tool execution was paused for approval
			// Return a special message that will be handled by the caller
			return "APPROVAL_REQUIRED", fmt.Errorf("tool approval required")
		}
	}

	// Execute the tool using ADK's Run method
	// tool.Tool is an interface, but concrete implementations have a Run method
	// We need to use type assertion or reflection to call it
	// Create a minimal tool context
	toolCtx := &minimalToolContext{
		Context: ctx,
		state:   p.State,
	}

	// Try to call Run using reflection or type assertion
	// Most ADK tools implement a Run(tool.Context, any) (map[string]any, error) method
	type runnableTool interface {
		Run(tool.Context, any) (map[string]any, error)
	}

	runnable, ok := selectedTool.(runnableTool)
	if !ok {
		return fmt.Sprintf("Error: Tool '%s' does not implement Run method", name), nil
	}

	result, err := runnable.Run(toolCtx, args)
	if err != nil {
		return fmt.Sprintf("Error executing tool: %v", err), nil
	}

	// Marshal result to string
	resultBytes, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf("%v", result), nil
	}
	return string(resultBytes), nil
}

// minimalToolContext implements tool.Context for ReAct tool execution
type minimalToolContext struct {
	context.Context // Embed to get Deadline, Done, Err, Value methods
	state           session.State
}

func (m *minimalToolContext) Actions() *session.EventActions {
	return &session.EventActions{}
}

func (m *minimalToolContext) Branch() string {
	return ""
}

func (m *minimalToolContext) AgentName() string {
	return "ReActPlanner"
}

func (m *minimalToolContext) AppName() string {
	return "astonish"
}

func (m *minimalToolContext) Artifacts() agent.Artifacts {
	return nil
}

func (m *minimalToolContext) FunctionCallID() string {
	return ""
}

func (m *minimalToolContext) InvocationID() string {
	return ""
}

func (m *minimalToolContext) SessionID() string {
	return ""
}

func (m *minimalToolContext) UserID() string {
	return ""
}

func (m *minimalToolContext) UserContent() *genai.Content {
	return nil
}

func (m *minimalToolContext) ReadonlyState() session.ReadonlyState {
	return nil
}

func (m *minimalToolContext) SearchMemory(ctx context.Context, query string) (*memory.SearchResponse, error) {
	return nil, nil
}

func (m *minimalToolContext) State() session.State {
	return m.state
}

func (m *minimalToolContext) RequestConfirmation(hint string, payload any) error {
	return nil // No HITL during ReAct planning
}

func (m *minimalToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation {
	return nil // No HITL during ReAct planning
}

func removeThinkTags(input string) string {
	re := regexp.MustCompile(`(?s)<think>.*?</think>`)
	return re.ReplaceAllString(input, "")
}
