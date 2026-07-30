package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/SAP/astonish/pkg/tui/backend"
)

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

func newModelPickerTestModel(t *testing.T, b backend.Backend) model {
	t.Helper()
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	return m
}

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
	// Cascade default + 2 providers
	if len(m.modelPicker.items) != 3 {
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
	next, _ := m.handleModelPickerKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.modelPicker.step != "provider" || !m.modelPicker.open {
		t.Fatalf("step=%q open=%v", m.modelPicker.step, m.modelPicker.open)
	}
}
