package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mem "github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// PlatformReflector is the platform-mode equivalent of MemoryReflector.
// It runs a post-turn LLM call to save any missed durable knowledge to the
// PG team memory store, then runs memory extraction/consolidation to organize
// all session memories into well-grouped, deduplicated entries.
//
// Unlike the file-based MemoryReflector, this operates entirely through
// store.MemoryStore (PG-backed) and reads tenant context from the Go context.
type PlatformReflector struct {
	LLM       model.LLM
	DebugMode bool
	Merger    *MemoryMerger // Cross-session memory dedup/merge (initialized lazily)
}

// merger returns the MemoryMerger instance, creating it lazily from the same LLM.
func (r *PlatformReflector) merger() *MemoryMerger {
	if r.Merger == nil {
		r.Merger = &MemoryMerger{LLM: r.LLM, DebugMode: r.DebugMode}
	}
	return r.Merger
}

// effectiveLLM returns the per-request LLM from context if available,
// otherwise falls back to the reflector's default LLM. This ensures the
// reflector uses the team's configured model in platform mode.
func (r *PlatformReflector) effectiveLLM(ctx context.Context) model.LLM {
	if override := LLMFromContext(ctx); override != nil {
		return override
	}
	return r.LLM
}

// MemorySaveOrMergeFunc returns a store.MemorySaveOrMergeFunc that tools can use
// to save memory entries with cross-session dedup/merge. This is injected into
// the runner context so the memory_save tool can perform intelligent merging
// without needing direct access to the LLM.
func (r *PlatformReflector) MemorySaveOrMergeFunc() store.MemorySaveOrMergeFunc {
	return func(ctx context.Context, memStore store.MemoryStore, entry store.MemoryEntry) error {
		_, err := r.merger().SaveOrMerge(ctx, memStore, entry)
		return err
	}
}

// platformReflectionPrompt is the system instruction for the reflection LLM call
// in platform mode. Same logic as the file-based version but saves are simpler
// (no "kind" routing needed — all PG entries use category directly).
const platformReflectionPrompt = `You are a memory management assistant. Your ONLY job is to decide whether the conversation and task execution below contain durable knowledge worth saving to persistent memory.

CRITICAL: If the "ALREADY SAVED IN TEAM MEMORY" section below contains information about a topic, do NOT re-save that same information. Only save genuinely NEW facts not already covered.

Durable knowledge includes:
- Connection details, configuration parameters, or environment-specific information (hostnames, API base URLs, auth methods, credential names, ports)
- Discovered access recipes: the credential name/type that worked, the service catalog type, base URL, HTTP method/path, and required safe headers/placeholders
- Workarounds discovered after initial failures, but phrase them as conditional cautions separate from the shortest successful path
- Non-obvious file paths, API endpoints, configuration patterns
- Shell command quirks, syntax gotchas, tool-specific behaviors
- Integration details (auth flows, required headers, API schemas, credential names)

NOT durable knowledge — NEVER save, even if they appear in tool results:
- Lists of resources that change over time (VMs, containers, pods, databases, storage volumes, user accounts, running processes). These MUST be fetched live each time — saving them creates stale snapshots that will become wrong.
- Resource names/IDs and their current mapping (e.g., "ID 100 = my-server, ID 101 = my-database"). These change when resources are added or removed.
- Current status of any resource (running, stopped, healthy, degraded, etc.)
- Command outputs, log snippets, disk usage, memory usage, or any live metrics
- Trivial factual information with no environment-specific value
- Generic programming concepts unrelated to this specific environment
- Secret values (passwords, tokens, API keys) — NEVER include actual secret values

IMPORTANT: Connection details (hostnames, API base URLs, auth methods, credential names) ARE durable knowledge — save them even if the user provided them. However, the actual CONTENTS retrieved from those connections (resource lists, statuses, query results) are NOT durable and must NOT be saved.

Example: if a task discovers that Kubernetes clusters in SAP Converged Cloud QA-DE-1 are listed through Kubernikus rather than Magnum, save the access recipe: use the openstack-keystone Keystone token credential, discover/use the kubernikus public endpoint from the Keystone catalog, and call GET $KUBERNIKUS_URL/api/v1/clusters with X-Auth-Token: {{CREDENTIAL:openstack-keystone:token}}. Do NOT save the current cluster list, node counts, health, or phase.

If you find durable knowledge worth saving, call memory_save with:
- category: a descriptive scenario heading using "kind/topic" format (e.g., "infrastructure/Proxmox API", "tools/SSH Patterns", "workarounds/Docker DNS")
- content: concise bullet points. Put the positive successful path first. If you include failed assumptions, prefix them as "Caution:" so the scenario-card generator keeps them out of the recommended path.

If there is nothing worth saving, respond with exactly: "No durable knowledge to save."

You may call memory_save multiple times if there are distinct categories of knowledge.`

