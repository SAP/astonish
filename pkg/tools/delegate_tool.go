package tools

import (
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/agent"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// subAgentManagerVar holds the sub-agent manager reference.
// Set by the launcher via SetSubAgentManager.
var subAgentManagerVar *agent.SubAgentManager

// SetSubAgentManager registers the sub-agent manager for the delegate_tasks tool.
func SetSubAgentManager(mgr *agent.SubAgentManager) {
	subAgentManagerVar = mgr
}

// GetSubAgentManager returns the sub-agent manager.
// Used by the fleet session API handlers to create fleet sessions.
func GetSubAgentManager() *agent.SubAgentManager {
	return subAgentManagerVar
}

// --- delegate_tasks tool ---

// SubTaskInput describes a single sub-task to delegate.
type SubTaskInput struct {
	Name         string   `json:"name" jsonschema:"Short identifier for this sub-agent (e.g., 'researcher', 'code-reviewer', 'api-tester'). Must be unique within the delegation call."`
	Task         string   `json:"task" jsonschema:"Clear description of what the sub-agent should accomplish. Be specific about the expected output."`
	Instructions string   `json:"instructions,omitempty" jsonschema:"Additional context or constraints for this sub-agent. Include relevant file paths, API details, or formatting requirements."`
	Tools        []string `json:"tools,omitempty" jsonschema:"Tool group names or individual tool names for this sub-agent (e.g., ['core'], ['browser'], ['core', 'web'], ['mcp:github']). If omitted, tools are auto-discovered based on the task description. Available groups are listed in the system prompt under Task Delegation."`
	PlanStep     string   `json:"plan_step,omitempty" jsonschema:"The plan step this task belongs to. Set this to the 'name' of the step from announce_plan that this task contributes to. Multiple tasks can share the same plan_step — the step completes only when ALL tasks with that plan_step finish. If omitted, progress tracking falls back to name-based matching."`
}

// DelegateTasksArgs is the input schema for the delegate_tasks tool.
type DelegateTasksArgs struct {
	Tasks []SubTaskInput `json:"tasks" jsonschema:"Array of sub-tasks to execute in parallel. Each task gets its own isolated session and tool context."`
}

// SubTaskResultItem holds the result of a single delegated sub-task.
type SubTaskResultItem struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	Result       string `json:"result,omitempty"`
	FullResultID string `json:"full_result_id,omitempty"` // ID for read_task_result if result was summarized
	ToolCalls    int    `json:"tool_calls"`
	Duration     string `json:"duration"`
	Error        string `json:"error,omitempty"`
}

// DelegateTasksResult is the output schema for the delegate_tasks tool.
type DelegateTasksResult struct {
	Status  string              `json:"status"`
	Results []SubTaskResultItem `json:"results"`
	Summary string              `json:"summary"`
}

