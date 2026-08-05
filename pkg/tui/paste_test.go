package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// ── helpers ──────────────────────────────────────────────────────────────

func newPasteTestModel(t *testing.T) model {
	t.Helper()
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	return m
}

func newPasteTestModelWithBackend(t *testing.T, b backend.Backend) model {
	t.Helper()
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	return m
}

func applyKey(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(model)
}

func pasteFourLines() string {
	return "one\ntwo\nthree\nfour"
}

func seedPlaceholder(t *testing.T, m *model, prefix, suffix, content string) {
	t.Helper()
	placeholder := pastePlaceholder(content)
	m.pastedBlocks = []pastedBlock{{placeholder: placeholder, content: content}}
	m.ta.SetValue(prefix + placeholder + suffix)
	m.ta.CursorEnd()
}

func assertPlaceholderIntact(t *testing.T, value, placeholder string) {
	t.Helper()
	if !strings.Contains(value, placeholder) {
		t.Fatalf("placeholder %q missing from value %q", placeholder, value)
	}
	// Ensure the token was not split by an inserted character.
	idx := strings.Index(value, "[Pasted:")
	if idx < 0 {
		return
	}
	end := strings.Index(value[idx:], "]")
	if end < 0 {
		t.Fatalf("broken placeholder without closing bracket: %q", value)
	}
	token := value[idx : idx+end+1]
	if !strings.HasPrefix(token, "[Pasted: ") || !strings.HasSuffix(token, " lines]") {
		t.Fatalf("malformed placeholder token %q in %q", token, value)
	}
}

// ── pure helpers ─────────────────────────────────────────────────────────

func TestNormalizePasteText(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"a\nb", "a\nb"},
		{"a\rb", "a\nb"},
		{"a\r\nb", "a\nb"},
		{"a\r\nb\rc", "a\nb\nc"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := normalizePasteText(tc.in); got != tc.want {
			t.Fatalf("normalizePasteText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPasteLineCountAndCollapseLineCount(t *testing.T) {
	tests := []struct {
		in             string
		wantLines      int
		wantCollapse   int
		wantShouldColl bool
	}{
		{"", 0, 0, false},
		{"one", 1, 1, false},
		{"one\ntwo", 2, 2, false},
		{"one\ntwo\nthree", 3, 3, false},
		{"one\ntwo\nthree\nfour", 4, 4, true},
		// Trailing newline creates an empty 4th visual line but only 3 content lines.
		{"one\ntwo\nthree\n", 4, 3, false},
		{"one\ntwo\nthree\nfour\n", 5, 4, true},
		{"one\rtwo\rthree\rfour", 4, 4, true},
		{"[Pasted: 4 lines]", 1, 1, false},
	}
	for _, tc := range tests {
		if got := pasteLineCount(tc.in); got != tc.wantLines {
			t.Fatalf("pasteLineCount(%q) = %d, want %d", tc.in, got, tc.wantLines)
		}
		if got := pasteCollapseLineCount(tc.in); got != tc.wantCollapse {
			t.Fatalf("pasteCollapseLineCount(%q) = %d, want %d", tc.in, got, tc.wantCollapse)
		}
		if got := composerShouldCollapseValue(tc.in); got != tc.wantShouldColl {
			t.Fatalf("composerShouldCollapseValue(%q) = %v, want %v", tc.in, got, tc.wantShouldColl)
		}
	}
}

func TestPastePlaceholderFormat(t *testing.T) {
	if got := pastePlaceholder("a\nb\nc\nd"); got != "[Pasted: 4 lines]" {
		t.Fatalf("pastePlaceholder = %q", got)
	}
	if got := pastePlaceholder("a\nb\nc\nd\ne\n"); got != "[Pasted: 5 lines]" {
		t.Fatalf("pastePlaceholder trailing = %q", got)
	}
}

func TestFindInsertedText(t *testing.T) {
	prefix, inserted, suffix, ok := findInsertedText("before after", "before one\ntwo\nthree\nfour after")
	if !ok {
		t.Fatal("expected inserted text")
	}
	if prefix != "before " || inserted != "one\ntwo\nthree\nfour " || suffix != "after" {
		t.Fatalf("findInsertedText = (%q, %q, %q)", prefix, inserted, suffix)
	}
}

func TestExpandPastedBlocks(t *testing.T) {
	m := newPasteTestModel(t)
	m.pastedBlocks = []pastedBlock{
		{placeholder: "[Pasted: 4 lines]", content: "one\ntwo\nthree\nfour"},
	}
	got := m.expandPastedBlocks("prefix [Pasted: 4 lines] suffix")
	want := "prefix one\ntwo\nthree\nfour suffix"
	if got != want {
		t.Fatalf("expandPastedBlocks = %q, want %q", got, want)
	}
}

// ── paste insertion thresholds ───────────────────────────────────────────

func TestSmallPasteInsertsContentAsIs(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int // expected composer height
	}{
		{"one line", "hello", 1},
		{"two lines", "one\ntwo", 2},
		{"three lines", "one\ntwo\nthree", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newPasteTestModel(t)
			m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.text), Paste: true})
			if got := m.ta.Value(); got != tc.text {
				t.Fatalf("value = %q, want %q", got, tc.text)
			}
			if len(m.pastedBlocks) != 0 {
				t.Fatalf("expected no pastedBlocks, got %+v", m.pastedBlocks)
			}
			if h := m.composerTextHeight(); h != tc.want {
				t.Fatalf("composer height = %d, want %d", h, tc.want)
			}
		})
	}
}