// Reflect analyzes the execution trace and conversation context, then optionally
// saves knowledge to the PG memory store. After saving, it runs extraction to
// consolidate all session memories.
//
// The ctx must carry store.MemoryStore and store.SessionID (injected by ChatRunner).
func (r *PlatformReflector) Reflect(ctx context.Context, trace *ExecutionTrace, events session.Events) {
	if r == nil || r.LLM == nil {
		return
	}

	memStore := store.MemoryStoreFromContext(ctx)
	sessionID := store.SessionIDFromContext(ctx)
	if memStore == nil || sessionID == "" {
		slog.Debug("platform reflector skipped: no memory store or session ID in context",
			"component", "platform-reflector")
		return
	}

	// Gate: skip trivial turns (same logic as file-based reflector)
	// A turn is trivial only if BOTH the user request AND the model output
	// are short AND no tool calls were made. This prevents skipping reflection
	// when the user states durable facts but the model just briefly acknowledges.
	totalToolCalls := countToolCallsRecursive(trace)
	if totalToolCalls == 0 && len(trace.FinalOutput) < minOutputForReflection && len(trace.UserRequest) < minOutputForReflection {
		slog.Debug("platform reflector skipped: trivial turn",
			"component", "platform-reflector",
			"toolCalls", 0,
			"outputLen", len(trace.FinalOutput))
		return
	}

	// Check if memory_save was already called during the turn
	memorySaveCalled := traceContainsMemorySave(trace)

	// Only run reflection if memory_save was NOT called (same as file-based)
	scenarioStatus := scenarioStatusFromTrace(trace)
	if !memorySaveCalled {
		r.runReflection(ctx, trace, events, memStore, sessionID, scenarioStatus)
	} else {
		slog.Debug("platform reflector: skipping reflection (memory_save already called), will run extraction",
			"component", "platform-reflector")
	}

	// After reflection (or skip), run extraction/consolidation on session memories.
	// Gate: only extract if there are 2+ memories in this session.
	r.runExtraction(ctx, memStore, sessionID, scenarioStatus)
	if scenarioStatus == mem.ScenarioCardStatusVerified {
		r.markScenarioCardsVerifiedForSession(ctx, memStore, sessionID)
	}
}

func scenarioStatusFromTrace(trace *ExecutionTrace) string {
	if countSuccessfulScenarioEvidenceToolCallsRecursive(trace) == 0 {
		return ""
	}
	return mem.ScenarioCardStatusVerified
}

func (r *PlatformReflector) markScenarioCardsVerifiedForSession(ctx context.Context, memStore store.MemoryStore, sessionID string) {
	memories, err := memStore.ListBySession(ctx, sessionID)
	if err != nil {
		slog.Debug("platform reflector: failed to list session cards for verification",
			"component", "platform-reflector",
			"sessionID", sessionID,
			"error", err)
		return
	}
	for _, entry := range memories {
		card, ok := mem.ParseScenarioCard(entry.Snippet)
		if !ok || card.Status == mem.ScenarioCardStatusVerified {
			continue
		}
		card.Status = mem.ScenarioCardStatusVerified
		if card.Confidence < 0.8 {
			card.Confidence = 0.8
		}
		if err := memStore.Update(ctx, entry.ID, mem.RenderScenarioCard(card), mem.ScenarioCardCategory); err != nil {
			slog.Debug("platform reflector: failed to mark scenario card verified",
				"component", "platform-reflector",
				"id", entry.ID,
				"error", err)
		}
	}
}

