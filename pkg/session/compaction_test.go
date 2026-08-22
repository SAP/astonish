package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func makeContent(role, text string) *genai.Content {
	return genai.NewContentFromText(text, genai.Role(role))
}

func makeFuncCallContent(role, funcName string, args map[string]any) *genai.Content {
	return &genai.Content{
		Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{
				Name: funcName,
				Args: args,
			},
		}},
		Role: role,
	}
}

func TestEstimateTokens_TextOnly(t *testing.T) {
	// 400 chars / 3 = ~133 tokens
	text := strings.Repeat("a", 400)
	contents := []*genai.Content{makeContent("user", text)}
	tokens := EstimateTokens(contents)
	if tokens != 133 {
		t.Errorf("EstimateTokens() = %d, want 133", tokens)
	}
}

func TestEstimateTokens_Empty(t *testing.T) {
	tokens := EstimateTokens(nil)
	if tokens != 0 {
		t.Errorf("EstimateTokens(nil) = %d, want 0", tokens)
	}

	tokens = EstimateTokens([]*genai.Content{})
	if tokens != 0 {
		t.Errorf("EstimateTokens([]) = %d, want 0", tokens)
	}
}

func TestEstimateTokens_WithFunctionCall(t *testing.T) {
	contents := []*genai.Content{
		makeContent("user", "hello"),
		makeFuncCallContent("model", "shell_command", map[string]any{
			"command": "ls -la",
		}),
	}
	tokens := EstimateTokens(contents)
	// "hello" = 1 token + func call overhead + args
	if tokens < 5 {
		t.Errorf("EstimateTokens() = %d, want >= 5", tokens)
	}
}

func TestEstimateTokens_NilContent(t *testing.T) {
	contents := []*genai.Content{nil, makeContent("user", "test"), nil}
	tokens := EstimateTokens(contents)
	if tokens != 1 { // "test" = 4 chars / 4 = 1
		t.Errorf("EstimateTokens() = %d, want 1", tokens)
	}
}

func TestNewCompactor(t *testing.T) {
	c := NewCompactor(200000)
	if c.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", c.ContextWindow)
	}
	if c.Threshold != 0.7 {
		t.Errorf("Threshold = %f, want 0.7", c.Threshold)
	}
	if c.PreserveRecent != 4 {
		t.Errorf("PreserveRecent = %d, want 4", c.PreserveRecent)
	}
}

func TestShouldCompact_UnderThreshold(t *testing.T) {
	c := NewCompactor(100000) // 100K tokens
	// Small conversation — way under threshold
	contents := []*genai.Content{
		makeContent("user", "Hello"),
		makeContent("model", "Hi there"),
	}
	if c.ShouldCompact(contents) {
		t.Error("ShouldCompact() = true for small conversation")
	}
}

func TestShouldCompact_OverThreshold(t *testing.T) {
	c := NewCompactor(100) // tiny window: 100 tokens
	c.Threshold = 0.5      // compact at 50%
	// Each message ~100 chars = ~25 tokens, 4 messages = ~100 tokens > 50
	contents := []*genai.Content{
		makeContent("user", strings.Repeat("x", 100)),
		makeContent("model", strings.Repeat("y", 100)),
		makeContent("user", strings.Repeat("z", 100)),
		makeContent("model", strings.Repeat("w", 100)),
	}
	if !c.ShouldCompact(contents) {
		t.Error("ShouldCompact() = false for large conversation in small window")
	}
}

func TestShouldCompact_ZeroWindow(t *testing.T) {
	c := NewCompactor(0) // disabled
	contents := []*genai.Content{
		makeContent("user", strings.Repeat("x", 100000)),
	}
	if c.ShouldCompact(contents) {
		t.Error("ShouldCompact() = true with zero context window")
	}
}

func TestCompactContents_FewMessages(t *testing.T) {
	c := NewCompactor(100)
	c.PreserveRecent = 4

	// Only 3 messages — nothing to compact (less than PreserveRecent)
	contents := []*genai.Content{
		makeContent("user", "a"),
		makeContent("model", "b"),
		makeContent("user", "c"),
	}

	result, err := c.CompactContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CompactContents() error = %v", err)
	}
	if len(result) != 3 {
		t.Errorf("len(result) = %d, want 3 (unchanged)", len(result))
	}
}

