package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// ThemePickerStage models disk operations separately from cursor navigation.
type ThemePickerStage uint8

const (
	ThemePickerLoading ThemePickerStage = iota
	ThemePickerReady
	ThemePickerSaving
	ThemePickerLoadError
	ThemePickerSaveError
)

type themePickerState struct {
	open           bool
	stage          ThemePickerStage
	cursor         int
	saved, pending CodeThemeID
	err            string
	generation     uint64
	slot           int
	backend        backend.Backend
	owner          *events.Transcript
	session        string
}
type themePreferenceMsg struct {
	generation uint64
	slot       int
	backend    backend.Backend
	owner      *events.Transcript
	session    string
	saving     bool
	id         CodeThemeID
	err        error
}

func (m model) codeThemePreferencesCap() backend.CodeThemePreferencesBackend {
	if m.info.Mode != "code" {
		return nil
	}
	cap, _ := m.backend.(backend.CodeThemePreferencesBackend)
	return cap
}

// resolveInitialCodeTheme is read-only: CLI > valid saved preference > default.
func resolveInitialCodeTheme(ctx context.Context, b backend.Backend, override CodeThemeID) (Theme, error) {
	if override != "" {
		return ResolveCodeTheme(override)
	}
	if cap, ok := b.(backend.CodeThemePreferencesBackend); ok {
		saved, err := cap.LoadCodeThemePreference(ctx)
		if err != nil {
			return DefaultTheme(), err
		}
		for _, option := range BuiltinCodeThemes() {
			if string(option.ID) == saved {
				return ResolveCodeTheme(option.ID)
			}
		}
	}
	return DefaultTheme(), nil
}
func (m model) openThemePicker() (tea.Model, tea.Cmd) {
	if m.codeThemePreferencesCap() == nil {
		m.tr.Apply(events.NewSystem("/themes is available only in local Code."))
		m.refreshViewport()
		return m, nil
	}
	m.themeGeneration++
	m.themePicker = themePickerState{open: true, stage: ThemePickerLoading, generation: m.themeGeneration, slot: m.activeBackendIdx, backend: m.backend, owner: m.tr, session: m.tr.SessionID}
	for i, option := range BuiltinCodeThemes() {
		if option.ID == m.theme.CodeThemeID {
			m.themePicker.cursor = i
		}
	}
	// A submitted command is not a draft; direct callers keep their existing draft.
	if strings.TrimSpace(m.ta.Value()) == "/themes" {
		m.ta.Reset()
	}
	m.slash = slashCompletion{}
	m.files = fileCompletion{}
	if m.ready {
		offset := m.vp.YOffset()
		m.layout()
		m.vp.SetYOffset(offset)
	}
	return m, m.themePreferenceCmd(false)
}
func (m model) themePreferenceCmd(saving bool) tea.Cmd {
	p := m.themePicker
	cap := m.codeThemePreferencesCap()
	return func() tea.Msg {
		reply := themePreferenceMsg{generation: p.generation, slot: p.slot, backend: p.backend, owner: p.owner, session: p.session, saving: saving, id: p.pending}
		if cap == nil {
			reply.err = fmt.Errorf("Code theme preferences unavailable")
			return reply
		}
		if saving {
			reply.err = cap.SaveCodeThemePreference(m.ctx, string(p.pending))
		} else {
			var saved string
			saved, reply.err = cap.LoadCodeThemePreference(m.ctx)
			for _, option := range BuiltinCodeThemes() {
				if string(option.ID) == saved {
					reply.id = option.ID
				}
			}
		}
		return reply
	}
}
func (m model) applyThemePreferenceMsg(msg themePreferenceMsg) (tea.Model, tea.Cmd) {
	p := &m.themePicker
	if !p.open || msg.generation != p.generation || msg.slot != m.activeBackendIdx || msg.backend != m.backend || msg.owner != m.tr || msg.session != m.tr.SessionID {
		return m, nil
	}
	if msg.saving {
		if p.stage != ThemePickerSaving {
			return m, nil
		}
		if msg.err != nil {
			p.stage = ThemePickerSaveError
			p.err = "Could not save theme: " + msg.err.Error()
			return m, nil
		}
		m.applyCodeTheme(msg.id)
		m.themePicker.open = false
	} else {
		if p.stage != ThemePickerLoading {
			return m, nil
		}
		p.saved = msg.id
		p.stage = ThemePickerReady
		if msg.err != nil {
			p.stage = ThemePickerLoadError
			p.err = "Could not load saved theme: " + msg.err.Error()
		}
	}
	return m, nil
}
func (m model) handleThemePickerKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := &m.themePicker
	if p.stage == ThemePickerSaving {
		if key.String() == "esc" {
			p.err = "Saving; please wait"
		}
		return m, nil
	}
	if key.String() == "esc" {
		p.open = false
		m.themeGeneration++
		return m, nil
	}
	if key.String() == "r" && (p.stage == ThemePickerLoadError || p.stage == ThemePickerSaveError) {
		saving := p.stage == ThemePickerSaveError
		p.stage = ThemePickerLoading
		if saving {
			p.stage = ThemePickerSaving
		}
		p.err = ""
		m.themeGeneration++
		p.generation = m.themeGeneration
		return m, m.themePreferenceCmd(saving)
	}
	if p.stage == ThemePickerLoading {
		return m, nil
	}
	catalog := BuiltinCodeThemes()
	if len(catalog) == 0 {
		p.stage = ThemePickerLoadError
		p.err = "No built-in themes available"
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		p.cursor = max(0, p.cursor-1)
		p.stage = ThemePickerReady
		p.err = ""
	case "down", "j":
		p.cursor = min(len(catalog)-1, p.cursor+1)
		p.stage = ThemePickerReady
		p.err = ""
	case "enter":
		if p.cursor < 0 || p.cursor >= len(catalog) {
			return m, nil
		}
		p.pending = catalog[p.cursor].ID
		if _, err := ResolveCodeTheme(p.pending); err != nil {
			p.stage = ThemePickerSaveError
			p.err = err.Error()
			return m, nil
		}
		p.stage = ThemePickerSaving
		p.err = ""
		m.themeGeneration++
		p.generation = m.themeGeneration
		return m, m.themePreferenceCmd(true)
	}
	return m, nil
}
func (m model) renderThemePicker() string {
	p := m.themePicker
	lines := []string{m.theme.Brand.Render("Code themes")}
	for i, option := range BuiltinCodeThemes() {
		marker := "  "
		if i == p.cursor {
			marker = "› "
		}
		label := marker + option.Label
		if option.ID == m.theme.CodeThemeID {
			label += " · current"
		}
		if option.Default {
			label += " · default"
		}
		if option.ID == p.saved {
			label += " · saved"
		}
		style := m.theme.Text
		if i == p.cursor {
			style = m.theme.Brand
		}
		lines = append(lines, style.Render(label))
	}
	switch p.stage {
	case ThemePickerLoading:
		lines = append(lines, "Loading saved theme…", "Esc cancel")
	case ThemePickerSaving:
		lines = append(lines, "Saving theme…", "Please wait for disk confirmation")
	case ThemePickerLoadError:
		lines = append(lines, "r retry load · Enter apply and save · Esc cancel")
	case ThemePickerSaveError:
		lines = append(lines, "r retry save · ↑/↓ select · Esc close")
	default:
		lines = append(lines, "↑/↓ select · Enter apply and save · Esc cancel")
	}
	if p.err != "" {
		lines = append(lines, m.theme.Error.Render(wrapPlain(p.err, max(1, min(64, m.width-8)))))
	}
	return m.theme.InputBorder.Width(max(1, min(68, m.width-4))).Padding(0, 1).Render(strings.Join(lines, "\n"))
}

