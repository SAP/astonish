package agent

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/provider/llmerror"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// retryBackoff returns the duration to wait before retrying after a transient
// LLM error. It respects the Retry-After header if present, otherwise uses
// exponential backoff: 2s, 5s, 15s.
func retryBackoff(attempt int, err error) time.Duration {
	// Respect provider's Retry-After if available
	if ra := llmerror.GetRetryAfter(err); ra > 0 {
		// Cap at 60s to avoid absurd waits
		if ra > 60*time.Second {
			ra = 60 * time.Second
		}
		return ra
	}

	// Exponential backoff: 2s, 5s, 15s
	backoffs := []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second}
	if attempt < len(backoffs) {
		return backoffs[attempt]
	}
	return backoffs[len(backoffs)-1]
}

// redactEventText applies credential redaction to any LLM text parts in an event.
// This prevents the LLM from leaking secrets (e.g., from resolve_credential) in
// its text responses to the user. Tool call arguments are NOT affected.
func redactEventText(r *credentials.Redactor, event *session.Event) {
	if r == nil || event == nil {
		return
	}
	if event.LLMResponse.Content != nil {
		for i, part := range event.LLMResponse.Content.Parts {
			if part.Text != "" {
				event.LLMResponse.Content.Parts[i].Text = r.Redact(part.Text)
			}
		}
	}
}

// truncateQuery shortens a string for debug logging, appending "..." if truncated.
func truncateQuery(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// urlPattern matches http/https URLs in text.
var urlPattern = regexp.MustCompile(`https?://\S+`)

// timestampPattern matches the [YYYY-MM-DD HH:MM:SS UTC] prefix prepended
// by NewTimestampedUserContent. This prefix dilutes embedding queries
// (especially for short tool descriptions) and must be stripped.
var timestampPattern = regexp.MustCompile(`^\[?\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}\s+\w+\]?\s*`)

// buildKnowledgeQuery pre-processes a user query for semantic search.
// It strips timestamps and URLs (which dilute embedding semantics for models like MiniLM).
func buildKnowledgeQuery(userText string) string {
	// Strip leading timestamp prefix (e.g., "[2026-03-28 03:10:11 UTC]")
	q := timestampPattern.ReplaceAllString(userText, "")
	// Strip URLs — they carry no semantic meaning for the embedding model
	q = urlPattern.ReplaceAllString(q, "")
	// Collapse whitespace
	q = strings.Join(strings.Fields(q), " ")
	return q
}

// shortQueryThreshold is the maximum character length for a user message to
// be considered "short" — i.e., lacking enough context for accurate tool
// discovery. When the cleaned query is shorter than this, we augment it
// with the tail of the last LLM response to provide topical context.
// Examples: "looks good" (10), "use it" (6), "go for it" (9), "yes" (3).
const shortQueryThreshold = 40

// lastModelResponseTail extracts the trailing text from the last model
// response in the session event history. This provides topical context
// when the user's message is too short to be meaningful for search
// (e.g., "looks good", "use it"). The tail is where the LLM's question
// or action prompt typically lives (e.g., "shall I proceed with the
// fleet plan?"), which carries the semantic signal we need.
//
// Returns at most maxLen characters from the end of the combined model
// text parts. Returns "" if no model response is found.
func lastModelResponseTail(events session.Events, maxLen int) string {
	if events == nil {
		return ""
	}
	// Walk backwards to find the last model content event.
	// Model responses may be split across multiple streaming events
	// (each with a small text fragment). We want the last *complete*
	// response, which is the contiguous sequence of model events
	// ending at the most recent model event before the user's message.
	n := events.Len()
	var textParts []string
	foundModel := false
	for i := n - 1; i >= 0; i-- {
		ev := events.At(i)
		if ev.Author == "user" {
			if foundModel {
				break // we've collected all model parts before this user message
			}
			continue // skip user events before we find model events
		}
		if ev.Author != "chat" {
			if foundModel {
				break // non-model, non-user event after finding model text
			}
			continue
		}
		// It's a model event — extract text parts
		if ev.Content != nil {
			for _, part := range ev.Content.Parts {
				if part.Text != "" {
					textParts = append(textParts, part.Text)
					foundModel = true
				}
			}
		}
	}
	if len(textParts) == 0 {
		return ""
	}
	// textParts are in reverse order (we walked backwards), reverse them
	for i, j := 0, len(textParts)-1; i < j; i, j = i+1, j-1 {
		textParts[i], textParts[j] = textParts[j], textParts[i]
	}
	full := strings.Join(textParts, "")
	// Take the tail — the question/prompt is usually at the end
	if len(full) > maxLen {
		full = full[len(full)-maxLen:]
	}
	// Strip markdown formatting noise
	full = strings.ReplaceAll(full, "**", "")
	full = strings.ReplaceAll(full, "```", "")
	full = strings.ReplaceAll(full, "#", "")
	// Collapse whitespace
	full = strings.Join(strings.Fields(full), " ")
	return full
}

// extractFlowFromResults scans knowledge search results for flow documents.
// If a single high-confidence flow is found (score >= threshold), it loads the
// corresponding YAML, builds an execution plan, emits a user notification,
// and returns the remaining (non-flow) results plus the plan text.
// If multiple flows score above threshold, all are kept as regular knowledge
// so the LLM can present the options and ask the user which to use.
// isUnknownToolError checks whether an error from ADK is caused by the LLM
// calling a tool name that doesn't exist as a hard Run abort
// (fmt.Errorf("unknown tool: %q")). Under ADK 1.5 this path is largely obsolete:
// missing tools become FunctionResponse errors handled by OnToolErrorCallbacks
// (see AutoInjectMissingToolCallback). Retained as a safety net for older ADK
// behavior or any remaining hard-abort variants.
func isUnknownToolError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unknown tool:")
}

