package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// KnowledgeSearchResult holds a single result from the knowledge vector search.
type KnowledgeSearchResult struct {
	ID        string
	Path      string
	Score     float64
	Snippet   string
	Category  string // e.g. "guidance", "skill", "flow", "knowledge"
	Scope     string // e.g. "personal", "team", "org" in platform mode
	CreatedBy string
	CreatedAt string
	SessionID string
}

// KnowledgeSearchFunc performs a hybrid search and returns matching results.
// Used to auto-retrieve relevant knowledge before LLM execution.
// The bm25Query parameter provides conversational context for BM25 keyword
// matching; when empty, BM25 uses the same query as vector search.
type KnowledgeSearchFunc func(ctx context.Context, query string, bm25Query string, maxResults int, minScore float64) ([]KnowledgeSearchResult, error)

// KnowledgeSearchByCategoryFunc performs a hybrid search filtered by category.
// Categories: "guidance", "skill", "flow", "self", "instructions", "knowledge".
type KnowledgeSearchByCategoryFunc func(ctx context.Context, query string, bm25Query string, maxResults int, minScore float64, category string) ([]KnowledgeSearchResult, error)

// ChatAgent implements a dynamic chat agent without flow definitions.
// It wraps ADK's llmagent in a persistent chat session where the LLM
// decides which tools to call and how to proceed.
//
// Execution records a trace. After reusable tasks, auto-distillation
// generates a flow YAML + knowledge doc. /distill remains as manual fallback.
type ChatAgent struct {
	LLM            model.LLM
	Tools          []tool.Tool
	Toolsets       []tool.Toolset
	SessionService session.Service
	SystemPrompt   *SystemPromptBuilder
	DebugMode      bool
	AutoApprove    bool
	MaxToolCalls   int // Max consecutive tool calls per turn (default: 25)

	// Flow distillation
	FlowSaveDir   string         // Directory for saved flows (default: agents dir)
	FlowRegistry  *FlowRegistry  // Registry for saved flows
	FlowDistiller *FlowDistiller // Distiller for trace-to-YAML conversion
	FlowRunner    FlowRunnerFunc // Executes a flow YAML for dry-run testing (nil = disabled)

	// Memory and knowledge
	PlatformReflector         *PlatformReflector            // Post-task memory reflection for platform mode (nil = disabled)
	KnowledgeSearch           KnowledgeSearchFunc           // Auto-retrieve relevant knowledge per turn (nil = disabled)
	KnowledgeSearchByCategory KnowledgeSearchByCategoryFunc // Auto-retrieve guidance docs per turn (nil = disabled)
	// Task delegation
	SubAgentManager *SubAgentManager // Sub-agent manager for trace attachment (nil = no delegation)
	// Tool discovery
	ToolIndex *ToolIndex // Semantic tool index for auto-discovery (nil = disabled)

	// Dynamic tool injection: per-turn state for the BeforeModelCallback
	// that injects relevant tools into each LLM request.
	dynamicToolMatches []ToolMatch // from hybrid search on user message (reset each turn)
	searchToolsResults []string    // tool names found via search_tools calls within current turn
	searchToolsMu      sync.Mutex  // protects searchToolsResults

	// toolsMu protects Tools / SystemPrompt.Tools when a late-configured
	// platform tool (e.g. perplexity_web_search) is added after init.
	toolsMu sync.Mutex

	// Self-management callbacks
	SelfMDRefresher func() // Called after config changes to regenerate SELF.md

	// Credential redaction
	Redactor          *credentials.Redactor                         // Redacts credential values from tool outputs (nil = disabled)
	CredentialStore   credentials.CredentialResolver                // Credential store for placeholder substitution (nil = disabled)
	PendingSecrets    *credentials.PendingVault                     // Per-session vault for <<<SECRET_N>>> token resolution (nil = disabled)
	RedactSessionFunc func(appName, userID, sessionID string) error // Called after save_credential to retroactively redact the session transcript (nil = disabled)

	// Context compaction
	Compactor *persistentsession.Compactor // Manages context window compaction (nil = disabled)

	// Sub-task transparency: when set, sub-agent events (tool calls, results,
	// text) are forwarded to the UI in real-time during delegate_tasks execution.
	// This callback streams display-only events that bypass session persistence.
	// Set by the launcher (console or Studio SSE handler). Thread-safe: may be
	// called concurrently from multiple sub-agent goroutines.
	UIEventCallback func(event *session.Event)

	// SubTaskProgressCallback, when set, is called for structured sub-task
	// lifecycle events (delegation_start, task_start, task_complete, task_failed).
	// Unlike UIEventCallback (which forwards raw ADK events), this provides
	// higher-level progress tracking for task plan visualization in the UI.
	// Thread-safe: may be called concurrently from multiple sub-agent goroutines.
	SubTaskProgressCallback func(event SubTaskProgressEvent)

	// subTaskProgressBySession holds per-session SubTaskProgressCallback
	// registrations. When multiple sessions run concurrently on the same
	// singleton ChatAgent, each runner registers its own callback keyed by
	// session ID. The SubAgentRunner's SubTaskProgress closure (set in
	// chat_factory.go) calls EmitSubTaskProgress which routes to the correct
	// session's callback. This prevents delegate_tasks events from leaking
	// across sessions.
	subTaskProgressBySession map[string]func(SubTaskProgressEvent)
	subTaskProgressMu        sync.RWMutex

	// uiEventBySession holds per-session UIEventCallback registrations.
	// Same pattern as subTaskProgressBySession — prevents sub-agent events
	// (tool_call, tool_result, text) from leaking across concurrent sessions.
	uiEventBySession map[string]func(*session.Event)
	uiEventMu        sync.RWMutex

	// Internal: reuse AstonishAgent for approval formatting
	approvalHelper *AstonishAgent

	// Internal: per-session execution traces for on-demand /distill
	traceHistory         map[string][]*ExecutionTrace // keyed by session ID
	pendingDistill       map[string]*distillPreview   // keyed by session ID
	pendingDistillReview map[string]*DistillReview    // keyed by session ID — interactive review state
	pendingTutorialBP    map[string]*TutorialBlueprintPending
	approvedTutorialBP   map[string]bool // session has creator-approved blueprint (sticky until re-present/cancel)
	traceMu              sync.Mutex      // protects traceHistory, pendingDistill, pendingDistillReview, pendingTutorialBP, approvedTutorialBP

	// Image side-channel: images stripped from tool results before they
	// enter session history, available for channels to deliver to users.
	pendingImages []ImageFromTool
	imageMu       sync.Mutex

	// File artifact side-channel: file paths captured from write_file and
	// edit_file tool calls, delivered to the UI for inline display/download.
	pendingFiles []FileArtifact
	fileMu       sync.Mutex

	// Flow output side-channel: large flow outputs are stripped from the
	// tool result (so the LLM doesn't try to summarize them) and stashed
	// here for direct delivery to the user via SSE or channel output.
	pendingFlowOutput string
	flowOutputMu      sync.Mutex

	// Plan auto-progression: tracks step state from announce_plan so that
	// AfterToolCallback can automatically mark steps running/complete
	// without requiring the LLM to call update_plan (saving full round-trips).
	activePlan                   *PlanState
	activePlanApproved           bool
	activePlanReplacementAllowed bool
	activePlanVersion            uint64
	activePlanMu                 sync.Mutex

	// activeGraphPlan is the GraphPlanState the gplan_* transition tools drive
	// for the current turn. Code mode sets it at turn start (to the per-session
	// state) so the transition-tool callback and the runtime gate operate on the
	// same object. Nil outside Graph-Optimized Plan mode.
	activeGraphPlan   *GraphPlanState
	activeGraphPlanMu sync.Mutex

	// planFilePath, when set, is the per-session PLAN.md path. When a plan is
	// active, it is written on announce and rewritten on every phase transition
	// so the plan survives context compaction. Code-mode only (native FS).
	planFilePath string
	planFileMu   sync.Mutex

	// Active app refinement: per-session state for iterative generative UI refinement.
	// When set, the chat handler injects the current app source into SessionContext
	// so the LLM can apply incremental changes.
	activeApps  map[string]*ActiveApp // keyed by session ID
	activeAppMu sync.Mutex

	// Code-mode authorization (astonish code). When EnforceAuthorization is
	// true, tools that are not in the read-only whitelist (agent.SafeTools)
	// require explicit user authorization to run, and filesystem paths outside
	// WorkingDir require folder-access authorization. Both are enforced by
	// BeforeToolCallbacks in Run. Studio/platform mode leaves this false — those
	// surfaces are already sandboxed / tenant-scoped.
	EnforceAuthorization bool
	// WorkingDir is the project root used for folder-access scoping (code mode).
	WorkingDir string
	// SubAgentAuthGate, when set, is the blocking authorization gate for sub-agents.
	// Set by the TUI backend (tui_code.go) when it starts a session. The gate
	// blocks the sub-agent goroutine and surfaces the request to the TUI for
	// user approval. Only used in code-mode with EnforceAuthorization=true.
	SubAgentAuthGate func(req SubAgentAuthRequest) SubAgentAuthResponse
	// authPolicies holds one SessionAuthPolicy per session, keyed by session ID.
	authPolicies sync.Map // map[string]*SessionAuthPolicy
	// graphPlanStates holds one GraphPlanState per session, keyed by session ID.
	// Used by Graph-Optimized Plan mode (code mode only) to drive the phased
	// runtime tool gate.
	graphPlanStates sync.Map // map[string]*GraphPlanState
}