// handleStreamingThemeCommand admits only the /themes spelling while the
// regular composer is locked mid-turn. Existing drafts remain untouched.
func (m model) handleStreamingThemeCommand(key tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.codeThemePreferencesCap() == nil {
		return m, nil, false
	}
	value := m.ta.Value()
	if key.String() == "enter" && value == "/themes" {
		next, cmd := m.openThemePicker()
		return next, cmd, true
	}
	candidate := value + key.String()
	typing := len([]rune(key.String())) == 1 && strings.HasPrefix("/themes", candidate)
	deleting := key.String() == "backspace" && value != "" && strings.HasPrefix("/themes", value)
	if !typing && !deleting {
		return m, nil, false
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(key)
	m.syncSlashCompletion()
	return m, cmd, true
}

// applyCodeTheme changes palette only; no turn cancellation or transcript replacement.
// Snapshots are taken now, not when the picker opened: streamed state remains authoritative.
func (m *model) applyCodeTheme(id CodeThemeID) {
	if m.info.Mode != "code" {
		return
	}
	th, err := ResolveCodeTheme(id)
	if err != nil {
		return
	}
	offset, fileOffset, delegationOffset := m.vp.YOffset(), m.fileViewer.vp.YOffset(), m.delegationDetail.vp.YOffset()
	sidebarOffset, detailOffset := m.planPanel.sidebar.YOffset(), m.planPanel.detail.YOffset()
	target, reveal := m.planPanel.target, m.planPanel.revealPending
	stickyH, stickyIdx, stickyExpanded := m.stickyHeaderLines, m.stickyPinnedIdx, m.stickyExpanded
	m.theme = th
	m.backends[m.activeBackendIdx].theme = th
	m.mdCache = nil
	m.itemRenderCache = nil
	m.backends[m.activeBackendIdx].mdCache = nil
	m.delegationDetail.cacheKey = ""
	th.ApplyTextareaStyles(&m.ta)
	m.spin.Style = th.spinnerStyle()
	m.vp.Style = th.Background
	m.fileViewer.vp.Style = th.Background
	m.delegationDetail.vp.Style = th.Background
	m.planPanel.sidebar.Style = th.Background
	m.planPanel.detail.Style = th.Background
	content, hits, artifacts := m.viewportContent()
	m.hitRegions, m.artifactHits = hits, artifacts
	m.vp.SetContent(content)
	m.layoutFileViewer()
	m.layoutDelegationDetail()
	m.layoutPlanPanel()
	m.vp.SetYOffset(offset)
	m.fileViewer.vp.SetYOffset(fileOffset)
	m.delegationDetail.vp.SetYOffset(delegationOffset)
	m.planPanel.sidebar.SetYOffset(sidebarOffset)
	m.planPanel.detail.SetYOffset(detailOffset)
	m.planPanel.target, m.planPanel.revealPending = target, reveal
	m.stickyHeaderLines, m.stickyPinnedIdx, m.stickyExpanded = stickyH, stickyIdx, stickyExpanded
}
