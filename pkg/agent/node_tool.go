package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

func (a *AstonishAgent) handleToolNode(ctx context.Context, node *config.Node, state session.State, yield func(*session.Event, error) bool) bool {
	// 1. Resolve arguments
	resolvedArgs := make(map[string]interface{})
	for key, val := range node.Args {
		if strVal, ok := val.(string); ok {
			resolvedArgs[key] = a.renderString(strVal, state)
		} else if mapVal, ok := val.(map[string]interface{}); ok && len(mapVal) == 1 {
			// Handle map arguments (e.g. owner: {owner: str}) -> resolve from state
			var stateKey string
			for k := range mapVal {
				stateKey = k
				break
			}

			if stateVal, err := state.Get(stateKey); err == nil {
				resolvedArgs[key] = stateVal
			} else {
				if a.DebugMode {
					slog.Warn("state key not found", "stateKey", stateKey, "arg", key)
				}
				resolvedArgs[key] = nil
			}
		} else {
			resolvedArgs[key] = val
		}
	}

	// 2. Identify Tool
	if len(node.ToolsSelection) == 0 {
		yield(nil, fmt.Errorf("tool node '%s' missing tools_selection", node.Name))
		return false
	}
	toolName := node.ToolsSelection[0]

	// 3. Approval Workflow — match llm-node semantics: per-node
	// tools_auto_approval OR global AutoApprove (headless / run_flow).
	approved := false
	if node.ToolsAutoApproval || a.AutoApprove {
		approved = true
	} else {
		// Check if we already have approval for this specific tool execution
		// Node-scoped approval key
		approvalKey := fmt.Sprintf("approval:%s:%s", node.Name, toolName)
		val, _ := state.Get(approvalKey)
		if isApproved, ok := val.(bool); ok && isApproved {
			approved = true
			// Clear approval so we don't loop forever if we come back here?
			// Actually, for a linear flow, it's fine. For a loop, we might want to clear it.
			// But clearing it might break if we crash and resume?
			// Let's clear it after execution.
		}
	}

	if !approved {
		// Set state for approval
		state.Set("awaiting_approval", true)
		state.Set("approval_tool", toolName)
		state.Set("approval_args", resolvedArgs)

		// Emit approval request
		approvalText := a.formatToolApprovalRequest(toolName, resolvedArgs)
		approvalEvent := &session.Event{
			LLMResponse: model.LLMResponse{
				Content: &genai.Content{
					Parts: []*genai.Part{{Text: approvalText}},
					Role:  "model",
				},
			},
			Actions: session.EventActions{
				StateDelta: map[string]any{
					"awaiting_approval": true,
					"current_node":      node.Name,
					"approval_tool":     toolName,
					"approval_args":     resolvedArgs,
					"approval_options":  []string{"Yes", "No"}, // Trigger interactive selection
				},
			},
		}
		// Yield and return false to pause execution
		yield(approvalEvent, nil)
		return false
	}

	// 4. Execute Tool
	// Find the tool in a.Tools
	var selectedTool tool.Tool
	for _, t := range a.Tools {
		if t.Name() == toolName {
			selectedTool = t
			break
		}
	}

	// If not found in internal tools, check Toolsets (MCP)
	if selectedTool == nil && a.Toolsets != nil {
		roCtx := &minimalReadonlyContext{Context: ctx}
		for _, ts := range a.Toolsets {
			if err := botSafeToolset(ctx, ts); err != nil {
				yield(nil, err)
				return false
			}
			tools, err := ts.Tools(roCtx)
			if err == nil {
				for _, t := range tools {
					if t.Name() == toolName {
						selectedTool = t
						break
					}
				}
			}
			if selectedTool != nil {
				break
			}
		}
	}
	if selectedTool == nil {
		yield(nil, fmt.Errorf("tool '%s' not found", toolName))
		return false
	}

	// 5. Type Conversion based on Schema
	if declTool, ok := selectedTool.(ToolWithDeclaration); ok {
		if a.DebugMode {
			slog.Debug("tool implements ToolWithDeclaration", "tool", toolName)
		}
		decl := declTool.Declaration()
		if decl != nil && decl.ParametersJsonSchema != nil {
			if a.DebugMode {
				slog.Debug("parameters json schema", "type", fmt.Sprintf("%T", decl.ParametersJsonSchema))
			}
			if schema, ok := decl.ParametersJsonSchema.(*genai.Schema); ok {
				if a.DebugMode {
					slog.Debug("schema type check", "schemaType", schema.Type, "expected", genai.TypeObject)
				}
				if schema.Type == genai.TypeObject {
					for key, val := range resolvedArgs {
						if strVal, ok := val.(string); ok {
							if prop, ok := schema.Properties[key]; ok {
								if prop.Type == genai.TypeNumber || prop.Type == genai.TypeInteger {
									if num, err := strconv.ParseFloat(strVal, 64); err == nil {
										resolvedArgs[key] = num
										if a.DebugMode {
											slog.Debug("converted arg to number", "arg", key, "value", num)
										}
									} else {
										// Fallback: Try to extract leading number (e.g. "709: Title" -> 709)
										// This handles cases where the selection includes the title
										re := regexp.MustCompile(`^(\d+)`)
										if match := re.FindStringSubmatch(strVal); len(match) > 1 {
											if num, err := strconv.ParseFloat(match[1], 64); err == nil {
												resolvedArgs[key] = num
												if a.DebugMode {
													slog.Debug("extracted number from arg", "arg", key, "value", num, "original", strVal)
												}
											}
										}
									}
								} else if prop.Type == genai.TypeBoolean {
									// Try to convert to boolean
									if b, err := strconv.ParseBool(strVal); err == nil {
										resolvedArgs[key] = b
										if a.DebugMode {
											slog.Debug("converted arg to boolean", "arg", key, "value", b)
										}
									}
								}
							}
						}
					}
				}
			} else if schemaMap, ok := decl.ParametersJsonSchema.(map[string]interface{}); ok {
				// Handle map[string]interface{} schema (common in MCP or other providers)

				if typeVal, ok := schemaMap["type"].(string); ok && typeVal == "object" {
					if props, ok := schemaMap["properties"].(map[string]interface{}); ok {
						for key, val := range resolvedArgs {
							if strVal, ok := val.(string); ok {
								if prop, ok := props[key].(map[string]interface{}); ok {
									propType, _ := prop["type"].(string)

									if propType == "number" || propType == "integer" {
										// Try to convert string to float64
										if num, err := strconv.ParseFloat(strVal, 64); err == nil {
											resolvedArgs[key] = num
										} else {
											// Fallback: Try to extract leading number (e.g. "709: Title" -> 709)
											re := regexp.MustCompile(`^(\d+)`)
											if match := re.FindStringSubmatch(strVal); len(match) > 1 {
												if num, err := strconv.ParseFloat(match[1], 64); err == nil {
													resolvedArgs[key] = num
												}
											}
										}
									} else if propType == "boolean" {
										// Try to convert string to boolean
										if b, err := strconv.ParseBool(strVal); err == nil {
											resolvedArgs[key] = b
										}
									}
								}
							}
							// Also handle int to float64 if needed
							if intVal, ok := val.(int); ok {
								// Check if schema expects number/integer
								if prop, ok := props[key].(map[string]interface{}); ok {
									propType, _ := prop["type"].(string)
									if propType == "number" {
										resolvedArgs[key] = float64(intVal)
									}
								}
							}
						}
					}
				}
			} else {
				if a.DebugMode {
					slog.Debug("parameters json schema is not *genai.Schema or map[string]interface{}")
				}
			}
		} else {
			if a.DebugMode {
				slog.Debug("declaration or parameters json schema is nil")
			}
		}
	} else {
		if a.DebugMode {
			slog.Debug("tool does not implement ToolWithDeclaration", "tool", toolName)
		}
	}

	// Execute using RunnableTool interface
	// Create tool context with session ID for sandbox routing
	stateDelta := make(map[string]any)
	var sessID string
	if sc, ok := ctx.(interface{ SessionID() string }); ok {
		sessID = sc.SessionID()
	} else if ic, ok := ctx.(agent.InvocationContext); ok && ic.Session() != nil {
		sessID = ic.Session().ID()
	}
	toolCtx := &minimalReadonlyContext{
		Context:   ctx,
		actions:   &session.EventActions{StateDelta: stateDelta},
		state:     state,
		sessionID: sessID,
	}

	runnable, ok := selectedTool.(RunnableTool)
	if !ok {
		yield(nil, fmt.Errorf("tool '%s' does not implement Run method", toolName))
		return false
	}

	var authorizedAction execution.ActionDescriptor
	if err := authorizeBotTool(ctx, botExecutableIdentity(selectedTool), resolvedArgs, &authorizedAction); err != nil {
		yield(nil, err)
		return false
	}

	// Resolve {{CREDENTIAL:name:field}} placeholders in tool args.
	// Tool-type nodes bypass ADK's tool lifecycle (no BeforeToolCallback),
	// so we must substitute credentials here before execution.
	var resolver credentials.CredentialResolver
	if cs := store.CredentialStoreFromContext(ctx); cs != nil {
		resolver = credentials.NewStoreAdapter(cs)
	} else if a.CredentialStore != nil {
		resolver = a.CredentialStore
	}
	if resolver != nil {
		var shellFields []string
		if toolName == "shell_command" || toolName == "process_write" {
			shellFields = []string{"command"}
		}
		credentials.SubstituteAndRestore(resolvedArgs, resolver, shellFields...)
		// Note: we don't restore — tool nodes don't need to redact args
		// back into session history (they emit structured results, not raw args).
	}

	// Unresolved credential placeholders are left as literal text — this
	// handles documentation/code that describes the placeholder format.
	if unresolved := credentials.UnresolvedCredentialNames(resolvedArgs); len(unresolved) > 0 {
		slog.Debug("credential placeholders remain unresolved in flow tool (treating as literal text)",
			"component", "credentials", "tool", toolName, "unresolved", unresolved)
	}

	// Resolve <<<SECRET_N>>> tokens (pending secrets from interactive capture).
	if a.PendingSecrets != nil {
		a.PendingSecrets.SubstituteAndRestore(resolvedArgs)
	}

	dispatchCtx, err := botAuthorizedInvocationContext(toolCtx, botExecutableIdentity(selectedTool), resolvedArgs, authorizedAction)
	if err != nil {
		yield(nil, err)
		return false
	}
	toolResult, err := runnable.Run(dispatchCtx, resolvedArgs)
	if err != nil {
		if node.ContinueOnError {
			// Capture error as result instead of failing
			if a.DebugMode {
				slog.Debug("tool execution failed, continuing", "error", err)
			}
			toolResult = map[string]any{
				"error":   err.Error(),
				"success": false,
			}
		} else {
			// Use LLM-based error recovery (same as LLM nodes)
			errCtx := ErrorContext{
				NodeName:     node.Name,
				NodeType:     "tool",
				ErrorType:    "tool_execution_error",
				ErrorMessage: err.Error(),
				AttemptCount: 1,
				MaxRetries:   1, // Tool nodes don't retry by default
				ToolName:     toolName,
				ToolArgs:     resolvedArgs,
			}

			// Use ErrorRecoveryNode to get intelligent analysis
			recovery := NewErrorRecoveryNode(a.LLM, a.DebugMode)
			decision, recoveryErr := recovery.Decide(ctx, errCtx)

			var title, reason, suggestion string
			if recoveryErr != nil {
				// LLM analysis failed, use basic error info
				title = "Tool Execution Failed"
				reason = fmt.Sprintf("Tool '%s' failed to execute", toolName)
				suggestion = ""
			} else {
				title = decision.Title
				reason = decision.Reason
				suggestion = decision.Suggestion
			}

			// Emit failure info with LLM analysis
			yield(&session.Event{
				Actions: session.EventActions{
					StateDelta: map[string]any{
						"_failure_info": map[string]any{
							"title":          title,
							"reason":         reason,
							"suggestion":     suggestion,
							"original_error": err.Error(),
							"node":           node.Name,
							"tool":           toolName,
						},
						"_processing_info": true,
					},
				},
			}, nil)

			// Store error details in state for error handler nodes
			state.Set("_last_error", err.Error())
			state.Set("_error_node", node.Name)
			state.Set("_has_error", true)
			state.Set("_error_analysis", reason)

			// Return false to end the node gracefully (flow will transition to next node or END)
			return false
		}
	} else if node.ContinueOnError {
		// Add success indicator when continue_on_error is enabled
		if toolResult == nil {
			toolResult = make(map[string]any)
		}
		toolResult["success"] = true
	}

	// 5. Process Output
	// toolResult is likely a struct (e.g. ShellCommandResult).
	// We need to extract fields based on `output_model` and `raw_tool_output`.

	// Redact credential values from tool output before storing in state.
	// resolve_credential now returns {{CREDENTIAL:...}} placeholders for
	// secret fields, so no exemption is needed.
	if a.Redactor != nil && toolResult != nil {
		toolResult = a.Redactor.RedactMap(toolResult)
	}

	// Convert result to map for easy access
	resultMap := make(map[string]interface{})
	// Marshal/Unmarshal hack to convert struct to map
	resultBytes, _ := json.Marshal(toolResult)
	err = json.Unmarshal(resultBytes, &resultMap)

	if err != nil {
		if a.DebugMode {
			slog.Debug("json unmarshal error", "error", err)
		}
	}

	// Handle raw_tool_output
	for key, mapping := range node.RawToolOutput {
		// mapping is the field name in the tool result (e.g. "stdout")
		// key is the state key to set (e.g. "pr_diff")
		if val, ok := resultMap[mapping]; ok {
			stateDelta[key] = val
			state.Set(key, val)
		}
	}

	// Handle output_model
	for key, typeName := range node.OutputModel {
		// Try to find the content from various common keys
		var val interface{}
		found := false

		// Priority list of keys to check
		keysToCheck := []string{"stdout", "output", "content", "formatted_diff", "result"}

		// 1. Check explicit mapping first (if any) - though output_model doesn't define mapping usually

		// 2. Check common keys
		for _, k := range keysToCheck {
			if v, ok := resultMap[k]; ok {
				val = v
				found = true
				break
			}
		}

		// 3. If not found, check if the result itself is a string or simple type (and not a map/struct wrapper)
		if !found && len(resultMap) == 0 {
			// This might happen if toolResult was not a struct/map
			val = toolResult
			found = true
		}

		if found {
			if typeName == "list" {
				// Check if val is already a slice
				switch v := val.(type) {
				case []interface{}:
					stateDelta[key] = v
					state.Set(key, v)
					continue
				case []string:
					// Convert to []interface{}
					var list []interface{}
					for _, s := range v {
						list = append(list, s)
					}
					stateDelta[key] = list
					state.Set(key, list)
					continue
				}

				// Try to parse as JSON list first
				valStr := fmt.Sprintf("%v", val)
				var list []any
				if err := json.Unmarshal([]byte(valStr), &list); err == nil {
					stateDelta[key] = list
					state.Set(key, list)
				} else {
					// Fallback: Split by newline
					lines := strings.Split(strings.TrimSpace(valStr), "\n")
					var cleanLines []string
					for _, line := range lines {
						trimmedLine := strings.TrimSpace(line)
						if trimmedLine != "" {
							cleanLines = append(cleanLines, trimmedLine)
						}
					}
					stateDelta[key] = cleanLines
					state.Set(key, cleanLines)
				}
			} else if typeName == "any" {
				// Just set the value as is
				stateDelta[key] = val
				state.Set(key, val)
			} else {
				// Default to string
				valStr := fmt.Sprintf("%v", val)
				stateDelta[key] = valStr
				state.Set(key, valStr)
			}
		} else {
			if a.DebugMode {
				slog.Warn("could not find output for key in tool result", "key", key, "result", resultMap)
			}
		}
	}

	// Clear awaiting_approval state
	state.Set("awaiting_approval", false)

	// IMPORTANT: Clear tool approval AFTER execution to ensure loops require re-approval
	// This is critical for circular flows where the same tool node is executed multiple
	// times with different parameters (e.g., paginated API calls)
	if !node.ToolsAutoApproval {
		approvalKey := fmt.Sprintf("approval:%s:%s", node.Name, toolName)
		state.Set(approvalKey, false)
	}

	// Yield result event
	yield(&session.Event{
		Actions: session.EventActions{
			StateDelta: stateDelta,
		},
	}, nil)

	return true
}