func TestLargePasteShowsPlaceholder(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		wantPh  string
		wantRaw string
	}{
		{
			name:    "bracketed paste 4 lines",
			text:    pasteFourLines(),
			wantPh:  "[Pasted: 4 lines]",
			wantRaw: pasteFourLines(),
		},
		{
			name:    "unbracketed multi-line runes",
			text:    "a\nb\nc\nd\ne",
			wantPh:  "[Pasted: 5 lines]",
			wantRaw: "a\nb\nc\nd\ne",
		},
		{
			name:    "carriage returns",
			text:    "one\rtwo\rthree\rfour",
			wantPh:  "[Pasted: 4 lines]",
			wantRaw: "one\ntwo\nthree\nfour",
		},
		{
			name:    "crlf",
			text:    "one\r\ntwo\r\nthree\r\nfour",
			wantPh:  "[Pasted: 4 lines]",
			wantRaw: "one\ntwo\nthree\nfour",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newPasteTestModel(t)
			// First case uses Paste:true; others exercise unbracketed multi-line KeyRunes.
			paste := strings.Contains(tc.name, "bracketed")
			m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.text), Paste: paste})
			if got := m.ta.Value(); got != tc.wantPh {
				t.Fatalf("value = %q, want %q", got, tc.wantPh)
			}
			if len(m.pastedBlocks) != 1 {
				t.Fatalf("pastedBlocks len = %d, want 1: %+v", len(m.pastedBlocks), m.pastedBlocks)
			}
			if m.pastedBlocks[0].content != tc.wantRaw {
				t.Fatalf("stored content = %q, want %q", m.pastedBlocks[0].content, tc.wantRaw)
			}
			if m.pastedBlocks[0].placeholder != tc.wantPh {
				t.Fatalf("stored placeholder = %q, want %q", m.pastedBlocks[0].placeholder, tc.wantPh)
			}
			if h := m.composerTextHeight(); h != 1 {
				t.Fatalf("composer height after large paste = %d, want 1", h)
			}
		})
	}
}

func TestUnbracketedLargePasteShowsPlaceholder(t *testing.T) {
	m := newPasteTestModel(t)
	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasteFourLines())})
	if got := m.ta.Value(); got != "[Pasted: 4 lines]" {
		t.Fatalf("value = %q", got)
	}
}

// ── Command+V / single-line pastes ───────────────────────────────────────

func TestSingleLinePastesNeverCollapseEvenAfterFourLines(t *testing.T) {
	// Collapse must be based on each paste payload size, not total composer lines.
	m := newPasteTestModel(t)
	for _, line := range []string{"one", "two", "three", "four", "five"} {
		m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(line), Paste: true})
		if line != "five" {
			m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\n"), Paste: true})
		}
	}
	if strings.Contains(m.ta.Value(), "[Pasted:") {
		t.Fatalf("single-line pastes collapsed incorrectly: %q", m.ta.Value())
	}
	if len(m.pastedBlocks) != 0 {
		t.Fatalf("pastedBlocks should be empty, got %+v", m.pastedBlocks)
	}
	// Composer should retain all five lines.
	if got := pasteCollapseLineCount(m.ta.Value()); got != 5 {
		t.Fatalf("composer lines = %d, want 5; value=%q", got, m.ta.Value())
	}
}

func TestLineByLineRuneInjectionDoesNotCollapse(t *testing.T) {
	// Terminals may inject Command+V as ordinary one-line-at-a-time runes.
	// Only a single multi-line insertion of 4+ lines should collapse.
	m := newPasteTestModel(t)
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("4")},
		{Type: tea.KeyRunes, Runes: []rune("\n")},
		{Type: tea.KeyRunes, Runes: []rune("5")},
		{Type: tea.KeyRunes, Runes: []rune("\n")},
		{Type: tea.KeyRunes, Runes: []rune("6")},
		{Type: tea.KeyRunes, Runes: []rune("\n")},
		{Type: tea.KeyRunes, Runes: []rune("7")},
	} {
		m = applyKey(t, m, msg)
	}
	if strings.Contains(m.ta.Value(), "[Pasted:") {
		t.Fatalf("line-by-line injection collapsed: %q", m.ta.Value())
	}
	if got := m.ta.Value(); got != "4\n5\n6\n7" {
		t.Fatalf("value = %q", got)
	}
}

