package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
		vp:                   viewport.New(80, 10),
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

	next, _ := m.handleMouse(tea.MouseMsg{X: 3, Y: m.viewportTopY(), Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = next.(model)
	next, _ = m.handleMouse(tea.MouseMsg{X: 3, Y: m.viewportTopY(), Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
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
			{Name: "researcher", Description: "Research", Status: "running", StartedAt: time.Now().Add(-12 * time.Second)},
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