func TestCompactContents_WithTruncation(t *testing.T) {
	c := NewCompactor(100)
	c.PreserveRecent = 2
	// No LLM — will use truncation fallback

	contents := []*genai.Content{
		makeContent("user", "First question about setup"),
		makeContent("model", "Here's how to set things up"),
		makeContent("user", "Now let's talk about deployment"),
		makeContent("model", "Deployment steps are..."),
		makeContent("user", "What about monitoring?"),
		makeContent("model", "For monitoring, use..."),
	}

	result, err := c.CompactContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CompactContents() error = %v", err)
	}

	// Should be: 1 summary + 1 task anchor + 2 recent = 4
	// Task anchor = "Now let's talk about deployment" (last user text in old portion)
	if len(result) != 4 {
		t.Errorf("len(result) = %d, want 4", len(result))
	}

	// First message should be the summary (role "model" because task anchor is "user")
	if result[0].Role != "model" {
		t.Errorf("summary role = %q, want %q", result[0].Role, "model")
	}
	summaryText := result[0].Parts[0].Text
	if !strings.Contains(summaryText, "Context Summary") {
		t.Errorf("summary should contain 'Context Summary', got %q", summaryText[:min(80, len(summaryText))])
	}

	// Second message should be the task anchor
	if result[1].Role != "user" {
		t.Errorf("task anchor role = %q, want 'user'", result[1].Role)
	}
	if !strings.Contains(result[1].Parts[0].Text, "deployment") {
		t.Errorf("task anchor should mention 'deployment', got %q", result[1].Parts[0].Text)
	}
}

func TestCompactContents_WithLLM(t *testing.T) {
	c := NewCompactor(100)
	c.PreserveRecent = 2
	c.LLM = func(ctx context.Context, prompt string) (string, error) {
		return "CURRENT TASK: The user asked about Go testing.\nPROGRESS: Examples were provided.\nCOMPLETED: Go basics explained.", nil
	}

	contents := []*genai.Content{
		makeContent("user", "What is Go?"),
		makeContent("model", "Go is a programming language..."),
		makeContent("user", "Show me an example"),
		makeContent("model", "Here's an example: func main() {}"),
		makeContent("user", "Thanks, now let's do testing"),
		makeContent("model", "For testing in Go, use the testing package"),
	}

	result, err := c.CompactContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CompactContents() error = %v", err)
	}

	// 1 summary + 1 task anchor + 2 recent = 4
	if len(result) != 4 {
		t.Errorf("len(result) = %d, want 4", len(result))
	}

	summaryText := result[0].Parts[0].Text
	if !strings.Contains(summaryText, "CURRENT TASK") {
		t.Errorf("summary should contain LLM output with 'CURRENT TASK', got %q", summaryText[:min(80, len(summaryText))])
	}
}

func TestCompactContents_IncrementsCount(t *testing.T) {
	c := NewCompactor(100)
	c.PreserveRecent = 1

	contents := []*genai.Content{
		makeContent("user", "old1"),
		makeContent("model", "old2"),
		makeContent("user", "recent"),
	}

	if c.CompactionCount() != 0 {
		t.Errorf("initial CompactionCount() = %d, want 0", c.CompactionCount())
	}

	_, err := c.CompactContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CompactContents() error = %v", err)
	}

	if c.CompactionCount() != 1 {
		t.Errorf("CompactionCount() = %d, want 1", c.CompactionCount())
	}
}

func TestTokenUsage(t *testing.T) {
	c := NewCompactor(200000)
	contents := []*genai.Content{
		makeContent("user", strings.Repeat("x", 4000)), // ~1333 tokens at 3 chars/token
	}

	// Before ShouldCompact, usage is 0
	est, win := c.TokenUsage()
	if est != 0 {
		t.Errorf("initial estimated = %d, want 0", est)
	}
	if win != 200000 {
		t.Errorf("window = %d, want 200000", win)
	}

	// After ShouldCompact, usage is updated
	c.ShouldCompact(contents)
	est, win = c.TokenUsage()
	if est != 1333 {
		t.Errorf("estimated = %d, want 1333", est)
	}
	if win != 200000 {
		t.Errorf("window = %d, want 200000", win)
	}
}

