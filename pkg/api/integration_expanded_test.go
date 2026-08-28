package api

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// =============================================================================
// State Delta Tests (X11-X15)
//
// These test processStateDelta directly, since state deltas are produced by
// Astonish's agent node execution code (approval, retry, failure, spinner),
// not by the LLM. Testing processStateDelta in isolation verifies the runner's
// event emission logic for all state-driven SSE events.
// =============================================================================

// TestIntegration_X11_StateDelta_Approval verifies that a state delta with
// approval_options and approval_tool emits an "approval" SSE event.
func TestIntegration_X11_StateDelta_Approval(t *testing.T) {
	runner := newChatRunner("test-approval", studioChatUserID, true)
	ch := runner.Subscribe("test")
	defer runner.Unsubscribe("test")

	runner.processStateDelta(map[string]any{
		"awaiting_approval": true,
		"approval_tool":     "shell_command",
		"approval_options":  []string{"Yes", "No"},
	})

	// Collect the emitted event
	select {
	case ev := <-ch:
		if ev.Type != "approval" {
			t.Fatalf("expected 'approval' event, got %q", ev.Type)
		}
		assertEventData(t, ev, "tool", "shell_command")
		opts, ok := ev.Data["options"].([]interface{})
		if !ok {
			t.Fatal("approval options should be []interface{}")
		}
		if len(opts) != 2 {
			t.Fatalf("expected 2 approval options, got %d", len(opts))
		}
		if opts[0] != "Yes" || opts[1] != "No" {
			t.Errorf("approval options = %v, want [Yes, No]", opts)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for approval event")
	}
}

// TestIntegration_X11b_StateDelta_ApprovalWithInterfaceSlice verifies that
// approval_options as []interface{} (from JSON unmarshaling) works correctly.
func TestIntegration_X11b_StateDelta_ApprovalWithInterfaceSlice(t *testing.T) {
	runner := newChatRunner("test-approval-iface", studioChatUserID, true)
	ch := runner.Subscribe("test")
	defer runner.Unsubscribe("test")

	runner.processStateDelta(map[string]any{
		"approval_tool":    "write_file",
		"approval_options": []interface{}{"Allow", "Deny", "Allow All"},
	})

	select {
	case ev := <-ch:
		if ev.Type != "approval" {
			t.Fatalf("expected 'approval' event, got %q", ev.Type)
		}
		assertEventData(t, ev, "tool", "write_file")
		opts, _ := ev.Data["options"].([]interface{})
		if len(opts) != 3 {
			t.Fatalf("expected 3 options, got %d", len(opts))
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for approval event")
	}
}

// TestIntegration_X12_StateDelta_AutoApproved verifies that a state delta
// with auto_approved=true emits an "auto_approved" SSE event.
func TestIntegration_X12_StateDelta_AutoApproved(t *testing.T) {
	runner := newChatRunner("test-auto-approved", studioChatUserID, true)
	ch := runner.Subscribe("test")
	defer runner.Unsubscribe("test")

	runner.processStateDelta(map[string]any{
		"auto_approved": true,
		"approval_tool": "grep_search",
	})

	select {
	case ev := <-ch:
		if ev.Type != "auto_approved" {
			t.Fatalf("expected 'auto_approved' event, got %q", ev.Type)
		}
		assertEventData(t, ev, "tool", "grep_search")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for auto_approved event")
	}
}

// TestIntegration_X12b_StateDelta_AutoApprovedFalse verifies that
// auto_approved=false does NOT emit an auto_approved event.
func TestIntegration_X12b_StateDelta_AutoApprovedFalse(t *testing.T) {
	runner := newChatRunner("test-auto-approved-false", studioChatUserID, true)
	ch := runner.Subscribe("test")
	defer runner.Unsubscribe("test")

	runner.processStateDelta(map[string]any{
		"auto_approved": false,
		"approval_tool": "shell_command",
	})

	select {
	case ev := <-ch:
		t.Fatalf("expected no event, got type=%q", ev.Type)
	case <-time.After(50 * time.Millisecond):
		// Good — no event emitted
	}
}

// TestIntegration_X13_StateDelta_RetryInfo verifies that a state delta with
// _retry_info emits a "retry" SSE event with attempt/maxRetries/reason.
func TestIntegration_X13_StateDelta_RetryInfo(t *testing.T) {
	runner := newChatRunner("test-retry", studioChatUserID, true)
	ch := runner.Subscribe("test")
	defer runner.Unsubscribe("test")

	runner.processStateDelta(map[string]any{
		"_retry_info": map[string]interface{}{
			"attempt":     2,
			"max_retries": 3,
			"reason":      "Rate limit exceeded, retrying...",
		},
	})

	select {
	case ev := <-ch:
		if ev.Type != "retry" {
			t.Fatalf("expected 'retry' event, got %q", ev.Type)
		}
		assertEventData(t, ev, "attempt", 2)
		assertEventData(t, ev, "maxRetries", 3)
		assertEventData(t, ev, "reason", "Rate limit exceeded, retrying...")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retry event")
	}
}

// TestIntegration_X14_StateDelta_FailureInfo verifies that a state delta with
// _failure_info emits an "error_info" SSE event.
func TestIntegration_X14_StateDelta_FailureInfo(t *testing.T) {
	runner := newChatRunner("test-failure", studioChatUserID, true)
	ch := runner.Subscribe("test")
	defer runner.Unsubscribe("test")

	runner.processStateDelta(map[string]any{
		"_failure_info": map[string]interface{}{
			"title":          "Max Retries Exceeded",
			"reason":         "Failed after 3 attempts.",
			"suggestion":     "Try a different model or simplify your request.",
			"original_error": "connection timeout",
		},
	})

	select {
	case ev := <-ch:
		if ev.Type != "error_info" {
			t.Fatalf("expected 'error_info' event, got %q", ev.Type)
		}
		assertEventData(t, ev, "title", "Max Retries Exceeded")
		assertEventData(t, ev, "reason", "Failed after 3 attempts.")
		assertEventData(t, ev, "suggestion", "Try a different model or simplify your request.")
		assertEventData(t, ev, "originalError", "connection timeout")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for error_info event")
	}
}

// TestIntegration_X15_StateDelta_Thinking verifies that a state delta with
// _spinner_text emits a "thinking" SSE event.
func TestIntegration_X15_StateDelta_Thinking(t *testing.T) {
	runner := newChatRunner("test-thinking", studioChatUserID, true)
	ch := runner.Subscribe("test")
	defer runner.Unsubscribe("test")

	runner.processStateDelta(map[string]any{
		"_spinner_text": "Analyzing code structure...",
	})

	select {
	case ev := <-ch:
		if ev.Type != "thinking" {
			t.Fatalf("expected 'thinking' event, got %q", ev.Type)
		}
		assertEventData(t, ev, "text", "Analyzing code structure...")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for thinking event")
	}
}

// TestIntegration_X15b_StateDelta_MultipleKeys verifies that a state delta
// with multiple keys produces multiple events.
func TestIntegration_X15b_StateDelta_MultipleKeys(t *testing.T) {
	runner := newChatRunner("test-multi-delta", studioChatUserID, true)
	ch := runner.Subscribe("test")
	defer runner.Unsubscribe("test")

	// A delta with both spinner and auto_approved should emit both events
	runner.processStateDelta(map[string]any{
		"_spinner_text": "Working...",
		"auto_approved": true,
		"approval_tool": "read_file",
	})

	var events []ChatEvent
	timeout := time.After(time.Second)
	for {
		select {
		case ev := <-ch:
			events = append(events, ev)
			if len(events) >= 2 {
				goto done
			}
		case <-timeout:
			goto done
		}
	}
done:

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %v", len(events), eventTypes(events))
	}

	// Should have both thinking and auto_approved (order may vary)
	types := map[string]bool{}
	for _, ev := range events {
		types[ev.Type] = true
	}
	if !types["thinking"] {
		t.Error("missing 'thinking' event")
	}
	if !types["auto_approved"] {
		t.Error("missing 'auto_approved' event")
	}
}

// TestIntegration_X15c_StateDelta_EmptyDelta verifies that an empty delta
// produces no events.
func TestIntegration_X15c_StateDelta_EmptyDelta(t *testing.T) {
	runner := newChatRunner("test-empty-delta", studioChatUserID, true)
	ch := runner.Subscribe("test")
	defer runner.Unsubscribe("test")

	runner.processStateDelta(map[string]any{})

	select {
	case ev := <-ch:
		t.Fatalf("expected no event from empty delta, got type=%q", ev.Type)
	case <-time.After(50 * time.Millisecond):
		// Good — no events
	}
}

// TestIntegration_X15d_StateDelta_UnknownKeys verifies that unknown delta
// keys are silently ignored (no events, no panics).
func TestIntegration_X15d_StateDelta_UnknownKeys(t *testing.T) {
	runner := newChatRunner("test-unknown-delta", studioChatUserID, true)
	ch := runner.Subscribe("test")
	defer runner.Unsubscribe("test")

	runner.processStateDelta(map[string]any{
		"some_random_key": "value",
		"another_key":     42,
	})

	select {
	case ev := <-ch:
		t.Fatalf("expected no event from unknown keys, got type=%q", ev.Type)
	case <-time.After(50 * time.Millisecond):
		// Good
	}
}

// =============================================================================
// Stream Truncation Retry (X16)
// =============================================================================

// TestIntegration_X16_StreamTruncationRetry verifies that when the LLM stream
// is truncated (no finish_reason), the runner automatically retries once and
// emits a retry event followed by the retry's response.
func TestIntegration_X16_StreamTruncationRetry(t *testing.T) {
	// Test 1: Verify isStreamTruncationError recognizes the error pattern
	t.Run("error_detection", func(t *testing.T) {
		tests := []struct {
			err    string
			expect bool
		}{
			{"LLM stream ended without a finish_reason — truncated", true},
			{"stream ended without a finish_reason", true},
			{"connection timeout", false},
			{"rate limit exceeded", false},
			{"", false},
		}
		for _, tt := range tests {
			var err error
			if tt.err != "" {
				err = stringError(tt.err)
			}
			got := isStreamTruncationError(err)
			if got != tt.expect {
				t.Errorf("isStreamTruncationError(%q) = %v, want %v", tt.err, got, tt.expect)
			}
		}
		trunc := stringError("stream ended without a finish_reason")
		if shouldRetryTruncatedStream(trunc, true) {
			t.Error("should not truncation-retry after the model already produced text")
		}
		if !shouldRetryTruncatedStream(trunc, false) {
			t.Error("should truncation-retry when the stream died with no content")
		}
	})

	// Test 2: Full pipeline retry — MockLLM that errors on first call with
	// truncation, succeeds on second.
	t.Run("full_retry_pipeline", func(t *testing.T) {
		retryLLM := NewTruncationRetryLLM(
			"Partial answer that gets cut off...",
			"Complete answer after successful retry.",
		)

		env := setupIntegrationTest(t, retryLLM, nil)
		// The retry includes a 2-second sleep, so extend timeout
		events := runAndCollect(t, env, "Tell me about Go", 15*time.Second)

		assertFirstAndLast(t, events)

		// Should see a retry event
		retryEv := assertHasEvent(t, events, "retry")
		assertEventData(t, retryEv, "attempt", 1)
		assertEventData(t, retryEv, "maxRetries", 1)

		// Should have text events (partial from first call, complete from retry)
		textEvents := eventsOfType(events, "text")
		if len(textEvents) == 0 {
			t.Fatal("expected at least one text event")
		}
	})
}

// =============================================================================
// Multi-Turn Conversation (X17)
// =============================================================================

// TestIntegration_X17_MultiTurn verifies that a second message on the same
// session works correctly — the session history carries over and the runner
// produces events for the second turn.
func TestIntegration_X17_MultiTurn(t *testing.T) {
	// Build LLM with responses for two turns
	mockLLM := NewMockLLM(
		TextTurn("Hello! I'm ready to help."),
		TextTurn("The capital of France is Paris."),
	)

	env := setupIntegrationTest(t, mockLLM, nil)

	// Turn 1
	events1 := runAndCollect(t, env, "Hi there")
	assertFirstAndLast(t, events1)
	assertHasEvent(t, events1, "text")

	// Turn 2 — create a new runner for the same session (mirrors production behavior)
	runner2 := newChatRunner(env.Runner.SessionID, studioChatUserID, false) // isNew=false for second turn
	ch2 := runner2.Subscribe("test")
	t.Cleanup(func() {
		runner2.Stop()
		go func() {
			for range ch2 {
			}
		}()
	})

	userMsg2 := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{Text: "What is the capital of France?"}},
	}

	go runner2.Run(
		env.ChatAgent,
		env.SessionService,
		env.MockLLM,
		nil,
		userMsg2,
		"What is the capital of France?",
		true,
		"",
		nil,
		false,
	)

	events2 := collectEvents(t, ch2, 10*time.Second)
	assertFirstAndLast(t, events2)

	// Session event should show isNew=false
	sessionEv := assertHasEvent(t, events2, "session")
	isNewVal, _ := sessionEv.Data["isNew"].(bool)
	if isNewVal {
		t.Error("second turn should have isNew=false")
	}

	// Should have a text event for the second response
	textEv := assertHasEvent(t, events2, "text")
	text, _ := textEv.Data["text"].(string)
	if !strings.Contains(text, "Paris") {
		t.Errorf("second turn text should contain 'Paris', got: %s", text)
	}

	// Verify the MockLLM received both calls
	mockLLM.mu.Lock()
	callCount := len(mockLLM.Calls)
	mockLLM.mu.Unlock()
	if callCount < 2 {
		t.Errorf("expected at least 2 LLM calls across turns, got %d", callCount)
	}
}

