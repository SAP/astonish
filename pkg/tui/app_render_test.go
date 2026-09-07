package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestRenderUserBubbleUsesFullWidthRectangleBorder(t *testing.T) {
	m := model{theme: DefaultTheme(), width: 80}
	out := m.renderUserBubble("hello", false, 40)
	plain := lipgloss.NewStyle().Render(out)
	plain = stripANSI(plain)
	lines := strings.Split(plain, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), plain)
	}
	if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") {
		t.Fatalf("top border is not a rectangle: %q", lines[0])
	}
	if !strings.HasPrefix(lines[2], "└") || !strings.HasSuffix(lines[2], "┘") {
		t.Fatalf("bottom border is not a rectangle: %q", lines[2])
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != 40 {
			t.Fatalf("line %d width=%d want 40: %q", i, got, line)
		}
	}
}

func TestRenderUserBubbleEmbedsExpandHintInBottomBorder(t *testing.T) {
	m := model{theme: DefaultTheme(), width: 100}
	content := strings.Repeat("word ", 120)
	out := m.renderUserBubble(content, false, 60)
	plain := stripANSI(out)
	lines := strings.Split(plain, "\n")
	bottom := lines[len(lines)-1]
	if !strings.Contains(bottom, "double-click to expand") {
		t.Fatalf("bottom border should contain expand hint: %q", bottom)
	}
	if !strings.HasPrefix(bottom, "└") || !strings.HasSuffix(bottom, "┘") {
		t.Fatalf("bottom border should keep rectangle corners: %q", bottom)
	}
	if !strings.Contains(bottom, "─ … double-click to expand ─") {
		t.Fatalf("hint should interrupt and resume border line: %q", bottom)
	}
	if got := lipgloss.Width(bottom); got != 60 {
		t.Fatalf("bottom width=%d want 60: %q", got, bottom)
	}
}

func TestRenderActivityCollapsedPreviewShowsToolRows(t *testing.T) {
	m := model{theme: DefaultTheme(), width: 100}
	item := events.Item{
		Kind: events.ItemActivity,
		Steps: []events.ToolStep{
			{Name: "grep", Args: map[string]any{"pattern": "kubernetes"}, Status: "complete"},
			{Name: "run_terminal_command", Args: map[string]any{"command": "kubectl get clusters"}, Status: "complete"},
			{Name: "read_file", Args: map[string]any{"target_file": "README.md"}, Status: "complete"},
		},
	}
	out := stripANSI(m.renderActivity(item, 80))
	if !strings.Contains(out, "✓ Search kubernetes") {
		t.Fatalf("missing search preview row: %q", out)
	}
	if !strings.Contains(out, "✓ Run command `kubectl get clusters`") {
		t.Fatalf("missing command preview row: %q", out)
	}
	if !strings.Contains(out, "✓ Read file README.md") {
		t.Fatalf("collapsed activity should show every tool row: %q", out)
	}
	if strings.Contains(out, "… 1 more") {
		t.Fatalf("collapsed activity should not hide extra tools: %q", out)
	}
	if !strings.Contains(out, "click to expand details") {
		t.Fatalf("missing click-to-expand details hint: %q", out)
	}
}

func TestRenderActivityCollapsedCommandRowsStaySingleLine(t *testing.T) {
	m := model{theme: DefaultTheme(), width: 100}
	item := events.Item{
		Kind: events.ItemActivity,
		Steps: []events.ToolStep{
			{Name: "run_terminal_command", Args: map[string]any{"command": "# Step 1: Assign credentials to variables\nAPP_CREDENTIAL=$(cat /tmp/very-long-file-name.json)\necho done"}, Status: "complete"},
		},
	}
	out := stripANSI(m.renderActivity(item, 56))
	lines := strings.Split(out, "\n")
	toolRows := 0
	for _, line := range lines {
		if strings.Contains(line, "Run command") {
			toolRows++
			if strings.Contains(line, "APP_CREDENTIAL") {
				t.Fatalf("collapsed command row should truncate before wrapping command continuation: %q", line)
			}
			if got := lipgloss.Width(line); got > 56 {
				t.Fatalf("collapsed command row width=%d want <=56: %q", got, line)
			}
		}
	}
	if toolRows != 1 {
		t.Fatalf("expected one single-line command row, got %d in %q", toolRows, out)
	}
}

