package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestModelFooterTextShowsProviderAndConcreteModel(t *testing.T) {
	got := modelFooterText("sap-ai-core", "anthropic--claude-3.7-sonnet")
	want := "sap-ai-core / anthropic--claude-3.7-sonnet"
	if got != want {
		t.Fatalf("modelFooterText() = %q, want %q", got, want)
	}
}

func TestModelFooterTextDoesNotShowDefaultAsModel(t *testing.T) {
	got := modelFooterText("sap-ai-core", "default")
	want := "sap-ai-core / model resolving…"
	if got != want {
		t.Fatalf("modelFooterText() = %q, want %q", got, want)
	}
}

func TestModelFooterTextWaitsForProviderAndModel(t *testing.T) {
	got := modelFooterText("", "")
	want := "Provider/model loading…"
	if got != want {
		t.Fatalf("modelFooterText() = %q, want %q", got, want)
	}
}

func TestFooterWorkDirText(t *testing.T) {
	if got := footerWorkDirText(""); got != "" {
		t.Fatalf("footerWorkDirText(\"\") = %q, want empty", got)
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	got := footerWorkDirText(filepath.Join(home, "Projects", "astonish"))
	want := "~" + string(filepath.Separator) + "Projects" + string(filepath.Separator) + "astonish"
	if got != want {
		t.Fatalf("footerWorkDirText(home/Projects/astonish) = %q, want %q", got, want)
	}
	if strings.Contains(got, home) {
		t.Fatalf("footerWorkDirText should not include the raw home path: %q", got)
	}

	if got := footerWorkDirText("/tmp/my-project"); got != "/tmp/my-project" {
		t.Fatalf("footerWorkDirText(non-home) = %q, want unchanged", got)
	}
}

func TestTruncatePathLeft(t *testing.T) {
	if got := truncatePathLeft("~/Projects/astonish", 80); got != "~/Projects/astonish" {
		t.Fatalf("short path should be unchanged, got %q", got)
	}
	if got := truncatePathLeft("~/Projects/astonish", 0); got != "" {
		t.Fatalf("width 0 should be empty, got %q", got)
	}
	if got := truncatePathLeft("~/Projects/astonish", 1); got != "…" {
		t.Fatalf("width 1 should be ellipsis, got %q", got)
	}

	got := truncatePathLeft("~/very/deep/nested/path/astonish", 12)
	if !strings.HasPrefix(got, "…") {
		t.Fatalf("truncated path should start with ellipsis, got %q", got)
	}
	if !strings.HasSuffix(got, "astonish") {
		t.Fatalf("truncated path should keep the project name, got %q", got)
	}
	if w := lipgloss.Width(got); w > 12 {
		t.Fatalf("truncated path wider than requested: width=%d %q", w, got)
	}
}

func TestTruncatePathLeftKeepsShortPath(t *testing.T) {
	if got := truncatePathLeft("pkg/tui", 20); got != "pkg/tui" {
		t.Fatalf("short path should be unchanged, got %q", got)
	}
}

func TestRenderFooterMetaShowsFolderInCodeMode(t *testing.T) {
	m := model{
		theme: DefaultTheme(),
		width: 120,
		info: backend.Info{
			Mode:       "code",
			Provider:   "sap-ai-core",
			Model:      "anthropic--claude-3.7-sonnet",
			WorkingDir: "/tmp/my-project",
		},
	}
	out := stripANSI(m.renderFooterMeta())
	if strings.Contains(out, "\n") {
		t.Fatalf("footer should stay on one line: %q", out)
	}
	if !strings.Contains(out, "sap-ai-core / anthropic--claude-3.7-sonnet") {
		t.Fatalf("footer missing model: %q", out)
	}
	if !strings.Contains(out, "/tmp/my-project") {
		t.Fatalf("footer missing project folder: %q", out)
	}
	if got := lipgloss.Width(out); got != 120 {
		t.Fatalf("footer width=%d want 120: %q", got, out)
	}
}

func TestRenderFooterMetaShowsFolderBetweenModelAndAutoApprove(t *testing.T) {
	m := model{
		theme: DefaultTheme(),
		width: 140,
		info: backend.Info{
			Mode:        "code",
			Provider:    "sap-ai-core",
			Model:       "anthropic--claude-3.7-sonnet",
			WorkingDir:  "/tmp/my-project",
			AutoApprove: true,
		},
	}
	out := stripANSI(m.renderFooterMeta())
	modelIdx := strings.Index(out, "sap-ai-core / anthropic--claude-3.7-sonnet")
	folderIdx := strings.Index(out, "/tmp/my-project")
	autoIdx := strings.Index(out, "auto-approve")
	if modelIdx < 0 || folderIdx < 0 || autoIdx < 0 {
		t.Fatalf("footer missing one of model/folder/auto-approve: %q", out)
	}
	if !(modelIdx < folderIdx && folderIdx < autoIdx) {
		t.Fatalf("expected model, then folder, then auto-approve: %q", out)
	}
}

func TestRenderFooterMetaDropsOrTruncatesFolderWhenNarrow(t *testing.T) {
	m := model{
		theme: DefaultTheme(),
		width: 48,
		info: backend.Info{
			Mode:       "code",
			Provider:   "sap-ai-core",
			Model:      "anthropic--claude-3.7-sonnet",
			WorkingDir: "/tmp/very/deep/nested/path/astonish",
		},
	}
	out := stripANSI(m.renderFooterMeta())
	if strings.Contains(out, "\n") {
		t.Fatalf("footer should stay on one line: %q", out)
	}
	if !strings.Contains(out, "sap-ai-core / anthropic--claude-3.7-sonnet") {
		t.Fatalf("model must remain visible on a narrow footer: %q", out)
	}
	if strings.Contains(out, "/tmp/very/deep/nested/path/astonish") {
		t.Fatalf("full long path should not leak on a narrow footer: %q", out)
	}
	if got := lipgloss.Width(out); got != 48 {
		t.Fatalf("footer width=%d want 48: %q", got, out)
	}
}

func TestRenderFooterMetaOmitsFolderInPlatformMode(t *testing.T) {
	m := model{
		theme: DefaultTheme(),
		width: 80,
		info: backend.Info{
			Mode:     "platform",
			Provider: "sap-ai-core",
			Model:    "anthropic--claude-3.7-sonnet",
		},
	}
	out := stripANSI(m.renderFooterMeta())
	if !strings.Contains(out, "sap-ai-core / anthropic--claude-3.7-sonnet") {
		t.Fatalf("platform footer missing model: %q", out)
	}
	if strings.Contains(out, "/") && strings.Contains(out, "tmp") {
		t.Fatalf("platform footer should not show a host folder: %q", out)
	}
	if strings.Contains(out, "Folder:") {
		t.Fatalf("platform footer should not use a Folder label: %q", out)
	}
}

func TestRenderFooterMetaHyphenatedFolderStaysOneLine(t *testing.T) {
	m := model{
		theme: DefaultTheme(),
		width: 120,
		info: backend.Info{
			Mode:       "code",
			Provider:   "sap-ai-core",
			Model:      "claude",
			WorkingDir: "/tmp/my-long-hyphenated-project-name-here",
		},
	}
	out := stripANSI(m.renderFooterMeta())
	if strings.Contains(out, "\n") {
		t.Fatalf("hyphenated folder must stay on one line: %q", out)
	}
	if !strings.Contains(out, "my-long-hyphenated-project-name-here") {
		t.Fatalf("hyphenated folder missing: %q", out)
	}
}

func TestStatusTextIncludesFolderInCodeMode(t *testing.T) {
	m := model{
		info: backend.Info{
			Mode:       "code",
			SessionID:  "sess-1",
			Provider:   "sap-ai-core",
			Model:      "claude",
			WorkingDir: "/tmp/my-project",
		},
		tr: events.NewTranscript(),
	}
	got := m.statusText()
	if !strings.Contains(got, "Folder: /tmp/my-project") {
		t.Fatalf("status missing folder: %q", got)
	}
}

func TestStatusTextOmitsFolderInPlatformMode(t *testing.T) {
	m := model{
		info: backend.Info{
			Mode:     "platform",
			Provider: "sap-ai-core",
			Model:    "claude",
		},
		tr: events.NewTranscript(),
	}
	got := m.statusText()
	if strings.Contains(got, "Folder:") {
		t.Fatalf("platform status should omit folder: %q", got)
	}
}
