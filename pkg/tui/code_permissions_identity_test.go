package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
)

type permissionDescendantBackend struct{ permissionTestBackend }

func (b *permissionDescendantBackend) ResumeSession(_ context.Context, _ string) ([]backend.HistoryEntry, error) {
	b.snapshot.SessionID = "compacted-child"
	return []backend.HistoryEntry{{Kind: "agent", Text: "Restored child history"}}, nil
}

func TestCodePermissionsPickerResumesActiveDescendant(t *testing.T) {
	for _, shortcut := range []bool{false, true} {
		b := &permissionDescendantBackend{permissionTestBackend{snapshot: backend.CodePermissionsSnapshot{SessionID: "previous", SessionAllFolders: true}}}
		m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 40})
		defer m.cancel()
		m.ready = true
		next, _ := m.Update(m.resumeSessionCmd("saved-parent")())
		m = next.(model)
		var cmd tea.Cmd
		if shortcut {
			next, cmd = m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
		} else {
			next, cmd = m.handleSlash("/permissions")
		}
		m = next.(model)
		next, _ = m.Update(cmd())
		m = next.(model)
		if m.tr.SessionID != "compacted-child" || m.info.SessionID != "compacted-child" || m.permissions.err != "" || !m.permissions.known {
			t.Fatalf("shortcut=%v: picker overwrote active descendant: transcript=%q header=%q permissions=%+v", shortcut, m.tr.SessionID, m.info.SessionID, m.permissions)
		}
		if !strings.Contains(m.renderCodePermissions(), "Enter revokes All folders for this session") {
			t.Fatal("active descendant grant was not actionable")
		}
		next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = next.(model)
		next, _ = m.Update(cmd())
		m = next.(model)
		if b.revoked != backend.CodePermissionSessionAllFolders || m.permissions.snapshot.SessionAllFolders || m.permissions.err != "" {
			t.Fatalf("descendant revoke failed: %+v", m.permissions)
		}
	}
}

func TestCodePermissionsEmptyStateExplainsCurrentAccess(t *testing.T) {
	for _, id := range []string{"", "existing"} {
		b := &permissionTestBackend{snapshot: backend.CodePermissionsSnapshot{SessionID: id}}
		m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 40})
		defer m.cancel()
		next, cmd := m.openCodePermissions()
		m = next.(model)
		next, _ = m.Update(cmd())
		m = next.(model)
		view := m.renderCodePermissions()
		for _, want := range []string{"Folders", "Tools", "Plan access", "Ask outside project", "Ask when needed", "r refresh", "esc close"} {
			if !strings.Contains(view, want) {
				t.Errorf("session=%q: missing %q in %q", id, want, view)
			}
		}
		if !strings.Contains(stripANSI(view), "› Folders") {
			t.Errorf("empty session has no selectable Folders row: %q", view)
		}
	}
}
