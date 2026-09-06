package events

import (
	"testing"
)

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

func agentItems(tr *Transcript) []Item {
	var out []Item
	for _, it := range tr.Items {
		if it.Kind == ItemAgent {
			out = append(out, it)
		}
	}
	return out
}

func activityItems(tr *Transcript) []Item {
	var out []Item
	for _, it := range tr.Items {
		if it.Kind == ItemActivity {
			out = append(out, it)
		}
	}
	return out
}

func wantRouting(t *testing.T, it Item, kind ItemKind, model, tier string) {
	t.Helper()
	if it.Kind != kind {
		t.Fatalf("item kind = %q, want %q", it.Kind, kind)
	}
	if it.RoutingTier != tier {
		t.Errorf("%s: RoutingTier = %q, want %q (model=%q)", kind, it.RoutingTier, tier, it.RoutingModel)
	}
	if it.RoutingModel != model {
		t.Errorf("%s: RoutingModel = %q, want %q", kind, it.RoutingModel, model)
	}
}

// TestRoutingBadgeStamping_TwoCallsWithTools verifies that routing tier badges
// are stamped on the correct agent text items in a multi-call turn.
//
// Scenario: 2 LLM calls separated by a tool call.
//   - Call 1 (weak): text → tool_call → routing_info(weak) → tool_result
//   - Call 2 (medium): text → routing_info(medium) → done
func TestRoutingBadgeStamping_TwoCallsWithTools(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true

	tr.Apply(Event{Kind: KindUser, Text: "hello"})

	tr.Apply(Event{Kind: KindText, Text: "I'll look into that."})
	tr.Apply(Event{Kind: KindToolCall, ToolName: "shell_command"})
	tr.Apply(makeRoutingInfo("gpt-4o-mini", "weak", 1))
	tr.Apply(Event{Kind: KindToolResult, ToolName: "shell_command"})

	tr.Apply(Event{Kind: KindText, Text: "Here is the answer."})
	tr.Apply(makeRoutingInfo("claude-haiku", "medium", 2))

	tr.Apply(Event{Kind: KindDone})

	agents := agentItems(tr)
	if len(agents) != 2 {
		t.Fatalf("expected 2 agent items, got %d (items: %+v)", len(agents), tr.Items)
	}
	wantRouting(t, agents[0], ItemAgent, "gpt-4o-mini", "weak")
	wantRouting(t, agents[1], ItemAgent, "claude-haiku", "medium")

	acts := activityItems(tr)
	if len(acts) != 1 {
		t.Fatalf("expected 1 activity item, got %d", len(acts))
	}
	wantRouting(t, acts[0], ItemActivity, "gpt-4o-mini", "weak")
}

// TestRoutingBadgeStamping_TwoCallsNoTools covers two LLM calls with no tools
// between them. In LinearThread mode the text is merged into one ItemAgent —
// the badge should show the LAST call's tier.
func TestRoutingBadgeStamping_TwoCallsNoTools(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true

	tr.Apply(Event{Kind: KindUser, Text: "hello"})
	tr.Apply(Event{Kind: KindText, Text: "First part."})
	tr.Apply(makeRoutingInfo("gpt-4o-mini", "weak", 1))
	tr.Apply(Event{Kind: KindText, Text: " Second part."})
	tr.Apply(makeRoutingInfo("claude-haiku", "medium", 2))
	tr.Apply(Event{Kind: KindDone})

	agents := agentItems(tr)
	if len(agents) == 0 {
		t.Fatal("no agent items found")
	}
	wantRouting(t, agents[len(agents)-1], ItemAgent, "claude-haiku", "medium")
}

// TestRoutingBadgeStamping_DoesNotCrossTurns verifies that a new turn's
// routing_info does not overwrite a completed previous turn's agent item.
func TestRoutingBadgeStamping_DoesNotCrossTurns(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true

	tr.Apply(Event{Kind: KindUser, Text: "first question"})
	tr.Apply(Event{Kind: KindText, Text: "Answer one."})
	tr.Apply(makeRoutingInfo("gpt-4o-mini", "weak", 1))
	tr.Apply(Event{Kind: KindDone})

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

	tr.Apply(Event{Kind: KindUser, Text: "second question"})
	tr.Apply(Event{Kind: KindText, Text: "Answer two."})
	tr.Apply(makeRoutingInfo("claude-opus", "strong", 2))
	tr.Apply(Event{Kind: KindDone})

	if turn1Agent.RoutingTier != "weak" {
		t.Errorf("turn 1 agent was overwritten: now tier=%q (expected weak)", turn1Agent.RoutingTier)
	}
}

