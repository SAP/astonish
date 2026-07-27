package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/adk/session"
)

// minOutputForReflection is the minimum length of final output text for a
// zero-tool-call turn to qualify for reflection. Turns with no tool calls
// AND very short output (e.g., "OK", "Done") are trivial acknowledgments.
const minOutputForReflection = 50

const maxReflectionToolResultLen = 1200
const maxReflectionShellStreamLen = 900

// countToolCallsRecursive returns the total number of tool calls in the trace,
// including any sub-agent traces attached to delegate_tasks steps.
func countToolCallsRecursive(trace *ExecutionTrace) int {
	if trace == nil {
		return 0
	}
	trace.mu.Lock()
	steps := make([]TraceStep, len(trace.Steps))
	copy(steps, trace.Steps)
	trace.mu.Unlock()

	count := len(steps)
	for _, step := range steps {
		for _, child := range step.SubAgentTraces {
			count += countToolCallsRecursive(child)
		}
	}
	return count
}

// traceContainsMemorySave checks whether memory_save was called in the trace
// or in any sub-agent traces (recursively).
func traceContainsMemorySave(trace *ExecutionTrace) bool {
	if trace == nil {
		return false
	}
	trace.mu.Lock()
	steps := make([]TraceStep, len(trace.Steps))
	copy(steps, trace.Steps)
	trace.mu.Unlock()

	for _, step := range steps {
		if step.ToolName == "memory_save" {
			return true
		}
		for _, child := range step.SubAgentTraces {
			if traceContainsMemorySave(child) {
				return true
			}
		}
	}
	return false
}

// buildReflectionContext creates a rich context for the reflection LLM prompt,
// including the conversation exchange and tool execution trace with sub-agent
// details.
func buildReflectionContext(trace *ExecutionTrace, events session.Events) string {
	var sb strings.Builder

	// Section 1: Conversation context from session events
	if events != nil {
		userText, modelText := extractCurrentTurnConversation(events)
		if userText != "" || modelText != "" {
			sb.WriteString("## Conversation Context\n\n")
			if userText != "" {
				if len(userText) > 2000 {
					userText = userText[:1997] + "..."
				}
				sb.WriteString(fmt.Sprintf("**User:** %s\n\n", userText))
			}
			if modelText != "" {
				if len(modelText) > 3000 {
					modelText = modelText[:2997] + "..."
				}
				sb.WriteString(fmt.Sprintf("**Agent:** %s\n\n", modelText))
			}
		}
	}

	// Section 2: Tool execution trace
	totalCalls := countToolCallsRecursive(trace)
	sb.WriteString("## Tool Execution Trace\n\n")
	sb.WriteString(fmt.Sprintf("**User Request:** %s\n\n", trace.UserRequest))
	sb.WriteString(fmt.Sprintf("**Total Tool Calls:** %d\n\n", totalCalls))

	if len(trace.Steps) > 0 {
		sb.WriteString("### Steps:\n")
		writeTraceSteps(&sb, trace.Steps, 0)
	}

	// Section 3: Final output
	if trace.FinalOutput != "" {
		output := trace.FinalOutput
		if len(output) > 2000 {
			output = output[:1997] + "..."
		}
		sb.WriteString(fmt.Sprintf("\n### Final Response (truncated):\n%s\n", output))
	}

	return sb.String()
}

// writeTraceSteps writes trace steps to the string builder, recursing into
// sub-agent traces with indentation.
func writeTraceSteps(sb *strings.Builder, steps []TraceStep, depth int) {
	indent := strings.Repeat("  ", depth)
	for i, step := range steps {
		status := "OK"
		if !step.Success {
			status = fmt.Sprintf("FAILED: %s", step.Error)
		}

		// Include args summary (truncated)
		argsSummary := ""
		if step.ToolArgs != nil {
			argsBytes, _ := json.Marshal(step.ToolArgs)
			argsSummary = string(argsBytes)
			if len(argsSummary) > 200 {
				argsSummary = argsSummary[:197] + "..."
			}
		}

		sb.WriteString(fmt.Sprintf("%s%d. **%s** [%s] %s\n", indent, i+1, step.ToolName, status, argsSummary))
		if resultSummary := summarizeReflectionToolResult(step); resultSummary != "" {
			sb.WriteString(fmt.Sprintf("%s   Result: %s\n", indent, resultSummary))
		}

		// Recurse into sub-agent traces
		for _, child := range step.SubAgentTraces {
			if child == nil {
				continue
			}
			name := "sub-agent"
			if child.UserRequest != "" {
				name = child.UserRequest
				if len(name) > 80 {
					name = name[:77] + "..."
				}
			}
			sb.WriteString(fmt.Sprintf("%s   Sub-agent: %s\n", indent, name))

			child.mu.Lock()
			childSteps := make([]TraceStep, len(child.Steps))
			copy(childSteps, child.Steps)
			child.mu.Unlock()

			writeTraceSteps(sb, childSteps, depth+1)

			// Include sub-agent final output if available
			if child.FinalOutput != "" {
				output := child.FinalOutput
				if len(output) > 500 {
					output = output[:497] + "..."
				}
				sb.WriteString(fmt.Sprintf("%s   Sub-agent result: %s\n", indent, output))
			}
		}
	}
}

func summarizeReflectionToolResult(step TraceStep) string {
	if len(step.ToolResult) == 0 {
		return ""
	}

	if step.ToolName == "shell_command" {
		return summarizeReflectionShellResult(step.ToolResult)
	}

	resultBytes, err := json.Marshal(step.ToolResult)
	if err != nil {
		return ""
	}
	return truncateReflectionText(string(resultBytes), maxReflectionToolResultLen)
}

func summarizeReflectionShellResult(result map[string]any) string {
	summary := make(map[string]any, len(result))
	for k, v := range result {
		switch k {
		case "stdout", "stderr":
			if s, ok := v.(string); ok {
				summary[k] = truncateReflectionText(s, maxReflectionShellStreamLen)
			} else {
				summary[k] = v
			}
		default:
			summary[k] = v
		}
	}
	resultBytes, err := json.Marshal(summary)
	if err != nil {
		return ""
	}
	return truncateReflectionText(string(resultBytes), maxReflectionToolResultLen)
}

func truncateReflectionText(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// extractCurrentTurnConversation extracts the user message(s) and model text
// responses from the current turn (the most recent user message to end of session).
// Returns (userText, modelText).
func extractCurrentTurnConversation(events session.Events) (string, string) {
	if events == nil || events.Len() == 0 {
		return "", ""
	}

	n := events.Len()

	// Walk backwards to find the last user event
	lastUserIdx := -1
	for i := n - 1; i >= 0; i-- {
		ev := events.At(i)
		if ev.Author == "user" {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		return "", ""
	}

	// Collect user text from the user event
	var userParts []string
	userEvent := events.At(lastUserIdx)
	if userEvent.Content != nil {
		for _, part := range userEvent.Content.Parts {
			if part.Text != "" {
				userParts = append(userParts, part.Text)
			}
		}
	}

	// Walk forward from user event to collect model text responses.
	var modelParts []string
	for i := lastUserIdx + 1; i < n; i++ {
		ev := events.At(i)
		if ev.Content == nil {
			continue
		}
		if ev.Author == "user" {
			break
		}
		for _, part := range ev.Content.Parts {
			if part.Text != "" && !part.Thought && part.FunctionCall == nil && part.FunctionResponse == nil {
				modelParts = append(modelParts, part.Text)
			}
		}
	}

	return strings.Join(userParts, "\n"), strings.Join(modelParts, "\n")
}
