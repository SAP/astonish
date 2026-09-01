package session

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// LLMFunc is a function that calls an LLM with a prompt and returns text.
type LLMFunc func(ctx context.Context, prompt string) (string, error)

// Compactor handles context window management by estimating token usage
// and compacting old messages when the context gets too full.
type Compactor struct {
	mu sync.Mutex

	// ContextWindow is the total token budget for the model.
	ContextWindow int
	// Threshold is the fraction (0-1) at which compaction triggers.
	// Default 0.7 (compact when 70% full).
	Threshold float64
	// PreserveRecent is how many recent messages to keep uncompacted.
	// Default 4.
	PreserveRecent int
	// LLM is the summarization function. If nil, compaction uses truncation.
	LLM LLMFunc
	// DebugMode enables verbose logging.
	DebugMode bool

	// PlanFilePath, when set, is the path to the per-session PLAN.md. When a
	// plan file exists at compaction time, its contents are inlined into the
	// context summary so the exact phases and completion status survive
	// compaction without a follow-up read. The Compactor stays domain-agnostic:
	// it only reads the file as opaque text.
	PlanFilePath string

	// Strategy, when set, controls how the old conversation portion is
	// summarized. Code mode uses CodeStrategy for structured summaries;
	// platform mode uses GenericStrategy (or nil, which defaults to generic).
	Strategy CompactionStrategy

	// Stats tracking
	lastEstimatedTokens int
	compactionCount     int
	forceCompact        bool // one-shot flag: force compaction on next ShouldCompact call

	// summaryCache memoizes the summary of an "old portion" so repeated model
	// calls within a session do not re-run the (expensive) summarizer LLM on the
	// same history. ADK rebuilds req.Contents from the full session before every
	// model call, so without this the compactor would re-summarize on every step
	// of a tool loop — the dominant cause of "slow/frozen after compaction".
	summaryCacheKey string
	summaryCacheVal string

	// onCompaction, when set, is invoked after a compaction actually reduces the
	// contents, with the before/after estimated token counts. Used to surface
	// compaction to the UI (status + transcript notice + header refresh). Kept
	// domain-agnostic: it reports only token counts, never content.
	onCompaction func(beforeTokens, afterTokens int)

	// persistCompacted, when set, is invoked after a successful CompactContents
	// from BeforeModelCallback with the session id and the compacted contents.
	// Code mode uses this to archive the full history and rewrite the active
	// session so the next model step does not rebuild the pre-compaction
	// transcript (the 8k→190k thrash). Optional; nil = in-memory-only compact.
	persistCompacted func(ctx context.Context, sessionID string, compacted []*genai.Content) error
}

// SetOnCompaction registers a callback invoked after a real compaction, with the
// before/after estimated token counts. Thread-safe. Pass nil to clear.
func (c *Compactor) SetOnCompaction(fn func(beforeTokens, afterTokens int)) {
	c.mu.Lock()
	c.onCompaction = fn
	c.mu.Unlock()
}

// SetPersistCompacted registers a persistence hook for BeforeModelCallback
// compaction. Thread-safe. Pass nil to clear.
func (c *Compactor) SetPersistCompacted(fn func(ctx context.Context, sessionID string, compacted []*genai.Content) error) {
	c.mu.Lock()
	c.persistCompacted = fn
	c.mu.Unlock()
}

// NewCompactor creates a Compactor with the given context window size.
func NewCompactor(contextWindow int) *Compactor {
	return &Compactor{
		ContextWindow:  contextWindow,
		Threshold:      0.7,
		PreserveRecent: 4,
	}
}

// SetContextWindow updates the context window size (thread-safe).
// Used when the model is hot-swapped to a model with a different context window.
func (c *Compactor) SetContextWindow(contextWindow int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ContextWindow = contextWindow
}

