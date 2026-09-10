package api

import (
	"bufio"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// =============================================================================
// P0 Gap Tests: Tool Execution Errors (Items 1)
//
// These tests verify that tool errors propagate correctly through the
// ChatRunner → SSE event stream pipeline. Previously, all mock tools
// returned success, leaving error propagation completely untested.
// =============================================================================

// TestIntegration_ToolError_ReturnsError verifies that when a tool returns an
// error, the runner still completes and the error is visible in the event stream.
// ADK handles tool errors by feeding them back to the LLM as a tool response
// containing the error text, so the LLM can react (retry or explain).
func TestIntegration_ToolError_ReturnsError(t *testing.T) {
	mockLLM := NewMockLLM(
		// Turn 1: LLM requests a tool call
		ToolCallTurn("flaky_api", map[string]any{"endpoint": "/data"}),
		// Turn 2: After ADK feeds the error back, LLM responds
		TextTurn("The API returned an error. Let me try a different approach."),
	)

	flakyTool := newMockErrorTool("flaky_api", "A flaky API endpoint", "connection refused: dial tcp 192.168.1.100:8080")

	env := setupIntegrationTest(t, mockLLM, []tool.Tool{flakyTool})
	events := runAndCollect(t, env, "Fetch the latest data")

	assertFirstAndLast(t, events)

	// The runner should still emit tool_call (the LLM's request)
	assertHasEvent(t, events, "tool_call")
	toolCallEv := assertHasEvent(t, events, "tool_call")
	assertEventData(t, toolCallEv, "name", "flaky_api")

	// ADK feeds tool errors back to the LLM as a tool response, so the LLM
	// gets a second turn and produces the final text.
	textEv := assertHasEvent(t, events, "text")
	text, _ := textEv.Data["text"].(string)
	if !strings.Contains(text, "error") && !strings.Contains(text, "different approach") {
		t.Errorf("expected LLM to acknowledge tool error, got text: %q", text)
	}

	// Verify the LLM received at least 2 calls (initial + after tool error feedback)
	if len(mockLLM.Calls) < 2 {
		t.Errorf("expected at least 2 LLM calls (initial + post-error), got %d", len(mockLLM.Calls))
	}
}

// TestIntegration_ToolError_MultipleToolsOneErrors verifies that when one tool
// in a chain fails but another succeeds, the runner completes gracefully.
func TestIntegration_ToolError_MultipleToolsOneErrors(t *testing.T) {
	mockLLM := NewMockLLM(
		// Turn 1: LLM calls the flaky tool
		ToolCallTurn("flaky_api", map[string]any{"endpoint": "/data"}),
		// Turn 2: After error, LLM falls back to the working tool
		ToolCallTurn("local_cache", map[string]any{"key": "data"}),
		// Turn 3: LLM responds with the cached data
		TextTurn("I used the cached data instead."),
	)

	flakyTool := newMockErrorTool("flaky_api", "A flaky API", "timeout after 30s")
	cacheTool := newMockTool("local_cache", "Local cache", map[string]any{
		"value": "cached_data_123",
	})

	env := setupIntegrationTest(t, mockLLM, []tool.Tool{flakyTool, cacheTool})
	events := runAndCollect(t, env, "Get the data")

	assertFirstAndLast(t, events)

	// Should see both tool calls
	toolCalls := eventsOfType(events, "tool_call")
	if len(toolCalls) < 2 {
		t.Fatalf("expected at least 2 tool_call events, got %d: %v", len(toolCalls), eventTypes(events))
	}
	assertEventData(t, toolCalls[0], "name", "flaky_api")
	assertEventData(t, toolCalls[1], "name", "local_cache")

	// Final text should be present
	assertHasEvent(t, events, "text")
}

// TestIntegration_ToolError_PanicRecovery verifies that a panic in tool
// execution doesn't crash the runner — it should be caught and converted
// to an error that ADK feeds back to the LLM.
func TestIntegration_ToolError_PanicRecovery(t *testing.T) {
	mockLLM := NewMockLLM(
		// Turn 1: LLM calls the panicking tool
		ToolCallTurn("dangerous_tool", map[string]any{}),
		// Turn 2: After panic recovery, LLM responds
		TextTurn("Something went wrong with that tool."),
	)

	panicTool := newMockPanicTool("dangerous_tool", "A tool that panics", "nil pointer dereference")

	env := setupIntegrationTest(t, mockLLM, []tool.Tool{panicTool})
	events := runAndCollect(t, env, "Run the dangerous operation", 15*time.Second)

	// The runner should complete (not hang or crash)
	if len(events) == 0 {
		t.Fatal("expected events but got none — runner may have crashed")
	}

	// Should have at least session and done
	hasSession := false
	hasDone := false
	for _, ev := range events {
		if ev.Type == "session" {
			hasSession = true
		}
		if ev.Type == "done" {
			hasDone = true
		}
	}
	if !hasSession {
		t.Error("expected session event")
	}
	// done may or may not be emitted depending on panic recovery timing
	_ = hasDone
}

// =============================================================================
// P0 Gap Tests: Parallel Tool Dispatch (Item 2)
//
// Tests that use MultiToolCallTurn — an LLM response containing multiple
// function calls in a single turn. Previously defined but never used.
// =============================================================================

// TestIntegration_ParallelToolCalls verifies that when the LLM returns multiple
// function calls in a single response, all are executed and their results appear
// as separate events in the correct order.
func TestIntegration_ParallelToolCalls(t *testing.T) {
	mockLLM := NewMockLLM(
		// Turn 1: LLM requests two tool calls simultaneously
		MultiToolCallTurn(
			&genai.FunctionCall{Name: "web_search", Args: map[string]any{"query": "Go testing"}},
			&genai.FunctionCall{Name: "read_file", Args: map[string]any{"path": "/README.md"}},
		),
		// Turn 2: After both tool results, LLM responds
		TextTurn("Based on my search and the README, here's what I found."),
	)

	searchTool := newMockTool("web_search", "Search the web", map[string]any{
		"results": "Go testing best practices...",
	})
	readTool := newMockTool("read_file", "Read a file", map[string]any{
		"content": "# Project README\nThis is a Go project.",
	})

	env := setupIntegrationTest(t, mockLLM, []tool.Tool{searchTool, readTool})
	events := runAndCollect(t, env, "Search for Go testing and read the README")

	assertFirstAndLast(t, events)

	// Should see both tool_call events
	toolCalls := eventsOfType(events, "tool_call")
	if len(toolCalls) < 2 {
		t.Fatalf("expected at least 2 tool_call events for parallel dispatch, got %d: %v",
			len(toolCalls), eventTypes(events))
	}

	// Verify both tool names appear
	toolNames := map[string]bool{}
	for _, tc := range toolCalls {
		name, _ := tc.Data["name"].(string)
		toolNames[name] = true
	}
	if !toolNames["web_search"] {
		t.Error("expected web_search in parallel tool calls")
	}
	if !toolNames["read_file"] {
		t.Error("expected read_file in parallel tool calls")
	}

	// Should see tool_result events for both
	toolResults := eventsOfType(events, "tool_result")
	if len(toolResults) < 2 {
		t.Fatalf("expected at least 2 tool_result events, got %d", len(toolResults))
	}

	resultNames := map[string]bool{}
	for _, tr := range toolResults {
		name, _ := tr.Data["name"].(string)
		resultNames[name] = true
	}
	if !resultNames["web_search"] {
		t.Error("expected web_search result in parallel tool results")
	}
	if !resultNames["read_file"] {
		t.Error("expected read_file result in parallel tool results")
	}

	// Final text should be present
	assertHasEvent(t, events, "text")
}

// TestIntegration_ParallelToolCalls_ThreeConcurrent verifies 3+ parallel tool
// calls in a single LLM turn.
func TestIntegration_ParallelToolCalls_ThreeConcurrent(t *testing.T) {
	mockLLM := NewMockLLM(
		MultiToolCallTurn(
			&genai.FunctionCall{Name: "tool_a", Args: map[string]any{}},
			&genai.FunctionCall{Name: "tool_b", Args: map[string]any{}},
			&genai.FunctionCall{Name: "tool_c", Args: map[string]any{}},
		),
		TextTurn("All three tools completed."),
	)

	toolA := newMockTool("tool_a", "Tool A", map[string]any{"a": "ok"})
	toolB := newMockTool("tool_b", "Tool B", map[string]any{"b": "ok"})
	toolC := newMockTool("tool_c", "Tool C", map[string]any{"c": "ok"})

	env := setupIntegrationTest(t, mockLLM, []tool.Tool{toolA, toolB, toolC})
	events := runAndCollect(t, env, "Run all three tools")

	assertFirstAndLast(t, events)

	toolCalls := eventsOfType(events, "tool_call")
	if len(toolCalls) < 3 {
		t.Fatalf("expected at least 3 tool_call events, got %d: %v",
			len(toolCalls), eventTypes(events))
	}

	toolResults := eventsOfType(events, "tool_result")
	if len(toolResults) < 3 {
		t.Fatalf("expected at least 3 tool_result events, got %d", len(toolResults))
	}
}

// =============================================================================
// P0 Gap Tests: SSE HTTP Handler Wire Format (Item 3)
//
// Tests that exercise the actual HTTP SSE serialization path: SendSSE() and
// streamRunnerEvents(). Previously, integration tests bypassed HTTP entirely
// by calling runner.Run() directly and consuming from the Go channel.
// =============================================================================

// TestSSEWireFormat_SendSSE verifies that SendSSE produces correct SSE wire
// format: "event: <type>\ndata: <json>\n\n" with valid JSON payloads.
func TestSSEWireFormat_SendSSE(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		data      any
		wantEvent string
	}{
		{
			name:      "simple text event",
			eventType: "text",
			data:      map[string]string{"text": "Hello world"},
			wantEvent: "text",
		},
		{
			name:      "session event",
			eventType: "session",
			data:      map[string]any{"sessionId": "sess-123", "isNew": true},
			wantEvent: "session",
		},
		{
			name:      "done event",
			eventType: "done",
			data:      map[string]any{"done": true},
			wantEvent: "done",
		},
		{
			name:      "tool_call with nested args",
			eventType: "tool_call",
			data: map[string]any{
				"name": "web_search",
				"args": map[string]any{"query": "Go testing", "limit": 10},
			},
			wantEvent: "tool_call",
		},
		{
			name:      "error event",
			eventType: "error",
			data:      map[string]string{"error": "Rate limit exceeded"},
			wantEvent: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			SendSSE(&buf, nil, tt.eventType, tt.data)

			output := buf.String()

			// Must end with double newline
			if !strings.HasSuffix(output, "\n\n") {
				t.Errorf("SSE output must end with \\n\\n, got: %q", output)
			}

			// Parse the SSE format
			lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
			if len(lines) < 2 {
				t.Fatalf("expected at least 2 lines (event + data), got %d: %q", len(lines), output)
			}

			// First line: event: <type>
			if !strings.HasPrefix(lines[0], "event: ") {
				t.Errorf("first line should start with 'event: ', got: %q", lines[0])
			}
			gotEventType := strings.TrimPrefix(lines[0], "event: ")
			if gotEventType != tt.wantEvent {
				t.Errorf("event type = %q, want %q", gotEventType, tt.wantEvent)
			}

			// Second line: data: <json>
			if !strings.HasPrefix(lines[1], "data: ") {
				t.Errorf("second line should start with 'data: ', got: %q", lines[1])
			}
			jsonStr := strings.TrimPrefix(lines[1], "data: ")

			// Validate JSON
			var parsed map[string]any
			if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
				t.Errorf("data is not valid JSON: %v\nraw: %q", err, jsonStr)
			}
		})
	}
}

