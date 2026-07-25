package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/SAP/astonish/pkg/tui/render"
)

// hitRegion maps a range of rendered transcript lines to a transcript item.
type hitRegion struct {
	start   int // inclusive content line (0-based in full transcript string)
	end     int // exclusive
	itemIdx int
	kind    events.ItemKind
}

// Config configures the terminal chat app.
type Config struct {
	Backend backend.Backend
	// Width/Height optional initial size; 0 means wait for WindowSizeMsg.
	Width  int
	Height int
}

// Run starts the fullscreen TUI and blocks until exit.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Backend == nil {
		return fmt.Errorf("tui: backend is required")
	}
	if err := cfg.Backend.Open(ctx); err != nil {
		return err
	}
	defer cfg.Backend.Close()

	m := newModel(ctx, cfg)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

// model is the root bubbletea model.
type model struct {
	ctx      context.Context
	cancel   context.CancelFunc
	backend  backend.Backend
	info     backend.Info
	theme    Theme
	tr       *events.Transcript
	vp       viewport.Model
	ta       textarea.Model
	spin     spinner.Model
	width    int
	height   int
	ready    bool
	err      string
	quitting bool
	// history of sent prompts for Up/Down.
	history    []string
	historyIdx int // -1 = not browsing
	// turnCancel cancels the in-flight RunTurn.
	turnCancel context.CancelFunc
	// eventCh is drained via tea.Cmds while a turn is active.
	eventCh <-chan events.Event

	// hitRegions maps rendered transcript lines → items (for mouse expand).
	hitRegions []hitRegion
	// double-click detection for expanding user bubbles.
	lastClickAt time.Time
	lastClickY  int
	lastClickX  int
}

func newModel(parent context.Context, cfg Config) model {
	ctx, cancel := context.WithCancel(parent)
	info := cfg.Backend.Info()
	th := DefaultTheme()

	ta := textarea.New()
	ta.Placeholder = "Message Astonish…"
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	// First line: prompt; continuation lines: spaces (avoids stacked ❯ clutter).
	ta.SetPromptFunc(2, func(lineIdx int) string {
		if lineIdx == 0 {
			return "❯ "
		}
		return "  "
	})
	// Enter is reserved for send at the app level; newlines use Shift+Enter / ctrl+j / alt+enter.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "ctrl+j", "alt+enter", "ctrl+m"),
		key.WithHelp("shift+enter", "newline"),
	)
	ta.SetHeight(1)
	th.ApplyTextareaStyles(&ta)
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))

	tr := events.NewTranscript()
	tr.SessionID = info.SessionID
	tr.Provider = info.Provider
	tr.Model = info.Model
	for _, n := range info.Notices {
		tr.Apply(events.NewSystem(n))
	}
	if info.IsResumed {
		tr.Apply(events.NewSystem("Welcome back — session resumed."))
	} else {
		tr.Apply(events.NewSystem("Hey! I'm Astonish, your AI assistant. What can I help you with today?"))
	}

	m := model{
		ctx:        ctx,
		cancel:     cancel,
		backend:    cfg.Backend,
		info:       info,
		theme:      th,
		tr:         tr,
		ta:         ta,
		spin:       sp,
		width:      cfg.Width,
		height:     cfg.Height,
		historyIdx: -1,
	}
	return m
}

// tea messages
type eventMsg events.Event
type turnDoneMsg struct{}
type turnErrMsg struct{ err error }