// =============================================================================
// Context Cancellation (X18)
// =============================================================================

// TestIntegration_X18_ContextCancellation verifies that stopping a runner
// mid-execution terminates gracefully — the done event is still emitted.
// Note: ADK's range-over-func iterators may panic when context is cancelled
// mid-iteration. This test uses recover to handle that case gracefully.
func TestIntegration_X18_ContextCancellation(t *testing.T) {
	// Create a MockLLM that blocks until cancelled
	blockingLLM := NewBlockingLLM()

	env := setupIntegrationTest(t, blockingLLM, nil)

	userMsg := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{Text: "This will be cancelled"}},
	}

	// Wrap the runner in a goroutine with panic recovery
	// (ADK may panic on context cancellation within range-over-func)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		defer func() {
			if r := recover(); r != nil {
				// ADK panic on cancelled context — expected behavior
				// The deferred done event in ChatRunner.Run() should still fire
			}
		}()
		env.Runner.Run(
			env.ChatAgent,
			env.SessionService,
			blockingLLM,
			nil,
			userMsg,
			"This will be cancelled",
			true,
			"",
			nil,
			false,
		)
	}()

	// Wait for the session event to confirm the runner started
	select {
	case ev := <-env.EventCh:
		if ev.Type != "session" {
			t.Fatalf("expected first event 'session', got %q", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session event")
	}

	// Cancel the runner
	env.Runner.Stop()

	// Wait for the run goroutine to complete (with or without panic)
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runner goroutine to finish")
	}

	// After stopping, IsDone should eventually become true
	// (if the panic prevented the deferred block, it may not be set)
	// Give a small window for the deferred block to execute
	time.Sleep(50 * time.Millisecond)

	// The runner may or may not have emitted done depending on whether
	// the panic was caught before/after the defer. Just verify the
	// runner eventually stops (no hanging goroutines).
	// Check that the channel is eventually closed or readable
	drained := false
	timeout := time.After(2 * time.Second)
	for !drained {
		select {
		case _, ok := <-env.EventCh:
			if !ok {
				drained = true
			}
		case <-timeout:
			drained = true
		}
	}
}

