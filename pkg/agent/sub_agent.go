package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool"

	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/provider/llmerror"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// SubAgentConfig holds configuration for the sub-agent system.
type SubAgentConfig struct {
	MaxDepth      int           `yaml:"max_depth,omitempty" json:"max_depth,omitempty"`           // Max delegation nesting (default: 2)
	MaxConcurrent int           `yaml:"max_concurrent,omitempty" json:"max_concurrent,omitempty"` // Max parallel sub-agents (default: 5)
	TaskTimeout   time.Duration `yaml:"task_timeout,omitempty" json:"task_timeout,omitempty"`     // Per-task timeout (default: 10m)
	MaxRetries    int           `yaml:"max_retries,omitempty" json:"max_retries,omitempty"`       // Inner LLM retry attempts per task (default: 3)
}

// SubTaskProgressEvent represents a structured lifecycle event for sub-task
// plan visualization in the UI. These are higher-level than raw ADK events.
type SubTaskProgressEvent struct {
	Type     string `json:"type"`                // "delegation_start", "task_start", "task_complete", "task_failed", "task_retry", "task_tool_call", "task_tool_result", "task_text", "plan_announced", "plan_step_update"
	TaskName string `json:"task_name,omitempty"` // Name of the sub-task (matches SubAgentTask.Name)
	PlanStep string `json:"plan_step,omitempty"` // Plan step this task belongs to (for progress tracking)
	// Fields for delegation_start
	Tasks []SubTaskInfo `json:"tasks,omitempty"` // All tasks in the delegation (only for delegation_start)
	// Fields for task_complete / task_failed
	Status   string `json:"status,omitempty"`   // "success", "error", "timeout" (for task_complete/task_failed)
	Duration string `json:"duration,omitempty"` // Human-readable duration (for task_complete/task_failed)
	Error    string `json:"error,omitempty"`    // Error message (for task_failed)
	// Fields for task_tool_call / task_tool_result / task_text
	ToolName   string `json:"tool_name,omitempty"`   // Tool name (for task_tool_call / task_tool_result)
	ToolArgs   any    `json:"tool_args,omitempty"`   // Tool arguments (for task_tool_call)
	ToolResult any    `json:"tool_result,omitempty"` // Tool result (for task_tool_result)
	Text       string `json:"text,omitempty"`        // Text output (for task_text)
	// Fields for plan_announced
	PlanGoal  string         `json:"plan_goal,omitempty"`  // Plan title (for plan_announced)
	PlanSteps []PlanStepInfo `json:"plan_steps,omitempty"` // Plan steps (for plan_announced)
	// Fields for plan_step_update
	StepName   string `json:"step_name,omitempty"`   // Step name to update (for plan_step_update)
	StepStatus string `json:"step_status,omitempty"` // New step status: running, complete, failed (for plan_step_update)
}

// SubTaskInfo provides a summary of a task for the delegation_start event.
type SubTaskInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	PlanStep    string `json:"plan_step,omitempty"` // Which plan step this task belongs to
}

// PlanStepInfo describes a step in the high-level execution plan.
type PlanStepInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SubAgentTask describes a single sub-agent task to execute.
type SubAgentTask struct {
	Name         string   // Short identifier for the sub-agent (e.g. "researcher", "coder")
	Instructions string   // Task-specific instructions for the sub-agent
	Description  string   // Brief description of what to accomplish
	ToolFilter   []string // Specific tool names to include (empty = all allowed)
	Model        string   // Override model (empty = use parent's model)
	Provider     string   // Override provider (empty = use parent's provider)
	PlanStep     string   // Plan step this task belongs to (for progress tracking)

	// CustomPrompt, when true, uses Instructions directly as the LLM system prompt
	// instead of wrapping it with buildChildPrompt(). This is used by fleet agents
	// that build their own complete prompt (via fleet.BuildAgentPrompt).
	CustomPrompt bool

	// TimeoutOverride, when > 0, overrides the SubAgentManager's Config.TaskTimeout
	// for this specific task. Used by the fleet orchestrator which needs more time
	// than individual worker sub-agents.
	TimeoutOverride time.Duration

	// SessionState holds additional key-value pairs to inject into the child session's
	// initial state. This allows callers to pass metadata that tools running inside
	// the sub-agent can access via ctx.State().Get(key).
	SessionState map[string]any

	// OnEvent is an optional callback invoked for each event produced by the
	// sub-agent's runner. It enables real-time progress streaming from sub-agents
	// (e.g., fleet orchestrator progress). The callback must be safe to call
	// from the RunTask goroutine. If nil, events are consumed silently.
	OnEvent func(event *adksession.Event)

	// OverrideTools, when non-nil, replaces the tools that would normally be
	// selected by resolveTools(). This is used by fleet sessions to provide
	// sandbox-wrapped tool copies without mutating the global SubAgentManager
	// singleton. The caller is responsible for applying any tool filter before
	// setting this field.
	OverrideTools []tool.Tool

	// OverrideToolsets, when non-nil, replaces the MCP toolsets that would
	// normally come from resolveTools(). Used by fleet sessions to provide
	// sandbox-wired MCP toolset copies (with ContainerMCPTransport) that route
	// MCP server processes through the fleet's container.
	OverrideToolsets []tool.Toolset

	// Internal: set by SubAgentManager, not by callers
	ParentDepth int    // Current nesting depth
	ParentID    string // Parent session ID for linking
}

// TaskResult holds the outcome of a single sub-agent task execution.
type TaskResult struct {
	Name      string          // Matches SubAgentTask.Name
	Status    string          // "success", "error", "timeout"
	Result    string          // Final text output from the sub-agent
	Trace     *ExecutionTrace // Execution trace for the sub-agent's work
	ToolCalls int             // Number of tool calls made
	Duration  time.Duration   // Wall clock time for the task
	Error     string          // Error message if Status != "success"
}

