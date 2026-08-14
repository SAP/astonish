package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// webSearchPickerState is the /websearch configuration overlay.
type webSearchPickerState struct {
	open    bool
	loading bool
	err     string
	notice  string

	// step: "list" | "apikey" | "perplexity-provider" | "perplexity-model"
	step string

	providers []backend.WebSearchProvider
	items     []string // filtered list currently shown
	filter    string
	cursor    int

	// Selected provider for API key entry or Perplexity flow.
	selectedProvider backend.WebSearchProvider

	// API key input (masked).
	apiKeyInput string

	// Perplexity flow state.
	perplexityOptions  []backend.PerplexityOption
	selectedPerplexity string // provider name for Perplexity
	perplexityModels   []string
}

// --- Messages ---

type webSearchProvidersLoadedMsg struct {
	providers []backend.WebSearchProvider
	err       error
}

type webSearchInstalledMsg struct {
	serverID string
	err      error
}

type perplexityOptionsLoadedMsg struct {
	options []backend.PerplexityOption
	err     error
}

type perplexityConfiguredMsg struct {
	err error
}

type webSearchClearedMsg struct {
	err error
}

// --- Capability check ---

// webSearchAdmin returns the WebSearchAdminBackend capability of the active
// backend, or nil when the backend does not support local web search management.
func (m model) webSearchAdmin() backend.WebSearchAdminBackend {
	if ws, ok := m.backend.(backend.WebSearchAdminBackend); ok {
		return ws
	}
	return nil
}

// --- Open ---

func (m model) openWebSearchPicker() (tea.Model, tea.Cmd) {
	ws := m.webSearchAdmin()
	if ws == nil {
		m.tr.Apply(events.NewSystem("Web search configuration is only available in local code mode."))
		m.refreshViewport()
		return m, nil
	}
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil
	}
	m.webSearchPicker = webSearchPickerState{
		open:    true,
		loading: true,
		step:    "list",
	}
	m.slash = slashCompletion{}
	m.files = fileCompletion{}
	m.ta.Reset()
	return m, m.loadWebSearchProvidersCmd()
}

// --- Commands ---

func (m model) loadWebSearchProvidersCmd() tea.Cmd {
	return func() tea.Msg {
		ws := m.webSearchAdmin()
		if ws == nil {
			return webSearchProvidersLoadedMsg{err: fmt.Errorf("web search admin unavailable")}
		}
		providers, err := ws.ListWebSearchProviders(m.ctx)
		return webSearchProvidersLoadedMsg{providers: providers, err: err}
	}
}

func (m model) installWebSearchCmd(serverID, apiKey string) tea.Cmd {
	return func() tea.Msg {
		ws := m.webSearchAdmin()
		if ws == nil {
			return webSearchInstalledMsg{err: fmt.Errorf("web search admin unavailable")}
		}
		err := ws.InstallWebSearch(m.ctx, serverID, apiKey)
		return webSearchInstalledMsg{serverID: serverID, err: err}
	}
}

func (m model) loadPerplexityOptionsCmd() tea.Cmd {
	return func() tea.Msg {
		ws := m.webSearchAdmin()
		if ws == nil {
			return perplexityOptionsLoadedMsg{err: fmt.Errorf("web search admin unavailable")}
		}
		opts, err := ws.ListPerplexityOptions(m.ctx)
		return perplexityOptionsLoadedMsg{options: opts, err: err}
	}
}

func (m model) configurePerplexityCmd(provider, model string) tea.Cmd {
	return func() tea.Msg {
		ws := m.webSearchAdmin()
		if ws == nil {
			return perplexityConfiguredMsg{err: fmt.Errorf("web search admin unavailable")}
		}
		err := ws.ConfigurePerplexityWebSearch(m.ctx, provider, model)
		return perplexityConfiguredMsg{err: err}
	}
}

func (m model) clearWebSearchCmd() tea.Cmd {
	return func() tea.Msg {
		ws := m.webSearchAdmin()
		if ws == nil {
			return webSearchClearedMsg{err: fmt.Errorf("web search admin unavailable")}
		}
		err := ws.ClearWebSearch(m.ctx)
		return webSearchClearedMsg{err: err}
	}
}

// --- Apply messages ---

func (m model) applyWebSearchProvidersLoaded(msg webSearchProvidersLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.webSearchPicker.open {
		return m, nil
	}
	m.webSearchPicker.loading = false
	if msg.err != nil {
		m.webSearchPicker.err = "Failed to load web search providers: " + msg.err.Error()
		return m, nil
	}
	m.webSearchPicker.providers = msg.providers
	m.webSearchPicker.rebuildItems()
	return m, nil
}

