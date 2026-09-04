package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
)

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------

type modelCatalogBackend struct {
	staticBackend
	providers []string
	models    map[string][]string
	pinP      string
	pinM      string
	setCalls  int
}

func (b *modelCatalogBackend) ListProviders(context.Context) ([]string, error) {
	return append([]string(nil), b.providers...), nil
}

func (b *modelCatalogBackend) ListModels(_ context.Context, provider string) ([]string, error) {
	return append([]string(nil), b.models[provider]...), nil
}

func (b *modelCatalogBackend) SetModelPin(_ context.Context, provider, model string) (string, string, error) {
	b.setCalls++
	b.pinP, b.pinM = provider, model
	if provider == "" && model == "" {
		return "cascade-provider", "cascade-model", nil
	}
	return provider, model, nil
}

// providerAdminStub is a fake ProviderAdminBackend for overlay tests.
type providerAdminStub struct {
	staticBackend
	instances []backend.ProviderInstance
	types     []backend.ProviderTypeInfo
	added     []struct {
		name, typeID string
		fields       map[string]string
	}
	removed []string
}

func (b *providerAdminStub) ListProviders(context.Context) ([]string, error) {
	names := make([]string, len(b.instances))
	for i, inst := range b.instances {
		names[i] = inst.Name
	}
	return names, nil
}

func (b *providerAdminStub) ListModels(context.Context, string) ([]string, error) {
	return nil, nil
}

func (b *providerAdminStub) SetModelPin(_ context.Context, provider, model string) (string, string, error) {
	return provider, model, nil
}

func (b *providerAdminStub) ListProviderInstances(context.Context) ([]backend.ProviderInstance, error) {
	return append([]backend.ProviderInstance(nil), b.instances...), nil
}

func (b *providerAdminStub) ProviderTypes() []backend.ProviderTypeInfo {
	return b.types
}

func (b *providerAdminStub) AddProvider(_ context.Context, name, typeID string, fields map[string]string) error {
	b.added = append(b.added, struct {
		name, typeID string
		fields       map[string]string
	}{name, typeID, fields})
	b.instances = append(b.instances, backend.ProviderInstance{Name: name, Type: typeID})
	return nil
}

func (b *providerAdminStub) RemoveProvider(_ context.Context, name string) error {
	b.removed = append(b.removed, name)
	return nil
}

func newProviderStub() *providerAdminStub {
	return &providerAdminStub{
		types: []backend.ProviderTypeInfo{
			{ID: "openai", DisplayName: "OpenAI", Fields: []backend.ProviderField{{Key: "api_key", Label: "API Key", Secret: true}}},
			{ID: "ollama", DisplayName: "Ollama", Fields: []backend.ProviderField{{Key: "base_url", Label: "Base URL", Default: "http://localhost:11434", Optional: true}}},
		},
	}
}

// xaiOAuthStub implements ProviderAdminBackend and XAIOAuthBackend so the
// overlay can exercise the two-phase device-code flow without a network.
type xaiOAuthStub struct {
	providerAdminStub
	started []string
	waited  int
}

func (b *xaiOAuthStub) StartXAIOAuth(_ context.Context, clientID string) (*backend.XAIOAuthPending, error) {
	b.started = append(b.started, clientID)
	return &backend.XAIOAuthPending{
		ClientID:        "cid",
		DeviceCode:      "dev",
		UserCode:        "ABCD-1234",
		VerificationURL: "https://accounts.x.ai/oauth2/device?user_code=ABCD-1234",
		Interval:        5,
	}, nil
}