// ToolGroup defines a named group of tools that sub-agents can request.
// The LLM references groups by name in the delegate_tasks tool's "tools" field
// (e.g., ["core", "browser", "mcp:github"]). Groups can contain regular tools,
// MCP toolsets, or both.
type ToolGroup struct {
	Name        string         // Group identifier (e.g., "core", "browser", "mcp:github")
	Description string         // Human-readable description for system prompt guidance
	Tools       []tool.Tool    // Regular tools in this group
	Toolsets    []tool.Toolset // MCP toolsets in this group
}

// SubAgentManager orchestrates the execution of sub-agent tasks.
type SubAgentManager struct {
	// Parent context
	LLM             model.LLM                      // Parent's LLM (used for children unless overridden)
	ToolGroups      map[string]*ToolGroup          // Named tool groups for sub-agent tool resolution
	FleetTools      []tool.Tool                    // Fleet-only tools (e.g., run_fleet_phase) not in main agent's tool list
	SessionService  adksession.Service             // Session persistence
	Compactor       *persistentsession.Compactor   // Context window compactor for sub-agents (nil = disabled)
	Redactor        *credentials.Redactor          // Redacts credential values from tool outputs (nil = disabled)
	CredentialStore credentials.CredentialResolver // Credential store for placeholder substitution (nil = disabled)
	PendingSecrets  *credentials.PendingVault      // Per-session vault for <<<SECRET_N>>> token resolution (nil = disabled)
	AppName         string                         // Application name for sessions
	UserID          string                         // User ID for sessions

	// Configuration
	Config SubAgentConfig

	// EventForwarder, when set, is called for each event produced by sub-agent
	// runners spawned via delegate_tasks. It enables transparent delegation:
	// events stream to the UI in real-time while the main LLM only receives
	// a compact summary. Set by the launcher to ChatAgent.ForwardSubTaskEvent.
	// Thread-safe: may be called from multiple sub-agent goroutines.
	EventForwarder func(event *adksession.Event)

	// SubTaskProgress, when set, is called for structured sub-task lifecycle
	// events (task_start, task_complete, task_failed) and tagged sub-agent
	// activity (task_tool_call, task_tool_result, task_text). This enables
	// task plan visualization in the UI. Set by the launcher to
	// ChatAgent.SubTaskProgressCallback. Thread-safe.
	SubTaskProgress func(event SubTaskProgressEvent)

	// OnChildSession, when set, is called after a sub-agent session is created
	// but before the sub-agent starts running. It receives the parent and child
	// session IDs. Used to alias the child session to the parent's sandbox
	// container so sub-agents share the same container instead of creating new
	// ones. Set by the launcher to NodeClientPool.Alias.
	OnChildSession func(parentSessionID, childSessionID string)

	// FileArtifactCapture, when set, is called when a sub-agent writes or
	// edits a file via write_file/edit_file tool calls. This propagates file
	// artifacts from sub-agents to the parent ChatAgent so they can be
	// delivered to the user (e.g., as Telegram document attachments).
	// Set by the launcher to ChatAgent.CaptureFileArtifact. Thread-safe.
	FileArtifactCapture func(path, toolName string)

	// Tool discovery: ToolIndex enables sub-agents to auto-discover which tools
	// they need based on the task description. When a sub-agent is created with
	// an empty ToolFilter, the index is queried to find relevant tool groups.
	ToolIndex *ToolIndex

	// SearchToolsFactory creates a child-scoped search_tools tool instance.
	// Each sub-agent gets its own instance whose onResults callback feeds into
	// the child's dynamic tool injection pipeline (not the parent's).
	// Set by the launcher; nil = search_tools not available to sub-agents.
	SearchToolsFactory func(onResults func([]string)) (tool.Tool, error)

	// SkillLookupTool is injected into every sub-agent so it can load
	// skill content on demand when it encounters a matching task (e.g.,
	// git/github skills for repository operations).
	SkillLookupTool tool.Tool

	// SkillIndex is the lightweight skill listing (names + descriptions)
	// injected into sub-agent system prompts so they know which skills
	// exist and can call skill_lookup to load them.
	SkillIndex string

	// WebSearchToolName is the configured web search tool (e.g., "tavily_search").
	// Injected into sub-agent prompts so they prefer dedicated search over web_fetch.
	WebSearchToolName string

	// WebExtractToolName is the configured web extract tool (e.g., "tavily_extract").
	// Injected into sub-agent prompts so they use it as fallback when web_fetch fails.
	WebExtractToolName string

	// SandboxEnabled indicates whether commands run inside an isolated sandbox.
	// When true, sub-agent prompts include workspace and recovery guidance.
	SandboxEnabled bool

	// SandboxWorkspaceDir is the persistent workspace directory inside the sandbox.
	// Sub-agents are instructed to use this for all work (not /tmp).
	SandboxWorkspaceDir string

	// MCPGroupResolver is a fallback resolver for MCP tool groups that are not
	// found in the ToolGroups map at resolution time. This handles the race
	// condition where async MCP tool discovery completes after the chat agent
	// was initialized (ToolGroups is built at init time from cached_tools, but
	// async discovery may populate cached_tools later). When set, resolveTools
	// calls this function for any "mcp:<server>" name not in ToolGroups, and
	// if it returns a non-nil ToolGroup, that group is injected into ToolGroups
	// for the remainder of this session.
	MCPGroupResolver func(ctx context.Context, serverName string) *ToolGroup

	// Internal
	sem          chan struct{}     // concurrency semaphore
	lastTracesMu sync.Mutex        // protects lastTraces
	lastTraces   []*ExecutionTrace // child traces from the most recent RunTasks call
}

// excludedChildTools are tools that sub-agents must NOT have access to.
var excludedChildTools = map[string]bool{
	"memory_save":       true, // Children can't write memory
	"memory_delete":     true, // Children can't delete memory
	"delegate_tasks":    true, // Prevent recursive delegation
	"schedule_job":      true, // Children can't schedule jobs
	"save_credential":   true, // Children can't modify credentials
	"remove_credential": true, // Children can't remove credentials
}

// IsExcludedChildTool returns true if the named tool is in the exclusion list.
// Used by sandbox wrapping to replicate the same filtering as resolveTools().
func IsExcludedChildTool(name string) bool {
	return excludedChildTools[name]
}