// SetPlanFilePath updates the per-session PLAN.md path (thread-safe). Empty
// disables the plan pointer in compaction summaries.
func (c *Compactor) SetPlanFilePath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.PlanFilePath = path
}

// SetStrategy sets the compaction strategy (thread-safe). Pass nil to use
// the default GenericStrategy.
func (c *Compactor) SetStrategy(s CompactionStrategy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Strategy = s
}

// StrategyName returns the name of the active strategy ("code" or "platform").
func (c *Compactor) StrategyName() string {
	c.mu.Lock()
	s := c.Strategy
	c.mu.Unlock()
	if s != nil {
		return s.Name()
	}
	return "platform"
}

// EstimateTokens estimates the token count for a slice of Contents.
// Uses a conservative heuristic: ~3 characters per token. This ratio was
// calibrated against real sessions heavy in tool calls and structured JSON
// content where the actual measured ratio is ~3.05 chars/token. A lower ratio
// is intentionally conservative — it's better to compact slightly too early
// than to overflow the provider's context window and get 400 errors.
func EstimateTokens(contents []*genai.Content) int {
	total := 0
	for _, c := range contents {
		if c == nil {
			continue
		}
		for _, p := range c.Parts {
			if p == nil {
				continue
			}
			if p.Text != "" {
				// ~3 chars per token (conservative; covers code/JSON-heavy conversations)
				total += len(p.Text) / 3
			}
			if p.FunctionCall != nil {
				// Function call: name + JSON args, estimate generously
				total += 20 // name + overhead
				for k, v := range p.FunctionCall.Args {
					total += len(k)/3 + estimateValueTokens(v)
				}
			}
			if p.FunctionResponse != nil {
				// Function response: name + JSON response
				total += 20 // name + overhead
				for k, v := range p.FunctionResponse.Response {
					total += len(k)/3 + estimateValueTokens(v)
				}
			}
		}
	}
	return total
}

// estimateValueTokens estimates token count for a generic JSON value.
func estimateValueTokens(v any) int {
	switch val := v.(type) {
	case string:
		return len(val) / 3
	case map[string]any:
		total := 0
		for k, inner := range val {
			total += len(k)/3 + estimateValueTokens(inner)
		}
		return total
	case []any:
		total := 0
		for _, inner := range val {
			total += estimateValueTokens(inner)
		}
		return total
	default:
		return 2 // numbers, bools, null
	}
}

// ShouldCompact returns true if the given contents exceed the compaction threshold.
func (c *Compactor) ShouldCompact(contents []*genai.Content) bool {
	if c.ContextWindow <= 0 {
		return false
	}
	estimated := EstimateTokens(contents)
	c.mu.Lock()
	c.lastEstimatedTokens = estimated
	forced := c.forceCompact
	if forced {
		c.forceCompact = false // consume the one-shot flag
	}
	c.mu.Unlock()
	if forced {
		return true
	}
	threshold := int(float64(c.ContextWindow) * c.Threshold)
	return estimated > threshold
}

// TokenUsage returns the last estimated token count and the context window size.
func (c *Compactor) TokenUsage() (estimated, window int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastEstimatedTokens, c.ContextWindow
}

// CompactionCount returns how many times compaction has been performed.
func (c *Compactor) CompactionCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.compactionCount
}

// ForceNextCompaction forces the next ShouldCompact call to return true,
// regardless of token estimation. Used as an emergency measure when a 400
// context overflow is detected — the heuristic underestimated, so we force
// compaction on the retry attempt.
func (c *Compactor) ForceNextCompaction() {
	c.mu.Lock()
	c.forceCompact = true
	c.mu.Unlock()
}

