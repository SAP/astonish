package events

import (
	"strings"
	"testing"
)

func TestTranscript_TextStreaming(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewUser("hello"))
	tr.Apply(NewText("Hi"))
	if !tr.Items[1].Provisional {
		t.Fatal("agent should be provisional while streaming")
	}
	if tr.Status != "Thinking…" {
		t.Fatalf("status=%q want Thinking…", tr.Status)
	}
	tr.Apply(NewText(" there"))
	tr.Apply(NewDone())

	if tr.Streaming {
		t.Fatal("expected not streaming after Done")
	}
	if len(tr.Items) != 2 {
		t.Fatalf("items=%d want 2", len(tr.Items))
	}
	if tr.Items[0].Kind != ItemUser || tr.Items[0].Content != "hello" {
		t.Fatalf("user item: %+v", tr.Items[0])
	}
	if tr.Items[1].Kind != ItemAgent || tr.Items[1].Content != "Hi there" {
		t.Fatalf("agent item: %+v", tr.Items[1])
	}
	if tr.Items[1].Provisional {
		t.Fatal("agent should be finalized after Done")
	}
}

func TestTranscript_ToolActivityFold(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewUser("edit it"))
	tr.Apply(NewToolCall("edit_file", "1", map[string]any{
		"path":       "a.go",
		"old_string": "x",
		"new_string": "y",
	}))
	tr.Apply(NewToolResult("edit_file", "1", map[string]any{"success": true}))
	tr.Apply(NewToolCall("read_file", "2", map[string]any{"path": "a.go"}))
	tr.Apply(NewToolResult("read_file", "2", "ok"))
	tr.Apply(NewText("Done."))
	tr.Apply(NewDone())

	// user, activity (2 steps), agent
	if len(tr.Items) != 3 {
		t.Fatalf("items=%d want 3: %#v", len(tr.Items), itemKinds(tr))
	}
	act := tr.Items[1]
	if act.Kind != ItemActivity {
		t.Fatalf("want activity, got %s", act.Kind)
	}
	if len(act.Steps) != 2 {
		t.Fatalf("steps=%d want 2", len(act.Steps))
	}
	if act.Steps[0].Status != "complete" || act.Steps[1].Status != "complete" {
		t.Fatalf("step status: %+v", act.Steps)
	}
}

func TestTranscript_StickyAgentReplacesBetweenTools(t *testing.T) {
	// Studio parity: agent text between tools is one sticky bubble that replaces.
	tr := NewTranscript()
	tr.Apply(NewUser("do stuff"))
	tr.Apply(NewText("I'll search first."))
	tr.Apply(NewToolCall("search_tools", "1", nil))
	tr.Apply(NewToolResult("search_tools", "1", "ok"))
	tr.Apply(NewText("Now running the tools.")) // replaces interstitial
	tr.Apply(NewToolCall("shell_command", "2", map[string]any{"command": "ls"}))
	tr.Apply(NewToolResult("shell_command", "2", "ok"))
	tr.Apply(NewToolCall("file_tree", "3", nil))
	tr.Apply(NewToolResult("file_tree", "3", "ok"))
	tr.Apply(NewText("Here are the results:\n\nAll done."))
	tr.Apply(NewDone())

	// user + one activity (all tools) + one agent
	if len(tr.Items) != 3 {
		t.Fatalf("items=%d want 3 (%s)", len(tr.Items), itemKinds(tr))
	}
	if tr.Items[0].Kind != ItemUser {
		t.Fatalf("0: %s", tr.Items[0].Kind)
	}
	if tr.Items[1].Kind != ItemActivity {
		t.Fatalf("1: %s", tr.Items[1].Kind)
	}
	if len(tr.Items[1].Steps) != 3 {
		t.Fatalf("steps=%d want 3", len(tr.Items[1].Steps))
	}
	if tr.Items[2].Kind != ItemAgent {
		t.Fatalf("2: %s", tr.Items[2].Kind)
	}
	if tr.Items[2].Content != "Here are the results:\n\nAll done." {
		t.Fatalf("final agent content: %q", tr.Items[2].Content)
	}
	if tr.Items[2].Provisional {
		t.Fatal("should be finalized")
	}
	// No leftover interstitial text as separate agents
	for i, it := range tr.Items {
		if it.Kind == ItemAgent && i != 2 {
			t.Fatalf("extra agent at %d", i)
		}
	}
}

func TestTranscript_AgentBeforeToolsMovesAfterActivity(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewUser("x"))
	tr.Apply(NewText("Let me look."))
	tr.Apply(NewToolCall("read_file", "1", nil))
	// Order should be user, activity, agent
	if itemKinds(tr) != "user,activity,agent" {
		t.Fatalf("kinds=%s", itemKinds(tr))
	}
	if !tr.Items[2].Provisional {
		t.Fatal("still provisional")
	}
	if tr.Items[2].Content != "Let me look." {
		t.Fatalf("content %q", tr.Items[2].Content)
	}
}

func TestTranscript_Approval(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewApproval("shell_command", map[string]any{"command": "ls"}, nil))
	if !tr.Awaiting {
		t.Fatal("expected awaiting approval")
	}
	if tr.ApprovalIdx < 0 {
		t.Fatal("expected approval idx")
	}
	item := tr.Items[tr.ApprovalIdx]
	if item.Kind != ItemApproval || len(item.Options) != 2 {
		t.Fatalf("approval item: %+v", item)
	}
	tr.ClearApproval()
	if tr.Awaiting || tr.ApprovalIdx != -1 {
		t.Fatal("clear approval failed")
	}
}

func TestTranscript_PartialTextDedupPattern(t *testing.T) {
	// Simulates backend only emitting partial chunks (no aggregate).
	tr := NewTranscript()
	tr.Apply(NewText("Hel"))
	tr.Apply(NewText("lo"))
	if got := tr.Items[0].Content; got != "Hello" {
		t.Fatalf("got %q", got)
	}
}

func TestTranscript_ErrorEndsStream(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewUser("x"))
	tr.Apply(NewError("boom"))
	if tr.Streaming {
		t.Fatal("error should end streaming")
	}
	if tr.Items[len(tr.Items)-1].Kind != ItemError {
		t.Fatal("last item should be error")
	}
}

func TestTranscript_ToggleLastActivity(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewToolCall("read_file", "1", nil))
	tr.Apply(NewToolResult("read_file", "1", "ok"))
	if tr.Items[0].Expanded {
		t.Fatal("default collapsed")
	}
	tr.ToggleLastActivity()
	if !tr.Items[0].Expanded {
		t.Fatal("expected expanded")
	}
}

func TestIsResultError(t *testing.T) {
	if !isResultError("Error: failed") {
		t.Fatal("string error")
	}
	if !isResultError(map[string]any{"success": false}) {
		t.Fatal("success false")
	}
	if isResultError(map[string]any{"success": true}) {
		t.Fatal("success true")
	}
}

func itemKinds(tr *Transcript) string {
	var b strings.Builder
	for i, it := range tr.Items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(string(it.Kind))
	}
	return b.String()
}
