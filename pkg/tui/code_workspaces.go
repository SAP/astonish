package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"fmt"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"path/filepath"
	"strings"
)

type WorkspacePickerStage uint8

const (
	WorkspaceChoosingMode WorkspacePickerStage = iota
	WorkspaceChoosingCopy
	WorkspacePreparing
	WorkspaceListing
	WorkspaceDetail
	WorkspaceReviewing
	WorkspaceConfirmingApply
	WorkspaceApplying
	WorkspaceConfirmingRemove
	WorkspaceRemoving
	WorkspaceBranchName
	WorkspaceError
)

type workspaceOperation uint8

const (
	workspaceLaunch workspaceOperation = iota
	workspaceList
	workspaceReview
	workspaceApply
	workspaceRemove
	workspaceBranch
)

type workspacePickerState struct {
	open                bool
	stage               WorkspacePickerStage
	cursor              int
	generation          uint64
	slot                int
	backend             backend.Backend
	owner               *events.Transcript
	session             string
	items               []backend.WorkspaceSummary
	selected            backend.WorkspaceSummary
	review              backend.WorkspaceReview
	request             backend.WorkspaceLaunchRequest
	op                  workspaceOperation
	loading             bool
	err, notice, branch string
}
type workspaceResultMsg struct {
	state   workspacePickerState
	items   []backend.WorkspaceSummary
	review  backend.WorkspaceReview
	request backend.WorkspaceLaunchRequest
	err     error
}

