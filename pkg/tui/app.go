package tui

import (
	"context"
	"fmt"
	"os"
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

type selectionPoint struct {
	line int
	col  int
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
	// transcriptPlainLines is the visible transcript without ANSI styling; used
	// for drag-to-copy selection in Bubble Tea mouse mode.
	transcriptPlainLines []string
	// double-click detection for expanding user bubbles.
	lastClickAt time.Time
	lastClickY  int
	lastClickX  int
	// drag selection state. Native terminal selection is unavailable while mouse
	// reporting is enabled, so the app copies selected transcript text itself.
	selecting      bool
	selectionStart selectionPoint
	selectionEnd   selectionPoint
	selectionMoved bool
	copyStatus     string
	copiedUntil    time.Time
	clickIsDouble  bool

	// overlays
	sessions sessionsState
	// slash command completion popup (active when composer starts with /)
	slash slashCompletion
	// @file completion popup (active while typing a trailing @token)
	files fileCompletion
	// planMode asks the platform agent for plans only, without execution.
	planMode bool
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
	if info.Usage != nil {
		tr.LastUsage = &events.Usage{Input: info.Usage.Input, Output: info.Usage.Output, Total: info.Usage.Total}
	}
	for _, n := range info.Notices {
		tr.Apply(events.NewSystem(n))
	}
	// Resumed sessions load history asynchronously in Init (historyLoadedMsg).

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
	cmds := []tea.Cmd{textarea.Blink, m.spin.Tick}
	if m.info.IsResumed && m.info.SessionID != "" {
		cmds = append(cmds, m.loadInitialHistoryCmd())
	}
	return tea.Batch(cmds...)
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
		// Sessions overlay captures keys first.
		if m.sessions.open {
			return m.handleSessionsKey(msg)
		}

		// Approval overlay: y/n before other handling.
		if m.tr.Awaiting {
			if next, cmd, handled := m.handleApprovalKey(msg); handled {
				return next, cmd
			}
		}

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
			return m.openSessionsPicker()
		case "ctrl+n":
			return m.startNewSession()
		}

		// While streaming, only allow cancel / scroll keys through textarea limited.
		if m.tr.Streaming {
			switch msg.String() {
			case "pgup", "pgdown", "up", "down":
				var cmd tea.Cmd
				m.vp, cmd = m.vp.Update(msg)
				return m, cmd
			default:
				// Block sending new messages mid-turn except approval path (handled above).
				if !m.tr.Awaiting {
					return m, nil
				}
			}
		}

		// Completion navigation (before enter-to-send). Slash wins at start of input;
		// @file wins everywhere else while typing a trailing @token.
		if m.slash.active && len(m.slash.matches) > 0 {
			if next, cmd, handled := m.handleSlashCompletionKey(msg); handled {
				return next, cmd
			}
		}
		if m.files.active && len(m.files.matches) > 0 {
			if next, cmd, handled := m.handleFileCompletionKey(msg); handled {
				return next, cmd
			}
		}

		if msg.String() == "shift+tab" {
			m.togglePlanMode()
			m.refreshViewport()
			return m, nil
		}

		// Enter sends; Shift+Enter / Alt+Enter / Ctrl+J insert a newline.
		// Match on String() so "shift+enter" is not treated as plain send.
		switch msg.String() {
		case "enter":
			return m.submit()
		case "shift+enter", "alt+enter", "ctrl+j", "ctrl+m":
			return m.insertNewline()
		}

	case sessionsLoadedMsg:
		m.sessions.loading = false
		if msg.err != nil {
			m.sessions.err = msg.err.Error()
		} else {
			m.sessions.items = msg.items
			m.sessions.cursor = 0
			// Highlight current session if present.
			for i, s := range msg.items {
				if s.ID == m.info.SessionID {
					m.sessions.cursor = i
					break
				}
			}
		}
		return m, nil

	case historyLoadedMsg:
		return m.applyHistory(msg)

	case sessionDeletedMsg:
		return m.applySessionDeleted(msg)

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

	case networkGrantApprovedMsg:
		if msg.err != nil {
			if m.turnCancel != nil {
				m.turnCancel()
			}
			m.turnCancel = nil
			m.tr.Streaming = false
			m.tr.Status = ""
			m.tr.Apply(events.NewError("Network approval failed: " + msg.err.Error()))
			m.refreshViewport()
			return m, nil
		}
		m.tr.Apply(events.NewSystem("Network access granted for " + msg.label + ". Retrying blocked command…"))
		m.tr.Streaming = true
		m.tr.Status = "Thinking…"
		m.eventCh = msg.ch
		m.refreshViewport()
		if msg.ch == nil {
			return m, nil
		}
		return m, waitEvent(msg.ch)

	case networkGrantDeniedMsg:
		if msg.err != nil {
			m.tr.Apply(events.NewError("Network denial failed: " + msg.err.Error()))
			m.refreshViewport()
		}
		return m, nil

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
		// Keep completion popups in sync with composer value after typing.
		m.syncSlashCompletion()
		m.syncFileCompletion()
	}
	return m, tea.Batch(cmds...)
}