func (m model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spin.Tick)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		m.ready = true
		m.refreshViewport()
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		// Global keys
		switch msg.String() {
		case "ctrl+c":
			if m.tr.Streaming && m.turnCancel != nil {
				m.turnCancel()
				m.turnCancel = nil
				m.tr.Streaming = false
				m.tr.Status = ""
				m.tr.Apply(events.NewSystem("Turn cancelled."))
				m.refreshViewport()
				return m, nil
			}
			m.quitting = true
			m.cancel()
			return m, tea.Quit
		case "ctrl+d":
			if m.ta.Value() == "" {
				m.quitting = true
				m.cancel()
				return m, tea.Quit
			}
		case "ctrl+o":
			m.tr.ToggleLastActivity()
			m.refreshViewport()
			return m, nil
		case "ctrl+l":
			// Sessions overlay — phase 5; show system hint for now.
			m.tr.Apply(events.NewSystem("Sessions picker coming soon. Use: astonish chat --resume <id>"))
			m.refreshViewport()
			return m, nil
		case "ctrl+n":
			if !m.tr.Streaming {
				m.tr.Apply(events.NewSystem("Use /new for a fresh session (slash commands expand in a later PR)."))
				m.refreshViewport()
			}
			return m, nil
		}

		// While streaming, only allow cancel / scroll keys through textarea limited.
		if m.tr.Streaming {
			switch msg.String() {
			case "pgup", "pgdown", "up", "down":
				var cmd tea.Cmd
				m.vp, cmd = m.vp.Update(msg)
				return m, cmd
			default:
				// Block sending new messages mid-turn except approval path handled below.
				if !m.tr.Awaiting {
					return m, nil
				}
			}
		}

		// Enter sends; Shift+Enter / Alt+Enter / Ctrl+J insert a newline.
		// Match on String() so "shift+enter" is not treated as plain send.
		switch msg.String() {
		case "enter":
			return m.submit()
		case "shift+enter", "alt+enter", "ctrl+j", "ctrl+m":
			return m.insertNewline()
		}

	case eventMsg:
		ev := events.Event(msg)
		m.tr.Apply(ev)
		// Keep info in sync
		if ev.Kind == events.KindSession && ev.SessionID != "" {
			m.info.SessionID = ev.SessionID
		}
		if ev.Kind == events.KindModelChanged {
			if ev.Provider != "" {
				m.info.Provider = ev.Provider
			}
			if ev.Model != "" {
				m.info.Model = ev.Model
			}
		}
		m.refreshViewport()
		if m.eventCh != nil {
			cmds = append(cmds, waitEvent(m.eventCh))
		}
		if m.tr.Streaming || m.tr.Status != "" {
			cmds = append(cmds, m.spin.Tick)
		}
		return m, tea.Batch(cmds...)

	case turnDoneMsg:
		m.eventCh = nil
		m.turnCancel = nil
		if m.tr.Streaming {
			m.tr.Apply(events.NewDone())
		}
		m.refreshViewport()
		return m, nil

	case turnErrMsg:
		m.eventCh = nil
		m.turnCancel = nil
		m.tr.Apply(events.NewError(msg.err.Error()))
		m.refreshViewport()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if m.tr.Streaming || m.tr.Status != "" {
			return m, cmd
		}
		return m, nil
	}

	// Delegate to textarea / viewport when ready.
	if m.ready {
		prevH := m.composerTextHeight()
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		cmds = append(cmds, cmd)
		// Grow/shrink composer when the user adds or removes newlines.
		if m.composerTextHeight() != prevH {
			m.layout()
			m.refreshViewport()
		}
	}
	return m, tea.Batch(cmds...)
}

// insertNewline inserts a line break in the composer (Shift+Enter / Ctrl+J).
func (m model) insertNewline() (tea.Model, tea.Cmd) {
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil
	}
	prevH := m.composerTextHeight()
	m.ta.InsertString("\n")
	if m.ready && m.composerTextHeight() != prevH {
		m.layout()
		m.refreshViewport()
	}
	return m, nil
}

func (m model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.ta.Value())
	if text == "" {
		return m, nil
	}
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil
	}

	// Local slash commands (minimal set for PR1).
	if strings.HasPrefix(text, "/") {
		return m.handleSlash(text)
	}

	m.history = append(m.history, text)
	m.historyIdx = -1
	m.ta.Reset()
	if m.ready {
		m.layout()
	}

	if m.tr.Awaiting {
		m.tr.ClearApproval()
	}

	m.tr.Apply(events.NewUser(text))
	m.refreshViewport()

	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel

	ch, err := m.backend.RunTurn(turnCtx, text)
	if err != nil {
		cancel()
		m.turnCancel = nil
		m.tr.Apply(events.NewError(err.Error()))
		m.refreshViewport()
		return m, nil
	}
	m.eventCh = ch
	return m, waitEvent(ch)
}

