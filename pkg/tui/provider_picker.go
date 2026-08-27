package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// providerPickerState is the /provider manager overlay for code mode. It lists
// configured provider instances and lets the user add or remove them; all
// changes persist to the local config file via the ProviderAdminBackend.
type providerPickerState struct {
	open    bool
	loading bool
	err     string
	notice  string

	// step: "list" | "type" | "form" | "oauth"
	step string

	instances []backend.ProviderInstance
	types     []backend.ProviderTypeInfo

	cursor int

	// type step
	selectedType backend.ProviderTypeInfo

	// form step
	instanceName string
	fields       []backend.ProviderField
	values       []string // one per field (+ index 0 is the instance name)
	fieldCursor  int      // 0 = instance name, then 1..len(fields)
}

type providerInstancesLoadedMsg struct {
	instances []backend.ProviderInstance
	err       error
}

type providerMutatedMsg struct {
	action string // "add" | "remove"
	name   string
	err    error
}

type xaiOAuthStartedMsg struct {
	name    string
	fields  map[string]string
	pending backend.XAIOAuthPending
	err     error
}

// providerAdmin returns the ProviderAdminBackend capability of the active
// backend, or nil when the backend does not support local provider management
// (e.g. platform chat). The /provider command is only offered when non-nil.
func (m model) providerAdmin() backend.ProviderAdminBackend {
	if pa, ok := m.backend.(backend.ProviderAdminBackend); ok {
		return pa
	}
	return nil
}

func (m model) xaiOAuth() backend.XAIOAuthBackend {
	if xo, ok := m.backend.(backend.XAIOAuthBackend); ok {
		return xo
	}
	return nil
}

func (m model) openProviderPicker() (tea.Model, tea.Cmd) {
	pa := m.providerAdmin()
	if pa == nil {
		m.tr.Apply(events.NewSystem("Provider management is only available in local code mode."))
		m.refreshViewport()
		return m, nil
	}
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil
	}
	m.providerPicker = providerPickerState{
		open:    true,
		loading: true,
		step:    "list",
		types:   pa.ProviderTypes(),
	}
	m.slash = slashCompletion{}
	m.files = fileCompletion{}
	m.ta.Reset()
	return m, m.loadProviderInstancesCmd()
}

func (m model) loadProviderInstancesCmd() tea.Cmd {
	pa := m.providerAdmin()
	return func() tea.Msg {
		if pa == nil {
			return providerInstancesLoadedMsg{err: fmt.Errorf("provider management unavailable")}
		}
		instances, err := pa.ListProviderInstances(m.ctx)
		return providerInstancesLoadedMsg{instances: instances, err: err}
	}
}

func (m model) addProviderCmd(name, typeID string, fields map[string]string) tea.Cmd {
	pa := m.providerAdmin()
	return func() tea.Msg {
		if pa == nil {
			return providerMutatedMsg{action: "add", name: name, err: fmt.Errorf("provider management unavailable")}
		}
		err := pa.AddProvider(m.ctx, name, typeID, fields)
		return providerMutatedMsg{action: "add", name: name, err: err}
	}
}

func (m model) startXAIOAuthCmd(name string, fields map[string]string) tea.Cmd {
	xo := m.xaiOAuth()
	return func() tea.Msg {
		if xo == nil {
			return xaiOAuthStartedMsg{name: name, fields: fields, err: fmt.Errorf("xAI OAuth unavailable")}
		}
		pending, err := xo.StartXAIOAuth(m.ctx, fields["client_id"])
		msg := xaiOAuthStartedMsg{name: name, fields: fields, err: err}
		if pending != nil {
			msg.pending = *pending
		}
		return msg
	}
}

func (m model) waitXAIOAuthCmd(msg xaiOAuthStartedMsg) tea.Cmd {
	xo := m.xaiOAuth()
	pa := m.providerAdmin()
	return func() tea.Msg {
		if xo == nil || pa == nil {
			return providerMutatedMsg{action: "add", name: msg.name, err: fmt.Errorf("xAI OAuth unavailable")}
		}
		tokens, err := xo.WaitXAIOAuth(m.ctx, msg.pending)
		if err != nil {
			return providerMutatedMsg{action: "add", name: msg.name, err: err}
		}
		fields := make(map[string]string, len(msg.fields)+len(tokens))
		for k, v := range msg.fields {
			fields[k] = v
		}
		for k, v := range tokens {
			fields[k] = v
		}
		err = pa.AddProvider(m.ctx, msg.name, "xai_oauth", fields)
		return providerMutatedMsg{action: "add", name: msg.name, err: err}
	}
}

