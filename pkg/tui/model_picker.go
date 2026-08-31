package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// modelPickerState is the /model overlay. It combines provider → model selection
// with inline provider management (add/delete) when ProviderAdminBackend is available.
//
// step values:
//
//	"provider"  – provider list (entry point); also supports add/delete in code mode
//	"model"     – model list for the selected provider
//	"add-type"  – choose provider type to add
//	"add-form"  – fill provider configuration fields
//	"oauth"     – xAI OAuth device-code wait screen
type modelPickerState struct {
	open             bool
	loading          bool
	err              string
	notice           string
	step             string // "provider" | "model" | "add-type" | "add-form" | "oauth"
	providers        []string
	models           []string
	items            []string // filtered list currently shown
	filter           string
	cursor           int
	selectedProvider string
	currentProvider  string
	currentModel     string

	// Provider management (code mode only, when ProviderAdminBackend is available).
	instances    []backend.ProviderInstance // loaded instances for provider list
	types        []backend.ProviderTypeInfo // available provider types for add flow
	selectedType backend.ProviderTypeInfo   // type chosen in add-type step
	instanceName string
	fields       []backend.ProviderField
	values       []string // one per field; index 0 is instance name
	fieldCursor  int      // 0 = instance name, then 1..len(fields)

	// Delete confirmation (inline on provider step).
	confirmDelete     bool   // true when showing "Delete X? y/n" prompt
	confirmDeleteName string // name of the provider to delete
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

// --- Provider management message types (moved from provider_picker.go) ---

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

// --- Backend capability checks (moved from provider_picker.go) ---

// providerAdmin returns the ProviderAdminBackend capability of the active
// backend, or nil when the backend does not support local provider management
// (e.g. platform chat).
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

// --- Open ---

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
	if pa := m.providerAdmin(); pa != nil {
		m.modelPicker.types = pa.ProviderTypes()
		sort.Slice(m.modelPicker.types, func(i, j int) bool {
			return strings.ToLower(m.modelPicker.types[i].DisplayName) < strings.ToLower(m.modelPicker.types[j].DisplayName)
		})
	}
	m.slash = slashCompletion{}
	m.files = fileCompletion{}
	m.ta.Reset()
	cmds := []tea.Cmd{m.loadModelProvidersCmd()}
	if m.providerAdmin() != nil {
		cmds = append(cmds, m.loadManageInstancesCmd())
	}
	return m, tea.Batch(cmds...)
}

// --- Commands ---

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

func (m model) loadManageInstancesCmd() tea.Cmd {
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

// --- Apply messages ---

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

func (m model) applyProviderInstancesLoaded(msg providerInstancesLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.modelPicker.open {
		return m, nil
	}
	if msg.err != nil {
		// Non-fatal: instances are supplementary info in the unified overlay.
		m.modelPicker.instances = nil
		return m, nil
	}
	m.modelPicker.instances = append([]backend.ProviderInstance(nil), msg.instances...)
	return m, nil
}

func (m model) applyXAIOAuthStarted(msg xaiOAuthStartedMsg) (tea.Model, tea.Cmd) {
	if !m.modelPicker.open {
		return m, nil
	}
	if msg.err != nil {
		m.modelPicker.loading = false
		m.modelPicker.err = "Failed: " + msg.err.Error()
		m.modelPicker.notice = ""
		m.modelPicker.step = "add-form"
		return m, nil
	}
	m.modelPicker.loading = true
	m.modelPicker.err = ""
	m.modelPicker.step = "oauth"
	m.modelPicker.notice = fmt.Sprintf(
		"Authorize in your browser\nCode: %s\nURL:  %s\nWaiting for approval…",
		msg.pending.UserCode, msg.pending.VerificationURL,
	)
	return m, m.waitXAIOAuthCmd(msg)
}

func (m model) applyProviderMutated(msg providerMutatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if m.modelPicker.open {
			m.modelPicker.loading = false
			m.modelPicker.err = "Failed: " + msg.err.Error()
			if m.modelPicker.step == "oauth" {
				// Poll failed; return to the form so the error is editable.
				m.modelPicker.step = "add-form"
				m.modelPicker.notice = ""
			}
			return m, nil
		}
		m.tr.Apply(events.NewError("Provider update failed: " + msg.err.Error()))
		m.refreshViewport()
		return m, nil
	}
	// Success: system notice and refresh.
	verb := "added"
	if msg.action == "remove" {
		verb = "removed"
	}
	m.tr.Apply(events.NewSystem(fmt.Sprintf("Provider %q %s.", msg.name, verb)))
	m.refreshViewport()
	if m.modelPicker.open {
		m.modelPicker.step = "provider"
		m.modelPicker.loading = true
		m.modelPicker.err = ""
		m.modelPicker.notice = ""
		m.modelPicker.filter = ""
		m.modelPicker.cursor = 0
		m.modelPicker.selectedType = backend.ProviderTypeInfo{}
		m.modelPicker.fields = nil
		m.modelPicker.values = nil
		m.modelPicker.instanceName = ""
		return m, tea.Batch(m.loadManageInstancesCmd(), m.loadModelProvidersCmd())
	}
	return m, nil
}

