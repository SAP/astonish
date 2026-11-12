package tui

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestDelegationProjectionLiveRestored(t *testing.T) {
	acts := []events.DelegationActivity{
		{Type: "tool_result", CallID: "b", ToolName: "edit_file", Result: map[string]any{"error": "denied"}},
		{Type: "tool_call", CallID: "a", ToolName: "edit_file", Args: map[string]any{"path": "a.go", "old_string": "old", "new_string": "new"}},
		{Type: "text", Text: "middle"},
		{Type: "tool_call", CallID: "b", ToolName: "edit_file", Args: map[string]any{"path": "b.go"}},
		{Type: "tool_result", CallID: "a", ToolName: "edit_file", Result: map[string]any{"success": true}},
	}
	for _, restore := range []bool{false, true} {
		tr := events.NewTranscript()
		start := events.NewDelegationStart([]events.DelegationTask{{Name: "worker"}})
		evs := []events.Event{start}
		for _, a := range acts {
			e := events.NewDelegationTaskActivity("task_"+a.Type, "worker", a.ToolName, a.Args, a.Result, a.Text)
			e.DelegationCallID = a.CallID
			evs = append(evs, e)
		}
		if restore {
			var history []events.HistoryMsg
			for _, e := range evs {
				e := e
				history = append(history, events.HistoryMsg{Delegation: &e})
			}
			tr.LoadHistory(history)
		} else {
			for _, e := range evs {
				tr.Apply(e)
			}
		}
		rows := projectDelegationActivity(tr.Items[0].DelegationTasks[0].Activity)
		if len(rows) != 3 || rows[0].step.Status != "complete" || rows[1].text != "middle" || rows[2].step.Status != "error" {
			t.Fatalf("restore=%v: %#v", restore, rows)
		}
	}
}

func TestDelegationWrappedWritePendingAndNarrow(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 36, Height: 24})
	content := strings.Repeat("line of source\n", 60) + "lastLine"
	call := events.DelegationActivity{Type: "tool_call", CallID: "w", ToolName: "execute_tool", Args: map[string]any{"name": "functions.write_file", "arguments": map[string]any{"path": "long/path/file.go", "content": content}}}
	task := events.DelegationTaskState{Activity: []events.DelegationActivity{call}}
	pending := stripANSI(m.renderDelegationActivity(task, 28))
	if !strings.Contains(pending, "pending") || strings.Contains(pending, "lastLine") {
		t.Fatal(pending)
	}
	task.Activity = append(task.Activity, events.DelegationActivity{Type: "tool_result", CallID: "w", ToolName: "execute_tool", Result: map[string]any{"success": true}})
	got := stripANSI(m.renderDelegationActivity(task, 28))
	if !strings.Contains(got, "lastLine") || !strings.Contains(got, "+61") {
		t.Fatal(got)
	}
	for _, line := range strings.Split(got, "\n") {
		if lipgloss.Width(line) > 28 {
			t.Fatalf("overflow %d: %q", lipgloss.Width(line), line)
		}
	}
}

const handoffFixture = `{"version":1,"task_id":"t","child_session_id":"c","attempt":2,"effective_task":"effective assignment","original_task":"original assignment","effective_instructions":"effective rules","original_instructions":"original rules","workdir":"/project","preferred_tools":["read_file"],"initial_tool_names":["read_file","edit_file"],"plan_step":"phase-a","plan":{"status":"matched","plan_id":"plan-id","revision":9007199254740993,"sequence":4,"goal":"phase goal","source_mirror":"/session/PLAN.md","document":{"context":"full context","what_not_to_do":"do not deploy","verification":"acceptance story"},"step":{"name":"phase-a","description":"phase description","details":"full definition","summary":"phase summary","files":[{"path":"app.go","kind":"modify"}],"outcome":"observable outcome","verify":"go test ./pkg/tui","verify_kind":"unit","parallel_group":"wave-1"}},"system_prompt":"SECRET PROMPT","history":"SECRET HISTORY"}`

func TestDelegationHandoffAllowlistMissingAndIdentity(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	task := events.DelegationTaskState{TaskID: "t", ChildSessionID: "c", Attempt: 2, Activity: []events.DelegationActivity{{Type: "handoff", Text: handoffFixture}}}
	got := stripANSI(m.renderDelegationHandoff(task, 76))
	for _, want := range []string{"effective assignment", "original assignment", "effective rules", "original rules", "phase goal", "full context", "do not deploy", "acceptance story", "full definition", "9007199254740993", "/session/PLAN.md", "Preferred tools", "Initially available tools", "go test ./pkg/tui", "modify app.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "SECRET") || strings.Contains(m.renderDelegationActivity(task, 76), "effective assignment") {
		t.Fatal("handoff leaked into activity or unsafe fields rendered")
	}
	task.Attempt = 3
	if !strings.Contains(m.renderDelegationHandoff(task, 76), "does not match") {
		t.Fatal("cross-attempt handoff accepted")
	}
	task.Activity = nil
	if !strings.Contains(m.renderDelegationHandoff(task, 76), "Handoff unavailable") {
		t.Fatal("missing fabricated")
	}
	task.Activity = []events.DelegationActivity{{Type: "handoff", Text: `{"version":99}`}}
	if !strings.Contains(m.renderDelegationHandoff(task, 76), "unsupported") {
		t.Fatal("unsupported accepted")
	}
}

