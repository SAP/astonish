package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/tui/events"
)

func TestDelegationDetailVerifiedEditDiff(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	task := events.DelegationTaskState{Name: "worker", Status: "complete", Activity: []events.DelegationActivity{
		{Type: "tool_call", CallID: "edit", ToolName: "edit_file", Args: map[string]any{"path": "/project/app.go", "old_string": "fallbackOld", "new_string": "fallbackNew"}},
		{Type: "text", Text: "Between call and result"},
		{Type: "tool_result", CallID: "edit", ToolName: "edit_file", Result: map[string]any{"verification_context": "@@ app.go:42\n- 42| verifiedOld\n+ 42| verifiedNew"}},
	}}
	got := stripANSI(m.renderDelegationDetailContent(task, 76))
	for _, want := range []string{"verifiedOld", "verifiedNew", "42", "Between call and result"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "fallbackNew") {
		t.Fatal("args overrode verification context")
	}
}

func TestDelegationDetailFailedEditDoesNotClaimSuccess(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	task := events.DelegationTaskState{Name: "worker", Status: "failed", Activity: []events.DelegationActivity{
		{Type: "tool_call", CallID: "edit", ToolName: "edit_file", Args: map[string]any{"path": "app.go", "old_string": "before", "new_string": "unapplied"}},
		{Type: "tool_result", CallID: "edit", ToolName: "edit_file", Result: map[string]any{"error": "permission denied"}},
	}}
	got := stripANSI(m.renderDelegationDetailContent(task, 76))
	if strings.Contains(got, "✓") || strings.Contains(got, "unapplied") || !strings.Contains(got, "permission denied") {
		t.Fatal(got)
	}
}
