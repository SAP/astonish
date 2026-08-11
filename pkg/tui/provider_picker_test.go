package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/SAP/astonish/pkg/tui/backend"
)

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

func newPickerModel(t *testing.T, b backend.Backend) model {
	t.Helper()
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	return m
}

// staticBackend (defined in view_header_test.go) does NOT implement
// ProviderAdminBackend, so /provider must degrade gracefully.
func TestProviderCommand_UnavailableOnPlainBackend(t *testing.T) {
	m := newPickerModel(t, staticBackend{})
	if m.providerAdmin() != nil {
		t.Fatal("plain backend should not expose provider admin")
	}
	next, _ := m.handleSlash("/provider")
	m = next.(model)
	if m.providerPicker.open {
		t.Fatal("provider picker must not open without capability")
	}
}

func TestProviderPicker_OpensAndLists(t *testing.T) {
	b := newProviderStub()
	b.instances = []backend.ProviderInstance{{Name: "openai", Type: "openai"}}
	m := newPickerModel(t, b)

	next, cmd := m.handleSlash("/provider")
	m = next.(model)
	if !m.providerPicker.open || !m.providerPicker.loading {
		t.Fatalf("open=%v loading=%v", m.providerPicker.open, m.providerPicker.loading)
	}
	if cmd == nil {
		t.Fatal("expected load command")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if m.providerPicker.loading {
		t.Fatal("expected loading to finish")
	}
	if len(m.providerPicker.instances) != 1 || m.providerPicker.instances[0].Name != "openai" {
		t.Fatalf("instances = %v", m.providerPicker.instances)
	}
}

func TestProviderPicker_AddFlow(t *testing.T) {
	b := newProviderStub()
	m := newPickerModel(t, b)
	m.providerPicker = providerPickerState{open: true, step: "list", types: b.types}

	// Press 'a' → type step.
	next, _ := m.handleProviderPickerKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = next.(model)
	if m.providerPicker.step != "type" {
		t.Fatalf("step after 'a' = %q", m.providerPicker.step)
	}

	// Select the first type (openai) with enter.
	next, _ = m.handleProviderPickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.providerPicker.step != "form" {
		t.Fatalf("step after type select = %q", m.providerPicker.step)
	}
	// values[0] is the instance name default; there is one field (api_key).
	if len(m.providerPicker.values) != 2 {
		t.Fatalf("expected name + 1 field, got %d values", len(m.providerPicker.values))
	}

	// Type an API key into the second field.
	m.providerPicker.fieldCursor = 1
	for _, r := range "sk-abc" {
		next, _ = m.handleProviderPickerKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(model)
	}
	if m.providerPicker.values[1] != "sk-abc" {
		t.Fatalf("api_key value = %q", m.providerPicker.values[1])
	}

	// Enter on the last field submits.
	next, cmd := m.handleProviderPickerKey(tea.KeyMsg{Type: tea.KeyEnter})
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
	// After success the overlay returns to the list step.
	if m.providerPicker.step != "list" {
		t.Fatalf("step after add = %q", m.providerPicker.step)
	}
}

func TestProviderPicker_DeleteFromList(t *testing.T) {
	b := newProviderStub()
	b.instances = []backend.ProviderInstance{{Name: "openai", Type: "openai"}}
	m := newPickerModel(t, b)
	m.providerPicker = providerPickerState{
		open:      true,
		step:      "list",
		types:     b.types,
		instances: b.instances,
	}
	next, cmd := m.handleProviderPickerKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected remove command")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if len(b.removed) != 1 || b.removed[0] != "openai" {
		t.Fatalf("removed = %v", b.removed)
	}
}

func TestProviderPicker_FormAcceptsPaste(t *testing.T) {
	b := newProviderStub()
	m := newPickerModel(t, b)
	m.providerPicker = providerPickerState{
		open:         true,
		step:         "form",
		selectedType: b.types[0], // openai, api_key secret
		fields:       b.types[0].Fields,
		values:       []string{"openai", ""},
		fieldCursor:  1,
	}
	// A bracketed paste arrives as a multi-rune KeyRunes with Paste=true.
	next, _ := m.handleProviderPickerKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("sk-pasted-key-123\n"),
		Paste: true,
	})
	m = next.(model)
	if got := m.providerPicker.values[1]; got != "sk-pasted-key-123" {
		t.Fatalf("expected pasted key (newline trimmed), got %q", got)
	}
}

func TestProviderPicker_FormMasksSecret(t *testing.T) {
	b := newProviderStub()
	m := newPickerModel(t, b)
	m.providerPicker = providerPickerState{
		open:         true,
		step:         "form",
		selectedType: b.types[0], // openai, api_key secret
		fields:       b.types[0].Fields,
		values:       []string{"openai", "sk-supersecret"},
		fieldCursor:  1,
	}
	out := stripANSI(m.renderProviderPickerOverlay())
	if strings.Contains(out, "sk-supersecret") {
		t.Fatalf("secret value must be masked in overlay:\n%s", out)
	}
	if !strings.Contains(out, "•") {
		t.Fatalf("expected masked bullets in overlay:\n%s", out)
	}
}

func TestProviderSlashCompletion_GatedByCapability(t *testing.T) {
	// With capability: /provider appears in completion.
	withCap := filterSlashCommands("prov", providerSlashCommand)
	if len(withCap) != 1 || withCap[0].Name != "provider" {
		t.Fatalf("expected provider match with capability, got %v", withCap)
	}
	// Without the extra command it must not appear.
	without := filterSlashCommands("prov")
	if len(without) != 0 {
		t.Fatalf("expected no provider match without capability, got %v", without)
	}
}