// CompactContents compacts the contents by summarizing older messages and
// keeping the most recent ones intact. Returns the new contents slice.
//
// Strategy:
//  1. Split contents into [old | recent] at the PreserveRecent boundary
//  2. Adjust the split point to avoid orphaning tool responses (see below)
//  3. Extract the last user text instruction from old contents (task anchor)
//  4. Summarize the old portion into a single system message
//  5. Build result: summary + task anchor + recent messages
//
// Tool-pair safety: OpenAI/Anthropic/Bedrock APIs require every tool response
// message to have a matching tool_call in the preceding assistant message.
// If the naive split would place a tool response at the start of the preserved
// portion (orphaning its matching tool_call in the summarized section), we
// move the split backward to include the matching assistant message.
//
// Task anchor: In tool-heavy sessions, the preserved recent messages are often
// all tool call/response pairs with no task context. To prevent the model from
// losing track of what it's doing, we extract the most recent user TEXT message
// (not a tool response) from the old portion and insert it after the summary.
// This ensures the current task instruction always survives compaction.
func (c *Compactor) CompactContents(ctx context.Context, contents []*genai.Content) ([]*genai.Content, error) {
	if len(contents) <= c.PreserveRecent {
		return contents, nil // nothing to compact
	}

	preserve := c.PreserveRecent
	if preserve > len(contents) {
		preserve = len(contents)
	}

	splitIdx := len(contents) - preserve

	// Adjust split point to avoid orphaning tool responses.
	// Walk backward from splitIdx while the item at splitIdx has a FunctionResponse
	// whose matching FunctionCall is in the "old" portion. Include both the
	// call and the response in the preserved section.
	splitIdx = adjustSplitForToolPairs(contents, splitIdx)

	oldContents := contents[:splitIdx]
	recentContents := contents[splitIdx:]

	// Hidden per-turn context must remain model-facing but must never be folded
	// into visible summaries. Preserve the latest compacted context separately.
	turnContextAnchor := findLastTurnContext(oldContents)
	visibleOldContents := excludeTurnContext(oldContents)

	// Extract the last user text instruction from old contents.
	// This is the "task anchor" — it ensures the model retains the active task
	// even when all preserved recent messages are tool call/response pairs.
	taskAnchor := findLastUserTextInstruction(visibleOldContents)

	// Build summary from user-visible history only.
	summary, err := c.summarize(ctx, visibleOldContents)
	if err != nil {
		slog.Debug("compactor summarization failed, falling back to truncation", "component", "compactor", "error", err)
		// Fallback: just keep recent messages with a note
		summary = c.truncationSummary(visibleOldContents)
	}

	// If a per-session plan file exists, inline it so the model recovers the
	// exact phases and completion status that prose summarization cannot
	// faithfully preserve.
	if ptr := c.planFilePointer(); ptr != "" {
		summary += "\n\n" + ptr
	}

	// Determine summary role for proper role alternation.
	// The sequence will be: summary → [taskAnchor?] → [turnContextAnchor?] → recent.
	firstAfterSummary := recentContents
	if taskAnchor != nil {
		firstAfterSummary = []*genai.Content{taskAnchor}
	} else if turnContextAnchor != nil {
		firstAfterSummary = []*genai.Content{turnContextAnchor}
	}

	summaryRole := "user"
	if len(firstAfterSummary) > 0 && firstAfterSummary[0].Role == "user" {
		summaryRole = "model"
	}
	summaryContent := &genai.Content{
		Parts: []*genai.Part{{
			Text: fmt.Sprintf("[Context Summary — %d earlier messages compacted]\n\n%s",
				len(oldContents), summary),
		}},
		Role: summaryRole,
	}

	// Build compacted result: summary + [task anchor] + [hidden context] + recent.
	resultCap := 1 + len(recentContents)
	if taskAnchor != nil {
		resultCap++
	}
	if turnContextAnchor != nil {
		resultCap++
	}
	result := make([]*genai.Content, 0, resultCap)
	result = append(result, summaryContent)
	if taskAnchor != nil {
		// Only include the task anchor if it wouldn't create a role alternation
		// violation (consecutive same-role messages).
		if summaryContent.Role != taskAnchor.Role {
			result = append(result, taskAnchor)
		} else {
			// Merge task anchor text into summary to avoid role violation
			summaryContent.Parts[0].Text += "\n\n[Active user instruction]: " + taskAnchor.Parts[0].Text
		}
	}
	if turnContextAnchor != nil {
		result = append(result, turnContextAnchor)
	}
	result = append(result, recentContents...)

	c.mu.Lock()
	c.compactionCount++
	beforeTokens := EstimateTokens(contents)
	afterTokens := EstimateTokens(result)
	c.lastEstimatedTokens = afterTokens
	hook := c.onCompaction
	c.mu.Unlock()

	if c.DebugMode {
		slog.Debug("compacted messages", "component", "compactor",
			"before", len(contents), "after", len(result), "estimatedTokens", afterTokens,
			"taskAnchor", taskAnchor != nil)
	}

	// Notify observers (UI) only when compaction actually reduced the estimate.
	if hook != nil && afterTokens < beforeTokens {
		hook(beforeTokens, afterTokens)
	}

	return result, nil
}