func (b *xaiOAuthStub) WaitXAIOAuth(_ context.Context, pending backend.XAIOAuthPending) (map[string]string, error) {
	b.waited++
	return map[string]string{
		"client_id":     pending.ClientID,
		"access_token":  "at",
		"refresh_token": "rt",
		"expires_at":    "2026-01-01T00:00:00Z",
	}, nil
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func newModelPickerTestModel(t *testing.T, b backend.Backend) model {
	t.Helper()
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	return m
}

// ---------------------------------------------------------------------------
// Existing model-picker tests (kept)
// ---------------------------------------------------------------------------

func TestModelSlashCommandOpensPicker(t *testing.T) {
	b := &modelCatalogBackend{
		staticBackend: staticBackend{info: backend.Info{Provider: "openai", Model: "gpt-4o"}},
		providers:     []string{"openai", "anthropic"},
		models:        map[string][]string{"openai": {"gpt-4o", "gpt-4.1"}},
	}
	m := newModelPickerTestModel(t, b)
	m.info = b.info

	next, cmd := m.handleSlash("/model")
	m = next.(model)
	if !m.modelPicker.open || !m.modelPicker.loading {
		t.Fatalf("picker open=%v loading=%v", m.modelPicker.open, m.modelPicker.loading)
	}
	if cmd == nil {
		t.Fatal("expected load providers command")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(model)
	if m.modelPicker.loading {
		t.Fatal("expected loading finished")
	}
	if len(m.modelPicker.providers) != 2 {
		t.Fatalf("providers = %v", m.modelPicker.providers)
	}
	// Cascade default + Auto + 2 providers
	if len(m.modelPicker.items) != 4 {
		t.Fatalf("items = %v", m.modelPicker.items)
	}
	if m.modelPicker.items[0] != "(cascade default)" {
		t.Fatalf("first item = %q", m.modelPicker.items[0])
	}
}

func TestModelPickerSelectProviderThenModel(t *testing.T) {
	b := &modelCatalogBackend{
		staticBackend: staticBackend{info: backend.Info{Provider: "openai", Model: "gpt-4o"}},
		providers:     []string{"openai", "anthropic"},
		models: map[string][]string{
			"openai":    {"gpt-4o", "o3"},
			"anthropic": {"claude-sonnet-4"},
		},
	}
	m := newModelPickerTestModel(t, b)
	m.info = b.info
	m.modelPicker = modelPickerState{
		open:            true,
		step:            "provider",
		providers:       b.providers,
		currentProvider: "openai",
		currentModel:    "gpt-4o",
	}
	m.modelPicker.rebuildItems()
	// Move off cascade default onto openai
	for i, item := range m.modelPicker.items {
		if item == "openai" {
			m.modelPicker.cursor = i
			break
		}
	}

	next, cmd := m.selectModelPickerItem()
	m = next.(model)
	if m.modelPicker.step != "model" || !m.modelPicker.loading {
		t.Fatalf("step=%q loading=%v", m.modelPicker.step, m.modelPicker.loading)
	}
	if cmd == nil {
		t.Fatal("expected load models command")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(model)
	if len(m.modelPicker.models) != 2 {
		t.Fatalf("models = %v", m.modelPicker.models)
	}

	// Select o3
	for i, item := range m.modelPicker.items {
		if item == "o3" {
			m.modelPicker.cursor = i
			break
		}
	}
	next, cmd = m.selectModelPickerItem()
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected pin command")
	}
	msg = cmd()
	next, _ = m.Update(msg)
	m = next.(model)

	if m.modelPicker.open {
		t.Fatal("picker should close after pin")
	}
	if b.pinP != "openai" || b.pinM != "o3" {
		t.Fatalf("pin = %s/%s", b.pinP, b.pinM)
	}
	if m.info.Provider != "openai" || m.info.Model != "o3" {
		t.Fatalf("info = %s/%s", m.info.Provider, m.info.Model)
	}
	found := false
	for _, it := range m.tr.Items {
		if it.Kind == "system" && strings.Contains(it.Content, "Model set to") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected system notice, items=%+v", m.tr.Items)
	}
}

func TestModelPickerClearCascadeDefault(t *testing.T) {
	b := &modelCatalogBackend{
		staticBackend: staticBackend{info: backend.Info{Provider: "openai", Model: "gpt-4o"}},
		providers:     []string{"openai"},
	}
	m := newModelPickerTestModel(t, b)
	m.info = b.info
	m.modelPicker = modelPickerState{
		open:      true,
		step:      "provider",
		providers: b.providers,
		cursor:    0, // cascade default
	}
	m.modelPicker.rebuildItems()

	next, cmd := m.selectModelPickerItem()
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected clear pin command")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(model)
	if b.setCalls != 1 || b.pinP != "" || b.pinM != "" {
		t.Fatalf("setCalls=%d pin=%q/%q", b.setCalls, b.pinP, b.pinM)
	}
	if m.info.Provider != "cascade-provider" || m.info.Model != "cascade-model" {
		t.Fatalf("info after clear = %s/%s", m.info.Provider, m.info.Model)
	}
}

func TestModelPickerFilterProviders(t *testing.T) {
	m := newModelPickerTestModel(t, &modelCatalogBackend{
		providers: []string{"openai", "anthropic", "sap"},
	})
	m.modelPicker = modelPickerState{
		open:      true,
		step:      "provider",
		providers: []string{"openai", "anthropic", "sap"},
	}
	m.modelPicker.rebuildItems()
	m.modelPicker.filter = "sap"
	m.modelPicker.rebuildItems()
	if len(m.modelPicker.items) != 1 || m.modelPicker.items[0] != "sap" {
		t.Fatalf("filtered items = %v", m.modelPicker.items)
	}
}

func TestModelPickerOverlayRendersProviders(t *testing.T) {
	m := newModelPickerTestModel(t, &modelCatalogBackend{})
	m.modelPicker = modelPickerState{
		open:            true,
		step:            "provider",
		providers:       []string{"openai"},
		currentProvider: "openai",
		currentModel:    "gpt-4o",
	}
	m.modelPicker.rebuildItems()
	out := stripANSI(m.renderModelPickerOverlay())
	for _, want := range []string{"Model", "openai", "cascade default", "Currently:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("overlay missing %q:\n%s", want, out)
		}
	}
}

func TestModelPickerEscFromModelStepGoesBack(t *testing.T) {
	m := newModelPickerTestModel(t, &modelCatalogBackend{providers: []string{"openai"}})
	m.modelPicker = modelPickerState{
		open:             true,
		step:             "model",
		selectedProvider: "openai",
		providers:        []string{"openai"},
		models:           []string{"gpt-4o"},
	}
	m.modelPicker.rebuildItems()
	next, _ := m.handleModelPickerKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(model)
	if m.modelPicker.step != "provider" || !m.modelPicker.open {
		t.Fatalf("step=%q open=%v", m.modelPicker.step, m.modelPicker.open)
	}
}

// ---------------------------------------------------------------------------
// Adapted provider management tests (using modelPickerState)
// ---------------------------------------------------------------------------

func TestModelPicker_InstancesLoadedWithProviders(t *testing.T) {
	b := newProviderStub()
	b.instances = []backend.ProviderInstance{{Name: "openai", Type: "openai"}}
	m := newModelPickerTestModel(t, b)

	next, cmd := m.openModelPicker()
	m = next.(model)
	if !m.modelPicker.open || !m.modelPicker.loading {
		t.Fatalf("open=%v loading=%v", m.modelPicker.open, m.modelPicker.loading)
	}
	if cmd == nil {
		t.Fatal("expected batch command")
	}

	// Execute all batched commands (providers + instances).
	msgs := executeBatch(cmd)
	for _, msg := range msgs {
		next, _ = m.Update(msg)
		m = next.(model)
	}

	// Providers should be loaded.
	if len(m.modelPicker.providers) != 1 || m.modelPicker.providers[0] != "openai" {
		t.Fatalf("providers = %v", m.modelPicker.providers)
	}
	// Instances should also be loaded.
	if len(m.modelPicker.instances) != 1 || m.modelPicker.instances[0].Name != "openai" {
		t.Fatalf("instances = %v", m.modelPicker.instances)
	}
}

func TestModelPicker_AddProviderFromProviderStep(t *testing.T) {
	b := newProviderStub()
	m := newModelPickerTestModel(t, b)
	m.modelPicker = modelPickerState{
		open:  true,
		step:  "provider",
		types: b.types,
	}
	m.modelPicker.rebuildItems()

	// Press 'a' → add-type step.
	next, _ := m.handleModelPickerKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = next.(model)
	if m.modelPicker.step != "add-type" {
		t.Fatalf("step after 'a' = %q", m.modelPicker.step)
	}

	// Select the first type (openai) with enter.
	next, _ = m.handleModelPickerKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if m.modelPicker.step != "add-form" {
		t.Fatalf("step after type select = %q", m.modelPicker.step)
	}
	// values[0] is the instance name default; there is one field (api_key).
	if len(m.modelPicker.values) != 2 {
		t.Fatalf("expected name + 1 field, got %d values", len(m.modelPicker.values))
	}

	// Type an API key into the second field.
	m.modelPicker.fieldCursor = 1
	for _, r := range "sk-abc" {
		next, _ = m.handleModelPickerKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = next.(model)
	}
	if m.modelPicker.values[1] != "sk-abc" {
		t.Fatalf("api_key value = %q", m.modelPicker.values[1])
	}

	// Enter on the last field submits.
	next, cmd := m.handleModelPickerKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected add command on submit")
	}
	next, _ = m.Update(cmd())
	m = next.(model)

	if len(b.added) != 1 {
		t.Fatalf("expected one AddProvider call, got %d", len(b.added))
	}
	if b.added[0].typeID != "openai" || b.added[0].fields["api_key"] != "sk-abc" {
		t.Fatalf("unexpected add: %+v", b.added[0])
	}
	// After providerMutatedMsg the overlay returns to the provider step.
	if m.modelPicker.step != "provider" {
		t.Fatalf("step after add = %q", m.modelPicker.step)
	}
}