// SetEnforceAuthorization toggles the code-mode tool/folder authorization gates.
func (c *ChatAgent) SetEnforceAuthorization(enforce bool) {
	if c == nil {
		return
	}
	c.EnforceAuthorization = enforce
}

// SetWorkingDir sets the project root used for folder-access scoping.
func (c *ChatAgent) SetWorkingDir(dir string) {
	if c == nil {
		return
	}
	c.WorkingDir = dir
}

// GetOrCreateAuthPolicy returns the per-session authorization policy, creating
// it (scoped to WorkingDir) on first use.
func (c *ChatAgent) GetOrCreateAuthPolicy(sessionID string) *SessionAuthPolicy {
	if c == nil {
		return nil
	}
	if existing, ok := c.authPolicies.Load(sessionID); ok {
		return existing.(*SessionAuthPolicy)
	}
	// Whitelist Astonish's own state directory (session transcripts, PLAN.md,
	// per-session workspaces, config) so routine writes there never prompt for
	// folder access — that directory lives outside the project root but is
	// owned by Astonish, not the user. Best-effort: if the config dir can't be
	// resolved the policy simply omits it (no extra allowance).
	var extraRoots []string
	if cfgDir, err := config.GetConfigDir(); err == nil && cfgDir != "" {
		extraRoots = append(extraRoots, cfgDir)
	}
	created := NewSessionAuthPolicy(c.WorkingDir, extraRoots...)
	actual, _ := c.authPolicies.LoadOrStore(sessionID, created)
	return actual.(*SessionAuthPolicy)
}

// GetOrCreateGraphPlanState returns the per-session Graph-Optimized Plan phase
// state machine, creating it (in the initial graph phase) on first use. Mirrors
// GetOrCreateAuthPolicy.
func (c *ChatAgent) GetOrCreateGraphPlanState(sessionID string) *GraphPlanState {
	if c == nil {
		return nil
	}
	if existing, ok := c.graphPlanStates.Load(sessionID); ok {
		return existing.(*GraphPlanState)
	}
	actual, _ := c.graphPlanStates.LoadOrStore(sessionID, NewGraphPlanState())
	return actual.(*GraphPlanState)
}

// ImageFromTool holds image data extracted from a tool result before the
// result is persisted to session history. This prevents large base64 blobs
// from polluting the session transcript and being replayed to the LLM.
type ImageFromTool struct {
	Data   []byte // raw image bytes
	Format string // "png" or "jpeg"
}

