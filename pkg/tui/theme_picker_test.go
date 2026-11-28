package tui

import (
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/charmbracelet/x/ansi"
	"reflect"
	"strings"
	"testing"
)

type themeTestBackend struct {
	staticBackend
	saved            string
	loadErr, saveErr error
	loads, saves     int
}

func (b *themeTestBackend) LoadCodeThemePreference(context.Context) (string, error) {
	b.loads++
	return b.saved, b.loadErr
}
func (b *themeTestBackend) SaveCodeThemePreference(_ context.Context, id string) error {
	b.saves++
	if b.saveErr == nil {
		b.saved = id
	}
	return b.saveErr
}
func themeTestModel(t *testing.T) (model, *themeTestBackend) {
	t.Helper()
	b := &themeTestBackend{staticBackend: staticBackend{info: backend.Info{Mode: "code"}}}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 35})
	t.Cleanup(m.cancel)
	m.ready = true
	m.layout()
	return m, b
}
func themeKey(m model, code rune) (model, tea.Cmd) {
	n, c := m.Update(tea.KeyPressMsg{Code: code, Text: func() string {
		if code >= 32 && code < 127 {
			return string(code)
		}
		return ""
	}()})
	return n.(model), c
}
func themeReply(m model, c tea.Cmd) model { n, _ := m.Update(c()); return n.(model) }
func TestCodeThemePickerSaveCancelAndRepaint(t *testing.T) {
	for _, plain := range []string{"", "1"} {
		t.Run("NO_COLOR="+plain, func(t *testing.T) {
			t.Setenv("NO_COLOR", plain)
			m, b := themeTestModel(t)
			m.tr.Apply(events.NewText(strings.Repeat("A line of transcript\n", 80)))
			m.layout()
			m.vp.SetYOffset(8)
			m.userScrolledUp = true
			m.ta.SetValue("draft")
			m.pastedBlocks = []pastedBlock{{placeholder: "paste"}}
			m.selecting = true
			m.selectionStart = selectionPoint{line: 4, col: 2}
			before := ansi.Strip(m.viewContent())
			owner := m.tr
			selection := m.selectionStart
			offsets := m.vp.YOffset()
			n, c := m.handleSlash("/themes")
			m = themeReply(n.(model), c)
			if m.themePicker.stage != ThemePickerReady {
				t.Fatal("not ready")
			}
			m, _ = themeKey(m, tea.KeyDown)
			if b.saves != 0 || m.theme.CodeThemeID != CodeThemeBlueGray {
				t.Fatal("navigation previewed or saved")
			}
			m, _ = themeKey(m, tea.KeyEscape)
			if m.ta.Value() != "draft" || m.vp.YOffset() != offsets {
				t.Fatal("cancel lost draft/offset")
			}
			n, c = m.openThemePicker()
			m = themeReply(n.(model), c)
			m, _ = themeKey(m, tea.KeyDown)
			m, c = themeKey(m, tea.KeyEnter)
			m, _ = themeKey(m, tea.KeyEscape)
			if !m.themePicker.open || !strings.Contains(m.themePicker.err, "wait") {
				t.Fatal("saving Esc falsely cancelled")
			}
			m = themeReply(m, c)
			if m.themePicker.open || m.theme.CodeThemeID != CodeThemeDarkOrange || b.saved != "dark-orange" {
				t.Fatal("save did not apply")
			}
			if m.tr != owner || m.ta.Value() != "draft" || m.vp.YOffset() != offsets || !m.userScrolledUp || !m.selecting || m.selectionStart != selection || len(m.pastedBlocks) != 1 {
				t.Fatal("repaint mutated interaction state")
			}
			if after := ansi.Strip(m.viewContent()); before != after {
				t.Fatal("palette changed layout")
			}
		})
	}
}
func TestCodeThemePickerErrorsRetriesStaleAndModalCapture(t *testing.T) {
	m, b := themeTestModel(t)
	m.ta.SetValue("draft")
	b.loadErr = errors.New("load blocked")
	n, c := m.openThemePicker()
	m = themeReply(n.(model), c)
	if m.themePicker.stage != ThemePickerLoadError {
		t.Fatal("no load error")
	}
	b.loadErr = nil
	m, c = themeKey(m, 'r')
	m = themeReply(m, c)
	m, _ = themeKey(m, tea.KeyDown)
	b.saveErr = errors.New("save blocked")
	m, c = themeKey(m, tea.KeyEnter)
	m = themeReply(m, c)
	if m.themePicker.stage != ThemePickerSaveError || m.theme.CodeThemeID != CodeThemeBlueGray {
		t.Fatal("failed save applied")
	}
	b.saveErr = nil
	m, c = themeKey(m, 'r')
	m = themeReply(m, c)
	if m.theme.CodeThemeID != CodeThemeDarkOrange {
		t.Fatal("retry did not apply")
	}
	n, c = m.openThemePicker()
	m = n.(model)
	stale := c()
	m, _ = themeKey(m, tea.KeyEscape)
	n, c = m.openThemePicker()
	m = n.(model)
	q, _ := m.Update(stale)
	m = q.(model)
	if m.themePicker.stage != ThemePickerLoading {
		t.Fatal("stale generation accepted")
	}
	m = themeReply(m, c)
	for _, msg := range []tea.Msg{tea.PasteMsg{Content: "leak"}, tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.KeyPressMsg{Code: 'x'}} {
		q, _ = m.Update(msg)
		m = q.(model)
	}
	if m.ta.Value() != "draft" {
		t.Fatal("modal leaked input")
	}
	owner := m.tr
	n, _ = m.Update(eventMsg(events.NewText("live while selecting")))
	m = n.(model)
	if m.tr != owner || !strings.Contains(m.tr.Items[len(m.tr.Items)-1].Content, "live while selecting") {
		t.Fatal("modal blocked stream")
	}
	n, c = m.openThemePicker()
	m = n.(model)
	stale = c()
	m.tr = events.NewTranscript()
	q, _ = m.Update(stale)
	if q.(model).themePicker.open {
		t.Fatal("stale owner accepted")
	}
}
func TestCodeThemeSavedPrecedenceAndLazySlot(t *testing.T) {
	b := &themeTestBackend{staticBackend: staticBackend{info: backend.Info{Mode: "code"}}, saved: "dark-orange"}
	for _, id := range []CodeThemeID{"", CodeThemeBlueGray} {
		m := newModel(context.Background(), Config{Backend: b, ThemeID: id})
		defer m.cancel()
		want := CodeThemeDarkOrange
		if id != "" {
			want = id
		}
		if m.theme.CodeThemeID != want {
			t.Fatal("launch precedence")
		}
	}
	remote := staticBackend{info: backend.Info{Mode: "platform"}}
	m := newModel(context.Background(), Config{Backend: remote, AltBackend: b, Width: 100, Height: 30})
	defer m.cancel()
	m.ready = true
	m.layout()
	platform := m.theme
	loads := b.loads
	n, _ := m.switchBackend()
	m = n.(model)
	if m.theme.CodeThemeID != CodeThemeDarkOrange || b.loads != loads+1 {
		t.Fatal("lazy load missing")
	}
	m.applyCodeTheme(CodeThemeBlueGray)
	n, _ = m.switchBackend()
	m = n.(model)
	if !reflect.DeepEqual(platform, m.theme) {
		t.Fatal("recolored Platform")
	}
	n, _ = m.switchBackend()
	m = n.(model)
	if b.loads != loads+1 || m.theme.CodeThemeID != CodeThemeBlueGray {
		t.Fatal("switch reloaded saved preference")
	}
	b.saved = "future-theme"
	th, err := resolveInitialCodeTheme(context.Background(), b, "")
	if err != nil || th.CodeThemeID != CodeThemeBlueGray {
		t.Fatal("unknown preference fallback")
	}
}
func TestCodeThemePickerStreamingCommandAndResize(t *testing.T) {
	m, _ := themeTestModel(t)
	m.tr.Streaming = true
	for _, r := range "/themes" {
		m, _ = themeKey(m, r)
	}
	if m.ta.Value() != "/themes" {
		t.Fatal("streaming command typing blocked")
	}
	m, c := themeKey(m, tea.KeyEnter)
	if !m.themePicker.open || c == nil {
		t.Fatal("streaming command did not open")
	}
	m = themeReply(m, c)
	owner := m.tr
	n, _ := m.Update(tea.WindowSizeMsg{Width: 72, Height: 28})
	m = n.(model)
	if m.width != 72 || !m.themePicker.open || m.tr != owner || !m.tr.Streaming {
		t.Fatal("modal blocked resize or lost stream")
	}
	m, _ = themeKey(m, tea.KeyEscape)
	m.ta.SetValue("existing draft")
	m, _ = themeKey(m, 'x')
	if m.ta.Value() != "existing draft" {
		t.Fatal("streaming theme admission changed unrelated draft")
	}
}

