package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/events"
)

// modelPickerState is the /model provider → model selection overlay.
type modelPickerState struct {
	open             bool
	loading          bool
	err              string
	notice           string
	step             string // "provider" | "model"
	providers        []string
	models           []string
	items            []string // filtered list currently shown
	filter           string
	cursor           int
	selectedProvider string
	currentProvider  string
	currentModel     string
}

type modelProvidersLoadedMsg struct {
	providers []string
	err       error
}

type modelModelsLoadedMsg struct {
	provider string
	models   []string
	err      error
}

type modelPinAppliedMsg struct {
	provider string
	model    string
	effP     string
	effM     string
	err      error
}

func (m model) openModelPicker() (tea.Model, tea.Cmd) {
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil
	}
	m.modelPicker = modelPickerState{
		open:            true,
		loading:         true,
		step:            "provider",
		currentProvider: m.info.Provider,
		currentModel:    m.info.Model,
	}
	m.slash = slashCompletion{}
	m.files = fileCompletion{}
	m.ta.Reset()
	return m, m.loadModelProvidersCmd()
}

func (m model) loadModelProvidersCmd() tea.Cmd {
	return func() tea.Msg {
		providers, err := m.backend.ListProviders(m.ctx)
		return modelProvidersLoadedMsg{providers: providers, err: err}
	}
}

func (m model) loadModelModelsCmd(provider string) tea.Cmd {
	return func() tea.Msg {
		models, err := m.backend.ListModels(m.ctx, provider)
		return modelModelsLoadedMsg{provider: provider, models: models, err: err}
	}
}

func (m model) setModelPinCmd(provider, model string) tea.Cmd {
	return func() tea.Msg {
		effP, effM, err := m.backend.SetModelPin(m.ctx, provider, model)
		return modelPinAppliedMsg{
			provider: provider,
			model:    model,
			effP:     effP,
			effM:     effM,
			err:      err,
		}
	}
}

func (m model) applyModelProvidersLoaded(msg modelProvidersLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.modelPicker.open {
		return m, nil
	}
	m.modelPicker.loading = false
	if msg.err != nil {
		m.modelPicker.err = "Failed to load providers: " + msg.err.Error()
		return m, nil
	}
	m.modelPicker.err = ""
	m.modelPicker.providers = append([]string(nil), msg.providers...)
	m.modelPicker.rebuildItems()
	// Prefer highlighting the current provider when present.
	for i, p := range m.modelPicker.items {
		if p == m.modelPicker.currentProvider {
			m.modelPicker.cursor = i
			break
		}
	}
	return m, nil
}

func (m model) applyModelModelsLoaded(msg modelModelsLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.modelPicker.open || m.modelPicker.selectedProvider != msg.provider {
		return m, nil
	}
	m.modelPicker.loading = false
	if msg.err != nil {
		m.modelPicker.err = "Failed to load models: " + msg.err.Error()
		m.modelPicker.step = "provider"
		m.modelPicker.rebuildItems()
		return m, nil
	}
	m.modelPicker.err = ""
	m.modelPicker.models = append([]string(nil), msg.models...)
	m.modelPicker.filter = ""
	m.modelPicker.cursor = 0
	m.modelPicker.rebuildItems()
	for i, modelName := range m.modelPicker.items {
		if modelName == m.modelPicker.currentModel {
			m.modelPicker.cursor = i
			break
		}
	}
	return m, nil
}

func (m model) applyModelPinApplied(msg modelPinAppliedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if m.modelPicker.open {
			m.modelPicker.loading = false
			m.modelPicker.err = "Failed to set model: " + msg.err.Error()
			return m, nil
		}
		m.tr.Apply(events.NewError("Failed to set model: " + msg.err.Error()))
		m.refreshViewport()
		return m, nil
	}
	if msg.effP != "" {
		m.info.Provider = msg.effP
	}
	if msg.effM != "" {
		m.info.Model = msg.effM
	}
	m.modelPicker = modelPickerState{}
	label := modelFooterText(m.info.Provider, m.info.Model)
	if msg.provider == "" && msg.model == "" {
		m.tr.Apply(events.NewSystem("Model pin cleared — using cascade default (" + label + ")."))
	} else {
		m.tr.Apply(events.NewSystem("Model set to " + label + "."))
	}
	m.refreshViewport()
	return m, nil
}