// FileArtifact holds metadata about a file created/modified by a tool call.
// Captured from write_file and edit_file tool args for UI display.
type FileArtifact struct {
	Path     string // Absolute file path
	ToolName string // "write_file" or "edit_file"
}

// DryRunExecResult holds the output from executing a distilled flow as a test run.
type DryRunExecResult struct {
	Success      bool     // Whether the flow completed without errors
	Output       string   // Combined output from all nodes
	Error        string   // Error message if the flow failed
	NodesVisited []string // Ordered list of nodes executed during the run
}

// FlowRunnerFunc is a function that executes a flow YAML with the given inputs
// and returns the execution result. Used for dry-run testing of distilled flows.
// The params map provides input variable values extracted from the original trace.
type FlowRunnerFunc func(ctx context.Context, yamlContent string, params map[string]string) (*DryRunExecResult, error)

// distillPreview holds the result of PreviewDistill for use by ConfirmAndDistill.
type distillPreview struct {
	Description string            // LLM-generated task description
	Traces      []*ExecutionTrace // selected traces to distill
}

// DistillReview holds the state of an interactive distill review session.
// The user can request modifications until they're satisfied, then save.
type DistillReview struct {
	YAML             string            // Current YAML draft
	FlowName         string            // Suggested flow name
	Description      string            // Flow description
	Tags             []string          // Flow tags
	Explanation      string            // Human-readable explanation
	Traces           []*ExecutionTrace // Original traces (for context in modifications)
	Modifications    []string          // History of user change requests
	LastDryRunOutput string            // Output from last test run (for modification context)
	LastDryRunError  string            // Error from last test run
}

// DistillSession identifies a session for distillation, providing the
// information needed to look up persisted session events for trace
// reconstruction across daemon restarts.
type DistillSession struct {
	SessionID string // persistent session key (e.g. "telegram:direct:12345")
	AppName   string // ADK app name (always "astonish")
	UserID    string // ADK user ID for session lookup
}

// NewChatAgent creates a ChatAgent with all configured tools and toolsets.
func NewChatAgent(llm model.LLM, internalTools []tool.Tool, toolsets []tool.Toolset,
	sessionService session.Service, promptBuilder *SystemPromptBuilder,
	debugMode bool, autoApprove bool) *ChatAgent {

	maxToolCalls := 100

	return &ChatAgent{
		LLM:                  llm,
		Tools:                internalTools,
		Toolsets:             toolsets,
		SessionService:       sessionService,
		SystemPrompt:         promptBuilder,
		DebugMode:            debugMode,
		AutoApprove:          autoApprove,
		MaxToolCalls:         maxToolCalls,
		approvalHelper:       &AstonishAgent{LLM: llm, AutoApprove: autoApprove},
		traceHistory:         make(map[string][]*ExecutionTrace),
		pendingDistill:       make(map[string]*distillPreview),
		pendingDistillReview: make(map[string]*DistillReview),
		pendingTutorialBP:    make(map[string]*TutorialBlueprintPending),
		approvedTutorialBP:   make(map[string]bool),
		activeApps:           make(map[string]*ActiveApp),
	}
}

// RegisterSearchToolsResults records tool names discovered by search_tools
// during the current turn. The DynamicToolInjectionCallback reads these
// on the next intra-turn BeforeModelCallback firing, making the tools
// immediately available for the LLM to call.
func (c *ChatAgent) RegisterSearchToolsResults(toolNames []string) {
	c.searchToolsMu.Lock()
	defer c.searchToolsMu.Unlock()
	c.searchToolsResults = append(c.searchToolsResults, toolNames...)
}

// EnsureMainThreadTool adds t to the agent's static tool list if missing.
// Used when platform web search is configured after the singleton agent was
// first initialized (or pre-warmed without tenant web settings).
func (c *ChatAgent) EnsureMainThreadTool(t tool.Tool) {
	if c == nil || t == nil {
		return
	}
	name := t.Name()
	c.toolsMu.Lock()
	defer c.toolsMu.Unlock()
	for _, existing := range c.Tools {
		if existing != nil && existing.Name() == name {
			return
		}
	}
	c.Tools = append(c.Tools, t)
	if c.SystemPrompt != nil {
		for _, existing := range c.SystemPrompt.Tools {
			if existing != nil && existing.Name() == name {
				return
			}
		}
		c.SystemPrompt.Tools = append(c.SystemPrompt.Tools, t)
	}
}

// HasMainThreadTool reports whether a tool with the given name is already
// registered on the agent.
func (c *ChatAgent) HasMainThreadTool(name string) bool {
	if c == nil || name == "" {
		return false
	}
	c.toolsMu.Lock()
	defer c.toolsMu.Unlock()
	for _, existing := range c.Tools {
		if existing != nil && existing.Name() == name {
			return true
		}
	}
	return false
}

// AutoInjectMissingToolCallback returns an OnToolErrorCallback that recovers
// when the LLM calls a tool that exists in ToolIndex but was not loaded into
// the current request. Under ADK 1.5, missing tools surface as FunctionResponse
// errors (not hard Run aborts); this callback registers the tool for injection
// on the next LLM round and tells the model to retry with the same arguments.
//
// The tool is not executed here — that would bypass BeforeTool/AfterTool
// (credentials, secrets, redaction, tracing). Injection + retry uses the
// normal callTool path on the next step.
func (c *ChatAgent) AutoInjectMissingToolCallback() llmagent.OnToolErrorCallback {
	return autoInjectMissingToolCallback(c.ToolIndex, c.RegisterSearchToolsResults, nil)
}