// TestRoutingBadgeStamping_ToolFoldGetsIcon verifies that the tool fold
// (ItemActivity) also gets the routing tier stamped so its header shows
// the correct icon alongside the agent text bubble.
func TestRoutingBadgeStamping_ToolFoldGetsIcon(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true

	tr.Apply(Event{Kind: KindUser, Text: "hello"})
	tr.Apply(Event{Kind: KindText, Text: "Let me check."})
	tr.Apply(Event{Kind: KindToolCall, ToolName: "shell_command"})
	tr.Apply(makeRoutingInfo("gpt-4o-mini", "weak", 1))
	tr.Apply(Event{Kind: KindDone})

	agents := agentItems(tr)
	if len(agents) == 0 {
		t.Fatal("no agent item found")
	}
	wantRouting(t, agents[0], ItemAgent, "gpt-4o-mini", "weak")

	acts := activityItems(tr)
	if len(acts) == 0 {
		t.Fatal("no activity item found")
	}
	wantRouting(t, acts[0], ItemActivity, "gpt-4o-mini", "weak")
}

// TestRoutingBadgeStamping_LiveCallStartBeforeText is the live TUI order:
// routing_info is emitted as soon as GenerateContent records a tier, which is
// before the first streamed text or tool_call. New items must inherit the
// badge immediately so Auto mode shows the model during execution.
func TestRoutingBadgeStamping_LiveCallStartBeforeText(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true

	tr.Apply(Event{Kind: KindUser, Text: "hello"})
	tr.Apply(makeRoutingInfo("gpt-4o-mini", "weak", 1))
	tr.Apply(Event{Kind: KindText, Text: "Looking that up."})
	tr.Apply(Event{Kind: KindToolCall, ToolName: "read_file"})
	tr.Apply(Event{Kind: KindToolResult, ToolName: "read_file", Result: "ok"})

	agents := agentItems(tr)
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent item, got %d", len(agents))
	}
	wantRouting(t, agents[0], ItemAgent, "gpt-4o-mini", "weak")

	acts := activityItems(tr)
	if len(acts) != 1 {
		t.Fatalf("expected 1 activity item, got %d", len(acts))
	}
	wantRouting(t, acts[0], ItemActivity, "gpt-4o-mini", "weak")
}

// TestRoutingBadgeStamping_LiveSecondCallDoesNotOverwriteTools ensures that
// routing_info for call 2 (emitted at call start, while the last item is still
// call 1's tool fold) does not rewrite call 1's badges.
func TestRoutingBadgeStamping_LiveSecondCallDoesNotOverwriteTools(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true

	tr.Apply(Event{Kind: KindUser, Text: "hello"})
	tr.Apply(makeRoutingInfo("gpt-4o-mini", "weak", 1))
	tr.Apply(Event{Kind: KindText, Text: "I'll look into that."})
	tr.Apply(Event{Kind: KindToolCall, ToolName: "shell_command"})
	tr.Apply(Event{Kind: KindToolResult, ToolName: "shell_command"})

	tr.Apply(makeRoutingInfo("claude-opus", "strong", 2))
	tr.Apply(Event{Kind: KindText, Text: "Done."})
	tr.Apply(Event{Kind: KindDone})

	agents := agentItems(tr)
	if len(agents) != 2 {
		t.Fatalf("expected 2 agent items, got %d", len(agents))
	}
	wantRouting(t, agents[0], ItemAgent, "gpt-4o-mini", "weak")
	wantRouting(t, agents[1], ItemAgent, "claude-opus", "strong")

	acts := activityItems(tr)
	if len(acts) != 1 {
		t.Fatalf("expected 1 activity item, got %d", len(acts))
	}
	wantRouting(t, acts[0], ItemActivity, "gpt-4o-mini", "weak")
}

// TestRoutingBadgeStamping_ToolOnlyResponse stamps a tool fold when the call
// produced no agent text.
func TestRoutingBadgeStamping_ToolOnlyResponse(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true

	tr.Apply(Event{Kind: KindUser, Text: "hello"})
	tr.Apply(Event{Kind: KindToolCall, ToolName: "read_file"})
	tr.Apply(makeRoutingInfo("claude-haiku", "medium", 1))
	tr.Apply(Event{Kind: KindDone})

	acts := activityItems(tr)
	if len(acts) != 1 {
		t.Fatalf("expected 1 activity item, got %d", len(acts))
	}
	wantRouting(t, acts[0], ItemActivity, "claude-haiku", "medium")
}
