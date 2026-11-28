package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/planner"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// executeReActFallback runs the ReAct planner as a fallback when native tool calling is not supported
func (a *AstonishAgent) executeReActFallback(ctx context.Context, node *config.Node, nodeName string, state session.State, yield func(*session.Event, error) bool, internalTools []tool.Tool, instruction string, userPrompt string) (bool, error) {
	// Emit a message indicating we're using the fallback via spinner update
	yield(&session.Event{
		Actions: session.EventActions{
			StateDelta: map[string]any{
				"_spinner_text": fmt.Sprintf("Processing %s (Fallback: internal tool reasoning)...", node.Name),
			},
		},
	}, nil)

	// Collect all tools (internal + MCP) for ReAct planner
	allTools := make([]tool.Tool, 0, len(internalTools))
	allTools = append(allTools, internalTools...)

	// Add MCP tools
	if len(a.Toolsets) > 0 {
		minimalCtx := &minimalReadonlyContext{Context: ctx}
		for _, ts := range a.Toolsets {
			if err := botSafeToolset(ctx, ts); err != nil {
				return false, err
			}
			tsTools, err := ts.Tools(minimalCtx)
			if err != nil {
				continue
			}
			// Filter by tools_selection if specified
			if len(node.ToolsSelection) > 0 {
				for _, t := range tsTools {
					for _, selected := range node.ToolsSelection {
						if t.Name() == selected {
							allTools = append(allTools, t)
							break
						}
					}
				}
			} else {
				allTools = append(allTools, tsTools...)
			}
		}
	}

	// Create approval callback if tools_auto_approval is false
	var approvalCallback planner.ApprovalCallback
	if !node.ToolsAutoApproval {
		approvalCallback = func(toolName string, args map[string]any) (bool, error) {
			// Node-scoped approval key
			approvalKey := fmt.Sprintf("approval:%s:%s", node.Name, toolName)

			// Check if we already have approval for this tool
			approvedVal, _ := state.Get(approvalKey)
			approved := false
			if b, ok := approvedVal.(bool); ok && b {
				approved = true
			}

			if approved {
				// Consume approval - each execution requires new approval
				state.Set(approvalKey, false)
				if a.DebugMode {
					slog.Debug("tool approved, allowing execution", "component", "react", "tool", toolName)
				}
				return true, nil
			}

			if a.DebugMode {
				slog.Debug("tool not approved, requesting approval", "component", "react", "tool", toolName)
			}

			// No approval - request it
			state.Set("force_pause", true)
			state.Set("awaiting_approval", true)
			state.Set("approval_tool", toolName)
			state.Set("approval_args", args)

			// Emit approval request event
			prompt := a.formatToolApprovalRequest(toolName, args)
			yield(&session.Event{
				LLMResponse: model.LLMResponse{
					Content: &genai.Content{
						Parts: []*genai.Part{{Text: prompt}},
						Role:  "model",
					},
				},
				Actions: session.EventActions{
					StateDelta: map[string]any{
						"awaiting_approval": true,
						"approval_tool":     toolName,
						"approval_options":  []string{"Yes", "No"},
					},
				},
			}, nil)

			return false, nil
		}
	}

	// Use manual ReAct planner with all tools and approval callback
	var reactPlanner *planner.ReActPlanner
	if approvalCallback != nil {
		reactPlanner = planner.NewReActPlannerWithApproval(a.LLM, allTools, approvalCallback, state, a.DebugMode)
	} else {
		reactPlanner = planner.NewReActPlanner(a.LLM, allTools)
	}

	reactPlanner.BotDispatch = func(ctx tool.Context, target tool.Tool, args map[string]any) (map[string]any, error) {
		identity := botExecutableIdentity(target)
		var authorizedAction execution.ActionDescriptor
		if err := authorizeBotTool(ctx, identity, args, &authorizedAction); err != nil {
			return nil, err
		}
		resolver := a.CredentialStore
		if cs := store.CredentialStoreFromContext(ctx); cs != nil {
			resolver = credentials.NewStoreAdapter(cs)
		}
		if resolver != nil {
			credentials.RegisterResolvedWithRedactor(args, resolver, a.Redactor)
			var shellFields []string
			if target.Name() == "shell_command" || target.Name() == "process_write" {
				shellFields = []string{"command"}
			}
			defer credentials.SubstituteAndRestore(args, resolver, shellFields...)()
		}
		if a.PendingSecrets != nil {
			defer a.PendingSecrets.SubstituteAndRestore(args)()
		}
		dispatchCtx, err := botAuthorizedInvocationContext(ctx, identity, args, authorizedAction)
		if err != nil {
			return nil, err
		}
		runnable, ok := target.(RunnableTool)
		if !ok {
			return nil, fmt.Errorf("tool is not runnable")
		}
		out, err := runnable.Run(dispatchCtx, args)
		if a.Redactor != nil && out != nil {
			out = a.Redactor.RedactMap(out)
		}
		return out, err
	}

	// Strip output_model instructions from system instruction for ReAct
	// The ReAct loop should focus on tool usage, not output formatting
	cleanInstruction := instruction
	if len(node.OutputModel) > 0 {
		// Remove lines mentioning output_model fields
		lines := strings.Split(instruction, "\n")
		var cleanLines []string
		for _, line := range lines {
			// Skip lines that mention the output model fields or JSON formatting
			skipLine := false
			for key := range node.OutputModel {
				if strings.Contains(line, key) || strings.Contains(line, "JSON") || strings.Contains(line, "json") {
					skipLine = true
					break
				}
			}
			if !skipLine {
				cleanLines = append(cleanLines, line)
			}
		}
		cleanInstruction = strings.Join(cleanLines, "\n")
	}

	result, err := reactPlanner.Run(ctx, userPrompt, cleanInstruction) // Pass cleaned instruction
	if err != nil {
		// Check if this is an approval required error
		if strings.HasPrefix(err.Error(), "APPROVAL_REQUIRED:") {
			// Approval is needed - the callback has already emitted the approval request
			// Just return false to pause execution
			if a.DebugMode {
				slog.Debug("pausing for tool approval", "component", "react")
			}
			return false, nil
		}
		return false, fmt.Errorf("ReAct planner failed: %w", err)
	}

	// Format output according to output_model if specified
	if len(node.OutputModel) > 0 {
		formattedResult, formatErr := reactPlanner.FormatOutput(ctx, result, node.OutputModel, instruction)
		if formatErr != nil {
			return false, fmt.Errorf("failed to format ReAct output: %w", formatErr)
		}
		result = formattedResult

		// Parse the formatted result and store in state
		var resultMap map[string]any
		if err := json.Unmarshal([]byte(result), &resultMap); err == nil {
			for key, value := range resultMap {
				state.Set(key, value)
			}
		}
	}

	// Handle user_message if defined
	if len(node.UserMessage) > 0 {
		var textParts []string
		for _, msgPart := range node.UserMessage {
			if val, err := state.Get(msgPart); err == nil {
				textParts = append(textParts, fmt.Sprintf("%v", val))
				if a.DebugMode {
					slog.Debug("resolved user message part", "component", "react", "part", msgPart, "value", val)
				}
			}
		}

		if len(textParts) > 0 {
			// Emit user_message event
			userMessageEvent := &session.Event{
				LLMResponse: model.LLMResponse{
					Content: &genai.Content{
						Parts: []*genai.Part{{Text: strings.Join(textParts, " ")}},
						Role:  "model",
					},
				},
				Actions: session.EventActions{
					StateDelta: map[string]any{
						"_user_message_display": true,
					},
				},
			}
			yield(userMessageEvent, nil)
		}
	} else {
		// No user_message - yield the full result
		yield(&session.Event{
			LLMResponse: model.LLMResponse{
				Content: &genai.Content{
					Parts: []*genai.Part{{Text: result}},
					Role:  "model",
				},
			},
		}, nil)
	}

	return true, nil
}