// autoInjectMissingToolCallback builds the shared OnToolErrorCallback used by
// ChatAgent and sub-agents. register records names for DynamicToolInjectionCallback
// (or the child equivalent). exclude skips tools that must not be injected
// (e.g. excludedChildTools).
func autoInjectMissingToolCallback(
	toolIndex *ToolIndex,
	register func([]string),
	exclude map[string]bool,
) llmagent.OnToolErrorCallback {
	return func(ctx agent.ToolContext, t tool.Tool, _ map[string]any, err error) (map[string]any, error) {
		if toolIndex == nil || register == nil || t == nil || !isToolNotFoundError(err, t.Name()) {
			return nil, nil // let ADK keep its default not-found response
		}

		name := t.Name()
		// Normalize app-style refs (mcp:email/send_email → send_email).
		resolved := resolveIndexedToolName(toolIndex, name)
		if exclude != nil && (exclude[name] || exclude[resolved]) {
			return nil, nil
		}
		if !canAutoInjectTool(ctx, toolIndex, resolved) {
			return nil, nil
		}

		register([]string{resolved})
		slog.Debug("auto-injected missing tool for next LLM call",
			"component", "chat", "tool", resolved, "requested", name)

		hint := resolved
		if resolved != name {
			hint = fmt.Sprintf("%s (not %q — use the bare tool name)", resolved, name)
		}
		return map[string]any{
			"error": fmt.Sprintf(
				"Tool %s exists but was not loaded for this turn. "+
					"It has been injected into the session — call %q again with the same arguments.",
				hint, resolved,
			),
		}, nil
	}
}

// isToolNotFoundError reports whether err is ADK's tool-not-found error for toolName.
// Matches ADK 1.5's "tool 'X' not found" FunctionResponse path and the legacy
// hard-error form "unknown tool:".
func isToolNotFoundError(err error, toolName string) bool {
	if err == nil || toolName == "" {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, fmt.Sprintf("tool '%s' not found", toolName)) {
		return true
	}
	if strings.Contains(msg, "unknown tool:") && strings.Contains(msg, toolName) {
		return true
	}
	return false
}

// canAutoInjectTool reports whether toolName may be injected from ToolIndex
// or request-scoped MCP groups (MCP access + team disabled-tool list).
// Accepts bare names and mcp:server/tool aliases.
func canAutoInjectTool(ctx context.Context, toolIndex *ToolIndex, toolName string) bool {
	if toolName == "" {
		return false
	}
	// Request-scoped MCP tools (team catalog) take precedence.
	if t, gName, ok := LookupRequestMCPTool(ctx, toolName); ok && t != nil {
		for _, disabled := range store.DisabledToolsFromContext(ctx) {
			if disabled == t.Name() || disabled == toolName {
				return false
			}
		}
		if serverName, isMCP := mcpServerNameFromGroup(gName); isMCP {
			if !isMCPServerAccessible(ctx, serverName) {
				return false
			}
		}
		return true
	}
	if toolIndex == nil {
		return false
	}
	resolved := resolveIndexedToolName(toolIndex, toolName)
	entry := toolIndex.GetToolEntry(resolved)
	if entry == nil || entry.Tool == nil {
		return false
	}
	for _, disabled := range store.DisabledToolsFromContext(ctx) {
		if disabled == resolved || disabled == toolName {
			return false
		}
	}
	if serverName, isMCP := mcpServerNameFromGroup(entry.GroupName); isMCP {
		if !isMCPServerAccessible(ctx, serverName) {
			return false
		}
	}
	return true
}

// DynamicToolInjectionCallback returns a BeforeModelCallback that injects
// relevant tools into each LLM request based on two sources:
//
//  1. Automatic: hybrid search matches computed at the start of each user turn
//  2. Explicit: tool names discovered via search_tools calls within the turn
//
// This fires on every LLM API call (including after tool results), so tools
// found via search_tools become available on the very next LLM call within
// the same turn.
func (c *ChatAgent) DynamicToolInjectionCallback() llmagent.BeforeModelCallback {
	return func(cbCtx agent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
		if c.ToolIndex == nil {
			return nil, nil
		}

		// Collect tool names to inject from both sources.
		toolsToInject := make(map[string]bool)

		// Source 1: hybrid search matches (set at start of turn)
		for _, m := range c.dynamicToolMatches {
			if !m.IsMainTool {
				toolsToInject[m.ToolName] = true
			}
		}

		// Source 2: search_tools explicit discoveries (accumulated intra-turn)
		c.searchToolsMu.Lock()
		for _, name := range c.searchToolsResults {
			toolsToInject[name] = true
		}
		c.searchToolsMu.Unlock()

		// Source 3: pinned tool groups from PromptOverrides (wizard sessions).
		// These ensure critical tools remain available across all turns of a
		// multi-turn guided conversation regardless of ToolIndex scoring.
		if po := PromptOverridesFromContext(cbCtx); po != nil && len(po.PinnedToolGroups) > 0 {
			for _, groupName := range po.PinnedToolGroups {
				entries := c.ToolIndex.GetToolsByGroup(groupName)
				for _, entry := range entries {
					if !entry.IsMainTool {
						toolsToInject[entry.Name] = true
					}
				}
				// Also check request-scoped groups (e.g., per-request A2A tools)
				// that are not in the singleton ToolIndex.
				if len(entries) == 0 {
					if reqGroups := RequestMCPGroupsFromContext(cbCtx); reqGroups != nil {
						if g := reqGroups[groupName]; g != nil {
							for _, t := range g.Tools {
								if t != nil {
									toolsToInject[t.Name()] = true
								}
							}
							readCtx := &minimalReadonlyContext{Context: cbCtx}
							for _, ts := range g.Toolsets {
								if ts == nil {
									continue
								}
								tools, err := ts.Tools(readCtx)
								if err != nil {
									continue
								}
								for _, t := range tools {
									if t != nil {
										toolsToInject[t.Name()] = true
									}
								}
							}
						}
					}
				}
			}
		}

		if len(toolsToInject) == 0 {
			return nil, nil
		}

		// Inject each tool into the request.
		injected := 0
		for toolName := range toolsToInject {
			resolved := resolveIndexedToolName(c.ToolIndex, toolName)
			if _, exists := req.Tools[resolved]; exists {
				continue // already registered (static main-thread tool)
			}

			// Prefer request-scoped MCP tools (team catalog) over stale index entries.
			if t, gName, ok := LookupRequestMCPTool(cbCtx, toolName); ok && t != nil {
				if serverName, isMCP := mcpServerNameFromGroup(gName); isMCP {
					if !isMCPServerAccessible(cbCtx, serverName) {
						continue
					}
				}
				if _, exists := req.Tools[t.Name()]; exists {
					continue
				}
				packToolIntoRequest(req, t)
				injected++
				continue
			}

			entry := c.ToolIndex.GetToolEntry(resolved)
			if entry == nil || entry.Tool == nil {
				// If the LLM/search asked for a whole MCP group (mcp:email),
				// inject every tool in that group (index + request-scoped).
				if group, bare, isRef := parseMCPToolRef(toolName); isRef && bare == "" {
					if reqGroups := RequestMCPGroupsFromContext(cbCtx); reqGroups != nil {
						if g := reqGroups[group]; g != nil {
							if serverName, isMCP := mcpServerNameFromGroup(group); isMCP {
								if !isMCPServerAccessible(cbCtx, serverName) {
									continue
								}
							}
							readCtx := &minimalReadonlyContext{Context: cbCtx}
							for _, ts := range g.Toolsets {
								tools, err := ts.Tools(readCtx)
								if err != nil {
									continue
								}
								for _, gt := range tools {
									if gt == nil {
										continue
									}
									if _, exists := req.Tools[gt.Name()]; exists {
										continue
									}
									packToolIntoRequest(req, gt)
									injected++
								}
							}
						}
					}
					if c.ToolIndex != nil {
						for _, ge := range c.ToolIndex.GetToolsByGroup(group) {
							if ge.Tool == nil {
								continue
							}
							if serverName, isMCP := mcpServerNameFromGroup(ge.GroupName); isMCP {
								if !isMCPServerAccessible(cbCtx, serverName) {
									continue
								}
							}
							if _, exists := req.Tools[ge.Name]; exists {
								continue
							}
							packToolIntoRequest(req, ge.Tool)
							injected++
						}
					}
				}
				continue
			}

			// MCP tool access control: in platform mode, only inject tools
			// from MCP servers the user's team/org has access to.
			if serverName, isMCP := mcpServerNameFromGroup(entry.GroupName); isMCP {
				if !isMCPServerAccessible(cbCtx, serverName) {
					continue
				}
			}

			packToolIntoRequest(req, entry.Tool)
			injected++
		}

		if c.DebugMode && injected > 0 {
			slog.Debug("dynamic tool injection", "component", "chat", "injected", injected)
		}

		return nil, nil
	}
}