func (m model) removeProviderCmd(name string) tea.Cmd {
	pa := m.providerAdmin()
	return func() tea.Msg {
		if pa == nil {
			return providerMutatedMsg{action: "remove", name: name, err: fmt.Errorf("provider management unavailable")}
		}
		err := pa.RemoveProvider(m.ctx, name)
		return providerMutatedMsg{action: "remove", name: name, err: err}
	}
}

func (m model) applyProviderInstancesLoaded(msg providerInstancesLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.providerPicker.open {
		return m, nil
	}
	m.providerPicker.loading = false
	if msg.err != nil {
		m.providerPicker.err = "Failed to load providers: " + msg.err.Error()
		return m, nil
	}
	m.providerPicker.err = ""
	m.providerPicker.instances = append([]backend.ProviderInstance(nil), msg.instances...)
	if m.providerPicker.cursor >= len(m.providerPicker.instances) {
		m.providerPicker.cursor = 0
	}
	return m, nil
}

// handleProviderPickerKey routes keys while the /provider overlay is open.
func (m model) handleProviderPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.providerPicker.loading {
		if key == "esc" || key == "ctrl+c" {
			if m.providerPicker.step == "oauth" {
				return m.cancelProviderOAuth()
			}
			m.providerPicker = providerPickerState{}
			return m, nil
		}
		return m, nil
	}
	switch m.providerPicker.step {
	case "list":
		return m.handleProviderListKey(key)
	case "type":
		return m.handleProviderTypeKey(key)
	case "form":
		return m.handleProviderFormKey(msg, key)
	case "oauth":
		return m.handleProviderOAuthKey(key)
	}
	return m, nil
}

func (m model) cancelProviderOAuth() (tea.Model, tea.Cmd) {
	// Return to the form so the user can retry without closing /provider.
	// The in-flight WaitXAIOAuth cmd may still complete; applyProviderMutated
	// ignores it if the overlay has left the oauth step (or was closed).
	m.providerPicker.step = "form"
	m.providerPicker.err = ""
	m.providerPicker.notice = ""
	m.providerPicker.loading = false
	return m, nil
}

func (m model) handleProviderOAuthKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+c":
		return m.cancelProviderOAuth()
	}
	return m, nil
}

func (m model) handleProviderListKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+c", "q":
		m.providerPicker = providerPickerState{}
		return m, nil
	case "up", "k":
		if m.providerPicker.cursor > 0 {
			m.providerPicker.cursor--
		}
	case "down", "j":
		if m.providerPicker.cursor < len(m.providerPicker.instances)-1 {
			m.providerPicker.cursor++
		}
	case "a":
		// Begin add flow at the type-selection step.
		m.providerPicker.step = "type"
		m.providerPicker.cursor = 0
		m.providerPicker.err = ""
	case "d", "x":
		if len(m.providerPicker.instances) == 0 {
			return m, nil
		}
		name := m.providerPicker.instances[m.providerPicker.cursor].Name
		m.providerPicker.loading = true
		m.providerPicker.notice = "Removing " + name + "…"
		return m, m.removeProviderCmd(name)
	}
	return m, nil
}

func (m model) handleProviderTypeKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+c":
		m.providerPicker.step = "list"
		m.providerPicker.err = ""
		m.providerPicker.cursor = 0
		return m, nil
	case "up", "k":
		if m.providerPicker.cursor > 0 {
			m.providerPicker.cursor--
		}
	case "down", "j":
		if m.providerPicker.cursor < len(m.providerPicker.types)-1 {
			m.providerPicker.cursor++
		}
	case "enter", " ":
		if len(m.providerPicker.types) == 0 {
			return m, nil
		}
		t := m.providerPicker.types[m.providerPicker.cursor]
		m.providerPicker.selectedType = t
		m.providerPicker.fields = append([]backend.ProviderField(nil), t.Fields...)
		// values[0] is the instance name; the rest map to fields.
		m.providerPicker.values = make([]string, len(t.Fields)+1)
		m.providerPicker.values[0] = t.ID // sensible default instance name
		for i, f := range t.Fields {
			m.providerPicker.values[i+1] = f.Default
		}
		m.providerPicker.instanceName = t.ID
		m.providerPicker.fieldCursor = 0
		m.providerPicker.step = "form"
		m.providerPicker.err = ""
	}
	return m, nil
}