func TestModelPicker_DeleteProviderWithConfirmation(t *testing.T) {
	b := newProviderStub()
	b.instances = []backend.ProviderInstance{{Name: "openai", Type: "openai"}}
	m := newModelPickerTestModel(t, b)
	m.modelPicker = modelPickerState{
		open:      true,
		step:      "provider",
		types:     b.types,
		instances: b.instances,
		providers: []string{"openai"},
	}
	m.modelPicker.rebuildItems()

	// Move cursor to "openai" (skip cascade default).
	for i, item := range m.modelPicker.items {
		if item == "openai" {
			m.modelPicker.cursor = i
			break
		}
	}

	// Press 'd' → confirmDelete=true.
	next, _ := m.handleModelPickerKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = next.(model)
	if !m.modelPicker.confirmDelete {
		t.Fatal("expected confirmDelete=true after 'd'")
	}
	if m.modelPicker.confirmDeleteName != "openai" {
		t.Fatalf("confirmDeleteName = %q", m.modelPicker.confirmDeleteName)
	}

	// Press 'n' → cancelled.
	next, _ = m.handleModelPickerKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = next.(model)
	if m.modelPicker.confirmDelete {
		t.Fatal("expected confirmDelete=false after 'n'")
	}

	// Press 'd' again then 'y' → fires removeProviderCmd.
	next, _ = m.handleModelPickerKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = next.(model)
	if !m.modelPicker.confirmDelete {
		t.Fatal("expected confirmDelete=true for second 'd'")
	}

	next, cmd := m.handleModelPickerKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected remove command after 'y'")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if len(b.removed) != 1 || b.removed[0] != "openai" {
		t.Fatalf("removed = %v", b.removed)
	}
}