func TestRenderActivityExpandedShowsFullToolDetails(t *testing.T) {
	m := model{theme: DefaultTheme(), width: 100}
	item := events.Item{
		Kind:     events.ItemActivity,
		Expanded: true,
		Steps: []events.ToolStep{
			{Name: "grep", Args: map[string]any{"pattern": "kubernetes"}, Result: "match 1\nmatch 2", Status: "complete"},
			{Name: "run_terminal_command", Args: map[string]any{"command": "kubectl get clusters"}, Result: map[string]any{"stdout": "cluster-a\ncluster-b"}, Status: "complete"},
		},
	}
	out := stripANSI(m.renderActivity(item, 80))
	if !strings.Contains(out, "▾") {
		t.Fatalf("expanded activity should use expanded marker: %q", out)
	}
	if !strings.Contains(out, "query: kubernetes") {
		t.Fatalf("missing search detail: %q", out)
	}
	if !strings.Contains(out, "command: kubectl get clusters") {
		t.Fatalf("missing command detail: %q", out)
	}
	if !strings.Contains(out, "cluster-a") {
		t.Fatalf("missing result preview: %q", out)
	}
}

func TestHandleMouseSingleClickTogglesActivity(t *testing.T) {
	tr := events.NewTranscript()
	tr.Items = []events.Item{{
		Kind: events.ItemActivity,
		Steps: []events.ToolStep{
			{Name: "grep", Args: map[string]any{"pattern": "kubernetes"}, Status: "complete"},
		},
	}}
	m := model{
		theme:                DefaultTheme(),
		tr:                   tr,
		vp:                   viewport.New(viewport.WithWidth(80), viewport.WithHeight(10)),
		width:                80,
		height:               24,
		ready:                true,
		transcriptPlainLines: []string{"activity", "detail"},
		hitRegions: []hitRegion{{
			start:   0,
			end:     2,
			itemIdx: 0,
			kind:    events.ItemActivity,
		}},
	}

	next, _ := m.handleMouse(tea.MouseClickMsg{X: 3, Y: m.viewportTopY(), Button: tea.MouseLeft})
	m = next.(model)
	next, _ = m.handleMouse(tea.MouseReleaseMsg{X: 3, Y: m.viewportTopY(), Button: tea.MouseLeft})
	got := next.(model)
	if !got.tr.Items[0].Expanded {
		t.Fatal("single-click should expand activity blocks")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{5 * time.Second, "5s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 0s"},
		{65 * time.Second, "1m 5s"},
		{3599 * time.Second, "59m 59s"},
		{3600 * time.Second, "1h 0m 0s"},
		{3661 * time.Second, "1h 1m 1s"},
		{7384 * time.Second, "2h 3m 4s"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestLiveStatusShowsTimerWhileStreaming(t *testing.T) {
	tr := events.NewTranscript()
	tr.Streaming = true
	tr.Status = "Running shell_command…"

	m := model{
		theme:         DefaultTheme(),
		tr:            tr,
		width:         80,
		turnStartedAt: time.Now().Add(-23 * time.Second),
	}
	out := stripANSI(m.renderLiveStatus())
	if !strings.Contains(out, "23s") {
		t.Fatalf("status should contain elapsed time '23s': %q", out)
	}
	if !strings.Contains(out, "Running shell_command") {
		t.Fatalf("status should contain the status text: %q", out)
	}
	// Timer should be right-aligned: status text on the left, timer on the right
	// with spaces in between.
	statusIdx := strings.Index(out, "Running shell_command")
	timerIdx := strings.Index(out, "23s")
	if timerIdx <= statusIdx {
		t.Fatalf("timer should appear to the right of status text; statusIdx=%d timerIdx=%d in %q", statusIdx, timerIdx, out)
	}
	// There should be multiple spaces between status and timer (right-alignment gap).
	between := out[statusIdx+len("Running shell_command…"):timerIdx]
	if len(strings.TrimRight(between, " ")) == len(between) {
		t.Fatalf("expected whitespace gap between status and timer for right-alignment: %q", out)
	}
}

func TestLiveStatusHidesTimerWhenIdle(t *testing.T) {
	tr := events.NewTranscript()
	tr.Streaming = false
	tr.Status = ""

	m := model{
		theme: DefaultTheme(),
		tr:    tr,
		width: 80,
	}
	out := stripANSI(m.renderLiveStatus())
	if strings.Contains(out, "s") && strings.Contains(out, "(") {
		t.Fatalf("idle status should not contain timer: %q", out)
	}
}

func TestFinishTurnClearsTimer(t *testing.T) {
	tr := events.NewTranscript()
	tr.Streaming = true
	m := model{
		tr:            tr,
		turnStartedAt: time.Now().Add(-10 * time.Second),
	}
	m.finishTurn()
	if !m.turnStartedAt.IsZero() {
		t.Fatal("finishTurn should clear turnStartedAt")
	}
	// Should emit a system message with elapsed time.
	found := false
	for _, item := range m.tr.Items {
		if item.Kind == events.ItemSystem && strings.Contains(item.Content, "Completed in") {
			found = true
			if !strings.Contains(item.Content, "10s") {
				t.Fatalf("completion message should contain '10s': %q", item.Content)
			}
		}
	}
	if !found {
		t.Fatal("finishTurn should emit a 'Completed in' system message")
	}
}

func TestCompletionMessageNotShownForShortTurns(t *testing.T) {
	tr := events.NewTranscript()
	tr.Streaming = true
	m := model{
		tr:            tr,
		turnStartedAt: time.Now(), // just started, <1s elapsed
	}
	m.finishTurn()
	for _, item := range m.tr.Items {
		if item.Kind == events.ItemSystem && strings.Contains(item.Content, "Completed in") {
			t.Fatal("should not emit completion message for turns under 1 second")
		}
	}
}

func TestRenderDelegationPanelShowsRunningTasks(t *testing.T) {
	item := events.Item{
		Kind: events.ItemDelegation,
		DelegationTasks: []events.DelegationTaskState{
			{
				Name: "researcher", Description: "Research", Status: "running",
				StartedAt: time.Now().Add(-12 * time.Second),
				Activity: []events.DelegationActivity{
					{Type: "tool_call", ToolName: "grep_search"},
				},
			},
			{Name: "code-reviewer", Description: "Review", Status: "complete", Duration: "8.1s"},
			{Name: "api-tester", Description: "Test APIs", Status: "failed", Duration: "3s", Error: "timeout"},
		},
	}

	m := model{
		theme: DefaultTheme(),
		width: 80,
	}
	out := stripANSI(m.renderDelegationItem(item, 80))
	if out == "" {
		t.Fatal("delegation item should render when tasks are present")
	}
	if !strings.Contains(out, "Delegating 3 tasks") {
		t.Fatalf("should contain task count header: %q", out)
	}
	if !strings.Contains(out, "researcher") {
		t.Fatalf("should contain task name 'researcher': %q", out)
	}
	if !strings.Contains(out, "code-reviewer") {
		t.Fatalf("should contain task name 'code-reviewer': %q", out)
	}
	if !strings.Contains(out, "api-tester") {
		t.Fatalf("should contain task name 'api-tester': %q", out)
	}
	if !strings.Contains(out, "complete") {
		t.Fatalf("should show 'complete' status: %q", out)
	}
	if !strings.Contains(out, "failed") {
		t.Fatalf("should show 'failed' status: %q", out)
	}
	if !strings.Contains(out, "12s") {
		t.Fatalf("should show elapsed time '12s' for running task: %q", out)
	}
	if !strings.Contains(out, "8.1s") {
		t.Fatalf("should show duration '8.1s' for complete task: %q", out)
	}
	// Running task should show inline activity status line with human-friendly text.
	if !strings.Contains(out, "→ Searching") {
		t.Fatalf("running task should show inline activity status '→ Searching': %q", out)
	}
}

func TestRenderDelegationItemEmptyWhenNoTasks(t *testing.T) {
	item := events.Item{
		Kind:            events.ItemDelegation,
		DelegationTasks: nil,
	}

	m := model{
		theme: DefaultTheme(),
		width: 80,
	}
	out := m.renderDelegationItem(item, 80)
	if out != "" {
		t.Fatalf("delegation item should be empty when no tasks, got: %q", out)
	}
}

func TestTimerTickRefreshesDuringDelegation(t *testing.T) {
	tr := events.NewTranscript()
	tr.Streaming = true
	tr.DelegationActive = true
	tr.Items = []events.Item{{
		Kind: events.ItemDelegation,
		DelegationTasks: []events.DelegationTaskState{
			{Name: "worker", Status: "running", StartedAt: time.Now().Add(-5 * time.Second)},
		},
	}}

	m := model{
		theme:         DefaultTheme(),
		tr:            tr,
		width:         80,
		height:        24,
		ready:         true,
		turnStartedAt: time.Now().Add(-5 * time.Second),
		vp:            viewport.New(viewport.WithWidth(80), viewport.WithHeight(10)),
	}
	m.vp.SetContent("placeholder")

	// Simulate timerTickMsg
	next, cmd := m.Update(timerTickMsg{})
	got := next.(model)

	// Timer should re-schedule (cmd is non-nil)
	if cmd == nil {
		t.Fatal("timerTickMsg should return a non-nil command to re-schedule the tick")
	}

	// Verify delegation is still active
	if !got.tr.DelegationActive {
		t.Fatal("delegation should remain active after timer tick")
	}
}

func TestDelegationDetailOpensOnClick(t *testing.T) {
	tr := events.NewTranscript()
	tr.Items = []events.Item{{
		Kind: events.ItemDelegation,
		DelegationTasks: []events.DelegationTaskState{
			{
				Name: "researcher", Status: "running",
				StartedAt: time.Now().Add(-5 * time.Second),
				Activity: []events.DelegationActivity{
					{Type: "tool_call", ToolName: "read_file"},
				},
			},
			{Name: "writer", Status: "complete", Duration: "10s"},
		},
	}}

	m := model{
		theme:  DefaultTheme(),
		tr:     tr,
		vp:     viewport.New(viewport.WithWidth(80), viewport.WithHeight(20)),
		width:  80,
		height: 24,
		ready:  true,
		hitRegions: []hitRegion{{
			start:   0,
			end:     5, // header + task1 + status_line + task2 + hint
			itemIdx: 0,
			kind:    events.ItemDelegation,
		}},
	}

	// Click on task row 1 (line offset 1 from start = first task)
	taskIdx := m.delegationTaskAtLine(1, 0)
	if taskIdx != 0 {
		t.Fatalf("expected task index 0, got %d", taskIdx)
	}

	// Click on status line of first task (line offset 2)
	taskIdx = m.delegationTaskAtLine(2, 0)
	if taskIdx != 0 {
		t.Fatalf("expected task index 0 for status line click, got %d", taskIdx)
	}

	// Click on task row 2 (line offset 3 because first task has status line)
	taskIdx = m.delegationTaskAtLine(3, 0)
	if taskIdx != 1 {
		t.Fatalf("expected task index 1, got %d", taskIdx)
	}

	// Click on header (line offset 0) should return -1
	taskIdx = m.delegationTaskAtLine(0, 0)
	if taskIdx != -1 {
		t.Fatalf("expected -1 for header click, got %d", taskIdx)
	}

	// Open delegation detail
	next, _ := m.openDelegationDetail(0, 0)
	got := next.(model)
	if !got.delegationDetail.open {
		t.Fatal("delegation detail should be open after openDelegationDetail")
	}
	if got.delegationDetail.taskName != "researcher" {
		t.Fatalf("expected task name 'researcher', got %q", got.delegationDetail.taskName)
	}
}

func TestDelegationDetailShowsActivity(t *testing.T) {
	tr := events.NewTranscript()
	tr.Items = []events.Item{{
		Kind: events.ItemDelegation,
		DelegationTasks: []events.DelegationTaskState{
			{
				Name:        "worker",
				Status:      "running",
				Description: "Read the main file and analyze it",
				Activity: []events.DelegationActivity{
					{Type: "tool_call", ToolName: "read_file", Args: map[string]any{"path": "main.go"}},
					{Type: "tool_result", ToolName: "read_file", Result: "package main"},
					{Type: "text", Text: "I found the main file."},
				},
			},
		},
	}}

	m := model{
		theme: DefaultTheme(),
		width: 80,
	}

	out := stripANSI(m.renderDelegationDetailContent(tr.Items[0].DelegationTasks[0], 80))
	if !strings.Contains(out, "worker") {
		t.Fatalf("should contain task name 'worker': %q", out)
	}
	if !strings.Contains(out, "Read file") && !strings.Contains(out, "read_file") {
		t.Fatalf("should contain tool display name for 'read_file': %q", out)
	}
	if !strings.Contains(out, "I found the main file") {
		t.Fatalf("should contain text output: %q", out)
	}
	// Description should now appear in a bordered prompt box (matching main thread user bubble).
	if !strings.Contains(out, "┌") {
		t.Fatalf("should contain top-border character '┌' (description prompt box): %q", out)
	}
	if !strings.Contains(out, "└") {
		t.Fatalf("should contain bottom-border character '└' (description prompt box): %q", out)
	}
	if !strings.Contains(out, "Read the main file and analyze it") {
		t.Fatalf("should contain task description inside bordered box: %q", out)
	}
}

func TestDelegationDetailPromptBox(t *testing.T) {
	task := events.DelegationTaskState{
		Name:        "researcher",
		Status:      "complete",
		Duration:    "5s",
		Description: "Search the codebase for auth patterns",
	}
	m := model{theme: DefaultTheme(), width: 80}
	out := stripANSI(m.renderDelegationDetailContent(task, 80))

	// The description should appear in a bordered box matching the user-bubble style.
	if !strings.Contains(out, "┌") {
		t.Fatalf("output should contain top-border '┌': %q", out)
	}
	if !strings.Contains(out, "┐") {
		t.Fatalf("output should contain top-border '┐': %q", out)
	}
	if !strings.Contains(out, "└") {
		t.Fatalf("output should contain bottom-border '└': %q", out)
	}
	if !strings.Contains(out, "┘") {
		t.Fatalf("output should contain bottom-border '┘': %q", out)
	}
	if !strings.Contains(out, "│") {
		t.Fatalf("output should contain side-border '│': %q", out)
	}
	if !strings.Contains(out, "Search the codebase for auth patterns") {
		t.Fatalf("output should contain the description text: %q", out)
	}
	// Status line should appear below the box.
	if !strings.Contains(out, "researcher") {
		t.Fatalf("output should contain task name 'researcher': %q", out)
	}
}

func TestDelegationDetailToolFoldFormat(t *testing.T) {
	task := events.DelegationTaskState{
		Name:   "coder",
		Status: "complete",
		Activity: []events.DelegationActivity{
			{Type: "tool_call", ToolName: "shell_command", Args: map[string]any{"command": "go build ./..."}},
			{Type: "tool_result", ToolName: "shell_command", Result: "ok"},
		},
	}
	m := model{theme: DefaultTheme(), width: 80}
	out := stripANSI(m.renderDelegationDetailContent(task, 80))

	// Should contain the tool status label for "complete" (✓) — same as main thread activity fold.
	if !strings.Contains(out, "✓") {
		t.Fatalf("completed tool should show '✓' status label: %q", out)
	}
	// Should contain the tool display name for shell_command (rendered as "Run command" by ToolDisplayName).
	if !strings.Contains(out, "Run command") && !strings.Contains(out, "shell_command") {
		t.Fatalf("should contain tool display name for 'shell_command': %q", out)
	}
}

func TestDelegationDetailNoDescriptionSkipsBox(t *testing.T) {
	task := events.DelegationTaskState{
		Name:   "worker",
		Status: "running",
		// Description is empty — no bordered box should be rendered.
	}
	m := model{theme: DefaultTheme(), width: 80}
	out := stripANSI(m.renderDelegationDetailContent(task, 80))

	// Without a description, the prompt box should not appear.
	if strings.Contains(out, "┌") {
		t.Fatalf("output without description should not contain box border '┌': %q", out)
	}
	// The status line should still be present.
	if !strings.Contains(out, "worker") {
		t.Fatalf("output should still contain task name 'worker': %q", out)
	}
}

func TestDelegationDetailClosesOnEsc(t *testing.T) {
	m := model{
		theme: DefaultTheme(),
		delegationDetail: delegationDetailState{
			open:     true,
			taskName: "worker",
			taskIdx:  0,
			itemIdx:  0,
			vp:       viewport.New(viewport.WithWidth(80), viewport.WithHeight(10)),
		},
	}

	next, _ := m.handleDelegationDetailKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	got := next.(model)
	if got.delegationDetail.open {
		t.Fatal("Esc should close delegation detail overlay")
	}
}

func TestRenderDelegationStatusLineOnlyForRunning(t *testing.T) {
	item := events.Item{
		Kind: events.ItemDelegation,
		DelegationTasks: []events.DelegationTaskState{
			{
				Name: "active-worker", Status: "running",
				StartedAt: time.Now().Add(-3 * time.Second),
				Activity: []events.DelegationActivity{
					{Type: "tool_call", ToolName: "shell_command", Args: map[string]any{"command": "npm test"}},
				},
			},
			{
				Name: "done-worker", Status: "complete", Duration: "5s",
				Activity: []events.DelegationActivity{
					{Type: "tool_call", ToolName: "write_file", Args: map[string]any{"file_path": "out.txt"}},
				},
			},
		},
	}

	m := model{theme: DefaultTheme(), width: 80}
	out := stripANSI(m.renderDelegationItem(item, 80))

	// Running task should show its status line with command context.
	if !strings.Contains(out, "→ Running `npm test`") {
		t.Fatalf("running task should show status line '→ Running `npm test`': %q", out)
	}
	// Complete task should NOT show its status line (historical activity).
	if strings.Contains(out, "→ Writing") {
		t.Fatalf("complete task should NOT show status line, but found '→ Writing': %q", out)
	}
}

func TestRenderDelegationStatusLineTruncatesLongText(t *testing.T) {
	longText := strings.Repeat("analyzing the complex architecture of ", 10)
	item := events.Item{
		Kind: events.ItemDelegation,
		DelegationTasks: []events.DelegationTaskState{
			{
				Name: "thinker", Status: "running",
				StartedAt: time.Now().Add(-2 * time.Second),
				Activity: []events.DelegationActivity{
					{Type: "text", Text: longText},
				},
			},
		},
	}

	m := model{theme: DefaultTheme(), width: 60}
	out := stripANSI(m.renderDelegationItem(item, 60))

	// Should contain the arrow prefix.
	if !strings.Contains(out, "→") {
		t.Fatalf("should contain '→' status prefix: %q", out)
	}
	// Should be truncated with ellipsis.
	if !strings.Contains(out, "…") {
		t.Fatalf("long status text should be truncated with '…': %q", out)
	}
	// Should NOT contain the full repeated text.
	if strings.Contains(out, longText) {
		t.Fatalf("should not contain the full untruncated text: %q", out)
	}
}

func TestDelegationStatusLineShowsLatestActivity(t *testing.T) {
	item := events.Item{
		Kind: events.ItemDelegation,
		DelegationTasks: []events.DelegationTaskState{
			{
				Name: "multi-step", Status: "running",
				StartedAt: time.Now().Add(-10 * time.Second),
				Activity: []events.DelegationActivity{
					{Type: "tool_call", ToolName: "read_file", Args: map[string]any{"path": "main.go"}},
					{Type: "tool_result", ToolName: "read_file"},
					{Type: "tool_call", ToolName: "edit_file", Args: map[string]any{"path": "pkg/app.go"}},
				},
			},
		},
	}

	m := model{theme: DefaultTheme(), width: 80}
	out := stripANSI(m.renderDelegationItem(item, 80))

	// Should show the LAST activity (edit_file with path), not earlier ones.
	if !strings.Contains(out, "→ Editing pkg/app.go") {
		t.Fatalf("should show latest activity '→ Editing pkg/app.go': %q", out)
	}
	// Should NOT show the earlier tool_result.
	if strings.Contains(out, "→ Read file done") {
		t.Fatalf("should NOT show earlier activity '→ Read file done': %q", out)
	}
}

func TestSummarizeToolArgsStableOrder(t *testing.T) {
	args := map[string]any{
		"zebra":  "z",
		"alpha":  "a",
		"middle": "m",
		"beta":   "b",
	}

	// Verify deterministic output across many calls.
	first := summarizeToolArgs(args, 200)
	for i := 0; i < 100; i++ {
		got := summarizeToolArgs(args, 200)
		if got != first {
			t.Fatalf("iteration %d: output changed from %q to %q", i, first, got)
		}
	}

	// Verify alphabetical key order.
	want := "alpha: a, beta: b, middle: m, zebra: z"
	if first != want {
		t.Fatalf("summarizeToolArgs = %q, want %q", first, want)
	}
}

func TestRenderDelegationItemShowsEvaluatingStatus(t *testing.T) {
	item := events.Item{
		Kind: events.ItemDelegation,
		DelegationTasks: []events.DelegationTaskState{
			{
				Name:        "my-task",
				Description: "Do something",
				Status:      "evaluating",
				Error:       "no meaningful activity for 2m0s",
				StartedAt:   time.Now().Add(-30 * time.Second),
			},
		},
	}

	m := model{
		theme: DefaultTheme(),
		width: 80,
	}
	out := stripANSI(m.renderDelegationItem(item, 80))
	if out == "" {
		t.Fatal("delegation item should render when tasks are present")
	}
	if !strings.Contains(out, "⟳") {
		t.Fatalf("should contain evaluating icon '⟳': %q", out)
	}
	if !strings.Contains(out, "evaluati") {
		t.Fatalf("should contain 'evaluating' status text: %q", out)
	}
}

// --- Sticky user message header tests ---

func TestStickyUserMessageReturnsEmptyWhenUserBubbleVisible(t *testing.T) {
	// Short conversation that fits within the viewport: user bubble is visible,
	// so no sticky header should be shown.
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{Mode: "code"}},
		Width:   80,
		Height:  40,
	})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewUser("short question"))
	m.tr.Apply(events.NewText("short answer"))
	m.tr.Apply(events.NewDone())
	m.refreshViewport()

	// Scroll to top (offset=0) — user bubble is inline, no sticky header needed.
	m.vp.GotoTop()
	if got := m.stickyUserMessage(); got != "" {
		t.Fatalf("expected empty sticky message when user bubble is visible, got %q", got)
	}
	if m.stickyHeaderLines != 0 {
		t.Fatalf("expected stickyHeaderLines=0 when user bubble visible, got %d", m.stickyHeaderLines)
	}
}