func TestCompactContents_LLMFailureFallback(t *testing.T) {
	c := NewCompactor(100)
	c.PreserveRecent = 1
	c.LLM = func(ctx context.Context, prompt string) (string, error) {
		return "", context.DeadlineExceeded
	}

	contents := []*genai.Content{
		makeContent("user", "old message 1"),
		makeContent("model", "old response 1"),
		makeContent("user", "recent message"),
	}

	result, err := c.CompactContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CompactContents() error = %v (should fallback, not fail)", err)
	}

	// Should still compact using truncation fallback.
	// Result: summary(model) + task anchor(user) + recent(user) = 3 items
	// Task anchor is "old message 1" (last user text in old portion)
	if len(result) != 3 {
		t.Errorf("len(result) = %d, want 3", len(result))
	}
	summaryText := result[0].Parts[0].Text
	if !strings.Contains(summaryText, "Context Summary") {
		t.Errorf("fallback summary should contain 'Context Summary'")
	}
}

func TestForceNextCompaction(t *testing.T) {
	c := NewCompactor(200000) // 200K window

	// Small content that would not normally trigger compaction.
	contents := []*genai.Content{
		makeContent("user", "hello"),
	}

	// Should not compact normally.
	if c.ShouldCompact(contents) {
		t.Fatal("ShouldCompact should be false for tiny content")
	}

	// Force next compaction.
	c.ForceNextCompaction()

	// Now it should compact.
	if !c.ShouldCompact(contents) {
		t.Fatal("ShouldCompact should be true after ForceNextCompaction")
	}

	// One-shot: subsequent call should NOT compact again.
	if c.ShouldCompact(contents) {
		t.Fatal("ShouldCompact should be false after force flag consumed")
	}
}

func makeFuncResponseContent(role, funcName string, response map[string]any) *genai.Content {
	return &genai.Content{
		Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{
				Name:     funcName,
				Response: response,
			},
		}},
		Role: role,
	}
}

func TestAdjustSplitForToolPairs_NoAdjustment(t *testing.T) {
	// When the split point lands on a non-tool-response message, no adjustment.
	contents := []*genai.Content{
		makeContent("user", "old"),
		makeContent("model", "old response"),
		makeContent("user", "recent question"), // splitIdx = 2
		makeContent("model", "recent answer"),
	}
	got := adjustSplitForToolPairs(contents, 2)
	if got != 2 {
		t.Errorf("adjustSplitForToolPairs = %d, want 2 (no change)", got)
	}
}

func TestAdjustSplitForToolPairs_OrphanedToolResponse(t *testing.T) {
	// Split lands on a tool response — must include the preceding fn_call.
	contents := []*genai.Content{
		makeContent("user", "old"),                                                             // 0
		makeContent("model", "old response"),                                                   // 1
		makeFuncCallContent("model", "shell_command", map[string]any{"cmd": "ls"}),             // 2
		makeFuncResponseContent("user", "shell_command", map[string]any{"output": "file.txt"}), // 3 ← naive splitIdx
		makeContent("model", "Here are the files"),                                             // 4
		makeContent("user", "thanks"),                                                          // 5
	}
	// Naive split at 3 would orphan the tool response.
	got := adjustSplitForToolPairs(contents, 3)
	if got != 2 {
		t.Errorf("adjustSplitForToolPairs = %d, want 2 (moved back to include fn_call)", got)
	}
}

func TestAdjustSplitForToolPairs_MultipleOrphans(t *testing.T) {
	// Split lands on a tool response preceded by another tool response.
	contents := []*genai.Content{
		makeContent("user", "old"),                                  // 0
		makeFuncCallContent("model", "tool_a", map[string]any{}),    // 1
		makeFuncResponseContent("user", "tool_a", map[string]any{}), // 2
		makeFuncCallContent("model", "tool_b", map[string]any{}),    // 3
		makeFuncResponseContent("user", "tool_b", map[string]any{}), // 4 ← naive splitIdx
		makeContent("model", "done"),                                // 5
	}
	got := adjustSplitForToolPairs(contents, 4)
	// Should move back to 3 (tool_b fn_call, which is NOT a fn_response).
	if got != 3 {
		t.Errorf("adjustSplitForToolPairs = %d, want 3", got)
	}
}