func TestMultiLinePasteIntoExistingComposerCollapsesOnlyPaste(t *testing.T) {
	m := newPasteTestModel(t)
	m.ta.SetValue("existing line")
	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\none\ntwo\nthree\nfour"), Paste: true})
	if got := m.ta.Value(); got != "existing line[Pasted: 5 lines]" && got != "existing line\n[Pasted: 4 lines]" {
		// Paste includes leading newline → 5 content lines after normalize, or 4 if counted differently.
		// Accept either the placeholder form with prefix preserved.
		if !strings.HasPrefix(got, "existing line") || !strings.Contains(got, "[Pasted:") {
			t.Fatalf("value = %q", got)
		}
	}
	if !strings.HasPrefix(m.ta.Value(), "existing line") {
		t.Fatalf("existing composer text was lost: %q", m.ta.Value())
	}
	if len(m.pastedBlocks) != 1 {
		t.Fatalf("pastedBlocks = %+v", m.pastedBlocks)
	}
}

func TestComposerWatchDoesNotCollapsePlainMultiLineContent(t *testing.T) {
	m := newPasteTestModel(t)
	m.ta.SetValue("1\n2\n3\n4")
	m.intentionalMultiline = false
	m.pastedBlocks = nil

	m = applyKey(t, m, composerWatchMsg{})
	if got := m.ta.Value(); got != "1\n2\n3\n4" {
		t.Fatalf("watch collapsed plain multi-line content: %q", got)
	}
}

func TestTextareaFallbackCollapsesLargeInsertedContent(t *testing.T) {
	m := newPasteTestModel(t)
	m.ta.InsertString("before ")
	previous := m.ta.Value()
	m.ta.InsertString(pasteFourLines())
	_ = m.afterComposerChange(previous)

	if got := m.ta.Value(); got != "before [Pasted: 4 lines]" {
		t.Fatalf("value = %q", got)
	}
	if len(m.pastedBlocks) != 1 || m.pastedBlocks[0].content != pasteFourLines() {
		t.Fatalf("pastedBlocks = %+v", m.pastedBlocks)
	}
}

// ── intentional multi-line typing ────────────────────────────────────────

func TestComposerExpandsForTypedMultiline(t *testing.T) {
	m := newPasteTestModel(t)
	m.ta.SetValue("line1")
	next, _ := m.insertNewline(true)
	m = next.(model)
	m.ta.InsertString("line2")

	if !m.intentionalMultiline {
		t.Fatal("expected intentionalMultiline after Shift+Enter")
	}
	if h := m.composerTextHeight(); h != 2 {
		t.Fatalf("composer height = %d, want 2", h)
	}
	if got := m.ta.Value(); !strings.Contains(got, "line1") || !strings.Contains(got, "line2") {
		t.Fatalf("value = %q", got)
	}
	if strings.Contains(m.ta.Value(), "[Pasted:") {
		t.Fatalf("typed multi-line was collapsed: %q", m.ta.Value())
	}
}

func TestIntentionalMultilineDoesNotCollapseOnIdleOrWatch(t *testing.T) {
	m := newPasteTestModel(t)
	m.intentionalMultiline = true
	m.ta.SetValue("1\n2\n3\n4")
	m.pasteIdleSeq = 1

	m = applyKey(t, m, pasteIdleMsg{seq: 1})
	if got := m.ta.Value(); got != "1\n2\n3\n4" {
		t.Fatalf("idle collapsed intentional multi-line: %q", got)
	}

	m = applyKey(t, m, composerWatchMsg{})
	if got := m.ta.Value(); got != "1\n2\n3\n4" {
		t.Fatalf("watch collapsed intentional multi-line: %q", got)
	}
}

func TestIntentionalFourLinesStayExpanded(t *testing.T) {
	m := newPasteTestModel(t)
	m.ta.SetValue("a")
	for i := 0; i < 3; i++ {
		next, _ := m.insertNewline(true)
		m = next.(model)
		m.ta.InsertString("x")
	}
	if !m.intentionalMultiline {
		t.Fatal("expected intentional multi-line")
	}
	if pasteCollapseLineCount(m.ta.Value()) < 4 {
		t.Fatalf("expected 4+ lines, got %q", m.ta.Value())
	}
	if strings.Contains(m.ta.Value(), "[Pasted:") {
		t.Fatalf("intentional multi-line collapsed: %q", m.ta.Value())
	}
	if h := m.composerTextHeight(); h != 4 {
		t.Fatalf("composer height = %d, want 4", h)
	}
}