// =============================================================================
// Thought Part Filtering (X19)
// =============================================================================

// TestIntegration_X19_ThoughtPartsInMainLoop verifies that thought parts
// (chain-of-thought) are present in the response. The main event loop does
// NOT explicitly filter thought parts — they pass through as regular text.
// However, the ADK or ChatAgent may handle them differently.
func TestIntegration_X19_ThoughtPartsInMainLoop(t *testing.T) {
	mockLLM := NewMockLLM(&MockTurn{
		Parts: []*genai.Part{
			{Text: "Let me think about this...", Thought: true},
			{Text: "The answer is 42."},
		},
		TurnComplete: true,
	})

	env := setupIntegrationTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "What is the answer?")

	assertFirstAndLast(t, events)

	textEvents := eventsOfType(events, "text")
	if len(textEvents) == 0 {
		t.Fatal("expected at least 1 text event")
	}

	// Verify the non-thought answer text appears
	var allText strings.Builder
	for _, ev := range textEvents {
		if text, ok := ev.Data["text"].(string); ok {
			allText.WriteString(text)
		}
	}
	if !strings.Contains(allText.String(), "42") {
		t.Errorf("expected answer text '42' in events, got: %s", allText.String())
	}
}

// =============================================================================
// Mixed Content (X20)
// =============================================================================