// syncSlashCompletion opens/filters/closes the / command palette from the input.
func (m *model) syncSlashCompletion() {
	val := m.ta.Value()
	ok, query := parseSlashInput(val)
	if !ok {
		m.slash = slashCompletion{}
		return
	}
	// If user already typed past the command name with a space, hide popup
	// (arguments mode) — still allow exact bare command completion.
	if strings.Contains(query, " ") {
		m.slash = slashCompletion{}
		return
	}
	matches := filterSlashCommands(query)
	cursor := m.slash.cursor
	if cursor >= len(matches) {
		cursor = len(matches) - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	m.slash = slashCompletion{
		active:  len(matches) > 0,
		query:   query,
		matches: matches,
		cursor:  cursor,
	}
	if m.slash.active {
		m.files = fileCompletion{}
	}
}

func (m *model) syncFileCompletion() {
	if m.slash.active {
		m.files = fileCompletion{}
		return
	}
	ok, query := parseFileMentionInput(m.ta.Value())
	if !ok {
		m.files = fileCompletion{}
		return
	}
	matches := listFileCandidates(".", query)
	cursor := m.files.cursor
	if cursor >= len(matches) {
		cursor = len(matches) - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	m.files = fileCompletion{
		active:  len(matches) > 0,
		query:   query,
		matches: matches,
		cursor:  cursor,
	}
}

func (m model) handleSlashCompletionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "up":
		if m.slash.cursor > 0 {
			m.slash.cursor--
		}
		return m, nil, true
	case "down", "tab":
		if m.slash.cursor < len(m.slash.matches)-1 {
			m.slash.cursor++
		} else if msg.String() == "tab" {
			m.slash.cursor = 0
		}
		return m, nil, true
	case "shift+tab":
		if m.slash.cursor > 0 {
			m.slash.cursor--
		} else {
			m.slash.cursor = len(m.slash.matches) - 1
		}
		return m, nil, true
	case "esc":
		m.slash = slashCompletion{}
		return m, nil, true
	case "enter":
		if cmd, ok := m.slash.selectedCommand(); ok {
			m.ta.SetValue(completionValue(cmd))
			m.ta.CursorEnd()
			m.slash = slashCompletion{}
			next, teaCmd := m.submit()
			return next, teaCmd, true
		}
	case "ctrl+y":
		if cmd, ok := m.slash.selectedCommand(); ok {
			m.ta.SetValue(completionValue(cmd))
			m.ta.CursorEnd()
			m.syncSlashCompletion()
		}
		return m, nil, true
	}
	return m, nil, false
}

func (m model) handleFileCompletionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "up":
		if m.files.cursor > 0 {
			m.files.cursor--
		}
		return m, nil, true
	case "down", "tab":
		if m.files.cursor < len(m.files.matches)-1 {
			m.files.cursor++
		} else if msg.String() == "tab" {
			m.files.cursor = 0
		}
		return m, nil, true
	case "shift+tab":
		if m.files.cursor > 0 {
			m.files.cursor--
		} else {
			m.files.cursor = len(m.files.matches) - 1
		}
		return m, nil, true
	case "esc":
		m.files = fileCompletion{}
		return m, nil, true
	case "enter", "ctrl+y":
		if file, ok := m.files.selectedFile(); ok {
			prevH := m.composerTextHeight()
			m.ta.SetValue(replaceActiveFileMention(m.ta.Value(), file.Path))
			m.ta.CursorEnd()
			m.files = fileCompletion{}
			if m.ready && m.composerTextHeight() != prevH {
				m.layout()
				m.refreshViewport()
			}
		}
		return m, nil, true
	}
	return m, nil, false
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

const planModeSystemContext = `You are in Astonish terminal plan mode.
Respond with a concise implementation plan only. Do not execute tools, edit files, run commands, or make external changes. If the user asks for action, describe the steps you would take and ask for confirmation to proceed outside plan mode.`

func (m *model) togglePlanMode() {
	m.planMode = !m.planMode
}

func (m model) turnOptions() backend.TurnOptions {
	if m.planMode {
		return backend.TurnOptions{SystemContext: planModeSystemContext}
	}
	return backend.TurnOptions{}
}