// NewSubAgentManager creates a new SubAgentManager with the given configuration.
func NewSubAgentManager(cfg SubAgentConfig) *SubAgentManager {
	if cfg.MaxDepth <= 0 {
		cfg.MaxDepth = 2
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 5
	}
	if cfg.TaskTimeout <= 0 {
		cfg.TaskTimeout = 10 * time.Minute
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}

	sem := make(chan struct{}, cfg.MaxConcurrent)
	return &SubAgentManager{
		Config: cfg,
		sem:    sem,
	}
}

// StashLastTraces stores sub-agent traces from the most recent RunTasks call.
// The afterToolCallback retrieves these via PopLastTraces to attach them to
// the parent trace's delegate_tasks step. Thread-safe; safe to call from any
// goroutine, though in practice ADK processes tool calls sequentially.
func (m *SubAgentManager) StashLastTraces(traces []*ExecutionTrace) {
	m.lastTracesMu.Lock()
	m.lastTraces = traces
	m.lastTracesMu.Unlock()
}

// PopLastTraces retrieves and clears the stashed sub-agent traces.
// Returns nil if no traces were stashed.
func (m *SubAgentManager) PopLastTraces() []*ExecutionTrace {
	m.lastTracesMu.Lock()
	traces := m.lastTraces
	m.lastTraces = nil
	m.lastTracesMu.Unlock()
	return traces
}

// RunTasks executes multiple sub-agent tasks concurrently and returns results.
// Tasks are fan-out with a semaphore controlling concurrency.
// Failed tasks that were making progress are automatically retried once with
// a fresh timeout and a continuation prompt carrying forward partial context.
// This method blocks until all tasks complete (or timeout).
func (m *SubAgentManager) RunTasks(ctx context.Context, tasks []SubAgentTask) []TaskResult {
	results := make([]TaskResult, len(tasks))
	var wg sync.WaitGroup

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t SubAgentTask) {
			defer wg.Done()

			// Acquire semaphore
			select {
			case m.sem <- struct{}{}:
				defer func() { <-m.sem }()
			case <-ctx.Done():
				results[idx] = TaskResult{
					Name:   t.Name,
					Status: "timeout",
					Error:  "context cancelled before task started",
				}
				return
			}

			result := m.RunTask(ctx, t)

			// Auto-retry: if the task failed with a retryable error and was
			// making progress (had tool calls or partial output), retry once
			// with a fresh timeout and a continuation prompt.
			if isRetryableFailure(result) && hasProgress(result) {
				// Emit task_retry event so the UI knows
				if m.SubTaskProgress != nil {
					m.SubTaskProgress(SubTaskProgressEvent{
						Type:     "task_retry",
						TaskName: t.Name,
						PlanStep: t.PlanStep,
						Error:    result.Error,
					})
				}

				slog.Info("retrying failed sub-task",
					"task", t.Name,
					"status", result.Status,
					"error", result.Error,
					"tool_calls", result.ToolCalls,
					"duration", result.Duration,
				)

				// Build continuation prompt with partial context from first attempt
				retryTask := t
				retryTask.Description = buildRetryPrompt(t.Description, result)
				result = m.RunTask(ctx, retryTask)
			}

			results[idx] = result
		}(i, task)
	}

	wg.Wait()
	return results
}

// isRetryableFailure returns true if the task result indicates a transient
// failure that is worth retrying (timeout, API errors, rate limits).
func isRetryableFailure(r TaskResult) bool {
	if r.Status == "timeout" {
		return true
	}
	if r.Status != "error" {
		return false
	}
	errLower := strings.ToLower(r.Error)
	transientPatterns := []string{
		"context deadline exceeded",
		"connection refused",
		"connection reset",
		"i/o timeout",
		"server error",
		"bad gateway",
		"service unavailable",
		"429",
		"502",
		"503",
		"504",
	}
	for _, pattern := range transientPatterns {
		if strings.Contains(errLower, pattern) {
			return true
		}
	}
	return false
}

// hasProgress returns true if the task made meaningful progress before failing
// (had tool calls or produced partial output).
func hasProgress(r TaskResult) bool {
	return r.ToolCalls > 0 || len(r.Result) > 0
}

// isRawContextDeadlineExceeded checks if an error is a context deadline exceeded
// that was NOT wrapped in an *llmerror.LLMError. This happens when the HTTP client
// hits the provider's gateway timeout (e.g., SAP AI Core 5-minute limit) — the
// error surfaces as a raw "Post ...: context deadline exceeded" without being
// classified by the provider layer. We treat it as retryable because the task's
// own context (taskCtx) is still valid — it's the per-request HTTP context that
// expired, not the overall task deadline.
func isRawContextDeadlineExceeded(err error) bool {
	if err == nil {
		return false
	}
	// If llmerror already classified it, let the caller use IsRetryable instead
	if llmerror.IsRetryable(err) {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "context deadline exceeded") ||
		strings.Contains(errStr, "timeout awaiting response headers")
}

// buildRetryPrompt creates a continuation prompt that includes partial context
// from the first attempt so the retried sub-agent can pick up where it left off.
func buildRetryPrompt(originalDescription string, firstAttempt TaskResult) string {
	var sb strings.Builder
	sb.WriteString("CONTINUATION: Your previous attempt was interrupted (")
	sb.WriteString(firstAttempt.Status)
	if firstAttempt.Error != "" {
		sb.WriteString(": ")
		sb.WriteString(firstAttempt.Error)
	}
	sb.WriteString(").\n\n")

	// Include partial output from first attempt (truncated to avoid bloating the prompt)
	if firstAttempt.Result != "" {
		partial := firstAttempt.Result
		const maxPartialLen = 2000
		if len(partial) > maxPartialLen {
			partial = partial[:maxPartialLen] + "\n... (truncated)"
		}
		sb.WriteString("Here is what you accomplished before the interruption:\n")
		sb.WriteString(partial)
		sb.WriteString("\n\n")
	}

	sb.WriteString("Continue from where you left off. Do NOT repeat work already done above. Original task:\n")
	sb.WriteString(originalDescription)
	return sb.String()
}