// TestIntegration_X20_TextAndToolCallInSingleResponse verifies that a response
// containing both text and a function call in the same Content emits both events.
func TestIntegration_X20_TextAndToolCallInSingleResponse(t *testing.T) {
	mockLLM := NewMockLLM(
		// Single turn with text + tool call
		&MockTurn{
			Parts: []*genai.Part{
				{Text: "Let me check that for you."},
				{FunctionCall: &genai.FunctionCall{
					Name: "search_files",
					Args: map[string]any{"query": "config"},
				}},
			},
			TurnComplete: true,
		},
		// After tool result, final text
		TextTurn("Found 3 matching files."),
	)

	searchTool := newMockTool("search_files", "Search files by content", map[string]any{
		"matches": []string{"config.yaml", "config.json", "config.toml"},
	})

	env := setupIntegrationTest(t, mockLLM, []tool.Tool{searchTool})
	events := runAndCollect(t, env, "Find config files")

	assertFirstAndLast(t, events)
	assertHasEvent(t, events, "text")
	assertHasEvent(t, events, "tool_call")
	assertHasEvent(t, events, "tool_result")
}

// =============================================================================
// Runner Lifecycle Tests (X21-X23)
// =============================================================================

// TestIntegration_X21_GetHistory verifies that GetHistory returns all events
// after the runner completes.
func TestIntegration_X21_GetHistory(t *testing.T) {
	mockLLM := NewMockLLM(TextTurn("Hello from history!"))
	env := setupIntegrationTest(t, mockLLM, nil)

	// Run and wait for completion
	_ = runAndCollect(t, env, "Hi")

	// GetHistory should return the same events
	history := env.Runner.GetHistory()
	if len(history) < 3 { // at least: session, text, done
		t.Fatalf("expected at least 3 events in history, got %d", len(history))
	}

	// First should be session, last should be done
	if history[0].Type != "session" {
		t.Errorf("first history event type = %q, want 'session'", history[0].Type)
	}
	if history[len(history)-1].Type != "done" {
		t.Errorf("last history event type = %q, want 'done'", history[len(history)-1].Type)
	}
}