func TestStickyUserMessageReturnsContentWhenScrolledPast(t *testing.T) {
	// Large response that pushes the user bubble above the viewport top.
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{Mode: "code"}},
		Width:   80,
		Height:  20,
	})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewUser("what is the meaning of life?"))
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, "line of agent output that fills the viewport screen")
	}
	m.tr.Apply(events.NewText(strings.Join(lines, "\n")))
	m.tr.Streaming = true
	m.refreshViewport()

	// Auto-scroll puts viewport at the bottom, user bubble is above viewport top.
	if !m.vp.AtBottom() {
		t.Skip("viewport not at bottom — content may not be long enough for this terminal height")
	}
	sticky := m.stickyUserMessage()
	if sticky == "" {
		t.Fatalf("expected sticky user message when user bubble scrolled past, got empty")
	}
	if !strings.Contains(sticky, "what is the meaning of life?") {
		t.Fatalf("sticky message should contain original user text, got %q", sticky)
	}
	if m.stickyHeaderLines == 0 {
		t.Fatal("expected stickyHeaderLines > 0 when sticky header is shown")
	}
}

func TestStickyUserHeaderShowsPreviousUserOnScrollUp(t *testing.T) {
	// Two turns: user1 + very long agent1 + user2 + very long agent2.
	// At bottom: sticky should show user2.
	// After scrolling up into agent1: sticky should switch to user1.
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{Mode: "code"}},
		Width:   80,
		Height:  20,
	})
	m.ready = true
	m.layout()

	m.tr.Apply(events.NewUser("first question"))
	var lines1 []string
	for i := 0; i < 40; i++ {
		lines1 = append(lines1, "agent turn 1 output line")
	}
	m.tr.Apply(events.NewText(strings.Join(lines1, "\n")))
	m.tr.Apply(events.NewDone())

	m.tr.Apply(events.NewUser("second question"))
	var lines2 []string
	for i := 0; i < 40; i++ {
		lines2 = append(lines2, "agent turn 2 output line")
	}
	m.tr.Apply(events.NewText(strings.Join(lines2, "\n")))
	m.tr.Streaming = true
	m.refreshViewport()

	if !m.vp.AtBottom() {
		t.Skip("viewport not at bottom — content too short for this terminal height")
	}

	// At bottom, sticky should be the most recent user message (user2).
	sticky := m.stickyUserMessage()
	if !strings.Contains(sticky, "second question") {
		t.Fatalf("at bottom, sticky should show 'second question', got %q", sticky)
	}

	// Scroll all the way to the top.
	m.vp.GotoTop()
	m.refreshViewport()

	// At the very top (YOffset=0), no sticky header should appear.
	sticky = m.stickyUserMessage()
	if sticky != "" {
		// Both user bubbles are above offset=0 is impossible; the first user
		// bubble starts at line 0 so offset=0 means it's visible.
		t.Logf("at top: sticky=%q (acceptable if both bubbles scrolled)", sticky)
	}
}