// runReflection performs the LLM call to identify and save missed knowledge.
func (r *PlatformReflector) runReflection(ctx context.Context, trace *ExecutionTrace, events session.Events, memStore store.MemoryStore, sessionID string, scenarioStatus string) {
	// Build rich context for the reflection LLM
	reflectionContext := buildReflectionContext(trace, events)

	// Inject a summary of existing team memories so the LLM knows what's
	// already saved and can avoid redundant saves.
	existingSummary := r.buildExistingMemorySummary(ctx, memStore)
	if existingSummary != "" {
		reflectionContext = existingSummary + "\n\n" + reflectionContext
	}

	if r.DebugMode {
		slog.Debug("running platform reflection",
			"component", "platform-reflector",
			"contextLen", len(reflectionContext))
	}

	// Build the LLM request with memory_save tool
	tempZero := float32(0.0)
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Parts: []*genai.Part{{Text: reflectionContext}},
				Role:  "user",
			},
		},
		Config: &genai.GenerateContentConfig{
			Temperature: &tempZero,
			SystemInstruction: &genai.Content{
				Parts: []*genai.Part{{Text: platformReflectionPrompt}},
			},
			Tools: []*genai.Tool{{
				FunctionDeclarations: []*genai.FunctionDeclaration{{
					Name:        "memory_save",
					Description: "Save durable facts to persistent team memory.",
					ParametersJsonSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"category": map[string]any{
								"type":        "string",
								"description": "A descriptive category heading using kind/topic format (e.g., infrastructure/Proxmox API, tools/SSH Patterns)",
							},
							"content": map[string]any{
								"type":        "string",
								"description": "The facts to save as concise bullet points",
							},
						},
						"required": []string{"category", "content"},
					},
				}},
			}},
		},
	}

	// Make the LLM call
	reflectCtx, reflectCancel := context.WithTimeout(ctx, 45*time.Second)
	defer reflectCancel()

	var lastResp *model.LLMResponse
	for resp, err := range r.effectiveLLM(ctx).GenerateContent(reflectCtx, req, false) {
		if err != nil {
			slog.Debug("platform reflection LLM error", "component", "platform-reflector", "error", err)
			return
		}
		lastResp = resp
	}

	if lastResp == nil || lastResp.Content == nil {
		slog.Debug("platform reflection: no response from LLM", "component", "platform-reflector")
		return
	}

	// Process the response — look for memory_save function calls
	saveCount := 0
	for _, part := range lastResp.Content.Parts {
		if part.FunctionCall != nil && part.FunctionCall.Name == "memory_save" {
			r.executePlatformSave(ctx, part.FunctionCall, memStore, sessionID, scenarioStatus)
			saveCount++
		}
	}

	if saveCount > 0 {
		slog.Debug("platform reflection saved entries", "component", "platform-reflector", "count", saveCount)
	} else {
		slog.Debug("platform reflection: model decided nothing worth saving", "component", "platform-reflector")
	}
}

// executePlatformSave saves a single memory entry to the PG team store.
// Uses cross-session dedup/merge: if a related memory already exists in any
// session, the content is merged via an LLM call rather than creating a duplicate.
func (r *PlatformReflector) executePlatformSave(ctx context.Context, fc *genai.FunctionCall, memStore store.MemoryStore, sessionID string, scenarioStatus string) {
	args := fc.Args
	if args == nil {
		return
	}

	category, _ := args["category"].(string)
	content, _ := args["content"].(string)

	if category == "" || content == "" {
		slog.Debug("platform reflection skipped save: missing category or content",
			"component", "platform-reflector")
		return
	}

	entry := store.MemoryEntry{
		Content:   content,
		Category:  category,
		SessionID: sessionID,
		CreatedBy: store.UserIDFromContext(ctx),
	}

	result, err := r.merger().SaveOrMergeWithStatus(ctx, memStore, entry, scenarioStatus)
	if err != nil {
		slog.Debug("platform reflection failed to save/merge",
			"component", "platform-reflector",
			"category", category,
			"error", err)
		return
	}

	slog.Debug("platform reflection save result",
		"component", "platform-reflector",
		"category", category,
		"action", result.Action,
		"sessionID", sessionID)
}

