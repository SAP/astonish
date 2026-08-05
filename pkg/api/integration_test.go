package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/common"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// --- Test Infrastructure ---

// integrationTestEnv holds the environment for a single integration test.
type integrationTestEnv struct {
	Runner         *ChatRunner
	EventCh        <-chan ChatEvent
	SessionService session.Service
	ChatAgent      *agent.ChatAgent
	MockLLM        *MockLLM
}

// setupIntegrationTest creates a minimal ChatRunner environment with a MockLLM.
// It wires the mock into a ChatAgent and returns everything needed to run and
// assert against the event stream. A session is pre-created in the service so
// the ADK runner can find it by ID.
func setupIntegrationTest(t *testing.T, mockLLM *MockLLM, tools []tool.Tool) *integrationTestEnv {
	t.Helper()

	sessionService := common.NewAutoInitService(session.InMemoryService())
	promptBuilder := &agent.SystemPromptBuilder{}
	chatAgent := agent.NewChatAgent(mockLLM, tools, nil, sessionService, promptBuilder, false, true)

	// Create the session in the service first (mirrors StudioChatHandler behavior)
	ctx := context.Background()
	createResp, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName: studioChatAppName,
		UserID:  studioChatUserID,
	})
	if err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}
	sessionID := createResp.Session.ID()

	runner := newChatRunner(sessionID, studioChatUserID, true)
	ch := runner.Subscribe("test")

	t.Cleanup(func() {
		runner.Stop()
		// Drain any remaining events to avoid goroutine leaks
		go func() {
			for range ch {
			}
		}()
	})

	return &integrationTestEnv{
		Runner:         runner,
		EventCh:        ch,
		SessionService: sessionService,
		ChatAgent:      chatAgent,
		MockLLM:        mockLLM,
	}
}

// runAndCollect starts the ChatRunner in a goroutine and collects all events
// until the runner completes (channel closed) or the timeout expires.
// An optional timeout can be provided; defaults to 10 seconds.
func runAndCollect(t *testing.T, env *integrationTestEnv, msg string, timeout ...time.Duration) []ChatEvent {
	t.Helper()

	to := 10 * time.Second
	if len(timeout) > 0 {
		to = timeout[0]
	}

	userMsg := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{Text: msg}},
	}

	go env.Runner.Run(
		env.ChatAgent,
		env.SessionService,
		env.MockLLM,
		nil, // titleSetter — not needed for integration tests
		userMsg,
		msg,
		true, // autoApprove
		"",   // systemContext
		nil,  // pinnedToolGroups
		false, // planMode
	)

	return collectEvents(t, env.EventCh, to)
}

// collectEvents reads from the event channel until it closes or the timeout fires.
func collectEvents(t *testing.T, ch <-chan ChatEvent, timeout time.Duration) []ChatEvent {
	t.Helper()
	var events []ChatEvent
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-timer.C:
			t.Fatalf("timed out after %v waiting for events (collected %d so far)", timeout, len(events))
			return events
		}
	}
}

// --- Assertion Helpers ---

// eventsOfType returns all events matching the given type.
func eventsOfType(events []ChatEvent, eventType string) []ChatEvent {
	var result []ChatEvent
	for _, ev := range events {
		if ev.Type == eventType {
			result = append(result, ev)
		}
	}
	return result
}

// assertHasEvent asserts at least one event of the given type exists and returns the first.
func assertHasEvent(t *testing.T, events []ChatEvent, eventType string) ChatEvent {
	t.Helper()
	for _, ev := range events {
		if ev.Type == eventType {
			return ev
		}
	}
	t.Fatalf("expected event type %q but none found in %d events: %v",
		eventType, len(events), eventTypes(events))
	return ChatEvent{}
}

// assertNoEvent asserts no event of the given type exists.
func assertNoEvent(t *testing.T, events []ChatEvent, eventType string) {
	t.Helper()
	for _, ev := range events {
		if ev.Type == eventType {
			t.Fatalf("expected no event of type %q but found: %+v", eventType, ev.Data)
		}
	}
}

// assertEventSequence asserts that the given event types appear in order
// (not necessarily contiguously) within the events slice.
func assertEventSequence(t *testing.T, events []ChatEvent, expectedTypes ...string) {
	t.Helper()
	idx := 0
	for _, ev := range events {
		if idx < len(expectedTypes) && ev.Type == expectedTypes[idx] {
			idx++
		}
	}
	if idx != len(expectedTypes) {
		t.Fatalf("event sequence mismatch: expected %v in order, but only matched %d/%d\nactual types: %v",
			expectedTypes, idx, len(expectedTypes), eventTypes(events))
	}
}