func TestCodeThemePickerViewerOffsetsAndPhaseTransition(t *testing.T) {
	for _, plain := range []string{"", "1"} {
		t.Run("NO_COLOR="+plain, func(t *testing.T) {
			t.Setenv("NO_COLOR", plain)
			m := sidebarTestModel(120, 35)
			t.Cleanup(m.cancel)
			b := &themeTestBackend{staticBackend: staticBackend{info: backend.Info{Mode: "code"}}}
			m.backend = b
			m.backends[m.activeBackendIdx].backend = b
			m.fileViewer = fileViewerState{open: true, artifact: events.Artifact{Path: "reference.go"}, content: strings.Repeat("// viewer line\n", 100), vp: viewport.New()}
			m.delegationDetail = delegationDetailState{open: true, itemIdx: -1, vp: viewport.New(), cacheKey: "old-palette", offsets: [2]int{3, 7}}
			m.delegationDetail.vp.SetContent(strings.Repeat("detail row\n", 100))
			m.planPanel.open = true
			m.layoutFileViewer()
			m.layoutDelegationDetail()
			m.layoutPlanPanel()
			m.vp.SetYOffset(9)
			m.fileViewer.vp.SetYOffset(6)
			m.delegationDetail.vp.SetYOffset(4)
			m.planPanel.sidebar.SetYOffset(8)
			m.planPanel.detail.SetYOffset(11)
			m.ta.SetValue("draft retained")
			m.userScrolledUp = true
			n, c := m.openThemePicker()
			m = themeReply(n.(model), c)
			// Advance the authoritative plan while the modal is open.
			snapshot := *m.tr.Items[0].PlanSnapshot
			snapshot.Steps = append([]agent.PlanStepInfo(nil), snapshot.Steps...)
			snapshot.Sequence++
			snapshot.Steps[1].Status = "complete"
			n, _ = m.Update(eventMsg(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &snapshot}))
			m = n.(model)
			before := [5]int{m.vp.YOffset(), m.fileViewer.vp.YOffset(), m.delegationDetail.vp.YOffset(), m.planPanel.sidebar.YOffset(), m.planPanel.detail.YOffset()}
			fileRows := ansi.Strip(m.renderFileViewerContent(116))
			target, reveal := m.planPanel.target, m.planPanel.revealPending
			owner := m.tr
			m, _ = themeKey(m, tea.KeyDown)
			m, c = themeKey(m, tea.KeyEnter)
			m = themeReply(m, c)
			after := [5]int{m.vp.YOffset(), m.fileViewer.vp.YOffset(), m.delegationDetail.vp.YOffset(), m.planPanel.sidebar.YOffset(), m.planPanel.detail.YOffset()}
			if before != after || m.tr != owner || m.ta.Value() != "draft retained" || !m.userScrolledUp || m.delegationDetail.offsets != [2]int{3, 7} || m.planPanel.target != target || m.planPanel.revealPending != reveal {
				t.Fatalf("repaint lost live navigation state: %v -> %v", before, after)
			}
			if !m.tr.Streaming || m.tr.Items[0].PlanSnapshot.Steps[1].Status != "complete" || m.delegationDetail.cacheKey != "" {
				t.Fatal("repaint reverted streamed plan or retained old viewer cache")
			}
			if fileRows != ansi.Strip(m.renderFileViewerContent(116)) {
				t.Fatal("file viewer changed rows on palette switch")
			}
		})
	}
}

