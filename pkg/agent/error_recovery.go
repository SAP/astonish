package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// ErrorContext captures the context of an error for recovery analysis
type ErrorContext struct {
	NodeName       string         `json:"node_name"`
	NodeType       string         `json:"node_type"`                // "llm", "tool", etc.
	ErrorType      string         `json:"error_type"`               // "tool_error", "parse_error", "llm_error", etc.
	ErrorMessage   string         `json:"error_message"`            // The actual error message
	AttemptCount   int            `json:"attempt_count"`            // Current retry attempt (1-indexed)
	MaxRetries     int            `json:"max_retries"`              // Configured maximum retries
	PreviousErrors []string       `json:"previous_errors"`          // History of error messages
	ToolName       string         `json:"tool_name,omitempty"`      // If tool error
	ToolArgs       map[string]any `json:"tool_args,omitempty"`      // If tool error
	OriginalInput  map[string]any `json:"original_input,omitempty"` // Original node inputs
}

// RecoveryDecision represents the decision made by the error recovery system
type RecoveryDecision struct {
	ShouldRetry bool   `json:"should_retry"` // true = retry, false = abort
	Title       string `json:"title"`        // Short, clear summary of the error (max 100 chars)
	OneLiner    string `json:"one_liner"`    // Ultra-short summary for badges (max 60 chars)
	Reason      string `json:"reason"`       // Explanation of the decision
	Suggestion  string `json:"suggestion"`   // What to fix or try differently (if retry)
}

// ErrorRecoveryNode analyzes errors and decides whether to retry or abort
type ErrorRecoveryNode struct {
	LLM       model.LLM
	DebugMode bool
}

// NewErrorRecoveryNode creates a new error recovery analyzer
func NewErrorRecoveryNode(llm model.LLM, debugMode bool) *ErrorRecoveryNode {
	return &ErrorRecoveryNode{
		LLM:       llm,
		DebugMode: debugMode,
	}
}

// Decide analyzes an error and decides whether to retry or abort
func (e *ErrorRecoveryNode) Decide(ctx context.Context, errCtx ErrorContext) (*RecoveryDecision, error) {
	// Build the analysis prompt
	systemPrompt := e.buildSystemPrompt()
	userPrompt := e.buildUserPrompt(errCtx)

	if e.DebugMode {
		slog.Debug("error recovery analyzing error",
			"component", "error-recovery", "node", errCtx.NodeName, "nodeType", errCtx.NodeType,
			"attempt", errCtx.AttemptCount, "maxRetries", errCtx.MaxRetries, "error", errCtx.ErrorMessage)
	}

	// Create request - combine system prompt and user prompt
	combinedPrompt := fmt.Sprintf("%s\n\n%s", systemPrompt, userPrompt)

	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Parts: []*genai.Part{{Text: combinedPrompt}},
				Role:  "user",
			},
		},
	}

	// Call LLM using GenerateContent (streaming interface)
	var responseText string
	for resp, err := range auxiliaryLLM(ctx, e.LLM).GenerateContent(ctx, req, false) {
		if err != nil {
			if isBotModelContext(ctx) {
				return nil, fmt.Errorf("Bot error recovery unavailable: %w", err)
			}
			slog.Debug("error recovery LLM call failed", "component", "error-recovery", "error", err)
			// Fallback to simple heuristic
			return e.fallbackDecision(errCtx), nil
		}

		// Extract response text from each chunk
		if resp.Content != nil && len(resp.Content.Parts) > 0 {
			responseText += resp.Content.Parts[0].Text
		}
	}

	if e.DebugMode {
		slog.Debug("error recovery LLM response", "component", "error-recovery", "response", responseText)
	}

	// Parse decision
	decision, err := e.parseDecision(responseText)
	if err != nil {
		slog.Debug("error recovery failed to parse decision", "component", "error-recovery", "error", err)
		// Fallback to simple heuristic
		return e.fallbackDecision(errCtx), nil
	}

	if e.DebugMode {
		if decision.ShouldRetry {
			slog.Debug("error recovery decision: RETRY", "component", "error-recovery", "reason", decision.Reason)
		} else {
			slog.Debug("error recovery decision: ABORT", "component", "error-recovery", "reason", decision.Reason)
		}
	}

	return decision, nil
}