func TestCompactContents_PreservesToolPairs(t *testing.T) {
	c := NewCompactor(100)
	c.PreserveRecent = 2 // Would naively split at len-2

	// 6 messages: the naive split at idx=4 lands on a tool response
	contents := []*genai.Content{
		makeContent("user", "what files exist?"),                                                // 0
		makeContent("model", "Let me check"),                                                    // 1
		makeFuncCallContent("model", "shell_command", map[string]any{"cmd": "ls"}),              // 2
		makeFuncResponseContent("user", "shell_command", map[string]any{"output": "a.go b.go"}), // 3
		makeFuncCallContent("model", "read_file", map[string]any{"path": "a.go"}),               // 4 ← naive splitIdx
		makeFuncResponseContent("user", "read_file", map[string]any{"content": "package main"}), // 5
	}

	result, err := c.CompactContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CompactContents error: %v", err)
	}

	// The preserved portion must start with the fn_call (idx 4), not a fn_response.
	// Since contents[4] is a fn_call (not fn_response), splitIdx stays at 4.
	// Result: summary + contents[4:] = 3 items
	if len(result) < 3 {
		t.Fatalf("len(result) = %d, want >= 3", len(result))
	}

	// Verify no orphaned tool responses: the second item (after summary)
	// should be the fn_call, not a fn_response.
	secondItem := result[1]
	if hasFunctionResponse(secondItem) {
		t.Errorf("first preserved message should not be a tool response")
	}
}

