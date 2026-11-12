package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/SAP/astonish/pkg/tui/events"
)

type delegationCountedPayload struct {
	calls *int
	text  string
}

func (p delegationCountedPayload) MarshalJSON() ([]byte, error) {
	*p.calls++
	return []byte(`{"content":"` + p.text + `"}`), nil
}

func TestDelegationStyledWrapPreservesDiffColors(t *testing.T) {
	input := "\x1b[31m− before\x1b[0m\n\x1b[32m+ after\x1b[0m"
	got := wrapDelegationStyled(input, 40)
	if got != input {
		t.Fatalf("lost diff styles: %q", got)
	}
}

func TestDelegationOutputPreviewBounded(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.delegationDetail.raw = true
	payload := strings.Repeat("x", 5<<20)
	task := events.DelegationTaskState{Activity: []events.DelegationActivity{
		{Type: "tool_result", ToolName: "read_file", Result: map[string]any{"content": payload}},
	}}
	start := time.Now()
	got := m.renderDelegationActivity(task, 76)
	if len(got) > 10000 || time.Since(start) > time.Second {
		t.Fatal("output preview is not bounded")
	}
	if len(task.Activity[0].Result.(map[string]any)["content"].(string)) != len(payload) {
		t.Fatal("mutated stored result")
	}
}

func TestDelegationCompactDefaultNoRawPayload(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	calls := 0
	task := events.DelegationTaskState{Name: "worker", Status: "interrupted", Activity: []events.DelegationActivity{
		{Type: "handoff", Text: handoffFixture},
		{Type: "tool_call", CallID: "r", ToolName: "read_file", Args: map[string]any{"path": "app.go", "internal_arg": "RAW_ARGS"}},
		{Type: "tool_result", CallID: "r", ToolName: "read_file", Result: delegationCountedPayload{&calls, strings.Repeat("RAW_RESULT", 100000)}},
		{Type: "text", Text: "**Actual assistant answer**"},
	}}
	got := stripANSI(m.renderDelegationDetailContent(task, 76))
	if calls != 0 {
		t.Errorf("default marshaled raw payload %d times", calls)
	}
	for _, want := range []string{"effective assignment", "┌", "Read file", "app.go", "Actual assistant answer"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(got, "RAW_ARGS") || strings.Contains(got, "RAW_RESULT") {
		t.Error("raw payload leaked into default Activity")
	}
	if !strings.HasPrefix(got, "  ┌") {
		t.Errorf("request box lost transcript margin: %.100s", got)
	}
}

func TestDelegationLargePayloadViewAndBackBounded(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: stateDetailBackend{}, Width: 80, Height: 24})
	calls := 0
	m.tr.Items = []events.Item{{Kind: events.ItemDelegation, DelegationTasks: []events.DelegationTaskState{{Name: "worker", ChildSessionID: "child", Status: "running", Activity: []events.DelegationActivity{
		{Type: "tool_call", CallID: "r", ToolName: "read_file", Args: map[string]any{"path": "huge.json"}},
		{Type: "tool_result", CallID: "r", ToolName: "read_file", Result: delegationCountedPayload{&calls, strings.Repeat("x", 5<<20)}},
	}}}}}
	start := time.Now()
	next, _ := m.openDelegationDetail(0, 0)
	m = next.(model)
	request := m.delegationDetail.request
	for i := 0; i < 10; i++ {
		_ = m.View()
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(model)
	next, _ = m.Update(delegationDetailLoadedMsg{request: request, activity: []events.DelegationActivity{{Type: "text", Text: "stale"}}})
	m = next.(model)
	if m.delegationDetail.open || m.quitting {
		t.Fatal("Esc failed to dismiss only overlay")
	}
	if calls != 0 {
		t.Errorf("opening/View marshaled 5MiB raw payload %d times", calls)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("opening/repeated View/Esc took %v", elapsed)
	}
}