// assertEventData asserts a specific key=value in an event's Data map.
func assertEventData(t *testing.T, ev ChatEvent, key string, expected any) {
	t.Helper()
	val, ok := ev.Data[key]
	if !ok {
		t.Fatalf("event %q missing data key %q; data: %v", ev.Type, key, ev.Data)
	}
	// Compare as strings for simplicity (handles int/float/string differences)
	got := toString(val)
	want := toString(expected)
	if got != want {
		t.Errorf("event %q data[%q] = %v, want %v", ev.Type, key, val, expected)
	}
}

// assertFirstAndLast asserts the first event is "session" and the last is "done".
func assertFirstAndLast(t *testing.T, events []ChatEvent) {
	t.Helper()
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (session + done), got %d", len(events))
	}
	if events[0].Type != "session" {
		t.Errorf("first event should be 'session', got %q", events[0].Type)
	}
	if events[len(events)-1].Type != "done" {
		t.Errorf("last event should be 'done', got %q", events[len(events)-1].Type)
	}
}

// eventTypes returns a slice of event type strings for debugging.
func eventTypes(events []ChatEvent) []string {
	types := make([]string, len(events))
	for i, ev := range events {
		types[i] = ev.Type
	}
	return types
}

// toString converts a value to string for comparison.
func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return intToStr(val)
	case int32:
		return intToStr(int(val))
	case int64:
		return intToStr(int(val))
	case float64:
		// JSON numbers come back as float64
		if val == float64(int(val)) {
			return intToStr(int(val))
		}
		return floatToStr(val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func intToStr(v int) string {
	s := make([]byte, 0, 20)
	if v < 0 {
		s = append(s, '-')
		v = -v
	}
	if v == 0 {
		return "0"
	}
	digits := make([]byte, 0, 20)
	for v > 0 {
		digits = append(digits, byte('0'+v%10))
		v /= 10
	}
	for i := len(digits) - 1; i >= 0; i-- {
		s = append(s, digits[i])
	}
	return string(s)
}

func floatToStr(v float64) string {
	// Simple float to string - use fmt for complex cases
	return toString(int(v))
}

// --- Mock Tool Factory ---

// mockToolArgs is a generic args type for mock tools.
type mockToolArgs struct {
	Input map[string]any `json:"input,omitempty"`
}

// mockToolResult is a generic result type for mock tools.
type mockToolResult struct {
	Output map[string]any `json:"output,omitempty"`
}

// newMockTool creates an ADK-compatible tool using functiontool.New.
// The tool accepts any JSON args and returns the provided result map.
func newMockTool(name, description string, result map[string]any) tool.Tool {
	handler := func(_ tool.Context, _ mockToolArgs) (mockToolResult, error) {
		return mockToolResult{Output: result}, nil
	}
	t, err := functiontool.New(functiontool.Config{
		Name:        name,
		Description: description,
	}, handler)
	if err != nil {
		panic(fmt.Sprintf("failed to create mock tool %q: %v", name, err))
	}
	return t
}

// newMockErrorTool creates an ADK-compatible tool that always returns an error.
// This tests error propagation from tool execution → runner → SSE event stream.
func newMockErrorTool(name, description, errMsg string) tool.Tool {
	handler := func(_ tool.Context, _ mockToolArgs) (mockToolResult, error) {
		return mockToolResult{}, fmt.Errorf("%s", errMsg)
	}
	t, err := functiontool.New(functiontool.Config{
		Name:        name,
		Description: description,
	}, handler)
	if err != nil {
		panic(fmt.Sprintf("failed to create mock error tool %q: %v", name, err))
	}
	return t
}

// newMockPanicTool creates an ADK-compatible tool that panics during execution.
// This tests that panics in tool execution are caught and converted to error events.
func newMockPanicTool(name, description, panicMsg string) tool.Tool {
	handler := func(_ tool.Context, _ mockToolArgs) (mockToolResult, error) {
		panic(panicMsg)
	}
	t, err := functiontool.New(functiontool.Config{
		Name:        name,
		Description: description,
	}, handler)
	if err != nil {
		panic(fmt.Sprintf("failed to create mock panic tool %q: %v", name, err))
	}
	return t
}

// runAndCollectWithApprove is like runAndCollect but allows setting autoApprove to false.
func runAndCollectWithApprove(t *testing.T, env *integrationTestEnv, msg string, autoApprove bool, systemContext string, timeout ...time.Duration) []ChatEvent {
	t.Helper()

	to := 10 * time.Second
	if len(timeout) > 0 {
		to = timeout[0]
	}

	userMsg := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{Text: msg}},
	}

	go env.Runner.Run(
		env.ChatAgent,
		env.SessionService,
		env.MockLLM,
		nil, // titleSetter
		userMsg,
		msg,
		autoApprove,
		systemContext,
		nil,   // pinnedToolGroups
		false, // planMode
	)

	return collectEvents(t, env.EventCh, to)
}