func (m model) handleSlash(text string) (tea.Model, tea.Cmd) {
	m.ta.Reset()
	switch {
	case text == "/help" || text == "/?":
		m.tr.Apply(events.NewSystem(helpText()))
	case text == "/status":
		m.tr.Apply(events.NewSystem(m.statusText()))
	case text == "/exit" || text == "/quit" || text == "/q":
		m.quitting = true
		m.cancel()
		return m, tea.Quit
	case text == "/new":
		m.tr.Apply(events.NewSystem("Fresh session requires restart for now: exit and run astonish chat again. Full /new lands in a later PR."))
	default:
		// Pass through to backend as a normal message so server/local slash handlers can run later.
		m.history = append(m.history, text)
		m.tr.Apply(events.NewUser(text))
		m.refreshViewport()
		turnCtx, cancel := context.WithCancel(m.ctx)
		m.turnCancel = cancel
		ch, err := m.backend.RunTurn(turnCtx, text)
		if err != nil {
			cancel()
			m.turnCancel = nil
			m.tr.Apply(events.NewError(err.Error()))
			m.refreshViewport()
			return m, nil
		}
		m.eventCh = ch
		return m, waitEvent(ch)
	}
	m.refreshViewport()
	return m, nil
}

func helpText() string {
	return strings.TrimSpace(`
Commands:
  /help       Show this help
  /status     Show session / provider / model
  /new        New session (partial in PR1)
  /exit       Quit (/quit, /q)
  ctrl+c      Cancel turn or quit
  ctrl+o      Expand/collapse last tool activity
  Enter       Send message
  shift+enter Newline in input (also ctrl+j / alt+enter)
`)
}

func (m model) statusText() string {
	info := m.info
	var b strings.Builder
	fmt.Fprintf(&b, "Mode: %s\n", first(info.Mode, "platform"))
	if info.ServerURL != "" {
		fmt.Fprintf(&b, "Server: %s\n", info.ServerURL)
	}
	if info.Org != "" {
		fmt.Fprintf(&b, "Org: %s  Team: %s\n", info.Org, info.Team)
	}
	fmt.Fprintf(&b, "Session: %s\n", first(info.SessionID, "(none)"))
	fmt.Fprintf(&b, "Provider: %s  Model: %s\n", first(info.Provider, "-"), first(info.Model, "-"))
	if m.tr.LastUsage != nil {
		fmt.Fprintf(&b, "Tokens: in=%d out=%d total=%d\n", m.tr.LastUsage.Input, m.tr.LastUsage.Output, m.tr.LastUsage.Total)
	}
	return strings.TrimSpace(b.String())
}

func waitEvent(ch <-chan events.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return turnDoneMsg{}
		}
		return eventMsg(ev)
	}
}

func (m *model) layout() {
	// Chrome: header(1) + sep + status(1) + composer(border+ta) + meta(1) + hints(1) + seps
	headerH := 1
	statusH := 1
	metaH := 1
	hintsH := 1
	seps := 2
	// Composer: 1 content line + 2 border rows; grow with multiline input.
	taH := m.composerTextHeight()
	composerH := taH + 2 // rounded border top/bottom
	chrome := headerH + statusH + composerH + metaH + hintsH + seps
	vh := m.height - chrome
	if vh < 5 {
		vh = 5
	}
	m.vp = viewport.New(m.width, vh)
	content, hits := m.renderTranscript()
	m.hitRegions = hits
	m.vp.SetContent(content)

	// Composer width: terminal width minus border (2) and padding (2).
	innerW := m.width - 4
	if innerW < 20 {
		innerW = 20
	}
	m.ta.SetWidth(innerW)
	m.ta.SetHeight(taH)
}

// composerTextHeight returns the textarea height: 1 by default, up to 4 when
// the user has entered multiple lines.
func (m model) composerTextHeight() int {
	lines := strings.Count(m.ta.Value(), "\n") + 1
	if lines < 1 {
		lines = 1
	}
	if lines > 4 {
		lines = 4
	}
	return lines
}