func (m model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.ta.Value())
	if text == "" {
		return m, nil
	}
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil
	}

	m.slash = slashCompletion{}
	m.files = fileCompletion{}

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

	message := text
	if !m.tr.Awaiting {
		expanded, err := expandFileMentions(text, ".")
		if err != nil {
			m.tr.Apply(events.NewError(err.Error()))
			m.refreshViewport()
			return m, nil
		}
		message = expanded
	}

	m.tr.Apply(events.NewUser(text))
	m.refreshViewport()

	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel

	ch, err := m.backend.RunTurn(turnCtx, message, m.turnOptions())
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
	case text == "/files":
		cwd, _ := os.Getwd()
		m.tr.Apply(events.NewSystem("Type `@` plus part of a local path to attach file context from " + cwd + "."))
	case text == "/plan":
		m.togglePlanMode()
	case text == "/status":
		m.tr.Apply(events.NewSystem(m.statusText()))
	case text == "/exit" || text == "/quit" || text == "/q":
		m.quitting = true
		m.cancel()
		return m, tea.Quit
	case text == "/new":
		m.planMode = false
		return m.startNewSession()
	case text == "/sessions" || text == "/session":
		return m.openSessionsPicker()
	default:
		// Pass through to backend as a normal message so server/local slash handlers can run later.
		m.history = append(m.history, text)
		m.tr.Apply(events.NewUser(text))
		m.refreshViewport()
		turnCtx, cancel := context.WithCancel(m.ctx)
		m.turnCancel = cancel
		ch, err := m.backend.RunTurn(turnCtx, text, m.turnOptions())
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
  /help          Show this help
  /status        Show session / provider / model
  /sessions      Open sessions picker (also ctrl+l)
  /new           Start a new session (also ctrl+n)
  /files         Show @file context help
  /plan          Toggle plan-only mode (also shift+tab)
  /exit          Quit (/quit, /q)

Type / to open command completion (filters as you type).
Type @ plus part of a local path to attach file context.
  ↑↓ / tab       Move selection
  enter          Run selected command
  esc            Close completion

Keys:
  enter          Send message
  shift+enter    Newline (also ctrl+j / alt+enter)
  y / n          Approve / deny tool (when prompted)
  ctrl+o         Expand/collapse last tool activity
  ctrl+l         Sessions picker
  ctrl+n         New session
  shift+tab      Toggle plan-only mode
  ctrl+c         Cancel turn or quit
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
	if m.planMode {
		fmt.Fprintln(&b, "Plan mode: on")
	} else {
		fmt.Fprintln(&b, "Plan mode: off")
	}
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
	// Chrome: header(1) + sep + status(1) + composer(border+ta) + meta(1) + hints(1) + seps.
	// Use a conservative screen height because some terminal panes report a
	// height a couple of rows larger than the visible alternate screen, which
	// causes the top rows (the header) to scroll out of view.
	screenH := m.screenHeight()
	headerH := 1
	statusH := 1
	metaH := 1
	hintsH := 1
	seps := 2
	// Composer: 1 content line + 2 border rows; grow with multiline input.
	taH := m.composerTextHeight()
	composerH := taH + 2 // rounded border top/bottom
	chrome := headerH + statusH + composerH + metaH + hintsH + seps
	vh := screenH - chrome
	if vh < 5 {
		vh = 5
	}
	m.vp = viewport.New(m.width, vh)
	m.vp.Style = m.theme.Background
	content, hits := m.viewportContent()
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

func (m model) screenHeight() int {
	return m.height
}

func (m model) paintHeight() int {
	return m.screenHeight()
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
	content, hits := m.viewportContent()
	m.hitRegions = hits
	m.vp.SetContent(content)
	if atBottom || m.tr.Streaming || m.isEmptyConversation() {
		m.vp.GotoBottom()
	}
}

func (m *model) viewportContent() (string, []hitRegion) {
	if m.isEmptyConversation() {
		m.transcriptPlainLines = nil
		return m.renderWelcome(), nil
	}
	return m.renderTranscript()
}

func (m model) isEmptyConversation() bool {
	return m.tr == nil || len(m.tr.Items) == 0
}

func (m model) renderWelcome() string {
	if m.vp.Width <= 0 || m.vp.Height <= 0 {
		return ""
	}

	boxW := m.vp.Width - 8
	if boxW > 88 {
		boxW = 88
	}
	if boxW < 56 {
		boxW = max(32, m.vp.Width-2)
	}
	contentW := boxW - 6 // border(2) + horizontal padding(4)
	if contentW < 24 {
		contentW = 24
	}

	lines := m.welcomeLines(contentW)

	body := lipgloss.NewStyle().
		Width(contentW).
		Padding(1, 2).
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("238")).
		Background(lipgloss.Color("#000000")).
		Render(strings.Join(lines, "\n"))

	return lipgloss.Place(
		m.vp.Width,
		m.vp.Height,
		lipgloss.Center,
		lipgloss.Center,
		body,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceBackground(lipgloss.Color("#000000")),
	)
}

func (m model) welcomeLines(width int) []string {
	th := m.theme
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color("208")).
		Background(lipgloss.Color("#000000")).
		Bold(true).
		Align(lipgloss.Center).
		Width(width).
		Render("✦ Astonish")

	return []string{
		title,
		"",
		th.Text.Width(width).Align(lipgloss.Center).Render("Build, investigate, and operate with Astonish agents."),
		th.Muted.Width(width).Align(lipgloss.Center).Render("Ask questions, plan work, run tasks, or attach @files."),
		th.Muted.Width(width).Align(lipgloss.Center).Render("Connected to your platform. Ready when you are."),
		"",
		th.Hint.Width(width).Align(lipgloss.Center).Render("/ commands  ·  @ files  ·  shift+tab plan  ·  shift+enter newline"),
	}
}

// viewportTopY is the screen row where the transcript viewport starts.
func (m model) viewportTopY() int {
	// The transcript viewport starts after the one-line header and separator.
	return 2
}