// toolWithDeclaration matches ADK's internal FunctionTool interface for tools
// that can declare their JSON schema. All function-based tools implement this.
type toolWithDeclaration interface {
	Declaration() *genai.FunctionDeclaration
}

// packToolIntoRequest adds a tool to an LLM request for both dispatch and
// schema declaration. This replicates the logic from ADK's internal PackTool
// (toolutils.go) and Astonish's NodeTool.ProcessRequest.
func packToolIntoRequest(req *model.LLMRequest, t tool.Tool) {
	if req.Tools == nil {
		req.Tools = make(map[string]any)
	}
	name := t.Name()
	if _, ok := req.Tools[name]; ok {
		return // already registered
	}
	req.Tools[name] = t

	// Get the function declaration via type assertion — tool.Tool doesn't
	// include Declaration(), but all function-based tools implement it.
	dt, ok := t.(toolWithDeclaration)
	if !ok {
		return
	}
	decl := dt.Declaration()
	if decl == nil {
		return
	}
	if req.Config == nil {
		req.Config = &genai.GenerateContentConfig{}
	}
	// Find existing FunctionDeclarations block (all function tools share one).
	var funcTool *genai.Tool
	for _, gt := range req.Config.Tools {
		if gt != nil && gt.FunctionDeclarations != nil {
			funcTool = gt
			break
		}
	}
	if funcTool == nil {
		req.Config.Tools = append(req.Config.Tools, &genai.Tool{
			FunctionDeclarations: []*genai.FunctionDeclaration{decl},
		})
	} else {
		funcTool.FunctionDeclarations = append(funcTool.FunctionDeclarations, decl)
	}
}

// ForwardSubTaskEvent processes a sub-agent event for transparent delegation.
// It extracts images from FunctionResponse parts (stashing them in pendingImages
// so DrainImages can deliver them to the UI), then routes the event to the
// correct session's UIEventCallback via EmitUIEvent. Thread-safe: may be called
// concurrently from multiple sub-agent goroutines.
// The sessionID identifies which parent session initiated the delegation.
func (c *ChatAgent) ForwardSubTaskEvent(sessionID string, event *session.Event) {
	if event == nil {
		return
	}

	// Extract images from tool responses before forwarding.
	// This ensures browser_take_screenshot images from sub-agents flow
	// through the same pipeline as main-thread images.
	if event.LLMResponse.Content != nil {
		for _, part := range event.LLMResponse.Content.Parts {
			if part.FunctionResponse != nil && part.FunctionResponse.Response != nil {
				// extractAndStripImages is thread-safe (uses c.imageMu)
				part.FunctionResponse.Response = c.extractAndStripImages(part.FunctionResponse.Response)
			}
		}
	}

	// Forward to the correct session's UI callback for real-time rendering
	c.EmitUIEvent(sessionID, event)
}

// EnqueueImagesFromContent extracts image/* InlineData parts from model (or
// tool) content into the pending image queue for Studio SSE and channel delivery.
// Parts are left intact so session history can reconstruct images on reload.
// Thread-safe.
func (c *ChatAgent) EnqueueImagesFromContent(content *genai.Content) {
	if content == nil {
		return
	}
	var imgs []ImageFromTool
	for _, part := range content.Parts {
		if part == nil || part.InlineData == nil || len(part.InlineData.Data) == 0 {
			continue
		}
		mime := part.InlineData.MIMEType
		if !strings.HasPrefix(mime, "image/") {
			continue
		}
		format := strings.TrimPrefix(mime, "image/")
		if format == "jpg" {
			format = "jpeg"
		}
		if format == "" {
			format = "png"
		}
		imgs = append(imgs, ImageFromTool{
			Data:   part.InlineData.Data,
			Format: format,
		})
	}
	if len(imgs) == 0 {
		return
	}
	c.imageMu.Lock()
	c.pendingImages = append(c.pendingImages, imgs...)
	c.imageMu.Unlock()
}