func TestModelPicker_AddFormAcceptsPaste(t *testing.T) {
	b := newProviderStub()
	m := newModelPickerTestModel(t, b)
	m.modelPicker = modelPickerState{
		open:         true,
		step:         "add-form",
		selectedType: b.types[0], // openai, api_key secret
		fields:       b.types[0].Fields,
		values:       []string{"openai", ""},
		fieldCursor:  1,
	}
	// A bracketed paste arrives as a KeyPressMsg with the text content.
	next, _ := m.handleModelPickerKey(tea.KeyPressMsg{Code: 's', Text: "sk-pasted-key-123"})
	m = next.(model)
	if got := m.modelPicker.values[1]; got != "sk-pasted-key-123" {
		t.Fatalf("expected pasted key, got %q", got)
	}
}

func TestModelPicker_AddFormMasksSecret(t *testing.T) {
	b := newProviderStub()
	m := newModelPickerTestModel(t, b)
	m.modelPicker = modelPickerState{
		open:         true,
		step:         "add-form",
		selectedType: b.types[0], // openai, api_key secret
		fields:       b.types[0].Fields,
		values:       []string{"openai", "sk-supersecret"},
		fieldCursor:  1,
	}
	out := stripANSI(m.renderModelPickerOverlay())
	if strings.Contains(out, "sk-supersecret") {
		t.Fatalf("secret value must be masked in overlay:\n%s", out)
	}
	if !strings.Contains(out, "•") {
		t.Fatalf("expected masked bullets in overlay:\n%s", out)
	}
}