func (s *modelPickerState) rebuildItems() {
	q := strings.ToLower(strings.TrimSpace(s.filter))
	src := s.providers
	if s.step == "model" {
		src = s.models
	}
	s.items = s.items[:0]
	if s.step == "provider" {
		// Always offer cascade default first when not filtering, or when it matches.
		cascade := "(cascade default)"
		if q == "" || strings.Contains(cascade, q) || strings.HasPrefix("default", q) || strings.HasPrefix("cascade", q) {
			s.items = append(s.items, cascade)
		}
	}
	for _, item := range src {
		if q == "" || strings.Contains(strings.ToLower(item), q) {
			s.items = append(s.items, item)
		}
	}
	if s.cursor >= len(s.items) {
		s.cursor = len(s.items) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

func (m model) handleModelPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.modelPicker.loading {
		if key == "esc" || key == "ctrl+c" {
			m.modelPicker = modelPickerState{}
			return m, nil
		}
		return m, nil
	}

	switch key {
	case "esc", "ctrl+c":
		if m.modelPicker.step == "model" {
			m.modelPicker.step = "provider"
			m.modelPicker.filter = ""
			m.modelPicker.selectedProvider = ""
			m.modelPicker.err = ""
			m.modelPicker.rebuildItems()
			return m, nil
		}
		m.modelPicker = modelPickerState{}
		return m, nil
	case "up", "k":
		if m.modelPicker.cursor > 0 {
			m.modelPicker.cursor--
		}
		return m, nil
	case "down", "j":
		if m.modelPicker.cursor < len(m.modelPicker.items)-1 {
			m.modelPicker.cursor++
		}
		return m, nil
	case "backspace", "ctrl+h":
		if m.modelPicker.filter != "" {
			r := []rune(m.modelPicker.filter)
			m.modelPicker.filter = string(r[:len(r)-1])
			m.modelPicker.rebuildItems()
		}
		return m, nil
	case "enter", " ":
		return m.selectModelPickerItem()
	}

	// Accumulate filter from printable runes.
	if msg.Key().Text != "" {
		m.modelPicker.filter += msg.Key().Text
		m.modelPicker.rebuildItems()
		return m, nil
	}
	return m, nil
}

func (m model) selectModelPickerItem() (tea.Model, tea.Cmd) {
	if len(m.modelPicker.items) == 0 {
		return m, nil
	}
	item := m.modelPicker.items[m.modelPicker.cursor]
	if m.modelPicker.step == "provider" {
		if item == "(cascade default)" {
			m.modelPicker.loading = true
			m.modelPicker.err = ""
			m.modelPicker.notice = "Clearing pin…"
			return m, m.setModelPinCmd("", "")
		}
		m.modelPicker.selectedProvider = item
		m.modelPicker.step = "model"
		m.modelPicker.loading = true
		m.modelPicker.filter = ""
		m.modelPicker.models = nil
		m.modelPicker.items = nil
		m.modelPicker.cursor = 0
		m.modelPicker.err = ""
		m.modelPicker.notice = "Loading models…"
		return m, m.loadModelModelsCmd(item)
	}
	// Model step
	m.modelPicker.loading = true
	m.modelPicker.err = ""
	m.modelPicker.notice = "Applying model…"
	return m, m.setModelPinCmd(m.modelPicker.selectedProvider, item)
}

func (m model) renderModelPickerOverlay() string {
	th := m.theme
	w := m.width
	if w < 40 {
		w = 40
	}
	h := m.height - 4
	if h < 8 {
		h = 8
	}

	var body strings.Builder
	title := "Model"
	help := "  ↑↓ move  enter select  type filter  esc close"
	if m.modelPicker.step == "model" {
		title = "Model · " + m.modelPicker.selectedProvider
		help = "  ↑↓ move  enter pin  type filter  esc back"
	}
	body.WriteString(th.Header.Render(title) + th.Muted.Render(help) + "\n")
	current := modelFooterText(m.modelPicker.currentProvider, m.modelPicker.currentModel)
	body.WriteString(th.Muted.Render("Currently: "+current) + "\n\n")

	if m.modelPicker.loading {
		body.WriteString(th.Muted.Render(first(m.modelPicker.notice, "Loading…")))
	} else if m.modelPicker.err != "" {
		body.WriteString(th.Error.Render(m.modelPicker.err))
	} else if len(m.modelPicker.items) == 0 {
		if m.modelPicker.filter != "" {
			body.WriteString(th.Muted.Render(fmt.Sprintf("No matches for %q", m.modelPicker.filter)))
		} else if m.modelPicker.step == "provider" {
			body.WriteString(th.Muted.Render("No providers configured. Add one in Studio Settings."))
		} else {
			body.WriteString(th.Muted.Render("No models returned for this provider."))
		}
	} else {
		if m.modelPicker.filter != "" {
			body.WriteString(th.Muted.Render("Filter: "+m.modelPicker.filter) + "\n\n")
		}
		maxRows := h - 6
		if maxRows < 3 {
			maxRows = 3
		}
		start := 0
		if m.modelPicker.cursor >= maxRows {
			start = m.modelPicker.cursor - maxRows + 1
		}
		end := start + maxRows
		if end > len(m.modelPicker.items) {
			end = len(m.modelPicker.items)
		}
		for i := start; i < end; i++ {
			item := m.modelPicker.items[i]
			mark := "  "
			style := th.Text
			if i == m.modelPicker.cursor {
				mark = "› "
				style = th.Success
			}
			// Mark current selection.
			suffix := ""
			if m.modelPicker.step == "provider" && item == m.modelPicker.currentProvider {
				suffix = th.Muted.Render("  (current)")
			}
			if m.modelPicker.step == "model" && item == m.modelPicker.currentModel &&
				m.modelPicker.selectedProvider == m.modelPicker.currentProvider {
				suffix = th.Muted.Render("  (current)")
			}
			body.WriteString(style.Render(mark+item) + suffix + "\n")
		}
		if end < len(m.modelPicker.items) {
			body.WriteString(th.Muted.Render(fmt.Sprintf("  … %d more", len(m.modelPicker.items)-end)))
		}
	}

	box := th.InputBorderFocus.
		Width(w-2).
		MaxHeight(h).
		Padding(1, 2).
		Render(body.String())
	return m.paintCompletionPopup(box, w-2)
}