func (m *model) refreshViewport() {
	if !m.ready {
		return
	}
	atBottom := m.vp.AtBottom()
	content, hits := m.renderTranscript()
	m.hitRegions = hits
	m.vp.SetContent(content)
	if atBottom || m.tr.Streaming {
		m.vp.GotoBottom()
	}
}

// viewportTopY is the screen row where the transcript viewport starts.
func (m model) viewportTopY() int {
	// header (1) + separator (1)
	return 2
}

func (m model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Wheel: let viewport scroll.
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}

	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress {
		return m, nil
	}

	// Double-click detection.
	now := time.Now()
	isDouble := now.Sub(m.lastClickAt) < doubleClickWindowMS*time.Millisecond &&
		abs(msg.Y-m.lastClickY) <= 1 &&
		abs(msg.X-m.lastClickX) <= 2
	m.lastClickAt = now
	m.lastClickY = msg.Y
	m.lastClickX = msg.X

	if !isDouble {
		return m, nil
	}

	// Map screen Y → content line inside viewport.
	top := m.viewportTopY()
	if msg.Y < top || msg.Y >= top+m.vp.Height {
		return m, nil
	}
	contentLine := m.vp.YOffset + (msg.Y - top)
	idx, ok := m.itemAtLine(contentLine)
	if !ok {
		return m, nil
	}
	it := m.tr.Items[idx]
	if it.Kind != events.ItemUser && it.Kind != events.ItemActivity {
		return m, nil
	}
	m.tr.ToggleExpand(idx)
	m.refreshViewport()
	return m, nil
}

func (m model) itemAtLine(line int) (int, bool) {
	for _, r := range m.hitRegions {
		if line >= r.start && line < r.end {
			return r.itemIdx, true
		}
	}
	return -1, false
}

func (m *model) renderTranscript() (string, []hitRegion) {
	var b strings.Builder
	var hits []hitRegion
	th := m.theme
	cw := contentWidth(m.width)
	lineNo := 0

	appendBlock := func(itemIdx int, kind events.ItemKind, block string) {
		if block == "" {
			return
		}
		// Trailing newline normalization: count lines before padding.
		block = strings.TrimRight(block, "\n")
		padded := padBlock(block)
		start := lineNo
		n := lineCount(padded)
		b.WriteString(padded)
		b.WriteString("\n\n") // vertical gap between messages
		lineNo += n + 2       // block lines + blank separator line
		hits = append(hits, hitRegion{start: start, end: start + n, itemIdx: itemIdx, kind: kind})
	}

	for i, it := range m.tr.Items {
		switch it.Kind {
		case events.ItemUser:
			appendBlock(i, it.Kind, m.renderUserBubble(it.Content, it.Expanded, cw))
		case events.ItemAgent:
			content := strings.TrimRight(it.Content, "\n")
			if content == "" {
				continue
			}
			// Provisional = interstitial during tool loop (Studio sticky agent).
			// Show as Thinking (muted, replaceable); finalize to full markdown on Done.
			if it.Provisional {
				appendBlock(i, it.Kind, m.renderThinkingBubble(content, cw))
				continue
			}
			md := render.Markdown(content, cw, th.RenderStyles())
			if md == "" {
				md = th.Agent.Width(cw).Render(content)
			}
			appendBlock(i, it.Kind, md)
		case events.ItemThinking:
			appendBlock(i, it.Kind, m.renderThinkingBubble(it.Content, cw))
		case events.ItemActivity:
			appendBlock(i, it.Kind, m.renderActivity(it, cw))
		case events.ItemSystem:
			appendBlock(i, it.Kind, th.System.Width(cw).Render(it.Content))
		case events.ItemError:
			appendBlock(i, it.Kind, th.Error.Width(cw).Render(it.Content))
		case events.ItemApproval:
			var ab strings.Builder
			ab.WriteString(th.Approval.Width(cw).Render("⚠ " + it.Content))
			if len(it.Options) > 0 {
				ab.WriteByte('\n')
				ab.WriteString(th.Muted.Width(cw).Render("Options: " + strings.Join(it.Options, " / ")))
			}
			ab.WriteByte('\n')
			ab.WriteString(th.Muted.Width(cw).Render("Type Yes or No (or an option) and press Enter."))
			appendBlock(i, it.Kind, ab.String())
		case events.ItemArtifact:
			appendBlock(i, it.Kind, th.Muted.Width(cw).Render("📄 "+first(it.Path, it.Content)))
		}
	}
	return b.String(), hits
}