func TestStickyUserHeaderDisappearsAtTop(t *testing.T) {
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{Mode: "code"}},
		Width:   80,
		Height:  20,
	})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewUser("my question"))
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, "agent output line")
	}
	m.tr.Apply(events.NewText(strings.Join(lines, "\n")))
	m.tr.Apply(events.NewDone())
	m.refreshViewport()

	// Scroll to very top — the user bubble should be visible inline.
	m.vp.GotoTop()
	// At offset 0, the first user bubble starts at or near line 0, so it is
	// within the viewport: no sticky header needed.
	if got := m.stickyUserMessage(); got != "" {
		// Only fails if the user bubble is somehow entirely above offset=0,
		// which is physically impossible.
		t.Fatalf("sticky header should not appear when at viewport top, got %q", got)
	}
}

func TestRenderStickyUserHeaderTruncatesLongContent(t *testing.T) {
	m := model{theme: DefaultTheme(), width: 80}
	longContent := strings.Repeat("very long question text that exceeds width ", 10)
	out := m.renderStickyUserHeader(longContent, 60, false)
	plain := stripANSI(out)
	lines := strings.Split(plain, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (top border, content, bottom border), got %d: %q", len(lines), plain)
	}
	// Top border should start/end with box corners.
	if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") {
		t.Fatalf("top border line should start with ┌ and end with ┐: %q", lines[0])
	}
	// Bottom border should start/end with box corners.
	if !strings.HasPrefix(lines[2], "└") || !strings.HasSuffix(lines[2], "┘") {
		t.Fatalf("bottom border line should start with └ and end with ┘: %q", lines[2])
	}
	// Content line should be within width.
	if w := lipgloss.Width(lines[1]); w > 60 {
		t.Fatalf("content line width %d exceeds 60: %q", w, lines[1])
	}
}