// buildSystemPrompt creates the system instruction for error analysis
func (e *ErrorRecoveryNode) buildSystemPrompt() string {
	return `You are an intelligent error recovery specialist for an AI agent system. Your job is to analyze errors and decide whether the system should:

1. **RETRY** - If the error is transient, recoverable, or can be fixed with a different approach
2. **ABORT** - If the error is permanent, unrecoverable, or indicates a fundamental problem

**Guidelines for RETRY (prefer RETRY when in doubt):**
- Network timeouts, rate limiting (429), temporary unavailability (503)
- Parsing errors where the LLM might produce better output on retry
- Invalid tool arguments or parameter errors - the AI CAN fix these on retry
- Tool execution errors - even if repeated, the AI can adjust approach
- Any error where the AI could potentially adjust its behavior
- When not at max retry attempts, default to RETRY

**Guidelines for ABORT (use sparingly, only for truly unrecoverable errors):**
- Authentication/Authorization failures (401, 403) - credentials won't change
- Resource not found (404) where the resource genuinely doesn't exist
- Invalid configuration or missing environment variables
- Max retry attempts exceeded
- User explicitly denied permission
- Errors about missing system dependencies (not installed software)

**IMPORTANT: Tool argument errors are ALWAYS retryable.** Even if the same error repeats, the AI can learn from each attempt and try different arguments. Only abort tool errors if they indicate a fundamental system problem (missing tool, auth failure).

**Response Format:**
You MUST respond with ONLY a JSON object in this exact format:
{
  "should_retry": true or false,
  "title": "Short, clear summary of the error (max 80 chars, no emoji)",
  "one_liner": "Ultra-short summary for UI badge (max 60 chars, no emoji)",
  "reason": "clear explanation of your decision (2-3 sentences)",
  "suggestion": "what to try differently (if retry) or how to fix (if abort)"
}

**Title Guidelines:**
- Be concise and specific (max 80 characters)
- State the actual problem, not just "error occurred"
- Use plain language, avoid technical jargon when possible
- Examples: "Pending review already exists", "Authentication required", "Resource not found"

**One-Liner Guidelines:**
- Even more concise than title (max 60 characters)
- Used in retry badges, must be extremely brief
- Examples: "Rate limit (429)", "Review exists", "Auth required"

Do not include any other text, markdown, or explanations outside the JSON object.`
}

// buildUserPrompt creates the user prompt with error context
func (e *ErrorRecoveryNode) buildUserPrompt(errCtx ErrorContext) string {
	var sb strings.Builder

	sb.WriteString("**Error Analysis Request**\n\n")
	sb.WriteString(fmt.Sprintf("**Node Information:**\n"))
	sb.WriteString(fmt.Sprintf("- Name: %s\n", errCtx.NodeName))
	sb.WriteString(fmt.Sprintf("- Type: %s\n", errCtx.NodeType))
	sb.WriteString(fmt.Sprintf("\n**Error Details:**\n"))
	sb.WriteString(fmt.Sprintf("- Current Attempt: %d of %d\n", errCtx.AttemptCount, errCtx.MaxRetries))
	sb.WriteString(fmt.Sprintf("- Error Type: %s\n", errCtx.ErrorType))
	sb.WriteString(fmt.Sprintf("- Error Message: %s\n", errCtx.ErrorMessage))

	if len(errCtx.PreviousErrors) > 0 {
		sb.WriteString(fmt.Sprintf("\n**Previous Errors:**\n"))
		for i, prevErr := range errCtx.PreviousErrors {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, prevErr))
		}
	}

	if errCtx.ToolName != "" {
		sb.WriteString(fmt.Sprintf("\n**Tool Information:**\n"))
		sb.WriteString(fmt.Sprintf("- Tool Name: %s\n", errCtx.ToolName))
		if len(errCtx.ToolArgs) > 0 {
			argsJSON, _ := json.MarshalIndent(errCtx.ToolArgs, "  ", "  ")
			sb.WriteString(fmt.Sprintf("- Tool Arguments:\n  %s\n", string(argsJSON)))
		}
	}

	sb.WriteString(fmt.Sprintf("\n**Question:** Should the system RETRY or ABORT?"))

	return sb.String()
}

