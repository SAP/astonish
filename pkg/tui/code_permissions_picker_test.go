package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
)

func TestCodePermissionsPickerArrowNavigation(t *testing.T) {
	b := &permissionTestBackend{snapshot: backend.CodePermissionsSnapshot{SessionID: "session", SessionAllFolders: true}}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	defer m.cancel()
	next, cmd := m.openCodePermissions()
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	for _, step := range []struct{ key, label string }{{"down", "Tools"}, {"down", "Plan access"}, {"up", "Tools"}, {"up", "Folders"}} {
		code := tea.KeyDown
		if step.key == "up" {
			code = tea.KeyUp
		}
		next, cmd = m.Update(tea.KeyPressMsg{Code: code})
		m = next.(model)
		view := stripANSI(m.renderCodePermissions())
		if cmd != nil || !strings.Contains(view, "› "+step.label) || !strings.Contains(view, "╭") {
			t.Fatalf("%s did not select %s in a model-style picker: %s", step.key, step.label, view)
		}
		if b.revoked != "" {
			t.Fatal("navigation revoked access")
		}
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	if b.revoked != backend.CodePermissionSessionAllFolders || m.permissions.snapshot.SessionAllFolders {
		t.Fatal("selected Folders did not revoke")
	}
}