func delegateTasks(ctx tool.Context, args DelegateTasksArgs) (DelegateTasksResult, error) {
	if subAgentManagerVar == nil {
		return DelegateTasksResult{
			Status:  "error",
			Summary: "Sub-agent system is not available",
		}, nil
	}

	if len(args.Tasks) == 0 {
		return DelegateTasksResult{
			Status:  "error",
			Summary: "No tasks provided",
		}, nil
	}

	if len(args.Tasks) > 10 {
		return DelegateTasksResult{
			Status:  "error",
			Summary: "Too many tasks (max 10 per delegation call)",
		}, nil
	}

	// Convert tool inputs to SubAgentTasks
	tasks := make([]agent.SubAgentTask, len(args.Tasks))
	for i, input := range args.Tasks {
		// Escape curly-brace patterns (e.g., %{http_code} from curl format strings)
		// to prevent ADK's InjectSessionState from misinterpreting them as state variables.
		safeTask := agent.EscapeCurlyPlaceholders(input.Task)
		safeInstructions := agent.EscapeCurlyPlaceholders(input.Instructions)

		// Capture the parent session ID for event routing — ensures sub-agent
		// events are forwarded only to the session that initiated the delegation.
		parentSessionID := ctx.SessionID()
		var onEvent func(event *adksession.Event)
		if subAgentManagerVar.EventForwarder != nil {
			onEvent = func(event *adksession.Event) {
				subAgentManagerVar.EventForwarder(parentSessionID, event)
			}
		}

		tasks[i] = agent.SubAgentTask{
			Name:         input.Name,
			Description:  safeTask,
			Instructions: safeInstructions,
			ToolFilter:   input.Tools,
			PlanStep:     input.PlanStep,
			ParentID:     parentSessionID,
			ParentDepth:  0,       // delegate_tasks creates top-level sub-agents
			OnEvent:      onEvent, // transparent streaming to UI (session-scoped)
		}
	}

	// Emit delegation_start progress event with the full task plan
	if subAgentManagerVar.SubTaskProgress != nil {
		taskInfos := make([]agent.SubTaskInfo, len(args.Tasks))
		for i, t := range args.Tasks {
			taskInfos[i] = agent.SubTaskInfo{
				Name:        t.Name,
				Description: t.Task,
				PlanStep:    t.PlanStep,
			}
		}
		subAgentManagerVar.SubTaskProgress(agent.SubTaskProgressEvent{
			Type:  "delegation_start",
			Tasks: taskInfos,
		})
	}

	// Execute all tasks via the SubAgentManager
	results := subAgentManagerVar.RunTasks(ctx, tasks)

	// Stash sub-agent execution traces for the parent's afterToolCallback.
	// The callback retrieves these via PopLastTraces() and attaches them to
	// the delegate_tasks TraceStep.SubAgentTraces field, giving the memory
	// reflection system visibility into what sub-agents actually did.
	var childTraces []*agent.ExecutionTrace
	for _, r := range results {
		if r.Trace != nil {
			childTraces = append(childTraces, r.Trace)
		}
	}
	if len(childTraces) > 0 {
		subAgentManagerVar.StashLastTraces(childTraces)
	}

	// Build response — summarize large results to prevent context explosion.
	// The full result is stored in TaskResultStore and can be retrieved via
	// read_task_result when the orchestrator needs complete data for synthesis.
	// Threshold is 20000 chars (~5k tokens): an extra read_task_result round-trip
	// costs ~30k tokens (full context resent), so inlining results under this
	// size is almost always cheaper than summarizing and round-tripping.
	const summarizeThreshold = 20000 // chars
	store := GetTaskResultStore()

	resultItems := make([]SubTaskResultItem, len(results))
	successCount := 0
	for i, r := range results {
		item := SubTaskResultItem{
			Name:      r.Name,
			Status:    r.Status,
			ToolCalls: r.ToolCalls,
			Duration:  r.Duration.Round(100 * 1e6).String(), // Round to 100ms
			Error:     r.Error,
		}

		if r.Status == "success" {
			successCount++
		}

		// For large results, store the full text and provide a summary
		if len(r.Result) > summarizeThreshold && r.Status == "success" {
			summary := summarizeResult(r.Result, r.Name)
			resultID := store.Store(r.Name, r.Result, summary)
			item.Result = summary
			item.FullResultID = resultID
		} else {
			item.Result = r.Result
		}

		resultItems[i] = item
	}

	// Build summary
	var summaryParts []string
	summaryParts = append(summaryParts, fmt.Sprintf("%d/%d tasks completed successfully", successCount, len(results)))
	for _, r := range results {
		if r.Status != "success" {
			summaryParts = append(summaryParts, fmt.Sprintf("  %s: %s — %s", r.Name, r.Status, r.Error))
		}
	}

	overallStatus := "success"
	if successCount == 0 {
		overallStatus = "error"
	} else if successCount < len(results) {
		overallStatus = "partial"
	}

	// Emit delegation_complete progress event
	if subAgentManagerVar.SubTaskProgress != nil {
		subAgentManagerVar.SubTaskProgress(agent.SubTaskProgressEvent{
			Type:   "delegation_complete",
			Status: overallStatus,
		})
	}

	return DelegateTasksResult{
		Status:  overallStatus,
		Results: resultItems,
		Summary: strings.Join(summaryParts, "\n"),
	}, nil
}