// --- Helpers ---

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

// instanceTypeForProvider looks up the provider type from loaded instances.
func (s *modelPickerState) instanceTypeForProvider(name string) string {
	for _, inst := range s.instances {
		if inst.Name == name {
			return inst.Type
		}
	}
	return ""
}

// isKnownInstance returns true if name appears in the loaded instances.
func (s *modelPickerState) isKnownInstance(name string) bool {
	for _, inst := range s.instances {
		if inst.Name == name {
			return true
		}
	}
	return false
}

func (m model) submitProviderForm() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(m.modelPicker.values[0])
	if name == "" {
		m.modelPicker.err = "Instance name is required."
		return m, nil
	}
	fields := make(map[string]string, len(m.modelPicker.fields))
	for i, f := range m.modelPicker.fields {
		fields[f.Key] = strings.TrimSpace(m.modelPicker.values[i+1])
	}
	m.modelPicker.loading = true
	m.modelPicker.err = ""
	if m.modelPicker.selectedType.ID == "xai_oauth" && m.xaiOAuth() != nil {
		m.modelPicker.notice = "Requesting xAI authorization…"
		return m, m.startXAIOAuthCmd(name, fields)
	}
	m.modelPicker.notice = "Saving " + name + "…"
	return m, m.addProviderCmd(name, m.modelPicker.selectedType.ID, fields)
}

func (m model) cancelProviderOAuth() (tea.Model, tea.Cmd) {
	// Return to the form so the user can retry without closing the overlay.
	// The in-flight WaitXAIOAuth cmd may still complete; applyProviderMutated
	// ignores it if the overlay has left the oauth step (or was closed).
	m.modelPicker.step = "add-form"
	m.modelPicker.err = ""
	m.modelPicker.notice = ""
	m.modelPicker.loading = false
	return m, nil
}

// --- Key handling ---

func (m model) handleModelPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Loading state: only allow esc/ctrl+c to close/cancel.
	if m.modelPicker.loading {
		if key == "esc" || key == "ctrl+c" {
			if m.modelPicker.step == "oauth" {
				return m.cancelProviderOAuth()
			}
			m.modelPicker = modelPickerState{}
			return m, nil
		}
		return m, nil
	}

	// Route by step.
	switch m.modelPicker.step {
	case "provider":
		return m.handleProviderStepKey(msg, key)
	case "model":
		return m.handleModelStepKey(msg, key)
	case "add-type":
		return m.handleAddTypeKey(msg, key)
	case "add-form":
		return m.handleAddFormKey(msg, key)
	case "oauth":
		return m.handleOAuthKey(key)
	}
	return m, nil
}