func TestCompactContents_OrphanedToolResponseFixed(t *testing.T) {
	c := NewCompactor(100)
	c.PreserveRecent = 3 // Naive split at len-3 = index 3

	// The naive split at idx=3 lands on a tool response (orphaned fn_call at idx=2)
	contents := []*genai.Content{
		makeContent("user", "question"),                                                  // 0
		makeContent("model", "thinking..."),                                              // 1
		makeFuncCallContent("model", "shell_command", map[string]any{"cmd": "ls"}),       // 2
		makeFuncResponseContent("user", "shell_command", map[string]any{"out": "files"}), // 3 ← naive splitIdx
		makeContent("model", "Here are the files"),                                       // 4
		makeContent("user", "thanks"),                                                    // 5
	}

	result, err := c.CompactContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CompactContents error: %v", err)
	}

	// Split moved back from 3 to 2, preserving the fn_call.
	// Old = [0,1], recent = [2,3,4,5]
	// Task anchor: "question" (last user text in old)
	// Result: summary + task_anchor + 4 preserved = 6 items
	if len(result) != 6 {
		t.Errorf("len(result) = %d, want 6 (summary + task_anchor + 4 preserved)", len(result))
	}

	// result[0] = summary(model), result[1] = task anchor(user), result[2] = fn_call(model)
	if len(result) >= 3 && !hasFunctionCall(result[2]) {
		t.Errorf("expected fn_call at result[2], got role=%q", result[2].Role)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestFindLastUserTextInstruction_Found(t *testing.T) {
	contents := []*genai.Content{
		makeContent("user", "What is Go?"),
		makeContent("model", "Go is a language"),
		makeContent("user", "Let's work on Security docs"),
		makeFuncCallContent("model", "read_file", map[string]any{"path": "security.md"}),
		makeFuncResponseContent("user", "read_file", map[string]any{"content": "# Security"}),
	}

	result := findLastUserTextInstruction(contents)
	if result == nil {
		t.Fatal("expected non-nil task anchor")
	}
	if result.Role != "user" {
		t.Errorf("role = %q, want 'user'", result.Role)
	}
	if !strings.Contains(result.Parts[0].Text, "Security docs") {
		t.Errorf("expected task anchor to contain 'Security docs', got %q", result.Parts[0].Text)
	}
	if !strings.Contains(result.Parts[0].Text, "[Active user instruction]") {
		t.Errorf("expected prefix '[Active user instruction]'")
	}
}

func TestFindLastUserTextInstruction_SkipsToolResponses(t *testing.T) {
	// All user messages are tool responses — should return nil
	contents := []*genai.Content{
		makeFuncCallContent("model", "read_file", map[string]any{"path": "a.go"}),
		makeFuncResponseContent("user", "read_file", map[string]any{"content": "data"}),
		makeFuncCallContent("model", "read_file", map[string]any{"path": "b.go"}),
		makeFuncResponseContent("user", "read_file", map[string]any{"content": "data"}),
	}

	result := findLastUserTextInstruction(contents)
	if result != nil {
		t.Errorf("expected nil for all-tool-response contents, got %v", result)
	}
}

func TestFindLastUserTextInstruction_Empty(t *testing.T) {
	result := findLastUserTextInstruction(nil)
	if result != nil {
		t.Errorf("expected nil for nil contents")
	}
	result = findLastUserTextInstruction([]*genai.Content{})
	if result != nil {
		t.Errorf("expected nil for empty contents")
	}
}

func TestCompactContents_TaskAnchorPreserved(t *testing.T) {
	c := NewCompactor(100)
	c.PreserveRecent = 2 // Only keeps last 2 messages (tool call + response)

	// Simulate a tool-heavy session: user instruction followed by many tool calls
	contents := []*genai.Content{
		makeContent("user", "Let's work on Security & Compliance"), // task instruction
		makeContent("model", "I'll read the security docs"),
		makeFuncCallContent("model", "read_file", map[string]any{"path": "security.md"}),
		makeFuncResponseContent("user", "read_file", map[string]any{"content": "# Security\nLong content here..."}),
		makeFuncCallContent("model", "read_file", map[string]any{"path": "auth.md"}),
		makeFuncResponseContent("user", "read_file", map[string]any{"content": "# Auth\nMore content..."}),
		makeFuncCallContent("model", "read_file", map[string]any{"path": "crypto.md"}),                // preserved[-2]
		makeFuncResponseContent("user", "read_file", map[string]any{"content": "# Crypto\nStuff..."}), // preserved[-1]
	}

	result, err := c.CompactContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CompactContents error: %v", err)
	}

	// The result should contain the task anchor text somewhere
	found := false
	for _, content := range result {
		for _, p := range content.Parts {
			if p != nil && strings.Contains(p.Text, "Security & Compliance") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("task anchor 'Security & Compliance' not found in compacted result")
		for i, c := range result {
			for _, p := range c.Parts {
				if p != nil && p.Text != "" {
					t.Logf("  result[%d] role=%s: %s", i, c.Role, truncateText(p.Text, 100))
				}
			}
		}
	}
}

func TestSummarize_CollapsesRepetitiveToolCalls(t *testing.T) {
	c := NewCompactor(200000)
	var capturedPrompt string
	c.LLM = func(ctx context.Context, prompt string) (string, error) {
		capturedPrompt = prompt
		return "CURRENT TASK: Testing\nPROGRESS: Done\nCOMPLETED: Nothing", nil
	}

	// Build contents with 10 consecutive read_file calls
	contents := []*genai.Content{
		makeContent("user", "Read all the security files"),
	}
	for i := 0; i < 10; i++ {
		contents = append(contents,
			makeFuncCallContent("model", "read_file", map[string]any{"path": fmt.Sprintf("file%d.md", i)}),
			makeFuncResponseContent("user", "read_file", map[string]any{"content": "data"}),
		)
	}

	_, err := c.summarize(context.Background(), contents)
	if err != nil {
		t.Fatalf("summarize error: %v", err)
	}

	// The prompt should NOT have 10 separate "Called tool: read_file" lines.
	// It should have a collapsed "read_file (×10 repeated calls)" entry.
	if strings.Contains(capturedPrompt, "×10") {
		// Good: collapsed
	} else {
		// Count occurrences of "Called tool: read_file"
		count := strings.Count(capturedPrompt, "Called tool: read_file")
		if count > 2 {
			t.Errorf("expected collapsed tool calls, but found %d separate 'Called tool: read_file' entries", count)
		}
	}

	// The prompt should contain the user's text instruction
	if !strings.Contains(capturedPrompt, "Read all the security files") {
		t.Error("summarizer prompt should contain user text")
	}

	// The prompt should ask about CURRENT TASK
	if !strings.Contains(capturedPrompt, "CURRENT TASK") {
		t.Error("summarizer prompt should ask about CURRENT TASK")
	}
}

func TestCompactContents_PlanFilePointer(t *testing.T) {
	contents := []*genai.Content{
		makeContent("user", "old message 1"),
		makeContent("model", "old response 1"),
		makeContent("user", "recent message"),
	}

	t.Run("pointer added when plan file exists", func(t *testing.T) {
		dir := t.TempDir()
		planPath := filepath.Join(dir, "sess.PLAN.md")
		if err := os.WriteFile(planPath, []byte("# Execution Plan\n\n**Goal:** Keep going\n"), 0644); err != nil {
			t.Fatalf("write plan file: %v", err)
		}
		c := NewCompactor(100)
		c.PreserveRecent = 1
		c.SetPlanFilePath(planPath)

		result, err := c.CompactContents(context.Background(), contents)
		if err != nil {
			t.Fatalf("CompactContents error: %v", err)
		}
		summary := result[0].Parts[0].Text
		if !strings.Contains(summary, "ACTIVE EXECUTION PLAN") {
			t.Errorf("expected plan pointer in summary, got:\n%s", summary)
		}
		if !strings.Contains(summary, planPath) {
			t.Errorf("expected plan path in summary, got:\n%s", summary)
		}
		if !strings.Contains(summary, "**Goal:** Keep going") {
			t.Errorf("expected inlined PLAN.md body in summary, got:\n%s", summary)
		}
	})

	t.Run("no pointer when plan file missing", func(t *testing.T) {
		c := NewCompactor(100)
		c.PreserveRecent = 1
		c.SetPlanFilePath(filepath.Join(t.TempDir(), "does-not-exist.PLAN.md"))

		result, err := c.CompactContents(context.Background(), contents)
		if err != nil {
			t.Fatalf("CompactContents error: %v", err)
		}
		if strings.Contains(result[0].Parts[0].Text, "ACTIVE EXECUTION PLAN") {
			t.Error("did not expect plan pointer when file is absent")
		}
	})

	t.Run("no pointer when path unset", func(t *testing.T) {
		c := NewCompactor(100)
		c.PreserveRecent = 1

		result, err := c.CompactContents(context.Background(), contents)
		if err != nil {
			t.Fatalf("CompactContents error: %v", err)
		}
		if strings.Contains(result[0].Parts[0].Text, "ACTIVE EXECUTION PLAN") {
			t.Error("did not expect plan pointer when path is unset")
		}
	})
}

// makeManyContents builds n simple text messages alternating user/model, large
// enough to exceed a small context window so compaction triggers.
func makeManyContents(n int) []*genai.Content {
	out := make([]*genai.Content, 0, n)
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "model"
		}
		// Pad text so token estimation is comfortably over threshold.
		out = append(out, makeContent(role, fmt.Sprintf("message %d: %s", i, strings.Repeat("x", 200))))
	}
	return out
}