func (m model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Wheel: let viewport scroll.
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}

	if msg.Button != tea.MouseButtonLeft {
		return m, nil
	}

	switch msg.Action {
	case tea.MouseActionPress:
		return m.handleMousePress(msg)
	case tea.MouseActionMotion:
		return m.handleMouseMotion(msg)
	case tea.MouseActionRelease:
		return m.handleMouseRelease(msg)
	default:
		return m, nil
	}
}

func (m model) handleMousePress(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	p, ok := m.selectionPointForMouse(msg)
	if !ok {
		m.selecting = false
		return m, nil
	}
	m.selecting = true
	m.selectionMoved = false
	m.selectionStart = p
	m.selectionEnd = p

	// Double-click detection. The action itself is handled on release, so a
	// click can still become a drag selection without triggering expansion.
	now := time.Now()
	m.clickIsDouble = now.Sub(m.lastClickAt) < doubleClickWindowMS*time.Millisecond &&
		abs(msg.Y-m.lastClickY) <= 1 &&
		abs(msg.X-m.lastClickX) <= 2
	m.lastClickAt = now
	m.lastClickY = msg.Y
	m.lastClickX = msg.X
	return m, nil
}

func (m model) handleMouseMotion(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !m.selecting {
		return m, nil
	}
	p, ok := m.selectionPointForMouse(msg)
	if !ok {
		return m, nil
	}
	if p != m.selectionStart {
		m.selectionMoved = true
	}
	m.selectionEnd = p
	m.refreshViewport()
	return m, nil
}

func (m model) handleMouseRelease(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !m.selecting {
		return m, nil
	}
	if p, ok := m.selectionPointForMouse(msg); ok {
		if p != m.selectionStart {
			m.selectionMoved = true
		}
		m.selectionEnd = p
	}
	text := ""
	if m.selectionMoved {
		text = selectionText(m.transcriptPlainLines, m.selectionStart, m.selectionEnd)
	}
	m.selecting = false
	m.selectionMoved = false
	m.refreshViewport()
	if strings.TrimSpace(text) != "" {
		if err := writeClipboard(text); err != nil {
			m.err = "Copy failed: " + err.Error()
		} else {
			m.copyStatus = fmt.Sprintf("Copied %d characters", len([]rune(text)))
			m.copiedUntil = time.Now().Add(2 * time.Second)
		}
		return m, nil
	}

	idx, ok := m.itemAtLine(m.selectionStart.line)
	if !ok {
		return m, nil
	}
	it := m.tr.Items[idx]
	if it.Kind == events.ItemActivity && !m.clickIsDouble {
		m.tr.ToggleExpand(idx)
		m.refreshViewport()
		return m, nil
	}
	if it.Kind == events.ItemUser && m.clickIsDouble {
		m.tr.ToggleExpand(idx)
		m.refreshViewport()
		return m, nil
	}
	return m, nil
}

func (m model) selectionPointForMouse(msg tea.MouseMsg) (selectionPoint, bool) {
	top := m.viewportTopY()
	if msg.Y < top || msg.Y >= top+m.vp.Height {
		return selectionPoint{}, false
	}
	line := m.vp.YOffset + (msg.Y - top)
	if line < 0 || line >= len(m.transcriptPlainLines) {
		return selectionPoint{}, false
	}
	col := msg.X
	if col < 0 {
		col = 0
	}
	return selectionPoint{line: line, col: col}, true
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
	var plainLines []string
	th := m.theme
	cw := contentWidth(m.width)
	lineNo := 0

	appendPlain := func(block string) {
		if block == "" {
			return
		}
		for _, line := range strings.Split(block, "\n") {
			plainLines = append(plainLines, stripANSI(line))
		}
	}

	appendBlock := func(itemIdx int, kind events.ItemKind, block string) {
		if block == "" {
			return
		}
		// Trailing newline normalization: count lines before padding.
		block = strings.TrimRight(block, "\n")
		rawPadded := padBlock(block)
		rawPadded = m.applySelectionToBlock(rawPadded, lineNo)
		padded := m.paintTranscriptBlock(rawPadded)
		start := lineNo
		n := lineCount(rawPadded)
		b.WriteString(padded)
		b.WriteString("\n")
		appendPlain(padded)
		gap := m.paintRow("", m.width)
		b.WriteString(gap)
		b.WriteString("\n") // vertical gap between messages
		plainLines = append(plainLines, stripANSI(gap))
		lineNo += n + 1 // block lines + one blank separator row
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
		case events.ItemNetworkDenial:
			appendBlock(i, it.Kind, m.renderNetworkDenialTranscript(it, cw))
		case events.ItemArtifact:
			appendBlock(i, it.Kind, th.Muted.Width(cw).Render("📄 "+first(it.Path, it.Content)))
		}
	}
	m.transcriptPlainLines = plainLines
	return b.String(), hits
}

const (
	ansiReset       = "\x1b[0m"
	ansiTrueBlackBG = "\x1b[48;2;0;0;0m"
	ansiDefaultBG   = "\x1b[49m"
)