func (m model) applyWebSearchInstalled(msg webSearchInstalledMsg) (tea.Model, tea.Cmd) {
	if !m.webSearchPicker.open {
		return m, nil
	}
	m.webSearchPicker.loading = false
	if msg.err != nil {
		m.webSearchPicker.err = "Failed to configure: " + msg.err.Error()
		m.webSearchPicker.step = "list"
		m.webSearchPicker.rebuildItems()
		return m, nil
	}
	m.webSearchPicker = webSearchPickerState{}
	m.tr.Apply(events.NewSystem("Web search configured! Start a /new session for the search tool to be available."))
	m.refreshViewport()
	return m, nil
}

func (m model) applyPerplexityOptionsLoaded(msg perplexityOptionsLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.webSearchPicker.open {
		return m, nil
	}
	m.webSearchPicker.loading = false
	if msg.err != nil {
		m.webSearchPicker.err = "Failed to load Perplexity options: " + msg.err.Error()
		m.webSearchPicker.step = "list"
		m.webSearchPicker.rebuildItems()
		return m, nil
	}
	if len(msg.options) == 0 {
		m.webSearchPicker.err = "No providers with Perplexity/Sonar models found. Add a provider with Sonar models first via /provider."
		m.webSearchPicker.step = "list"
		m.webSearchPicker.rebuildItems()
		return m, nil
	}
	m.webSearchPicker.perplexityOptions = msg.options
	m.webSearchPicker.step = "perplexity-provider"
	m.webSearchPicker.filter = ""
	m.webSearchPicker.cursor = 0
	m.webSearchPicker.rebuildItems()
	return m, nil
}

func (m model) applyPerplexityConfigured(msg perplexityConfiguredMsg) (tea.Model, tea.Cmd) {
	if !m.webSearchPicker.open {
		return m, nil
	}
	m.webSearchPicker.loading = false
	if msg.err != nil {
		m.webSearchPicker.err = "Failed to configure Perplexity: " + msg.err.Error()
		m.webSearchPicker.step = "perplexity-provider"
		m.webSearchPicker.rebuildItems()
		return m, nil
	}
	m.webSearchPicker = webSearchPickerState{}
	m.tr.Apply(events.NewSystem("Perplexity web search configured! Start a /new session for the search tool to be available."))
	m.refreshViewport()
	return m, nil
}

func (m model) applyWebSearchCleared(msg webSearchClearedMsg) (tea.Model, tea.Cmd) {
	if !m.webSearchPicker.open {
		return m, nil
	}
	m.webSearchPicker.loading = false
	if msg.err != nil {
		m.webSearchPicker.err = "Failed to clear: " + msg.err.Error()
		return m, nil
	}
	m.webSearchPicker = webSearchPickerState{}
	m.tr.Apply(events.NewSystem("Web search disabled."))
	m.refreshViewport()
	return m, nil
}

// --- Rebuild items ---

