package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestCodeThemePickerCatalogNavigationDoesNotSave(t *testing.T) {
	m, b := themeTestModel(t)
	n, cmd := m.openThemePicker()
	m = themeReply(n.(model), cmd)
	catalog := BuiltinCodeThemes()
	if len(catalog) != 10 {
		t.Fatalf("want ten palettes, got %d", len(catalog))
	}
	original := m.theme.CodeThemeID
	for i := 0; i < len(catalog)+2; i++ {
		m, cmd = themeKey(m, tea.KeyDown)
		if cmd != nil || b.saves != 0 || m.theme.CodeThemeID != original {
			t.Fatal("cursor navigation applied or saved a palette")
		}
	}
	if m.themePicker.cursor != len(catalog)-1 {
		t.Fatalf("cursor = %d, want last palette", m.themePicker.cursor)
	}
	if view := ansi.Strip(m.renderThemePicker()); !strings.Contains(view, "› "+catalog[len(catalog)-1].Label) {
		t.Fatalf("last palette is not selected in view:\n%s", view)
	}
	for i := 0; i < len(catalog)+2; i++ {
		m, cmd = themeKey(m, tea.KeyUp)
		if cmd != nil || b.saves != 0 || m.theme.CodeThemeID != original {
			t.Fatal("up navigation applied or saved a palette")
		}
	}
	if m.themePicker.cursor != 0 {
		t.Fatal("up navigation escaped catalog bounds")
	}
}

func TestCodeThemePickerCatalogFitsGeometry(t *testing.T) {
	for _, size := range [][2]int{{100, 35}, {72, 10}, {40, 8}, {24, 6}, {12, 5}, {8, 4}, {4, 3}, {1, 1}} {
		for _, stage := range []ThemePickerStage{ThemePickerReady, ThemePickerLoading, ThemePickerSaving, ThemePickerLoadError, ThemePickerSaveError} {
			t.Run(fmt.Sprintf("%dx%d/stage%d", size[0], size[1], stage), func(t *testing.T) {
				m, _ := themeTestModel(t)
				catalog := BuiltinCodeThemes()
				m.width, m.height = size[0], size[1]
				m.themePicker = themePickerState{open: true, cursor: len(catalog) - 1, stage: stage}
				if stage == ThemePickerLoadError || stage == ThemePickerSaveError {
					m.themePicker.err = strings.Repeat("Disk preference could not be accessed. ", 20)
				}
				view := m.renderThemePicker()
				if lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height {
					t.Fatalf("picker %dx%d exceeds terminal %dx%d:\n%s", lipgloss.Width(view), lipgloss.Height(view), m.width, m.height, ansi.Strip(view))
				}
				plain := ansi.Strip(view)
				if m.width >= 4 && !strings.Contains(plain, "› ") {
					t.Fatalf("selected row missing:\n%s", plain)
				}
				if m.width >= 24 && stage == ThemePickerReady && !strings.Contains(plain, "Enter") {
					t.Fatalf("selection help missing:\n%s", plain)
				}
				if m.width >= 72 && !strings.Contains(plain, catalog[len(catalog)-1].Label) {
					t.Fatalf("last palette label missing:\n%s", plain)
				}
				if len(catalog) > m.height && m.height <= 10 && m.width >= 40 && strings.Contains(plain, catalog[0].Label) {
					t.Fatalf("catalog did not scroll to selected last row:\n%s", plain)
				}
			})
		}
	}
}