func (m model) paintTranscriptBlock(block string) string {
	if m.theme.NoColor || block == "" {
		return block
	}
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		lines[i] = m.paintRow(line, m.width)
	}
	return strings.Join(lines, "\n")
}

func (m model) paintRow(line string, width int) string {
	if m.theme.NoColor {
		return line
	}
	if width < 1 {
		width = m.width
	}
	line = forceTrueBlackAfterReset(line)
	w := lipgloss.Width(line)
	if w < width {
		line += ansiTrueBlackBG + strings.Repeat(" ", width-w)
	}
	return ansiTrueBlackBG + line + ansiDefaultBG
}

func forceTrueBlackAfterReset(s string) string {
	return strings.ReplaceAll(s, ansiReset, ansiReset+ansiTrueBlackBG)
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
func (m model) renderActivityCollapsedPreview(steps []events.ToolStep, width int) string {
	if len(steps) == 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(steps); i++ {
		step := render.ToolStep{Name: steps[i].Name, Args: steps[i].Args, Result: steps[i].Result, Status: steps[i].Status}
		b.WriteByte('\n')
		line := collapsedToolLine("  "+render.ToolDetailLine(step), width)
		if steps[i].Status == "error" {
			b.WriteString(m.theme.Error.Width(width).Render(line))
			continue
		}
		b.WriteString(m.theme.Muted.Width(width).Render(line))
	}
	b.WriteByte('\n')
	b.WriteString(m.theme.Hint.Width(width).Render("  click to expand details"))
	return b.String()
}

func collapsedToolLine(line string, width int) string {
	line = strings.Join(strings.Fields(stripANSI(line)), " ")
	if width > 0 && lipgloss.Width(line) > width {
		line = truncateToWidth(line, width)
	}
	return line
}

func (m model) renderNetworkDenialTranscript(it events.Item, width int) string {
	th := m.theme
	denial, ok := firstNetworkDenial(&it)
	var b strings.Builder
	b.WriteString(th.Approval.Width(width).Render("⚠ Network access blocked"))
	if ok {
		b.WriteByte('\n')
		line := "Endpoint: " + endpointLabel(denial.Host, denial.Port)
		if denial.Binary != "" {
			line += " via " + denial.Binary
		}
		b.WriteString(th.Muted.Width(width).Render(line))
	}
	b.WriteByte('\n')
	b.WriteString(th.Muted.Width(width).Render("Choose in the authorization prompt below."))
	return b.String()
}

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
	head := th.Activity.Render(lead + summary)
	if statsStr != "" {
		// Right-align metrics on the same row when width allows.
		pad := width - lipgloss.Width(head) - lipgloss.Width(statsStr) - 1
		if pad < 1 {
			pad = 1
		}
		head = head + th.Background.Render(strings.Repeat(" ", pad)) + statsStr
	}

	if !it.Expanded {
		return head + m.renderActivityCollapsedPreview(it.Steps, width)
	}

	var b strings.Builder
	b.WriteString(head)
	for idx, s := range it.Steps {
		b.WriteByte('\n')
		line := render.ToolStatusLabel(s.Status) + "  " + render.ToolDisplayName(s.Name)
		if idx == len(it.Steps)-1 && s.Status == "running" {
			b.WriteString(th.Activity.Width(width).Render("  " + line))
		} else if s.Status == "error" {
			b.WriteString(th.Error.Width(width).Render("  " + line))
		} else {
			b.WriteString(th.Muted.Width(width).Render("  " + line))
		}

		step := render.ToolStep{Name: s.Name, Args: s.Args, Result: s.Result, Status: s.Status}
		if detail := render.ToolDetailBody(step, width-4); detail != "" {
			b.WriteByte('\n')
			b.WriteString(th.Muted.Width(width).Render("    " + detail))
		}
		// Show file diffs for edit/write tools.
		if d := render.DiffFromToolArgs(s.Name, s.Args, width, true, rs); d != "" {
			b.WriteByte('\n')
			b.WriteString(d)
			continue
		}
		if preview := render.ToolResultPreview(step, width-6); preview != "" {
			for _, line := range strings.Split(preview, "\n") {
				b.WriteByte('\n')
				style := th.Muted
				if s.Status == "error" {
					style = th.Error
				}
				b.WriteString(style.Width(width).Render("    " + line))
			}
		}
	}
	return b.String()
}