// renderThinkingBubble is the mid-turn sticky agent slot (replaces between tools).
func (m model) renderThinkingBubble(content string, width int) string {
	th := m.theme
	label := th.Muted.Italic(true).Render("Thinking")
	body := strings.TrimSpace(content)
	if body == "" {
		return label + th.Muted.Render("…")
	}
	// Single compact preview line + full muted body (still one bubble, not final response).
	preview := body
	// Soft-wrap muted text; keep visual distinction from final agent markdown.
	wrapped := th.Muted.Width(width).Render(preview)
	return label + "\n" + wrapped
}

// renderActivity builds collapsed summary (+N/−M) and expanded tool/diff detail.
func (m model) renderActivity(it events.Item, width int) string {
	th := m.theme
	rs := th.RenderStyles()
	steps := make([]render.ToolStep, 0, len(it.Steps))
	for _, s := range it.Steps {
		steps = append(steps, render.ToolStep{
			Name:   s.Name,
			Args:   s.Args,
			Result: s.Result,
			Status: s.Status,
		})
	}
	streaming := false
	for _, s := range steps {
		if s.Status == "running" {
			streaming = true
			break
		}
	}
	summary := render.ActivitySummary(steps, streaming)
	if summary == "" {
		summary = it.Summary
	}
	if summary == "" {
		summary = "Tools"
	}
	stats := render.StatsFromSteps(steps)
	statsStr := render.FormatStats(stats, rs)

	lead := "▸ "
	if it.Expanded {
		lead = "▾ "
	}
	head := th.Activity.Render(lead+summary)
	if statsStr != "" {
		// Right-align metrics on the same row when width allows.
		pad := width - lipgloss.Width(head) - lipgloss.Width(statsStr) - 1
		if pad < 1 {
			pad = 1
		}
		head = head + strings.Repeat(" ", pad) + statsStr
	}

	if !it.Expanded {
		return head
	}

	var b strings.Builder
	b.WriteString(head)
	for _, s := range it.Steps {
		b.WriteByte('\n')
		status := s.Status
		line := fmt.Sprintf("  %s  [%s]", s.Name, status)
		b.WriteString(th.Muted.Width(width).Render(line))
		// Show file diffs for edit/write tools.
		if d := render.DiffFromToolArgs(s.Name, s.Args, width, true, rs); d != "" {
			b.WriteByte('\n')
			b.WriteString(d)
		}
	}
	return b.String()
}

// renderUserBubble paints a soft gray user band with vertical breathing room,
// height-capped unless expanded. Expand/collapse cue is right-aligned + brand-colored.
func (m model) renderUserBubble(content string, expanded bool, width int) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	th := m.theme
	// Horizontal pad is 2+2 inside the bubble; wrap body to the inner text width.
	inner := width - 4
	if inner < 8 {
		inner = width
		if inner < 8 {
			inner = 8
		}
	}

	bodyStyle := th.UserBubble.Width(width)
	// Empty line with the same background = top/bottom padding inside the band.
	padLine := lipgloss.NewStyle().
		Background(th.UserBubble.GetBackground()).
		Width(width).
		Render(" ")

	// How many body lines would the full message use?
	fullBody := wrapPlain(content, inner)
	fullBodyLines := lineCount(fullBody)

	var body string
	var hint string
	switch {
	case !expanded && fullBodyLines > userBubbleMaxLines:
		// Cap body lines; expand cue is a separate right-aligned row (not mixed into body).
		cut, _ := truncateVisualLines(fullBody, userBubbleMaxLines, "")
		cut = strings.TrimRight(cut, "\n")
		body = bodyStyle.Render(cut)
		hint = th.UserExpandHint.Width(width).Render("… double-click to expand")
	case expanded && fullBodyLines > userBubbleMaxLines:
		body = bodyStyle.Render(content)
		hint = th.UserExpandHint.Width(width).Render("… double-click to collapse")
	default:
		body = bodyStyle.Render(content)
	}

	parts := []string{padLine, body}
	if hint != "" {
		parts = append(parts, hint)
	}
	parts = append(parts, padLine)
	return strings.Join(parts, "\n")
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (m model) View() string {
	if m.quitting {
		return ""
	}
	if !m.ready {
		return "\n  Initializing Astonish…\n"
	}

	th := m.theme
	sep := th.Border.Render(strings.Repeat("─", max(1, m.width)))

	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderHeader(),
		sep,
		m.vp.View(),
		sep,
		m.renderLiveStatus(),
		m.renderComposer(),
		m.renderFooterMeta(),
		m.renderHints(),
	)
}