// DrainImages returns and clears all pending images that were extracted from
// tool results or model InlineData during the current agent run. Thread-safe.
// The channel manager calls this to retrieve images for delivery without
// relying on session events.
func (c *ChatAgent) DrainImages() []ImageFromTool {
	c.imageMu.Lock()
	defer c.imageMu.Unlock()
	imgs := c.pendingImages
	c.pendingImages = nil
	return imgs
}

// CaptureFileArtifact records a file artifact produced by a tool call.
// Thread-safe: may be called from the afterToolCallback goroutine.
func (c *ChatAgent) CaptureFileArtifact(path string, toolName string) {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	c.pendingFiles = append(c.pendingFiles, FileArtifact{
		Path:     path,
		ToolName: toolName,
	})
}

// DrainFiles returns and clears all pending file artifacts captured from
// tool results during the current agent run. Thread-safe.
func (c *ChatAgent) DrainFiles() []FileArtifact {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	files := c.pendingFiles
	c.pendingFiles = nil
	return files
}

// DrainFlowOutput returns and clears any pending flow output that was
// extracted from a run_flow tool result during the current agent run.
// Thread-safe. The SSE handler calls this to deliver the full flow output
// directly to the user without it being re-processed by the chat LLM.
func (c *ChatAgent) DrainFlowOutput() string {
	c.flowOutputMu.Lock()
	defer c.flowOutputMu.Unlock()
	out := c.pendingFlowOutput
	c.pendingFlowOutput = ""
	return out
}

// SetActivePlan stores the plan state for auto-progression.
// Thread-safe: called from the announce_plan tool's planStateCallback.
//
// When a per-session plan file path is configured (SetPlanFilePath), the plan
// is written to PLAN.md immediately and rewritten on every phase transition so
// it survives context compaction.
func (c *ChatAgent) SetActivePlan(plan *PlanState) {
	c.activePlanMu.Lock()
	c.activePlanVersion++
	version := c.activePlanVersion
	c.activePlan = plan
	c.activePlanApproved = false
	c.activePlanReplacementAllowed = false
	c.activePlanMu.Unlock()
	c.persistActivePlan(plan, version)
}

// TrySetActivePlan installs a newly announced plan unless the current plan has
// already been approved for execution. The check and replacement are atomic, so
// stale or concurrent announce_plan calls cannot overwrite the approved state.
func (c *ChatAgent) TrySetActivePlan(plan *PlanState) bool {
	c.activePlanMu.Lock()
	if c.activePlan != nil && (c.activePlanApproved || !c.activePlanReplacementAllowed) {
		c.activePlanMu.Unlock()
		return false
	}
	c.activePlanVersion++
	version := c.activePlanVersion
	c.activePlan = plan
	c.activePlanReplacementAllowed = false
	c.activePlanMu.Unlock()

	c.persistActivePlan(plan, version)
	return true
}

// AllowActivePlanReplacement opens one revision slot for a planning turn after
// the user explicitly requests changes. The next accepted announcement consumes
// the slot, preventing parallel announcements from both replacing the plan.
func (c *ChatAgent) AllowActivePlanReplacement() {
	c.activePlanMu.Lock()
	if !c.activePlanApproved {
		c.activePlanReplacementAllowed = true
	}
	c.activePlanMu.Unlock()
}

// MarkActivePlanApproved seals the current plan against replacement while
// allowing update_plan to continue mutating its step statuses.
func (c *ChatAgent) MarkActivePlanApproved() {
	c.activePlanMu.Lock()
	if c.activePlan != nil {
		c.activePlanApproved = true
	}
	c.activePlanMu.Unlock()
}

// RestoreApprovedPlan loads the authoritative PLAN.md sidecar and rehydrates
// PlanState before an approved execution turn. This removes reliance on the
// model remembering to read the file after resume or context compaction.
func (c *ChatAgent) RestoreApprovedPlan() error {
	c.planFileMu.Lock()
	path := c.planFilePath
	c.planFileMu.Unlock()
	if path == "" {
		return fmt.Errorf("plan file path is not configured")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read active plan: %w", err)
	}
	doc, goal, steps, err := ParsePlanDocument(string(data))
	if err != nil {
		return fmt.Errorf("parse active plan: %w", err)
	}
	plan := NewPlanState(goal, doc, steps)
	c.SetActivePlan(plan)
	c.MarkActivePlanApproved()
	return nil
}

func (c *ChatAgent) persistActivePlan(plan *PlanState, version uint64) {
	if plan == nil {
		return
	}

	c.planFileMu.Lock()
	path := c.planFilePath
	c.planFileMu.Unlock()
	if path == "" {
		return
	}

	// Persist on every phase transition, and once immediately so the file
	// exists the moment the plan is announced. Version checks prevent an older
	// plan's callback from rewriting the file after a newer plan was installed.
	plan.SetOnChange(func() {
		goal, steps := plan.snapshotLocked()
		c.writePlanFileVersion(version, renderPlanMarkdownWithDoc(goal, plan.doc, steps))
	})
	goal, steps := plan.Snapshot()
	c.writePlanFileVersion(version, renderPlanMarkdownWithDoc(goal, plan.SnapshotDoc(), steps))
}

// SetPlanFilePath configures the per-session PLAN.md path. Empty disables
// plan-file persistence (e.g. in tests or chat-only backends).
func (c *ChatAgent) SetPlanFilePath(path string) {
	c.planFileMu.Lock()
	c.planFilePath = path
	c.planFileMu.Unlock()
}