// renderUserBubble paints a full-width warm accent rectangle around the user
// message. The interior stays black, not filled gray. Long messages remain
// height-capped unless expanded; the expand/collapse cue is embedded in the
// bottom border near the bottom-right.
func (m model) renderUserBubble(content string, expanded bool, width int) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	if width < 18 {
		width = 18
	}
	inner := width - 6 // left/right border + two-space horizontal padding
	if inner < 8 {
		inner = 8
	}

	fullBody := wrapPlain(content, inner)
	fullBodyLines := lineCount(fullBody)

	body := fullBody
	var hint string
	switch {
	case !expanded && fullBodyLines > userBubbleMaxLines:
		cut, _ := truncateVisualLines(fullBody, userBubbleMaxLines, "")
		body = strings.TrimRight(cut, "\n")
		hint = "… double-click to expand"
	case expanded && fullBodyLines > userBubbleMaxLines:
		body = fullBody
		hint = "… double-click to collapse"
	}

	border := m.theme.Number
	text := m.theme.Text
	bg := m.theme.Background

	var b strings.Builder
	b.WriteString(border.Render("┌" + strings.Repeat("─", width-2) + "┐"))
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		b.WriteByte('\n')
		lineW := lipgloss.Width(line)
		if lineW > inner {
			line = truncateToWidth(line, inner)
			lineW = lipgloss.Width(line)
		}
		pad := inner - lineW
		if pad < 0 {
			pad = 0
		}
		b.WriteString(border.Render("│"))
		b.WriteString(bg.Render("  "))
		b.WriteString(text.Render(line))
		b.WriteString(bg.Render(strings.Repeat(" ", pad+2)))
		b.WriteString(border.Render("│"))
	}
	b.WriteByte('\n')
	b.WriteString(m.renderUserBubbleBottomBorder(width, hint, border, bg))
	return b.String()
}

func (m model) renderUserBubbleBottomBorder(width int, hint string, border lipgloss.Style, bg lipgloss.Style) string {
	if hint == "" || width < 32 {
		return border.Render("└" + strings.Repeat("─", width-2) + "┘")
	}
	hint = " " + hint + " "
	maxHint := width - 8
	if lipgloss.Width(hint) > maxHint {
		hint = truncateToWidth(hint, maxHint)
	}
	hintW := lipgloss.Width(hint)
	lineW := width - 2 - hintW
	if lineW < 2 {
		lineW = 2
	}
	leftW := lineW - 1
	rightW := 1
	return border.Render("└"+strings.Repeat("─", leftW)) +
		bg.Render(m.theme.UserExpandHint.Render(hint)) +
		border.Render(strings.Repeat("─", rightW)+"┘")
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
		return m.paintBackground("\n  Initializing Astonish…\n")
	}

	th := m.theme
	sep := th.Border.Width(m.width).Render(strings.Repeat("─", max(1, m.width)))

	// Completion popups sit just above the composer (filter-as-you-type).
	composerBlock := m.renderComposer()
	if !m.tr.Awaiting && !m.sessions.open {
		switch {
		case m.slash.active && len(m.slash.matches) > 0:
			composerBlock = lipgloss.JoinVertical(lipgloss.Left,
				m.renderSlashCompletion(),
				composerBlock,
			)
		case m.files.active && len(m.files.matches) > 0:
			composerBlock = lipgloss.JoinVertical(lipgloss.Left,
				m.renderFileCompletion(),
				composerBlock,
			)
		}
	}

	main := lipgloss.JoinVertical(lipgloss.Left,
		m.renderHeader(),
		sep,
		m.vp.View(),
		sep,
		m.renderLiveStatus(),
		composerBlock,
		m.renderFooterMeta(),
		m.renderHints(),
	)

	// Overlays: sessions picker or approval card on top of the main chrome.
	if m.sessions.open {
		overlay := m.renderSessionsOverlay()
		return m.paintBackground(lipgloss.Place(m.width, m.screenHeight(), lipgloss.Center, lipgloss.Center, overlay,
			lipgloss.WithWhitespaceChars(" "),
			lipgloss.WithWhitespaceBackground(lipgloss.Color("#000000")),
		))
	}
	if m.tr.Awaiting {
		// Stack approval card above composer area by replacing bottom of view.
		overlay := m.renderApprovalOverlay()
		return m.paintBackground(lipgloss.JoinVertical(lipgloss.Left,
			m.renderHeader(),
			sep,
			m.vp.View(),
			sep,
			overlay,
			m.renderHints(),
		))
	}
	return m.paintBackground(main)
}

func (m model) paintBackground(s string) string {
	paintH := m.paintHeight()
	if m.theme.NoColor || m.width <= 0 || paintH <= 0 {
		return s
	}
	placed := lipgloss.Place(
		m.width,
		paintH,
		lipgloss.Left,
		lipgloss.Top,
		forceTrueBlackAfterReset(s),
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceBackground(lipgloss.Color("#000000")),
	)
	placed = m.padPaintedHeight(placed, paintH)
	return ansiTrueBlackBG + placed + ansiDefaultBG
}

func (m model) padPaintedHeight(s string, height int) string {
	for renderedLineCount(s) < height {
		if s != "" {
			s += "\n"
		}
		s += ansiTrueBlackBG + strings.Repeat(" ", m.width)
	}
	return s
}

func renderedLineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// renderSlashCompletion draws the filterable / command list above the input.
func (m model) renderSlashCompletion() string {
	th := m.theme
	w := m.width - 2
	if w < 24 {
		w = 24
	}

	var b strings.Builder
	b.WriteString(th.Muted.Render("Commands") + th.Muted.Render("  ↑↓  tab  enter run  esc") + "\n")
	maxShow := 8
	start := 0
	if m.slash.cursor >= maxShow {
		start = m.slash.cursor - maxShow + 1
	}
	end := start + maxShow
	if end > len(m.slash.matches) {
		end = len(m.slash.matches)
	}
	for i := start; i < end; i++ {
		cmd := m.slash.matches[i]
		name := "/" + cmd.Name
		desc := cmd.Description
		line := name
		// pad name column
		pad := 14 - lipgloss.Width(name)
		if pad < 1 {
			pad = 1
		}
		line += strings.Repeat(" ", pad) + desc
		if lipgloss.Width(line) > w-4 {
			runes := []rune(line)
			if len(runes) > w-5 {
				line = string(runes[:w-5]) + "…"
			}
		}
		if i == m.slash.cursor {
			b.WriteString(th.Brand.Render("› " + line))
		} else {
			b.WriteString(th.Text.Render("  " + line))
		}
		b.WriteByte('\n')
	}
	body := strings.TrimRight(b.String(), "\n")
	return m.paintCompletionPopup(th.InputBorder.
		Width(w).
		Padding(0, 1).
		Render(body), w)
}

// renderFileCompletion draws the filterable @file candidate list above the input.
func (m model) renderFileCompletion() string {
	th := m.theme
	w := m.width - 2
	if w < 24 {
		w = 24
	}

	var b strings.Builder
	b.WriteString(th.Muted.Render("Files") + th.Muted.Render("  ↑↓  tab  enter attach  esc") + "\n")
	maxShow := 8
	start := 0
	if m.files.cursor >= maxShow {
		start = m.files.cursor - maxShow + 1
	}
	end := start + maxShow
	if end > len(m.files.matches) {
		end = len(m.files.matches)
	}
	for i := start; i < end; i++ {
		line := "@" + m.files.matches[i].Path
		if lipgloss.Width(line) > w-4 {
			runes := []rune(line)
			if len(runes) > w-5 {
				line = string(runes[:w-5]) + "…"
			}
		}
		if i == m.files.cursor {
			b.WriteString(th.Brand.Render("› " + line))
		} else {
			b.WriteString(th.Text.Render("  " + line))
		}
		b.WriteByte('\n')
	}
	body := strings.TrimRight(b.String(), "\n")
	return m.paintCompletionPopup(th.InputBorder.
		Width(w).
		Padding(0, 1).
		Render(body), w)
}

func (m model) paintCompletionPopup(s string, width int) string {
	if m.theme.NoColor {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = m.paintRow(line, width)
	}
	return strings.Join(lines, "\n")
}

func (m model) renderHeader() string {
	th := m.theme
	width := m.width
	if width < 1 {
		width = 80
	}

	leftPlain := m.headerConnectionText()
	rightPlain := m.headerUsageText()
	leftW := lipgloss.Width(leftPlain)
	rightW := lipgloss.Width(rightPlain)

	if rightPlain != "" && leftW+rightW+1 > width {
		leftMax := width - rightW - 1
		if leftMax < 8 {
			rightPlain = truncateToWidth(rightPlain, max(0, width-9))
			rightW = lipgloss.Width(rightPlain)
			leftMax = width - rightW - 1
		}
		leftPlain = truncateToWidth(leftPlain, leftMax)
		leftW = lipgloss.Width(leftPlain)
	}

	left := m.renderHeaderLeft(leftPlain)
	right := th.Muted.Render(rightPlain)
	gap := width - leftW - rightW
	if rightPlain != "" {
		if gap < 1 {
			gap = 1
		}
		return m.paintRow(left+strings.Repeat(" ", gap)+right, width)
	}
	return m.paintRow(left, width)
}

func (m model) headerConnectionText() string {
	parts := []string{"Astonish"}
	if m.info.ServerURL != "" {
		parts = append(parts, m.info.ServerURL)
	}
	if m.info.User != "" {
		parts = append(parts, m.info.User)
	}
	if m.info.ServerURL == "" && m.info.User == "" {
		parts = append(parts, first(m.info.Mode, "platform"))
	}
	return strings.Join(parts, " · ")
}

func (m model) renderHeaderLeft(text string) string {
	if text == "" {
		return ""
	}
	if text == "Astonish" {
		return m.theme.Header.Render(text)
	}
	if strings.HasPrefix(text, "Astonish") {
		return m.theme.Header.Render("Astonish") + m.theme.Muted.Render(strings.TrimPrefix(text, "Astonish"))
	}
	return m.theme.Muted.Render(text)
}

func (m model) headerUsageText() string {
	usage := &events.Usage{}
	if m.tr != nil && m.tr.LastUsage != nil {
		usage = m.tr.LastUsage
	}
	if usage.Total <= 0 {
		return "Usage 0"
	}
	return fmt.Sprintf("Usage %s · in %s · out %s",
		formatTokenCount(usage.Total),
		formatTokenCount(usage.Input),
		formatTokenCount(usage.Output),
	)
}

func formatTokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// renderLiveStatus shows spinner text while a turn is active; otherwise a blank
// spacer so layout does not jump.
func (m model) renderLiveStatus() string {
	th := m.theme
	if m.err != "" {
		return m.paintRow(th.Error.Render(m.err), m.width)
	}
	if m.tr.Status != "" || m.tr.Streaming {
		return m.paintRow(th.Status.Render(m.spin.View()+" "+first(m.tr.Status, "Working…")), m.width)
	}
	if m.copyStatus != "" && time.Now().Before(m.copiedUntil) {
		return m.paintRow(th.Success.Render(m.copyStatus), m.width)
	}
	// Keep one row so chrome height is stable and painted with the app background.
	return m.paintRow("", m.width)
}