func (m model) handleProviderStepKey(msg tea.KeyMsg, key string) (tea.Model, tea.Cmd) {
	// Delete confirmation mode: only y/n/esc.
	if m.modelPicker.confirmDelete {
		switch key {
		case "y":
			name := m.modelPicker.confirmDeleteName
			m.modelPicker.confirmDelete = false
			m.modelPicker.confirmDeleteName = ""
			m.modelPicker.loading = true
			m.modelPicker.notice = "Removing " + name + "…"
			return m, m.removeProviderCmd(name)
		case "n", "esc", "ctrl+c":
			m.modelPicker.confirmDelete = false
			m.modelPicker.confirmDeleteName = ""
			return m, nil
		}
		return m, nil
	}

	switch key {
	case "esc", "ctrl+c":
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
	case "a":
		if m.providerAdmin() != nil {
			m.modelPicker.step = "add-type"
			m.modelPicker.cursor = 0
			m.modelPicker.filter = ""
			m.modelPicker.err = ""
			return m, nil
		}
	case "d", "x":
		if m.providerAdmin() != nil && len(m.modelPicker.items) > 0 {
			item := m.modelPicker.items[m.modelPicker.cursor]
			if item != "(cascade default)" && m.modelPicker.isKnownInstance(item) {
				m.modelPicker.confirmDelete = true
				m.modelPicker.confirmDeleteName = item
				return m, nil
			}
		}
	}

	// Accumulate filter from printable runes.
	if msg.Key().Text != "" {
		m.modelPicker.filter += msg.Key().Text
		m.modelPicker.rebuildItems()
		return m, nil
	}
	return m, nil
}

func (m model) handleModelStepKey(msg tea.KeyMsg, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+c":
		m.modelPicker.step = "provider"
		m.modelPicker.filter = ""
		m.modelPicker.selectedProvider = ""
		m.modelPicker.err = ""
		m.modelPicker.rebuildItems()
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

func (m model) handleAddTypeKey(_ tea.KeyMsg, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+c":
		m.modelPicker.step = "provider"
		m.modelPicker.err = ""
		m.modelPicker.cursor = 0
		m.modelPicker.rebuildItems()
		return m, nil
	case "up", "k":
		if m.modelPicker.cursor > 0 {
			m.modelPicker.cursor--
		}
	case "down", "j":
		if m.modelPicker.cursor < len(m.modelPicker.types)-1 {
			m.modelPicker.cursor++
		}
	case "enter", " ":
		if len(m.modelPicker.types) == 0 {
			return m, nil
		}
		t := m.modelPicker.types[m.modelPicker.cursor]
		m.modelPicker.selectedType = t
		m.modelPicker.fields = append([]backend.ProviderField(nil), t.Fields...)
		// values[0] is the instance name; the rest map to fields.
		m.modelPicker.values = make([]string, len(t.Fields)+1)
		m.modelPicker.values[0] = t.ID // sensible default instance name
		for i, f := range t.Fields {
			m.modelPicker.values[i+1] = f.Default
		}
		m.modelPicker.instanceName = t.ID
		m.modelPicker.fieldCursor = 0
		m.modelPicker.step = "add-form"
		m.modelPicker.err = ""
	}
	return m, nil
}

func (m model) handleAddFormKey(msg tea.KeyMsg, key string) (tea.Model, tea.Cmd) {
	total := len(m.modelPicker.values) // name + fields
	switch key {
	case "esc", "ctrl+c":
		m.modelPicker.step = "add-type"
		m.modelPicker.err = ""
		return m, nil
	case "ctrl+v", "super+v", "cmd+v":
		// Explicit clipboard paste into the focused field.
		if text, err := clipboard.ReadAll(); err == nil && text != "" {
			clean := strings.TrimSpace(strings.NewReplacer("\r", "", "\n", " ", "\t", " ").Replace(text))
			m.modelPicker.values[m.modelPicker.fieldCursor] += clean
		}
		return m, nil
	case "up":
		if m.modelPicker.fieldCursor > 0 {
			m.modelPicker.fieldCursor--
		}
		return m, nil
	case "down", "tab":
		if m.modelPicker.fieldCursor < total-1 {
			m.modelPicker.fieldCursor++
		}
		return m, nil
	case "enter":
		// Enter on the last field submits; otherwise advance.
		if m.modelPicker.fieldCursor < total-1 {
			m.modelPicker.fieldCursor++
			return m, nil
		}
		return m.submitProviderForm()
	case "backspace", "ctrl+h":
		i := m.modelPicker.fieldCursor
		if v := m.modelPicker.values[i]; v != "" {
			r := []rune(v)
			m.modelPicker.values[i] = string(r[:len(r)-1])
		}
		return m, nil
	}
	// Accept typed runes and pasted text.
	if msg.Key().Text != "" {
		text := msg.Key().Text
		m.modelPicker.values[m.modelPicker.fieldCursor] += text
	}
	return m, nil
}