// RunTask executes a single sub-agent task synchronously.
// It creates a child session, builds a filtered ChatAgent, runs the full
// agent loop, collects the output and trace, then returns the result.
func (m *SubAgentManager) RunTask(ctx context.Context, task SubAgentTask) TaskResult {
	start := time.Now()

	// Emit task_start progress event
	if m.SubTaskProgress != nil {
		m.SubTaskProgress(SubTaskProgressEvent{
			Type:     "task_start",
			TaskName: task.Name,
			PlanStep: task.PlanStep,
		})
	}

	// Emit task_complete or task_failed on every exit path
	var result TaskResult
	defer func() {
		if m.SubTaskProgress != nil {
			evtType := "task_complete"
			if result.Status != "success" {
				evtType = "task_failed"
			}
			m.SubTaskProgress(SubTaskProgressEvent{
				Type:     evtType,
				TaskName: task.Name,
				PlanStep: task.PlanStep,
				Status:   result.Status,
				Duration: result.Duration.Round(100 * 1e6).String(),
				Error:    result.Error,
			})
		}
	}()

	// Apply task timeout (use override if set)
	timeout := m.Config.TaskTimeout
	if task.TimeoutOverride > 0 {
		timeout = task.TimeoutOverride
	}
	taskCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Depth check
	if task.ParentDepth >= m.Config.MaxDepth {
		result = TaskResult{
			Name:     task.Name,
			Status:   "error",
			Error:    fmt.Sprintf("max delegation depth %d reached", m.Config.MaxDepth),
			Duration: time.Since(start),
		}
		return result
	}

	// Resolve tools for the child from requested groups/names
	childTools := task.OverrideTools
	var childToolsets []tool.Toolset
	if task.OverrideToolsets != nil {
		childToolsets = task.OverrideToolsets
	}
	var resolveWarnings []string
	if childTools == nil {
		// If the parent specified tool groups, use those directly.
		// If the parent specified nothing (empty ToolFilter), auto-discover
		// tools using the ToolIndex based on the task description.
		toolFilter := task.ToolFilter
		if len(toolFilter) == 0 && m.ToolIndex != nil && task.Description != "" {
			discoveredGroups := m.ToolIndex.SearchGroupsHybrid(
				context.Background(), task.Description, 12, 0.005,
			)
			if len(discoveredGroups) > 0 {
				toolFilter = discoveredGroups
			}
		}
		childTools, childToolsets, resolveWarnings = m.resolveTools(taskCtx, toolFilter)
	}

	// --- Dynamic tool injection for sub-agents ---
	// Mirrors the main ChatAgent's DynamicToolInjectionCallback: tools discovered
	// via (1) automatic hybrid search on the task description and (2) explicit
	// search_tools calls mid-execution are injected into the child's LLM requests.
	var childSearchToolsResults []string
	var childSearchToolsMu sync.Mutex
	var childDynamicMatches []ToolMatch

	// Source 1: Automatic hybrid search on the task description.
	// This is the equivalent of chat_agent_run.go's per-turn tool search.
	if m.ToolIndex != nil && task.Description != "" {
		matches, err := m.ToolIndex.SearchHybrid(
			context.Background(), task.Description, 8, 0.005,
		)
		if err == nil && len(matches) > 0 {
			// Filter out MCP tools the user's team doesn't have access to
			matches = FilterAccessibleToolMatches(taskCtx, matches)
			childDynamicMatches = matches
		}
	}

	// Source 2: Create a child-scoped search_tools instance whose onResults
	// callback feeds into this child's injection pipeline (not the parent's).
	if m.SearchToolsFactory != nil {
		childSearchTool, stErr := m.SearchToolsFactory(func(names []string) {
			childSearchToolsMu.Lock()
			childSearchToolsResults = append(childSearchToolsResults, names...)
			childSearchToolsMu.Unlock()
		})
		if stErr == nil {
			childTools = append(childTools, childSearchTool)
		} else {
			slog.Warn("failed to create child search_tools", "task", task.Name, "error", stErr)
		}
	}

	// Inject skill_lookup into every sub-agent so it can load skill content
	// on demand (e.g., git/github skills for repository operations).
	if m.SkillLookupTool != nil {
		childTools = append(childTools, m.SkillLookupTool)
	}

	// If tool resolution produced warnings AND resolved zero tools, fail early
	// with a clear message so the calling LLM can self-correct.
	if len(resolveWarnings) > 0 && len(childTools) == 0 && len(childToolsets) == 0 {
		result = TaskResult{
			Name:     task.Name,
			Status:   "error",
			Error:    strings.Join(resolveWarnings, "; "),
			Duration: time.Since(start),
		}
		return result
	}

	// Build child system prompt: use custom prompt if set, otherwise build default
	var childPrompt string
	if task.CustomPrompt && task.Instructions != "" {
		childPrompt = task.Instructions
	} else {
		childPrompt = m.buildChildPrompt(taskCtx, task)
	}

	// Append dynamically discovered tools to the prompt so the LLM knows
	// what additional tools are available beyond its static set.
	if len(childDynamicMatches) > 0 {
		relevantTools := FormatToolMatchesForPrompt(childDynamicMatches)
		if relevantTools != "" {
			childPrompt += "\n## Dynamically Available Tools\nThese tools have been auto-discovered based on your task and are available for you to call directly:\n" + relevantTools
		}
	}

	// Create child session linked to parent.
	// Prefer the per-request session service from context (e.g., pgstore in
	// platform mode) over the factory-time default (m.SessionService).
	sessionSvc := m.SessionService
	if ctxSvc := store.SessionServiceFromContext(ctx); ctxSvc != nil {
		sessionSvc = ctxSvc
	}

	// Prefer the per-request user ID from context (e.g., platform user UUID)
	// over the factory-time default (m.UserID = "console_user"). The pgstore
	// user_id column is UUID-typed and rejects non-UUID strings.
	userID := m.UserID
	if ctxUID := store.UserIDFromContext(ctx); ctxUID != "" {
		userID = ctxUID
	}

	childSessionID := uuid.NewString()
	createState := map[string]any{}
	if task.ParentID != "" {
		createState[persistentsession.StateKeyParentID] = task.ParentID
	}
	// Inject caller-provided session state
	for k, v := range task.SessionState {
		createState[k] = v
	}

	_, err := sessionSvc.Create(taskCtx, &adksession.CreateRequest{
		AppName:   m.AppName,
		UserID:    userID,
		SessionID: childSessionID,
		State:     createState,
	})
	if err != nil {
		result = TaskResult{
			Name:     task.Name,
			Status:   "error",
			Error:    fmt.Sprintf("failed to create child session: %v", err),
			Duration: time.Since(start),
		}
		return result
	}

	// Persist the task name as the session title so fleet reconstruction
	// can derive phase/agent info from titles like "fleet-<fleet>-<phase>".
	if titleSetter, ok := sessionSvc.(interface {
		SetSessionTitle(context.Context, string, string) error
	}); ok {
		if err := titleSetter.SetSessionTitle(taskCtx, childSessionID, task.Name); err != nil {
			slog.Warn("failed to set sub-agent session title", "session_id", childSessionID, "title", task.Name, "error", err)
		}
	}

	// Alias the child session to the parent's sandbox container so sub-agents
	// share the same container instead of creating new ones.
	if m.OnChildSession != nil && task.ParentID != "" {
		m.OnChildSession(task.ParentID, childSessionID)
	}

	// Wire context compaction for sub-agents to prevent exceeding the context window
	// during long multi-step tool work (e.g., fleet agents reading/writing many files).
	var beforeModelCallbacks []llmagent.BeforeModelCallback

	// Truncate oversized tool responses before they reach the model. This prevents
	// a single large response (e.g., file_tree on /) from causing a 400 Bad Request.
	// Must run BEFORE compaction so the compactor sees reasonable-sized content.
	beforeModelCallbacks = append(beforeModelCallbacks, TruncateToolResponsesCallback())

	// Dynamically inject relevant tools into each child LLM request.
	// Mirrors the main ChatAgent's DynamicToolInjectionCallback: tools from
	// automatic hybrid search (childDynamicMatches) and explicit search_tools
	// discoveries (childSearchToolsResults) are injected into every LLM call.
	if m.ToolIndex != nil {
		toolIndex := m.ToolIndex // capture for closure
		beforeModelCallbacks = append(beforeModelCallbacks, func(cbCtx adkagent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
			toolsToInject := make(map[string]bool)

			// Source 1: automatic hybrid search matches on task description
			for _, match := range childDynamicMatches {
				if !match.IsMainTool {
					toolsToInject[match.ToolName] = true
				}
			}

			// Source 2: search_tools explicit discoveries (accumulated intra-execution)
			childSearchToolsMu.Lock()
			for _, name := range childSearchToolsResults {
				toolsToInject[name] = true
			}
			childSearchToolsMu.Unlock()

			if len(toolsToInject) == 0 {
				return nil, nil
			}

			for toolName := range toolsToInject {
				if _, exists := req.Tools[toolName]; exists {
					continue // already registered
				}
				// Respect the sub-agent exclusion list
				if excludedChildTools[toolName] {
					continue
				}
				entry := toolIndex.GetToolEntry(toolName)
				if entry == nil || entry.Tool == nil {
					continue
				}
				// MCP tool access control: skip tools from inaccessible servers
				if serverName, isMCP := mcpServerNameFromGroup(entry.GroupName); isMCP {
					if !isMCPServerAccessible(cbCtx, serverName) {
						continue
					}
				}
				packToolIntoRequest(req, entry.Tool)
			}

			return nil, nil
		})
	}

	if m.Compactor != nil {
		beforeModelCallbacks = append(beforeModelCallbacks, m.Compactor.BeforeModelCallback())
	}

	// Per-call restore functions keyed by FunctionCallID so parallel
	// tool calls don't clobber each other's restore closures.
	var restoreFuncs sync.Map // map[string]func()

	// Wire credential redaction so sub-agent tool outputs don't leak secrets
	// into the session transcript. resolve_credential now returns placeholders,
	// so no exemption is needed. Also restores credential placeholders in the
	// args map after tool execution.
	var afterToolCallbacks []llmagent.AfterToolCallback
	if m.Redactor != nil {
		redactor := m.Redactor
		afterToolCallbacks = append(afterToolCallbacks, func(ctx tool.Context, t tool.Tool, input, output map[string]any, err error) (map[string]any, error) {
			if fn, ok := restoreFuncs.LoadAndDelete(ctx.FunctionCallID()); ok {
				fn.(func())()
			}
			if output != nil {
				return redactor.RedactMap(output), err
			}
			return output, err
		})
	} else {
		// Even without a redactor, we need to restore credential placeholders.
		afterToolCallbacks = append(afterToolCallbacks, func(ctx tool.Context, t tool.Tool, input, output map[string]any, err error) (map[string]any, error) {
			if fn, ok := restoreFuncs.LoadAndDelete(ctx.FunctionCallID()); ok {
				fn.(func())()
			}
			return output, err
		})
	}

	// Capture file artifacts from sub-agent write_file/edit_file tool calls.
	// This propagates file artifacts to the parent ChatAgent so they can be
	// delivered to the user via channels (e.g., as Telegram documents).
	if m.FileArtifactCapture != nil {
		capture := m.FileArtifactCapture
		afterToolCallbacks = append(afterToolCallbacks, func(ctx tool.Context, t tool.Tool, input, output map[string]any, err error) (map[string]any, error) {
			if err != nil {
				return output, err
			}
			switch t.Name() {
			case "write_file":
				if path, ok := input["file_path"].(string); ok && path != "" {
					capture(path, t.Name())
				}
			case "edit_file":
				if path, ok := input["path"].(string); ok && path != "" {
					capture(path, t.Name())
				}
			case "browser_stop_recording":
				if path, ok := output["path"].(string); ok && path != "" {
					capture(path, t.Name())
				}
			case "run_drill":
				captureRunDrillArtifacts(capture, output)
			}
			return output, err
		})
	}

	// Wire credential placeholder substitution so sub-agents can use
	// {{CREDENTIAL:...}} tokens in tool args.
	var beforeToolCallbacks []llmagent.BeforeToolCallback
	{
		agentResolver := m.CredentialStore // may be nil if file-based store failed
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
				return nil, nil // no credential store available at all
			}

			// Use shell-safe env-var injection for shell_command tools.
			var shellFields []string
			if t.Name() == "shell_command" || t.Name() == "process_write" {
				shellFields = []string{"command"}
			}
			credRestore := credentials.SubstituteAndRestore(args, resolver, shellFields...)
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
	if m.PendingSecrets != nil {
		vault := m.PendingSecrets
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

	// Create child LLM agent via ADK
	childAgent, err := llmagent.New(llmagent.Config{
		Name:                 task.Name,
		Model:                m.LLM,
		Instruction:          childPrompt,
		Tools:                childTools,
		Toolsets:             childToolsets,
		BeforeToolCallbacks:  beforeToolCallbacks,
		BeforeModelCallbacks: beforeModelCallbacks,
		AfterToolCallbacks:   afterToolCallbacks,
		// Same auto-inject-on-miss path as ChatAgent: known-but-unloaded tools
		// are registered into childSearchToolsResults for the next LLM round.
		OnToolErrorCallbacks: []llmagent.OnToolErrorCallback{
			autoInjectMissingToolCallback(m.ToolIndex, func(names []string) {
				childSearchToolsMu.Lock()
				childSearchToolsResults = append(childSearchToolsResults, names...)
				childSearchToolsMu.Unlock()
			}, excludedChildTools),
		},
	})
	if err != nil {
		result = TaskResult{
			Name:     task.Name,
			Status:   "error",
			Error:    fmt.Sprintf("failed to create child agent: %v", err),
			Duration: time.Since(start),
		}
		return result
	}

	// Create runner
	r, err := runner.New(runner.Config{
		AppName:        m.AppName,
		Agent:          childAgent,
		SessionService: sessionSvc,
	})
	if err != nil {
		result = TaskResult{
			Name:     task.Name,
			Status:   "error",
			Error:    fmt.Sprintf("failed to create runner: %v", err),
			Duration: time.Since(start),
		}
		return result
	}

	// Build user message from task description (with absolute timestamp for
	// temporal context; see NewTimestampedUserContent for cache-stability rationale).
	userMsg := NewTimestampedUserContent(task.Description)

	// Execute the agent and collect results, with inner retry for transient
	// LLM errors (429, 502, 503, 504, gateway timeout). This mirrors the main
	// chat agent's retry loop in chat_agent_run.go — on a retryable error the
	// runner is re-invoked on the SAME session so the LLM preserves full
	// conversation history (unlike the outer task-level retry in RunTasks which
	// creates a fresh session with a continuation prompt).
	trace := NewExecutionTrace(task.Description)
	var outputParts []string
	var toolCallCount int
	maxRetries := m.Config.MaxRetries

	for attempt := range maxRetries {
		retried := false
		for event, runErr := range r.Run(taskCtx, userID, childSessionID, userMsg, adkagent.RunConfig{}) {
			if runErr != nil {
				// Check for retryable LLM errors (rate limit, server overload, timeout)
				if llmerror.IsRetryable(runErr) && attempt < maxRetries-1 {
					wait := retryBackoff(attempt, runErr)
					slog.Info("sub-agent retrying transient LLM error",
						"task", task.Name,
						"attempt", attempt+1,
						"max_retries", maxRetries,
						"error", runErr,
						"wait", wait,
						"tool_calls_so_far", toolCallCount,
					)
					select {
					case <-time.After(wait):
					case <-taskCtx.Done():
						// Context expired during backoff wait — fall through to timeout handling below
					}
					retried = true
					break // break inner for-range → continue outer retry loop
				}

				// Also treat raw "context deadline exceeded" from HTTP client as retryable
				// (SAP AI Core gateway timeouts surface as non-wrapped context errors)
				if isRawContextDeadlineExceeded(runErr) && attempt < maxRetries-1 {
					wait := retryBackoff(attempt, runErr)
					slog.Info("sub-agent retrying context deadline exceeded",
						"task", task.Name,
						"attempt", attempt+1,
						"max_retries", maxRetries,
						"error", runErr,
						"wait", wait,
						"tool_calls_so_far", toolCallCount,
					)
					select {
					case <-time.After(wait):
					case <-taskCtx.Done():
					}
					retried = true
					break
				}

				// Non-retryable error or retries exhausted
				trace.Finalize()
				result = TaskResult{
					Name:      task.Name,
					Status:    "error",
					Result:    strings.Join(outputParts, ""),
					Error:     fmt.Sprintf("agent run error: %v", runErr),
					Trace:     trace,
					ToolCalls: toolCallCount,
					Duration:  time.Since(start),
				}
				return result
			}

			if event == nil {
				continue
			}

			// Forward event to callback for real-time progress streaming
			if task.OnEvent != nil {
				task.OnEvent(event)
			}

			// Collect text output (skip thought/reasoning parts — these are
			// internal chain-of-thought and should not appear in the result).
			if event.LLMResponse.Content != nil {
				for _, part := range event.LLMResponse.Content.Parts {
					if part.Text != "" && !part.Thought {
						outputParts = append(outputParts, part.Text)
						// Emit task_text progress event
						if m.SubTaskProgress != nil {
							m.SubTaskProgress(SubTaskProgressEvent{
								Type:     "task_text",
								TaskName: task.Name,
								Text:     part.Text,
							})
						}
					}
					// Record tool calls in trace
					if part.FunctionCall != nil {
						toolCallCount++
						args := make(map[string]any)
						if part.FunctionCall.Args != nil {
							for k, v := range part.FunctionCall.Args {
								args[k] = v
							}
						}
						trace.RecordStep(part.FunctionCall.Name, args, nil, nil)
						// Emit task_tool_call progress event
						if m.SubTaskProgress != nil {
							m.SubTaskProgress(SubTaskProgressEvent{
								Type:     "task_tool_call",
								TaskName: task.Name,
								ToolName: part.FunctionCall.Name,
								ToolArgs: args,
							})
						}
					}
					// Record tool results in trace
					if part.FunctionResponse != nil {
						// Update the last trace step with the result
						trace.mu.Lock()
						if len(trace.Steps) > 0 {
							lastStep := &trace.Steps[len(trace.Steps)-1]
							if lastStep.ToolName == part.FunctionResponse.Name {
								lastStep.ToolResult = part.FunctionResponse.Response
								lastStep.Success = true
							}
						}
						trace.mu.Unlock()
						// Emit task_tool_result progress event
						if m.SubTaskProgress != nil {
							m.SubTaskProgress(SubTaskProgressEvent{
								Type:       "task_tool_result",
								TaskName:   task.Name,
								ToolName:   part.FunctionResponse.Name,
								ToolResult: part.FunctionResponse.Response,
							})
						}
					}
				}
			}
		}
		if !retried {
			break // completed successfully or hit non-retryable error (already returned)
		}
	}

	trace.Finalize()
	finalOutput := strings.Join(outputParts, "")
	trace.AppendOutput(finalOutput)

	// Prepend any tool resolution warnings so the calling LLM sees them
	if len(resolveWarnings) > 0 {
		finalOutput = strings.Join(resolveWarnings, "\n") + "\n\n" + finalOutput
	}

	// Check if context was cancelled (timeout)
	status := "success"
	errMsg := ""
	if taskCtx.Err() != nil {
		status = "timeout"
		errMsg = "task timed out"
	}

	result = TaskResult{
		Name:      task.Name,
		Status:    status,
		Result:    finalOutput,
		Trace:     trace,
		ToolCalls: toolCallCount,
		Duration:  time.Since(start),
		Error:     errMsg,
	}
	return result
}

// resolveTools resolves the requested tool names/groups into concrete tools
// and toolsets for a sub-agent. Each name in the request can be:
//   - A group name (e.g., "core", "browser", "mcp:github") → expands to all tools/toolsets in that group
//   - An individual tool name (e.g., "grep_search") → includes that specific tool
//
// If the request is empty, the sub-agent gets ZERO tools (callers must be explicit).
// Excluded tools (delegate_tasks, memory_save, etc.) are always removed unless
// the tool comes from FleetTools via explicit allow-list.
// In platform mode, MCP groups are filtered by the user's team/org access (via ctx).
func (m *SubAgentManager) resolveTools(ctx context.Context, request []string) ([]tool.Tool, []tool.Toolset, []string) {
	if len(request) == 0 {
		return nil, nil, nil
	}

	// Separate group names from individual tool names
	var groupNames []string
	individualNames := make(map[string]bool)
	var unknownGroups []string
	for _, name := range request {
		if _, isGroup := m.ToolGroups[name]; isGroup {
			groupNames = append(groupNames, name)
		} else if serverName, isMCP := mcpServerNameFromGroup(name); isMCP && m.MCPGroupResolver != nil {
			// Fallback: the LLM requested an MCP group that wasn't in ToolGroups
			// at init time (race with async discovery). Try to resolve it now.
			if resolved := m.MCPGroupResolver(ctx, serverName); resolved != nil {
				m.ToolGroups[name] = resolved
				groupNames = append(groupNames, name)
				slog.Info("resolved MCP tool group via fallback", "group", name, "tools", len(resolved.Tools)+len(resolved.Toolsets))
			} else {
				individualNames[name] = true
			}
		} else {
			individualNames[name] = true
		}
	}

	// Collect tools and toolsets from requested groups
	seen := make(map[string]bool) // dedup by tool name
	var resultTools []tool.Tool
	var resultToolsets []tool.Toolset

	for _, gName := range groupNames {
		// MCP tool access control: skip groups for MCP servers the user
		// doesn't have access to in platform mode.
		if serverName, isMCP := mcpServerNameFromGroup(gName); isMCP {
			if !isMCPServerAccessible(ctx, serverName) {
				unknownGroups = append(unknownGroups, gName)
				continue
			}
		}

		g := m.ToolGroups[gName]
		for _, t := range g.Tools {
			name := t.Name()
			if excludedChildTools[name] || seen[name] {
				continue
			}
			seen[name] = true
			resultTools = append(resultTools, t)
		}
		resultToolsets = append(resultToolsets, g.Toolsets...)
	}

	// Resolve individual tool names by searching all groups
	if len(individualNames) > 0 {
		for _, g := range m.ToolGroups {
			for _, t := range g.Tools {
				name := t.Name()
				if !individualNames[name] || excludedChildTools[name] || seen[name] {
					continue
				}
				seen[name] = true
				resultTools = append(resultTools, t)
			}
		}
		// Also search FleetTools for individually requested tools
		// (fleet tools are only accessible via explicit request)
		for _, t := range m.FleetTools {
			name := t.Name()
			if individualNames[name] && !seen[name] {
				seen[name] = true
				resultTools = append(resultTools, t)
			}
		}

		// Check for individual names that didn't resolve to any tool —
		// these are likely misspelled group names (e.g., "drills" instead of "drill").
		for name := range individualNames {
			if !seen[name] && !excludedChildTools[name] {
				unknownGroups = append(unknownGroups, name)
			}
		}
	}

	// Build warnings for unresolved names
	var warnings []string
	if len(unknownGroups) > 0 {
		sort.Strings(unknownGroups)
		available := make([]string, 0, len(m.ToolGroups))
		for gName := range m.ToolGroups {
			available = append(available, gName)
		}
		sort.Strings(available)
		warnings = append(warnings, fmt.Sprintf(
			"WARNING: unknown tool group(s) or tool name(s): %v — not found in any group. Available groups: %v",
			unknownGroups, available,
		))
	}

	return resultTools, resultToolsets, warnings
}

// AllTools returns all tools from all groups (used for SELF.md generation and flow distillation).
func (m *SubAgentManager) AllTools() []tool.Tool {
	seen := make(map[string]bool)
	var all []tool.Tool
	for _, g := range m.ToolGroups {
		for _, t := range g.Tools {
			if !seen[t.Name()] {
				seen[t.Name()] = true
				all = append(all, t)
			}
		}
	}
	return all
}

// AllToolsets returns all toolsets from all groups (used for SELF.md generation and flow distillation).
func (m *SubAgentManager) AllToolsets() []tool.Toolset {
	var all []tool.Toolset
	for _, g := range m.ToolGroups {
		all = append(all, g.Toolsets...)
	}
	return all
}

// AvailableGroups returns summaries of all tool groups for system prompt generation.
// Returns groups sorted by name for deterministic output.
func (m *SubAgentManager) AvailableGroups() []*ToolGroup {
	groups := make([]*ToolGroup, 0, len(m.ToolGroups))
	for _, g := range m.ToolGroups {
		groups = append(groups, g)
	}
	// Sort by name for deterministic output
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Name < groups[j].Name
	})
	return groups
}

