package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net"
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

func preProviderRetrievalError(operation string, timeout time.Duration, err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return fmt.Errorf("pre-provider %s timed out after %s: %w", operation, timeout, err)
	}
	return fmt.Errorf("pre-provider %s failed: %w", operation, err)
}

type preProviderRetrievalResult struct {
	guidance []KnowledgeSearchResult
	general  []KnowledgeSearchResult
	tools    []ToolMatch
}

func (c *ChatAgent) retrievePreProvider(ctx context.Context, lifecycle *lifecycleDiagnosticRecorder, semanticQuery, keywordQuery, toolQuery string) (preProviderRetrievalResult, error) {
	var result preProviderRetrievalResult
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type retrievalFailure struct {
		order     int
		operation string
		err       error
	}
	failures := make(chan retrievalFailure, 3)
	var wg sync.WaitGroup
	run := func(order int, operation, stage string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			finish := lifecycle.begin(stage)
			err := fn(workCtx)
			finish(err)
			if err != nil {
				failures <- retrievalFailure{order: order, operation: operation, err: err}
				cancel()
			}
		}()
	}
	searchTools := func(prepared PreparedKnowledgeRetrieval) {
		run(2, "tool index search", "tool_retrieval", func(searchCtx context.Context) error {
			var err error
			if prepared != nil && toolQuery == prepared.SemanticQuery() {
				embedding, identity := prepared.Embedding()
				if c.ToolIndex.CanUsePreparedEmbedding(identity, embedding) {
					result.tools, err = c.ToolIndex.SearchHybridPrepared(searchCtx, toolQuery, embedding, 8, 0.005)
					return err
				}
			}
			result.tools, err = c.ToolIndex.SearchHybrid(searchCtx, toolQuery, 8, 0.005)
			return err
		})
	}

	// A different tool query cannot reuse the memory embedding, so start its
	// single embedding and search while the memory query is being prepared.
	toolStarted := c.ToolIndex != nil && toolQuery != "" && toolQuery != semanticQuery
	if toolStarted {
		searchTools(nil)
	}

	var prepared PreparedKnowledgeRetrieval
	if c.KnowledgeRetrieval != nil && semanticQuery != "" {
		finishEmbedding := lifecycle.begin("memory_embedding")
		var err error
		prepared, err = c.KnowledgeRetrieval(workCtx, semanticQuery, keywordQuery)
		finishEmbedding(err)
		if err != nil {
			cancel()
			wg.Wait()
			return result, fmt.Errorf("memory embedding: %w", err)
		}
	}
	if prepared != nil {
		run(0, "guidance search", "guidance_retrieval", func(searchCtx context.Context) error {
			var err error
			result.guidance, err = prepared.Search(searchCtx, 3, 0.3, "guidance")
			return err
		})
		run(1, "knowledge search", "general_retrieval", func(searchCtx context.Context) error {
			var err error
			result.general, err = prepared.Search(searchCtx, 5, 0.3, "")
			return err
		})
	}
	if c.ToolIndex != nil && toolQuery != "" && !toolStarted {
		searchTools(prepared)
	}
	wg.Wait()
	close(failures)
	var first, firstCause *retrievalFailure
	for failure := range failures {
		if first == nil || failure.order < first.order {
			copy := failure
			first = &copy
		}
		if !errors.Is(failure.err, context.Canceled) && (firstCause == nil || failure.order < firstCause.order) {
			copy := failure
			firstCause = &copy
		}
	}
	if firstCause != nil {
		return result, fmt.Errorf("%s: %w", firstCause.operation, firstCause.err)
	}
	if first != nil {
		return result, fmt.Errorf("%s: %w", first.operation, first.err)
	}
	return result, nil
}