func (m model) handleOAuthKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+c":
		return m.cancelProviderOAuth()
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

// --- Rendering ---

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

	switch m.modelPicker.step {
	case "provider":
		m.renderProviderStep(&body, th, h)
	case "model":
		m.renderModelStep(&body, th, h)
	case "add-type":
		m.renderAddTypeStep(&body, th)
	case "add-form":
		m.renderAddFormStep(&body, th)
	case "oauth":
		m.renderOAuthStep(&body, th)
	}

	box := th.InputBorderFocus.
		Width(w-2).
		MaxHeight(h).
		Padding(1, 2).
		Render(body.String())
	return m.paintCompletionPopup(box, w-2)
}

func (m model) renderProviderStep(body *strings.Builder, th Theme, h int) {
	title := "Model"
	help := "  ↑↓ move  enter select  type filter  esc close"
	if m.providerAdmin() != nil {
		help = "  ↑↓ move  enter select  filter  a add  d delete  esc close"
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
		} else {
			if m.providerAdmin() != nil {
				body.WriteString(th.Muted.Render("No providers configured. Press 'a' to add one."))
			} else {
				body.WriteString(th.Muted.Render("No providers configured. Add one in Studio Settings."))
			}
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
			// Mark current selection and annotate type.
			suffix := ""
			if item == m.modelPicker.currentProvider {
				suffix = th.Muted.Render("  (current)")
			}
			if t := m.modelPicker.instanceTypeForProvider(item); t != "" && suffix == "" {
				suffix = th.Muted.Render("  (" + t + ")")
			}
			body.WriteString(style.Render(mark+item) + suffix + "\n")
		}
		if end < len(m.modelPicker.items) {
			body.WriteString(th.Muted.Render(fmt.Sprintf("  … %d more", len(m.modelPicker.items)-end)))
		}
	}

	// Delete confirmation prompt.
	if m.modelPicker.confirmDelete {
		body.WriteString("\n" + th.Error.Render(
			fmt.Sprintf("Delete %q? y confirm  n cancel", m.modelPicker.confirmDeleteName)))
	}
}

func (m model) renderModelStep(body *strings.Builder, th Theme, h int) {
	title := "Model · " + m.modelPicker.selectedProvider
	help := "  ↑↓ move  enter pin  type filter  esc back"
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
			suffix := ""
			if item == m.modelPicker.currentModel &&
				m.modelPicker.selectedProvider == m.modelPicker.currentProvider {
				suffix = th.Muted.Render("  (current)")
			}
			body.WriteString(style.Render(mark+item) + suffix + "\n")
		}
		if end < len(m.modelPicker.items) {
			body.WriteString(th.Muted.Render(fmt.Sprintf("  … %d more", len(m.modelPicker.items)-end)))
		}
	}
}

func (m model) renderAddTypeStep(body *strings.Builder, th Theme) {
	body.WriteString(th.Header.Render("Add provider · choose type") +
		th.Muted.Render("  ↑↓ move  enter select  esc back") + "\n\n")
	if m.modelPicker.err != "" {
		body.WriteString(th.Error.Render(m.modelPicker.err) + "\n\n")
	}
	for i, t := range m.modelPicker.types {
		mark, style := "  ", th.Text
		if i == m.modelPicker.cursor {
			mark, style = "› ", th.Success
		}
		body.WriteString(style.Render(mark+t.DisplayName) + th.Muted.Render("  ("+t.ID+")") + "\n")
	}
}

func (m model) renderAddFormStep(body *strings.Builder, th Theme) {
	pp := m.modelPicker
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
}

func (m model) renderOAuthStep(body *strings.Builder, th Theme) {
	pp := m.modelPicker
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