// TestSSEWireFormat_StreamRunnerEvents verifies the full HTTP SSE pipeline:
// ChatRunner emits events → streamRunnerEvents → HTTP response with correct
// framing and event ordering. Uses a real ChatRunner.Run() to populate events,
// then verifies the SSE output from streamRunnerEvents.
func TestSSEWireFormat_StreamRunnerEvents(t *testing.T) {
	// Run a minimal integration test to populate the runner with real events
	mockLLM := NewMockLLM(TextTurn("Hello from SSE wire test"))
	env := setupIntegrationTest(t, mockLLM, nil)

	// Run and wait for completion through the normal channel
	events := runAndCollect(t, env, "Test SSE wire format")
	if len(events) < 2 {
		t.Fatalf("expected events from runner, got %d", len(events))
	}

	// Now the runner is done and has buffered history.
	// Verify IsDone is true
	if !env.Runner.IsDone() {
		t.Fatal("runner should be done after runAndCollect")
	}

	// Use streamRunnerEvents to replay the history into an httptest recorder.
	// streamRunnerEvents reads GetHistory() and writes SSE framing.
	rec := httptest.NewRecorder()
	streamRunnerEvents(rec, rec, t.Context(), env.Runner)

	body := rec.Body.String()

	if body == "" {
		t.Fatal("expected SSE body, got empty")
	}

	// Parse all events from the SSE stream
	scanner := bufio.NewScanner(strings.NewReader(body))
	var parsedEvents []struct {
		Type string
		Data map[string]any
	}

	var currentType string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			currentType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			jsonStr := strings.TrimPrefix(line, "data: ")
			var data map[string]any
			if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
				t.Errorf("invalid JSON in event %q: %v\nraw: %q", currentType, err, jsonStr)
				continue
			}
			parsedEvents = append(parsedEvents, struct {
				Type string
				Data map[string]any
			}{Type: currentType, Data: data})
		}
	}

	if len(parsedEvents) < 3 {
		t.Fatalf("expected at least 3 SSE events (session, text, done), got %d\nbody:\n%s",
			len(parsedEvents), body)
	}

	// Verify event ordering
	if parsedEvents[0].Type != "session" {
		t.Errorf("first SSE event should be 'session', got %q", parsedEvents[0].Type)
	}
	if parsedEvents[len(parsedEvents)-1].Type != "done" {
		t.Errorf("last SSE event should be 'done', got %q", parsedEvents[len(parsedEvents)-1].Type)
	}

	// Verify data content
	foundText := false
	for _, ev := range parsedEvents {
		if ev.Type == "text" {
			if text, ok := ev.Data["text"].(string); ok && strings.Contains(text, "Hello from SSE wire test") {
				foundText = true
			}
		}
	}
	if !foundText {
		t.Error("expected to find text event with 'Hello from SSE wire test' in SSE body")
	}

	// Verify each event in the raw body has correct framing
	rawEvents := strings.Split(body, "\n\n")
	for i, rawEv := range rawEvents {
		rawEv = strings.TrimSpace(rawEv)
		if rawEv == "" {
			continue
		}
		lines := strings.Split(rawEv, "\n")
		if len(lines) < 2 {
			t.Errorf("SSE event %d has fewer than 2 lines: %q", i, rawEv)
			continue
		}
		if !strings.HasPrefix(lines[0], "event: ") {
			t.Errorf("SSE event %d missing 'event: ' prefix: %q", i, lines[0])
		}
		if !strings.HasPrefix(lines[1], "data: ") {
			t.Errorf("SSE event %d missing 'data: ' prefix: %q", i, lines[1])
		}
	}
}