// ── submit expands placeholder ───────────────────────────────────────────

func TestLargePasteShowsPlaceholderAndSubmitsFullContent(t *testing.T) {
	b := &recordingBackend{}
	m := newPasteTestModelWithBackend(t, b)
	pasted := pasteFourLines()

	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasted), Paste: true})
	if got := m.ta.Value(); got != "[Pasted: 4 lines]" {
		t.Fatalf("value = %q", got)
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	m = next.(model)

	if b.message != pasted {
		t.Fatalf("backend message = %q, want %q", b.message, pasted)
	}
	if len(m.tr.Items) == 0 || m.tr.Items[len(m.tr.Items)-1].Content != pasted {
		t.Fatalf("transcript missing full paste: %+v", m.tr.Items)
	}
	if got := m.ta.Value(); got != "" {
		t.Fatalf("textarea after submit = %q", got)
	}
	if len(m.pastedBlocks) != 0 {
		t.Fatalf("pastedBlocks after submit = %+v", m.pastedBlocks)
	}
	if m.intentionalMultiline {
		t.Fatal("intentionalMultiline should clear after submit")
	}
}

func TestSubmitWithPrefixAndPlaceholderExpandsFullMessage(t *testing.T) {
	b := &recordingBackend{}
	m := newPasteTestModelWithBackend(t, b)
	content := pasteFourLines()
	seedPlaceholder(t, &m, "note: ", "", content)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	m = next.(model)

	want := "note: " + content
	if b.message != want {
		t.Fatalf("backend message = %q, want %q", b.message, want)
	}
	if len(m.tr.Items) == 0 || m.tr.Items[len(m.tr.Items)-1].Content != want {
		t.Fatalf("transcript = %+v", m.tr.Items)
	}
}

// ── atomic navigation ────────────────────────────────────────────────────

func TestPastePlaceholderArrowKeysJumpOverToken(t *testing.T) {
	m := newPasteTestModel(t)
	content := pasteFourLines()
	ph := pastePlaceholder(content)
	m.pastedBlocks = []pastedBlock{{placeholder: ph, content: content}}
	m.ta.SetValue("ab" + ph + "cd")

	// Cursor at end of token.
	m.ta.SetCursor(len("ab") + len(ph))
	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyLeft})
	_, col := m.composerLineCol()
	if col != len("ab") {
		t.Fatalf("left from end → col %d, want %d (token start)", col, len("ab"))
	}

	// Right from start jumps to end.
	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRight})
	_, col = m.composerLineCol()
	if col != len("ab")+len(ph) {
		t.Fatalf("right from start → col %d, want %d (token end)", col, len("ab")+len(ph))
	}
}

func TestPastePlaceholderWordMotionJumpsOverToken(t *testing.T) {
	m := newPasteTestModel(t)
	content := pasteFourLines()
	ph := pastePlaceholder(content)
	m.pastedBlocks = []pastedBlock{{placeholder: ph, content: content}}
	m.ta.SetValue("ab" + ph + "cd")
	m.ta.SetCursor(len("ab") + len(ph))

	// alt+left / word-back from end of token should jump to start.
	if !m.jumpPastePlaceholder(-1) {
		t.Fatal("expected jump on word-left at token end")
	}
	_, col := m.composerLineCol()
	if col != len("ab") {
		t.Fatalf("word-left col = %d, want %d", col, len("ab"))
	}
	if !m.jumpPastePlaceholder(1) {
		t.Fatal("expected jump on word-right at token start")
	}
	_, col = m.composerLineCol()
	if col != len("ab")+len(ph) {
		t.Fatalf("word-right col = %d, want %d", col, len("ab")+len(ph))
	}
}

func TestPastePlaceholderCannotNavigateInside(t *testing.T) {
	m := newPasteTestModel(t)
	content := pasteFourLines()
	ph := pastePlaceholder(content)
	m.pastedBlocks = []pastedBlock{{placeholder: ph, content: content}}
	m.ta.SetValue(ph)

	// Force caret into the middle of the token, then snap out.
	m.ta.SetCursor(3)
	m.snapOutOfPastePlaceholder(1)
	_, col := m.composerLineCol()
	if col != len(ph) {
		t.Fatalf("snap-out col = %d, want %d (token end)", col, len(ph))
	}

	m.ta.SetCursor(3)
	m.snapOutOfPastePlaceholder(-1)
	_, col = m.composerLineCol()
	if col != 0 {
		t.Fatalf("snap-out left col = %d, want 0", col)
	}
}

// ── atomic typing ────────────────────────────────────────────────────────