// buildChildPrompt constructs the system prompt for a sub-agent.
func (m *SubAgentManager) buildChildPrompt(ctx context.Context, task SubAgentTask) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("You are %q, a focused sub-agent working on a specific task.\n\n", task.Name))

	sb.WriteString("## Your Task\n")
	sb.WriteString(task.Description)
	sb.WriteString("\n\n")

	if task.Instructions != "" {
		sb.WriteString("## Instructions\n")
		sb.WriteString(task.Instructions)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Behavior Rules\n")
	sb.WriteString("- You are a sub-agent. Focus ONLY on the task described above.\n")
	sb.WriteString("- Complete the task efficiently using the tools available to you.\n")
	sb.WriteString("- When done, provide a clear summary of what you accomplished and any relevant results.\n")
	sb.WriteString("- Do NOT ask clarifying questions — work with the information provided.\n")
	sb.WriteString("- Do NOT attempt to save to memory or schedule jobs — you don't have those capabilities.\n")
	sb.WriteString("- Do NOT write scripts or code unless explicitly asked — use your tools directly.\n")
	sb.WriteString("- If you encounter an error, report it clearly in your response.\n")
	if len(task.ToolFilter) > 0 {
		sb.WriteString(fmt.Sprintf("- Your assigned tools (%s) are your PRIMARY tools — use them first. Additional tools may be available via `search_tools`, but use them only if your primary tools are genuinely insufficient to complete the task (e.g., all primary tools are failing or the task requires a capability they don't have).\n", strings.Join(task.ToolFilter, ", ")))
	}

	// Tool-specific operational guidance based on what tools the child actually has
	resolvedTools, _, resolveWarnings := m.resolveTools(ctx, task.ToolFilter)
	if len(resolveWarnings) > 0 {
		slog.Warn("failed to resolve tools for sub-agent", "tool_filter", task.ToolFilter, "warnings", resolveWarnings)
	}
	childToolSet := make(map[string]bool, len(resolvedTools))
	for _, t := range resolvedTools {
		childToolSet[t.Name()] = true
	}

	if childToolSet["http_request"] {
		sb.WriteString("\n## HTTP Requests\n")
		sb.WriteString("- Use the `http_request` tool for all API calls. Do NOT write scripts or use shell_command for HTTP.\n")
		sb.WriteString("- Set `credential` to a stored credential name for authenticated requests (the auth header is injected automatically).\n")
		sb.WriteString("- Use `list_credentials` to see available credentials if you need to find the right one.\n")
		sb.WriteString("- For JSON APIs, Content-Type is set automatically when the body is JSON.\n")
	}

	if childToolSet["resolve_credential"] {
		sb.WriteString("\n## Credentials\n")
		sb.WriteString("- Use `resolve_credential` to get raw credential fields (username, password) for non-HTTP use.\n")
		sb.WriteString("- Use `list_credentials` to discover available credentials.\n")
		sb.WriteString("- You cannot create or modify credentials — only read existing ones.\n")
	}

	// Web tool guidance: help sub-agents choose between search, fetch, and browser
	if childToolSet["web_fetch"] && m.WebSearchToolName != "" {
		sb.WriteString("\n## Web Tools\n")
		sb.WriteString(fmt.Sprintf("- `%s` — for quick factual lookups and discovering URLs/resources. Returns search-engine-indexed results (may be hours/days stale).\n", m.WebSearchToolName))
		sb.WriteString("- `web_fetch` — for fetching content from a specific known URL.\n")
		if m.WebExtractToolName != "" {
			sb.WriteString(fmt.Sprintf("- `%s` — fallback for extracting content from URLs when `web_fetch` returns empty or broken content.\n", m.WebExtractToolName))
		}
		if childToolSet["browser_navigate"] {
			sb.WriteString("- **Browser tools** (`browser_navigate`, `browser_snapshot`, etc.) — for navigating websites to get live/current data (prices, availability, dynamic content). Prefer browser when the task requires what a site shows *right now*, not what was indexed days ago.\n")
		}
	}

	// Sandbox workspace/recovery guidance for sub-agents
	if m.SandboxEnabled {
		wsDir := m.SandboxWorkspaceDir
		if wsDir == "" {
			wsDir = "/root"
		}
		sb.WriteString("\n## Sandbox\n")
		sb.WriteString(fmt.Sprintf("- Your shell commands run inside an isolated sandbox. Persistent workspace: `%s`.\n", wsDir))
		sb.WriteString("- Always work in the persistent workspace (NOT /tmp). If files are missing (sandbox was recycled), silently re-clone and continue.\n")
	}

	// Inject skill index so sub-agents know which skills exist and can
	// call skill_lookup to load them on demand.
	if m.SkillIndex != "" {
		sb.WriteString("\n")
		sb.WriteString(m.SkillIndex)
	}

	return sb.String()
}