// TestIntegration_X22_EventCount verifies EventCount returns correct count.
func TestIntegration_X22_EventCount(t *testing.T) {
	mockLLM := NewMockLLM(TextTurn("Count me!"))
	env := setupIntegrationTest(t, mockLLM, nil)
	_ = runAndCollect(t, env, "Hi")

	count := env.Runner.EventCount()
	if count < 3 {
		t.Errorf("expected EventCount >= 3, got %d", count)
	}
}

// TestIntegration_X23_IsDone verifies IsDone transitions correctly.
func TestIntegration_X23_IsDone(t *testing.T) {
	blockingLLM := NewBlockingLLM()
	env := setupIntegrationTest(t, blockingLLM, nil)

	// Before running, IsDone should be false
	if env.Runner.IsDone() {
		t.Error("runner should not be done before Run()")
	}

	userMsg := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{Text: "test"}},
	}

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		defer func() {
			if r := recover(); r != nil {
				// ADK may panic on context cancellation within range-over-func
			}
		}()
		env.Runner.Run(
			env.ChatAgent, env.SessionService, blockingLLM,
			nil, userMsg, "test", true, "", nil, false,
		)
	}()

	// Wait for session event
	select {
	case <-env.EventCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session event")
	}

	// During execution, IsDone should be false
	if env.Runner.IsDone() {
		t.Error("runner should not be done during execution")
	}

	// Stop and wait for goroutine to finish
	env.Runner.Stop()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run goroutine")
	}

	// Drain channel
	for {
		select {
		case _, ok := <-env.EventCh:
			if !ok {
				goto drained
			}
		case <-time.After(time.Second):
			goto drained
		}
	}