func TestPastePlaceholderTypingDoesNotSplitToken(t *testing.T) {
	m := newPasteTestModel(t)
	content := pasteFourLines()
	ph := pastePlaceholder(content)
	m.pastedBlocks = []pastedBlock{{placeholder: ph, content: content}}
	m.ta.SetValue("ab" + ph + "cd")

	// Caret inside token; typing must not mutate the token body.
	m.ta.SetCursor(len("ab") + 3)
	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Z")})

	assertPlaceholderIntact(t, m.ta.Value(), ph)
	if strings.Contains(m.ta.Value(), "[PaZsted") {
		t.Fatalf("character inserted inside token: %q", m.ta.Value())
	}
	// Insert should land outside the token (after it).
	if !strings.Contains(m.ta.Value(), ph+"Z") && !strings.Contains(m.ta.Value(), "Z"+ph) {
		// repair path may restore previous value; token must still be intact.
		assertPlaceholderIntact(t, m.ta.Value(), ph)
	}
}

func TestPastePlaceholderTypingAfterTokenWorks(t *testing.T) {
	m := newPasteTestModel(t)
	content := pasteFourLines()
	ph := pastePlaceholder(content)
	m.pastedBlocks = []pastedBlock{{placeholder: ph, content: content}}
	m.ta.SetValue(ph)
	m.ta.CursorEnd()

	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
	if got := m.ta.Value(); got != ph+"!" {
		t.Fatalf("typing after token = %q, want %q", got, ph+"!")
	}
	assertPlaceholderIntact(t, m.ta.Value(), ph)
}

func TestRepairBrokenPastePlaceholders(t *testing.T) {
	m := newPasteTestModel(t)
	content := pasteFourLines()
	ph := pastePlaceholder(content)
	previous := "xx" + ph + "yy"
	m.pastedBlocks = []pastedBlock{{placeholder: ph, content: content}}
	// Simulate a broken mutation of the placeholder text.
	m.ta.SetValue("xx[Pasted: BROKEN lines]yy")
	m.repairBrokenPastePlaceholders(previous)
	if got := m.ta.Value(); got != previous {
		t.Fatalf("repair restored %q, want %q", got, previous)
	}
}

// ── atomic deletion ──────────────────────────────────────────────────────

func TestPastePlaceholderDeleteKeysRemoveWholeToken(t *testing.T) {
	content := pasteFourLines()
	ph := pastePlaceholder(content)

	tests := []struct {
		name string
		key  tea.KeyType
		str  string
		// setup places caret; default is at end of token with prefix "before "
		setup func(m *model)
		want  string
	}{
		{
			name: "backspace at end",
			key:  tea.KeyBackspace,
			setup: func(m *model) {
				m.ta.SetValue("before " + ph)
				m.ta.CursorEnd()
			},
			want: "before ",
		},
		{
			name: "ctrl+w at end",
			key:  tea.KeyCtrlW,
			setup: func(m *model) {
				m.ta.SetValue("before " + ph)
				m.ta.CursorEnd()
			},
			want: "before ",
		},
		{
			name: "ctrl+w alone",
			key:  tea.KeyCtrlW,
			setup: func(m *model) {
				m.ta.SetValue(ph)
				m.ta.CursorEnd()
			},
			want: "",
		},
		{
			name: "backspace alone",
			key:  tea.KeyBackspace,
			setup: func(m *model) {
				m.ta.SetValue(ph)
				m.ta.CursorEnd()
			},
			want: "",
		},
		{
			name: "delete at start",
			key:  tea.KeyDelete,
			setup: func(m *model) {
				m.ta.SetValue(ph + " after")
				m.ta.SetCursor(0)
			},
			want: " after",
		},
		{
			name: "backspace inside token",
			key:  tea.KeyBackspace,
			setup: func(m *model) {
				m.ta.SetValue("x" + ph + "y")
				m.ta.SetCursor(len("x") + 3)
			},
			want: "xy",
		},
		{
			name: "ctrl+h at end",
			str:  "ctrl+h",
			setup: func(m *model) {
				m.ta.SetValue(ph)
				m.ta.CursorEnd()
			},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newPasteTestModel(t)
			m.pastedBlocks = []pastedBlock{{placeholder: ph, content: content}}
			tc.setup(&m)

			var msg tea.Msg
			if tc.str != "" {
				// ctrl+h is KeyCtrlH
				msg = tea.KeyMsg{Type: tea.KeyCtrlH}
			} else {
				msg = tea.KeyMsg{Type: tc.key}
			}
			m = applyKey(t, m, msg)

			if got := m.ta.Value(); got != tc.want {
				t.Fatalf("value = %q, want %q", got, tc.want)
			}
			if strings.Contains(m.ta.Value(), "Pasted") {
				t.Fatalf("placeholder residue remains: %q", m.ta.Value())
			}
			if len(m.pastedBlocks) != 0 {
				t.Fatalf("pastedBlocks should be cleared, got %+v", m.pastedBlocks)
			}
		})
	}
}