func TestModelPicker_XAIOAuthShowsUserCode(t *testing.T) {
	b := &xaiOAuthStub{}
	b.types = []backend.ProviderTypeInfo{
		{ID: "xai_oauth", DisplayName: "xAI (OAuth)", Fields: []backend.ProviderField{
			{Key: "client_id", Label: "OAuth Client ID", Optional: true},
		}},
	}
	m := newModelPickerTestModel(t, b)
	m.modelPicker = modelPickerState{
		open:         true,
		step:         "add-form",
		selectedType: b.types[0],
		fields:       b.types[0].Fields,
		values:       []string{"xai_oauth", ""},
		fieldCursor:  0,
	}

	next, cmd := m.submitProviderForm()
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected start-oauth command")
	}
	if !strings.Contains(m.modelPicker.notice, "Requesting xAI authorization") {
		t.Fatalf("notice after submit = %q", m.modelPicker.notice)
	}

	next, waitCmd := m.Update(cmd())
	m = next.(model)
	if waitCmd == nil {
		t.Fatal("expected wait-oauth command after start")
	}
	if m.modelPicker.step != "oauth" {
		t.Fatalf("step after start = %q, want oauth", m.modelPicker.step)
	}
	if !strings.Contains(m.modelPicker.notice, "ABCD-1234") {
		t.Fatalf("notice missing user code:\n%s", m.modelPicker.notice)
	}
	out := stripANSI(m.renderModelPickerOverlay())
	if !strings.Contains(out, "ABCD-1234") {
		t.Fatalf("overlay must show user code:\n%s", out)
	}
	if !strings.Contains(out, "Authorize in your browser") {
		t.Fatalf("overlay must instruct the user to authorize:\n%s", out)
	}
	if !strings.Contains(out, "Authorize xAI (OAuth)") {
		t.Fatalf("overlay must be the dedicated oauth wait step:\n%s", out)
	}
	if strings.Contains(out, "↑↓ move  a add") {
		t.Fatalf("oauth wait must not render the provider list:\n%s", out)
	}

	next, _ = m.Update(waitCmd())
	m = next.(model)
	if b.waited != 1 {
		t.Fatalf("WaitXAIOAuth calls = %d, want 1", b.waited)
	}
	if len(b.added) != 1 {
		t.Fatalf("expected one AddProvider call, got %d", len(b.added))
	}
	if b.added[0].fields["access_token"] != "at" {
		t.Fatalf("unexpected add fields: %+v", b.added[0].fields)
	}
	if m.modelPicker.step != "provider" {
		t.Fatalf("step after oauth add = %q", m.modelPicker.step)
	}
}

// ---------------------------------------------------------------------------
// New tests
// ---------------------------------------------------------------------------

// TestModelPicker_AddDeleteGatedOnCapability verifies that 'a' and 'd' are
// no-ops when the backend does not implement ProviderAdminBackend.
func TestModelPicker_AddDeleteGatedOnCapability(t *testing.T) {
	b := &modelCatalogBackend{
		providers: []string{"openai"},
	}
	m := newModelPickerTestModel(t, b)
	m.modelPicker = modelPickerState{
		open:      true,
		step:      "provider",
		providers: b.providers,
	}
	m.modelPicker.rebuildItems()

	// 'a' should do nothing on a plain backend.
	next, cmd := m.handleModelPickerKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = next.(model)
	if m.modelPicker.step != "provider" {
		t.Fatalf("step should stay 'provider' without capability, got %q", m.modelPicker.step)
	}
	if cmd != nil {
		t.Fatal("expected no command from 'a' without capability")
	}

	// Move to "openai" and press 'd' – should do nothing.
	for i, item := range m.modelPicker.items {
		if item == "openai" {
			m.modelPicker.cursor = i
			break
		}
	}
	next, cmd = m.handleModelPickerKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = next.(model)
	if m.modelPicker.confirmDelete {
		t.Fatal("confirmDelete should not be set without capability")
	}
	if cmd != nil {
		t.Fatal("expected no command from 'd' without capability")
	}
}