// parseDecision extracts the decision from the LLM response
func (e *ErrorRecoveryNode) parseDecision(response string) (*RecoveryDecision, error) {
	// Clean the response (remove markdown code blocks if present)
	cleaned := strings.TrimSpace(response)

	// Remove markdown code blocks
	if strings.HasPrefix(cleaned, "```json") {
		cleaned = strings.TrimPrefix(cleaned, "```json")
		cleaned = strings.TrimSuffix(cleaned, "```")
		cleaned = strings.TrimSpace(cleaned)
	} else if strings.HasPrefix(cleaned, "```") {
		cleaned = strings.TrimPrefix(cleaned, "```")
		cleaned = strings.TrimSuffix(cleaned, "```")
		cleaned = strings.TrimSpace(cleaned)
	}

	// Find JSON object
	startIdx := strings.Index(cleaned, "{")
	endIdx := strings.LastIndex(cleaned, "}")

	if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
		return nil, fmt.Errorf("no valid JSON object found in response")
	}

	jsonStr := cleaned[startIdx : endIdx+1]

	var decision RecoveryDecision
	if err := json.Unmarshal([]byte(jsonStr), &decision); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return &decision, nil
}

// fallbackDecision provides a simple heuristic-based decision when LLM analysis fails
func (e *ErrorRecoveryNode) fallbackDecision(errCtx ErrorContext) *RecoveryDecision {
	// Simple heuristics for common error patterns
	errorLower := strings.ToLower(errCtx.ErrorMessage)

	// Check for non-recoverable errors
	nonRecoverablePatterns := map[string]string{
		"401":                   "Authentication Required",
		"403":                   "Access Forbidden",
		"unauthorized":          "Authentication Required",
		"forbidden":             "Access Forbidden",
		"404":                   "Resource Not Found",
		"not found":             "Resource Not Found",
		"invalid configuration": "Invalid Configuration",
		"authentication failed": "Authentication Failed",
	}

	for pattern, title := range nonRecoverablePatterns {
		if strings.Contains(errorLower, pattern) {
			oneLiner := title
			if len(oneLiner) > 60 {
				oneLiner = oneLiner[:57] + "..."
			}
			return &RecoveryDecision{
				ShouldRetry: false,
				Title:       title,
				OneLiner:    oneLiner,
				Reason:      fmt.Sprintf("Non-recoverable error detected: %s", pattern),
				Suggestion:  "Please check your configuration and ensure all required resources exist",
			}
		}
	}

	// Check for recoverable errors
	recoverablePatterns := map[string]string{
		"429":                 "Rate Limit Exceeded",
		"rate limit":          "Rate Limit Exceeded",
		"503":                 "Service Temporarily Unavailable",
		"service unavailable": "Service Temporarily Unavailable",
		"timeout":             "Request Timeout",
		"connection":          "Connection Error",
		"temporary":           "Temporary Error",
		"parse":               "Parsing Error",
		"parsing":             "Parsing Error",
	}

	for pattern, title := range recoverablePatterns {
		if strings.Contains(errorLower, pattern) {
			oneLiner := title
			if len(oneLiner) > 60 {
				oneLiner = oneLiner[:57] + "..."
			}
			return &RecoveryDecision{
				ShouldRetry: true,
				Title:       title,
				OneLiner:    oneLiner,
				Reason:      fmt.Sprintf("Transient error detected: %s", pattern),
				Suggestion:  "Retrying with the same parameters",
			}
		}
	}

	// Default: retry if not at max attempts, otherwise abort
	if errCtx.AttemptCount < errCtx.MaxRetries {
		return &RecoveryDecision{
			ShouldRetry: true,
			Title:       "Retry Attempt",
			OneLiner:    "Uncertain error",
			Reason:      "Error type uncertain, but retry budget available",
			Suggestion:  "Attempting retry with same parameters",
		}
	}

	return &RecoveryDecision{
		ShouldRetry: false,
		Title:       "Max Retries Exceeded",
		OneLiner:    "Max retries reached",
		Reason:      "Max retry attempts reached",
		Suggestion:  "Please review the error and adjust the node configuration",
	}
}