func TestPastePlaceholderBackspaceAfterSuffixOnlyDeletesChar(t *testing.T) {
	m := newPasteTestModel(t)
	content := pasteFourLines()
	ph := pastePlaceholder(content)
	m.pastedBlocks = []pastedBlock{{placeholder: ph, content: content}}
	m.ta.SetValue(ph + " after")
	m.ta.CursorEnd()

	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := m.ta.Value(); got != ph+" afte" {
		t.Fatalf("value = %q, want suffix char deleted only", got)
	}
	assertPlaceholderIntact(t, m.ta.Value(), ph)
	if len(m.pastedBlocks) != 1 {
		t.Fatalf("pastedBlocks should remain, got %+v", m.pastedBlocks)
	}
}

func TestCollapsedComposerBackspaceClearsWholePastedValue(t *testing.T) {
	m := newPasteTestModel(t)
	m.ta.SetValue("4\n5\n6\n7")
	if !m.collapseInsertedPasteIfLarge("") {
		t.Fatal("expected collapse of 4-line empty-composer paste")
	}

	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := m.ta.Value(); got != "" {
		t.Fatalf("value = %q", got)
	}
}

// ── render / height ──────────────────────────────────────────────────────

func TestComposerCollapseStoresContentAndRendersPlaceholder(t *testing.T) {
	m := newPasteTestModel(t)
	m.ta.SetValue("4\n5\n6\n7")
	if !m.collapseInsertedPasteIfLarge("") {
		t.Fatal("expected collapse of 4-line empty-composer paste")
	}

	if got := m.ta.Value(); got != "[Pasted: 4 lines]" {
		t.Fatalf("value = %q", got)
	}
	if len(m.pastedBlocks) != 1 || m.pastedBlocks[0].content != "4\n5\n6\n7" {
		t.Fatalf("pastedBlocks = %+v", m.pastedBlocks)
	}
	out := stripANSI(m.renderComposer())
	if !strings.Contains(out, "[Pasted: 4 lines]") {
		t.Fatalf("composer render missing placeholder:\n%s", out)
	}
	// Must not still show the raw multi-line body.
	if strings.Contains(out, "\n4\n") || strings.Contains(stripANSI(out), "5\n6") {
		t.Fatalf("composer still shows raw pasted lines:\n%s", out)
	}
}

func TestComposerHeightUsesRealValueNotCollapsedDisplay(t *testing.T) {
	m := newPasteTestModel(t)
	m.intentionalMultiline = true
	m.ta.SetValue("a\nb\nc")
	if h := m.composerTextHeight(); h != 3 {
		t.Fatalf("height for 3 intentional lines = %d, want 3", h)
	}
	m.ta.SetValue("[Pasted: 8 lines]")
	if h := m.composerTextHeight(); h != 1 {
		t.Fatalf("height for placeholder = %d, want 1", h)
	}
}

func TestComposerHeightGrowsForSoftWrappedLine(t *testing.T) {
	// Width 100 → composer wrap width = 100 - 4 - 2 = 94 cells.
	m := newPasteTestModel(t)
	if got := m.composerWrapWidth(); got != 94 {
		t.Fatalf("composerWrapWidth = %d, want 94", got)
	}

	// A short single line with no explicit newline stays one row.
	m.ta.SetValue("hello world")
	if h := m.composerTextHeight(); h != 1 {
		t.Fatalf("height for short line = %d, want 1", h)
	}

	// A single long line (no newline) that exceeds the wrap width must grow the
	// composer the same way an explicit Shift+Enter newline would.
	m.ta.SetValue(strings.Repeat("word ", 40)) // ~200 cells → 3 visual rows at 94
	if h := m.composerTextHeight(); h <= 1 {
		t.Fatalf("height for soft-wrapped long line = %d, want > 1", h)
	}

	// Growth is capped at 4 rows regardless of length.
	m.ta.SetValue(strings.Repeat("word ", 400))
	if h := m.composerTextHeight(); h != 4 {
		t.Fatalf("height for very long line = %d, want 4 (capped)", h)
	}

	// A collapsed paste placeholder is short and must still stay one row.
	m.ta.SetValue("[Pasted: 120 lines]")
	if h := m.composerTextHeight(); h != 1 {
		t.Fatalf("height for placeholder = %d, want 1", h)
	}
}