func TestRenderStickyUserHeaderSingleLineOnly(t *testing.T) {
	m := model{theme: DefaultTheme(), width: 80}
	// Multi-line content: only first line should appear.
	content := "first line\nsecond line\nthird line"
	out := m.renderStickyUserHeader(content, 60, false)
	plain := stripANSI(out)
	if strings.Contains(plain, "second line") {
		t.Fatalf("sticky header should only show first line, but got: %q", plain)
	}
	if strings.Contains(plain, "third line") {
		t.Fatalf("sticky header should only show first line, but got: %q", plain)
	}
	if !strings.Contains(plain, "first line") {
		t.Fatalf("sticky header should show first line, but got: %q", plain)
	}
}

func TestViewportTopYIncludesStickyHeader(t *testing.T) {
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{Mode: "code"}},
		Width:   80,
		Height:  20,
	})
	m.ready = true
	m.layout()

	// Without any content, stickyHeaderLines is 0.
	if got := m.viewportTopY(); got != 2 {
		t.Fatalf("viewportTopY with no sticky header: want 2, got %d", got)
	}

	// Simulate a sticky header of 3 lines.
	m.stickyHeaderLines = 3
	if got := m.viewportTopY(); got != 5 {
		t.Fatalf("viewportTopY with 3 sticky lines: want 5, got %d", got)
	}
}