func (m model) renderHeader() string {
	th := m.theme
	mode := first(m.info.Mode, "platform")
	sid := m.info.SessionID
	if len(sid) > 12 {
		sid = sid[:12]
	}
	left := th.Header.Render("Astonish") + th.Muted.Render(" · "+mode)
	if sid != "" {
		left += th.Muted.Render(" · "+sid)
	}
	// URL only in header; model lives in footer meta (Grok-style).
	right := ""
	if m.info.ServerURL != "" {
		right = th.Muted.Render(m.info.ServerURL)
	}
	if right == "" {
		return left
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// renderLiveStatus shows spinner text while a turn is active; otherwise a blank
// spacer so layout does not jump.
func (m model) renderLiveStatus() string {
	th := m.theme
	if m.err != "" {
		return th.Error.Render(m.err)
	}
	if m.tr.Status != "" || m.tr.Streaming {
		return th.Status.Render(m.spin.View() + " " + first(m.tr.Status, "Working…"))
	}
	// Keep one row so chrome height is stable.
	return " "
}

// renderComposer draws the bordered input box (Grok-style).
func (m model) renderComposer() string {
	th := m.theme
	box := th.InputBorderFocus
	if !m.ta.Focused() {
		box = th.InputBorder
	}
	// lipgloss Width is the content width; border adds 2 columns outside.
	// Pad content so total ≈ terminal width.
	w := m.width - 2
	if w < 10 {
		w = 10
	}
	return box.Width(w).Render(m.ta.View())
}

// renderFooterMeta shows provider/model and approval mode (Grok footer strip).
func (m model) renderFooterMeta() string {
	th := m.theme
	provider := first(m.info.Provider, m.tr.Provider)
	modelName := first(m.info.Model, m.tr.Model)
	left := "default model"
	if provider != "" || modelName != "" {
		left = fmt.Sprintf("%s / %s", first(provider, "—"), first(modelName, "—"))
	}
	right := "normal"
	if m.info.AutoApprove {
		right = "auto-approve"
	}
	leftR := th.FooterMeta.Render(left)
	rightR := th.FooterMeta.Render(right)
	gap := m.width - lipgloss.Width(leftR) - lipgloss.Width(rightR)
	if gap < 1 {
		gap = 1
	}
	return leftR + strings.Repeat(" ", gap) + rightR
}

// renderHints is the keybinding help line under the composer.
func (m model) renderHints() string {
	th := m.theme
	full := "Enter send  ·  shift+enter newline  ·  ctrl+o tools  ·  /help  ·  ctrl+c quit"
	short := "Enter send  ·  shift+enter ↵  ·  /help  ·  ctrl+c quit"
	line := full
	if m.width > 0 && lipgloss.Width(full) > m.width {
		line = short
	}
	if m.width > 0 && lipgloss.Width(line) > m.width {
		// Last resort: hard truncate.
		runes := []rune(line)
		if m.width > 1 && len(runes) > m.width-1 {
			line = string(runes[:m.width-1]) + "…"
		}
	}
	return th.Hint.Render(line)
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