// runExtraction consolidates all memories from the current session.
// Only runs if there are 2+ memories (nothing to consolidate with 0 or 1).
func (r *PlatformReflector) runExtraction(ctx context.Context, memStore store.MemoryStore, sessionID string, scenarioStatus string) {
	memories, err := memStore.ListBySession(ctx, sessionID)
	if err != nil {
		slog.Debug("platform reflector extraction: failed to list session memories",
			"component", "platform-reflector",
			"error", err)
		return
	}

	if len(memories) < 2 {
		slog.Debug("platform reflector extraction: skipped (fewer than 2 memories)",
			"component", "platform-reflector",
			"count", len(memories))
		return
	}

	if r.DebugMode {
		slog.Debug("running platform memory extraction",
			"component", "platform-reflector",
			"sessionID", sessionID,
			"memoryCount", len(memories))
	}

	// Build the user prompt with all session memories
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Here are %d memories saved during session %s:\n\n", len(memories), sessionID))
	for i, m := range memories {
		sb.WriteString(fmt.Sprintf("--- Memory %d ---\n", i+1))
		sb.WriteString(fmt.Sprintf("Category: %s\n", m.Category))
		sb.WriteString(fmt.Sprintf("Content:\n%s\n\n", m.Snippet))
	}
	sb.WriteString("Please consolidate these memories into well-organized entries grouped by topic.")

	// Call LLM with consolidate_memories tool
	llmReq := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Parts: []*genai.Part{{Text: sb.String()}},
				Role:  "user",
			},
		},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: &genai.Content{
				Parts: []*genai.Part{{Text: platformExtractionSystemPrompt}},
			},
			Tools: []*genai.Tool{{
				FunctionDeclarations: []*genai.FunctionDeclaration{{
					Name:        "consolidate_memories",
					Description: "Output the consolidated memory entries.",
					ParametersJsonSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"entries": map[string]any{
								"type": "array",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"category": map[string]any{
											"type":        "string",
											"description": "Short category heading for this group",
										},
										"content": map[string]any{
											"type":        "string",
											"description": "Consolidated content as bullet points",
										},
									},
									"required": []string{"category", "content"},
								},
								"description": "Array of consolidated memory entries",
							},
						},
						"required": []string{"entries"},
					},
				}},
			}},
		},
	}

	extractCtx, extractCancel := context.WithTimeout(ctx, 60*time.Second)
	defer extractCancel()

	var lastResp *model.LLMResponse
	for resp, err := range r.effectiveLLM(ctx).GenerateContent(extractCtx, llmReq, false) {
		if err != nil {
			slog.Debug("platform extraction LLM error",
				"component", "platform-reflector",
				"error", err)
			return
		}
		lastResp = resp
	}

	if lastResp == nil || lastResp.Content == nil {
		slog.Debug("platform extraction: no response from LLM", "component", "platform-reflector")
		return
	}

	// Parse the consolidate_memories function call
	entries := parseConsolidateResponse(lastResp)
	if len(entries) == 0 {
		slog.Debug("platform extraction: LLM returned no entries", "component", "platform-reflector")
		return
	}

	// Apply: delete originals and insert consolidated entries
	r.applyExtraction(ctx, memStore, sessionID, memories, entries, scenarioStatus)
}

// extractionEntry represents a single consolidated memory entry.
type extractionEntry struct {
	Category string
	Content  string
}

// parseConsolidateResponse extracts entries from the LLM response.
func parseConsolidateResponse(resp *model.LLMResponse) []extractionEntry {
	if resp == nil || resp.Content == nil {
		return nil
	}
	for _, part := range resp.Content.Parts {
		if part.FunctionCall != nil && part.FunctionCall.Name == "consolidate_memories" {
			args := part.FunctionCall.Args
			if args == nil {
				continue
			}
			entriesRaw, ok := args["entries"]
			if !ok {
				continue
			}
			entriesSlice, ok := entriesRaw.([]any)
			if !ok {
				continue
			}
			var entries []extractionEntry
			for _, item := range entriesSlice {
				itemMap, ok := item.(map[string]any)
				if !ok {
					continue
				}
				cat, _ := itemMap["category"].(string)
				content, _ := itemMap["content"].(string)
				if cat != "" && content != "" {
					entries = append(entries, extractionEntry{
						Category: cat,
						Content:  content,
					})
				}
			}
			return entries
		}
	}
	return nil
}

