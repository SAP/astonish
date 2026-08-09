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
	tr.Apply(NewToolResult("edit_file", "1", map[string]any{
		"success":              true,
		"verification_context": "@@ a.go:1\n- 1| x\n+ 1| y\n",
	}))
	tr.Apply(NewToolCall("read_file", "2", map[string]any{"path": "a.go"}))
	tr.Apply(NewToolResult("read_file", "2", "ok"))
	tr.Apply(NewText("Done."))
	tr.Apply(NewDone())

	// A tool that runs AFTER a code change starts a NEW activity fold so it
	// renders below the diff (not merged back into the pre-diff fold):
	//   user, activity(edit), file_diff, activity(read), agent
	if len(tr.Items) != 5 {
		t.Fatalf("items=%d want 5: %#v", len(tr.Items), itemKinds(tr))
	}
	if got := itemKinds(tr); got != "user,activity,file_diff,activity,agent" {
		t.Fatalf("kinds=%q want user,activity,file_diff,activity,agent", got)
	}
	editAct := tr.Items[1]
	if editAct.Kind != ItemActivity {
		t.Fatalf("want activity, got %s", editAct.Kind)
	}
	if len(editAct.Steps) != 1 || editAct.Steps[0].Name != "edit_file" {
		t.Fatalf("edit fold steps: %+v", editAct.Steps)
	}
	if editAct.Steps[0].Status != "complete" {
		t.Fatalf("edit step status: %+v", editAct.Steps)
	}
	diff := tr.Items[2]
	if diff.Kind != ItemFileDiff {
		t.Fatalf("want file_diff at [2], got %s", diff.Kind)
	}
	if diff.Path != "a.go" {
		t.Fatalf("file_diff path=%q", diff.Path)
	}
	if diff.DiffVerification == "" {
		t.Fatal("file_diff missing DiffVerification")
	}
	readAct := tr.Items[3]
	if readAct.Kind != ItemActivity {
		t.Fatalf("want activity at [3], got %s", readAct.Kind)
	}
	if len(readAct.Steps) != 1 || readAct.Steps[0].Name != "read_file" {
		t.Fatalf("read fold steps: %+v", readAct.Steps)
	}
	if readAct.Steps[0].Status != "complete" {
		t.Fatalf("read step status: %+v", readAct.Steps)
	}
	if tr.Items[4].Kind != ItemAgent {
		t.Fatalf("want agent last, got %s", tr.Items[4].Kind)
	}
}

func TestTranscript_FileDiffMainThread(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewUser("patch"))
	tr.Apply(NewToolCall("edit_file", "1", map[string]any{
		"path":       "/root/README.md",
		"old_string": "old",
		"new_string": "",
	}))
	tr.Apply(NewToolResult("edit_file", "1", map[string]any{
		"success":              true,
		"path":                 "/root/README.md",
		"verification_context": "@@ README.md:10\n- 10| old\n",
	}))
	tr.Apply(NewDone())

	kinds := itemKinds(tr)
	// user, activity, file_diff
	wantPrefix := string(ItemUser) + "," + string(ItemActivity) + "," + string(ItemFileDiff)
	if !strings.HasPrefix(kinds, wantPrefix) {
		t.Fatalf("kinds=%q want prefix %q", kinds, wantPrefix)
	}
	diffCount := strings.Count(kinds, string(ItemFileDiff))
	if diffCount != 1 {
		t.Fatalf("file_diff count=%d want 1 in %q", diffCount, kinds)
	}
}