func (s *webSearchPickerState) rebuildItems() {
	q := strings.ToLower(strings.TrimSpace(s.filter))
	s.items = s.items[:0]

	switch s.step {
	case "list":
		for _, p := range s.providers {
			label := p.DisplayName
			if q == "" || strings.Contains(strings.ToLower(label), q) {
				s.items = append(s.items, label)
			}
		}
		// Add "None (disable)" option.
		none := "(None — disable web search)"
		if q == "" || strings.Contains(strings.ToLower(none), q) {
			s.items = append(s.items, none)
		}
	case "perplexity-provider":
		for _, opt := range s.perplexityOptions {
			if q == "" || strings.Contains(strings.ToLower(opt.Provider), q) {
				s.items = append(s.items, opt.Provider)
			}
		}
	case "perplexity-model":
		for _, model := range s.perplexityModels {
			if q == "" || strings.Contains(strings.ToLower(model), q) {
				s.items = append(s.items, model)
			}
		}
	}

	if s.cursor >= len(s.items) {
		s.cursor = len(s.items) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

// --- Key handling ---

func (m model) handleWebSearchPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if m.webSearchPicker.loading {
		if key == "esc" || key == "ctrl+c" {
			m.webSearchPicker = webSearchPickerState{}
			return m, nil
		}
		return m, nil
	}

	// API key input mode has special handling.
	if m.webSearchPicker.step == "apikey" {
		return m.handleWebSearchAPIKeyInput(msg)
	}

	switch key {
	case "esc", "ctrl+c":
		switch m.webSearchPicker.step {
		case "perplexity-model":
			m.webSearchPicker.step = "perplexity-provider"
			m.webSearchPicker.filter = ""
			m.webSearchPicker.rebuildItems()
			return m, nil
		case "perplexity-provider":
			m.webSearchPicker.step = "list"
			m.webSearchPicker.filter = ""
			m.webSearchPicker.rebuildItems()
			return m, nil
		default:
			m.webSearchPicker = webSearchPickerState{}
			return m, nil
		}
	case "up", "k":
		if m.webSearchPicker.cursor > 0 {
			m.webSearchPicker.cursor--
		}
		return m, nil
	case "down", "j":
		if m.webSearchPicker.cursor < len(m.webSearchPicker.items)-1 {
			m.webSearchPicker.cursor++
		}
		return m, nil
	case "backspace", "ctrl+h":
		if m.webSearchPicker.filter != "" {
			r := []rune(m.webSearchPicker.filter)
			m.webSearchPicker.filter = string(r[:len(r)-1])
			m.webSearchPicker.rebuildItems()
		}
		return m, nil
	case "enter", " ":
		return m.selectWebSearchPickerItem()
	}

	// Accumulate filter from printable runes.
	if msg.Key().Text != "" {
		m.webSearchPicker.filter += msg.Key().Text
		m.webSearchPicker.rebuildItems()
		return m, nil
	}
	return m, nil
}

func (m model) handleWebSearchAPIKeyInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc", "ctrl+c":
		m.webSearchPicker.step = "list"
		m.webSearchPicker.apiKeyInput = ""
		m.webSearchPicker.err = ""
		m.webSearchPicker.rebuildItems()
		return m, nil
	case "backspace", "ctrl+h":
		if len(m.webSearchPicker.apiKeyInput) > 0 {
			r := []rune(m.webSearchPicker.apiKeyInput)
			m.webSearchPicker.apiKeyInput = string(r[:len(r)-1])
		}
		return m, nil
	case "enter":
		apiKey := strings.TrimSpace(m.webSearchPicker.apiKeyInput)
		if apiKey == "" {
			m.webSearchPicker.err = "API key is required"
			return m, nil
		}
		m.webSearchPicker.loading = true
		m.webSearchPicker.err = ""
		m.webSearchPicker.notice = "Configuring…"
		return m, m.installWebSearchCmd(m.webSearchPicker.selectedProvider.ID, apiKey)
	}

	// Accumulate printable runes into API key (both typed and pasted).
	if msg.Key().Text != "" {
		m.webSearchPicker.apiKeyInput += msg.Key().Text
		return m, nil
	}
	return m, nil
}

// --- Select item ---

func (m model) selectWebSearchPickerItem() (tea.Model, tea.Cmd) {
	if len(m.webSearchPicker.items) == 0 {
		return m, nil
	}
	item := m.webSearchPicker.items[m.webSearchPicker.cursor]

	switch m.webSearchPicker.step {
	case "list":
		// "(None — disable web search)" option.
		if strings.HasPrefix(item, "(None") {
			m.webSearchPicker.loading = true
			m.webSearchPicker.notice = "Clearing…"
			return m, m.clearWebSearchCmd()
		}
		// Find the matching provider.
		var selected *backend.WebSearchProvider
		for i := range m.webSearchPicker.providers {
			if m.webSearchPicker.providers[i].DisplayName == item {
				selected = &m.webSearchPicker.providers[i]
				break
			}
		}
		if selected == nil {
			return m, nil
		}
		m.webSearchPicker.selectedProvider = *selected

		if selected.Kind == "model" {
			// Perplexity flow: load provider options.
			m.webSearchPicker.loading = true
			m.webSearchPicker.notice = "Loading Perplexity options…"
			return m, m.loadPerplexityOptionsCmd()
		}
		// MCP server: prompt for API key.
		m.webSearchPicker.step = "apikey"
		m.webSearchPicker.apiKeyInput = ""
		m.webSearchPicker.filter = ""
		m.webSearchPicker.err = ""
		return m, nil

	case "perplexity-provider":
		// Find models for the selected provider.
		m.webSearchPicker.selectedPerplexity = item
		for _, opt := range m.webSearchPicker.perplexityOptions {
			if opt.Provider == item {
				m.webSearchPicker.perplexityModels = opt.Models
				break
			}
		}
		m.webSearchPicker.step = "perplexity-model"
		m.webSearchPicker.filter = ""
		m.webSearchPicker.cursor = 0
		m.webSearchPicker.rebuildItems()
		return m, nil

	case "perplexity-model":
		// Configure Perplexity with selected provider + model.
		m.webSearchPicker.loading = true
		m.webSearchPicker.notice = "Configuring Perplexity…"
		return m, m.configurePerplexityCmd(m.webSearchPicker.selectedPerplexity, item)
	}

	return m, nil
}