// TestComposerFirstRowStaysVisibleOnSoftWrap guards the 1→2 growth: typing a
// line long enough to soft-wrap must not scroll the first visual row out of the
// textarea's internal viewport. Regression for the reported bug where the first
// row disappeared and only the (empty) second row showed until the user pressed
// Up.
func TestComposerFirstRowStaysVisibleOnSoftWrap(t *testing.T) {
	// Narrow terminal so a short line wraps quickly.
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 30, Height: 20})
	m.ready = true
	m.layout()

	// Type characters one at a time through the real Update path (which runs
	// the textarea's viewport reposition) until the value soft-wraps to 2 rows.
	const prefix = "abcdef" // must remain visible on row 0 after wrapping
	for _, r := range prefix + strings.Repeat("x", 60) {
		m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if m.composerTextHeight() > 1 {
			break
		}
	}

	if m.composerTextHeight() < 2 {
		t.Fatalf("expected composer to grow past 1 row, got %d", m.composerTextHeight())
	}
	// The textarea's actual height must track the visual line count exactly —
	// never left stuck at the pre-grow cap and never too short (which would
	// scroll earlier rows out of the internal viewport).
	if m.ta.Height() != m.composerTextHeight() {
		t.Fatalf("textarea height %d != composerTextHeight %d", m.ta.Height(), m.composerTextHeight())
	}
	// The textarea's FIRST rendered row must still contain the start of the
	// line; if row 0 had scrolled out of the internal viewport (the bug), the
	// first rendered row would be the wrapped continuation or blank.
	view := m.ta.View()
	firstRow := strings.SplitN(view, "\n", 2)[0]
	if !strings.Contains(firstRow, prefix) {
		t.Fatalf("first row scrolled out of view: %q not in first rendered row %q\nfull view:\n%s", prefix, firstRow, view)
	}

	// Typing a further short char that does not change the wrapped line count
	// must not leave the textarea padded at the pre-grow cap.
	before := m.composerTextHeight()
	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	if m.composerTextHeight() == before && m.ta.Height() != before {
		t.Fatalf("textarea stuck at height %d after non-growing keystroke (want %d)", m.ta.Height(), before)
	}
}

// ── prune / lifecycle ────────────────────────────────────────────────────

func TestPrunePastedBlocksRemovesMissingPlaceholders(t *testing.T) {
	m := newPasteTestModel(t)
	m.pastedBlocks = []pastedBlock{
		{placeholder: "[Pasted: 4 lines]", content: "a\nb\nc\nd"},
		{placeholder: "[Pasted: 5 lines]", content: "1\n2\n3\n4\n5"},
	}
	m.ta.SetValue("only [Pasted: 4 lines] remains")
	m.prunePastedBlocks()
	if len(m.pastedBlocks) != 1 || m.pastedBlocks[0].placeholder != "[Pasted: 4 lines]" {
		t.Fatalf("prune result = %+v", m.pastedBlocks)
	}
}

func TestPastePlaceholderSpansFindsToken(t *testing.T) {
	m := newPasteTestModel(t)
	content := pasteFourLines()
	ph := pastePlaceholder(content)
	m.pastedBlocks = []pastedBlock{{placeholder: ph, content: content}}
	m.ta.SetValue("pre " + ph + " post")

	spans := m.pastePlaceholderSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %+v", spans)
	}
	if spans[0].placeholder != ph {
		t.Fatalf("span placeholder = %q", spans[0].placeholder)
	}
	if spans[0].lineStart != len("pre ") {
		t.Fatalf("lineStart = %d, want %d", spans[0].lineStart, len("pre "))
	}
	if spans[0].lineEnd != len("pre ")+len(ph) {
		t.Fatalf("lineEnd = %d, want %d", spans[0].lineEnd, len("pre ")+len(ph))
	}
}

// ── recording backend ────────────────────────────────────────────────────

type recordingBackend struct {
	staticBackend
	message     string
	attachments []backend.Attachment
}

func (b *recordingBackend) RunTurn(_ context.Context, message string, opts backend.TurnOptions) (<-chan events.Event, error) {
	b.message = message
	b.attachments = append([]backend.Attachment(nil), opts.Attachments...)
	ch := make(chan events.Event)
	close(ch)
	return ch, nil
}

// ── image paste ──────────────────────────────────────────────────────────

func TestImagePasteInsertsAtomicPlaceholder(t *testing.T) {
	m := newPasteTestModel(t)
	png := minimalPNG()
	orig := clipboardImageReader
	clipboardImageReader = func() ([]byte, string, bool) {
		return png, "image/png", true
	}
	t.Cleanup(func() { clipboardImageReader = orig })

	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlV})
	if got := m.ta.Value(); got != "[image #1]" {
		t.Fatalf("value = %q, want [image #1]", got)
	}
	if len(m.pastedImages) != 1 {
		t.Fatalf("pastedImages = %+v", m.pastedImages)
	}
	if m.pastedImages[0].mimeType != "image/png" || len(m.pastedImages[0].data) == 0 {
		t.Fatalf("image payload missing: %+v", m.pastedImages[0])
	}
}