// TestTranscript_ToolsInterleaveWithDiffs verifies that each code change closes
// the current activity fold so subsequent tools render below their diff, and a
// second edit produces its own diff after its own fold. Layout:
//
//	user, activity(edit1), file_diff(1), activity(shell), activity? ...
//
// More precisely with edit → shell → edit:
//
//	user, activity(edit1), file_diff, activity(shell+edit2)…
//
// The key invariants: tools after a diff never merge into the pre-diff fold, and
// each diff sits immediately after the fold that produced it, with the agent last.
func TestTranscript_ToolsInterleaveWithDiffs(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewUser("do a lot"))
	// edit A (produces diff A)
	tr.Apply(NewToolCall("edit_file", "1", map[string]any{"path": "a.go", "old_string": "x", "new_string": "y"}))
	tr.Apply(NewToolResult("edit_file", "1", map[string]any{
		"success": true, "path": "a.go", "verification_context": "@@ a.go:1\n- 1| x\n+ 1| y\n",
	}))
	// shell command after the diff (must start a new fold below diff A)
	tr.Apply(NewToolCall("shell_command", "2", map[string]any{"command": "go build"}))
	tr.Apply(NewToolResult("shell_command", "2", "ok"))
	// edit B (produces diff B, must land after the new fold)
	tr.Apply(NewToolCall("edit_file", "3", map[string]any{"path": "b.go", "old_string": "p", "new_string": "q"}))
	tr.Apply(NewToolResult("edit_file", "3", map[string]any{
		"success": true, "path": "b.go", "verification_context": "@@ b.go:1\n- 1| p\n+ 1| q\n",
	}))
	tr.Apply(NewText("All done."))
	tr.Apply(NewDone())

	// Expected: user, activity(edit A), file_diff(a), activity(shell + edit B),
	// file_diff(b), agent.
	want := "user,activity,file_diff,activity,file_diff,agent"
	if got := itemKinds(tr); got != want {
		t.Fatalf("kinds=%q want %q", got, want)
	}
	// The pre-diff fold holds only edit A (not shell / edit B).
	if len(tr.Items[1].Steps) != 1 || tr.Items[1].Steps[0].Name != "edit_file" {
		t.Fatalf("first fold should hold only edit A: %+v", tr.Items[1].Steps)
	}
	// diff A is for a.go, right after the first fold.
	if tr.Items[2].Kind != ItemFileDiff || tr.Items[2].Path != "a.go" {
		t.Fatalf("diff A: kind=%s path=%q", tr.Items[2].Kind, tr.Items[2].Path)
	}
	// The second fold holds the shell command and edit B (tools after diff A).
	names := []string{}
	for _, s := range tr.Items[3].Steps {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "shell_command,edit_file" {
		t.Fatalf("second fold steps=%v want shell_command,edit_file", names)
	}
	// diff B is for b.go, after the second fold.
	if tr.Items[4].Kind != ItemFileDiff || tr.Items[4].Path != "b.go" {
		t.Fatalf("diff B: kind=%s path=%q", tr.Items[4].Kind, tr.Items[4].Path)
	}
	if tr.Items[5].Kind != ItemAgent {
		t.Fatalf("want agent last, got %s", tr.Items[5].Kind)
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

func TestTranscript_AuthorizationApproval(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewAuthorizationApproval(
		"write_file",
		map[string]any{"file_path": "x.go"},
		[]string{"Allow", "Always Allow", "Deny"},
		"tool", nil,
	))
	if !tr.Awaiting || tr.ApprovalIdx < 0 {
		t.Fatal("expected awaiting authorization")
	}
	if tr.ApprovalCursor != 0 {
		t.Fatalf("expected default approval cursor 0, got %d", tr.ApprovalCursor)
	}
	item := tr.Items[tr.ApprovalIdx]
	if item.Kind != ItemApproval || item.ApprovalKind != "tool" || len(item.Options) != 3 {
		t.Fatalf("tool authorization item: %+v", item)
	}

	tr2 := NewTranscript()
	tr2.Apply(NewAuthorizationApproval(
		"read_file",
		map[string]any{"path": "/etc/hosts"},
		[]string{"Allow", "Always Allow", "Deny"},
		"folder", []string{"/etc/hosts"},
	))
	fitem := tr2.Items[tr2.ApprovalIdx]
	if fitem.ApprovalKind != "folder" || len(fitem.Paths) != 1 || fitem.Paths[0] != "/etc/hosts" {
		t.Fatalf("folder authorization item: %+v", fitem)
	}
}

func TestTranscript_NetworkDenial(t *testing.T) {
	tr := NewTranscript()
	denials := []NetworkDenial{{Host: "api.example.com", Port: 443, ChunkID: "chunk-1"}}
	tr.Apply(NewNetworkDenial("sess-1", "sandbox-1", denials))
	if !tr.Awaiting {
		t.Fatal("expected awaiting network authorization")
	}
	if tr.ApprovalIdx < 0 {
		t.Fatal("expected approval idx")
	}
	item := tr.Items[tr.ApprovalIdx]
	if item.Kind != ItemNetworkDenial || item.SessionID != "sess-1" || item.SandboxName != "sandbox-1" {
		t.Fatalf("network denial item: %+v", item)
	}
	if len(item.NetworkDenials) != 1 || item.NetworkDenials[0].Host != "api.example.com" {
		t.Fatalf("denials: %+v", item.NetworkDenials)
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

func TestTranscript_UsageAccumulates(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(Event{Kind: KindUsage, Usage: &Usage{Input: 100, Output: 50, Total: 150}})
	tr.Apply(Event{Kind: KindUsage, Usage: &Usage{Input: 200, Output: 100, Total: 300}})
	if tr.LastUsage == nil {
		t.Fatal("expected usage")
	}
	if tr.LastUsage.Input != 300 || tr.LastUsage.Output != 150 || tr.LastUsage.Total != 450 {
		t.Fatalf("usage=%+v", tr.LastUsage)
	}
}

func TestTranscript_ContextTokensTracksMax(t *testing.T) {
	tr := NewTranscript()
	// A tool loop: prompt grows each call. Context occupancy is the largest.
	tr.Apply(Event{Kind: KindUsage, Usage: &Usage{Input: 1000, Output: 200, Total: 1200}})
	tr.Apply(Event{Kind: KindUsage, Usage: &Usage{Input: 3000, Output: 400, Total: 3400}})
	tr.Apply(Event{Kind: KindUsage, Usage: &Usage{Input: 0, Output: 0, Total: 0}}) // stray zero
	if tr.ContextTokens != 3400 {
		t.Fatalf("ContextTokens=%d want 3400 (max seen)", tr.ContextTokens)
	}
	// Cumulative usage still accumulates independently.
	if tr.LastUsage.Total != 4600 {
		t.Fatalf("cumulative usage=%d want 4600", tr.LastUsage.Total)
	}
	// Total==0 event with input/output falls back to input+output.
	tr.Apply(Event{Kind: KindUsage, Usage: &Usage{Input: 4000, Output: 500}})
	if tr.ContextTokens != 4500 {
		t.Fatalf("ContextTokens=%d want 4500", tr.ContextTokens)
	}
	tr.Reset()
	if tr.ContextTokens != 0 {
		t.Fatalf("ContextTokens after reset=%d want 0", tr.ContextTokens)
	}
}

func TestTranscript_EstimatedUsageUpdatesContextOnly(t *testing.T) {
	tr := NewTranscript()
	// A real reading accumulates into cumulative usage.
	tr.Apply(Event{Kind: KindUsage, Usage: &Usage{Input: 100, Output: 50, Total: 150}})
	// An estimated reading (provider returned no usage) must update the context
	// occupancy figure but NOT inflate cumulative usage — each estimate is the
	// full current context, not a per-call delta.
	tr.Apply(Event{Kind: KindUsage, Usage: &Usage{Input: 80000, Estimated: true}})
	tr.Apply(Event{Kind: KindUsage, Usage: &Usage{Input: 90000, Estimated: true}})

	if tr.ContextTokens != 90000 {
		t.Fatalf("ContextTokens=%d want 90000 (latest estimate)", tr.ContextTokens)
	}
	// Estimates are authoritative snapshots and must be able to move DOWN (e.g.
	// after compaction shrinks the context), unlike provider per-call usage.
	tr.Apply(Event{Kind: KindUsage, Usage: &Usage{Input: 30000, Estimated: true}})
	if tr.ContextTokens != 30000 {
		t.Fatalf("ContextTokens=%d want 30000 (estimate can decrease after compaction)", tr.ContextTokens)
	}
	if tr.LastUsage.Total != 150 {
		t.Fatalf("cumulative usage=%d want 150 (estimates must not accumulate)", tr.LastUsage.Total)
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
	if !isResultError("failed to write to file /tmp/x: permission denied") {
		t.Fatal("failed to write string")
	}
	if !isResultError(map[string]any{"success": false}) {
		t.Fatal("success false")
	}
	if isResultError(map[string]any{"success": true}) {
		t.Fatal("success true")
	}
}

func TestTranscript_FailedWriteNoFileDiff(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewUser("save it"))
	// Failed write: error result, but args still carry the intended content.
	tr.Apply(NewToolCall("write_file", "1", map[string]any{
		"file_path": "/root/forbidden/out.md",
		"content":   "# Should not appear as main-thread diff\n",
	}))
	tr.Apply(NewToolResult("write_file", "1",
		"failed to write to file /root/forbidden/out.md: permission denied"))
	// Successful write to a different path.
	tr.Apply(NewToolCall("write_file", "2", map[string]any{
		"file_path": "/root/ok/out.md",
		"content":   "# real content\n",
	}))
	tr.Apply(NewToolResult("write_file", "2", map[string]any{
		"message":              "Created /root/ok/out.md",
		"path":                 "/root/ok/out.md",
		"created":              true,
		"verification_context": "@@ out.md:1 (created)\n+ 1| # real content\n",
	}))
	tr.Apply(NewDone())

	diffCount := 0
	var paths []string
	for _, it := range tr.Items {
		if it.Kind == ItemFileDiff {
			diffCount++
			paths = append(paths, it.Path)
		}
	}
	if diffCount != 1 {
		t.Fatalf("file_diff count=%d want 1 (failed write must not emit), kinds=%s paths=%v",
			diffCount, itemKinds(tr), paths)
	}
	if paths[0] != "/root/ok/out.md" {
		t.Fatalf("path=%q want successful path", paths[0])
	}
	// Activity step for the failure is still present with error status.
	var act *Item
	for i := range tr.Items {
		if tr.Items[i].Kind == ItemActivity {
			act = &tr.Items[i]
			break
		}
	}
	if act == nil || len(act.Steps) < 2 {
		t.Fatalf("want activity with 2 steps, got %#v", act)
	}
	if act.Steps[0].Status != "error" {
		t.Fatalf("first write status=%q want error", act.Steps[0].Status)
	}
}

func TestTranscript_NoDiffWithoutVerificationContext(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewUser("edit"))
	// Complete-looking result but no verification_context (and no success field).
	// Must not invent a main-thread diff from args alone.
	tr.Apply(NewToolCall("edit_file", "1", map[string]any{
		"path":       "a.go",
		"old_string": "x",
		"new_string": "y",
	}))
	tr.Apply(NewToolResult("edit_file", "1", map[string]any{
		"message": "ok",
	}))
	tr.Apply(NewDone())
	for _, it := range tr.Items {
		if it.Kind == ItemFileDiff {
			t.Fatalf("unexpected file_diff without verification_context: %#v", it)
		}
	}
}

func TestTranscript_LoadHistory_Sticky(t *testing.T) {
	tr := NewTranscript()
	tr.LoadHistory([]HistoryMsg{
		{Kind: "user", Text: "hi"},
		{Kind: "agent", Text: "thinking out loud"},
		{Kind: "tool_call", ToolName: "read_file", ToolID: "1"},
		{Kind: "tool_result", ToolName: "read_file", ToolID: "1", Result: "ok"},
		{Kind: "agent", Text: "final answer"},
	})
	if itemKinds(tr) != "user,activity,agent" {
		t.Fatalf("kinds=%s", itemKinds(tr))
	}
	if tr.Items[2].Content != "final answer" {
		t.Fatalf("agent=%q", tr.Items[2].Content)
	}
	if tr.Items[2].Provisional {
		t.Fatal("history agent should not be provisional")
	}
	if tr.Streaming {
		t.Fatal("not streaming after load")
	}
}

func TestLinearThread_MessageToolMessageToolOrder(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true
	tr.Apply(NewUser("do it"))
	tr.Apply(NewText("First, let me look."))
	tr.Apply(NewToolCall("read_file", "1", map[string]any{"path": "a.go"}))
	tr.Apply(NewToolResult("read_file", "1", "ok"))
	tr.Apply(NewText("Now I'll run something."))
	tr.Apply(NewToolCall("shell_command", "2", map[string]any{"command": "ls"}))
	tr.Apply(NewToolResult("shell_command", "2", "ok"))
	tr.Apply(NewDone())

	// Chronological thread: user, agent, activity, agent, activity.
	if got := itemKinds(tr); got != "user,agent,activity,agent,activity" {
		t.Fatalf("kinds=%q want user,agent,activity,agent,activity", got)
	}
	if tr.Items[1].Content != "First, let me look." {
		t.Fatalf("first agent=%q", tr.Items[1].Content)
	}
	if tr.Items[3].Content != "Now I'll run something." {
		t.Fatalf("second agent=%q", tr.Items[3].Content)
	}
	// Messages must be permanent, never provisional/collapsed/replaced.
	if tr.Items[1].Provisional || tr.Items[3].Provisional {
		t.Fatal("linear agents must never be provisional")
	}
}

func TestLinearThread_MessageBreaksToolGroup(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true
	tr.Apply(NewUser("go"))
	tr.Apply(NewToolCall("read_file", "1", map[string]any{"path": "a.go"}))
	tr.Apply(NewToolResult("read_file", "1", "ok"))
	tr.Apply(NewToolCall("read_file", "2", map[string]any{"path": "b.go"}))
	tr.Apply(NewToolResult("read_file", "2", "ok"))
	tr.Apply(NewText("Found the issue."))
	tr.Apply(NewToolCall("read_file", "3", map[string]any{"path": "c.go"}))
	tr.Apply(NewToolResult("read_file", "3", "ok"))
	tr.Apply(NewDone())

	// A message between tool groups forces a fresh fold:
	//   user, activity(2 steps), agent, activity(1 step)
	if got := itemKinds(tr); got != "user,activity,agent,activity" {
		t.Fatalf("kinds=%q want user,activity,agent,activity", got)
	}
	if len(tr.Items[1].Steps) != 2 {
		t.Fatalf("first fold steps=%d want 2", len(tr.Items[1].Steps))
	}
	if len(tr.Items[3].Steps) != 1 {
		t.Fatalf("second fold steps=%d want 1", len(tr.Items[3].Steps))
	}
}

func TestLinearThread_InterstitialTextNonProvisional(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true
	tr.Apply(NewUser("go"))
	tr.Apply(NewToolCall("read_file", "1", map[string]any{"path": "a.go"}))
	tr.Apply(NewToolResult("read_file", "1", "ok"))
	tr.Apply(NewText("Mid-loop thought."))

	// The interstitial text is immediately its own permanent bubble.
	if got := itemKinds(tr); got != "user,activity,agent" {
		t.Fatalf("kinds=%q want user,activity,agent", got)
	}
	if tr.Items[2].Provisional {
		t.Fatal("linear interstitial text must not be provisional")
	}
	if tr.Items[2].Content != "Mid-loop thought." {
		t.Fatalf("agent=%q", tr.Items[2].Content)
	}
}

func TestLinearThread_LoadHistoryKeepsSeparateMessages(t *testing.T) {
	tr := NewTranscript()
	tr.LinearThread = true
	tr.LoadHistory([]HistoryMsg{
		{Kind: "user", Text: "hi"},
		{Kind: "agent", Text: "thinking out loud"},
		{Kind: "tool_call", ToolName: "read_file", ToolID: "1"},
		{Kind: "tool_result", ToolName: "read_file", ToolID: "1", Result: "ok"},
		{Kind: "agent", Text: "final answer"},
	})
	// Each historical agent message stays its own bubble:
	//   user, agent, activity, agent
	if got := itemKinds(tr); got != "user,agent,activity,agent" {
		t.Fatalf("kinds=%q want user,agent,activity,agent", got)
	}
	if tr.Items[1].Content != "thinking out loud" {
		t.Fatalf("first agent=%q", tr.Items[1].Content)
	}
	if tr.Items[3].Content != "final answer" {
		t.Fatalf("second agent=%q", tr.Items[3].Content)
	}
	if tr.Items[1].Provisional || tr.Items[3].Provisional {
		t.Fatal("history agents should not be provisional")
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

func TestTranscript_ApprovalQueueing(t *testing.T) {
	tr := NewTranscript()

	// First approval arrives — should become active.
	tr.Apply(NewAuthorizationApproval(
		"edit_file",
		map[string]any{"path": "app.go"},
		[]string{"Allow", "Always Allow", "Deny"},
		"tool", nil,
	))
	if !tr.Awaiting {
		t.Fatal("expected awaiting after first approval")
	}
	firstIdx := tr.ApprovalIdx
	if firstIdx < 0 {
		t.Fatal("expected valid ApprovalIdx for first approval")
	}
	if tr.Items[firstIdx].ToolName != "edit_file" {
		t.Fatalf("first active approval should be edit_file, got %q", tr.Items[firstIdx].ToolName)
	}

	// Second approval arrives while first is still pending — should NOT
	// overwrite the active index.
	tr.Apply(NewApproval("announce_plan", nil, []string{"Approve & implement", "Request changes", "Decline"}))
	if tr.ApprovalIdx != firstIdx {
		t.Fatalf("second approval should not overwrite active idx: got %d want %d", tr.ApprovalIdx, firstIdx)
	}
	if tr.Items[tr.ApprovalIdx].ToolName != "edit_file" {
		t.Fatal("active approval should still be edit_file")
	}

	// Clear the first approval — the second should become active.
	tr.ClearApproval()
	if !tr.Awaiting {
		t.Fatal("expected awaiting after clearing first approval (second is queued)")
	}
	if tr.ApprovalIdx < 0 {
		t.Fatal("expected valid ApprovalIdx for queued approval")
	}
	if tr.Items[tr.ApprovalIdx].ToolName != "announce_plan" {
		t.Fatalf("queued approval should now be active: got %q", tr.Items[tr.ApprovalIdx].ToolName)
	}

	// Clear the second — no more approvals.
	tr.ClearApproval()
	if tr.Awaiting {
		t.Fatal("should not be awaiting after clearing all approvals")
	}
	if tr.ApprovalIdx != -1 {
		t.Fatalf("ApprovalIdx should be -1, got %d", tr.ApprovalIdx)
	}
}

func TestTranscriptDelegationStart(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewUser("do something"))
	tr.Apply(NewDelegationStart([]DelegationTask{
		{Name: "researcher", Description: "Research the topic"},
		{Name: "coder", Description: "Write the code"},
		{Name: "reviewer", Description: "Review the output"},
	}))

	if !tr.DelegationActive {
		t.Fatal("DelegationActive should be true after delegation start")
	}
	if len(tr.Delegation) != 3 {
		t.Fatalf("expected 3 delegation tasks, got %d", len(tr.Delegation))
	}
	if tr.Delegation[0].Name != "researcher" {
		t.Fatalf("first task name=%q want researcher", tr.Delegation[0].Name)
	}
	for i, task := range tr.Delegation {
		if task.Status != "running" {
			t.Fatalf("task %d status=%q want running", i, task.Status)
		}
		if task.StartedAt.IsZero() {
			t.Fatalf("task %d StartedAt should be set", i)
		}
	}
	if tr.Status != "Delegating tasks…" {
		t.Fatalf("status=%q want 'Delegating tasks…'", tr.Status)
	}
	// Verify an ItemDelegation was inserted into Items.
	found := false
	for _, it := range tr.Items {
		if it.Kind == ItemDelegation {
			found = true
			if len(it.DelegationTasks) != 3 {
				t.Fatalf("ItemDelegation should have 3 tasks, got %d", len(it.DelegationTasks))
			}
			if it.DelegationTasks[0].Name != "researcher" {
				t.Fatalf("ItemDelegation task[0] name=%q want researcher", it.DelegationTasks[0].Name)
			}
		}
	}
	if !found {
		t.Fatal("expected an ItemDelegation item in transcript Items")
	}
}

func TestTranscriptDelegationTaskComplete(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewDelegationStart([]DelegationTask{
		{Name: "researcher", Description: "Research"},
		{Name: "coder", Description: "Code"},
	}))
	tr.Apply(NewDelegationTaskUpdate("task_complete", "researcher", "5.2s", ""))

	if tr.Delegation[0].Status != "complete" {
		t.Fatalf("researcher status=%q want complete", tr.Delegation[0].Status)
	}
	if tr.Delegation[0].Duration != "5.2s" {
		t.Fatalf("researcher duration=%q want 5.2s", tr.Delegation[0].Duration)
	}
	// The other task should still be running.
	if tr.Delegation[1].Status != "running" {
		t.Fatalf("coder status=%q want running", tr.Delegation[1].Status)
	}
	// Verify the inline Item is also updated.
	for _, it := range tr.Items {
		if it.Kind == ItemDelegation {
			if it.DelegationTasks[0].Status != "complete" {
				t.Fatalf("ItemDelegation task[0] status=%q want complete", it.DelegationTasks[0].Status)
			}
			if it.DelegationTasks[0].Duration != "5.2s" {
				t.Fatalf("ItemDelegation task[0] duration=%q want 5.2s", it.DelegationTasks[0].Duration)
			}
		}
	}
}

func TestTranscriptDelegationTaskFailed(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewDelegationStart([]DelegationTask{
		{Name: "researcher", Description: "Research"},
	}))
	tr.Apply(NewDelegationTaskUpdate("task_failed", "researcher", "10s", "timeout"))

	if tr.Delegation[0].Status != "failed" {
		t.Fatalf("researcher status=%q want failed", tr.Delegation[0].Status)
	}
	if tr.Delegation[0].Error != "timeout" {
		t.Fatalf("researcher error=%q want timeout", tr.Delegation[0].Error)
	}
	// Verify the inline Item is also updated.
	for _, it := range tr.Items {
		if it.Kind == ItemDelegation {
			if it.DelegationTasks[0].Status != "failed" {
				t.Fatalf("ItemDelegation task[0] status=%q want failed", it.DelegationTasks[0].Status)
			}
		}
	}
}

func TestTranscriptDelegationDone(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewDelegationStart([]DelegationTask{
		{Name: "researcher", Description: "Research"},
	}))
	if !tr.DelegationActive {
		t.Fatal("DelegationActive should be true")
	}

	tr.Apply(NewDelegation("done"))
	if tr.DelegationActive {
		t.Fatal("DelegationActive should be false after 'done'")
	}
	// The ItemDelegation should remain in Items (persists in the thread).
	found := false
	for _, it := range tr.Items {
		if it.Kind == ItemDelegation {
			found = true
		}
	}
	if !found {
		t.Fatal("ItemDelegation should remain in Items after 'done'")
	}
}

func TestTranscriptKindDoneClearsDelegation(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(NewDelegationStart([]DelegationTask{
		{Name: "researcher", Description: "Research"},
	}))
	tr.Apply(NewDone())

	if tr.DelegationActive {
		t.Fatal("DelegationActive should be false after KindDone")
	}
	if tr.Delegation != nil {
		t.Fatal("Delegation should be nil after KindDone")
	}
	// The ItemDelegation should persist in Items (visible in transcript history).
	found := false
	for _, it := range tr.Items {
		if it.Kind == ItemDelegation {
			found = true
		}
	}
	if !found {
		t.Fatal("ItemDelegation should remain in Items after KindDone")
	}
}