// renderComposer draws the bordered input box (Grok-style), with the current
// mode embedded in the bottom border.
func (m model) renderComposer() string {
	th := m.theme
	w := m.width
	if w < 12 {
		w = 12
	}
	innerW := w - 2
	contentW := innerW - 2 // one-space padding on each side
	if contentW < 8 {
		contentW = 8
	}

	border := m.composerBorderStyle()
	label := "Normal"
	if m.planMode {
		label = "Plan"
	}

	var b strings.Builder
	b.WriteString(border.Render("╭" + strings.Repeat("─", innerW) + "╮"))
	for _, line := range strings.Split(strings.TrimRight(m.ta.View(), "\n"), "\n") {
		plainW := lipgloss.Width(stripANSI(line))
		if plainW > contentW {
			line = truncateToWidth(stripANSI(line), contentW)
			plainW = lipgloss.Width(line)
		}
		pad := contentW - plainW
		if pad < 0 {
			pad = 0
		}
		b.WriteByte('\n')
		b.WriteString(border.Render("│"))
		b.WriteString(th.Background.Render(" "))
		b.WriteString(line)
		b.WriteString(th.Background.Render(strings.Repeat(" ", pad+1)))
		b.WriteString(border.Render("│"))
	}
	b.WriteByte('\n')
	b.WriteString(m.renderComposerBottomBorder(w, label, border, th.Background))
	return b.String()
}

func (m model) composerBorderStyle() lipgloss.Style {
	if m.theme.NoColor {
		return lipgloss.NewStyle()
	}
	color := lipgloss.Color("246")
	if m.planMode {
		color = lipgloss.Color("172")
	}
	return lipgloss.NewStyle().Foreground(color).Background(lipgloss.Color("#000000"))
}

func (m model) renderComposerBottomBorder(width int, label string, border lipgloss.Style, bg lipgloss.Style) string {
	if label == "" || width < 18 {
		return border.Render("╰" + strings.Repeat("─", max(0, width-2)) + "╯")
	}
	text := " " + label + " "
	textW := lipgloss.Width(text)
	inner := width - 2
	left := inner - textW - 2
	if left < 1 {
		left = 1
	}
	right := inner - left - textW
	if right < 1 {
		right = 1
		left = inner - textW - right
		if left < 1 {
			left = 1
		}
	}
	return border.Render("╰"+strings.Repeat("─", left)) +
		bg.Render(text) +
		border.Render(strings.Repeat("─", right)+"╯")
}

// renderFooterMeta shows provider/model and approval mode (Grok footer strip).
func (m model) renderFooterMeta() string {
	th := m.theme
	provider := first(m.info.Provider, m.tr.Provider)
	modelName := first(m.info.Model, m.tr.Model)
	left := modelFooterText(provider, modelName)
	right := ""
	if m.info.AutoApprove {
		right = "auto-approve"
	}
	leftR := th.FooterMeta.Render(left)
	if right == "" {
		return m.paintRow(leftR, m.width)
	}
	rightR := th.FooterMeta.Render(right)
	gap := m.width - lipgloss.Width(leftR) - lipgloss.Width(rightR)
	if gap < 1 {
		gap = 1
	}
	return m.paintRow(leftR+strings.Repeat(" ", gap)+rightR, m.width)
}

// renderHints is the keybinding help line under the composer.
func modelFooterText(provider, modelName string) string {
	provider = strings.TrimSpace(provider)
	modelName = strings.TrimSpace(modelName)
	if isAmbiguousModelLabel(modelName) {
		modelName = ""
	}
	if provider == "" && modelName == "" {
		return "Provider/model loading…"
	}
	if provider == "" {
		return "Provider resolving… / " + modelName
	}
	if modelName == "" {
		return provider + " / model resolving…"
	}
	return provider + " / " + modelName
}

func isAmbiguousModelLabel(modelName string) bool {
	return strings.EqualFold(strings.TrimSpace(modelName), "default")
}

func (m model) renderHints() string {
	th := m.theme
	if m.tr.Awaiting {
		return th.Hint.Render("y approve  ·  n deny  ·  1/2 select  ·  esc deny")
	}
	if m.slash.active {
		return th.Hint.Render("↑↓ select  ·  enter run  ·  tab next  ·  esc close")
	}
	if m.files.active {
		return th.Hint.Render("↑↓ select  ·  enter attach file  ·  tab next  ·  esc close")
	}
	full := "Enter send  ·  / commands  ·  @ files  ·  shift+tab plan  ·  shift+enter newline  ·  ctrl+l sessions  ·  ctrl+c quit"
	short := "Enter send  ·  / commands  ·  @ files  ·  shift+tab plan"
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
	return m.paintRow(th.Hint.Render(line), m.width)
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