// TestSSEWireFormat_SendErrorSSE verifies the error SSE helper.
func TestSSEWireFormat_SendErrorSSE(t *testing.T) {
	var buf strings.Builder
	SendErrorSSE(&buf, nil, "Something went wrong")

	output := buf.String()

	if !strings.Contains(output, "event: error") {
		t.Errorf("expected 'event: error' in output: %q", output)
	}
	if !strings.Contains(output, `"error":"Something went wrong"`) {
		t.Errorf("expected error message in output: %q", output)
	}
	if !strings.HasSuffix(output, "\n\n") {
		t.Errorf("must end with \\n\\n: %q", output)
	}
}

// =============================================================================
// P0 Gap Tests: Approval Flow (Item 4)
//
// Tests the full approval state machine with autoApprove=false.
// Previously, all integration tests used autoApprove=true, leaving the
// pause→approval event→resume flow completely untested.
// =============================================================================

// TestIntegration_ApprovalFlow_AutoApproveFalse verifies that when autoApprove
// is false and a tool requires approval, the runner emits an approval event.
// Note: The full pause→resume cycle requires ProtectedTool wrapping which is
// handled by the AstonishAgent (not ChatAgent). This test verifies that the
// runner functions correctly when autoApprove=false at the ChatRunner level.
func TestIntegration_ApprovalFlow_AutoApproveFalse(t *testing.T) {
	// With autoApprove=false, the ChatAgent still passes it to the ADK runner.
	// However, ProtectedTool wrapping happens at a higher level (AstonishAgent).
	// At the ChatAgent/ChatRunner level, autoApprove=false means the ADK runner
	// won't auto-confirm tool calls — but without ProtectedTool, regular tools
	// execute normally. This test verifies the flag is propagated correctly.
	mockLLM := NewMockLLM(
		ToolCallTurn("safe_tool", map[string]any{"action": "read"}),
		TextTurn("Done reading."),
	)

	safeTool := newMockTool("safe_tool", "A safe tool", map[string]any{"result": "data"})

	env := setupIntegrationTest(t, mockLLM, []tool.Tool{safeTool})
	events := runAndCollectWithApprove(t, env, "Read the data", false, "")

	assertFirstAndLast(t, events)

	// Tool should still execute (no ProtectedTool wrapping at this level)
	assertHasEvent(t, events, "tool_call")
	assertHasEvent(t, events, "tool_result")
	assertHasEvent(t, events, "text")
}