// NewDelegateTasksTool creates the delegate_tasks tool.
func NewDelegateTasksTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "delegate_tasks",
		Description: `Delegate tasks to parallel sub-agents with isolated sessions. Each sub-agent gets the tool groups you specify (e.g., ["core"], ["browser"], ["core", "web"]) or auto-discovers tools based on the task description if tools is omitted. Use this for specialized tasks like browser automation, web fetching, email, API calls, or any task requiring tools not on the main thread. Each sub-agent has read-only memory, a search_tools capability, and a 10-minute timeout (automatically retried once if making progress). Max 10 tasks per call.

When a plan is active (you called announce_plan), set the plan_step field on each task to link it to a plan step. Multiple tasks can share the same plan_step — the step is marked complete only when ALL tasks with that plan_step finish. This drives accurate progress tracking in the UI.`,
	}, delegateTasks)
}

// GetDelegateTasksTool returns the delegate_tasks tool (or nil if unavailable).
func GetDelegateTasksTool() (tool.Tool, error) {
	return NewDelegateTasksTool()
}

// summarizeResult creates a concise summary of a large sub-task result.
// This is a structural extraction — it preserves section headings, key findings,
// tables, and conclusions while removing verbose explanations and code blocks.
// For truly effective summarization, an LLM call would be ideal, but this
// approach avoids the latency and cost while providing a good-enough result
// for the orchestrator to decide whether to read the full output.
func summarizeResult(fullResult string, taskName string) string {
	lines := strings.Split(fullResult, "\n")

	var summary strings.Builder
	summary.WriteString(fmt.Sprintf("[Summary of %q — full output: %d chars. Use read_task_result with the full_result_id to get complete text.]\n\n", taskName, len(fullResult)))

	// Extract headings, key bullets, and the first few lines of each section
	const maxSummaryLines = 60
	summaryLines := 0
	inCodeBlock := false
	skipUntilHeading := false
	linesInSection := 0

	for _, line := range lines {
		if summaryLines >= maxSummaryLines {
			summary.WriteString("\n... (truncated — use read_task_result for full content)\n")
			break
		}

		trimmed := strings.TrimSpace(line)

		// Track code blocks and skip their contents
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			if inCodeBlock {
				continue // skip code block opener
			}
			continue // skip code block closer
		}
		if inCodeBlock {
			continue
		}

		// Always include headings
		if strings.HasPrefix(trimmed, "#") {
			skipUntilHeading = false
			linesInSection = 0
			summary.WriteString(line)
			summary.WriteString("\n")
			summaryLines++
			continue
		}

		if skipUntilHeading {
			continue
		}

		// Include table rows (they contain structured data)
		if strings.HasPrefix(trimmed, "|") {
			summary.WriteString(line)
			summary.WriteString("\n")
			summaryLines++
			continue
		}

		// Include key bullet points and findings
		if strings.HasPrefix(trimmed, "- **") || strings.HasPrefix(trimmed, "* **") ||
			strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			linesInSection++
			if linesInSection <= 6 { // first 6 bullets per section
				summary.WriteString(line)
				summary.WriteString("\n")
				summaryLines++
			}
			continue
		}

		// Include non-empty lines up to a limit per section
		if trimmed != "" {
			linesInSection++
			if linesInSection <= 4 { // first 4 prose lines per section
				summary.WriteString(line)
				summary.WriteString("\n")
				summaryLines++
			} else if linesInSection == 5 {
				skipUntilHeading = true
			}
		}
	}

	return summary.String()
}