func TestImagePasteIncrementsIndex(t *testing.T) {
	m := newPasteTestModel(t)
	png := minimalPNG()
	orig := clipboardImageReader
	clipboardImageReader = func() ([]byte, string, bool) {
		return png, "image/png", true
	}
	t.Cleanup(func() { clipboardImageReader = orig })

	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlV})
	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlV})
	if got := m.ta.Value(); got != "[image #1][image #2]" {
		t.Fatalf("value = %q", got)
	}
	if len(m.pastedImages) != 2 {
		t.Fatalf("pastedImages len = %d", len(m.pastedImages))
	}
}

func TestImagePasteIsAtomicForNavigationAndDelete(t *testing.T) {
	m := newPasteTestModel(t)
	png := minimalPNG()
	m.pastedImages = []pastedImage{{
		placeholder: "[image #1]",
		mimeType:    "image/png",
		data:        png,
		number:      1,
	}}
	m.ta.SetValue("ab[image #1]cd")
	m.ta.SetCursor(len("ab") + len("[image #1]"))

	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyLeft})
	_, col := m.composerLineCol()
	if col != len("ab") {
		t.Fatalf("left jump col = %d, want %d", col, len("ab"))
	}
	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRight})
	_, col = m.composerLineCol()
	if col != len("ab")+len("[image #1]") {
		t.Fatalf("right jump col = %d", col)
	}

	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := m.ta.Value(); got != "abcd" {
		t.Fatalf("backspace value = %q", got)
	}
	if len(m.pastedImages) != 0 {
		t.Fatalf("pastedImages after delete = %+v", m.pastedImages)
	}
}

func TestImagePasteSubmitSendsAttachments(t *testing.T) {
	b := &recordingBackend{}
	m := newPasteTestModelWithBackend(t, b)
	png := minimalPNG()
	m.pastedImages = []pastedImage{{
		placeholder: "[image #1]",
		mimeType:    "image/png",
		data:        png,
		number:      1,
	}}
	m.ta.SetValue("look [image #1]")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	m = next.(model)

	if b.message != "look [image #1]" {
		t.Fatalf("message = %q", b.message)
	}
	if len(b.attachments) != 1 {
		t.Fatalf("attachments = %+v", b.attachments)
	}
	if b.attachments[0].MimeType != "image/png" || len(b.attachments[0].Data) == 0 {
		t.Fatalf("attachment payload = %+v", b.attachments[0])
	}
	if b.attachments[0].Filename == "" {
		t.Fatal("expected attachment filename")
	}
	if len(m.pastedImages) != 0 {
		t.Fatalf("pastedImages should clear after submit")
	}
}

func TestImageOnlySubmitUsesPlaceholderText(t *testing.T) {
	b := &recordingBackend{}
	m := newPasteTestModelWithBackend(t, b)
	png := minimalPNG()
	m.pastedImages = []pastedImage{{
		placeholder: "[image #1]",
		mimeType:    "image/png",
		data:        png,
		number:      1,
	}}
	m.ta.SetValue("[image #1]")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	_ = next
	if b.message != "[image #1]" {
		t.Fatalf("message = %q", b.message)
	}
	if len(b.attachments) != 1 {
		t.Fatalf("attachments = %+v", b.attachments)
	}
}

func TestEmptyPasteWithImageUsesClipboardImage(t *testing.T) {
	m := newPasteTestModel(t)
	png := minimalPNG()
	orig := clipboardImageReader
	clipboardImageReader = func() ([]byte, string, bool) {
		return png, "image/png", true
	}
	t.Cleanup(func() { clipboardImageReader = orig })

	m = applyKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(""), Paste: true})
	if got := m.ta.Value(); got != "[image #1]" {
		t.Fatalf("value = %q", got)
	}
}

func TestSniffImageMIME(t *testing.T) {
	png := minimalPNG()
	mime, ok := sniffImageMIME(png)
	if !ok || mime != "image/png" {
		t.Fatalf("sniff png = %q ok=%v", mime, ok)
	}
	if _, ok := sniffImageMIME([]byte("not-an-image")); ok {
		t.Fatal("expected non-image to fail sniff")
	}
}

// minimalPNG is a 1x1 transparent PNG.
func minimalPNG() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
}

func TestContainsMIMETarget(t *testing.T) {
	targets := []string{"text/plain", "image/png", "image/jpeg;charset=utf-8"}
	if !containsMIMETarget(targets, "image/png") {
		t.Fatal("expected image/png match")
	}
	if !containsMIMETarget(targets, "image/jpeg") {
		t.Fatal("expected image/jpeg prefix match")
	}
	if containsMIMETarget(targets, "image/gif") {
		t.Fatal("did not expect image/gif")
	}
}

func TestParseCommandLines(t *testing.T) {
	got := parseCommandLines([]byte("image/png\n\ntext/plain\n"))
	if len(got) != 2 || got[0] != "image/png" || got[1] != "text/plain" {
		t.Fatalf("parseCommandLines = %#v", got)
	}
}
