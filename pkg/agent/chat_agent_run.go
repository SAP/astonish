package agent

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/provider/llmerror"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// Run implements the agent.Run interface for ADK.
// It is called by the ADK runner for each user message.
func (c *ChatAgent) Run(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		// Wrap yield to strip chain-of-thought content and redact credentials.
		// The think-tag filter is stateful (tracks whether we are inside a
		// <think> block across streaming chunks), so it must be created once
		// per Run invocation.
		thinkFilter := &thinkTagFilter{}
		origYield := yield
		yield = func(event *session.Event, err error) bool {
			filterEventThinkContent(thinkFilter, event)
			redactEventText(c.Redactor, event)
			return origYield(event, err)
		}

		// Extract user text, sanitizing <<<secret>>> tags before the LLM sees them.
		// The PendingVault replaces raw values with <<<SECRET_N>>> tokens and
		// immediately registers the raw values with the Redactor as a safety net.
		userText := ""
		if ctx.UserContent() != nil {
			for _, p := range ctx.UserContent().Parts {
				if p.Text != "" {
					if c.PendingSecrets != nil {
						p.Text = c.PendingSecrets.Extract(p.Text)
					}
					userText += p.Text
				}
			}
		}

		if c.DebugMode {
			slog.Debug("user message", "component", "chat", "text", userText)
		}

		// --- Code-mode authorization resume ---
		// When a previous turn suspended awaiting tool/folder authorization, the
		// user's decision arrives as this turn's message. Apply it to the
		// per-session policy, then rewrite the message into a retry (granted) or
		// denial (refused) instruction so the model proceeds coherently. This
		// mirrors the flow engine's handleToolApproval, adapted to the chat loop.
		if c.EnforceAuthorization {
			policy := c.GetOrCreateAuthPolicy(ctx.Session().ID())
			if policy != nil && policy.Pending() != nil {
				decision := policy.ApplyAuthorizationDecision(userText)
				if decision != nil {
					if decision.Granted {
						userText = fmt.Sprintf(
							"The user authorized `%s`. Immediately retry the previous tool call with the exact same arguments.",
							decision.Tool,
						)
					} else if decision.Kind == "folder" {
						userText = fmt.Sprintf(
							"The user did NOT authorize `%s` to access files outside the project directory. "+
								"Do not retry it. Stay within the project folder or ask the user how to proceed.",
							decision.Tool,
						)
					} else {
						userText = AuthorizationDeniedMessage(decision.Tool)
					}
					if ctx.UserContent() != nil {
						ctx.UserContent().Parts = []*genai.Part{{Text: userText}}
					}
				}
			} else if policy != nil {
				// Genuinely new user message: reset iteration-scoped grants so
				// each turn re-requests authorization for not-whitelisted tools.
				policy.ResetForNewTurn()
			}
		}

		// --- Phase A: Dynamic Execution ---
		trace := NewExecutionTrace(userText)

		// Per-turn dynamic content: auto-retrieved knowledge.
		// Appended to the end of the system prompt via
		// SystemPromptBuilder.RelevantKnowledge field,
		// so it carries system-level authority for instruction following.
		var relevantKnowledge string
		var knowledgeTrackingQuery string
		var knowledgeTrackingBM25Query string
		var knowledgeTrackingResults []KnowledgeSearchResult // for session tracking event

		// Auto-retrieve relevant knowledge from vector store.
		// Three partitioned searches: guidance (max 3) + general knowledge (max 5).
		// Flow documents are discovered naturally through the general search.
		// When a high-confidence flow match is found, it is loaded as an
		// actionable execution plan rather than passive knowledge.
		if (c.KnowledgeSearch != nil || c.KnowledgeSearchByCategory != nil) && userText != "" {
			searchQuery := buildKnowledgeQuery(userText)
			if len(searchQuery) < 5 {
				if c.DebugMode {
					slog.Debug("auto knowledge search skipped: query too short", "component", "chat", "query", searchQuery)
				}
			} else {
				knowledgeTrackingQuery = searchQuery
				// Build an augmented BM25 query that includes conversational
				// context from the last model response. This helps follow-up
				// queries like "show me per VM" find relevant docs by matching
				// topic keywords (e.g., "proxmox", "server") that the user's
				// message alone doesn't contain. Vector search stays on the
				// raw query to avoid semantic dilution.
				bm25Query := ""
				if tail := lastModelResponseTail(ctx.Session().Events(), 300); tail != "" {
					bm25Query = tail + " " + searchQuery
					knowledgeTrackingBM25Query = bm25Query
					if c.DebugMode {
						slog.Debug("augmented BM25 query with conversation context",
							"component", "chat",
							"bm25_query_len", len(bm25Query))
					}
				}

				var allResults []KnowledgeSearchResult

				// Partition 1: Guidance docs (how-to instructions for capabilities)
				if c.KnowledgeSearchByCategory != nil {
					guidanceResults, err := c.KnowledgeSearchByCategory(ctx, searchQuery, bm25Query, 3, 0.3, "guidance")
					if err != nil {
						if c.DebugMode {
							slog.Debug("guidance search failed", "component", "chat", "error", err)
						}
					} else {
						allResults = append(allResults, guidanceResults...)
					}
				}

				// Partition 2: Everything else (memory, skills, flows, knowledge)
				if c.KnowledgeSearch != nil {
					knowledgeResults, err := c.KnowledgeSearch(ctx, searchQuery, bm25Query, 5, 0.3)
					if err != nil {
						if c.DebugMode {
							slog.Debug("knowledge search failed", "component", "chat", "error", err)
						}
					} else {
						allResults = append(allResults, knowledgeResults...)
					}
				}

				// Deduplicate
				allResults = deduplicateSearchResults(allResults)

				// Format remaining results as knowledge text
				if len(allResults) > 0 {
					knowledgeTrackingResults = allResults
					var kb strings.Builder
					for _, r := range allResults {
						kb.WriteString(fmt.Sprintf("**%s** (relevance: %.0f%%)\n", r.Path, r.Score*100))
						kb.WriteString(r.Snippet)
						kb.WriteString("\n\n")
					}
					relevantKnowledge = EscapeCurlyPlaceholders(kb.String())
					if c.DebugMode {
						slog.Debug("auto knowledge search results injected", "component", "chat", "results", len(allResults), "query", truncateQuery(searchQuery, 60))
					}
				} else if c.DebugMode {
					slog.Debug("auto knowledge search: no results", "component", "chat", "query", truncateQuery(searchQuery, 60))
				}
			}
		} else if c.KnowledgeSearch == nil && c.KnowledgeSearchByCategory == nil && c.DebugMode {
			slog.Debug("auto knowledge search disabled: no search functions wired", "component", "chat")
		}

		// Persist a tracking event recording what knowledge was injected.
		// Content is nil so ADK's ContentsRequestProcessor skips it (never
		// sent to the LLM) and eventsToMessages() skips it (never shown in UI).
		// The event is still written to the session .jsonl file for diagnostics.
		yieldKnowledgeTrackingEvent(yield, knowledgeTrackingQuery, knowledgeTrackingBM25Query, relevantKnowledge, "", knowledgeTrackingResults)

		// Auto-retrieve relevant tools from the tool index.
		// Matches drive two things: (1) prompt text listing relevant tools,
		// (2) dynamic injection of concrete tool instances into the LLM request
		// via DynamicToolInjectionCallback so the LLM can call them directly.
		var relevantTools string
		var toolMatches []ToolMatch
		var toolSearchQuery string
		if c.ToolIndex != nil && userText != "" {
			toolSearchQuery = buildKnowledgeQuery(userText)
			// For short messages ("looks good", "use it", "yes"), the user text
			// alone lacks topical signal for tool discovery. Augment the query
			// with the tail of the last LLM response, which typically contains
			// the question or action prompt that gives us context.
			if len(toolSearchQuery) < shortQueryThreshold {
				if tail := lastModelResponseTail(ctx.Session().Events(), 200); tail != "" {
					toolSearchQuery = tail + " " + toolSearchQuery
					if c.DebugMode {
						slog.Debug("short message — augmented tool search query with LLM context", "component", "chat")
					}
				}
			}
			if len(toolSearchQuery) >= 5 {
				matches, err := c.ToolIndex.SearchHybrid(context.Background(), toolSearchQuery, 8, 0.005)
				if err != nil {
					if c.DebugMode {
						slog.Debug("tool index search failed", "component", "chat", "error", err)
					}
				} else {
					// Filter out MCP tools the user's team doesn't have access to
					matches = FilterAccessibleToolMatches(ctx, matches)
					toolMatches = matches
				}
			}
			// When the user names an MCP server (e.g. "use the mcp server email"),
			// force-inject that group's tools even if hybrid search scored poorly.
			// Prefer request-scoped MCP groups (this team's catalog), then index.
			// This keeps single MCP actions on the main thread instead of
			// falling through to delegate_tasks.
			if mcpHits := MatchRequestMCPGroupsFromQuery(ctx, userText); len(mcpHits) > 0 {
				mcpHits = FilterAccessibleToolMatches(ctx, mcpHits)
				toolMatches = MergeToolMatches(toolMatches, mcpHits)
			}
			if mcpHits := MatchMCPGroupsFromQuery(c.ToolIndex, userText); len(mcpHits) > 0 {
				mcpHits = FilterAccessibleToolMatches(ctx, mcpHits)
				toolMatches = MergeToolMatches(toolMatches, mcpHits)
			}
			if len(toolMatches) > 0 {
				relevantTools = FormatToolMatchesForPrompt(toolMatches)
				if c.DebugMode {
					slog.Debug("tool index search results", "component", "chat", "matches", len(toolMatches), "query", truncateQuery(toolSearchQuery, 60))
				}
			}
		}
		yieldToolTrackingEvent(yield, toolSearchQuery, relevantTools, toolMatches)

		// Store per-turn tool matches for the DynamicToolInjectionCallback
		// and reset any search_tools discoveries from the previous turn.
		c.dynamicToolMatches = toolMatches
		c.searchToolsMu.Lock()
		c.searchToolsResults = nil
		c.searchToolsMu.Unlock()

		// Clone the SystemPromptBuilder for this request to avoid races with
		// concurrent callers (scheduler jobs, channel messages, Studio chat).
		// Per-turn dynamic fields are set on the clone, which is then discarded.
		promptBuilder := c.SystemPrompt.Clone()
		if promptBuilder == nil {
			yield(nil, fmt.Errorf("SystemPrompt builder is nil"))
			return
		}

		// Apply per-turn overrides injected by callers via context
		planMode := false
		graphPlan := false
		askMode := false
		approvedPlanExecution := false
		if po := PromptOverridesFromContext(ctx); po != nil {
			planMode = po.PlanMode
			graphPlan = po.GraphPlanMode
			askMode = po.AskMode
			approvedPlanExecution = po.ApprovedPlanExecution
			if po.ChannelHints != "" {
				promptBuilder.ChannelHints = po.ChannelHints
			}
			if po.SchedulerHints != "" {
				promptBuilder.SchedulerHints = po.SchedulerHints
			}
			if po.SessionContext != "" {
				promptBuilder.SessionContext = po.SessionContext
			}
			if po.SkillIndex != "" {
				promptBuilder.SkillIndex = po.SkillIndex
			}
			// Platform/team web tools: always take the per-request values when set
			// so every user sees the platform-selected search tool, not whatever
			// was baked into the singleton agent at first init/pre-warm.
			if po.WebSearchAvailable != nil {
				promptBuilder.WebSearchAvailable = *po.WebSearchAvailable
				promptBuilder.WebSearchToolName = po.WebSearchToolName
			}
			if po.WebExtractAvailable != nil {
				promptBuilder.WebExtractAvailable = *po.WebExtractAvailable
				promptBuilder.WebExtractToolName = po.WebExtractToolName
			}
		}

		// Per-team tool restrictions: filter disabled tools from the prompt builder
		// so the LLM doesn't see them in the system prompt's capabilities list.
		if disabledTools := store.DisabledToolsFromContext(ctx); len(disabledTools) > 0 {
			disabledSet := make(map[string]bool, len(disabledTools))
			for _, name := range disabledTools {
				disabledSet[name] = true
			}
			filtered := make([]tool.Tool, 0, len(promptBuilder.Tools))
			for _, t := range promptBuilder.Tools {
				if disabledSet[t.Name()] {
					continue
				}
				filtered = append(filtered, t)
			}
			promptBuilder.Tools = filtered
		}

		// Set per-turn dynamic fields on the cloned builder, then build.
		// These are appended at the end of the system prompt so the static prefix
		// remains cacheable by providers.
		promptBuilder.RelevantKnowledge = relevantKnowledge
		promptBuilder.RelevantTools = relevantTools

		// Per-turn MCP access filter: in platform mode, only show MCP groups
		// the current user's team/org has access to in the delegation catalog.
		mcpStores := store.MCPServerStoresFromContext(ctx)
		if mcpStores != nil {
			promptBuilder.MCPAccessFilter = func(serverName string) bool {
				return isMCPServerAccessible(ctx, serverName)
			}
		} else {
			promptBuilder.MCPAccessFilter = nil // personal mode — no filtering
		}

		instruction := promptBuilder.Build()

		// Capture session identity for use in AfterToolCallback closure.
		sessionID := ctx.Session().ID()
		sessionAppName := ctx.Session().AppName()
		sessionUserID := ctx.Session().UserID()

		// Per-call restore functions for credential and pending-secret
		// placeholder substitution. Keyed by FunctionCallID so parallel
		// tool calls (dispatched concurrently by ADK) don't clobber each
		// other's restore closures.
		var restoreFuncs sync.Map // map[string]func() — one entry per in-flight call

		// Create the AfterToolCallback for trace recording
		afterToolCallback := func(ctx tool.Context, t tool.Tool, input, output map[string]any, err error) (map[string]any, error) {
			// Restore credential + pending-secret placeholders for this
			// specific tool call, ensuring the session event retains
			// {{CREDENTIAL:...}} / <<<SECRET_N>>> tokens instead of real values.
			callID := ctx.FunctionCallID()
			if fn, ok := restoreFuncs.LoadAndDelete(callID); ok {
				fn.(func())()
			}

			// Redact credential values from all tool outputs before the LLM sees them.
			// resolve_credential now returns {{CREDENTIAL:...}} placeholders instead
			// of raw values, so no exemption is needed — placeholders pass through
			// the redactor unchanged.
			redactedOutput := output
			if c.Redactor != nil && output != nil {
				redactedOutput = c.Redactor.RedactMap(output)
			}

			// Strip image_base64 from tool results to prevent large binary
			// blobs from entering session history. The raw image bytes are
			// stashed in the ChatAgent's image queue for channel delivery.
			redactedOutput = c.extractAndStripImages(redactedOutput)

			// Capture file artifacts from write_file, edit_file,
			// browser_stop_recording, and run_drill (tutorial scene clips).
			// Paths are stashed for UI delivery.
			// Only capture on success — failed writes must not emit artifact events,
			// otherwise the live SSE pipeline and session-detail reconstruction diverge.
			if err == nil {
				switch t.Name() {
				case "write_file":
					if path, ok := input["file_path"].(string); ok && path != "" {
						c.CaptureFileArtifact(resolveAbsPath(path), t.Name())
					}
				case "edit_file":
					if path, ok := input["path"].(string); ok && path != "" {
						c.CaptureFileArtifact(resolveAbsPath(path), t.Name())
					}
				case "browser_stop_recording":
					// Path comes from the tool response (Manager chose the output file).
					if path, ok := redactedOutput["path"].(string); ok && path != "" {
						c.CaptureFileArtifact(resolveAbsPath(path), t.Name())
					}
				case "run_drill":
					captureRunDrillArtifacts(c.CaptureFileArtifact, redactedOutput)
				}
			}

			// Strip large flow output from run_flow results. The full output
			// is stashed for direct delivery to the user (via SSE or channel),
			// and replaced with a short pointer so the LLM doesn't try to
			// summarize or reproduce it. The output is already AI-generated
			// content that should not be re-processed by another LLM.
			if t.Name() == "run_flow" && redactedOutput != nil {
				redactedOutput = c.extractAndStripFlowOutput(redactedOutput)
			}

			trace.RecordStep(t.Name(), input, redactedOutput, err)

			// Attach sub-agent execution traces to the delegate_tasks step.
			// The delegate tool stashes child traces via SubAgentManager after
			// RunTasks completes; we pop them here and attach them so the memory
			// reflection system can see what sub-agents actually did.
			if t.Name() == "delegate_tasks" && c.SubAgentManager != nil {
				if childTraces := c.SubAgentManager.PopLastTraces(); len(childTraces) > 0 {
					trace.AttachSubAgentTraces(childTraces)
				}
			}

			// Plan step progression for delegate_tasks is handled by the
			// SubTaskProgress event handler (task_start / task_complete)
			// with name-based matching — not here. See chat_factory.go wiring.

			if c.DebugMode {
				status := "OK"
				if err != nil {
					status = fmt.Sprintf("ERROR: %v", err)
				}
				slog.Debug("tool call recorded", "component", "chat", "tool", t.Name(), "status", status)
			}

			// After save_credential succeeds, retroactively redact the current
			// session transcript. The redactor now knows the new secret values,
			// so user messages that contained raw secrets (submitted before the
			// credential was saved) can be scrubbed on disk and in memory.
			if t.Name() == "save_credential" && err == nil {
				redacted := false
				// File-based mode: use the pre-wired RedactSessionFunc.
				if c.RedactSessionFunc != nil {
					if redactErr := c.RedactSessionFunc(sessionAppName, sessionUserID, sessionID); redactErr != nil {
						if c.DebugMode {
							slog.Debug("retroactive session redaction failed", "component", "chat", "error", redactErr)
						}
					} else {
						redacted = true
					}
				}
				// Platform mode: resolve session store from context and redact.
				// The per-request session store is injected via InjectSessionService
				// and carries the correct tenant-scoped Ent store.
				if !redacted {
					if ss := store.SessionServiceFromContext(ctx); ss != nil && c.Redactor != nil {
						ss.SetRedactFunc(c.Redactor.Redact)
						if redactErr := ss.RedactSession(ctx, sessionAppName, sessionUserID, sessionID); redactErr != nil {
							if c.DebugMode {
								slog.Debug("retroactive session redaction (platform) failed", "component", "chat", "error", redactErr)
							}
						} else if c.DebugMode {
							slog.Debug("retroactive session redaction (platform) completed", "component", "chat")
						}
					}
				}
				if c.DebugMode && redacted {
					slog.Debug("retroactive session redaction completed", "component", "chat")
				}
			}

			return redactedOutput, err
		}

		// Build BeforeToolCallbacks — credential placeholder substitution.
		// When the LLM uses {{CREDENTIAL:name:field}} tokens in tool args
		// (from resolve_credential output), this callback replaces them with
		// real values just before the tool executes. The AfterToolCallback
		// restores the original placeholders so the session event (which
		// shares the same args map by reference) never persists real secrets.
		var beforeToolCallbacks []llmagent.BeforeToolCallback

		// authEventBuffer collects the authorization-prompt event emitted by the
		// code-mode gates. ADK may invoke BeforeToolCallbacks from a goroutine
		// where calling yield directly would panic, so the gate buffers the
		// event and the main event loop drains it (see the llmAgent.Run range).
		authEventBuffer := &callbackEventBuffer{}

		// Always register credential substitution callback. In platform mode,
		// the PG-backed credential store is injected into the context per-request
		// (even if the file-based store failed to open). The callback checks both
		// the context store and the agent-level fallback.
		{
			agentResolver := c.CredentialStore // may be nil if file-based store failed
			beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				// In platform mode, prefer the tenant-scoped PG credential store
				// injected into the context by chat_handlers.go. Fall back to the
				// agent-level file-based store for personal mode.
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

				// Use shell-safe env-var injection for shell_command tools to
				// prevent $, `, ! etc. in credential values from being expanded.
				var shellFields []string
				if t.Name() == "shell_command" || t.Name() == "process_write" {
					shellFields = []string{"command"}
				}

				// Register resolved credential values with the Redactor BEFORE
				// substitution so AfterToolCallback's RedactMap catches them.
				credentials.RegisterResolvedWithRedactor(args, resolver, c.Redactor)

				credRestore := credentials.SubstituteAndRestore(args, resolver, shellFields...)

				// After substitution, check if any placeholders remain unresolved.
				// Unresolved placeholders are left as literal text. This handles
				// the case where the LLM generates documentation or code that
				// describes the placeholder format (e.g., "{{CREDENTIAL:name:field}}")
				// without intending to use a real credential. If the LLM genuinely
				// meant to use a credential (via resolve_credential), the placeholder
				// will have been resolved above — only truly nonexistent credentials
				// remain, and downstream auth failures will surface naturally.
				if unresolved := credentials.UnresolvedCredentialNames(args); len(unresolved) > 0 {
					slog.Debug("credential placeholders remain unresolved (treating as literal text)",
						"component", "credentials", "tool", t.Name(), "unresolved", unresolved)
				}

				callID := ctx.FunctionCallID()
				// Chain with any existing restore (e.g. pending secrets added later).
				if prev, loaded := restoreFuncs.Load(callID); loaded {
					prevFn := prev.(func())
					restoreFuncs.Store(callID, func() { credRestore(); prevFn() })
				} else {
					restoreFuncs.Store(callID, credRestore)
				}
				return nil, nil // proceed with (possibly mutated) args
			})
		}

		// Resolve <<<SECRET_N>>> tokens in tool args to real values.
		// These tokens come from user messages sanitized by PendingVault.Extract().
		if c.PendingSecrets != nil {
			vault := c.PendingSecrets
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

		// Hard-block validate_drill / save_drill / blueprint_to_tutorial_drill for
		// mode:tutorial until the creator Approves a present_tutorial_blueprint card.
		beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
			blocked, result := CheckTutorialDrillToolGate(
				t.Name(), args, c.HasTutorialBlueprintApproved(sessionID),
			)
			if blocked {
				return result, nil
			}
			return nil, nil
		})

		// Plan-mode hard gate: when the turn is in plan mode, refuse any tool
		// that is not read-only (and refuse delegate_tasks, which could bypass
		// the gate via a sub-agent). Returning a result — rather than an error
		// that aborts the turn — lets the model self-correct and keep building
		// the plan. This is the runtime enforcement backing PlanModeSystemContext.
		//
		// graphPlan and planMode are mutually exclusive; when graphPlan is set
		// the phased gate below replaces the plan-mode gate.
		if planMode && !graphPlan {
			beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				name := t.Name()
				if name == "delegate_tasks" || !IsToolSafe(name) {
					return map[string]any{
						"status": "blocked_plan_mode",
						"error":  PlanModeBlockedMessage(name),
					}, nil
				}
				return nil, nil
			})
		}

		// Graph-Optimized Plan hard gate (code mode only): a phased state machine
		// determines the tool allow-list. The model advances phases via the
		// gplan_* transition tools. Transition tools and announce_plan/update_plan
		// are always allowed; every other tool must be permitted by the current
		// phase's allow-list (GraphPlanPhaseTools). Anything else — including any
		// non-SafeTools mutator and delegate_tasks outside the gap/plan phases —
		// returns a blocked_graph_plan result (not an error) so the model
		// self-corrects. This is a NO-CHANGES mode in every phase.
		if graphPlan {
			gpState := c.GetOrCreateGraphPlanState(sessionID)
			beforeToolCallbacks = append(beforeToolCallbacks, func(_ tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				name := t.Name()
				// Transition tools + plan recording are always allowed.
				if IsGraphPlanTransitionTool(name) || name == "announce_plan" || name == "update_plan" {
					return nil, nil
				}
				phase := gpState.Phase()
				if GraphPlanPhaseTools(phase)[name] {
					if limitMessage := gpState.ChargeExploration(name, args); limitMessage != "" {
						return map[string]any{
							"status": "blocked_graph_plan_budget",
							"phase":  string(phase),
							"error":  limitMessage,
						}, nil
					}
					return nil, nil
				}
				return map[string]any{
					"status": "blocked_graph_plan",
					"phase":  string(phase),
					"error":  GraphPlanBlockedMessage(name, phase),
				}, nil
			})
		}

		// Ask-mode hard gate (code mode only): in ask mode, refuse any tool that
		// is not read-only, plus delegate_tasks and announce_plan (which could
		// produce plans or bypass the gate via sub-agents). This is a pure
		// research/Q&A mode — no changes, no plans, no execution.
		if askMode && !planMode && !graphPlan {
			beforeToolCallbacks = append(beforeToolCallbacks, func(_ tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				name := t.Name()
				if name == "delegate_tasks" || name == "announce_plan" || !IsToolSafe(name) {
					return map[string]any{
						"status": "blocked_ask_mode",
						"error":  AskModeBlockedMessage(name),
					}, nil
				}
				return nil, nil
			})
		}

		// Approved-plan execution is normal mode for all implementation tools, but
		// the plan itself is immutable. Reject re-announcement before the tool body
		// can replace PlanState or rewrite PLAN.md. update_plan remains allowed.
		if approvedPlanExecution {
			var executionResearchMu sync.Mutex
			executionResearch := map[string]int{}
			beforeToolCallbacks = append(beforeToolCallbacks, func(_ tool.Context, t tool.Tool, _ map[string]any) (map[string]any, error) {
				name := t.Name()
				if approvedPlanExecutionToolBlocked(name) {
					return map[string]any{
						"status": "blocked_approved_plan_execution",
						"error":  ApprovedPlanExecutionBlockedMessage(),
					}, nil
				}

				kind := approvedExecutionResearchKind(name)
				if kind == "" {
					return nil, nil
				}
				limit := ApprovedExecutionMaxSourceReads
				switch kind {
				case "codegraph":
					limit = ApprovedExecutionMaxCodegraphCalls
				case "search":
					limit = ApprovedExecutionMaxSearchCalls
				}
				executionResearchMu.Lock()
				defer executionResearchMu.Unlock()
				if executionResearch[kind] >= limit {
					return map[string]any{
						"status": "blocked_approved_plan_research",
						"error":  ApprovedPlanExecutionResearchBlockedMessage(kind, limit),
					}, nil
				}
				executionResearch[kind]++
				return nil, nil
			})
		}

		// ── Code-mode authorization gates ──
		// Active in code mode (EnforceAuthorization) for both Normal and Ask mode
		// (planMode/graphPlan handled above). Two independent gates make
		// `astonish code` safe-by-default despite running tools directly on the host:
		//
		//  1. Folder-access gate — a tool touching a path outside the project
		//     working directory pauses for user authorization (once / session).
		//     Active in BOTH Normal and Ask mode (read-only tools can still
		//     access sensitive paths outside the project).
		//  2. Tool-execution gate — a not-whitelisted tool (anything outside
		//     agent.SafeTools) pauses for user authorization (once / all this
		//     iteration). Normal mode only — in Ask mode, non-safe tools are
		//     already refused by the ask-mode hard gate.
		//
		// Both mirror the flow engine's approval protocol: set awaiting_approval
		// state, buffer an approval event (drained + yielded by the main loop,
		// which then suspends the turn), and record the pending request on the
		// per-session policy so the resume handler at the top of Run can apply
		// the user's decision. AutoApprove (--yolo) skips these gates entirely.
		if c.EnforceAuthorization && !planMode && !graphPlan && !c.AutoApprove {
			authPolicy := c.GetOrCreateAuthPolicy(sessionID)

			emitAuthPrompt := func(kind, toolName, prompt string, options []string, paths []string, args map[string]any) map[string]any {
				// Only the callback that atomically claims the pending slot may emit
				// an approval. Parallel tool calls remain suspended behind that owner.
				if !authPolicy.TrySetPending(&PendingAuthorization{Kind: kind, Tool: toolName, Paths: paths}) {
					return map[string]any{
						"status": "pending_authorization",
						"info":   "Waiting for user authorization on a previous tool call.",
					}
				}
				delta := map[string]any{
					"awaiting_approval": true,
					"approval_tool":     toolName,
					"approval_options":  options,
					"approval_kind":     kind,
				}
				if len(paths) > 0 {
					delta["approval_paths"] = paths
				}
				if len(args) > 0 {
					delta["approval_args"] = args
				}
				authEventBuffer.append(&session.Event{
					LLMResponse: model.LLMResponse{
						Content: &genai.Content{
							Parts: []*genai.Part{{Text: prompt}},
							Role:  "model",
						},
					},
					Actions: session.EventActions{StateDelta: delta},
				})
				return map[string]any{
					"status": "pending_authorization",
					"info":   "Execution paused for user authorization. Do not retry until the user responds.",
				}
			}

			// Folder-access gate (checked first: an out-of-scope path is a
			// stronger constraint than tool category). Active in both Normal
			// and Ask mode — read-only tools can still target sensitive paths.
			beforeToolCallbacks = append(beforeToolCallbacks, func(_ tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				if authPolicy.Pending() != nil {
					return map[string]any{
						"status": "pending_authorization",
						"info":   "Waiting for user authorization on a previous tool call.",
					}, nil
				}
				outside := authPolicy.OutOfScopePaths(args)
				if len(outside) == 0 {
					// Allowed. Consume any one-shot ("Allow") path grant that
					// covered this access so a later access to the same
					// out-of-project path prompts again (an "Allow" is not an
					// "Always Allow"). No-op for paths inside the project root or
					// covered by a session grant.
					authPolicy.ConsumePathGrants(args)
					return nil, nil
				}
				prompt := c.approvalHelper.formatFolderApprovalRequest(t.Name(), outside, authPolicy.Root())
				return emitAuthPrompt("folder", t.Name(), prompt, FolderApprovalOptions(), outside, args), nil
			})

			// Tool-execution gate (Normal-mode whitelist = agent.SafeTools).
			// Skipped in Ask mode: non-safe tools are already refused by the
			// ask-mode hard gate above, making this prompt redundant.
			if !askMode {
				beforeToolCallbacks = append(beforeToolCallbacks, func(_ tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
					name := t.Name()
					if !RequiresToolAuthorization(name, false) {
						return nil, nil
					}
					if authPolicy.Pending() != nil {
						return map[string]any{
							"status": "pending_authorization",
							"info":   "Waiting for user authorization on a previous tool call.",
						}, nil
					}
					if authPolicy.ToolAuthorized(name) {
						return nil, nil // an active grant covers this execution
					}
					prompt := c.approvalHelper.formatToolApprovalRequest(name, args)
					return emitAuthPrompt("tool", name, prompt, ToolApprovalOptions(), nil, args), nil
				})
			}
		}

		// ── Auto-progress plan steps (before tool execution) ──
		// When a plan is active, mark the first pending step as "running"
		// when any non-delegate tool starts. This handles the initial
		// tool calls after announce_plan (e.g., shell_command for cloning).
		//
		// delegate_tasks steps are driven by sub-task lifecycle events
		// (task_start / task_complete) via name-based matching in the
		// SubTaskProgress handler — NOT by positional advancement here.
		beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
			c.activePlanMu.Lock()
			plan := c.activePlan
			c.activePlanMu.Unlock()

			if plan == nil || t.Name() == "announce_plan" || t.Name() == "delegate_tasks" {
				return nil, nil
			}

			// For any non-delegate tool, ensure a step is running.
			if started := plan.AdvanceOnToolStart(); started != "" {
				if c.SubTaskProgressCallback != nil {
					c.SubTaskProgressCallback(SubTaskProgressEvent{
						Type:       "plan_step_update",
						StepName:   started,
						StepStatus: "running",
					})
				}
			}

			return nil, nil
		})

		// Build BeforeModelCallbacks
		var beforeModelCallbacks []llmagent.BeforeModelCallback

		// Truncate oversized tool responses before they reach the model
		beforeModelCallbacks = append(beforeModelCallbacks, TruncateToolResponsesCallback())

		// Per-team tool restrictions: remove disabled tools from the LLM request.
		// This ensures the LLM cannot see or call tools the team admin has disabled.
		if disabledTools := store.DisabledToolsFromContext(ctx); len(disabledTools) > 0 {
			disabledSet := make(map[string]bool, len(disabledTools))
			for _, name := range disabledTools {
				disabledSet[name] = true
			}
			beforeModelCallbacks = append(beforeModelCallbacks, func(_ agent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
				for name := range disabledSet {
					delete(req.Tools, name)
				}
				return nil, nil
			})
		}

		// Dynamically inject relevant tools into each LLM request.
		// Fires on every LLM API call (including after tool results), adding
		// tools from hybrid search matches and search_tools discoveries.
		beforeModelCallbacks = append(beforeModelCallbacks, c.DynamicToolInjectionCallback())

		if c.Compactor != nil {
			beforeModelCallbacks = append(beforeModelCallbacks, c.Compactor.BeforeModelCallback())
		}

		// Resolve LLM: prefer per-request override from context (set by channel
		// manager or other per-message provider resolution), fall back to the
		// agent's default LLM set at construction time.
		effectiveLLM := LLMFromContext(ctx)
		if effectiveLLM == nil {
			effectiveLLM = c.LLM
			slog.Debug("[agent] No LLM override in context; using default c.LLM")
		} else {
			slog.Debug("[agent] Using context-injected LLM override")
		}

		// Create llmagent with static tools.
		// Use InstructionProvider (not Instruction) so ADK does NOT run
		// InjectSessionState on the system prompt. Chat prompts and tool
		// descriptions contain many {braces} (JSON examples, React snippets,
		// {{CREDENTIAL:...}} docs) that ADK would treat as required session
		// keys and fail with "state key does not exist".
		// Same pattern as node_llm.go for flow agents.
		instr := instruction
		llmAgent, err := llmagent.New(llmagent.Config{
			Name:  "chat",
			Model: effectiveLLM,
			InstructionProvider: func(_ agent.ReadonlyContext) (string, error) {
				return instr, nil
			},
			Tools:                c.Tools,
			Toolsets:             c.Toolsets,
			BeforeToolCallbacks:  beforeToolCallbacks,
			BeforeModelCallbacks: beforeModelCallbacks,
			AfterToolCallbacks: []llmagent.AfterToolCallback{
				afterToolCallback,
			},
			// Auto-inject ToolIndex-known tools that the LLM called before they
			// were loaded this turn (ADK 1.5 surfaces these as FunctionResponse
			// errors; the next LLM round gets the tool via dynamic injection).
			OnToolErrorCallbacks: []llmagent.OnToolErrorCallback{
				c.AutoInjectMissingToolCallback(),
			},
		})
		if err != nil {
			yield(nil, fmt.Errorf("failed to create chat llmagent: %w", err))
			return
		}

		// Run the llmagent with retry for transient errors (429, 502, 503, etc.)
		// Also handles legacy unknown-tool hard aborts (pre-ADK-1.5). Under ADK 1.5,
		// missing tools are FunctionResponses handled by OnToolErrorCallbacks
		// (AutoInjectMissingToolCallback); this branch is a safety net only.
		const maxRetries = 3
		const maxUnknownToolRetries = 2 // separate cap for tool name hallucinations
		toolCallCount := 0
		maxToolCalls := c.MaxToolCalls
		lastToolCallSeen := false
		anyTextYielded := false
		unknownToolRetries := 0
		contextOverflowRetried := false // only retry context overflow once

		// Track the last FunctionCall parts seen so we can build synthetic
		// error responses if ADK still hard-aborts on an unknown tool name.
		var lastFunctionCalls []*genai.FunctionCall

		for attempt := range maxRetries {

			retried := false
			for event, err := range llmAgent.Run(ctx) {
				if err != nil {
					// Check for retryable errors (rate limit, server overload)
					if llmerror.IsRetryable(err) && attempt < maxRetries-1 {
						wait := retryBackoff(attempt, err)
						if c.DebugMode {
							slog.Debug("retryable error", "component", "chat", "attempt", attempt+1, "maxRetries", maxRetries, "error", err, "wait", wait)
						}
						select {
						case <-time.After(wait):
						case <-ctx.Done():
							yield(nil, ctx.Err())
							return
						}
						retried = true
						break // break inner for-range, continue outer retry loop
					}

					// Check for unknown tool error (model hallucinated a tool name).
					// Instead of aborting the turn, inject a synthetic FunctionResponse
					// with a corrective error message so the LLM can self-correct.
					if isUnknownToolError(err) && unknownToolRetries < maxUnknownToolRetries && len(lastFunctionCalls) > 0 {
						unknownToolRetries++
						if c.DebugMode {
							slog.Debug("unknown tool error", "component", "chat", "retry", unknownToolRetries, "maxRetries", maxUnknownToolRetries, "error", err)
						}
						syntheticEvent := buildUnknownToolResponse(lastFunctionCalls, c.Tools, c.Toolsets)
						yield(syntheticEvent, nil) // runner persists this to session
						lastFunctionCalls = nil
						retried = true
						break // re-run llmAgent to let the LLM see the error and retry
					}

					// Check for context overflow (400 Bad Request). When the compactor
					// exists, force emergency compaction by temporarily setting threshold
					// to 0 so the BeforeModelCallback will compact on the next call.
					// This handles cases where the heuristic underestimates token usage
					// and the request exceeds the provider's context window.
					if llmerror.IsContextOverflow(err) && c.Compactor != nil && !contextOverflowRetried {
						contextOverflowRetried = true
						c.Compactor.ForceNextCompaction()
						slog.Info("context overflow detected, forcing emergency compaction and retrying",
							"component", "chat", "error", err)
						retried = true
						break // retry with forced compaction
					}

					// Non-retryable error, or retries exhausted
					if c.DebugMode && attempt > 0 {
						slog.Debug("error after retries", "component", "chat", "attempts", attempt, "error", err)
					}

					yield(nil, err)
					return
				}

				// Count tool calls, track FunctionCall parts, and capture text output
				if event.LLMResponse.Content != nil {
					// Collect FunctionCalls from this event for unknown-tool recovery
					var eventFunctionCalls []*genai.FunctionCall
					for _, p := range event.LLMResponse.Content.Parts {
						if p.FunctionCall != nil {
							eventFunctionCalls = append(eventFunctionCalls, p.FunctionCall)
						}
					}
					if len(eventFunctionCalls) > 0 {
						lastFunctionCalls = eventFunctionCalls
					}

					for _, p := range event.LLMResponse.Content.Parts {
						if p.FunctionCall != nil {
							toolCallCount++
							lastToolCallSeen = true
							if toolCallCount >= maxToolCalls {
								yield(&session.Event{
									LLMResponse: model.LLMResponse{
										Content: &genai.Content{
											Parts: []*genai.Part{{Text: fmt.Sprintf("\n\nI've been working on this for a while now (%d tool calls). Let me pause here — would you like me to continue?", maxToolCalls)}},
											Role:  "model",
										},
									},
								}, nil)
								goto postLoop
							}
						}
						// Track whether any user-facing text was produced
						if p.Text != "" && !p.Thought && p.FunctionCall == nil && p.FunctionResponse == nil {
							anyTextYielded = true
						}
						// Capture model output text for the execution trace.
						// Previously gated by lastToolCallSeen, but the reflector's
						// triviality gate uses FinalOutput length — so we must always
						// capture it, otherwise no-tool-call turns are incorrectly
						// classified as trivial and reflection is skipped.
						if p.Text != "" {
							trace.AppendOutput(p.Text)
						}
					}
				}

				// Check for approval pause
				if event.Actions.StateDelta != nil {
					if awaitingVal, ok := event.Actions.StateDelta["awaiting_approval"]; ok {
						if awaiting, ok := awaitingVal.(bool); ok && awaiting {
							// Yield the approval event and return -- the runner will
							// call us again with the user's response
							yield(event, nil)
							return
						}
					}
				}

				// Drain code-mode authorization prompts buffered by the gates.
				// A gate runs in an ADK goroutine and cannot yield directly, so
				// it buffers its approval event here. When one carries
				// awaiting_approval, yield it and suspend the turn (the runner
				// re-invokes Run with the user's decision, handled at the top of
				// Run). Drain BEFORE yielding the current tool-response event so
				// the approval prompt reaches the UI ahead of the placeholder
				// tool result.
				for _, buffered := range authEventBuffer.drain() {
					if !yield(buffered, nil) {
						return
					}
					if buffered.Actions.StateDelta != nil {
						if awaiting, ok := buffered.Actions.StateDelta["awaiting_approval"].(bool); ok && awaiting {
							return
						}
					}
				}

				// Yield event to the caller (console/web)
				if !yield(event, nil) {
					return
				}
			} // end inner for-range over llmAgent.Run(ctx)

			// If we didn't retry, the run completed successfully — break out
			if !retried {
				break
			}
			// Otherwise, the retry loop continues with the next attempt
		} // end outer retry loop

		// If the LLM made tool calls but never produced user-facing text,
		// yield a synthetic message so the consumer doesn't see silence.
		// This commonly happens after context compaction degrades the
		// conversation history, causing the LLM to call tools but skip
		// the final summary.
		if lastToolCallSeen && !anyTextYielded {
			yield(&session.Event{
				LLMResponse: model.LLMResponse{
					Content: &genai.Content{
						Parts: []*genai.Part{{Text: "I completed the requested actions. Let me know if you'd like me to elaborate on the results or if there's anything else I can help with."}},
						Role:  "model",
					},
				},
			}, nil)
		}

	postLoop:
		// Auto-complete any remaining plan steps at end of turn — but ONLY when
		// the model did not explicitly drive the plan via update_plan. If the
		// model tracked progress itself, its reported statuses are authoritative
		// and we must not fabricate a bulk "everything complete" sweep (which
		// previously made PLAN.md show all phases done regardless of reality).
		c.activePlanMu.Lock()
		endPlan := c.activePlan
		c.activePlanMu.Unlock()

		if endPlan != nil {
			// Only auto-complete when execution actually began this turn. A plan
			// that was merely announced (every step still pending — e.g. the
			// finalization turn in Plan mode, or an announce-only turn) must NOT
			// be swept to "complete": doing so would record a freshly announced
			// plan as fully done before any work is performed. In that case we
			// also keep the plan active so it carries into the next turn where
			// execution starts.
			started := endPlan.HasStartedSteps()
			if started {
				if !endPlan.IsManuallyTracked() {
					for _, stepName := range endPlan.CompleteAll() {
						if c.SubTaskProgressCallback != nil {
							c.SubTaskProgressCallback(SubTaskProgressEvent{
								Type:       "plan_step_update",
								StepName:   stepName,
								StepStatus: "complete",
							})
						}
					}
				}
				// Work happened this turn — the plan is done for this turn.
				c.activePlanMu.Lock()
				c.activePlan = nil
				c.activePlanMu.Unlock()
			}
		}

		// Finalize the trace
		trace.Finalize()

		if c.DebugMode {
			slog.Debug("postLoop reached", "component", "chat", "toolCallCount", trace.ToolCallCount())
			for i, step := range trace.Steps {
				slog.Debug("trace step", "component", "chat", "step", i+1, "tool", step.ToolName, "success", step.Success)
			}
		}

		// Post-task memory reflection: give the LLM one last chance to save
		// durable knowledge discovered during the turn. Runs asynchronously
		// so it does not block the runner's "done" SSE event. The reflection
		// is purely a background knowledge-save operation with no user-visible
		// output. Snapshot session events before launching the goroutine since
		// the invocation context (and its session) may become invalid after
		// the agent Run function returns.
		if c.PlatformReflector != nil {
			slog.Info("platform reflector triggered",
				"component", "platform-reflector",
				"toolCalls", trace.ToolCallCount(),
				"sessionID", sessionID)
			// Platform mode: the reflector needs the runner context (which has
			// MemoryStore, SessionID, UserID injected by ChatRunner). We derive
			// a new context from the invocation context (which IS the runner ctx)
			// with a timeout so it can't hang indefinitely.
			events := ctx.Session().Events()
			platformReflector := c.PlatformReflector
			// Propagate store values from invocation context to a detached ctx
			// so the goroutine survives after the ADK Run returns.
			reflectCtx := context.Background()
			reflectCtx = store.WithMemoryStore(reflectCtx, store.MemoryStoreFromContext(ctx))
			if scope := store.MemoryScopeFromContext(ctx); scope != "" {
				reflectCtx = store.WithMemoryScope(reflectCtx, scope)
			}
			reflectCtx = store.WithSessionID(reflectCtx, store.SessionIDFromContext(ctx))
			reflectCtx = store.WithUserID(reflectCtx, store.UserIDFromContext(ctx))
			// Propagate per-request LLM override so the reflector uses the
			// team's configured model (not the global singleton default).
			if llmOverride := LLMFromContext(ctx); llmOverride != nil {
				reflectCtx = WithLLM(reflectCtx, llmOverride)
			}
			go func() {
				tCtx, tCancel := context.WithTimeout(reflectCtx, 120*time.Second)
				defer tCancel()
				platformReflector.Reflect(tCtx, trace, events)
			}()
		}

		// Store the trace keyed by session ID for on-demand /distill
		c.traceMu.Lock()
		c.traceHistory[sessionID] = append(c.traceHistory[sessionID], trace)
		// Prune: keep at most 20 traces per session
		if len(c.traceHistory[sessionID]) > 20 {
			c.traceHistory[sessionID] = c.traceHistory[sessionID][len(c.traceHistory[sessionID])-20:]
		}
		c.traceMu.Unlock()
	}
}

// resolveAbsPath ensures a file path is absolute. If the path is relative,
// it is resolved against the current working directory. Used when capturing
// file artifacts to ensure consistent absolute paths for later retrieval.
func resolveAbsPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// captureRunDrillArtifacts registers tutorial scene MP4s and scene_manifest.json
// from a successful run_drill tool response onto the session Files list.
func captureRunDrillArtifacts(capture func(path, toolName string), output map[string]any) {
	if capture == nil || output == nil {
		return
	}
	for _, p := range extractRunDrillArtifactPaths(output) {
		capture(resolveAbsPath(p), "run_drill")
	}
}

// extractRunDrillArtifactPaths reads artifact_paths and/or manifest_path from
// a run_drill tool response map (JSON-decoded values may be []any or []string).
func extractRunDrillArtifactPaths(output map[string]any) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	switch v := output["artifact_paths"].(type) {
	case []string:
		for _, p := range v {
			add(p)
		}
	case []any:
		for _, item := range v {
			if p, ok := item.(string); ok {
				add(p)
			}
		}
	}
	if p, ok := output["manifest_path"].(string); ok {
		add(p)
	}
	return out
}
