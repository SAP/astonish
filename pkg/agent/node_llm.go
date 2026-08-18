package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// callbackEventBuffer collects events produced by BeforeToolCallback and
// AfterToolCallback so they can be yielded later on the goroutine that
// owns the range-over-func iterator.
//
// ADK's handleFunctionCalls spawns goroutines for concurrent tool calls.
// Calling yield directly from those goroutines violates Go's range-over-func
// contract and causes a fatal panic:
//
//	"runtime error: range function continued iteration after loop body panic"
//
// Instead, callbacks append events here, and the main event loop drains them
// after each ADK event.
type callbackEventBuffer struct {
	mu     sync.Mutex
	events []*session.Event
}

func (b *callbackEventBuffer) append(event *session.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
}

// drain returns all buffered events and resets the buffer.
// Must be called from the goroutine that owns yield.
func (b *callbackEventBuffer) drain() []*session.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.events) == 0 {
		return nil
	}
	out := b.events
	b.events = nil
	return out
}

// executeLLMNode executes an LLM node with intelligent retry logic
func (a *AstonishAgent) executeLLMNode(ctx agent.InvocationContext, node *config.Node, nodeName string, state session.State, yield func(*session.Event, error) bool) bool {
	// Clear any previous error state at the start
	state.Set("_has_error", false)
	state.Set("_last_error", "")
	state.Set("_error_node", "")

	// Determine max retries
	maxRetries := 3 // default
	if node.MaxRetries > 0 {
		maxRetries = node.MaxRetries
	}

	if a.DebugMode {
		slog.Warn("starting execute llm node", "component", "retry", "node", nodeName, "max_retries", maxRetries)
	}

	// Determine retry strategy
	useIntelligentRetry := true
	if node.RetryStrategy == "simple" {
		useIntelligentRetry = false
	}

	// Error context for intelligent recovery
	errorHistory := []string{}
	var lastErr error // Track the last error for use after the loop

	// Retry loop
	for attempt := 0; attempt < maxRetries; attempt++ {
		if a.DebugMode && attempt > 0 {
			slog.Warn("retry attempt", "component", "retry", "attempt", attempt+1, "max_retries", maxRetries, "node", nodeName)
		}

		// Execute the node
		success, err := a.executeLLMNodeAttempt(ctx, node, nodeName, state, yield)
		lastErr = err // Track the last error

		if success {
			// Success! Clear any error state and return
			state.Set("_error_context", nil)
			state.Set("_has_error", false)
			return true
		}

		// Node failed - decide whether to retry
		if err == nil {
			// No error but failed - check if we're pausing for user approval
			// This is a valid pause state, not a failure!
			awaitingApproval, _ := state.Get("awaiting_approval")
			if isAwaiting, ok := awaitingApproval.(bool); ok && isAwaiting {
				if a.DebugMode {
					slog.Warn("pausing for user approval", "component", "retry")
				}
				// This is a pause, not a failure - ensure _has_error is false
				// so the main loop will pause (return) instead of going to END
				state.Set("_has_error", false)
				return false // Pause - main loop will return when _has_error=false
			}
			// Truly failed with no error (shouldn't happen, but handle it)
			return false
		}

		// Build error context
		errCtx := ErrorContext{
			NodeName:       nodeName,
			NodeType:       node.Type,
			ErrorType:      "execution_error",
			ErrorMessage:   err.Error(),
			AttemptCount:   attempt + 1,
			MaxRetries:     maxRetries,
			PreviousErrors: errorHistory,
		}

		// Store error context in state
		state.Set("_error_context", errCtx)
		state.Set("_has_error", true)

		// Check if this is the last attempt
		isLastAttempt := (attempt >= maxRetries-1)

		// Decide whether to retry using intelligent recovery or simple retry
		var shouldRetry bool
		var errorTitle string
		var oneLiner string
		var explanation string

		if useIntelligentRetry && !isLastAttempt {
			// Use LLM-based error recovery
			recovery := NewErrorRecoveryNode(a.LLM, a.DebugMode)
			var recoveryErr error
			decision, recoveryErr := recovery.Decide(ctx, errCtx)

			if recoveryErr != nil {
				// Error recovery failed, fall back to simple retry
				if a.DebugMode {
					slog.Warn("error recovery failed, using simple retry", "component", "retry", "error", recoveryErr)
				}
				shouldRetry = true
				errorTitle = "Retry Attempt"
				oneLiner = "Error analysis failed"
				explanation = "Error analysis failed, retrying automatically"
			} else {
				shouldRetry = decision.ShouldRetry
				errorTitle = decision.Title
				oneLiner = decision.OneLiner
				explanation = decision.Reason

				if decision.Suggestion != "" {
					explanation += "\n\nSuggestion: " + decision.Suggestion
				}
			}
		} else if !isLastAttempt {
			// Simple retry strategy
			shouldRetry = true
			errorTitle = "Retry Attempt"
			oneLiner = fmt.Sprintf("Attempt %d/%d", attempt+2, maxRetries)
			explanation = fmt.Sprintf("Retrying automatically (attempt %d/%d)", attempt+2, maxRetries)
		}

		// Emit retry badge ONLY if we are actually going to retry.
		// This prevents showing "Retry" on the last attempt (where we show Max Retries Failure)
		// or when the agent decides to Abort (where we show the Abort Failure).
		if shouldRetry {
			if oneLiner == "" {
				oneLiner = errorTitle
			}

			if !yield(&session.Event{
				Actions: session.EventActions{
					StateDelta: map[string]any{
						"_retry_info": map[string]any{
							"attempt":     attempt + 1,
							"max_retries": maxRetries,
							"reason":      oneLiner,
						},
						"_processing_info": true,
					},
				},
			}, nil) {
				return false
			}
		}

		if isLastAttempt {
			// Show final error after retry badge
			if a.DebugMode {
				slog.Warn("max retries exceeded", "component", "retry", "max_retries", maxRetries, "node", nodeName)
			}

			// Emit final failure info via StateDelta
			if !yield(&session.Event{
				Actions: session.EventActions{
					StateDelta: map[string]any{
						"_failure_info": map[string]any{
							"title":          "Max Retries Exceeded",
							"reason":         fmt.Sprintf("Failed after %d attempts. The error persisted across all retry attempts.", maxRetries),
							"original_error": err.Error(),
						},
						"_processing_info": true,
					},
				},
			}, nil) {
				// Yield was cancelled, stop immediately
				return false
			}

			// Store error details in state for error handler nodes
			state.Set("_last_error", err.Error())
			state.Set("_error_node", nodeName)
			state.Set("_has_error", true)

			if a.DebugMode {
				slog.Warn("max retries message yielded, breaking retry loop", "component", "retry")
			}

			// Break out of retry loop
			break
		}

		if !shouldRetry {
			// Error recovery decided to abort - don't retry
			if a.DebugMode {
				slog.Warn("error recovery decided to abort", "component", "retry", "explanation", explanation)
			}

			// Split explanation into reason and suggestion
			reason := explanation
			suggestion := ""

			// Check if explanation contains a "Suggestion:" section
			if strings.Contains(explanation, "Suggestion: ") {
				parts := strings.SplitN(explanation, "Suggestion: ", 2)
				if len(parts) == 2 {
					reason = strings.TrimSpace(parts[0])
					suggestion = strings.TrimSpace(parts[1])
				}
			} else if strings.Contains(explanation, "\n\nSuggestion: ") {
				parts := strings.SplitN(explanation, "\n\nSuggestion: ", 2)
				if len(parts) == 2 {
					reason = strings.TrimSpace(parts[0])
					suggestion = strings.TrimSpace(parts[1])
				}
			}

			title := errorTitle
			if title == "" {
				title = "Error"
			}

			// Emit abort info via StateDelta (same pattern as _failure_info)
			if !yield(&session.Event{
				Actions: session.EventActions{
					StateDelta: map[string]any{
						"_failure_info": map[string]any{
							"title":          title,
							"reason":         reason,
							"suggestion":     suggestion,
							"original_error": err.Error(),
						},
						"_processing_info": true, // No "Agent:" prefix for this display
					},
				},
			}, nil) {
				// Yield was cancelled, stop immediately
				return false
			}

			// Store error details in state for error handler nodes
			state.Set("_last_error", err.Error())
			state.Set("_error_node", nodeName)
			state.Set("_has_error", true)

			if a.DebugMode {
				slog.Warn("abort message yielded, breaking retry loop", "component", "retry")
			}

			// Break out of retry loop - error recovery decided not to retry
			break
		}

		// Continue with retry
		if a.DebugMode {
			slog.Warn("proceeding with retry", "component", "retry", "attempt", attempt+2, "max_retries", maxRetries)
		}

		// Add error to history
		errorHistory = append(errorHistory, err.Error())

		// Exponential backoff before retry: 2s, 4s, 8s, ...
		// Prevents hammering the provider on rate limits (429) and transient errors.
		backoff := time.Duration(1<<uint(attempt+1)) * time.Second
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
		if a.DebugMode {
			slog.Warn("retry backoff", "component", "retry", "delay", backoff, "node", nodeName)
		}
		select {
		case <-time.After(backoff):
			// Backoff complete, proceed with retry
		case <-ctx.Done():
			// Context cancelled during backoff — stop retrying
			if a.DebugMode {
				slog.Warn("context cancelled during retry backoff", "component", "retry", "node", nodeName)
			}
			state.Set("_last_error", ctx.Err().Error())
			state.Set("_error_node", nodeName)
			state.Set("_has_error", true)
			return false
		}

		// Continue to next attempt
	}

	// If we exit the loop, it means we exhausted retries or error recovery decided to abort
	// Check if there's an error transition in the flow for this node
	nextNode, transErr := a.getNextNode(nodeName, state)

	if transErr != nil || nextNode == "" {
		// No error transition configured - stop execution with error
		if a.DebugMode {
			slog.Warn("no error transition found, stopping execution", "component", "retry", "node", nodeName)
		}
		yield(nil, fmt.Errorf("node '%s' failed: %w", nodeName, lastErr))
		return false
	}

	// There is a transition (possibly to an error handler node)
	// Return false to indicate failure, let the main loop handle the transition
	if a.DebugMode {
		slog.Warn("error transition found", "component", "retry", "from_node", nodeName, "to_node", nextNode)
	}
	return false
}