func (m model) handleProviderFormKey(msg tea.KeyMsg, key string) (tea.Model, tea.Cmd) {
	total := len(m.providerPicker.values) // name + fields
	switch key {
	case "esc", "ctrl+c":
		m.providerPicker.step = "type"
		m.providerPicker.err = ""
		return m, nil
	case "ctrl+v", "super+v", "cmd+v":
		// Explicit clipboard paste into the focused field.
		if text, err := clipboard.ReadAll(); err == nil && text != "" {
			clean := strings.TrimSpace(strings.NewReplacer("\r", "", "\n", " ", "\t", " ").Replace(text))
			m.providerPicker.values[m.providerPicker.fieldCursor] += clean
		}
		return m, nil
	case "up":
		if m.providerPicker.fieldCursor > 0 {
			m.providerPicker.fieldCursor--
		}
		return m, nil
	case "down", "tab":
		if m.providerPicker.fieldCursor < total-1 {
			m.providerPicker.fieldCursor++
		}
		return m, nil
	case "enter":
		// Enter on the last field submits; otherwise advance.
		if m.providerPicker.fieldCursor < total-1 {
			m.providerPicker.fieldCursor++
			return m, nil
		}
		return m.submitProviderForm()
	case "backspace", "ctrl+h":
		i := m.providerPicker.fieldCursor
		if v := m.providerPicker.values[i]; v != "" {
			r := []rune(v)
			m.providerPicker.values[i] = string(r[:len(r)-1])
		}
		return m, nil
	}
	// Accept typed runes and pasted text. Pastes arrive as KeyRunes with
	// Paste=true (and may be multi-rune); API-key/base-url fields are
	// single-line, so collapse any newlines/tabs from a paste into spaces and
	// trim surrounding whitespace.
	if msg.Key().Text != "" {
		text := msg.Key().Text
		m.providerPicker.values[m.providerPicker.fieldCursor] += text
	}
	return m, nil
}

func (m model) submitProviderForm() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(m.providerPicker.values[0])
	if name == "" {
		m.providerPicker.err = "Instance name is required."
		return m, nil
	}
	fields := make(map[string]string, len(m.providerPicker.fields))
	for i, f := range m.providerPicker.fields {
		fields[f.Key] = strings.TrimSpace(m.providerPicker.values[i+1])
	}
	m.providerPicker.loading = true
	m.providerPicker.err = ""
	if m.providerPicker.selectedType.ID == "xai_oauth" && m.xaiOAuth() != nil {
		m.providerPicker.notice = "Requesting xAI authorization…"
		return m, m.startXAIOAuthCmd(name, fields)
	}
	m.providerPicker.notice = "Saving " + name + "…"
	return m, m.addProviderCmd(name, m.providerPicker.selectedType.ID, fields)
}

func (m model) applyXAIOAuthStarted(msg xaiOAuthStartedMsg) (tea.Model, tea.Cmd) {
	if !m.providerPicker.open {
		return m, nil
	}
	if msg.err != nil {
		m.providerPicker.loading = false
		m.providerPicker.err = "Failed: " + msg.err.Error()
		m.providerPicker.notice = ""
		m.providerPicker.step = "form"
		return m, nil
	}
	m.providerPicker.loading = true
	m.providerPicker.err = ""
	m.providerPicker.step = "oauth"
	m.providerPicker.notice = fmt.Sprintf(
		"Authorize in your browser\nCode: %s\nURL:  %s\nWaiting for approval…",
		msg.pending.UserCode, msg.pending.VerificationURL,
	)
	return m, m.waitXAIOAuthCmd(msg)
}

func (m model) applyProviderMutated(msg providerMutatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if m.providerPicker.open {
			m.providerPicker.loading = false
			m.providerPicker.err = "Failed: " + msg.err.Error()
			if m.providerPicker.step == "oauth" {
				// Poll failed; return to the form so the error is editable.
				m.providerPicker.step = "form"
				m.providerPicker.notice = ""
			}
			return m, nil
		}
		m.tr.Apply(events.NewError("Provider update failed: " + msg.err.Error()))
		m.refreshViewport()
		return m, nil
	}
	// Success: return to the list and reload.
	verb := "added"
	if msg.action == "remove" {
		verb = "removed"
	}
	m.tr.Apply(events.NewSystem(fmt.Sprintf("Provider %q %s.", msg.name, verb)))
	m.refreshViewport()
	if m.providerPicker.open {
		m.providerPicker.step = "list"
		m.providerPicker.loading = true
		m.providerPicker.err = ""
		m.providerPicker.notice = ""
		m.providerPicker.cursor = 0
		m.providerPicker.selectedType = backend.ProviderTypeInfo{}
		m.providerPicker.fields = nil
		m.providerPicker.values = nil
		m.providerPicker.instanceName = ""
		return m, m.loadProviderInstancesCmd()
	}
	return m, nil
}