func TestDelegationHandoffTabsPollingAndBack(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: stateDetailBackend{}, Width: 50, Height: 20})
	m.tr.Items = []events.Item{{Kind: events.ItemDelegation, DelegationTasks: []events.DelegationTaskState{{Name: "selected", Status: "running", TaskID: "t", ChildSessionID: "c", Attempt: 2, Activity: []events.DelegationActivity{{Type: "text", Text: strings.Repeat("activity line\n", 80)}}}}}}
	next, _ := m.openDelegationDetail(0, 0)
	m = next.(model)
	request := m.delegationDetail.request
	next, poll := m.Update(delegationDetailLoadedMsg{request: request, activity: []events.DelegationActivity{{Type: "handoff", Text: handoffFixture}}})
	m = next.(model)
	if poll == nil {
		t.Fatal("active detail not polled")
	}
	m.layoutDelegationDetail()
	m.delegationDetail.vp.SetYOffset(10)
	next, _ = m.handleDelegationDetailKey(tea.KeyPressMsg{Code: 'h', Text: "h"})
	m = next.(model)
	if m.delegationDetail.tab != delegationHandoffTab || m.delegationDetail.vp.YOffset() != 0 {
		t.Fatal("handoff did not open at top")
	}
	m.delegationDetail.vp.SetYOffset(8)
	m.layoutDelegationDetail()
	if m.delegationDetail.vp.YOffset() != 8 {
		t.Fatal("refresh jumped scroll")
	}
	next, _ = m.handleDelegationDetailKey(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(model)
	if m.delegationDetail.vp.YOffset() != 10 {
		t.Fatal("activity position lost")
	}
	next, cmd := m.Update(delegationDetailPollMsg{request: request})
	m = next.(model)
	if cmd == nil || !m.delegationDetail.loading {
		t.Fatal("poll did not fetch")
	}
	next, _ = m.handleDelegationDetailKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(model)
	next, cmd = m.Update(delegationDetailPollMsg{request: request})
	m = next.(model)
	if m.delegationDetail.open || cmd != nil || m.quitting {
		t.Fatal("back did not close only viewer")
	}
}

func TestDelegationDetailLongNarrowContent(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 32, Height: 24})
	task := events.DelegationTaskState{Name: strings.Repeat("worker", 16), Status: "failed", Attempt: 2,
		Description: strings.Repeat("long assignment ", 20), Error: strings.Repeat("failure-reason-", 12),
		Activity: []events.DelegationActivity{{Type: "handoff", Text: handoffFixture}},
	}
	m.tr.Items = []events.Item{{Kind: events.ItemDelegation, DelegationTasks: []events.DelegationTaskState{task}}}
	next, _ := m.openDelegationDetail(0, 0)
	m = next.(model)
	for name, content := range map[string]string{
		"handoff":  m.renderDelegationHandoff(task, 28),
		"activity": m.renderDelegationDetailContent(task, 28),
		"header":   m.renderDelegationDetailHeader(),
	} {
		width := 28
		if name == "header" {
			width = 32
		}
		for _, line := range strings.Split(content, "\n") {
			if lipgloss.Width(line) > width {
				t.Errorf("%s overflow: %q", name, line)
			}
		}
	}
}

func TestDelegationDetailMergePreservesMultiplicity(t *testing.T) {
	a := events.DelegationActivity{Type: "text", Text: "same"}
	snapshot := []events.DelegationActivity{{Type: "handoff", Text: handoffFixture}, a}
	live := []events.DelegationActivity{a, a, {Type: "tool_call", CallID: "id", ToolName: "read_file"}}
	got := mergeDelegationDetailActivity(snapshot, live)
	if len(got) != 4 {
		t.Fatalf("%#v", got)
	}
	raw, _ := json.Marshal(got)
	var restored []events.DelegationActivity
	_ = json.Unmarshal(raw, &restored)
	if !reflect.DeepEqual(projectDelegationActivity(got), projectDelegationActivity(restored)) {
		t.Fatal("restore differs")
	}
}
