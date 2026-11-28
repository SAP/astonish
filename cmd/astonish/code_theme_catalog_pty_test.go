package astonish

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
)

// These are terminal acceptance expectations, intentionally independent of the
// production catalog: changing a palette must also update its visible contract.
var codeThemeCatalogPTYCases = []struct {
	id, label, canvas, card, muted, accent string
	light                                  bool
}{
	{"blue-gray", "Blue-gray", "#101922", "#18232e", "#9cafbf", "#ff8700", false},
	{"dark-orange", "Dark orange", "#111416", "#1a1e21", "#8a8a8a", "#ff8700", false},
	{"tokyo-night", "Tokyo Night", "#1a1b26", "#24283b", "#9aa5ce", "#7aa2f7", false},
	{"catppuccin-mocha", "Catppuccin Mocha", "#1e1e2e", "#313244", "#a6adc8", "#cba6f7", false},
	{"nord", "Nord", "#2e3440", "#3b4252", "#d8dee9", "#88c0d0", false},
	{"dracula", "Dracula-inspired", "#282a36", "#343746", "#a0aacb", "#bd93f9", false},
	{"gruvbox", "Gruvbox", "#282828", "#3c3836", "#bdae93", "#fe8019", false},
	{"rose-pine", "Rosé Pine", "#191724", "#1f1d2e", "#908caa", "#ebbcba", false},
	{"solarized-light", "Solarized Light-inspired", "#fdf6e3", "#eee8d5", "#50656c", "#006b9e", true},
	{"github-light", "GitHub Light-inspired", "#ffffff", "#f6f8fa", "#59636e", "#0969da", true},
}

// Exercise the actual Code launch and disk preferences through a PTY, not View.
// No provider request is needed to select, save, or restore a local UI palette.
func TestCodeThemeCatalogPTYAcceptance(t *testing.T) {
	for index, tc := range codeThemeCatalogPTYCases {
		for _, plain := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/plain=%v", tc.id, plain), func(t *testing.T) {
				f := newSidebarPTYFixture(t)
				configDir, err := config.GetConfigDir()
				if err != nil {
					t.Fatal(err)
				}
				configPath := filepath.Join(configDir, "config.yaml")
				term, colored := startCodeThemePTY(t, f, plain, "code")
				defer func() { term.close() }()
				term.wait("Enter send")
				beforeConfig, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatal(err)
				}
				term.send("/themes\r")
				term.wait("Code themes", "Enter apply and save", "› Blue-gray")
				if index > 0 {
					term.send(strings.Repeat("\x1b[B", index))
				}
				term.wait("› " + tc.label)
				term.send("\r")
				term.wait("Enter send")
				assertCodeThemePTYText(t, term, colored, "Astonish · code", tc.accent, tc.canvas, plain)
				assertCodeThemePTYText(t, term, colored, "fixture / pty-model", tc.muted, tc.canvas, plain)
				assertCodeThemePTYCanvasPalette(t, term, colored, plain, tc.canvas, tc.card)
				pref, err := config.LoadCodeUIPreferences()
				if err != nil || pref.ThemeID != tc.id {
					t.Fatalf("picker did not persist %s: %+v, %v", tc.id, pref, err)
				}
				afterConfig, err := os.ReadFile(configPath)
				if err != nil || string(afterConfig) != string(beforeConfig) {
					t.Fatalf("saving %s changed config.yaml: %v", tc.id, err)
				}
				if tc.light {
					prefPath, err := config.GetCodeUIPreferencesPath()
					if err != nil {
						t.Fatal(err)
					}
					beforePreference, err := os.ReadFile(prefPath)
					if err != nil {
						t.Fatal(err)
					}
					term.close()
					// No --theme override: the new process must load the saved palette.
					term, colored = startCodeThemePTY(t, f, plain, "code")
					term.wait("Enter send")
					assertCodeThemePTYText(t, term, colored, "Astonish · code", tc.accent, tc.canvas, plain)
					assertCodeThemePTYCanvasPalette(t, term, colored, plain, tc.canvas, tc.card)
					term.send("/themes\r")
					term.wait("› "+tc.label+" · current · saved", "Enter apply and save")
					term.send("\x1b")
					term.wait("Enter send")
					afterPreference, err := os.ReadFile(prefPath)
					if err != nil || string(afterPreference) != string(beforePreference) {
						t.Fatalf("reopening/canceling rewrote %s preference: %v", tc.id, err)
					}
				}
				if got := f.calls.Load(); got != 0 {
					t.Fatalf("theme-only interaction made %d provider calls", got)
				}
			})
		}
	}
}

func TestCodeThemeCatalogShortPickerPTYAcceptance(t *testing.T) {
	f := newSidebarPTYFixture(t)
	term, colored := startCodeThemePTY(t, f, false, "code")
	defer term.close()
	term.wait("Enter send")
	// Fewer visible rows than catalog entries: navigation must scroll the picker
	// rather than placing the final theme or the operation hints below the screen.
	resizeSidebarPTYColor(term, colored, 72, 12)
	term.send("/themes\r")
	term.wait("Code themes", "› Blue-gray", "Enter apply and save")
	term.send(strings.Repeat("\x1b[B", len(codeThemeCatalogPTYCases)-1))
	last := codeThemeCatalogPTYCases[len(codeThemeCatalogPTYCases)-1]
	term.wait("› "+last.label, "Enter apply and save", "Esc cancel")
	assertCodeThemePTYText(t, term, colored, "› "+last.label, "#ff8700", "#101922", false)
	term.send("\x1b[A")
	term.wait("› Solarized Light-inspired", "Enter apply and save")
	term.send("\x1b[B\r")
	term.wait("Message Astonish…")
	assertCodeThemePTYText(t, term, colored, "Astonish · code", last.accent, last.canvas, false)
	pref, err := config.LoadCodeUIPreferences()
	if err != nil || pref.ThemeID != last.id {
		t.Fatalf("short picker did not save final option: %+v, %v", pref, err)
	}
	resizeSidebarPTYColor(term, colored, 120, 65)
	term.wait("Enter send")
	assertCodeThemePTYCanvasPalette(t, term, colored, false, last.canvas, last.card)
	if got := f.calls.Load(); got != 0 {
		t.Fatalf("short picker made %d provider calls", got)
	}
}