// SetActiveGraphPlan records the GraphPlanState the gplan_* transition tools
// should drive for the current turn (Graph-Optimized Plan mode, code mode
// only). Pass nil to clear it outside that mode.
func (c *ChatAgent) SetActiveGraphPlan(g *GraphPlanState) {
	c.activeGraphPlanMu.Lock()
	c.activeGraphPlan = g
	c.activeGraphPlanMu.Unlock()
}

// GetActiveGraphPlan returns the GraphPlanState the transition tools drive, or
// nil if not in Graph-Optimized Plan mode.
func (c *ChatAgent) GetActiveGraphPlan() *GraphPlanState {
	c.activeGraphPlanMu.Lock()
	defer c.activeGraphPlanMu.Unlock()
	return c.activeGraphPlan
}

func (c *ChatAgent) writePlanFileVersion(version uint64, content string) {
	c.activePlanMu.Lock()
	defer c.activePlanMu.Unlock()
	if version != c.activePlanVersion {
		return
	}
	c.writePlanFile(content)
}

// writePlanFile writes the rendered plan document to the configured PLAN.md
// path. Best-effort: write failures are logged, not surfaced to the model.
func (c *ChatAgent) writePlanFile(content string) {
	c.planFileMu.Lock()
	path := c.planFilePath
	c.planFileMu.Unlock()
	if path == "" {
		return
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		slog.Debug("failed to write PLAN.md", "component", "chat_agent", "path", path, "error", err)
	}
}

// GetActivePlan returns the current plan state, or nil if no plan is active.
// Thread-safe: may be called from sub-agent progress event handlers.
func (c *ChatAgent) GetActivePlan() *PlanState {
	c.activePlanMu.Lock()
	defer c.activePlanMu.Unlock()
	return c.activePlan
}

// extractAndStripFlowOutput checks a run_flow tool result for a large "output"
// field. If found, the full output is stashed for direct delivery to the user,
// and replaced with a short pointer so the LLM does not try to summarize it.
// Returns a new map (does not mutate the original).
func (c *ChatAgent) extractAndStripFlowOutput(output map[string]any) map[string]any {
	const minStripLen = 500 // only strip outputs larger than this

	rawOutput, ok := output["output"].(string)
	if !ok || len(rawOutput) <= minStripLen {
		return output
	}

	// Stash the full output for direct delivery
	c.flowOutputMu.Lock()
	c.pendingFlowOutput = rawOutput
	c.flowOutputMu.Unlock()

	// Replace with a pointer — copy the map to avoid mutating the original
	stripped := make(map[string]any, len(output))
	for k, v := range output {
		stripped[k] = v
	}
	stripped["output"] = fmt.Sprintf(
		"[Flow output (%d characters) has been delivered directly to the user's screen. "+
			"Do NOT reproduce, summarize, or paraphrase it — the user already sees the full content. "+
			"Just present the input_options/input_prompt if any, or confirm the flow completed successfully.]",
		len(rawOutput),
	)
	return stripped
}

// extractAndStripImages checks a tool result map for an "image_base64" key.
// If found, the base64 data is decoded and stashed in the pending images queue
// for channel delivery, and the key is replaced with a short placeholder so the
// LLM knows a screenshot was taken without the full binary data polluting the
// session history or being replayed on subsequent LLM calls.
func (c *ChatAgent) extractAndStripImages(output map[string]any) map[string]any {
	if output == nil {
		return output
	}

	b64, ok := output["image_base64"].(string)
	if !ok || b64 == "" {
		return output
	}

	// Decode and stash the image for channel delivery
	data, err := base64.StdEncoding.DecodeString(b64)
	if err == nil && len(data) > 0 {
		format := "png"
		if f, ok := output["format"].(string); ok && f != "" {
			format = f
		}
		c.imageMu.Lock()
		c.pendingImages = append(c.pendingImages, ImageFromTool{
			Data:   data,
			Format: format,
		})
		c.imageMu.Unlock()
	}

	// Replace the base64 blob with a lightweight placeholder.
	// Copy the map to avoid mutating the original.
	stripped := make(map[string]any, len(output))
	for k, v := range output {
		stripped[k] = v
	}
	stripped["image_base64"] = fmt.Sprintf("[screenshot captured, %d bytes]", len(b64))
	return stripped
}

// isMCPServerAccessible checks whether the given MCP server name is accessible
// to the current user based on their org and team stores in the context.
// Returns true if:
//   - Not in platform mode (no stores in context → personal mode, allow all)
//   - The server exists in either the org store or the user's team store AND is enabled
//
// This is the per-request authorization gate for MCP tools.
func isMCPServerAccessible(ctx context.Context, serverName string) bool {
	stores := store.MCPServerStoresFromContext(ctx)
	if stores == nil {
		return true // no stores in context — allow all
	}
	// Standard servers (Tavily, Brave, etc.) are always accessible when installed.
	if config.IsStandardServerInstalled(serverName) {
		return true
	}
	// Check all three tiers: platform → org → team
	if stores.Platform != nil {
		if s, _ := stores.Platform.Get(ctx, serverName); s != nil {
			return s.IsEnabled()
		}
	}
	if stores.Org != nil {
		if s, _ := stores.Org.Get(ctx, serverName); s != nil {
			return s.IsEnabled()
		}
	}
	if stores.Team != nil {
		if s, _ := stores.Team.Get(ctx, serverName); s != nil {
			return s.IsEnabled()
		}
	}
	return false
}

// mcpServerNameFromGroup extracts the MCP server name from a tool group name.
// Group names follow the pattern "mcp:<serverName>".
// Returns the server name and true if it's an MCP group, empty string and false otherwise.
func mcpServerNameFromGroup(groupName string) (string, bool) {
	if strings.HasPrefix(groupName, "mcp:") {
		return strings.TrimPrefix(groupName, "mcp:"), true
	}
	return "", false
}

