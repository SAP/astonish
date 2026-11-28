package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"strings"
	"testing"
)

type workspaceTestBackend struct {
	staticBackend
	calls    int
	newCalls int
	fail     bool
	request  backend.WorkspaceLaunchRequest
	applied  bool
	removed  bool
	branch   string
}

func (b *workspaceTestBackend) Info() backend.Info {
	return backend.Info{Mode: "code", WorkingDir: "/original", SessionID: "existing", WorkspaceMode: backend.WorkspaceCurrent}
}
func (b *workspaceTestBackend) NewSession() { b.newCalls++ }
func (b *workspaceTestBackend) ListCodeWorkspaces(context.Context) ([]backend.WorkspaceSummary, error) {
	return []backend.WorkspaceSummary{{ID: "private", Label: "Task", WorkingDir: "/private", ProjectRoot: "/original", Mode: backend.WorkspaceIndependent, State: "retained"}}, nil
}
func (b *workspaceTestBackend) PrepareWorkspaceLaunch(_ context.Context, r backend.WorkspaceLaunchRequest) (backend.WorkspaceLaunchRequest, error) {
	b.calls++
	b.request = r
	if b.fail {
		return r, errors.New("prepare failed; retained path /private")
	}
	r.WorkingDir = "/private"
	r.Mode = backend.WorkspaceCurrent
	return r, nil
}
func (b *workspaceTestBackend) ReviewCodeWorkspace(context.Context, string) (backend.WorkspaceReview, error) {
	return backend.WorkspaceReview{WorkspaceID: "private", Digest: "digest", Summary: "Two changes", Files: []string{"a.txt", "b.bin"}}, nil
}
func (b *workspaceTestBackend) ApplyCodeWorkspace(context.Context, backend.WorkspaceReview) error {
	b.applied = true
	return nil
}
func (b *workspaceTestBackend) RemoveCodeWorkspace(context.Context, string) error {
	b.removed = true
	return nil
}
func (b *workspaceTestBackend) CreateCodeWorkspaceBranch(_ context.Context, _, name string) error {
	b.branch = name
	return nil
}
func workspaceModel(t *testing.T) (model, *workspaceTestBackend) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := &workspaceTestBackend{}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	t.Cleanup(m.cancel)
	m.ready = true
	m.ta.SetValue("keep draft")
	m.tr.Apply(events.NewSystem("keep transcript"))
	return m, b
}
func workspaceKey(m model, key string) (model, tea.Cmd) {
	var k tea.KeyPressMsg
	switch key {
	case "enter":
		k.Code = tea.KeyEnter
	case "esc":
		k.Code = tea.KeyEscape
	case "down":
		k.Code = tea.KeyDown
	case "up":
		k.Code = tea.KeyUp
	default:
		k.Code = []rune(key)[0]
	}
	next, cmd := m.Update(k)
	return next.(model), cmd
}
func workspaceReply(m model, cmd tea.Cmd) model { next, _ := m.Update(cmd()); return next.(model) }
func TestCodeWorkspaceNewChoicesAndHandoff(t *testing.T) {
	m, b := workspaceModel(t)
	owner := m.tr
	n, _ := m.startNewSession()
	m = n.(model)
	if m.workspacePicker.stage != WorkspaceChoosingMode || b.calls != 0 || b.newCalls != 0 {
		t.Fatal("chooser not read only")
	}
	m, _ = workspaceKey(m, "down")
	m, _ = workspaceKey(m, "enter")
	if m.workspacePicker.stage != WorkspaceChoosingCopy {
		t.Fatal("no copy chooser")
	}
	m, _ = workspaceKey(m, "esc")
	if m.workspacePicker.stage != WorkspaceChoosingMode {
		t.Fatal("no back")
	}
	m, _ = workspaceKey(m, "enter")
	m, _ = workspaceKey(m, "down")
	m, cmd := workspaceKey(m, "enter")
	if !m.workspacePicker.loading || cmd == nil {
		t.Fatal("not preparing")
	}
	m, _ = workspaceKey(m, "esc")
	if !m.workspacePicker.open || !strings.Contains(m.workspacePicker.notice, "progress") {
		t.Fatal("false cancellation")
	}
	m = workspaceReply(m, cmd)
	if m.workspaceHandoff == nil || !m.quitting || m.tr != owner || m.ta.Value() != "keep draft" || b.request.CopyMode != backend.WorkspaceCurrentWork || b.newCalls != 0 {
		t.Fatal("handoff lost state")
	}
}
func TestCodeWorkspaceCurrentCancelBusy(t *testing.T) {
	for _, busy := range []bool{false, true} {
		m, b := workspaceModel(t)
		m.compacting = busy
		n, _ := m.startNewSession()
		m = n.(model)
		m, cmd := workspaceKey(m, "enter")
		if busy {
			if b.newCalls != 0 || cmd != nil || !strings.Contains(m.workspacePicker.err, "Finish") {
				t.Fatal("busy discarded execution")
			}
			m, _ = workspaceKey(m, "esc")
			if m.ta.Value() != "keep draft" {
				t.Fatal("lost draft")
			}
		} else if b.newCalls != 1 || m.workspacePicker.open {
			t.Fatal("current did not create ordinary session")
		}
	}
}
func TestCodeWorkspaceErrorsRetryAndStale(t *testing.T) {
	m, b := workspaceModel(t)
	b.fail = true
	n, _ := m.startNewSession()
	m = n.(model)
	m, _ = workspaceKey(m, "down")
	m, _ = workspaceKey(m, "enter")
	m, cmd := workspaceKey(m, "enter")
	r := cmd().(workspaceResultMsg)
	old := m
	m.tr = events.NewTranscript()
	n, _ = m.applyWorkspaceResult(r)
	if n.(model).workspaceHandoff != nil {
		t.Fatal("stale owner accepted")
	}
	m = old
	n, _ = m.Update(r)
	m = n.(model)
	if m.workspacePicker.stage != WorkspaceError || !strings.Contains(m.renderWorkspacePicker(), "retained") {
		t.Fatal("error missing")
	}
	b.fail = false
	m, cmd = workspaceKey(m, "r")
	m = workspaceReply(m, cmd)
	if m.workspaceHandoff == nil {
		t.Fatal("retry failed")
	}
}
func TestCodeWorkspaceManagement(t *testing.T) {
	m, b := workspaceModel(t)
	n, cmd := m.openWorkspacePicker(true)
	m = workspaceReply(n.(model), cmd)
	m, _ = workspaceKey(m, "down") // select existing workspace after migrate action
	m, _ = workspaceKey(m, "enter")
	m, _ = workspaceKey(m, "down")
	m, cmd = workspaceKey(m, "enter")
	m = workspaceReply(m, cmd)
	if m.workspacePicker.stage != WorkspaceReviewing || !strings.Contains(m.renderWorkspacePicker(), "b.bin") {
		t.Fatal("review missing")
	}
	m, _ = workspaceKey(m, "enter")
	if m.workspacePicker.stage != WorkspaceConfirmingApply {
		t.Fatal("no confirmation")
	}
	m, _ = workspaceKey(m, "esc")
	if b.applied {
		t.Fatal("cancel applied")
	}
	m, _ = workspaceKey(m, "enter")
	m, cmd = workspaceKey(m, "enter")
	m = workspaceReply(m, cmd)
	if !b.applied || m.workspacePicker.stage != WorkspaceReviewing || !strings.Contains(m.workspacePicker.notice, "No commit") {
		t.Fatal("apply result")
	}
	m, _ = workspaceKey(m, "esc")
	m.workspacePicker.cursor = 2
	m, _ = workspaceKey(m, "enter")
	m, _ = workspaceKey(m, "t")
	m, cmd = workspaceKey(m, "enter")
	m = workspaceReply(m, cmd)
	if b.branch != "t" {
		t.Fatal("branch")
	}
	m.workspacePicker.cursor = 3
	m, _ = workspaceKey(m, "enter")
	m, _ = workspaceKey(m, "esc")
	if b.removed {
		t.Fatal("cancel removed")
	}
	m.workspacePicker.cursor = 3
	m, _ = workspaceKey(m, "enter")
	m, cmd = workspaceKey(m, "enter")
	m = workspaceReply(m, cmd)
	if !b.removed || m.workspacePicker.selected.State != "removed" {
		t.Fatal("remove")
	}
	m.workspacePicker.cursor = 0
	m, cmd = workspaceKey(m, "enter")
	if cmd != nil || !strings.Contains(m.workspacePicker.err, "missing") {
		t.Fatal("removed recreated")
	}
}
func TestCodeWorkspaceStagesGeometryAndGating(t *testing.T) {
	m, _ := workspaceModel(t)
	n, _ := m.openWorkspacePicker(false)
	m = n.(model)
	for _, theme := range BuiltinCodeThemes() {
		m.theme, _ = ResolveCodeTheme(theme.ID)
		for stage := WorkspaceChoosingMode; stage <= WorkspaceError; stage++ {
			m.workspacePicker.stage = stage
			for _, size := range [][2]int{{100, 30}, {22, 8}, {4, 3}} {
				m.width, m.height = size[0], size[1]
				s := m.renderWorkspacePicker()
				if lipgloss.Width(s) > m.width || lipgloss.Height(s) > m.screenHeight() {
					t.Fatalf("stage %d size %v rendered %dx%d", stage, size, lipgloss.Width(s), lipgloss.Height(s))
				}
			}
		}
	}
	m.info.Mode = "platform"
	if m.workspaceCap() != nil {
		t.Fatal("platform capability leak")
	}
}
func TestCodeWorkspaceSessionDestination(t *testing.T) {
	m, b := workspaceModel(t)
	m.sessions = sessionsState{open: true, items: []backend.SessionSummary{{ID: "resume-A", WorkspaceID: "A", WorkspacePath: "/other"}}}
	n, cmd := m.handleSessionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = n.(model)
	if cmd == nil {
		t.Fatal("no handoff")
	}
	m = workspaceReply(m, cmd)
	if b.request.SessionID != "resume-A" || m.workspaceHandoff == nil {
		t.Fatal("wrong resume")
	}
}
func TestCodeWorkspaceDualBackendMutationGuard(t *testing.T) {
	m, _ := workspaceModel(t)
	m.workspacePicker = workspacePickerState{loading: true}
	n, cmd := m.switchBackend()
	if cmd != nil || n.(model).activeBackendIdx != m.activeBackendIdx {
		t.Fatal("switched during mutation")
	}
}
