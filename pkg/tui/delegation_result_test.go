package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/tui/events"
)

func TestDelegationResultFailuresAndInlineStatus(t *testing.T) {
	for _, r := range []any{
		map[string]any{"error": "denied"}, map[string]any{"error": map[string]any{"message": "denied"}},
		map[string]any{"success": false}, map[string]any{"isError": true},
		map[string]any{"status": "pending_authorization"}, map[string]any{"status": "cancelled"},
		map[string]any{"result": map[string]any{"error": "nested"}}, `{"error":"encoded"}`, "Error: refused",
	} {
		if got := delegationResultStatus(r); got != "error" {
			t.Fatalf("%#v => %s", r, got)
		}
	}
	task := events.DelegationTaskState{Status: "running", Activity: []events.DelegationActivity{
		{Type: "tool_call", CallID: "x", ToolName: "edit_file"},
		{Type: "tool_result", CallID: "x", ToolName: "edit_file", Result: map[string]any{"error": "denied"}},
		{Type: "handoff", Text: handoffFixture},
	}}
	got := delegationTaskStatusLine(task, 0)
	if !strings.Contains(got, "failed") || strings.Contains(got, "done") {
		t.Fatal(got)
	}
}

func TestDelegationWrappedVerificationResult(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	task := events.DelegationTaskState{Activity: []events.DelegationActivity{
		{Type: "tool_result", CallID: "x", ToolName: "execute_tool", Result: map[string]any{"result": map[string]any{"verification_context": "@@ app.go:91\n- 91| oldVerified\n+ 91| newVerified"}}},
		{Type: "tool_call", CallID: "x", ToolName: "execute_tool", Args: map[string]any{"name": "functions.edit_file", "arguments": `{"path":"app.go","old_string":"oldFallback","new_string":"newFallback"}`}},
	}}
	got := stripANSI(m.renderDelegationActivity(task, 76))
	if !strings.Contains(got, "newVerified") || strings.Contains(got, "newFallback") || strings.Count(got, "Edit file") != 1 {
		t.Fatal(got)
	}
}

func TestDelegationIDLessPairingIsConservative(t *testing.T) {
	rows := projectDelegationActivity([]events.DelegationActivity{
		{Type: "tool_call", ToolName: "edit_file"},
		{Type: "text", Text: "interleaved"},
		{Type: "tool_result", ToolName: "edit_file", Result: map[string]any{"success": true}},
	})
	if len(rows) != 3 || rows[0].step.Status != "running" || rows[2].step.Status != "complete" {
		t.Fatal(rows)
	}
}