// TestModelPicker_DeleteConfirmationEsc verifies that pressing esc during
// delete confirmation cancels it.
func TestModelPicker_DeleteConfirmationEsc(t *testing.T) {
	b := newProviderStub()
	b.instances = []backend.ProviderInstance{{Name: "openai", Type: "openai"}}
	m := newModelPickerTestModel(t, b)
	m.modelPicker = modelPickerState{
		open:      true,
		step:      "provider",
		types:     b.types,
		instances: b.instances,
		providers: []string{"openai"},
	}
	m.modelPicker.rebuildItems()
	for i, item := range m.modelPicker.items {
		if item == "openai" {
			m.modelPicker.cursor = i
			break
		}
	}

	// Press 'd' → confirmDelete.
	next, _ := m.handleModelPickerKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = next.(model)
	if !m.modelPicker.confirmDelete {
		t.Fatal("expected confirmDelete")
	}

	// Press esc → cancelled.
	next, _ = m.handleModelPickerKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(model)
	if m.modelPicker.confirmDelete {
		t.Fatal("expected confirmDelete=false after esc")
	}
	if !m.modelPicker.open {
		t.Fatal("picker should still be open after esc in confirm mode")
	}
}

// TestModelPicker_DeleteCascadeDefaultIgnored verifies that pressing 'd' on
// "(cascade default)" does nothing (you can't delete it).
func TestModelPicker_DeleteCascadeDefaultIgnored(t *testing.T) {
	b := newProviderStub()
	b.instances = []backend.ProviderInstance{{Name: "openai", Type: "openai"}}
	m := newModelPickerTestModel(t, b)
	m.modelPicker = modelPickerState{
		open:      true,
		step:      "provider",
		types:     b.types,
		instances: b.instances,
		providers: []string{"openai"},
	}
	m.modelPicker.rebuildItems()
	// cursor 0 is "(cascade default)"
	m.modelPicker.cursor = 0

	next, _ := m.handleModelPickerKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = next.(model)
	if m.modelPicker.confirmDelete {
		t.Fatal("confirmDelete must not be set for cascade default")
	}
}

// TestModelPicker_RenderShowsAddDeleteHints checks that the provider step
// shows "a add" and "d delete" hints only when providerAdmin is available.
func TestModelPicker_RenderShowsAddDeleteHints(t *testing.T) {
	// With providerAdmin.
	b := newProviderStub()
	b.instances = []backend.ProviderInstance{{Name: "openai", Type: "openai"}}
	m := newModelPickerTestModel(t, b)
	m.modelPicker = modelPickerState{
		open:      true,
		step:      "provider",
		types:     b.types,
		instances: b.instances,
		providers: []string{"openai"},
	}
	m.modelPicker.rebuildItems()
	out := stripANSI(m.renderModelPickerOverlay())
	if !strings.Contains(out, "a add") || !strings.Contains(out, "d delete") {
		t.Fatalf("expected 'a add' and 'd delete' hints with providerAdmin:\n%s", out)
	}

	// Without providerAdmin.
	bPlain := &modelCatalogBackend{providers: []string{"openai"}}
	m2 := newModelPickerTestModel(t, bPlain)
	m2.modelPicker = modelPickerState{
		open:      true,
		step:      "provider",
		providers: []string{"openai"},
	}
	m2.modelPicker.rebuildItems()
	out2 := stripANSI(m2.renderModelPickerOverlay())
	if strings.Contains(out2, "a add") || strings.Contains(out2, "d delete") {
		t.Fatalf("should NOT show 'a add' / 'd delete' without providerAdmin:\n%s", out2)
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// executeBatch runs a tea.Cmd and, if it's a tea.BatchMsg, collects all
// sub-command results. This handles the tea.Batch() pattern used in openModelPicker.
func executeBatch(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, c := range batch {
			if c != nil {
				msgs = append(msgs, c())
			}
		}
		return msgs
	}
	return []tea.Msg{msg}
}