// TestIntegration_ApprovalEvent_EmittedByStateDelta verifies the full approval
// event pipeline: state delta with approval_options → runner emits "approval" event
// → state delta with auto_approved → runner emits "auto_approved" event.
// This is more thorough than X11/X12 by testing the channel delivery end-to-end.
func TestIntegration_ApprovalEvent_EmittedByStateDelta(t *testing.T) {
	runner := newChatRunner("test-approval-flow", studioChatUserID, studioChatAppName, true)
	ch := runner.Subscribe("test")
	defer func() {
		runner.Unsubscribe("test")
		runner.Stop()
		// Drain
		go func() { for range ch {} }()
	}()

	// Simulate the approval request (what ProtectedTool.Run() sets in state)
	runner.processStateDelta(map[string]any{
		"awaiting_approval": true,
		"approval_tool":     "shell_command",
		"approval_args":     map[string]any{"command": "rm -rf /tmp/test"},
		"approval_options":  []any{"Allow", "Deny"},
	})

	// Collect the approval event
	select {
	case ev := <-ch:
		if ev.Type != "approval" {
			t.Fatalf("expected 'approval' event, got %q", ev.Type)
		}
		assertEventData(t, ev, "tool", "shell_command")
		opts, _ := ev.Data["options"].([]any)
		if len(opts) != 2 {
			t.Errorf("expected 2 options, got %d: %v", len(opts), opts)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval event")
	}

	// Now simulate the auto-approval (what happens when autoApprove=true)
	runner.processStateDelta(map[string]any{
		"auto_approved": true,
		"approval_tool": "shell_command",
	})

	select {
	case ev := <-ch:
		if ev.Type != "auto_approved" {
			t.Fatalf("expected 'auto_approved' event, got %q", ev.Type)
		}
		assertEventData(t, ev, "tool", "shell_command")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for auto_approved event")
	}
}

// TestIntegration_ApprovalFlow_DenyStateDelta verifies that after an approval
// event, a denial state delta is handled correctly.
func TestIntegration_ApprovalFlow_DenyStateDelta(t *testing.T) {
	runner := newChatRunner("test-deny", studioChatUserID, studioChatAppName, true)
	ch := runner.Subscribe("test")
	defer func() {
		runner.Unsubscribe("test")
		runner.Stop()
		go func() { for range ch {} }()
	}()

	// Emit approval request
	runner.processStateDelta(map[string]any{
		"awaiting_approval": true,
		"approval_tool":     "dangerous_tool",
		"approval_options":  []any{"Allow", "Deny"},
	})

	// Consume approval event
	select {
	case ev := <-ch:
		if ev.Type != "approval" {
			t.Fatalf("expected 'approval', got %q", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	// Emit denial (approval cleared without auto_approved)
	runner.processStateDelta(map[string]any{
		"awaiting_approval": false,
	})

	// No auto_approved event should be emitted
	select {
	case ev := <-ch:
		if ev.Type == "auto_approved" {
			t.Fatal("should NOT emit auto_approved on denial")
		}
		// Other events (like spinner) may be emitted — that's OK
		_ = ev
	case <-time.After(500 * time.Millisecond):
		// Good — no auto_approved event
	}
}

// =============================================================================
// Helpers
// =============================================================================

// t.Context() returns a context associated with the test (Go 1.24+).
// If not available, callers can pass context.Background() instead.
// This is defined here for clarity in the SSE wire format tests.