func TestCodeThemeCopyExcludesComponentPadding(t *testing.T) {
	for _, id := range []CodeThemeID{CodeThemeBlueGray, CodeThemeDarkOrange} {
		m, _ := themeTestModel(t)
		m.applyCodeTheme(id)
		m.tr.Apply(events.NewUser("copy body sentinel"))
		m.refreshViewport()
		found := false
		for row, line := range m.transcriptPlainLines {
			if !strings.Contains(line, "copy body sentinel") {
				continue
			}
			found = true
			got := selectionText(m.transcriptPlainLines, m.transcriptContentSpans, selectionPoint{line: row, col: 0}, selectionPoint{line: row, col: len([]rune(line))})
			if got != "copy body sentinel" {
				t.Fatalf("%s copied frame/padding: %q", id, got)
			}
		}
		if !found {
			t.Fatal("copy fixture absent")
		}
	}
}

func TestCodeThemePickerRejectsEveryStaleReplyIdentity(t *testing.T) {
	for _, identity := range []string{"generation", "slot", "backend", "owner", "session", "stage"} {
		t.Run(identity, func(t *testing.T) {
			m, _ := themeTestModel(t)
			n, c := m.openThemePicker()
			m = themeReply(n.(model), c)
			m, _ = themeKey(m, tea.KeyDown)
			m, c = themeKey(m, tea.KeyEnter)
			reply := c().(themePreferenceMsg)
			switch identity {
			case "generation":
				reply.generation++
			case "slot":
				reply.slot++
			case "backend":
				reply.backend = &themeTestBackend{}
			case "owner":
				reply.owner = events.NewTranscript()
			case "session":
				reply.session += "stale"
			case "stage":
				m.themePicker.stage = ThemePickerReady
			}
			n, _ = m.Update(reply)
			if n.(model).theme.CodeThemeID != CodeThemeBlueGray {
				t.Fatal("stale save recolored the screen")
			}
		})
	}
}

func TestThemesSlashCapabilityCompletion(t *testing.T) {
	m, _ := themeTestModel(t)
	m.ta.SetValue("/the")
	m.syncSlashCompletion()
	if len(m.slash.matches) != 1 || m.slash.matches[0].Name != "themes" {
		t.Fatal("missing completion")
	}
	m.info.Mode = "platform"
	m.syncSlashCompletion()
	if len(m.slash.matches) != 0 {
		t.Fatal("remote exposed themes")
	}
	n, c := m.handleSlash("/themes")
	if n.(model).themePicker.open || c != nil {
		t.Fatal("remote executed themes")
	}
}