// parseMCPToolRef parses LLM/app-style MCP references:
//
//	"mcp:email"            → group "mcp:email", tool ""
//	"mcp:email/send_email" → group "mcp:email", tool "send_email"
//
// Bare tool names (no mcp: prefix) return ok=false. The slash form is common in
// visual apps (useAppAction) but is NOT a valid ADK tool name — chat must map
// it to the bare tool name for injection/execution.
func parseMCPToolRef(name string) (groupName, toolName string, ok bool) {
	if !strings.HasPrefix(name, "mcp:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, "mcp:")
	if rest == "" {
		return "", "", false
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		server := rest[:i]
		tool := rest[i+1:]
		if server == "" {
			return "", "", false
		}
		return "mcp:" + server, tool, true
	}
	return "mcp:" + rest, "", true
}

// resolveIndexedToolName maps an LLM-facing name to the ToolIndex registry key.
// Accepts bare tool names and "mcp:server/tool" aliases.
func resolveIndexedToolName(toolIndex *ToolIndex, name string) string {
	if toolIndex == nil || name == "" {
		return name
	}
	if entry := toolIndex.GetToolEntry(name); entry != nil {
		return name
	}
	if _, toolName, isRef := parseMCPToolRef(name); isRef && toolName != "" {
		if entry := toolIndex.GetToolEntry(toolName); entry != nil {
			return toolName
		}
	}
	return name
}

// FilterAccessibleToolMatches removes ToolMatch entries for MCP servers the
// current user doesn't have access to. In personal mode (no stores in context),
// all matches are returned unchanged.
//
// This must be called after ToolIndex.SearchHybrid() and before the results are
// used for prompt generation or returned to the user (e.g., in search_tools).
func FilterAccessibleToolMatches(ctx context.Context, matches []ToolMatch) []ToolMatch {
	stores := store.MCPServerStoresFromContext(ctx)
	if stores == nil {
		return matches // personal mode — no filtering
	}
	filtered := make([]ToolMatch, 0, len(matches))
	for _, m := range matches {
		if serverName, isMCP := mcpServerNameFromGroup(m.GroupName); isMCP {
			if !isMCPServerAccessible(ctx, serverName) {
				continue
			}
		}
		filtered = append(filtered, m)
	}
	return filtered
}

// IsMCPGroupInaccessible returns true if the given group name refers to an MCP
// server that the current user does NOT have access to. Returns false for
// non-MCP groups (they're always accessible) and in personal mode.
func IsMCPGroupInaccessible(ctx context.Context, groupName string) bool {
	serverName, isMCP := mcpServerNameFromGroup(groupName)
	if !isMCP {
		return false // not an MCP group — always accessible
	}
	return !isMCPServerAccessible(ctx, serverName)
}

// RegisterSubTaskProgress registers a per-session SubTaskProgressCallback.
// This allows multiple concurrent sessions on the same singleton ChatAgent to
// each receive only their own delegate_tasks events without cross-session leakage.
func (c *ChatAgent) RegisterSubTaskProgress(sessionID string, cb func(SubTaskProgressEvent)) {
	c.subTaskProgressMu.Lock()
	defer c.subTaskProgressMu.Unlock()
	if c.subTaskProgressBySession == nil {
		c.subTaskProgressBySession = make(map[string]func(SubTaskProgressEvent))
	}
	c.subTaskProgressBySession[sessionID] = cb
}

// UnregisterSubTaskProgress removes the per-session callback registration.
func (c *ChatAgent) UnregisterSubTaskProgress(sessionID string) {
	c.subTaskProgressMu.Lock()
	defer c.subTaskProgressMu.Unlock()
	delete(c.subTaskProgressBySession, sessionID)
}

// EmitSubTaskProgress routes a sub-task progress event to the correct session's
// callback. It first checks the per-session map (preferred for concurrent
// sessions), then falls back to the legacy SubTaskProgressCallback field for
// backwards compatibility with single-session modes (CLI, tests).
func (c *ChatAgent) EmitSubTaskProgress(sessionID string, evt SubTaskProgressEvent) {
	c.subTaskProgressMu.RLock()
	cb := c.subTaskProgressBySession[sessionID]
	c.subTaskProgressMu.RUnlock()
	if cb != nil {
		cb(evt)
		return
	}
	// Fallback: legacy single-callback path (CLI / tests)
	if c.SubTaskProgressCallback != nil {
		c.SubTaskProgressCallback(evt)
	}
}

// RegisterUIEvent registers a per-session UIEventCallback.
func (c *ChatAgent) RegisterUIEvent(sessionID string, cb func(*session.Event)) {
	c.uiEventMu.Lock()
	defer c.uiEventMu.Unlock()
	if c.uiEventBySession == nil {
		c.uiEventBySession = make(map[string]func(*session.Event))
	}
	c.uiEventBySession[sessionID] = cb
}

// UnregisterUIEvent removes the per-session UIEventCallback registration.
func (c *ChatAgent) UnregisterUIEvent(sessionID string) {
	c.uiEventMu.Lock()
	defer c.uiEventMu.Unlock()
	delete(c.uiEventBySession, sessionID)
}

// EmitUIEvent routes a UI event to the correct session's callback.
// Falls back to the legacy UIEventCallback field for single-session modes.
func (c *ChatAgent) EmitUIEvent(sessionID string, event *session.Event) {
	c.uiEventMu.RLock()
	cb := c.uiEventBySession[sessionID]
	c.uiEventMu.RUnlock()
	if cb != nil {
		cb(event)
		return
	}
	// Fallback: legacy single-callback path (CLI / tests)
	if c.UIEventCallback != nil {
		c.UIEventCallback(event)
	}
}

// HasSubTaskProgressForSession returns true if a per-session SubTaskProgress
// callback is registered for the given session. Used to decide whether to
// suppress flat tool_call/tool_result emission in favour of TaskPlanPanel.
func (c *ChatAgent) HasSubTaskProgressForSession(sessionID string) bool {
	c.subTaskProgressMu.RLock()
	defer c.subTaskProgressMu.RUnlock()
	_, ok := c.subTaskProgressBySession[sessionID]
	return ok
}