func (m model) workspaceCap() backend.CodeWorkspaceBackend {
	if m.info.Mode != "code" || m.botSelected() {
		return nil
	}
	c, _ := m.backend.(backend.CodeWorkspaceBackend)
	return c
}
func (m model) workspaceBusy() bool {
	return m.tr.Streaming || m.tr.Awaiting || m.compacting || m.planApprovalPending || m.turnCancel != nil || m.verificationLabel() != "" || m.workspacePicker.loading
}
func (m model) openWorkspacePicker(list bool) (tea.Model, tea.Cmd) {
	if m.workspaceCap() == nil {
		return m, m.workspaceUnavailable()
	}
	m.workspaceGeneration++
	m.workspacePicker = workspacePickerState{open: true, stage: WorkspaceChoosingMode, generation: m.workspaceGeneration, slot: m.activeBackendIdx, backend: m.backend, owner: m.tr, session: m.tr.SessionID}
	m.slash = slashCompletion{}
	m.files = fileCompletion{}
	if value := strings.TrimSpace(m.ta.Value()); value == "/workspaces" || value == "/new" {
		m.ta.Reset()
	}
	if list {
		m.workspacePicker.stage = WorkspaceListing
		return m.dispatchWorkspace(workspaceList)
	}
	p, err := config.LoadCodeWorkspacePreferences()
	if err != nil {
		m.workspacePicker.notice = err.Error()
	} else if p.NewSessionMode == "independent" {
		m.workspacePicker.cursor = 1
	}
	return m, nil
}
func (m *model) workspaceUnavailable() tea.Cmd {
	m.tr.Apply(events.NewSystem("Workspaces are available only in initialized local Code."))
	m.refreshViewport()
	return nil
}
func (m model) workspaceCmd(op workspaceOperation) tea.Cmd {
	m.workspacePicker.op = op // callers also set operation before dispatch
	p := m.workspacePicker
	p.op = op
	cap := m.workspaceCap()
	management, _ := m.backend.(backend.CodeWorkspaceManagementBackend)
	return func() tea.Msg {
		r := workspaceResultMsg{state: p}
		if cap == nil {
			r.err = fmt.Errorf("Code workspace support unavailable")
			return r
		}
		switch op {
		case workspaceLaunch:
			r.request, r.err = cap.PrepareWorkspaceLaunch(m.ctx, p.request)
		case workspaceList:
			r.items, r.err = cap.ListCodeWorkspaces(m.ctx)
		default:
			if management == nil {
				r.err = fmt.Errorf("Workspace management unavailable")
				return r
			}
			switch op {
			case workspaceReview:
				r.review, r.err = management.ReviewCodeWorkspace(m.ctx, p.selected.ID)
			case workspaceApply:
				r.err = management.ApplyCodeWorkspace(m.ctx, p.review)
			case workspaceRemove:
				r.err = management.RemoveCodeWorkspace(m.ctx, p.selected.ID)
			case workspaceBranch:
				r.err = management.CreateCodeWorkspaceBranch(m.ctx, p.selected.ID, p.branch)
			}
		}
		return r
	}
}
func (m model) dispatchWorkspace(op workspaceOperation) (tea.Model, tea.Cmd) {
	if op != workspaceList && op != workspaceReview && m.workspaceBusy() {
		m.workspacePicker.err = "Finish or cancel the current operation first"
		return m, nil
	}
	p := &m.workspacePicker
	p.op = op
	p.loading = true
	p.err = ""
	m.workspaceGeneration++
	p.generation = m.workspaceGeneration
	switch op {
	case workspaceLaunch, workspaceBranch:
		p.stage = WorkspacePreparing
	case workspaceList:
		p.stage = WorkspaceListing
	case workspaceReview:
		p.stage = WorkspaceReviewing
	case workspaceApply:
		p.stage = WorkspaceApplying
	case workspaceRemove:
		p.stage = WorkspaceRemoving
	}
	return m, m.workspaceCmd(op)
}
func (m model) applyWorkspaceResult(r workspaceResultMsg) (tea.Model, tea.Cmd) {
	p := &m.workspacePicker
	if !p.open || p.generation != r.state.generation || m.backend != r.state.backend || m.activeBackendIdx != r.state.slot || m.tr != r.state.owner || m.tr.SessionID != r.state.session {
		return m, nil
	}
	p.loading = false
	if r.err != nil {
		p.stage = WorkspaceError
		p.err = r.err.Error()
		p.notice = "Existing conversation and workspace files retained."
		return m, nil
	}
	switch r.state.op {
	case workspaceLaunch:
		m.workspaceHandoff = &backend.WorkspaceHandoffError{Request: r.request}
		m.quitting = true
		return m, tea.Quit
	case workspaceList:
		p.items = r.items
		p.cursor = min(p.cursor, max(0, len(p.items)-1))
		p.stage = WorkspaceListing
	case workspaceReview:
		p.review = r.review
		p.stage = WorkspaceReviewing
		p.cursor = 0
	case workspaceApply:
		p.stage = WorkspaceReviewing
		p.notice = "Changes applied to original checkout; staging preserved. No commit created."
		p.review.Digest = ""
	case workspaceRemove:
		p.stage = WorkspaceDetail
		p.selected.State = "removed"
		p.notice = "Checkout removed; transcript and branch retained."
	case workspaceBranch:
		p.stage = WorkspaceDetail
		p.notice = "Branch created; no checkout switch or commit."
	}
	return m, nil
}
func (m model) handleWorkspaceKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := &m.workspacePicker
	k := msg.String()
	if p.loading {
		p.notice = "Operation in progress"
		return m, nil
	}
	if k == "esc" || k == "ctrl+c" {
		switch p.stage {
		case WorkspaceChoosingCopy:
			p.stage = WorkspaceChoosingMode
			p.cursor = 1
		case WorkspaceDetail:
			p.stage = WorkspaceListing
			p.cursor = 0
		case WorkspaceReviewing, WorkspaceConfirmingRemove, WorkspaceBranchName:
			p.stage = WorkspaceDetail
			p.cursor = 0
		case WorkspaceConfirmingApply:
			p.stage = WorkspaceReviewing
		default:
			p.open = false
			m.workspaceGeneration++
		}
		return m, nil
	}
	if p.stage == WorkspaceError {
		if k == "r" {
			return m.dispatchWorkspace(p.op)
		}
		return m, nil
	}
	if p.stage == WorkspaceBranchName {
		switch k {
		case "enter":
			if strings.TrimSpace(p.branch) == "" {
				p.err = "Enter a branch name"
				return m, nil
			}
			return m.dispatchWorkspace(workspaceBranch)
		case "backspace":
			r := []rune(p.branch)
			if len(r) > 0 {
				p.branch = string(r[:len(r)-1])
			}
		default:
			if len([]rune(k)) == 1 {
				p.branch += k
			}
		}
		return m, nil
	}
	rows := m.workspaceRows()
	if k == "up" || k == "k" {
		p.cursor = max(0, p.cursor-1)
		return m, nil
	}
	if k == "down" || k == "j" {
		p.cursor = min(max(0, len(rows)-1), p.cursor+1)
		return m, nil
	}
	if p.stage == WorkspaceListing && k == "r" {
		return m.dispatchWorkspace(workspaceList)
	}
	if p.stage == WorkspaceListing && k == "n" {
		return m.openWorkspacePicker(false)
	}
	if k != "enter" {
		return m, nil
	}
	switch p.stage {
	case WorkspaceChoosingMode:
		if p.cursor >= 2 {
			mode := "current"
			if p.cursor == 3 {
				mode = "independent"
			}
			if err := config.SaveCodeWorkspacePreferences(config.CodeWorkspacePreferences{NewSessionMode: mode}); err != nil {
				p.err = err.Error()
			} else {
				p.notice = "Saved new-session preference"
			}
			return m, nil
		}
		if m.workspaceBusy() {
			p.err = "Finish or cancel the current operation first"
			return m, nil
		}
		if p.cursor == 0 {
			p.open = false
			return m.startCurrentSession()
		}
		p.stage = WorkspaceChoosingCopy
		p.cursor = 0
	case WorkspaceChoosingCopy:
		sessionID := p.request.SessionID
		p.request = backend.WorkspaceLaunchRequest{Mode: backend.WorkspaceIndependent, CopyMode: backend.WorkspaceCommitted, SessionID: sessionID}
		if p.cursor == 1 {
			p.request.CopyMode = backend.WorkspaceCurrentWork
		}
		return m.dispatchWorkspace(workspaceLaunch)
	case WorkspaceListing:
		if p.cursor == 0 {
			if m.tr.SessionID == "" {
				p.err = "Send a message first so the current session can be moved"
				return m, nil
			}
			p.request = backend.WorkspaceLaunchRequest{Mode: backend.WorkspaceIndependent, SessionID: m.tr.SessionID}
			p.stage = WorkspaceChoosingCopy
			p.cursor = 1 // Moving current work defaults to preserving its files.
			return m, nil
		}
		p.selected = p.items[p.cursor-1]
		p.stage = WorkspaceDetail
		p.cursor = 0
	case WorkspaceDetail:
		if p.selected.Active && filepath.Clean(p.selected.WorkingDir) != filepath.Clean(m.info.WorkingDir) {
			p.err = "Workspace active elsewhere; writer operations unavailable"
			return m, nil
		}
		switch p.cursor {
		case 0:
			if p.selected.State == "removed" {
				p.err = "Checkout missing; history retained. No automatic recreation."
				return m, nil
			}
			p.request = backend.WorkspaceLaunchRequest{Mode: backend.WorkspaceCurrent, WorkspaceID: p.selected.ID, WorkingDir: p.selected.WorkingDir}
			return m.dispatchWorkspace(workspaceLaunch)
		case 1:
			return m.dispatchWorkspace(workspaceReview)
		case 2:
			p.stage = WorkspaceBranchName
			p.branch = ""
		case 3:
			p.stage = WorkspaceConfirmingRemove
		}
	case WorkspaceReviewing:
		if len(p.review.Conflicts) > 0 {
			p.err = "Conflicts must be resolved before apply"
			return m, nil
		}
		if p.review.Digest == "" {
			return m.dispatchWorkspace(workspaceReview)
		}
		p.stage = WorkspaceConfirmingApply
	case WorkspaceConfirmingApply:
		return m.dispatchWorkspace(workspaceApply)
	case WorkspaceConfirmingRemove:
		return m.dispatchWorkspace(workspaceRemove)
	}
	return m, nil
}
func (m model) workspaceRows() []string {
	p := m.workspacePicker
	switch p.stage {
	case WorkspaceChoosingMode:
		return []string{"Current workspace", "Independent workspace", "Always current (save preference)", "Always independent (save preference)"}
	case WorkspaceChoosingCopy:
		return []string{"Committed HEAD (clean base)", "Include current staged, unstaged and untracked work"}
	case WorkspaceListing:
		r := []string{"Move current session to independent workspace"}
		for _, w := range p.items {
			label := first(w.Label, w.ID) + " · " + string(w.Mode) + " · " + w.State
			if w.Active {
				label += " · active"
			}
			if w.Retained {
				label += " · retained"
			}
			r = append(r, label)
		}
		return r
	case WorkspaceDetail:
		return []string{"Resume", "Review changes", "Create branch", "Remove checkout"}
	case WorkspaceReviewing:
		return []string{"Enter review / apply changes", p.review.Summary, fmt.Sprintf("%d paths · %d conflicts", len(p.review.Files), len(p.review.Conflicts))}
	case WorkspaceConfirmingApply:
		return []string{"Apply to " + p.selected.ProjectRoot + "?", "Preserve current staging · Enter confirm"}
	case WorkspaceConfirmingRemove:
		return []string{"Remove checkout " + p.selected.WorkingDir + "?", "Transcript and branch retained · Enter confirm"}
	case WorkspaceBranchName:
		return []string{"Branch name: " + p.branch, "Enter create · Esc cancel"}
	case WorkspaceError:
		return []string{p.err, "r retry · Esc close"}
	default:
		return []string{"Operation in progress"}
	}
}
func (m model) renderWorkspacePicker() string {
	p := m.workspacePicker
	box := m.theme.InputBorder.Width(max(1, min(76, m.width-4))).Padding(0, 1)
	if m.width < 6 || m.screenHeight() < 5 {
		box = box.Border(lipgloss.Border{}).Padding(0).Width(max(1, m.width))
	}
	width := max(1, box.GetWidth()-box.GetHorizontalFrameSize())
	height := max(1, m.screenHeight()-box.GetVerticalFrameSize())
	lines := []string{m.theme.Brand.Render(truncateToWidth("Code workspaces", width))}
	rows := m.workspaceRows()
	if p.stage == WorkspaceReviewing {
		rows = append(rows, "Source: "+p.selected.WorkingDir, "Target: "+p.selected.ProjectRoot)
		rows = append(rows, p.review.Files...)
		for _, c := range p.review.Conflicts {
			rows = append(rows, "Conflict: "+c)
		}
	}
	budget := max(1, height-3)
	start := max(0, p.cursor-budget+1)
	for i := start; i < min(len(rows), start+budget); i++ {
		s := "  " + rows[i]
		style := m.theme.Text
		if i == p.cursor {
			s = "› " + rows[i]
			style = m.theme.Brand
		}
		lines = append(lines, style.Render(truncateToWidth(s, width)))
	}
	footer := first(p.err, p.notice, "↑/↓ select · Enter choose · Esc back · r refresh")
	lines = append(lines, m.theme.Muted.Render(truncateToWidth(footer, width)))
	if len(lines) > height {
		lines = lines[:height]
	}
	return box.Render(strings.Join(lines, "\n"))
}
