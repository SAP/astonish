package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/backend"
)

func codeSessionDeleteModel(t *testing.T) (model, *sessionDeleteBackend) {
	t.Helper()
	b := &sessionDeleteBackend{staticBackend: staticBackend{info: backend.Info{Mode: "code", SessionID: "active"}}}
	m := newModel(context.Background(), Config{Backend: b, Width: 160, Height: 30})
	t.Cleanup(m.cancel)
	m.sessions = sessionsState{open: true, items: []backend.SessionSummary{{ID: "active", Title: "Code work", WorkspaceID: "managed"}}}
	return m, b
}

func TestCodeSessionDeleteConfirmationAndCancellation(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyEsc}, {Code: 'n', Text: "n"}, {Code: 'q', Text: "q"}, {Code: 'c', Mod: tea.ModCtrl}} {
		m, b := codeSessionDeleteModel(t)
		next, cmd := m.handleSessionsKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
		m = next.(model)
		if cmd != nil || !m.sessions.confirmDelete {
			t.Fatal("delete must require confirmation")
		}
		plain := stripANSI(m.renderSessionsOverlay())
		for _, want := range []string{"Delete this session and its dedicated workspace?", "permanently discards workspace files", "uncommitted changes", "detached commits", "original checkout and named branches are preserved", "Shared workspaces are retained", "Code returns to the original checkout"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("missing %q in %s", want, plain)
			}
		}
		next, cmd = m.handleSessionsKey(key)
		m = next.(model)
		if cmd != nil || m.sessions.confirmDelete || b.deletedID != "" || b.newCalled {
			t.Fatal("cancellation mutated session")
		}
	}
}

func TestCodeSessionDeleteRemoteWordingUnchanged(t *testing.T) {
	m, _ := codeSessionDeleteModel(t)
	for _, mode := range []string{"chat", "code"} {
		m.info.Mode = mode
		if mode == "code" {
			m.sessions.items[0].WorkspaceID = ""
		}
		m.sessions.confirmDelete = true
		plain := stripANSI(m.renderSessionsOverlay())
		if !strings.Contains(plain, "Delete this session?") || !strings.Contains(plain, "This removes the saved transcript. Press y to delete or Esc to cancel.") || strings.Contains(plain, "dedicated workspace") {
			t.Fatal(plain)
		}
	}
}

func TestCodeSessionDeleteBusyInitiationAndConfirmation(t *testing.T) {
	cases := map[string]func(*model){
		"streaming":           func(m *model) { m.tr.Streaming = true },
		"approval":            func(m *model) { m.tr.Awaiting = true },
		"compaction":          func(m *model) { m.compacting = true },
		"plan approval":       func(m *model) { m.planApprovalPending = true },
		"turn":                func(m *model) { m.turnCancel = func() {} },
		"workspace operation": func(m *model) { m.workspacePicker.loading = true },
		"verification": func(m *model) {
			m.verification = planVerificationState{owner: m.tr, progress: agent.PlanVerificationProgress{Stage: agent.PlanVerificationRunning}, received: time.Now()}
		},
	}
	for name, busy := range cases {
		for _, confirming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/confirm=%v", name, confirming), func(t *testing.T) {
				m, b := codeSessionDeleteModel(t)
				m.sessions.confirmDelete = confirming
				busy(&m)
				key := 'd'
				if confirming {
					key = 'y'
				}
				next, cmd := m.handleSessionsKey(tea.KeyPressMsg{Code: key, Text: string(key)})
				m = next.(model)
				if cmd != nil || m.sessions.deleting || m.sessions.confirmDelete != confirming || m.sessions.err == "" || b.deletedID != "" {
					t.Fatalf("busy deletion not blocked: %+v", m.sessions)
				}
			})
		}
	}
}

func TestCodeSessionDeleteDispatchedIgnoresCancellationAndDuplicates(t *testing.T) {
	m, b := codeSessionDeleteModel(t)
	m.sessions.confirmDelete = true
	next, cmd := m.handleSessionsKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = next.(model)
	if cmd == nil || !m.sessions.deleting {
		t.Fatal("delete not dispatched")
	}
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyEsc}, {Code: 'c', Mod: tea.ModCtrl}, {Code: 'y', Text: "y"}, {Code: 'd', Text: "d"}, {Code: tea.KeyEnter}, {Code: 'n', Text: "n"}} {
		next, duplicate := m.handleSessionsKey(key)
		m = next.(model)
		if duplicate != nil || !m.sessions.open || !m.sessions.deleting || !m.sessions.loading {
			t.Fatal("in-flight delete canceled or duplicated")
		}
	}
	cmd()
	if b.deletedID != "active" {
		t.Fatal("wrong session deleted")
	}
}

func TestCodeSessionDeleteErrorPreservesSession(t *testing.T) {
	m, b := codeSessionDeleteModel(t)
	m.sessions.deleting, m.sessions.loading = true, true
	next, cmd := m.applySessionDeleted(sessionDeletedMsg{id: "active", err: errors.New("workspace busy")})
	m = next.(model)
	if cmd != nil || m.sessions.loading || m.sessions.deleting || m.quitting || m.workspaceHandoff != nil || len(m.sessions.items) != 1 || b.newCalled || !strings.Contains(m.sessions.err, "workspace busy") {
		t.Fatalf("failed delete changed session: %+v", m.sessions)
	}
}

func TestCodeSessionDeleteHandoffQuitsWithoutNewSession(t *testing.T) {
	m, b := codeSessionDeleteModel(t)
	m.sessions.deleting, m.sessions.loading = true, true
	request := backend.WorkspaceLaunchRequest{Mode: backend.WorkspaceCurrent, WorkingDir: "/original", ProjectRoot: "/original", DeleteSessionID: "active"}
	handoff := &backend.WorkspaceHandoffError{Request: request}
	next, cmd := m.applySessionDeleted(sessionDeletedMsg{id: "active", err: fmt.Errorf("handoff: %w", handoff)})
	m = next.(model)
	if cmd == nil {
		t.Fatal("handoff did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("handoff did not return tea.Quit")
	}
	if !m.quitting || m.workspaceHandoff != handoff || m.workspaceHandoff.Request != request || m.sessions.err != "" || len(m.sessions.items) != 1 || b.newCalled {
		t.Fatal("handoff reset session or reported failure")
	}
}