func (m model) renderProviderPickerOverlay() string {
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
	pp := m.providerPicker

	switch pp.step {
	case "list":
		body.WriteString(th.Header.Render("Providers") +
			th.Muted.Render("  ↑↓ move  a add  d delete  esc close") + "\n")
		body.WriteString(th.Muted.Render("Configured in ~/.config/astonish/config.yaml") + "\n\n")
		if pp.loading {
			body.WriteString(th.Muted.Render(first(pp.notice, "Loading…")))
		} else if pp.err != "" {
			body.WriteString(th.Error.Render(pp.err))
		} else if len(pp.instances) == 0 {
			body.WriteString(th.Muted.Render("No providers configured. Press 'a' to add one."))
		} else {
			for i, inst := range pp.instances {
				mark, style := "  ", th.Text
				if i == pp.cursor {
					mark, style = "› ", th.Success
				}
				body.WriteString(style.Render(mark+inst.Name) + th.Muted.Render("  ("+inst.Type+")") + "\n")
			}
		}

	case "type":
		body.WriteString(th.Header.Render("Add provider · choose type") +
			th.Muted.Render("  ↑↓ move  enter select  esc back") + "\n\n")
		if pp.err != "" {
			body.WriteString(th.Error.Render(pp.err) + "\n\n")
		}
		for i, t := range pp.types {
			mark, style := "  ", th.Text
			if i == pp.cursor {
				mark, style = "› ", th.Success
			}
			body.WriteString(style.Render(mark+t.DisplayName) + th.Muted.Render("  ("+t.ID+")") + "\n")
		}

	case "form":
		body.WriteString(th.Header.Render("Add "+pp.selectedType.DisplayName) +
			th.Muted.Render("  ↑↓/tab move  enter next/save  esc back") + "\n\n")
		if pp.err != "" {
			body.WriteString(th.Error.Render(pp.err) + "\n\n")
		}
		// Row 0 is the instance name.
		rows := make([]struct {
			label  string
			value  string
			secret bool
		}, 0, len(pp.fields)+1)
		rows = append(rows, struct {
			label  string
			value  string
			secret bool
		}{"Instance Name", pp.values[0], false})
		for i, f := range pp.fields {
			rows = append(rows, struct {
				label  string
				value  string
				secret bool
			}{f.Label, pp.values[i+1], f.Secret})
		}
		for i, r := range rows {
			mark, style := "  ", th.Text
			if i == pp.fieldCursor {
				mark, style = "› ", th.Success
			}
			shown := r.value
			if r.secret {
				shown = strings.Repeat("•", len([]rune(r.value)))
			}
			if i == pp.fieldCursor {
				shown += "▏"
			}
			body.WriteString(style.Render(mark+r.label+": ") + th.Text.Render(shown) + "\n")
		}
		if pp.loading {
			body.WriteString("\n")
			if pp.notice != "" {
				for _, line := range strings.Split(pp.notice, "\n") {
					body.WriteString(th.Success.Render(line) + "\n")
				}
			} else {
				body.WriteString(th.Muted.Render("Saving…"))
			}
		}

	case "oauth":
		body.WriteString(th.Header.Render("Authorize "+first(pp.selectedType.DisplayName, "xAI (OAuth)")) +
			th.Muted.Render("  esc cancel") + "\n\n")
		if pp.err != "" {
			body.WriteString(th.Error.Render(pp.err) + "\n\n")
		}
		if pp.notice != "" {
			for _, line := range strings.Split(pp.notice, "\n") {
				if line == "" {
					continue
				}
				body.WriteString(th.Success.Render(line) + "\n")
			}
		} else if pp.loading {
			body.WriteString(th.Muted.Render("Waiting for approval…"))
		}
	}

	box := th.InputBorderFocus.
		Width(w-2).
		MaxHeight(h).
		Padding(1, 2).
		Render(body.String())
	return m.paintCompletionPopup(box, w-2)
}