// TestCompactor_OnCompactionHookFires verifies the OnCompaction hook is invoked
// with before/after token counts when a compaction actually reduces the context.
func TestCompactor_OnCompactionHookFires(t *testing.T) {
	c := NewCompactor(100) // tiny window so anything real is "over"
	c.PreserveRecent = 2
	// No LLM → truncation summary (deterministic, no network).

	var gotBefore, gotAfter int
	var calls int
	c.SetOnCompaction(func(before, after int) {
		calls++
		gotBefore, gotAfter = before, after
	})

	contents := makeManyContents(20)
	result, err := c.CompactContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CompactContents error: %v", err)
	}
	if len(result) >= len(contents) {
		t.Fatalf("expected fewer messages after compaction: %d -> %d", len(contents), len(result))
	}
	if calls != 1 {
		t.Fatalf("expected OnCompaction to fire once, got %d", calls)
	}
	if gotBefore <= gotAfter {
		t.Fatalf("expected before(%d) > after(%d) tokens", gotBefore, gotAfter)
	}
}

// TestCompactor_SummaryMemoized verifies that summarizing the same "old portion"
// twice calls the summarizer LLM only once — the fix for repeated, expensive
// re-summarization on every model call within a tool loop.
func TestCompactor_SummaryMemoized(t *testing.T) {
	c := NewCompactor(100)
	c.PreserveRecent = 2

	var llmCalls int
	c.LLM = func(ctx context.Context, prompt string) (string, error) {
		llmCalls++
		return "SUMMARY", nil
	}

	contents := makeManyContents(20)

	if _, err := c.CompactContents(context.Background(), contents); err != nil {
		t.Fatalf("first CompactContents: %v", err)
	}
	if _, err := c.CompactContents(context.Background(), contents); err != nil {
		t.Fatalf("second CompactContents: %v", err)
	}
	if llmCalls != 1 {
		t.Fatalf("expected summarizer LLM called once (memoized), got %d", llmCalls)
	}
}