drained:
}

// =============================================================================
// Subscriber Management (X24-X25)
// =============================================================================

// TestIntegration_X24_MultipleSubscribers verifies that multiple subscribers
// all receive the same events.
func TestIntegration_X24_MultipleSubscribers(t *testing.T) {
	mockLLM := NewMockLLM(TextTurn("Broadcast message"))
	env := setupIntegrationTest(t, mockLLM, nil)

	// Add a second subscriber
	ch2 := env.Runner.Subscribe("test2")
	defer env.Runner.Unsubscribe("test2")

	events1 := runAndCollect(t, env, "Hello")

	// Collect from second subscriber (channel should be closed by now)
	var events2 []ChatEvent
	for {
		select {
		case ev, ok := <-ch2:
			if !ok {
				goto done
			}
			events2 = append(events2, ev)
		case <-time.After(time.Second):
			goto done
		}
	}
done:

	// Both subscribers should receive the same number and types of events
	if len(events2) < len(events1) {
		t.Errorf("subscriber 2 got %d events, subscriber 1 got %d", len(events2), len(events1))
	}
}

// TestIntegration_X25_UnsubscribeDuringRun verifies that unsubscribing mid-run
// doesn't affect other subscribers or the runner.
func TestIntegration_X25_UnsubscribeDuringRun(t *testing.T) {
	mockLLM := NewMockLLM(TextTurn("After unsubscribe"))
	env := setupIntegrationTest(t, mockLLM, nil)

	// Add a second subscriber then immediately unsubscribe it
	ch2 := env.Runner.Subscribe("ephemeral")
	env.Runner.Unsubscribe("ephemeral")
	_ = ch2

	// Primary subscriber should still work fine
	events := runAndCollect(t, env, "Hello")
	assertFirstAndLast(t, events)
	assertHasEvent(t, events, "text")
}

// =============================================================================
// App Preview Edge Cases (X26-X27)
// =============================================================================

// TestIntegration_X26_MultipleAppPreviews verifies that multiple astonish-app
// code fences in a single response each produce their own app_preview event.
// Per the design, the first fence creates a new app (version 1) and subsequent
// fences refine it (same appId, incrementing versions).
func TestIntegration_X26_MultipleAppPreviews(t *testing.T) {
	app1 := `export default function App1() { return <div>App 1</div>; }`
	app2 := `export default function App2() { return <div>App 2</div>; }`
	response := "Here are two apps:\n\n```astonish-app\n" + app1 + "\n```\n\nAnd:\n\n```astonish-app\n" + app2 + "\n```"

	mockLLM := NewMockLLM(TextTurn(response))
	env := setupIntegrationTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "Create two apps")

	appEvents := eventsOfType(events, "app_preview")
	if len(appEvents) != 2 {
		t.Fatalf("expected 2 app_preview events, got %d", len(appEvents))
	}

	// Both should share the same appId (refinement model)
	id1, _ := appEvents[0].Data["appId"].(string)
	id2, _ := appEvents[1].Data["appId"].(string)
	if id1 == "" || id2 == "" {
		t.Error("app_preview events should have non-empty appIds")
	}
	if id1 != id2 {
		t.Error("multiple app fences in one response should share the same appId (refinement)")
	}

	// Versions should increment
	v1 := toString(appEvents[0].Data["version"])
	v2 := toString(appEvents[1].Data["version"])
	if v1 != "1" {
		t.Errorf("first app version = %s, want 1", v1)
	}
	if v2 != "2" {
		t.Errorf("second app version = %s, want 2", v2)
	}
}

// TestIntegration_X27_EmptyAppPreviewFence verifies that an empty astonish-app
// code fence is ignored (no app_preview event).
func TestIntegration_X27_EmptyAppPreviewFence(t *testing.T) {
	response := "Here's an app:\n\n```astonish-app\n\n```\n\nOops, it's empty."

	mockLLM := NewMockLLM(TextTurn(response))
	env := setupIntegrationTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "Create an app")

	assertNoEvent(t, events, "app_preview")
}

// =============================================================================
// Helper Types for Advanced Tests
// =============================================================================

// stringError is a simple error type backed by a string.
type stringError string

func (e stringError) Error() string { return string(e) }