// buildUnknownToolResponse creates a synthetic FunctionResponse event for
// orphaned FunctionCall parts that referenced unknown tool names. The response
// includes the error message and a hint listing available tool names so the
// LLM can self-correct on retry.
func buildUnknownToolResponse(calls []*genai.FunctionCall, tools []tool.Tool, toolsets []tool.Toolset) *session.Event {
	// Collect available tool names for the hint
	var toolNames []string
	for _, t := range tools {
		toolNames = append(toolNames, t.Name())
	}
	// Include toolset names as a group hint (individual tools require context to resolve)
	for _, ts := range toolsets {
		toolNames = append(toolNames, ts.Name()+".*")
	}
	hint := strings.Join(toolNames, ", ")

	var parts []*genai.Part
	for _, fc := range calls {
		parts = append(parts, &genai.Part{
			FunctionResponse: &genai.FunctionResponse{
				ID:   fc.ID,
				Name: fc.Name,
				Response: map[string]any{
					"error": fmt.Sprintf(
						"Unknown tool %q. This tool does not exist. Available tools: %s. "+
							"Use the correct tool name and try again.",
						fc.Name, hint,
					),
				},
			},
		})
	}

	ev := session.NewEvent("unknown-tool-recovery")
	ev.Author = "chat"
	ev.LLMResponse = model.LLMResponse{
		Content: &genai.Content{
			Role:  "user",
			Parts: parts,
		},
	}
	return ev
}

// deduplicateSearchResults removes duplicate knowledge results that appear
// in both the guidance and general knowledge partitions. Deduplication is by
// path + snippet content. Earlier entries (guidance) take priority.
func deduplicateSearchResults(results []KnowledgeSearchResult) []KnowledgeSearchResult {
	seen := make(map[string]bool)
	var deduped []KnowledgeSearchResult
	for _, r := range results {
		// Key by path + first 100 chars of snippet to catch overlapping chunks
		snippet := r.Snippet
		if len(snippet) > 100 {
			snippet = snippet[:100]
		}
		k := r.Path + ":" + snippet
		if !seen[k] {
			seen[k] = true
			deduped = append(deduped, r)
		}
	}
	return deduped
}

// what knowledge was injected (or that none was found) for this turn.
//
// Because Content is nil, ADK's ContentsRequestProcessor skips the event when
// building the LLM request (it checks content == nil at contents_processor.go:71)
// and eventsToMessages() skips it in the Studio UI. The event is still persisted
// to the session .jsonl file, making it available for diagnostic inspection.
func yieldKnowledgeTrackingEvent(
	yield func(*session.Event, error) bool,
	query, bm25Query string,
	relevantKnowledge, executionPlan string,
	results []KnowledgeSearchResult,
) {
	// Build result summaries for the tracking payload.
	resultEntries := make([]map[string]any, 0, len(results))
	for _, r := range results {
		entry := map[string]any{
			"path":     r.Path,
			"score":    r.Score,
			"category": r.Category,
		}
		if r.ID != "" {
			entry["id"] = r.ID
		}
		if r.Scope != "" {
			entry["scope"] = r.Scope
		}
		if r.CreatedBy != "" {
			entry["created_by"] = r.CreatedBy
		}
		if r.CreatedAt != "" {
			entry["created_at"] = r.CreatedAt
		}
		if r.SessionID != "" {
			entry["session_id"] = r.SessionID
		}
		resultEntries = append(resultEntries, entry)
	}

	// Determine injection type.
	injectionType := "none"
	if executionPlan != "" && relevantKnowledge != "" {
		injectionType = "plan+knowledge"
	} else if executionPlan != "" {
		injectionType = "plan"
	} else if relevantKnowledge != "" {
		injectionType = "knowledge"
	}

	// Estimate token count (~4 chars per token).
	estimatedTokens := (len(relevantKnowledge) + len(executionPlan)) / 4

	yield(&session.Event{
		ID:        fmt.Sprintf("knowledge-%d", time.Now().UnixMilli()),
		Author:    "system",
		Timestamp: time.Now(),
		Actions: session.EventActions{
			StateDelta: map[string]any{
				"_knowledge_injection": map[string]any{
					"type":             injectionType,
					"query":            query,
					"bm25_query_len":   len(bm25Query),
					"results":          resultEntries,
					"result_count":     len(resultEntries),
					"estimated_tokens": estimatedTokens,
				},
			},
		},
	}, nil)
}

// yieldToolTrackingEvent emits a content-less session event that records
// what tools were discovered (or that none were found) for this turn.
// Mirrors yieldKnowledgeTrackingEvent for the tool index.
func yieldToolTrackingEvent(
	yield func(*session.Event, error) bool,
	query, relevantTools string,
	matches []ToolMatch,
) {
	matchEntries := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		matchEntries = append(matchEntries, map[string]any{
			"tool":  m.ToolName,
			"group": m.GroupName,
			"score": m.Score,
		})
	}

	estimatedTokens := len(relevantTools) / 4

	yield(&session.Event{
		ID:        fmt.Sprintf("tools-%d", time.Now().UnixMilli()),
		Author:    "system",
		Timestamp: time.Now(),
		Actions: session.EventActions{
			StateDelta: map[string]any{
				"_tool_injection": map[string]any{
					"query":            query,
					"matches":          matchEntries,
					"match_count":      len(matches),
					"estimated_tokens": estimatedTokens,
				},
			},
		},
	}, nil)
}