// applyExtraction converts consolidated session memories into scenario cards and
// deletes the raw originals only after card upsert/discard processing succeeds.
func (r *PlatformReflector) applyExtraction(ctx context.Context, memStore store.MemoryStore, sessionID string, originals []store.MemorySearchResult, entries []extractionEntry, scenarioStatus string) {
	userID := store.UserIDFromContext(ctx)
	cardCount := 0
	discardCount := 0
	for _, entry := range entries {
		memEntry := store.MemoryEntry{
			Content:   entry.Content,
			Category:  entry.Category,
			SessionID: sessionID,
			CreatedBy: userID,
		}
		result, err := r.merger().SaveOrMergeWithStatus(ctx, memStore, memEntry, scenarioStatus)
		if err != nil {
			slog.Warn("platform extraction: failed to save scenario card",
				"component", "platform-reflector",
				"category", entry.Category,
				"error", err)
			return
		}
		if result.Action == "discarded" {
			discardCount++
		} else {
			cardCount++
		}
	}

	deleteCount := 0
	for _, m := range originals {
		if err := memStore.Delete(ctx, m.ID); err != nil {
			slog.Warn("platform extraction: failed to delete original",
				"component", "platform-reflector",
				"id", m.ID,
				"error", err)
		} else {
			deleteCount++
		}
	}

	slog.Debug("platform extraction completed",
		"component", "platform-reflector",
		"sessionID", sessionID,
		"deleted", deleteCount,
		"cards", cardCount,
		"discarded", discardCount,
		"originalCount", len(originals))
}

// buildExistingMemorySummary generates a concise summary of existing team
// memories to inject into the reflection prompt. This helps the reflection LLM
// avoid re-saving knowledge that's already stored.
func (r *PlatformReflector) buildExistingMemorySummary(ctx context.Context, memStore store.MemoryStore) string {
	// Fetch recent/all team memories (limit to a reasonable number)
	memories, err := memStore.List(ctx, "", 50, 0)
	if err != nil || len(memories) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("=== ALREADY SAVED IN TEAM MEMORY (do NOT re-save this information) ===\n")
	for _, m := range memories {
		sb.WriteString(fmt.Sprintf("• [%s] %s\n", m.Category, truncateForSummary(m.Snippet, 120)))
	}
	sb.WriteString("=== END OF EXISTING MEMORY ===\n")
	sb.WriteString("Only save GENUINELY NEW knowledge not already covered above.")

	return sb.String()
}

// truncateForSummary shortens a string to maxLen, appending "..." if truncated.
func truncateForSummary(s string, maxLen int) string {
	// Replace newlines with "; " for inline display
	s = strings.ReplaceAll(s, "\n", "; ")
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// extractionSystemPrompt is shared with the API handler — same consolidation instructions.
const platformExtractionSystemPrompt = `You are a memory consolidation assistant. You receive a list of memories that were saved during a single session. Your job is to reorganize and consolidate them into well-structured, deduplicated entries grouped by topic or subject.

Rules:
1. Group memories by operational scenario, not by every shared word. A prerequisite credential/tooling lesson may be its own card; a concrete workflow such as listing OpenStack Octavia load balancers should be its own focused card.
2. Deduplicate: if the same fact appears in multiple entries, keep it only once.
3. Keep each entry focused. Do not mix unrelated service endpoints into a narrow workflow card unless the scenario is explicitly service discovery.
4. Preserve factual content, but separate positive steps from failed assumptions. Prefix failures, wrong credentials, placeholder issues, and "do not use" notes as "Caution:" so they do not become the recommended path.
5. Each output entry must have a clear, descriptive category name.
6. Content should be concise bullet points with the shortest successful path first.
7. If memories are already well-organized (single topic, no duplicates), return them as-is.
8. NEVER add information that wasn't in the original memories.
9. NEVER save secret values (passwords, tokens, API keys).

Output format: Call consolidate_memories with an array of consolidated entries. Each entry has a "category" (short scenario heading) and "content" (bullet-point successful path plus any Caution bullets).`
