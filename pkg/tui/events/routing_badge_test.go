package events

import (
	"testing"
)

// TestRoutingBadgeStamping verifies that routing tier badges are stamped on
// the correct agent text items in a multi-call turn (tool-using agent loop).
//
// Scenario: 2 LLM calls separated by a tool call.
//   - Call 1 (weak): text → tool_call → routing_info(weak) → tool_result
//   - Call 2 (medium): text → routing_info(medium) → done
//
// Expected: first agent item stamped "weak", second agent item stamped "medium".
func TestRoutingBadgeStamping_TwoCallsWithTools(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true // code mode // LinearThread = code mode

	tr.Apply(Event{Kind: KindUser, Text: "hello"})

	// Call 1: weak
	tr.Apply(Event{Kind: KindText, Text: "I'll look into that."})
	tr.Apply(Event{Kind: KindToolCall, ToolName: "shell_command"})
	tr.Apply(makeRoutingInfo("gpt-4o-mini", "weak", 1))
	tr.Apply(Event{Kind: KindToolResult, ToolName: "shell_command"})

	// Call 2: medium (text comes after tool result → new ItemAgent)
	tr.Apply(Event{Kind: KindText, Text: "Here is the answer."})
	tr.Apply(makeRoutingInfo("claude-haiku", "medium", 2))

	tr.Apply(Event{Kind: KindDone})

	// Collect agent items
	var agentItems []Item
	for _, it := range tr.Items {
		if it.Kind == ItemAgent {
			agentItems = append(agentItems, it)
		}
	}

	if len(agentItems) != 2 {
		t.Fatalf("expected 2 agent items, got %d (items: %+v)", len(agentItems), tr.Items)
	}

	// First agent item should be stamped with weak
	if agentItems[0].RoutingTier != "weak" {
		t.Errorf("first agent item: want tier=weak, got %q (model=%q)", agentItems[0].RoutingTier, agentItems[0].RoutingModel)
	}
	if agentItems[0].RoutingModel != "gpt-4o-mini" {
		t.Errorf("first agent item: want model=gpt-4o-mini, got %q", agentItems[0].RoutingModel)
	}

	// Second agent item should be stamped with medium
	if agentItems[1].RoutingTier != "medium" {
		t.Errorf("second agent item: want tier=medium, got %q (model=%q)", agentItems[1].RoutingTier, agentItems[1].RoutingModel)
	}
	if agentItems[1].RoutingModel != "claude-haiku" {
		t.Errorf("second agent item: want model=claude-haiku, got %q", agentItems[1].RoutingModel)
	}
}

// TestRoutingBadgeStamping_NoToolsBetweenCalls covers the case where two LLM
// calls produce text with no tool calls between them. In LinearThread mode
// the text is merged into one ItemAgent — the badge should show the LAST
// call's tier (since that's the most recent decision for that item).
func TestRoutingBadgeStamping_TwoCallsNoTools(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true // code mode

	tr.Apply(Event{Kind: KindUser, Text: "hello"})

	// Call 1: weak — text only, no tools
	tr.Apply(Event{Kind: KindText, Text: "First part."})
	tr.Apply(makeRoutingInfo("gpt-4o-mini", "weak", 1))

	// Call 2: medium — text only, no tools; in LinearThread this appends to same item
	tr.Apply(Event{Kind: KindText, Text: " Second part."})
	tr.Apply(makeRoutingInfo("claude-haiku", "medium", 2))

	tr.Apply(Event{Kind: KindDone})

	var agentItems []Item
	for _, it := range tr.Items {
		if it.Kind == ItemAgent {
			agentItems = append(agentItems, it)
		}
	}

	if len(agentItems) == 0 {
		t.Fatal("no agent items found")
	}
	// The last agent item should show the last routing decision (medium)
	last := agentItems[len(agentItems)-1]
	if last.RoutingTier != "medium" {
		t.Errorf("last agent item: want tier=medium, got %q", last.RoutingTier)
	}
}

// TestRoutingBadgeStamping_DoesNotCrossTurns verifies that a new turn's
// routing_info does not overwrite a completed previous turn's agent item.
func TestRoutingBadgeStamping_DoesNotCrossTurns(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true // code mode

	// Turn 1
	tr.Apply(Event{Kind: KindUser, Text: "first question"})
	tr.Apply(Event{Kind: KindText, Text: "Answer one."})
	tr.Apply(makeRoutingInfo("gpt-4o-mini", "weak", 1))
	tr.Apply(Event{Kind: KindDone})

	// Find the agent item from turn 1
	var turn1Agent *Item
	for i := range tr.Items {
		if tr.Items[i].Kind == ItemAgent {
			turn1Agent = &tr.Items[i]
			break
		}
	}
	if turn1Agent == nil {
		t.Fatal("no agent item after turn 1")
	}
	if turn1Agent.RoutingTier != "weak" {
		t.Errorf("turn 1 agent: want tier=weak, got %q", turn1Agent.RoutingTier)
	}

	// Turn 2
	tr.Apply(Event{Kind: KindUser, Text: "second question"})
	tr.Apply(Event{Kind: KindText, Text: "Answer two."})
	tr.Apply(makeRoutingInfo("claude-opus", "strong", 2))
	tr.Apply(Event{Kind: KindDone})

	// Turn 1's agent item must still say "weak"
	if turn1Agent.RoutingTier != "weak" {
		t.Errorf("turn 1 agent was overwritten: now tier=%q (expected weak)", turn1Agent.RoutingTier)
	}
}

// makeRoutingInfo builds a minimal KindRoutingInfo event for testing.
func makeRoutingInfo(model, tier string, total int64) Event {
	var strongPct, mediumPct, weakPct float64
	switch tier {
	case "strong":
		strongPct = 100
	case "medium":
		mediumPct = 100
	default:
		weakPct = 100
	}
	return NewRoutingInfo(model, tier == "strong", strongPct, weakPct, total,
		"claude-opus", "gpt-4o-mini", tier, "claude-haiku", mediumPct, 0)
}

// TestRoutingBadgeStamping_ToolFoldGetsIcon verifies that the tool fold
// (ItemActivity) also gets the routing tier stamped so its header shows
// the correct icon alongside the agent text bubble.
func TestRoutingBadgeStamping_ToolFoldGetsIcon(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true

	tr.Apply(Event{Kind: KindUser, Text: "hello"})

	// One LLM response: text + tool call (parts loop emits both before routing_info)
	tr.Apply(Event{Kind: KindText, Text: "Let me check."})
	tr.Apply(Event{Kind: KindToolCall, ToolName: "shell_command"})
	// routing_info fires after the entire parts loop
	tr.Apply(makeRoutingInfo("gpt-4o-mini", "weak", 1))

	tr.Apply(Event{Kind: KindDone})

	var agentItem, activityItem *Item
	for i := range tr.Items {
		if tr.Items[i].Kind == ItemAgent {
			agentItem = &tr.Items[i]
		}
		if tr.Items[i].Kind == ItemActivity {
			activityItem = &tr.Items[i]
		}
	}
	if agentItem == nil {
		t.Fatal("no agent item found")
	}
	if agentItem.RoutingTier != "weak" {
		t.Errorf("agent: want tier=weak, got %q", agentItem.RoutingTier)
	}
	if activityItem == nil {
		t.Fatal("no activity item found")
	}
	if activityItem.RoutingTier != "weak" {
		t.Errorf("activity: want tier=weak, got %q (tool fold should show icon)", activityItem.RoutingTier)
	}
}