// executeLLMNodeAttempt executes a single attempt of an LLM node using ADK's llmagent
func (a *AstonishAgent) executeLLMNodeAttempt(ctx agent.InvocationContext, node *config.Node, nodeName string, state session.State, yield func(*session.Event, error) bool) (bool, error) {
	// Apply per-node timeout to prevent indefinite hangs on stalled LLM calls.
	// The timeout covers the entire attempt (LLM call + tool calls + processing).
	// 10 minutes allows research-heavy tasks (e.g., browser automation with many
	// page visits) to complete without being prematurely killed.
	const nodeTimeout = 10 * time.Minute
	timeoutCtx, cancel := context.WithTimeout(ctx, nodeTimeout)
	defer cancel()
	ctx = ctx.WithContext(timeoutCtx)

	// Render prompt and system instruction
	userPrompt := a.renderString(node.Prompt, state)
	systemInstruction := a.renderString(node.System, state)

	// Append raw_context verbatim (no renderString) — used for reference scripts
	// that contain shell syntax ({}, ${}, awk, jq) which would be corrupted by
	// state variable interpolation.
	if node.RawContext != "" {
		if systemInstruction != "" {
			systemInstruction += "\n\n"
		}
		// Resolve state variables nested INSIDE credential placeholders only.
		// Pattern: {{CREDENTIAL:{state_var}:field}} → {{CREDENTIAL:resolved:field}}
		// This allows flows to use dynamic credential names (e.g. from input nodes)
		// while keeping the rest of raw_context untouched (shell ${}, awk {}, etc.).
		rawCtx := a.resolveCredentialVarsInRawContext(node.RawContext, state)
		systemInstruction += rawCtx
	}

	// Use system instruction as the main instruction for the agent
	// This ensures it goes to the System Prompt in the LLM request
	instruction := systemInstruction

	// If no system instruction, use the user prompt as instruction (fallback behavior)
	// But for Bedrock/Claude, we prefer separation.
	if instruction == "" {
		instruction = "You are a helpful AI assistant."
	}

	// If the instruction contains {{CREDENTIAL:...}} placeholders, append a
	// directive telling the LLM to preserve them verbatim in tool arguments.
	// The placeholders are resolved at the tool execution boundary (BeforeToolCallback)
	// AFTER the LLM emits the tool call — the LLM must NOT interpret or strip them.
	if credentials.ContainsPlaceholder(instruction) {
		instruction += "\n\n" +
			"IMPORTANT: The text above contains {{CREDENTIAL:...}} placeholder tokens (e.g. " +
			"{{CREDENTIAL:openstack:username}}). These are NOT literal values — they are " +
			"secure credential references that will be resolved automatically at execution time. " +
			"When you use these values in tool calls (e.g. shell_command), you MUST copy them " +
			"EXACTLY as-is, including the double curly braces. Do NOT replace them with the " +
			"credential name, do NOT strip the braces, and do NOT attempt to resolve them yourself."
	}

	if a.DebugMode {
		slog.Debug("final user prompt", "prompt", userPrompt)
		slog.Debug("final system instruction", "instruction", instruction)
	}

	// Manually append the User Message to the session history
	// This ensures that the LLM sees a User Message even if llmagent doesn't pick it up from context
	// or if history is empty.
	userEvent := &session.Event{
		InvocationID: ctx.InvocationID(),
		Branch:       ctx.Branch(),
		Author:       "user",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Parts: []*genai.Part{{Text: userPrompt}},
				Role:  "user",
			},
		},
	}

	sess := ctx.Session()

	if a.SessionService != nil {

		// Unwrap ScopedSession if present, as SessionService might expect the underlying session type
		if scopedSess, ok := sess.(*ScopedSession); ok {
			sess = scopedSess.Session
		}

		// Try to append with (potentially unwrapped) session object
		if err := a.SessionService.AppendEvent(ctx, sess, userEvent); err != nil {
			// Retry with session fetched via Get (last resort)
			if sess.ID() != "" {
				appName := sess.AppName()
				if appName == "" {
					appName = "astonish"
				}
				userID := sess.UserID()
				if userID == "" {
					userID = "console_user"
				}

				getResp, getErr := a.SessionService.Get(ctx, &session.GetRequest{
					SessionID: sess.ID(),
					AppName:   appName,
					UserID:    userID,
				})
				if getErr == nil && getResp != nil && getResp.Session != nil {
					if err := a.SessionService.AppendEvent(ctx, getResp.Session, userEvent); err != nil {
						slog.Error("failed to append user event to session", "error", err)
					}
				}
			}
		}
	}

	// 2. Initialize LLM Agent
	// We need to pass tools if the node uses them
	var nodeTools []tool.Tool
	if node.Tools {
		// Validate that all selected tools exist
		if len(node.ToolsSelection) > 0 {
			foundTools := make(map[string]bool)

			// Check internal tools
			for _, t := range a.Tools {
				foundTools[t.Name()] = true
			}

			// Check MCP toolsets
			if len(a.Toolsets) > 0 {
				minimalCtx := &minimalReadonlyContext{Context: ctx}
				for _, ts := range a.Toolsets {
					tools, err := ts.Tools(minimalCtx)
					if err == nil {
						for _, t := range tools {
							foundTools[t.Name()] = true
						}
					}
				}
			}

			var missingTools []string
			for _, selected := range node.ToolsSelection {
				if !foundTools[selected] {
					missingTools = append(missingTools, selected)
				}
			}

			if len(missingTools) > 0 {
				toolErr := fmt.Errorf("configured tools not found: %s", strings.Join(missingTools, ", "))
				yield(nil, toolErr)
				return false, toolErr
			}
		}

		// Filter based on ToolsSelection
		if len(node.ToolsSelection) > 0 {

			for _, t := range a.Tools {
				for _, selected := range node.ToolsSelection {
					// Check against the underlying tool name if wrapped?
					// t.Name() should return the name.
					if t.Name() == selected {
						nodeTools = append(nodeTools, t)

					}
				}
			}

		} else {
			// If no selection, add all? Or none?
			// Python adds all if selection is empty?
			// For now, assume selection is required

			nodeTools = a.Tools
		}
	} else {

	}

	// Inject tool use instruction if tools are enabled
	// Inject tool use instruction if tools are enabled
	if node.Tools {
		instruction += "\n\nIMPORTANT: You have access to tools that you MUST use to complete this task. Do not describe what you would do or say you are waiting for results. Instead, immediately call the appropriate tool with the required parameters. The tools are available and ready to use right now."
	}

	// Inject instruction for raw_tool_output
	if len(node.RawToolOutput) > 0 {
		instruction += "\n\nIMPORTANT: The tool will return the raw content directly to the state. Your final task for this step is to confirm its retrieval."
	}

	// Build OutputSchema from output_model if defined
	// This leverages ADK's native structured output support
	var outputSchema *genai.Schema
	var outputKey string
	if len(node.OutputModel) > 0 {
		// Add explicit instruction about the required output format
		instruction += "\n\nIMPORTANT: Your response MUST be a valid JSON object with the following structure:\n"
		instruction += "{\n"
		for key, typeName := range node.OutputModel {
			instruction += fmt.Sprintf("  \"%s\": <%s>,\n", key, typeName)
		}
		instruction += "}\n"
		instruction += "Do not include any other text, explanations, or markdown formatting. Return ONLY the JSON object."

		properties := make(map[string]*genai.Schema)
		required := []string{}

		for key, typeName := range node.OutputModel {
			var propType genai.Type
			var items *genai.Schema

			switch typeName {
			case "str", "string":
				propType = genai.TypeString
			case "int", "integer":
				propType = genai.TypeInteger
			case "float", "number":
				propType = genai.TypeNumber
			case "bool", "boolean":
				propType = genai.TypeBoolean
			case "list", "array":
				propType = genai.TypeArray
				// Default to string items, can be enhanced later
				items = &genai.Schema{Type: genai.TypeString}
			case "dict", "object", "any":
				propType = genai.TypeObject
			default:
				propType = genai.TypeString
			}

			schema := &genai.Schema{
				Type: propType,
			}
			if items != nil {
				schema.Items = items
			}

			properties[key] = schema
			required = append(required, key)
		}

		outputSchema = &genai.Schema{
			Type:       genai.TypeObject,
			Properties: properties,
			Required:   required,
		}

		// If there is only one output key, we might want to map it directly
		// But for now, we stick to the map/object structure
	}

	// Create ADK llmagent for this node
	// Strategy:
	// - Internal tools go via Tools field
	// - MCP toolsets go via Toolsets field
	// - OutputSchema is used for structured output (replaces manual JSON parsing)
	// Declare l (agent) early so it can be captured by the callback
	var l agent.Agent

	var llmAgent agent.Agent
	var err error

	// Buffer for events produced by callbacks running in ADK goroutines.
	// ADK's handleFunctionCalls spawns goroutines for concurrent tool calls,
	// and calling yield from those goroutines causes a fatal panic. Callbacks
	// append events here; the main event loop drains them on the owning goroutine.
	cbBuf := &callbackEventBuffer{}

	var internalTools []tool.Tool
	if node.Tools {
		// Add universal instruction for tool-enabled nodes to prevent repeating completed work
		// This helps models like GPT that may not correctly interpret conversation history
		instruction += "\n\nIMPORTANT: When executing tools, check the conversation history first. " +
			"If a tool has already been called and returned a successful result (not 'pending_approval'), " +
			"do NOT call that tool again. Proceed only with tools that haven't completed successfully yet."

		// Prepare internal tools (no wrapping needed - callback handles approval)
		if len(nodeTools) > 0 {
			internalTools = append(internalTools, nodeTools...)
		}

		// Prepare MCP toolsets (no wrapping needed - callback handles approval)
		var mcpToolsets []tool.Toolset
		if len(a.Toolsets) > 0 {
			for _, ts := range a.Toolsets {
				// Skip toolsets that don't contain any of the requested tools (if filtering is enabled)
				if len(node.ToolsSelection) > 0 {
					// Check if this toolset has any of the requested tools
					minimalCtx := &minimalReadonlyContext{Context: ctx}
					tsTools, err := ts.Tools(minimalCtx)
					if err != nil {

						continue
					}

					// Check if any tool in this toolset matches our selection
					hasMatchingTool := false
					for _, t := range tsTools {
						for _, allowed := range node.ToolsSelection {
							if t.Name() == allowed {
								hasMatchingTool = true
								break
							}
						}
						if hasMatchingTool {
							break
						}
					}

					if !hasMatchingTool {

						continue
					}
				}

				mcpToolsets = append(mcpToolsets, ts)

			}
		}

		// Apply tools_selection filter if specified
		if len(node.ToolsSelection) > 0 {

			// Filter internal tools
			var filteredInternalTools []tool.Tool
			for _, t := range internalTools {
				toolName := t.Name()
				for _, allowed := range node.ToolsSelection {
					if toolName == allowed {
						filteredInternalTools = append(filteredInternalTools, t)
						break
					}
				}
			}
			internalTools = filteredInternalTools

			// Wrap MCP toolsets with FilteredToolset to filter tools
			var filteredMCPToolsets []tool.Toolset
			for _, ts := range mcpToolsets {
				filteredMCPToolsets = append(filteredMCPToolsets, &FilteredToolset{
					underlying:   ts,
					allowedTools: node.ToolsSelection,
				})
			}
			mcpToolsets = filteredMCPToolsets

		}

		// Create BeforeToolCallback for approval if needed
		var beforeToolCallbacks []llmagent.BeforeToolCallback
		var afterToolCallbacks []llmagent.AfterToolCallback

		if !node.ToolsAutoApproval && !a.AutoApprove {
			beforeToolCallbacks = []llmagent.BeforeToolCallback{
				a.buildApprovalCallback(node, state, cbBuf),
			}
		} else {
			// Auto-approval enabled: Register callback to buffer visual event
			// and then allow the tool to execute normally
			beforeToolCallbacks = []llmagent.BeforeToolCallback{
				func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
					toolName := t.Name()

					// Buffer auto-approval visual event (NOT yield — runs in ADK goroutine)
					prompt := a.formatToolApprovalRequest(toolName, args)
					cbBuf.append(&session.Event{
						LLMResponse: model.LLMResponse{
							Content: &genai.Content{
								Parts: []*genai.Part{{Text: prompt}},
								Role:  "model",
							},
						},
						Actions: session.EventActions{
							StateDelta: map[string]any{
								"auto_approved": true,
							},
						},
					})

					// Return nil to allow the actual tool to execute
					return nil, nil
				},
			}
		}

		// Add credential placeholder substitution callback.
		// Uses SubstituteAndRestore so the AfterToolCallback can undo the
		// in-place mutation, keeping placeholders in the session event.
		// Per-call restore functions keyed by FunctionCallID so parallel
		// tool calls don't clobber each other's restore closures.
		var restoreFuncs sync.Map // map[string]func()
		{
			agentResolver := a.CredentialStore // may be nil if file-based store failed
			beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				// In platform mode, prefer the tenant-scoped PG credential store
				// injected into the context. Fall back to agent-level store.
				var resolver credentials.CredentialResolver
				if cs := store.CredentialStoreFromContext(ctx); cs != nil {
					resolver = credentials.NewStoreAdapter(cs)
				} else if agentResolver != nil {
					resolver = agentResolver
				}

				if resolver == nil {
					// No credential store available — unresolved placeholders are
					// treated as literal text (e.g., documentation examples).
					return nil, nil
				}

				// Use shell-safe env-var injection for shell_command tools.
				var shellFields []string
				if t.Name() == "shell_command" || t.Name() == "process_write" {
					shellFields = []string{"command"}
				}

				// Register resolved credential values with the Redactor BEFORE
				// substitution. This ensures dynamically-fetched tokens (e.g.,
				// Keystone) are known to RedactMap in the AfterToolCallback,
				// even when the resolver is a DB-backed adapter that doesn't
				// share the agent's Redactor instance.
				credentials.RegisterResolvedWithRedactor(args, resolver, a.Redactor)

				credRestore := credentials.SubstituteAndRestore(args, resolver, shellFields...)

				// After substitution, check if any placeholders remain unresolved.
				// Unresolved placeholders are left as literal text — this handles
				// the case where the LLM generates documentation or code that
				// describes the placeholder format without intending to use a
				// real credential. Downstream auth failures will surface naturally.
				if unresolved := credentials.UnresolvedCredentialNames(args); len(unresolved) > 0 {
					slog.Debug("credential placeholders remain unresolved (treating as literal text)",
						"component", "credentials", "tool", t.Name(), "unresolved", unresolved)
				}

				callID := ctx.FunctionCallID()
				if prev, loaded := restoreFuncs.Load(callID); loaded {
					prevFn := prev.(func())
					restoreFuncs.Store(callID, func() { credRestore(); prevFn() })
				} else {
					restoreFuncs.Store(callID, credRestore)
				}
				return nil, nil
			})
		}

		// Resolve <<<SECRET_N>>> tokens in tool args to real values.
		if a.PendingSecrets != nil {
			vault := a.PendingSecrets
			beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				secRestore := vault.SubstituteAndRestore(args)
				callID := ctx.FunctionCallID()
				if prev, loaded := restoreFuncs.Load(callID); loaded {
					prevFn := prev.(func())
					restoreFuncs.Store(callID, func() { secRestore(); prevFn() })
				} else {
					restoreFuncs.Store(callID, secRestore)
				}
				return nil, nil
			})
		}

		// Add AfterToolCallback for debugging and raw_tool_output handling.
		// Wraps buildAfterToolCallback with credential and pending secret placeholder restore.
		innerAfterTool := a.buildAfterToolCallback(node, state, cbBuf)
		afterToolCallbacks = []llmagent.AfterToolCallback{
			func(ctx tool.Context, t tool.Tool, args map[string]any, result map[string]any, err error) (map[string]any, error) {
				if fn, ok := restoreFuncs.LoadAndDelete(ctx.FunctionCallID()); ok {
					fn.(func())()
				}
				return innerAfterTool(ctx, t, args, result, err)
			},
		}

		llmAgent, err = llmagent.New(llmagent.Config{
			Name:  nodeName,
			Model: a.LLM,
			// Use InstructionProvider instead of Instruction to bypass ADK's
			// InjectSessionState template processing. We already resolved all
			// {var} placeholders via renderString; ADK's stricter processor
			// would error on state keys that exist but are empty, or on literal
			// braces in shell scripts / JSON content.
			InstructionProvider: func(_ agent.ReadonlyContext) (string, error) {
				return instruction, nil
			},
			Tools:               internalTools,
			Toolsets:            mcpToolsets,
			OutputSchema:        outputSchema,
			OutputKey:           outputKey,
			BeforeToolCallbacks: beforeToolCallbacks,
			AfterToolCallbacks:  afterToolCallbacks,
		})
	} else {
		// No tools enabled
		llmAgent, err = llmagent.New(llmagent.Config{
			Name:  nodeName,
			Model: a.LLM,
			InstructionProvider: func(_ agent.ReadonlyContext) (string, error) {
				return instruction, nil
			},
			Tools:        nodeTools,
			OutputSchema: outputSchema,
			OutputKey:    outputKey,
		})
	}
	l = llmAgent // Assign to 'l' after creation

	// Wrap session in LiveSession to ensure fresh history with node-scoped filtering
	liveSess := &LiveSession{
		service: a.SessionService,
		ctx:     ctx,
		base:    sess,
		agent:   a,
	}

	// Create a ScopedContext that uses the LiveSession and the correct Agent (l)
	// This ensures:
	// 1. llmagent sees fresh history (via LiveSession.Events)
	// 2. llmagent sees itself as the agent (via ScopedContext.Agent override), fixing ContentsRequestProcessor
	scopedCtx := &ScopedContext{
		InvocationContext: ctx,
		session:           liveSess,
		state:             state,
		agent:             l,
	}

	// Use scopedCtx for Run
	ctx = scopedCtx

	if err != nil {
		return false, fmt.Errorf("failed to create llmagent: %w", err)
	}

	// Reset the pause flag before starting
	state.Set("force_pause", false)

	// NOTE: Text suppression is now handled by console.go buffering logic.
	// We allow all text to flow through so greetings before tool calls are visible.
	// The console.go will buffer and only show relevant text (before tool boxes).

	// Run the agent - ADK handles everything (native function calling)
	var fullResponse strings.Builder
	var debugTextBuffer strings.Builder // Buffer for debug output
	toolCallCount := 0
	const maxToolCalls = 20 // Maximum tool calls to prevent infinite loops

	// Check if we should use ReAct fallback
	useReActFallback := false
	if fallbackVal, err := state.Get("_use_react_fallback"); err == nil {
		if b, ok := fallbackVal.(bool); ok && b {
			useReActFallback = true
		}
	}

	if useReActFallback {
		return a.executeReActFallback(ctx, node, nodeName, state, yield, internalTools, instruction, userPrompt)
	}

	// We'll wrap the iteration in a function to allow retry
	runAgent := func() iter.Seq2[*session.Event, error] {
		return l.Run(ctx)
	}

	// Execute with fallback retry
	for event, err := range runAgent() {
		if err != nil {
			// Check for "Tool calling is not supported" error or OpenRouter 404
			if strings.Contains(err.Error(), "Tool calling is not supported") ||
				strings.Contains(err.Error(), "No endpoints found that support tool use") ||
				strings.Contains(err.Error(), "Function calling is not enabled") ||
				strings.Contains(err.Error(), "does not support tools") ||
				strings.Contains(err.Error(), "`tool calling` is not supported") {
				if a.DebugMode {
					slog.Debug("caught tool calling error, switching to react fallback", "error", err)
				}

				// Enable fallback for future runs
				state.Set("_use_react_fallback", true)

				return a.executeReActFallback(ctx, node, nodeName, state, yield, internalTools, instruction, userPrompt)
			}

			// Genuine error
			return false, err
		}

		// [ERROR HANDLING] Track tool errors but let them flow to the LLM
		// The LLM needs to see the error response to understand the tool failed
		// We'll stop after the LLM processes the error
		hasToolError := false
		var toolErrorMsg string
		if event.LLMResponse.Content != nil {
			for _, part := range event.LLMResponse.Content.Parts {
				if part.FunctionResponse != nil {
					resp := part.FunctionResponse.Response

					// Check for the "error" key
					if errVal, hasError := resp["error"]; hasError && errVal != nil {
						toolName := part.FunctionResponse.Name

						// Convert error to string if it's an error type
						var errorStr string
						if errObj, ok := errVal.(error); ok {
							errorStr = errObj.Error()
						} else {
							errBytes, _ := json.Marshal(errVal)
							errorStr = string(errBytes)
						}

						if a.DebugMode {
							slog.Debug("tool failed with error", "tool", toolName, "error", errorStr)
						}

						hasToolError = true
						toolErrorMsg = fmt.Sprintf("tool '%s' failed: %s", toolName, errorStr)
						// Don't return yet - let the error response flow to the LLM first
					}
				}
			}
		}

		// Debug logging for event flow
		if a.DebugMode {
			if event.LLMResponse.Content != nil {
				for _, part := range event.LLMResponse.Content.Parts {
					if part.FunctionCall != nil {
						slog.Debug("function call", "name", part.FunctionCall.Name)
						toolCallCount++
					}
					if part.FunctionResponse != nil {
						respJSON, _ := json.MarshalIndent(part.FunctionResponse.Response, "", "  ")
						slog.Debug("tool execution result", "tool", part.FunctionResponse.Name, "response", string(respJSON))
					}
					if part.Text != "" {
						// Buffer text instead of printing immediately
						debugTextBuffer.WriteString(part.Text)
					}
				}
			}
		}

		// Accumulate text response for output_model (Unconditionally at start of loop)
		if event.LLMResponse.Content != nil {
			for _, part := range event.LLMResponse.Content.Parts {
				if part.Text != "" {
					fullResponse.WriteString(part.Text)
				}
			}
		}

		// Count tool calls to prevent infinite loops
		if event.LLMResponse.Content != nil {
			for _, part := range event.LLMResponse.Content.Parts {
				if part.FunctionCall != nil {
					if !a.DebugMode {
						toolCallCount++
					}
				}
			}
		}

		// Hard limit to prevent infinite loops
		if toolCallCount > maxToolCalls {
			if a.DebugMode {
				slog.Debug("tool call limit exceeded", "max_tool_calls", maxToolCalls, "node", nodeName)
			}
			return false, fmt.Errorf("tool call limit exceeded (%d) for node '%s'", maxToolCalls, nodeName)
		}

		// 1. Determine if this event should be displayed to the user
		shouldYieldEvent := true

		if event.LLMResponse.Content != nil && len(event.LLMResponse.Content.Parts) > 0 {
			// Check if this is a text-only event (no tool calls/responses)
			isTextOnly := true
			for _, part := range event.LLMResponse.Content.Parts {
				if part.FunctionCall != nil || part.FunctionResponse != nil {
					isTextOnly = false
					break
				}
			}

			// Suppress text-only events when node has output_model
			// This prevents raw JSON from being displayed to the user.
			// The JSON is parsed and values are distributed to StateDelta.
			// If user_message is defined, it will handle displaying content from state.
			if isTextOnly && len(node.OutputModel) > 0 {
				shouldYieldEvent = false
			}
		}

		// 2. FORWARD the event if it should be displayed
		if shouldYieldEvent {
			if !yield(event, nil) {
				return false, nil
			}
		}

		// 2.1 Drain events buffered by callbacks (BeforeToolCallback /
		// AfterToolCallback). These callbacks run in ADK goroutines where
		// calling yield directly would panic, so they buffer events instead.
		// We drain here on the yield-owning goroutine.
		for _, buffered := range cbBuf.drain() {
			if !yield(buffered, nil) {
				return false, nil
			}
		}

		// 2a. If we detected a tool error, stop after yielding the error response
		// This allows the LLM to see the error before we stop
		if hasToolError {
			return false, fmt.Errorf("%s", toolErrorMsg)
		}

		// 2b. CHECK FOR INTERRUPT SIGNAL
		// If the tool set this flag, we must stop immediately
		if shouldPause, _ := state.Get("force_pause"); shouldPause == true {
			// Clear the flag so it doesn't block the next run
			state.Set("force_pause", false)
			return false, nil // Stops the loop, effectively pausing the agent
		}

		// 4. Handle raw_tool_output state updates
		if event.LLMResponse.Content != nil {
			for _, part := range event.LLMResponse.Content.Parts {
				if part.FunctionResponse != nil {
					// This is a tool response - print it for debugging
					if a.DebugMode {
						respJSON, _ := json.MarshalIndent(part.FunctionResponse.Response, "", "  ")
						slog.Debug("tool response", "tool", part.FunctionResponse.Name, "response", string(respJSON))
					}

				}
			}
		}
	}

	// Print accumulated debug text
	if a.DebugMode && debugTextBuffer.Len() > 0 {
		slog.Debug("full llm response", "response", debugTextBuffer.String())
	}

	if a.DebugMode {
		slog.Debug("after llmagent run loop", "output_model_length", len(node.OutputModel), "user_message_length", len(node.UserMessage))
	}

	// Distribute output_model values by parsing the LLM's text response
	// ADK's OutputSchema doesn't work reliably with tool-enabled nodes
	if len(node.OutputModel) > 0 {
		// Get the accumulated text response
		responseText := strings.TrimSpace(fullResponse.String())

		if a.DebugMode {
			slog.Debug("attempting to extract output_model", "response_length", len(responseText))
		}

		if responseText != "" {
			// Try to parse as JSON
			cleaned := a.cleanAndFixJson(responseText)

			if a.DebugMode {
				slog.Debug("cleaned json", "json", cleaned)
			}

			var parsedOutput map[string]any
			if err := json.Unmarshal([]byte(cleaned), &parsedOutput); err == nil {
				if a.DebugMode {
					slog.Debug("successfully parsed json", "keys", getKeys(parsedOutput))
				}

				// Distribute values to individual output_model keys
				delta := make(map[string]any)
				for key := range node.OutputModel {
					if val, ok := parsedOutput[key]; ok {
						if a.DebugMode {
							slog.Debug("setting state key", "key", key, "value_type", fmt.Sprintf("%T", val))
						}
						state.Set(key, val)
						delta[key] = val
					} else {
						if a.DebugMode {
							slog.Debug("key not found in parsed output", "key", key)
						}
					}
				}

				// Emit state delta if we updated anything
				if len(delta) > 0 {
					if a.DebugMode {
						slog.Debug("emitting state delta", "keys", getKeys(delta))
					}
					yield(&session.Event{
						Actions: session.EventActions{
							StateDelta: delta,
						},
					}, nil)
				}
			} else {
				// JSON parsing failed - return error to trigger retry
				if a.DebugMode {
					slog.Debug("failed to parse json", "error", err)
				}
				// Create a descriptive error that will help intelligent retry
				truncatedPreview := cleaned
				if len(truncatedPreview) > 200 {
					truncatedPreview = truncatedPreview[:200] + "..."
				}
				return false, fmt.Errorf("failed to parse LLM output as JSON for output_model extraction: %v. Response preview: %s", err, truncatedPreview)
			}
		} else {
			// Empty response when output_model is expected - return error
			if a.DebugMode {
				slog.Debug("response text is empty, required for output_model extraction")
			}
			return false, fmt.Errorf("LLM returned empty response but output_model requires JSON output with keys: %v", getKeysStr(node.OutputModel))
		}
	}

	// Emit raw_tool_output values as StateDelta for persistence across session restarts
	// The AfterToolCallback stores the data in state, but we must also emit it as StateDelta
	// so it's preserved in event history when the session pauses for user input
	if len(node.RawToolOutput) > 0 {
		delta := make(map[string]any)
		for stateKey := range node.RawToolOutput {
			val, err := state.Get(stateKey)
			if err == nil && val != nil {
				// Only include non-empty values (empty string means not yet populated)
				if strVal, ok := val.(string); ok && strVal == "" {
					continue
				}
				delta[stateKey] = val
				if a.DebugMode {
					slog.Debug("including raw_tool_output in state delta for persistence", "state_key", stateKey)
				}
			}
		}

		if len(delta) > 0 {
			if a.DebugMode {
				slog.Debug("emitting raw_tool_output state delta", "keys", getKeys(delta))
			}
			yield(&session.Event{
				Actions: session.EventActions{
					StateDelta: delta,
				},
			}, nil)
		}
	}

	// Handle user_message if defined
	// IMPORTANT: We need to emit this with BOTH text content AND StateDelta
	// The text content will be displayed, and we'll add a special marker in StateDelta
	// to tell console.go to print the "Agent:" prefix
	if len(node.UserMessage) > 0 {
		var textParts []string

		for _, msgPart := range node.UserMessage {
			// Try to resolve as state variable first
			if val, err := state.Get(msgPart); err == nil {
				textParts = append(textParts, fmt.Sprintf("%v", val))

				if a.DebugMode {
					slog.Debug("resolved user message part", "part", msgPart, "value", val)
				}
			}
		}

		if len(textParts) > 0 {
			if a.DebugMode {
				slog.Debug("emitting user_message event", "text", strings.Join(textParts, " "))
			}

			// Emit event with text content AND a special marker in StateDelta
			// The marker tells console.go this is a user_message that needs the "Agent:" prefix
			// Note: Field values are NOT included here - they are already in the output_model StateDelta event
			userMessageEvent := &session.Event{
				LLMResponse: model.LLMResponse{
					Content: &genai.Content{
						Parts: []*genai.Part{{Text: strings.Join(textParts, " ")}},
						Role:  "model",
					},
				},
				Actions: session.EventActions{
					StateDelta: map[string]any{
						"_user_message_display": true, // Special marker for console.go
					},
				},
			}

			if !yield(userMessageEvent, nil) {
				return false, nil
			}

			if a.DebugMode {
				slog.Debug("user_message event yielded successfully")
			}
		}
	}

	return true, nil
}