// --- Render ---

func (m model) renderWebSearchPickerOverlay() string {
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
	title := "Web Search"
	help := "  ↑↓ move  enter select  type filter  esc close"

	switch m.webSearchPicker.step {
	case "apikey":
		title = "Web Search · " + m.webSearchPicker.selectedProvider.DisplayName
		help = "  type key  enter confirm  esc back"
	case "perplexity-provider":
		title = "Web Search · Perplexity · Provider"
		help = "  ↑↓ move  enter select  type filter  esc back"
	case "perplexity-model":
		title = "Web Search · Perplexity · " + m.webSearchPicker.selectedPerplexity
		help = "  ↑↓ move  enter select  type filter  esc back"
	}

	body.WriteString(th.Header.Render(title) + th.Muted.Render(help) + "\n")

	// Show current status.
	if m.webSearchPicker.step == "list" {
		var current string
		for _, p := range m.webSearchPicker.providers {
			if p.Active {
				current = p.DisplayName
				break
			}
		}
		if current == "" {
			current = "(none)"
		}
		body.WriteString(th.Muted.Render("Currently: "+current) + "\n\n")
	}

	if m.webSearchPicker.loading {
		body.WriteString(th.Muted.Render(first(m.webSearchPicker.notice, "Loading…")))
	} else if m.webSearchPicker.err != "" {
		body.WriteString(th.Error.Render(m.webSearchPicker.err))
	} else if m.webSearchPicker.step == "apikey" {
		// API key input form.
		body.WriteString(th.Text.Render("Enter API key for "+m.webSearchPicker.selectedProvider.DisplayName+":") + "\n\n")
		masked := strings.Repeat("•", len(m.webSearchPicker.apiKeyInput))
		if masked == "" {
			masked = th.Muted.Render("(paste or type your key)")
		}
		body.WriteString("  " + masked + "\n")
	} else if len(m.webSearchPicker.items) == 0 {
		if m.webSearchPicker.filter != "" {
			body.WriteString(th.Muted.Render(fmt.Sprintf("No matches for %q", m.webSearchPicker.filter)))
		} else {
			body.WriteString(th.Muted.Render("No options available."))
		}
	} else {
		if m.webSearchPicker.filter != "" {
			body.WriteString(th.Muted.Render("Filter: "+m.webSearchPicker.filter) + "\n\n")
		}
		maxRows := h - 6
		if maxRows < 3 {
			maxRows = 3
		}
		start := 0
		if m.webSearchPicker.cursor >= maxRows {
			start = m.webSearchPicker.cursor - maxRows + 1
		}
		end := start + maxRows
		if end > len(m.webSearchPicker.items) {
			end = len(m.webSearchPicker.items)
		}
		for i := start; i < end; i++ {
			item := m.webSearchPicker.items[i]
			mark := "  "
			style := th.Text
			if i == m.webSearchPicker.cursor {
				mark = "› "
				style = th.Success
			}
			// Add status suffix for the list step.
			suffix := ""
			if m.webSearchPicker.step == "list" && !strings.HasPrefix(item, "(None") {
				for _, p := range m.webSearchPicker.providers {
					if p.DisplayName == item {
						if p.Active {
							suffix = th.Success.Render("  ✓ active")
						} else if p.Installed {
							suffix = th.Muted.Render("  (installed)")
						}
						break
					}
				}
			}
			body.WriteString(style.Render(mark+item) + suffix + "\n")
		}
		if end < len(m.webSearchPicker.items) {
			body.WriteString(th.Muted.Render(fmt.Sprintf("  … %d more", len(m.webSearchPicker.items)-end)))
		}
	}

	box := th.InputBorderFocus.
		Width(w - 2).
		MaxHeight(h).
		Padding(1, 2).
		Render(body.String())
	return m.paintCompletionPopup(box, w-2)
}