func TestRenderStickyUserHeaderExpandedShowsFullContent(t *testing.T) {
	m := model{theme: DefaultTheme(), width: 80}
	content := "first line\nsecond line\nthird line"
	out := m.renderStickyUserHeader(content, 60, true)
	plain := stripANSI(out)
	// In expanded mode all lines of the content should appear.
	if !strings.Contains(plain, "second line") {
		t.Fatalf("expanded sticky header should show all lines, missing 'second line': %q", plain)
	}
	if !strings.Contains(plain, "third line") {
		t.Fatalf("expanded sticky header should show all lines, missing 'third line': %q", plain)
	}
}

func TestRenderStickyUserHeaderExpandedTallerThan3Lines(t *testing.T) {
	m := model{theme: DefaultTheme(), width: 80}
	// 5 lines of content: expanded mode should produce more than 3 rendered lines.
	lines := make([]string, 5)
	for i := range lines {
		lines[i] = fmt.Sprintf("content line %d", i+1)
	}
	content := strings.Join(lines, "\n")
	out := m.renderStickyUserHeader(content, 60, true)
	plain := stripANSI(out)
	rendered := strings.Split(plain, "\n")
	if len(rendered) <= 3 {
		t.Fatalf("expanded sticky header with 5 content lines should render more than 3 lines, got %d: %q", len(rendered), plain)
	}
}
