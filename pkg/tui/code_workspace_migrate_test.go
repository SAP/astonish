package tui

import (
	"testing"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestCodeWorkspaceMoveCurrentSessionAction(t *testing.T) {
	m, b := workspaceModel(t)
	m.tr.SessionID = "existing"
	n, cmd := m.openWorkspacePicker(true)
	m = workspaceReply(n.(model), cmd)
	rows := m.workspaceRows()
	if rows[0] != "Move current session to independent workspace" {
		t.Fatalf("rows %v", rows)
	}
	m, cmd = workspaceKey(m, "enter")
	if cmd != nil || m.workspacePicker.stage != WorkspaceChoosingCopy || m.workspacePicker.cursor != 1 {
		t.Fatal("move did not choose copy mode")
	}
	m, cmd = workspaceKey(m, "enter")
	if cmd == nil {
		t.Fatal("move did not prepare")
	}
	m = workspaceReply(m, cmd)
	if m.workspaceHandoff == nil || b.request.SessionID != "existing" || b.request.CopyMode != backend.WorkspaceCurrentWork {
		t.Fatalf("request %+v", b.request)
	}
}

func TestCodeWorkspaceMoveRequiresSavedSession(t *testing.T) {
	m, _ := workspaceModel(t)
	m.tr = events.NewTranscript()
	n, cmd := m.openWorkspacePicker(true)
	m = workspaceReply(n.(model), cmd)
	m, cmd = workspaceKey(m, "enter")
	if cmd != nil || m.workspacePicker.err == "" {
		t.Fatal("unsaved session moved")
	}
}