// Run implements the agent.Run interface for ADK.
// It is called by the ADK runner for each user message.
func (c *ChatAgent) Run(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		lifecycle := lifecycleRecorder(ctx, ctx.InvocationID())
		defer lifecycle.close()
		finishRequestPreparation := lifecycle.begin("request_session_preparation")

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

		cleanUserText := CleanUserText(userText)
		sessionID := ctx.Session().ID()
		finishRequestPreparation(nil)

		retrievalTimeout := c.preProviderRetrievalTimeout()
		retrievalCtx, cancelRetrieval := context.WithTimeout(ctx, retrievalTimeout)
		defer cancelRetrieval()

		// --- Phase A: Dynamic Execution ---
		trace := NewExecutionTrace(cleanUserText)

		// Per-turn dynamic content is retrieved under one shared deadline.
		var relevantKnowledge string
		var knowledgeTrackingQuery string
		var knowledgeTrackingBM25Query string
		var knowledgeTrackingResults []KnowledgeSearchResult
		var relevantTools string
		var toolMatches []ToolMatch

		searchQuery := buildKnowledgeQuery(cleanUserText)
		bm25Query := ""
		toolSearchQuery := searchQuery
		if len(searchQuery) >= 5 {
			knowledgeTrackingQuery = searchQuery
			if tail := lastModelResponseTail(ctx.Session().Events(), 300); tail != "" {
				bm25Query = tail + " " + searchQuery
				knowledgeTrackingBM25Query = bm25Query
			}
		}
		if len(toolSearchQuery) < shortQueryThreshold {
			if tail := lastModelResponseTail(ctx.Session().Events(), 200); tail != "" {
				toolSearchQuery = tail + " " + toolSearchQuery
			}
		}
		if len(searchQuery) < 5 {
			searchQuery = ""
		}
		if len(toolSearchQuery) < 5 {
			toolSearchQuery = ""
		}

		retrieved, err := c.retrievePreProvider(retrievalCtx, lifecycle, searchQuery, bm25Query, toolSearchQuery)
		if err != nil {
			operation := "retrieval"
			cause := err
			if prefix, rest, ok := strings.Cut(err.Error(), ": "); ok {
				operation = prefix
				cause = errors.Unwrap(err)
				if cause == nil {
					cause = errors.New(rest)
				}
			}
			yield(nil, preProviderRetrievalError(operation, retrievalTimeout, cause))
			return
		}

		allResults := append(append([]KnowledgeSearchResult(nil), retrieved.guidance...), retrieved.general...)
		allResults = deduplicateSearchResults(allResults)
		knowledgeTrackingResults = allResults
		if len(allResults) > 0 {
			var kb strings.Builder
			for _, result := range allResults {
				kb.WriteString(fmt.Sprintf("**%s** (relevance: %.0f%%)\n", result.Path, result.Score*100))
				kb.WriteString(result.Snippet)
				kb.WriteString("\n\n")
			}
			relevantKnowledge = EscapeCurlyPlaceholders(kb.String())
		}
		yieldKnowledgeTrackingEvent(yield, knowledgeTrackingQuery, knowledgeTrackingBM25Query, relevantKnowledge, "", knowledgeTrackingResults)

		toolMatches = FilterAccessibleToolMatches(ctx, retrieved.tools)
		if c.ToolIndex != nil && cleanUserText != "" {
			if mcpHits := MatchRequestMCPGroupsFromQuery(ctx, cleanUserText); len(mcpHits) > 0 {
				toolMatches = MergeToolMatches(toolMatches, FilterAccessibleToolMatches(ctx, mcpHits))
			}
			if mcpHits := MatchMCPGroupsFromQuery(c.ToolIndex, cleanUserText); len(mcpHits) > 0 {
				toolMatches = MergeToolMatches(toolMatches, FilterAccessibleToolMatches(ctx, mcpHits))
			}
		}
		if len(toolMatches) > 0 {
			relevantTools = FormatToolMatchesForPrompt(toolMatches)
		}
		yieldToolTrackingEvent(yield, toolSearchQuery, relevantTools, toolMatches)

		// Build per-turn context separately from the session-stable system prompt.
		finishPromptPersistence := lifecycle.begin("prompt_context_persistence")
		promptBuilder := c.SystemPrompt.Clone()
		if promptBuilder == nil {
			err := fmt.Errorf("SystemPrompt builder is nil")
			finishPromptPersistence(err)
			yield(nil, err)
			return
		}

		// Read per-turn overrides injected by callers via context.
		planMode := false
		graphPlan := false
		askMode := false
		approvedPlanExecution := false
		approvedPlanExecutionExplicit := false
		promptOverrides := PromptOverridesFromContext(ctx)
		turnOverrides := &PromptOverrides{
			ChannelHints:   promptBuilder.ChannelHints,
			SchedulerHints: promptBuilder.SchedulerHints,
			SessionContext: promptBuilder.SessionContext,
			SkillIndex:     promptBuilder.SkillIndex,
		}
		promptBuilder.ChannelHints = ""
		promptBuilder.SchedulerHints = ""
		promptBuilder.SessionContext = ""
		promptBuilder.SkillIndex = ""
		promptBuilder.RelevantKnowledge = ""
		promptBuilder.RelevantTools = ""
		if po := promptOverrides; po != nil {
			*turnOverrides = *po
			planMode = po.PlanMode
			graphPlan = po.GraphPlanMode
			askMode = po.AskMode
			approvedPlanExecution = po.ApprovedPlanExecution
			approvedPlanExecutionExplicit = po.ApprovedPlanExecutionExplicit
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

		instruction, promptStateEvent := stableSystemPrompt(ctx.Session().State(), promptBuilder.Build)
		if promptStateEvent != nil && !yield(promptStateEvent, nil) {
			finishPromptPersistence(context.Canceled)
			return
		}

		// Persist model-facing context so replay reconstructs identical bytes.
		if turnContext := buildTurnContextContent(turnOverrides, relevantTools, relevantKnowledge); turnContext != nil {
			if !yield(newTurnContextEvent(turnContext), nil) {
				finishPromptPersistence(context.Canceled)
				return
			}
		}
		finishPromptPersistence(nil)

		// Capture session identity for use in AfterToolCallback closure.
		sessionAppName := ctx.Session().AppName()
		sessionUserID := ctx.Session().UserID()

		// Per-call restore functions for credential and pending-secret
		// placeholder substitution. Keyed by FunctionCallID so parallel
		// tool calls (dispatched concurrently by ADK) don't clobber each
		// other's restore closures.
		var restoreFuncs sync.Map // map[string]func() — one entry per in-flight call

		// Create the AfterToolCallback for trace recording
		afterToolCallback := func(ctx tool.Context, t tool.Tool, input, output map[string]any, err error) (map[string]any, error) {
			t, input = c.effectiveToolCall(ctx, t, input)
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
				t, args = c.effectiveToolCall(ctx, t, args)
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
				_, args = c.effectiveToolCall(ctx, t, args)
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

		// Runtime authorization remains authoritative even when cache-stable
		// sessions retain a declaration that was disabled after session start.
		beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
			t, _ = c.effectiveToolCall(ctx, t, args)
			if isToolDisabled(ctx, t.Name()) {
				return map[string]any{
					"status": "blocked_disabled_tool",
					"error":  fmt.Sprintf("tool %q is disabled", t.Name()),
				}, nil
			}
			return nil, nil
		})

		// Hard-block validate_drill / save_drill / blueprint_to_tutorial_drill for
		// mode:tutorial until the creator Approves a present_tutorial_blueprint card.
		beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
			t, args = c.effectiveToolCall(ctx, t, args)
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
				t, _ = c.effectiveToolCall(ctx, t, args)
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
		// gplan_* transition tools. Transition tools and update_plan are always
		// allowed; announce_plan is allowed only in the PLAN phase via
		// GraphPlanPhaseTools. Anything else — including any non-SafeTools
		// mutator and delegate_tasks outside the gap/plan phases — returns a
		// blocked_graph_plan result (not an error) so the model self-corrects.
		// This is a NO-CHANGES mode in every phase.
		if graphPlan {
			gpState := c.GetOrCreateGraphPlanState(sessionID)
			beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				t, _ = c.effectiveToolCall(ctx, t, args)
				name := t.Name()
				// Transition tools + progress updates are always allowed.
				// announce_plan is phase-gated (PLAN phase only).
				if IsGraphPlanTransitionTool(name) || name == "update_plan" {
					return nil, nil
				}
				phase := gpState.Phase()
				if GraphPlanPhaseTools(phase)[name] {
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
			beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				t, _ = c.effectiveToolCall(ctx, t, args)
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

		// announce_plan exists only in Plan / Graph-Optimized Plan mode.
		// Hide it from the model in Normal/Ask and refuse it if it is still called.
		if !planMode && !graphPlan {
			beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				t, _ = c.effectiveToolCall(ctx, t, args)
				if t.Name() == "announce_plan" {
					return map[string]any{
						"status": "blocked_announce_plan_not_in_plan_mode",
						"error":  AnnouncePlanNotInPlanModeBlockedMessage(),
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
			beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				t, _ = c.effectiveToolCall(ctx, t, args)
				name := t.Name()
				if approvedPlanExecutionToolBlocked(name) {
					return map[string]any{
						"status": "blocked_approved_plan_execution",
						"error":  ApprovedPlanExecutionBlockedMessage(),
					}, nil
				}

				kind := approvedExecutionResearchKind(name)
				limit := approvedExecutionResearchLimit(kind)
				// The research clamp is armed only on the explicit execution
				// turn. Inferred continuation turns keep plan immutability
				// (announce_plan blocked above) but discover like Normal mode.
				if !approvedExecutionResearchApplies(approvedPlanExecutionExplicit) {
					return nil, nil
				}
				if kind == "" || limit <= 0 {
					return nil, nil
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
			beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				t, args = c.effectiveToolCall(ctx, t, args)
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
				beforeToolCallbacks = append(beforeToolCallbacks, func(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
					t, args = c.effectiveToolCall(ctx, t, args)
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
			t, _ = c.effectiveToolCall(ctx, t, args)
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

		// Truncate oversized tool responses before they reach the model.
		beforeModelCallbacks = append(beforeModelCallbacks, TruncateToolResponsesCallback())

		if c.Compactor != nil {
			compact := c.Compactor.BeforeModelCallback()
			beforeModelCallbacks = append(beforeModelCallbacks, func(callbackCtx agent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
				finishCompaction := lifecycle.begin("compaction")
				response, err := compact(callbackCtx, req)
				finishCompaction(err)
				return response, err
			})
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
		diagnosticsHook := cacheDiagnosticsHookFromContext(ctx)
		if diagnosticsHook == nil && store.DebugEnabledFromContext(ctx) && lifecycle != nil {
			diagnosticsHook = func(diagnostic CacheDiagnostic) {
				lifecycle.enqueue(cacheDiagnosticForStore(diagnostic))
			}
		}
		effectiveLLM = newDiagnosticLLM(effectiveLLM, diagnosticsHook, ctx.InvocationID(), credentials.RedactorFromContext(ctx))

		// Create llmagent with static tools.
		// Use InstructionProvider (not Instruction) so ADK does NOT run
		// InjectSessionState on the system prompt. Chat prompts and tool
		// descriptions contain many {braces} (JSON examples, React snippets,
		// {{CREDENTIAL:...}} docs) that ADK would treat as required session
		// keys and fail with "state key does not exist".
		// Same pattern as node_llm.go for flow agents.
		instr := instruction
		mainThreadTools := append([]tool.Tool(nil), c.Tools...)
		llmAgent, err := llmagent.New(llmagent.Config{
			Name:  "chat",
			Model: effectiveLLM,
			InstructionProvider: func(_ agent.ReadonlyContext) (string, error) {
				return instr, nil
			},
			Tools:                mainThreadTools,
			Toolsets:             c.Toolsets,
			BeforeToolCallbacks:  beforeToolCallbacks,
			BeforeModelCallbacks: beforeModelCallbacks,
			AfterToolCallbacks: []llmagent.AfterToolCallback{
				afterToolCallback,
			},
		})
		if err != nil {
			err = fmt.Errorf("failed to create chat llmagent: %w", err)
			finishProviderDispatch := lifecycle.begin("provider_dispatch")
			finishProviderDispatch(err)
			yield(nil, err)
			return
		}

		// Run the llmagent with retry for transient errors (429, 502, 503, etc.)
		// Also handles legacy unknown-tool hard aborts for transcript compatibility.
		const maxRetries = 3
		const maxUnknownToolRetries = 2 // separate cap for tool name hallucinations
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
							lastToolCallSeen = true
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
				// Keep the in-memory plan while work remains so update_plan on
				// the next Normal turn still has something to drive. Clear only
				// when every phase is terminal.
				if !endPlan.HasPendingSteps() {
					c.activePlanMu.Lock()
					c.activePlan = nil
					c.activePlanMu.Unlock()
				}
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