// planFilePointer returns a pointer instruction if a per-session plan file is
// configured and present on disk, or "" otherwise. Thread-safe.
func (c *Compactor) planFilePointer() string {
	c.mu.Lock()
	path := c.PlanFilePath
	c.mu.Unlock()
	if path == "" {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	body := string(data)
	const maxPlanInlineBytes = 32 * 1024
	if len(body) > maxPlanInlineBytes {
		body = body[:maxPlanInlineBytes] + "\n… [PLAN.md truncated]"
	}
	return fmt.Sprintf(
		"[ACTIVE EXECUTION PLAN] An execution plan with per-phase completion status is persisted at %s. "+
			"Follow this inlined plan; do not reconstruct it or re-investigate confirmed files.\n\n%s",
		path, strings.TrimRight(body, "\n"),
	)
}

const turnContextPrefix = "[Astonish Per-Turn Context — not user-authored]\n"

func isTurnContextContent(content *genai.Content) bool {
	if content == nil {
		return false
	}
	for _, part := range content.Parts {
		if part != nil && strings.HasPrefix(part.Text, turnContextPrefix) {
			return true
		}
	}
	return false
}

func findLastTurnContext(contents []*genai.Content) *genai.Content {
	for i := len(contents) - 1; i >= 0; i-- {
		if isTurnContextContent(contents[i]) {
			return contents[i]
		}
	}
	return nil
}

func excludeTurnContext(contents []*genai.Content) []*genai.Content {
	visible := make([]*genai.Content, 0, len(contents))
	for _, content := range contents {
		if !isTurnContextContent(content) {
			visible = append(visible, content)
		}
	}
	return visible
}

// findLastUserTextInstruction scans backward through contents to find the most
// recent user message that contains meaningful text (not a FunctionResponse).
// Returns a Content with the user's instruction, prefixed for clarity.
// Returns nil if no suitable user text is found in the old portion.
//
// When the last user instruction is short (< 200 chars, likely a follow-up like
// "do it" or "let's start with X"), the function also looks for the most recent
// *substantive* user message (≥ 200 chars) and includes it as context. This
// prevents multi-message request flows from being lost — the short instruction
// alone may be meaningless without the preceding detailed description.
func findLastUserTextInstruction(contents []*genai.Content) *genai.Content {
	var lastInstruction string
	var lastSubstantive string
	lastInstructionIdx := -1

	for i := len(contents) - 1; i >= 0; i-- {
		c := contents[i]
		if c == nil || c.Role != "user" {
			continue
		}
		// Check that this message has meaningful text (not just a tool response)
		for _, p := range c.Parts {
			if p == nil {
				continue
			}
			if p.FunctionResponse != nil {
				break // this is a tool response, skip it
			}
			if p.Text != "" {
				if lastInstruction == "" {
					lastInstruction = p.Text
					lastInstructionIdx = i
				} else if lastSubstantive == "" && len(p.Text) >= 200 && i != lastInstructionIdx {
					// Found a substantive message that preceded the short instruction.
					lastSubstantive = p.Text
				}
				break // only look at the first text part per user message
			}
		}
		// Stop searching once we have both
		if lastInstruction != "" && lastSubstantive != "" {
			break
		}
	}

	if lastInstruction == "" {
		return nil
	}

	var text string
	if lastSubstantive != "" && len(lastInstruction) < 200 {
		// The last instruction is short — include the preceding substantive
		// message as context so the model understands what the short instruction
		// refers to. Cap the substantive text to avoid bloating the anchor.
		subText := lastSubstantive
		if len(subText) > 3000 {
			subText = subText[:3000] + "..."
		}
		text = "[Prior user context]: " + subText + "\n\n[Active user instruction]: " + lastInstruction
	} else {
		text = "[Active user instruction]: " + lastInstruction
	}

	return &genai.Content{
		Parts: []*genai.Part{{
			Text: text,
		}},
		Role: "user",
	}
}

// summarize uses the LLM to create a concise summary of old messages.
// adjustSplitForToolPairs moves splitIdx backward to ensure the preserved
// portion doesn't start with an orphaned tool response. An orphaned tool
// response is one whose matching FunctionCall is in contents[:splitIdx].
//
// The algorithm: while contents[splitIdx] has FunctionResponse parts, move
// splitIdx backward by 1. This includes the preceding message (which should
// be the matching FunctionCall). Repeat until the first preserved item is
// NOT a tool response.
//
// Safety cap: move back at most 8 extra positions to avoid degenerate cases
// where the entire tail is interleaved call/response pairs.
func adjustSplitForToolPairs(contents []*genai.Content, splitIdx int) int {
	const maxBacktrack = 8

	moved := 0
	for splitIdx > 0 && moved < maxBacktrack {
		if !hasFunctionResponse(contents[splitIdx]) {
			break // safe: first preserved item is not a tool response
		}
		splitIdx--
		moved++
	}
	return splitIdx
}

// hasFunctionResponse returns true if the Content has any FunctionResponse part.
func hasFunctionResponse(c *genai.Content) bool {
	if c == nil {
		return false
	}
	for _, p := range c.Parts {
		if p != nil && p.FunctionResponse != nil {
			return true
		}
	}
	return false
}

// hasFunctionCall returns true if the Content has any FunctionCall part.
func hasFunctionCall(c *genai.Content) bool {
	if c == nil {
		return false
	}
	for _, p := range c.Parts {
		if p != nil && p.FunctionCall != nil {
			return true
		}
	}
	return false
}

// summarize uses the LLM to create a concise summary of old messages.
func (c *Compactor) summarize(ctx context.Context, contents []*genai.Content) (string, error) {
	if c.LLM == nil {
		return c.truncationSummary(contents), nil
	}

	// Reuse a cached summary when the old portion is unchanged. ADK rebuilds the
	// request contents from the full session before every model call, so the
	// same "old portion" is summarized repeatedly within a tool loop; caching
	// avoids re-running the summarizer LLM each step (the main "slow" symptom).
	fp := summaryFingerprint(contents)
	c.mu.Lock()
	if fp != "" && fp == c.summaryCacheKey && c.summaryCacheVal != "" {
		cached := c.summaryCacheVal
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	// Build prompt using the configured strategy (or default generic).
	var prompt string
	c.mu.Lock()
	strategy := c.Strategy
	c.mu.Unlock()
	if strategy != nil {
		prompt = strategy.BuildSummarizationPrompt(contents)
	} else {
		prompt = (&GenericStrategy{}).BuildSummarizationPrompt(contents)
	}

	out, err := c.LLM(ctx, prompt)
	if err != nil {
		return "", err
	}
	if fp != "" {
		c.mu.Lock()
		c.summaryCacheKey = fp
		c.summaryCacheVal = out
		c.mu.Unlock()
	}
	return out, nil
}

// summaryFingerprint returns a cheap, stable key for the "old portion" of a
// conversation so identical re-compactions reuse the cached summary instead of
// re-invoking the summarizer LLM. It combines the message count, estimated
// tokens, and a small sample of the first/last text so unrelated histories do
// not collide.
func summaryFingerprint(contents []*genai.Content) string {
	if len(contents) == 0 {
		return ""
	}
	var firstText, lastText string
	for _, c := range contents {
		if c == nil {
			continue
		}
		for _, p := range c.Parts {
			if p != nil && p.Text != "" {
				if firstText == "" {
					firstText = p.Text
				}
				lastText = p.Text
			}
		}
	}
	sample := func(s string) string {
		if len(s) > 64 {
			return s[:64]
		}
		return s
	}
	return fmt.Sprintf("%d:%d:%s:%s", len(contents), EstimateTokens(contents), sample(firstText), sample(lastText))
}

// truncationSummary creates a basic summary without LLM, extracting key info.
func (c *Compactor) truncationSummary(contents []*genai.Content) string {
	var sb strings.Builder
	sb.WriteString("Previous conversation context:\n")

	messageCount := 0
	toolCallCount := 0

	for _, content := range contents {
		if content == nil {
			continue
		}
		for _, p := range content.Parts {
			if p == nil {
				continue
			}
			if p.Text != "" {
				messageCount++
				// Keep first and last text snippets
				if messageCount <= 2 || messageCount == len(contents) {
					text := truncateText(p.Text, 200)
					sb.WriteString(fmt.Sprintf("- [%s]: %s\n", content.Role, text))
				}
			}
			if p.FunctionCall != nil {
				toolCallCount++
			}
		}
	}

	if toolCallCount > 0 {
		sb.WriteString(fmt.Sprintf("\n(%d messages, %d tool calls compacted)\n", messageCount, toolCallCount))
	}

	return sb.String()
}

// truncateText shortens text to maxLen characters, appending "..." if truncated.
func truncateText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// BeforeModelCallback returns a callback suitable for llmagent.Config.BeforeModelCallbacks.
// It checks token usage and compacts the request contents if needed. When a
// PersistCompacted hook is set, the compacted contents are also written back to
// the session store so the next model step rebuilds from the compact history
// (not the full pre-compaction transcript).
func (c *Compactor) BeforeModelCallback() llmagent.BeforeModelCallback {
	return func(ctx adkagent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
		if req == nil || len(req.Contents) == 0 {
			return nil, nil // proceed normally
		}

		if !c.ShouldCompact(req.Contents) {
			return nil, nil // under threshold, proceed normally
		}

		if c.DebugMode {
			est := EstimateTokens(req.Contents)
			slog.Debug("compactor threshold exceeded, compacting",
				"component", "compactor", "tokens", est, "window", c.ContextWindow,
				"usage", fmt.Sprintf("%.0f%%", float64(est)/float64(c.ContextWindow)*100))
		}

		compacted, err := c.CompactContents(ctx, req.Contents)
		if err != nil {
			slog.Debug("compaction failed", "component", "compactor", "error", err)
			return nil, nil // proceed with original contents
		}

		// Persist so the next model step does not rebuild the full history.
		c.mu.Lock()
		persist := c.persistCompacted
		c.mu.Unlock()
		if persist != nil && ctx != nil {
			if sid := ctx.SessionID(); sid != "" {
				if pErr := persist(ctx, sid, compacted); pErr != nil {
					slog.Warn("failed to persist compaction into session",
						"component", "compactor", "session_id", sid, "error", pErr)
				}
			}
		}

		// Replace the request contents in place
		req.Contents = compacted
		return nil, nil // proceed with compacted contents
	}
}
