package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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

type pastedBlock struct {
	placeholder string
	content     string
}

// pastedImage is a clipboard/image attachment shown as an atomic [image #N] token.
type pastedImage struct {
	placeholder string
	mimeType    string
	data        []byte
	number      int
}

type artifactHit struct {
	start       int
	end         int
	itemIdx     int
	artifactIdx int
}

type fileViewerState struct {
	open     bool
	loading  bool
	err      string
	artifact events.Artifact
	content  string
	vp       viewport.Model
}

// delegationDetailState holds the overlay state for viewing a sub-task's activity.
type delegationDetailState struct {
	open     bool
	taskName string
	taskIdx  int // index into the DelegationTasks slice
	itemIdx  int // transcript item index of the ItemDelegation
	vp       viewport.Model
}

// Config configures the terminal chat app.
type Config struct {
	Backend backend.Backend
	// AltBackend enables Ctrl+Tab switching between two modes (e.g. code ↔
	// platform). When nil, the TUI operates in single-backend mode and Ctrl+Tab
	// is a no-op. The alt backend is lazily opened on first switch.
	AltBackend backend.Backend
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

	// Close alt backend if it was opened during the session.
	if cfg.AltBackend != nil && m.backends[1].opened {
		cfg.AltBackend.Close()
	}

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
	// turnStartedAt records when the current turn began (or resumed after a
	// HITL pause), enabling a live elapsed-time counter in the status bar.
	// Zero value means the timer is not actively ticking (either no turn or paused).
	turnStartedAt time.Time
	// timerAccumulated holds elapsed execution time from previous segments of
	// the same logical turn (before HITL pauses). The total elapsed time for
	// the current turn is timerAccumulated + time.Since(turnStartedAt).
	timerAccumulated time.Duration
	// compacting is true while an async /compact request is in flight. Used to
	// show "Compacting…" immediately and reject a second /compact until done.
	compacting bool

	// hitRegions maps rendered transcript lines → items (for mouse expand).
	hitRegions []hitRegion
	// artifactHits maps individual rendered file rows to generated artifacts.
	artifactHits []artifactHit
	// transcriptPlainLines is the visible transcript without ANSI styling; used
	// for drag-to-copy selection in Bubble Tea mouse mode.
	transcriptPlainLines []string
	// transcriptContentSpans records, for each entry in transcriptPlainLines,
	// the [start,end) rune-column range that is actual content rather than
	// decorative chrome (box borders, padding, expand hints). Drag-to-copy
	// clamps the selection to this span so borders/padding never reach the
	// clipboard. A span of {0, len(line)} means the whole line is content
	// (the default for undecorated blocks); {0,0} means a pure chrome row.
	transcriptContentSpans [][2]int
	// mdCache memoizes expensive markdown rendering (goldmark + chroma) for
	// agent message blocks, keyed by width+content. Finalized transcript items
	// never change, so this turns per-event rendering from O(whole transcript)
	// into O(changed item) and keeps the UI responsive under a burst of events.
	// Cleared on resize (see layout/WindowSizeMsg) since width is part of output.
	mdCache map[string]string
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
	sessions       sessionsState
	rollback       rollbackState
	modelPicker    modelPickerState
	providerPicker providerPickerState
	webSearchPicker webSearchPickerState
	fileViewer      fileViewerState
	delegationDetail delegationDetailState
	// slash command completion popup (active when composer starts with /)
	slash slashCompletion
	// @file completion popup (active while typing a trailing @token)
	files fileCompletion
	// planMode is the platform-mode plan flag. In platform mode, shift+tab
	// cycles Normal ↔ Plan using this bool. Not used in code mode.
	planMode bool
	// graphPlanMode is the code-mode Plan mode (phased gate: graph → read →
	// gap → plan, driven by codegraph + gplan_* transition tools). In code
	// mode, shift+tab cycles Normal → Plan → Ask → Normal using this bool.
	graphPlanMode bool
	// askMode is a research-only mode (code mode only). The agent can investigate
	// with read-only tools but cannot plan or execute. shift+tab cycles
	// Normal → Plan → Ask → Normal.
	askMode bool
	// Pasted blocks keep the composer compact while preserving submitted content.
	pastedBlocks []pastedBlock
	// Pasted images from the clipboard, shown as atomic [image #N] tokens.
	pastedImages []pastedImage
	nextImageNum int
	// intentionalMultiline is set when the user presses Shift+Enter / Alt+Enter /
	// Ctrl+J. While true, the composer may keep 4+ visible lines. Command+V and
	// other terminal-injected multi-line pastes leave this false, so they collapse.
	intentionalMultiline bool
	// pasteStreamUntil marks a short window after a collapse where trailing
	// terminal-injected paste characters are merged into the same paste block.
	pasteStreamUntil time.Time
	pasteIdleSeq     int
	// composerWatching keeps a short tick loop alive so Command+V collapse does
	// not wait for the next keypress to re-enter Update.
	composerWatching bool
	// workDir is the workspace/project root (process CWD in code mode). Used to
	// render project-relative file paths in diff headers.
	workDir string

	// Dual-backend mode: Ctrl+Tab switches between two independent backend
	// panels (e.g. local code ↔ platform chat). Each slot preserves its own
	// transcript, plan mode, and theme so switching is instantaneous.
	dualMode         bool
	activeBackendIdx int
	backends         [2]backendSlot
}

func newModel(parent context.Context, cfg Config) model {
	ctx, cancel := context.WithCancel(parent)
	info := cfg.Backend.Info()
	th := ThemeForMode(info.Mode)

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
	// Prefer explicit clipboard paste when the terminal reports super/cmd.
	ta.KeyMap.Paste = key.NewBinding(
		key.WithKeys("ctrl+v", "super+v"),
		key.WithHelp("ctrl+v", "paste"),
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
	// Code mode renders a linear reasoning thread (messages persist, tools
	// group, a message breaks the group). Studio/platform keeps sticky-agent.
	tr.LinearThread = info.Mode == "code"
	if info.Usage != nil {
		tr.LastUsage = &events.Usage{Input: info.Usage.Input, Output: info.Usage.Output, Total: info.Usage.Total}
	}
	if info.ContextTokens > 0 {
		tr.ContextTokens = info.ContextTokens
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
		workDir:    workspaceRoot(),
		dualMode:   cfg.AltBackend != nil,
	}

	// Initialize dual-backend slots.
	m.backends[0] = backendSlot{
		backend:    cfg.Backend,
		theme:      th,
		tr:         tr,
		historyIdx: -1,
		opened:     true, // primary is already opened by Run()
	}
	if cfg.AltBackend != nil {
		altInfo := cfg.AltBackend.Info()
		m.backends[1] = backendSlot{
			backend:    cfg.AltBackend,
			theme:      ThemeForMode(altInfo.Mode),
			historyIdx: -1,
			opened:     false, // lazily opened on first Ctrl+Tab
		}
	}

	return m
}

// workspaceRoot returns the process working directory, which in code mode is
// the project root the agent's tools operate against. Empty on error (diff
// headers then fall back to showing the raw path).
func workspaceRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// tea messages
type eventMsg events.Event
type turnDoneMsg struct{}
type turnErrMsg struct{ err error }
type timerTickMsg struct{}
type pasteIdleMsg struct{ seq int }
type composerWatchMsg struct{}
type artifactContentLoadedMsg struct {
	path    string
	content string
	err     error
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{textarea.Blink, m.spin.Tick, tea.EnableBracketedPaste}
	if m.info.IsResumed && m.info.SessionID != "" {
		cmds = append(cmds, m.loadInitialHistoryCmd())
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// A textarea paste (Ctrl+V keybinding → clipboard read) arrives as a
	// textarea.pasteMsg. When it carries text, insert it. When it is empty, the
	// clipboard likely holds an image with no text representation — try an image
	// paste instead of dropping the event.
	if text, isPaste := textareaPasteMsg(msg); isPaste {
		if next, cmd, handled := m.tryPasteImage(); handled {
			return next, cmd
		}
		if strings.TrimSpace(text) == "" {
			// No image and no text: nothing to insert. Swallow the empty paste
			// so it does not fall through to the textarea as a no-op.
			return m, nil
		}
		return m.handlePaste(text)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Width is part of the markdown cache key; drop stale-width entries so
		// the cache does not accumulate one set per historical window size.
		m.mdCache = nil
		m.layout()
		m.ready = true
		m.refreshViewport()
		return m, nil

	case tea.MouseMsg:
		if m.fileViewer.open {
			return m.handleFileViewerMouse(msg)
		}
		if m.delegationDetail.open {
			return m.handleDelegationDetailMouse(msg)
		}
		return m.handleMouse(msg)

	case tea.KeyMsg:
		if m.fileViewer.open {
			return m.handleFileViewerKey(msg)
		}
		if m.delegationDetail.open {
			return m.handleDelegationDetailKey(msg)
		}
		// Sessions / model picker overlays capture keys first.
		if m.sessions.open {
			return m.handleSessionsKey(msg)
		}
		if m.modelPicker.open {
			return m.handleModelPickerKey(msg)
		}
		if m.providerPicker.open {
			return m.handleProviderPickerKey(msg)
		}
		if m.webSearchPicker.open {
			return m.handleWebSearchPickerKey(msg)
		}
		if m.rollback.open {
			return m.handleRollbackKey(msg)
		}

		// Explicit paste bindings (Ctrl+V / Super+V) prefer clipboard images.
		if isClipboardPasteKey(msg) {
			if next, cmd, handled := m.tryPasteImage(); handled {
				return next, cmd
			}
		}
		if msg.Type == tea.KeyRunes {
			text := normalizePasteText(string(msg.Runes))
			// Real paste events and multi-line rune bursts (common for terminal
			// Command+V without bracketed-paste markers) go through handlePaste.
			if msg.Paste || strings.Contains(text, "\n") {
				if msg.Paste && text == "" {
					if next, cmd, handled := m.tryPasteImage(); handled {
						return next, cmd
					}
				}
				return m.handlePaste(text)
			}
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
			// Mid-turn: cancel the in-flight RunTurn. Idle: quit the app.
			if next, cmd, handled := m.cancelInFlightTurn(); handled {
				return next, cmd
			}
			m.quitting = true
			m.cancel()
			return m, tea.Quit
		case "esc":
			// Esc cancels an in-flight turn (Claude Code / OpenCode style) but
			// never quits the app when idle — overlays/approvals already handled
			// Esc above this switch.
			if next, cmd, handled := m.cancelInFlightTurn(); handled {
				return next, cmd
			}
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
		case "ctrl+\\":
			if m.dualMode {
				return m.switchBackend()
			}
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
		// Note: KeyCtrlM and KeyEnter share the same code, so "ctrl+m" is "enter".
		switch msg.String() {
		case "enter":
			return m.submit()
		case "shift+enter", "alt+enter", "ctrl+j":
			return m.insertNewline(true)
		case "left", "ctrl+b":
			if m.jumpPastePlaceholder(-1) {
				return m, nil
			}
		case "right", "ctrl+f":
			if m.jumpPastePlaceholder(1) {
				return m, nil
			}
		case "alt+left", "alt+b", "ctrl+left":
			if m.jumpPastePlaceholder(-1) {
				return m, nil
			}
		case "alt+right", "alt+f", "ctrl+right":
			if m.jumpPastePlaceholder(1) {
				return m, nil
			}
		case "backspace", "ctrl+h", "ctrl+w", "alt+backspace", "delete", "ctrl+d":
			// Treat paste placeholders as a single token for delete operations.
			if next, cmd, handled := m.handlePastePlaceholderDelete(msg.String()); handled {
				return next, cmd
			}
		}

	case pasteIdleMsg:
		if msg.seq != m.pasteIdleSeq {
			return m, nil
		}
		// Idle only keeps an in-flight multi-line paste stream watch alive; it
		// must not collapse based on total composer line count.
		return m, m.ensureComposerWatch()

	case composerWatchMsg:
		// Watch loop is only for finishing a multi-line paste stream that already
		// produced a placeholder (trailing injection). Never collapse plain typing.
		if m.shouldWatchComposer() {
			return m, m.composerWatchCmd()
		}
		m.composerWatching = false
		return m, nil

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

	case rollbackLoadedMsg:
		return m.applyRollbackLoaded(msg)

	case rolledBackMsg:
		return m.applyRolledBack(msg)

	case modelProvidersLoadedMsg:
		return m.applyModelProvidersLoaded(msg)

	case modelModelsLoadedMsg:
		return m.applyModelModelsLoaded(msg)

	case modelPinAppliedMsg:
		return m.applyModelPinApplied(msg)

	case providerInstancesLoadedMsg:
		return m.applyProviderInstancesLoaded(msg)

	case providerMutatedMsg:
		return m.applyProviderMutated(msg)

	case webSearchProvidersLoadedMsg:
		return m.applyWebSearchProvidersLoaded(msg)

	case webSearchInstalledMsg:
		return m.applyWebSearchInstalled(msg)

	case perplexityOptionsLoadedMsg:
		return m.applyPerplexityOptionsLoaded(msg)

	case perplexityConfiguredMsg:
		return m.applyPerplexityConfigured(msg)

	case webSearchClearedMsg:
		return m.applyWebSearchCleared(msg)

	case artifactContentLoadedMsg:
		return m.applyArtifactContentLoaded(msg)

	case eventMsg:
		// Apply this event plus any others already sitting in the channel, then
		// repaint once. A burst of tool output (e.g. a large diff followed by
		// several results) otherwise triggers one full transcript render per
		// event and the UI falls behind — the loop can appear frozen while the
		// backend keeps working. Coalescing bounds repaints to one per batch.
		// The drain is bounded and non-blocking so key messages (Esc/cancel) are
		// never starved: we take only events already buffered, then yield.
		m.applyEvent(events.Event(msg))
		drained := 0
		for m.eventCh != nil && drained < maxCoalescedEvents {
			select {
			case ev, ok := <-m.eventCh:
				if !ok {
					// Channel closed mid-drain: the turn finished. Finalize now.
					m.eventCh = nil
					cmd := m.finishTurn()
					m.refreshViewport()
					return m, cmd
				}
				m.applyEvent(ev)
				drained++
			default:
				// Nothing more buffered right now.
				drained = maxCoalescedEvents
			}
		}
		// After draining, if a plan approval just became active the approval
		// widget replaces the composer and reduces the viewport height.
		// Re-layout and re-scroll so the plan text above the widget is visible.
		if m.tr.Awaiting {
			m.layout()
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
			m.timerReset()
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
		m.timerResume()
		m.refreshViewport()
		if msg.ch == nil {
			return m, nil
		}
		return m, tea.Batch(waitEvent(msg.ch), timerTick())

	case networkGrantDeniedMsg:
		if msg.err != nil {
			m.tr.Apply(events.NewError("Network denial failed: " + msg.err.Error()))
			m.refreshViewport()
		}
		return m, nil

	case turnDoneMsg:
		m.eventCh = nil
		m.turnCancel = nil
		if m.tr.Awaiting {
			// HITL approval pending — pause timer, don't finalize.
			m.timerPause()
		} else if m.timerActive() {
			d := m.timerElapsed().Truncate(time.Second)
			if d >= time.Second {
				m.tr.Apply(events.NewSystem("Completed in " + formatDuration(d)))
			}
			m.timerReset()
		}
		if m.tr.Streaming && !m.tr.Awaiting {
			m.tr.Apply(events.NewDone())
		}
		m.refreshViewport()
		return m, nil

	case turnErrMsg:
		m.eventCh = nil
		m.turnCancel = nil
		m.timerReset()
		m.tr.Apply(events.NewError(msg.err.Error()))
		m.refreshViewport()
		return m, nil

	case compactDoneMsg:
		return m.applyCompactDone(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if m.tr.Streaming || m.tr.Status != "" {
			return m, cmd
		}
		return m, nil

	case timerTickMsg:
		// Re-schedule the next tick only while the timer is actively running.
		if m.timerRunning() {
			// Refresh viewport when delegation is active so per-task
			// elapsed timers update live (they compute time.Since on render).
			if m.tr != nil && m.tr.DelegationActive {
				m.refreshViewport()
			}
			return m, timerTick()
		}
		return m, nil
	}

	// Delegate to textarea / viewport when ready.
	if m.ready {
		// Never insert text inside a paste placeholder — treat it as one cell.
		if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.Type == tea.KeyRunes && !keyMsg.Paste {
			m.escapePastePlaceholderForInsert()
		}
		prevValue := m.ta.Value()
		prevH := m.composerTextHeight()
		// Pre-grow the textarea to its max height before it processes the key,
		// then let layout() below set the accurate height once the new value is
		// known. The textarea repositions its internal viewport during Update;
		// if it is still too short when the cursor crosses a soft-wrap boundary
		// it scrolls the earlier rows out of view and a later SetHeight cannot
		// bring them back (bubbles' viewport does not re-clamp YOffset when the
		// height grows). Sizing to the cap up front keeps every row visible for
		// any growth transition (1→2, or a wrapping paste 1→3/4).
		if m.ta.Height() < composerMaxRows {
			m.ta.SetHeight(composerMaxRows)
		}
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		cmds = append(cmds, cmd)
		// If navigation landed inside a placeholder, snap out.
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			m.snapOutOfPastePlaceholder(pasteNavDir(keyMsg.String()))
		}
		// If typing somehow mutated a placeholder token, restore atomic tokens.
		m.repairBrokenPastePlaceholders(prevValue)
		if pasteCmd := m.afterComposerChange(prevValue); pasteCmd != nil {
			cmds = append(cmds, pasteCmd)
		}
		// Grow/shrink composer when the visual line count changes; layout()
		// recomputes the viewport around the new composer height.
		if m.composerTextHeight() != prevH {
			m.layout()
			m.refreshViewport()
		} else if m.ta.Height() != m.composerTextHeight() {
			// We pre-grew the textarea above but the height did not actually
			// change (e.g. a short keystroke) — snap it back to the accurate
			// height so it does not stay padded at composerMaxRows.
			m.ta.SetHeight(m.composerTextHeight())
		}
		m.prunePastedBlocks()
		// Keep completion popups in sync with composer value after typing.
		m.syncSlashCompletion()
		m.syncFileCompletion()
		if watch := m.ensureComposerWatch(); watch != nil {
			cmds = append(cmds, watch)
		}
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
	var extra []slashCommand
	if m.providerAdmin() != nil {
		extra = append(extra, providerSlashCommand)
	}
	if m.webSearchAdmin() != nil {
		extra = append(extra, webSearchSlashCommand)
	}
	if m.rollbackCap() != nil {
		extra = append(extra, rollbackSlashCommand)
	}
	if m.compactionCap() != nil {
		extra = append(extra, compactSlashCommand)
	}
	matches := filterSlashCommands(query, extra...)
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
// intentional marks user-authored multi-line editing so Command+V paste collapse
// does not rewrite deliberately multi-line prompts.
func (m model) insertNewline(intentional bool) (tea.Model, tea.Cmd) {
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil
	}
	if intentional {
		m.intentionalMultiline = true
	}
	prevValue := m.ta.Value()
	prevH := m.composerTextHeight()
	m.ta.InsertString("\n")
	cmd := m.afterComposerChange(prevValue)
	if m.ready && m.composerTextHeight() != prevH {
		m.layout()
		m.refreshViewport()
	}
	return m, cmd
}

// planModeSystemContext must stay in sync with agent.PlanModeSystemContext
// (the runtime gate's source of truth). Read-only tools listed here — including
// the tree-sitter navigation tools — are allowed by the gate (agent.SafeTools).
const planModeSystemContext = `You are in Astonish PLAN MODE. This is a hard constraint enforced by the runtime, not a suggestion.

RULES:
- You MUST NOT make any changes. Mutating tools (write_file, edit_file, shell_command, and every other non-read-only tool) and delegate_tasks are DISABLED by the runtime and will be refused if you call them.
- You MAY use read-only tools (read_file, grep_search, find_files, file_tree, code_definition, code_references, repo_map, memory_search, etc.) to investigate and build an accurate plan.
- Do NOT attempt to execute the plan. End by asking the user to exit Plan mode (shift+tab) to proceed with execution.

Your job is to produce a COMPLETE plan the user can approve with confidence — not a partial sketch. Work through these four disciplines:

1. INVESTIGATE THOROUGHLY. Understand the code you will touch before you plan it. Use repo_map once to orient in unfamiliar areas, then code_definition to read the actual declaration of each symbol you will change, and code_references to enumerate ALL its call sites. Read the real regions with read_file. Batch independent read-only lookups in the same turn so they run in parallel. Keep investigating until you are confident no affected file, caller, interface, type, test, migration, generated file, or doc remains unexamined — first-pass results routinely miss dependents.

2. COVER EVERY DEPENDENCY — NO PARTIAL IMPLEMENTATIONS. A complete plan touches every layer the change reaches: the symbol itself AND all its callers, the interfaces/schemas/types it depends on, the tests that exercise it, any generated code that must be regenerated, migrations, and the docs (AGENTS.md / docs/architecture) the project requires. Order phases dependency-first: shared types and interfaces before the consumers that use them. Verify that no phase leaves orphaned or unwired code — every new symbol must be integrated by the end of the plan.

3. SURFACE DECISIONS FOR THE USER. Call out anything that needs a human decision — breaking changes, meaningful alternative approaches with their trade-offs, or ambiguous requirements — explicitly in the plan so the user can decide before execution begins. If a pivotal requirement is genuinely ambiguous and you cannot resolve it by reading the code, ask ONE concise clarifying question rather than guessing.

4. BE EFFICIENT — SPEND EFFORT PROPORTIONAL TO BLAST RADIUS. A one-file tweak needs a quick look; a cross-cutting change needs full tracing. Stop exploring once you can name every file you would change and why — do not read the whole repo. Prefer structural tools (code_definition/code_references) over broad grep, and never re-read a file already in your context.

When your plan is finalized, record it with announce_plan (goal + ordered, dependency-first phases). For each phase:
- 'files': list every file the phase touches (marked new/modify/delete) — the symbol AND its callers, tests, generated code, migrations, docs.
- 'details': write a concrete, self-contained description of exactly what to do in this phase — specific structs/functions to add or remove, the exact logic change, new fields, interface updates. Write enough detail that execution can proceed directly from this text without re-reading the code. This is the most important field; a vague 'details' makes the plan useless.
- 'verify': the command that proves the phase is done (build/test/lint).

Call announce_plan WITHOUT any preceding prose or summary — the plan document is shown directly to the user and speaks for itself. Do NOT write a "Here's my plan..." narration before the tool call. This persists the full plan to a session PLAN.md that survives context compaction and is shown to the user. Do NOT hand-write PLAN.md yourself. (You will drive phase status with update_plan once execution begins. When executing, treat PLAN.md as the authoritative source — do NOT re-investigate files or symbols already confirmed in the plan unless the code has changed since planning.)`

// graphPlanModeSystemContext must stay in sync with
// agent.GraphPlanModeSystemContext (the runtime gate's source of truth). It
// teaches the model the phased "plan-for-the-plan" discipline enforced by the
// staged tool gate in Graph-Optimized Plan mode.
const graphPlanModeSystemContext = `You are in Astonish GRAPH-OPTIMIZED PLAN MODE. This is a hard constraint enforced by the runtime through a staged tool gate, not a suggestion. Like Plan mode, this is a NO-CHANGES mode: write_file, edit_file, shell_command and every other mutating tool are DISABLED in every phase and will be refused.

The runtime advances through four phases. Each phase unlocks a specific set of tools; you move between phases by calling small transition tools. Do NOT try to call a tool before its phase — the gate will refuse it and tell you which phase it belongs to.

PHASE 1 — GRAPH (current at turn start). Only ` + "`codegraph_explore`" + ` and ` + "`find_files`" + ` are available. codegraph is a pre-computed knowledge graph of this repo: symbols, call edges, dependencies, cross-file references, and change blast-radius. Query it FIRST to understand the code you will touch — it answers most structural questions in 1-4 calls with far fewer tokens than grep. Compound your findings as you go: never re-query the graph for something already in your context. When you have identified the exact regions you need to read, call ` + "`gplan_reads`" + ` with the synthesized read list (each entry: path + why you need it). Only include paths that ` + "`codegraph_explore`" + ` explicitly returned — do NOT guess or infer filenames; if you need a file but do not have its confirmed path, use ` + "`find_files`" + ` to locate it first. This advances you to the READ phase. If codegraph returns no coverage (language unsupported / not indexed), call ` + "`gplan_gaps`" + ` immediately to skip straight to the GAP phase.

PHASE 2 — READ. ` + "`read_file`" + ` (and read_pdf/filter_json) unlock, plus codegraph_explore. Read exactly the regions you listed — do NOT re-search for information you already have. When you have read everything the graph pointed you to, decide: if genuine gaps remain that codegraph could not answer, call ` + "`gplan_gaps`" + ` with those gaps (each: the question + why codegraph was insufficient) to advance to the GAP phase. If there are no gaps, call ` + "`gplan_finalize`" + ` to skip straight to the PLAN phase.

PHASE 3 — GAP (complementary). The remaining read-only tools unlock: grep_search, find_files, file_tree, repo_map, code_definition, code_references, web_fetch, memory_search, memory_get, skill_lookup — and delegate_tasks. Use these ONLY for the genuine gaps codegraph could not fill. Prefer ` + "`delegate_tasks`" + ` with read-only ` + "`tools`" + ` filters (e.g. ["grep_search","read_file","code_references"]) to fan out independent gap questions in parallel. Do not re-answer anything already established. When gaps are closed, call ` + "`gplan_finalize`" + ` to advance to the PLAN phase.

PHASE 4 — PLAN. ` + "`announce_plan`" + ` unlocks. Call it WITHOUT any preceding prose — the plan document is shown directly to the user. Record the finalized plan: goal + ordered, dependency-first phases. For each phase list its affected files (each marked new/modify/delete — the symbol AND its callers, tests, generated code, migrations, docs, so nothing is left unwired); write a concrete, self-contained 'details' field describing exactly what to do (specific functions/structs to add or change, the exact logic, new fields, interface updates — enough detail that execution can proceed directly from it without re-reading the code); and give a verify step (the build/test/lint command that proves the phase is done). File paths must be confirmed: only record a file path in 'details' or 'files' if codegraph_explore, code_definition, find_files, or read_file explicitly returned that exact path this session — do NOT infer paths from symbol names or directory conventions; if a path was not confirmed, call find_files before adding it to the plan. This persists the full plan to a session PLAN.md shown to the user; do NOT hand-write PLAN.md. End by asking the user to exit to Normal mode (shift+tab) before any execution. When executing later, treat PLAN.md as authoritative — do NOT re-investigate files or symbols already confirmed in the plan.

Produce a COMPLETE plan — cover every dependency the change reaches, order phases dependency-first, and surface any human decisions (breaking changes, alternatives with trade-offs, ambiguous requirements) explicitly. Spend effort proportional to blast radius.`

// askModeSystemContext must stay in sync with agent.AskModeSystemContext
// (the runtime gate's source of truth). It teaches the model it is in a
// read-only research mode — no changes, no plans, just Q&A.
const askModeSystemContext = `You are in Astonish ASK MODE. This is a hard constraint enforced by the runtime, not a suggestion.

RULES:
- You are in a RESEARCH-ONLY mode. Your job is to answer questions, explain architecture, discuss possible solutions, and help the user understand how things work.
- You MUST NOT make any changes. Mutating tools (write_file, edit_file, shell_command, and every other non-read-only tool), delegate_tasks, and announce_plan are DISABLED by the runtime and will be refused if you call them.
- You MUST NOT produce implementation plans or attempt to execute anything. This is not Plan mode — do not use announce_plan.
- You MAY use read-only tools (read_file, grep_search, find_files, file_tree, code_definition, code_references, repo_map, codegraph_explore, memory_search, web_fetch, etc.) to investigate the codebase and gather information.
- Focus on providing clear, accurate, well-researched answers. Cite specific files, functions, and line numbers when relevant.
- If the user asks you to make changes or create a plan, remind them they are in Ask mode and suggest switching to Normal or Plan mode (shift+tab).`

func (m *model) togglePlanMode() {
	// In platform mode, cycle Normal ↔ Plan only (no Graph Plan / Ask — they're
	// code-mode-specific and rely on codegraph / local tools).
	if m.info.Mode == "platform" {
		m.planMode = !m.planMode
		m.graphPlanMode = false
		m.askMode = false
		return
	}
	// Code mode: cycle Normal → Plan → Ask → Normal.
	switch {
	case !m.planMode && !m.graphPlanMode && !m.askMode:
		m.graphPlanMode = true
	case m.graphPlanMode:
		m.graphPlanMode = false
		m.askMode = true
	default: // askMode
		m.askMode = false
	}
}

func (m model) turnOptions() backend.TurnOptions {
	if m.graphPlanMode {
		return backend.TurnOptions{SystemContext: graphPlanModeSystemContext, GraphPlanMode: true}
	}
	if m.planMode {
		return backend.TurnOptions{SystemContext: planModeSystemContext, PlanMode: true}
	}
	if m.askMode {
		return backend.TurnOptions{SystemContext: askModeSystemContext, AskMode: true}
	}
	return backend.TurnOptions{}
}

func isClipboardPasteKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "ctrl+v", "super+v", "cmd+v":
		return true
	default:
		return false
	}
}

func (m model) tryPasteImage() (tea.Model, tea.Cmd, bool) {
	if m.sessions.open || m.rollback.open || m.modelPicker.open || m.providerPicker.open || m.webSearchPicker.open || m.fileViewer.open {
		return m, nil, false
	}
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil, false
	}
	data, mime, ok := clipboardImageReader()
	if !ok || len(data) == 0 {
		return m, nil, false
	}
	if mime == "" {
		if sniffed, ok := sniffImageMIME(data); ok {
			mime = sniffed
		} else {
			mime = "image/png"
		}
	}
	next, cmd := m.insertPastedImage(data, mime)
	return next, cmd, true
}

func (m model) insertPastedImage(data []byte, mimeType string) (tea.Model, tea.Cmd) {
	if m.sessions.open || m.rollback.open || m.modelPicker.open || m.providerPicker.open || m.webSearchPicker.open || m.fileViewer.open {
		return m, nil
	}
	prevH := m.composerTextHeight()
	m.nextImageNum++
	num := m.nextImageNum
	placeholder := fmt.Sprintf("[image #%d]", num)
	m.ta.InsertString(placeholder)
	m.pastedImages = append(m.pastedImages, pastedImage{
		placeholder: placeholder,
		mimeType:    mimeType,
		data:        data,
		number:      num,
	})
	if m.ready && m.composerTextHeight() != prevH {
		m.layout()
		m.refreshViewport()
	}
	m.syncSlashCompletion()
	m.syncFileCompletion()
	return m, nil
}

func (m model) handlePaste(text string) (tea.Model, tea.Cmd) {
	if m.sessions.open || m.rollback.open || m.modelPicker.open || m.providerPicker.open || m.webSearchPicker.open || m.fileViewer.open {
		return m, nil
	}
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil
	}
	text = normalizePasteText(text)
	// Truly empty paste (no runes) often means an image-only clipboard on macOS.
	// Do not treat pure newlines as empty — Command+V line streams use "\n" runes.
	if text == "" {
		if next, cmd, handled := m.tryPasteImage(); handled {
			return next, cmd
		}
		return m, nil
	}
	m.intentionalMultiline = false
	prevH := m.composerTextHeight()
	// Collapse only when THIS paste payload itself has 4+ lines — never based on
	// how many lines already exist in the composer.
	if pasteCollapseLineCount(text) < 4 {
		m.ta.InsertString(text)
	} else {
		m.insertPastedBlock(text)
	}
	if m.ready && m.composerTextHeight() != prevH {
		m.layout()
		m.refreshViewport()
	}
	m.syncSlashCompletion()
	m.syncFileCompletion()
	return m, nil
}

// afterComposerChange reacts to composer mutations that were not explicit paste
// events. Collapse only when the newly inserted span itself is 4+ lines (a true
// multi-line paste burst). Existing composer lines are ignored. Shift+Enter
// multi-line editing sets intentionalMultiline and is left expanded.
func (m *model) afterComposerChange(previous string) tea.Cmd {
	current := m.ta.Value()
	if current == previous {
		return nil
	}
	if m.intentionalMultiline {
		return nil
	}

	// Mid-stream continuation after a multi-line paste already collapsed: merge
	// only additional lines that arrive while the paste stream window is open.
	if m.pasteStreamActive() && len(m.pastedBlocks) > 0 && pasteLineCount(current) > 1 {
		expanded := m.expandPastedBlocks(current)
		prevContentLines := 0
		for _, block := range m.pastedBlocks {
			if n := pasteCollapseLineCount(block.content); n > prevContentLines {
				prevContentLines = n
			}
		}
		if expanded != current && pasteCollapseLineCount(expanded) > prevContentLines {
			prevH := m.composerTextHeight()
			// Re-collapse using the expanded full paste content only for the
			// active placeholder region (prefix/suffix outside placeholders stay).
			if prefix, _, suffix, ok := findInsertedText(previous, current); ok {
				// Prefer keeping surrounding typed text; expand placeholders first.
				_ = prefix
				_ = suffix
			}
			// Whole-value re-collapse of expanded paste content when the composer
			// is only the placeholder plus trailing paste stream chars.
			m.replaceComposerPaste("", expanded, "")
			m.touchPasteStream()
			if m.ready && m.composerTextHeight() != prevH {
				m.layout()
				m.refreshViewport()
			}
			return m.ensureComposerWatch()
		}
	}

	// Collapse only the inserted delta when that delta itself is 4+ lines.
	m.collapseInsertedPasteIfLarge(previous)
	return m.ensureComposerWatch()
}

func (m *model) pasteStreamActive() bool {
	return !m.pasteStreamUntil.IsZero() && time.Now().Before(m.pasteStreamUntil)
}

func (m *model) touchPasteStream() {
	m.pasteStreamUntil = time.Now().Add(200 * time.Millisecond)
}

func (m *model) shouldWatchComposer() bool {
	// Only keep the watch alive to finish an in-flight multi-line paste stream
	// that already produced a placeholder. Do not watch merely because the
	// composer has many lines of normal typing/single-line pastes.
	return !m.intentionalMultiline && m.pasteStreamActive() && len(m.pastedBlocks) > 0
}

func (m *model) composerWatchCmd() tea.Cmd {
	return tea.Tick(16*time.Millisecond, func(time.Time) tea.Msg {
		return composerWatchMsg{}
	})
}

func (m *model) ensureComposerWatch() tea.Cmd {
	if !m.shouldWatchComposer() {
		m.composerWatching = false
		return nil
	}
	if m.composerWatching {
		return nil
	}
	m.composerWatching = true
	return m.composerWatchCmd()
}

// collapseInsertedPasteIfLarge collapses only when the newly inserted span
// itself has 4+ content lines. Existing composer lines are never counted.
func (m *model) collapseInsertedPasteIfLarge(previous string) bool {
	if m.intentionalMultiline {
		return false
	}
	current := m.ta.Value()
	if current == previous {
		return false
	}
	var prefix, inserted, suffix string
	var ok bool
	if previous == "" {
		// Composer was empty: the whole value is the insertion.
		prefix, inserted, suffix, ok = "", current, "", true
	} else {
		prefix, inserted, suffix, ok = findInsertedText(previous, current)
	}
	if !ok || pasteCollapseLineCount(inserted) < 4 {
		return false
	}
	prevH := m.composerTextHeight()
	m.replaceComposerPaste(prefix, inserted, suffix)
	m.touchPasteStream()
	if m.ready && m.composerTextHeight() != prevH {
		m.layout()
		m.refreshViewport()
	}
	return true
}

func (m *model) insertPastedBlock(content string) {
	placeholder := pastePlaceholder(content)
	m.ta.InsertString(placeholder)
	m.pastedBlocks = append(m.pastedBlocks, pastedBlock{placeholder: placeholder, content: content})
	m.touchPasteStream()
}

func (m *model) replaceComposerPaste(prefix, content, suffix string) {
	// Replace any prior blocks for this composer value with a single mapping.
	m.pastedBlocks = nil
	placeholder := pastePlaceholder(content)
	m.ta.SetValue(prefix + placeholder + suffix)
	m.ta.CursorEnd()
	m.pastedBlocks = append(m.pastedBlocks, pastedBlock{placeholder: placeholder, content: content})
	m.touchPasteStream()
}

func pastePlaceholder(content string) string {
	n := pasteCollapseLineCount(content)
	if n < 1 {
		n = pasteLineCount(content)
	}
	return fmt.Sprintf("[Pasted: %d lines]", n)
}

func findInsertedText(previous, current string) (prefix, inserted, suffix string, ok bool) {
	if len(current) <= len(previous) {
		return "", "", "", false
	}
	start := 0
	for start < len(previous) && start < len(current) && previous[start] == current[start] {
		start++
	}
	prevEnd := len(previous)
	currentEnd := len(current)
	for prevEnd > start && currentEnd > start && previous[prevEnd-1] == current[currentEnd-1] {
		prevEnd--
		currentEnd--
	}
	if previous[:start]+current[currentEnd:] != previous {
		return "", "", "", false
	}
	return current[:start], current[start:currentEnd], current[currentEnd:], true
}

type pastePlaceholderSpan struct {
	start       int // rune offset in Value()
	end         int // rune offset exclusive
	placeholder string
	line        int // hard line index
	lineStart   int // rune offset of placeholder start within the hard line
	lineEnd     int // rune offset exclusive within the hard line
}

func pasteNavDir(key string) int {
	switch key {
	case "left", "ctrl+b", "alt+left", "alt+b", "ctrl+left", "up", "ctrl+p", "home", "ctrl+a":
		return -1
	case "right", "ctrl+f", "alt+right", "alt+f", "ctrl+right", "down", "ctrl+n", "end", "ctrl+e":
		return 1
	default:
		return 0
	}
}

func (m model) pastePlaceholderSpans() []pastePlaceholderSpan {
	value := m.ta.Value()
	if value == "" {
		return nil
	}
	valueRunes := []rune(value)
	used := make([]bool, len(valueRunes))
	var spans []pastePlaceholderSpan

	add := func(placeholder string) {
		if placeholder == "" {
			return
		}
		phRunes := []rune(placeholder)
		for i := 0; i+len(phRunes) <= len(valueRunes); i++ {
			match := true
			for j := 0; j < len(phRunes); j++ {
				if used[i+j] || valueRunes[i+j] != phRunes[j] {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			for j := 0; j < len(phRunes); j++ {
				used[i+j] = true
			}
			line, lineStart := runeOffsetToLineCol(value, i)
			spans = append(spans, pastePlaceholderSpan{
				start:       i,
				end:         i + len(phRunes),
				placeholder: placeholder,
				line:        line,
				lineStart:   lineStart,
				lineEnd:     lineStart + len(phRunes),
			})
			return
		}
	}

	for _, block := range m.pastedBlocks {
		add(block.placeholder)
	}
	for _, img := range m.pastedImages {
		add(img.placeholder)
	}
	return spans
}

func runeOffsetToLineCol(value string, offset int) (line, col int) {
	runes := []rune(value)
	if offset < 0 {
		offset = 0
	}
	if offset > len(runes) {
		offset = len(runes)
	}
	for i := 0; i < offset; i++ {
		if runes[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return line, col
}

func (m model) composerLineCol() (line, col int) {
	line = m.ta.Line()
	li := m.ta.LineInfo()
	col = li.StartColumn + li.ColumnOffset
	if col < 0 {
		col = 0
	}
	return line, col
}

func (m *model) setComposerCursorRuneOffset(offset int) {
	value := m.ta.Value()
	runes := []rune(value)
	if offset < 0 {
		offset = 0
	}
	if offset > len(runes) {
		offset = len(runes)
	}
	line, col := runeOffsetToLineCol(value, offset)
	// Walk to the target hard line from the top. Soft-wrap can make this imperfect,
	// but paste placeholders are single-line ASCII tokens and this is good enough.
	for m.ta.Line() > 0 {
		m.ta.CursorUp()
	}
	m.ta.CursorStart()
	for m.ta.Line() < line {
		prev := m.ta.Line()
		m.ta.CursorDown()
		if m.ta.Line() == prev {
			break
		}
	}
	m.ta.SetCursor(col)
}

// jumpPastePlaceholder makes left/right treat a paste token as one cell.
func (m *model) jumpPastePlaceholder(dir int) bool {
	if dir == 0 || (len(m.pastedBlocks) == 0 && len(m.pastedImages) == 0) {
		return false
	}
	line, col := m.composerLineCol()
	for _, span := range m.pastePlaceholderSpans() {
		if span.line != line {
			continue
		}
		if dir < 0 {
			// At end of token or inside it → jump to start.
			if col == span.lineEnd || (col > span.lineStart && col < span.lineEnd) {
				m.ta.SetCursor(span.lineStart)
				return true
			}
		}
		if dir > 0 {
			// At start of token or inside it → jump to end.
			if col == span.lineStart || (col > span.lineStart && col < span.lineEnd) {
				m.ta.SetCursor(span.lineEnd)
				return true
			}
		}
	}
	return false
}

// escapePastePlaceholderForInsert moves the caret out of a paste token before typing.
func (m *model) escapePastePlaceholderForInsert() {
	line, col := m.composerLineCol()
	for _, span := range m.pastePlaceholderSpans() {
		if span.line != line {
			continue
		}
		if col > span.lineStart && col < span.lineEnd {
			// Prefer inserting after the token.
			m.ta.SetCursor(span.lineEnd)
			return
		}
	}
}

// snapOutOfPastePlaceholder moves the caret out if it landed inside a paste token.
func (m *model) snapOutOfPastePlaceholder(dir int) {
	line, col := m.composerLineCol()
	for _, span := range m.pastePlaceholderSpans() {
		if span.line != line {
			continue
		}
		if col > span.lineStart && col < span.lineEnd {
			if dir < 0 {
				m.ta.SetCursor(span.lineStart)
			} else {
				m.ta.SetCursor(span.lineEnd)
			}
			return
		}
	}
}

func (m model) handlePastePlaceholderDelete(key string) (tea.Model, tea.Cmd, bool) {
	if len(m.pastedBlocks) == 0 && len(m.pastedImages) == 0 {
		return m, nil, false
	}
	line, col := m.composerLineCol()
	forward := key == "delete" || key == "ctrl+d"
	for _, span := range m.pastePlaceholderSpans() {
		if span.line != line {
			continue
		}
		// Treat the whole token as one cell: delete if caret is on the token
		// (start/inside) or at its trailing edge (end).
		onToken := col >= span.lineStart && col <= span.lineEnd
		if !onToken {
			continue
		}
		// Avoid stealing backspace when caret is at token start and there is
		// text before it that the user intends to delete (except ctrl+w which
		// should still remove the token when on its leading edge).
		if !forward && col == span.lineStart && key != "ctrl+w" && key != "alt+backspace" {
			continue
		}
		// Avoid stealing forward-delete when caret is past the token end.
		if forward && col == span.lineEnd {
			continue
		}
		return m.removePastePlaceholderSpan(span)
	}
	return m, nil, false
}

func (m model) removePastePlaceholderSpan(span pastePlaceholderSpan) (tea.Model, tea.Cmd, bool) {
	value := m.ta.Value()
	runes := []rune(value)
	if span.start < 0 || span.end > len(runes) || span.start >= span.end {
		return m, nil, false
	}
	prevH := m.composerTextHeight()
	newRunes := append(append([]rune{}, runes[:span.start]...), runes[span.end:]...)
	m.ta.SetValue(string(newRunes))
	m.setComposerCursorRuneOffset(span.start)
	m.removeLastPastedBlock(span.placeholder)
	if m.ready && m.composerTextHeight() != prevH {
		m.layout()
		m.refreshViewport()
	}
	m.syncSlashCompletion()
	m.syncFileCompletion()
	return m, nil, true
}

// repairBrokenPastePlaceholders restores paste tokens if typing split one.
func (m *model) repairBrokenPastePlaceholders(previous string) {
	if len(m.pastedBlocks) == 0 && len(m.pastedImages) == 0 {
		return
	}
	current := m.ta.Value()
	if current == previous {
		return
	}
	check := func(placeholder string) bool {
		if placeholder == "" || strings.Contains(current, placeholder) {
			return false
		}
		if !strings.Contains(previous, placeholder) {
			return false
		}
		// Placeholder text was modified. Revert to previous atomic value and
		// place the caret after the token so typing continues outside it.
		m.ta.SetValue(previous)
		if idx := strings.Index(previous, placeholder); idx >= 0 {
			m.setComposerCursorRuneOffset(utf8.RuneCountInString(previous[:idx]) + utf8.RuneCountInString(placeholder))
		}
		return true
	}
	for _, block := range m.pastedBlocks {
		if check(block.placeholder) {
			return
		}
	}
	for _, img := range m.pastedImages {
		if check(img.placeholder) {
			return
		}
	}
}

func (m *model) removeLastPastedBlock(placeholder string) {
	for i := len(m.pastedBlocks) - 1; i >= 0; i-- {
		if m.pastedBlocks[i].placeholder == placeholder {
			m.pastedBlocks = append(m.pastedBlocks[:i], m.pastedBlocks[i+1:]...)
			return
		}
	}
	for i := len(m.pastedImages) - 1; i >= 0; i-- {
		if m.pastedImages[i].placeholder == placeholder {
			m.pastedImages = append(m.pastedImages[:i], m.pastedImages[i+1:]...)
			return
		}
	}
}

func (m *model) prunePastedBlocks() {
	value := m.ta.Value()
	if len(m.pastedBlocks) > 0 {
		kept := m.pastedBlocks[:0]
		for _, block := range m.pastedBlocks {
			if strings.Contains(value, block.placeholder) {
				kept = append(kept, block)
			}
		}
		m.pastedBlocks = kept
	}
	if len(m.pastedImages) > 0 {
		keptImg := m.pastedImages[:0]
		for _, img := range m.pastedImages {
			if strings.Contains(value, img.placeholder) {
				keptImg = append(keptImg, img)
			}
		}
		m.pastedImages = keptImg
	}
}

func (m model) expandPastedBlocks(text string) string {
	for _, block := range m.pastedBlocks {
		text = strings.Replace(text, block.placeholder, block.content, 1)
	}
	return text
}

func composerShouldCollapseValue(text string) bool {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "[Pasted: ") && strings.HasSuffix(trimmed, " lines]") && !strings.Contains(trimmed, "\n") {
		return false
	}
	// Ignore a trailing empty line so Command+V does not collapse on the
	// newline before the final content line has arrived.
	return pasteCollapseLineCount(text) >= 4
}

// textareaPasteMsg reports whether msg is a textarea.pasteMsg and its (possibly
// empty) text. bubbles emits an empty pasteMsg when the Ctrl+V keybinding reads
// a clipboard that holds an image (no text) — callers use the empty case to try
// an image paste instead of dropping the event silently.
func textareaPasteMsg(msg tea.Msg) (string, bool) {
	if msg == nil {
		return "", false
	}
	if fmt.Sprintf("%T", msg) != "textarea.pasteMsg" {
		return "", false
	}
	return fmt.Sprint(msg), true
}

func normalizePasteText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

func pasteLineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(normalizePasteText(text), "\n") + 1
}

// pasteCollapseLineCount counts content lines for paste collapse, ignoring a
// single trailing newline so a paste stream is not collapsed before the final
// line body has been inserted.
func pasteCollapseLineCount(text string) int {
	if text == "" {
		return 0
	}
	text = normalizePasteText(text)
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func (m model) submit() (tea.Model, tea.Cmd) {
	rawValue := m.ta.Value()
	attachments := m.collectSubmitAttachments(rawValue)
	text := strings.TrimSpace(m.expandPastedBlocks(rawValue))
	if text == "" && len(attachments) == 0 {
		return m, nil
	}
	if text == "" && len(attachments) > 0 {
		// Image-only turn still needs a visible/user message marker.
		text = strings.TrimSpace(rawValue)
	}
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil
	}

	m.slash = slashCompletion{}
	m.files = fileCompletion{}

	// Local slash commands (minimal set for PR1).
	if strings.HasPrefix(text, "/") {
		m.pastedBlocks = nil
		m.pastedImages = nil
		return m.handleSlash(text)
	}

	m.history = append(m.history, text)
	m.historyIdx = -1
	m.ta.Reset()
	m.pastedBlocks = nil
	m.pastedImages = nil
	m.intentionalMultiline = false
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

	opts := m.turnOptions()
	opts.Attachments = attachments
	ch, err := m.backend.RunTurn(turnCtx, message, opts)
	if err != nil {
		cancel()
		m.turnCancel = nil
		m.tr.Apply(events.NewError(err.Error()))
		m.refreshViewport()
		return m, nil
	}
	m.eventCh = ch
	m.timerStart()
	return m, tea.Batch(waitEvent(ch), timerTick())
}

func (m model) collectSubmitAttachments(composerValue string) []backend.Attachment {
	if len(m.pastedImages) == 0 {
		return nil
	}
	var out []backend.Attachment
	for _, img := range m.pastedImages {
		if !strings.Contains(composerValue, img.placeholder) {
			continue
		}
		ext := "png"
		switch img.mimeType {
		case "image/jpeg":
			ext = "jpg"
		case "image/gif":
			ext = "gif"
		case "image/webp":
			ext = "webp"
		}
		out = append(out, backend.Attachment{
			Filename: fmt.Sprintf("image-%d.%s", img.number, ext),
			MimeType: img.mimeType,
			Data:     img.data,
		})
	}
	return out
}

func (m model) handleSlash(text string) (tea.Model, tea.Cmd) {
	m.ta.Reset()
	switch {
	case text == "/help" || text == "/?":
		m.tr.Apply(events.NewSystem(helpText(m.providerAdmin() != nil, m.webSearchAdmin() != nil, m.rollbackCap() != nil, m.compactionCap() != nil)))
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
	case text == "/model" || text == "/models":
		return m.openModelPicker()
	case text == "/provider" || text == "/providers":
		return m.openProviderPicker()
	case text == "/websearch" || text == "/search":
		return m.openWebSearchPicker()
	case text == "/rollback" || text == "/revert":
		return m.openRollbackPicker()
	case text == "/compact":
		return m.runCompact()
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
		m.timerStart()
		return m, tea.Batch(waitEvent(ch), timerTick())
	}
	m.refreshViewport()
	return m, nil
}

func helpText(providerAdmin bool, webSearch bool, rollback bool, compaction bool) string {
	providerLine := ""
	if providerAdmin {
		providerLine = "\n  /provider      Manage local providers (add/remove)"
	}
	webSearchLine := ""
	if webSearch {
		webSearchLine = "\n  /websearch     Configure web search provider"
	}
	rollbackLine := ""
	if rollback {
		rollbackLine = "\n  /rollback      Revert chat and file changes to an earlier message"
	}
	compactLine := ""
	if compaction {
		compactLine = "\n  /compact       Compact the conversation context to free up the window"
	}
	return strings.TrimSpace(`
Commands:
  /help          Show this help (/?)
  /status        Show session / provider / model
  /sessions      Open sessions picker (also ctrl+l)
  /model         Choose provider and model` + providerLine + webSearchLine + rollbackLine + compactLine + `
  /new           Start a new session (also ctrl+n)
  /files         Show @file context help
  /plan          Toggle plan-only mode (also shift+tab)
  /exit          Quit (/quit, /q)

Type / to open command completion (filters as you type).
Type @ plus part of a local path to attach file context.
Paste multi-line text (4+ lines) to insert [Pasted: N lines].
Paste an image from the clipboard to insert [image #N].
  ↑↓ / tab       Move selection
  enter          Run selected command
  esc            Close completion

Keys:
  enter          Send message
  shift+enter    Newline (also ctrl+j / alt+enter)
  ctrl+v         Paste text or image from clipboard
  y / n          Approve / deny tool (when prompted)
  ctrl+o         Expand/collapse last tool activity
  ctrl+l         Sessions picker
  ctrl+n         New session
  shift+tab      Toggle plan-only mode
  esc            Cancel the current turn
  ctrl+c         Cancel turn or quit
  ctrl+d         Quit (when input is empty)
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

// maxCoalescedEvents bounds how many already-buffered events a single Update
// applies before yielding, so a flood of tool output cannot starve key input.
const maxCoalescedEvents = 256

func waitEvent(ch <-chan events.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return turnDoneMsg{}
		}
		return eventMsg(ev)
	}
}

// timerTick returns a command that fires a timerTickMsg after 1 second,
// driving the live elapsed-time counter in the status bar.
func timerTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return timerTickMsg{}
	})
}

// applyEvent applies one streamed event to the transcript and keeps the header
// info in sync. It does NOT repaint — callers coalesce a batch and repaint once.
func (m *model) applyEvent(ev events.Event) {
	m.tr.Apply(ev)
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
	// Pause the timer when a HITL approval suspends execution. The timer
	// resumes when the user submits their approval choice.
	if ev.Kind == events.KindApproval || ev.Kind == events.KindNetworkDenial {
		m.timerPause()
	}
}

// finishTurn finalizes a turn whose event channel has closed (mirrors the
// turnDoneMsg handler). Returns the follow-up command (none).
func (m *model) finishTurn() tea.Cmd {
	m.turnCancel = nil
	if m.tr.Awaiting {
		// HITL approval pending — pause timer, don't finalize.
		m.timerPause()
	} else if m.timerActive() {
		d := m.timerElapsed().Truncate(time.Second)
		if d >= time.Second {
			m.tr.Apply(events.NewSystem("Completed in " + formatDuration(d)))
		}
		m.timerReset()
	}
	if m.tr.Streaming && !m.tr.Awaiting {
		m.tr.Apply(events.NewDone())
	}
	return nil
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
	oldOffset := 0
	if m.vp.Height > 0 {
		oldOffset = m.vp.YOffset
	}
	m.vp = viewport.New(m.width, vh)
	m.vp.Style = m.theme.Background
	content, hits, artifactHits := m.viewportContent()
	m.hitRegions = hits
	m.artifactHits = artifactHits
	m.vp.SetContent(content)
	m.vp.SetYOffset(oldOffset)
	m.layoutFileViewer()

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

// composerMaxRows caps how many visible rows the composer textarea grows to.
const composerMaxRows = 4

// composerTextHeight returns the textarea height: 1 by default, up to 4 when
// the user has entered multiple lines. Uses the real textarea value so typed
// multi-line content expands; collapsed paste placeholders stay one line.
//
// Lines are counted visually (soft-wrapped), so a single long typed line that
// spills onto a second display row grows the composer the same way an explicit
// Shift+Enter newline does.
func (m model) composerTextHeight() int {
	lines := visualLineCount(m.ta.Value(), m.composerWrapWidth())
	if lines < 1 {
		lines = 1
	}
	if lines > composerMaxRows {
		lines = composerMaxRows
	}
	return lines
}

// composerWrapWidth returns the effective text width the composer textarea
// soft-wraps at: the terminal width minus the border/padding (matching the
// SetWidth call in layout) minus the 2-cell prompt reserved by
// SetPromptFunc. Returns 0 when the terminal size is not yet known so callers
// fall back to logical line counting.
func (m model) composerWrapWidth() int {
	innerW := m.width - 4
	if innerW < 20 {
		innerW = 20
	}
	// SetPromptFunc(2, …) reserves 2 cells for the "❯ " prompt.
	wrapW := innerW - 2
	if m.width <= 0 {
		return 0
	}
	if wrapW < 1 {
		wrapW = 1
	}
	return wrapW
}

func (m *model) refreshViewport() {
	if !m.ready {
		return
	}
	atBottom := m.vp.AtBottom()
	content, hits, artifactHits := m.viewportContent()
	m.hitRegions = hits
	m.artifactHits = artifactHits
	m.vp.SetContent(content)
	if atBottom || m.tr.Streaming || m.isEmptyConversation() {
		m.vp.GotoBottom()
	}
}

func (m *model) viewportContent() (string, []hitRegion, []artifactHit) {
	if m.isEmptyConversation() {
		m.transcriptPlainLines = nil
		m.transcriptContentSpans = nil
		return m.renderWelcome(), nil, nil
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
	if m.info.Mode == "code" {
		return m.codeWelcomeLines(width)
	}

	th := m.theme
	title := lipgloss.NewStyle().
		Foreground(m.theme.AccentColor).
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

// codeWelcomeLines renders the welcome card for `astonish code` — the local,
// unsandboxed coding tool. Unlike platform chat, tools run directly on the host
// filesystem in the working directory, so the copy makes that context clear and
// shows the directory being operated on.
func (m model) codeWelcomeLines(width int) []string {
	th := m.theme
	title := lipgloss.NewStyle().
		Foreground(m.theme.AccentColor).
		Background(lipgloss.Color("#000000")).
		Bold(true).
		Align(lipgloss.Center).
		Width(width).
		Render("✦ Astonish Code")

	lines := []string{
		title,
		"",
		th.Text.Width(width).Align(lipgloss.Center).Render("Your local AI coding tool — reads, writes, and runs code right here."),
	}

	if dir := abbreviateHomePath(m.info.WorkingDir); dir != "" {
		lines = append(lines,
			th.Muted.Width(width).Align(lipgloss.Center).Render("Working in "+dir),
		)
	}

	lines = append(lines,
		th.Muted.Width(width).Align(lipgloss.Center).Render(codeApprovalNotice(m.info.AutoApprove)),
		th.Muted.Width(width).Align(lipgloss.Center).Render("Ready when you are."),
		"",
		th.Hint.Width(width).Align(lipgloss.Center).Render("/commands · @files · /rollback · shift+tab plan · shift+enter newline"),
	)

	return lines
}

// codeApprovalNotice describes code mode's tool-execution policy for the welcome
// card. Read-only tools always run without prompting; only file-modifying and
// command-running tools are gated — and that gate is skipped entirely under
// --auto-approve.
func codeApprovalNotice(autoApprove bool) string {
	if autoApprove {
		return "Astonish intelligence, right where your code lives — no prompts."
	}
	return "Astonish intelligence, right where your code lives."
}

// abbreviateHomePath collapses the user's home directory prefix to "~" for
// display. Returns the input unchanged when it is not under the home directory,
// and empty for empty input.
func abbreviateHomePath(path string) string {
	if path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rel, err := filepath.Rel(home, path); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
		return "~" + string(filepath.Separator) + rel
	}
	return path
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
		text = selectionText(m.transcriptPlainLines, m.transcriptContentSpans, m.selectionStart, m.selectionEnd)
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

	if artifact, ok := m.artifactAtLine(m.selectionStart.line); ok {
		return m.openArtifactViewer(artifact)
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
	if it.Kind == events.ItemDelegation && !m.clickIsDouble {
		// Find which task row was clicked based on line offset within the hit region.
		taskIdx := m.delegationTaskAtLine(m.selectionStart.line, idx)
		if taskIdx >= 0 {
			return m.openDelegationDetail(idx, taskIdx)
		}
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

func (m model) artifactAtLine(line int) (events.Artifact, bool) {
	for _, r := range m.artifactHits {
		if line < r.start || line >= r.end {
			continue
		}
		if r.itemIdx < 0 || r.itemIdx >= len(m.tr.Items) {
			return events.Artifact{}, false
		}
		artifacts := m.tr.Items[r.itemIdx].Artifacts
		if r.artifactIdx < 0 || r.artifactIdx >= len(artifacts) {
			return events.Artifact{}, false
		}
		return artifacts[r.artifactIdx], true
	}
	return events.Artifact{}, false
}

func (m model) openArtifactViewer(artifact events.Artifact) (tea.Model, tea.Cmd) {
	if artifact.Path == "" {
		return m, nil
	}
	m.fileViewer = fileViewerState{
		open:     true,
		loading:  true,
		artifact: artifact,
		vp:       viewport.New(max(20, m.width-4), max(5, m.screenHeight()-4)),
	}
	return m, m.loadArtifactContentCmd(artifact.Path)
}

func (m model) loadArtifactContentCmd(path string) tea.Cmd {
	sessionID := first(m.info.SessionID, m.tr.SessionID)
	return func() tea.Msg {
		content, err := m.backend.ReadArtifactContent(m.ctx, sessionID, path)
		if err != nil {
			return artifactContentLoadedMsg{path: path, err: err}
		}
		if content.Path == "" {
			content.Path = path
		}
		return artifactContentLoadedMsg{path: content.Path, content: content.Content}
	}
}

func (m model) applyArtifactContentLoaded(msg artifactContentLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.path == "" {
		msg.path = m.fileViewer.artifact.Path
	}
	if !m.fileViewer.open || msg.path != m.fileViewer.artifact.Path {
		return m, nil
	}
	m.fileViewer.loading = false
	if msg.err != nil {
		m.fileViewer.err = msg.err.Error()
	} else {
		m.fileViewer.content = msg.content
		m.fileViewer.err = ""
	}
	m.layoutFileViewer()
	return m, nil
}

func (m *model) layoutFileViewer() {
	if !m.fileViewer.open {
		return
	}
	w := max(20, m.width-4)
	h := max(5, m.screenHeight()-4)
	m.fileViewer.vp.Width = w
	m.fileViewer.vp.Height = h
	m.fileViewer.vp.SetContent(m.renderFileViewerContent(w))
}

func (m model) handleFileViewerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.fileViewer = fileViewerState{}
		return m, nil
	case "ctrl+c":
		m.quitting = true
		m.cancel()
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.fileViewer.vp, cmd = m.fileViewer.vp.Update(msg)
	return m, cmd
}

func (m model) handleFileViewerMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseButtonWheelUp && msg.Button != tea.MouseButtonWheelDown {
		return m, nil
	}
	var cmd tea.Cmd
	m.fileViewer.vp, cmd = m.fileViewer.vp.Update(msg)
	return m, cmd
}

// renderAgentMarkdown returns the rendered markdown for an agent message block,
// memoized by width+content. Markdown rendering (goldmark + chroma syntax
// highlighting) is the dominant per-event cost; caching finalized blocks keeps
// the UI loop from re-highlighting the entire transcript on every streamed
// event. The cache is bounded implicitly by the number of distinct
// (width, content) blocks in a session and cleared on resize.
func (m *model) renderAgentMarkdown(content string, width int) string {
	if m.mdCache == nil {
		m.mdCache = make(map[string]string)
	}
	// Key on width + content. A NUL separator avoids collisions between the
	// width digits and content. Streaming (last) blocks change content every
	// event, so they naturally get a fresh entry each time; finalized blocks
	// are stable and hit the cache.
	key := strconv.Itoa(width) + "\x00" + content
	if cached, ok := m.mdCache[key]; ok {
		return cached
	}
	out := render.Markdown(content, width, m.theme.RenderStyles())
	m.mdCache[key] = out
	return out
}

func (m *model) renderTranscript() (string, []hitRegion, []artifactHit) {
	var b strings.Builder
	var hits []hitRegion
	var artifactHits []artifactHit
	var plainLines []string
	var contentSpans [][2]int
	th := m.theme
	cw := contentWidth(m.width)
	lineNo := 0

	// appendPlainSpanned appends the plain (ANSI-stripped) lines of block along
	// with a content span for each line. spanFor maps a block-local line index
	// and its plain text to the [start,end) rune-column range that is real
	// content (excluding decorative chrome). Spans are shifted by the padBlock
	// margin so they line up with the padded plain lines used for selection.
	appendPlainSpanned := func(block string, spanFor func(i int, plain string) [2]int) {
		if block == "" {
			return
		}
		for i, line := range strings.Split(block, "\n") {
			plain := stripANSI(line)
			plainLines = append(plainLines, plain)
			total := len([]rune(plain))
			span := [2]int{0, total}
			if spanFor != nil {
				span = spanFor(i, plain)
			}
			span[0] = clamp(span[0], 0, total)
			span[1] = clamp(span[1], 0, total)
			if span[0] > span[1] {
				span[0], span[1] = span[1], span[0]
			}
			contentSpans = append(contentSpans, span)
		}
	}

	// appendBlockSpanned renders block into the transcript. spanFor (optional)
	// declares per-line content spans relative to the *padded* block so
	// drag-to-copy can exclude decorative chrome. When spanFor is nil the whole
	// line is treated as content.
	appendBlockSpanned := func(itemIdx int, kind events.ItemKind, block string, spanFor func(i int, plain string) [2]int) int {
		if block == "" {
			return lineNo
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
		appendPlainSpanned(padded, spanFor)
		gap := m.paintRow("", m.width)
		b.WriteString(gap)
		b.WriteString("\n") // vertical gap between messages
		plainLines = append(plainLines, stripANSI(gap))
		contentSpans = append(contentSpans, [2]int{0, 0})
		lineNo += n + 1 // block lines + one blank separator row
		hits = append(hits, hitRegion{start: start, end: start + n, itemIdx: itemIdx, kind: kind})
		return start
	}

	appendBlock := func(itemIdx int, kind events.ItemKind, block string) int {
		return appendBlockSpanned(itemIdx, kind, block, nil)
	}

	for i, it := range m.tr.Items {
		switch it.Kind {
		case events.ItemUser:
			appendBlockSpanned(i, it.Kind, m.renderUserBubble(it.Content, it.Expanded, cw), userBubbleContentSpan)
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
			md := m.renderAgentMarkdown(content, cw)
			if md == "" {
				md = th.Agent.Width(cw).Render(content)
			}
			appendBlock(i, it.Kind, md)
		case events.ItemThinking:
			appendBlock(i, it.Kind, m.renderThinkingBubble(it.Content, cw))
		case events.ItemActivity:
			appendBlock(i, it.Kind, m.renderActivity(it, cw))
		case events.ItemFileDiff:
			appendBlock(i, it.Kind, m.renderFileDiff(it, cw))
		case events.ItemSystem:
			appendBlock(i, it.Kind, th.System.Width(cw).Render(it.Content))
		case events.ItemError:
			appendBlock(i, it.Kind, th.Error.Width(cw).Render(it.Content))
		case events.ItemApproval:
			if it.ApprovalKind == "plan" {
				continue // plan approval shown in footer, not transcript
			}
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
			block, rows := m.renderArtifactList(it, cw)
			start := appendBlock(i, it.Kind, block)
			for _, row := range rows {
				artifactHits = append(artifactHits, artifactHit{
					start:       start + row.line,
					end:         start + row.line + 1,
					itemIdx:     i,
					artifactIdx: row.artifactIdx,
				})
			}
		case events.ItemDelegation:
			appendBlock(i, it.Kind, m.renderDelegationItem(it, cw))
		case events.ItemPlan:
			appendBlockSpanned(i, it.Kind, m.renderPlanDocument(it.Content, cw), planDocumentContentSpan)
		}
	}
	m.transcriptPlainLines = plainLines
	m.transcriptContentSpans = contentSpans
	return b.String(), hits, artifactHits
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

type artifactRow struct {
	line        int
	artifactIdx int
}

func (m model) renderArtifactList(it events.Item, width int) (string, []artifactRow) {
	th := m.theme
	artifacts := it.Artifacts
	if len(artifacts) == 0 && it.Path != "" {
		artifacts = []events.Artifact{{Path: it.Path, FileName: artifactDisplayName(events.Artifact{Path: it.Path})}}
	}
	if len(artifacts) == 0 {
		return "", nil
	}

	var b strings.Builder
	rows := make([]artifactRow, 0, len(artifacts))
	header := fmt.Sprintf("Files generated (%d)", len(artifacts))
	b.WriteString(th.Header.Render(header))
	b.WriteString(th.Muted.Render("  click to open · esc closes viewer"))
	for i, artifact := range artifacts {
		b.WriteByte('\n')
		line := fmt.Sprintf("  📄 %s", artifactDisplayName(artifact))
		meta := artifactMeta(artifact)
		if meta != "" {
			line += "  · " + meta
		}
		if artifact.IsReport {
			line += "  report"
		}
		if lipgloss.Width(line) > width {
			line = truncateToWidth(line, width)
		}
		b.WriteString(th.Text.Render(line))
		rows = append(rows, artifactRow{line: i + 1, artifactIdx: i})
	}
	return b.String(), rows
}

func artifactDisplayName(artifact events.Artifact) string {
	if artifact.ReportTitle != "" {
		return artifact.ReportTitle
	}
	if artifact.FileName != "" {
		return artifact.FileName
	}
	base := filepath.Base(artifact.Path)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return artifact.Path
	}
	return base
}

func artifactMeta(artifact events.Artifact) string {
	parts := make([]string, 0, 3)
	if artifact.FileType != "" && artifact.FileType != "File" {
		parts = append(parts, artifact.FileType)
	}
	if artifact.ToolName != "" {
		parts = append(parts, artifact.ToolName)
	}
	if artifact.Path != "" {
		parts = append(parts, artifact.Path)
	}
	return strings.Join(parts, " · ")
}

func artifactIsMarkdown(artifact events.Artifact) bool {
	if strings.EqualFold(artifact.FileType, "Markdown") {
		return true
	}
	switch strings.ToLower(filepath.Ext(artifact.Path)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return true
	default:
		return false
	}
}

func artifactLanguage(artifact events.Artifact) string {
	switch strings.ToLower(filepath.Ext(artifact.Path)) {
	case ".go":
		return "go"
	case ".js", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".py":
		return "python"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".html", ".htm":
		return "html"
	case ".css":
		return "css"
	case ".sh", ".bash", ".zsh":
		return "bash"
	case ".sql":
		return "sql"
	default:
		return strings.TrimPrefix(strings.ToLower(filepath.Ext(artifact.Path)), ".")
	}
}

func (m model) renderFileViewer() string {
	if !m.fileViewer.open {
		return ""
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderFileViewerHeader(),
		m.fileViewer.vp.View(),
	)
}

func (m model) renderFileViewerHeader() string {
	th := m.theme
	w := max(20, m.width)
	artifact := m.fileViewer.artifact
	left := "File · " + artifactDisplayName(artifact)
	if artifact.IsReport {
		left = "Report · " + artifactDisplayName(artifact)
	}
	left = truncateToWidth(left, max(8, w-32))
	right := "esc back · ↑↓ scroll"
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return m.paintRow(th.Header.Render(left)+strings.Repeat(" ", gap)+th.Muted.Render(right), w)
}

func (m model) renderFileViewerContent(width int) string {
	th := m.theme
	artifact := m.fileViewer.artifact
	bodyWidth := width - 4
	if bodyWidth < 20 {
		bodyWidth = width
	}
	var content string
	switch {
	case m.fileViewer.loading:
		content = th.Muted.Width(bodyWidth).Render("Loading file…")
	case m.fileViewer.err != "":
		content = th.Error.Width(bodyWidth).Render("Failed to load file: " + m.fileViewer.err)
	case artifactIsMarkdown(artifact):
		content = render.Markdown(m.fileViewer.content, bodyWidth, th.RenderStyles())
		if content == "" {
			content = th.Text.Width(bodyWidth).Render(m.fileViewer.content)
		}
	default:
		content = render.CodeBlock(m.fileViewer.content, artifactLanguage(artifact), bodyWidth, th.RenderStyles(), false)
	}
	return padBlock(content)
}

// renderFileDiff paints a main-thread single-gutter editor-style file change.
// Diffs live outside the tool activity fold so they stay visible while tools
// stay collapsed; the fold holds raw request/response only.
func (m model) renderFileDiff(it events.Item, width int) string {
	rs := m.theme.RenderStyles()
	name := it.ToolName
	if name == "" {
		name = "edit_file"
	}
	// Prefer verification_context from the tool (stored on the item).
	if it.DiffVerification != "" {
		if out := render.RenderVerificationDiff(it.DiffVerification, it.Path, width, true, m.workDir, rs); out != "" {
			return out
		}
	}
	// Fallback: build from args (old_string/new_string/content).
	return render.DiffFromToolArgs(name, it.Args, width, true, m.workDir, rs)
}

// renderActivity builds collapsed summary (+N/−M) and expanded raw tool detail.
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
		// Raw request (args) — file diffs are main-thread ItemFileDiff, not here.
		if argsPreview := render.ToolArgsPreview(step, width-6); argsPreview != "" {
			b.WriteByte('\n')
			b.WriteString(th.Muted.Width(width).Render("    request:"))
			for _, ln := range strings.Split(argsPreview, "\n") {
				b.WriteByte('\n')
				b.WriteString(th.Muted.Width(width).Render("    " + ln))
			}
		}
		// Raw response (result JSON / text).
		if preview := render.ToolResultPreview(step, width-6); preview != "" {
			b.WriteByte('\n')
			b.WriteString(th.Muted.Width(width).Render("    response:"))
			for _, ln := range strings.Split(preview, "\n") {
				b.WriteByte('\n')
				style := th.Muted
				if s.Status == "error" {
					style = th.Error
				}
				b.WriteString(style.Width(width).Render("    " + ln))
			}
		}
	}
	return b.String()
}

// userBubbleContentSpan returns the [start,end) rune-column range of real
// content within a rendered (padded) user-bubble line, excluding the box
// border, interior padding, and any embedded expand/collapse hint. It is used
// so drag-to-copy yields the prompt text alone rather than the surrounding
// chrome. Border rows (┌─┐ / └─┘) and any line without a pair of vertical
// borders contribute no copyable content.
//
// A padded body line looks like: "<margin>│  <content><pad>│". We locate the
// first and last vertical border rune, then trim the interior padding spaces
// that the bubble adds inside the borders.
func userBubbleContentSpan(_ int, plain string) [2]int {
	runes := []rune(plain)
	first := -1
	last := -1
	for i, r := range runes {
		if r == '│' {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	// Border/decoration rows (top/bottom) have no interior verticals, or only
	// the corner glyphs — either way there is no body content to copy.
	if first == -1 || last <= first {
		return [2]int{0, 0}
	}
	start := first + 1
	end := last
	// Trim the interior padding the bubble inserts inside the borders so the
	// copied text starts and ends at the actual content, not the spaces.
	for start < end && runes[start] == ' ' {
		start++
	}
	for end > start && runes[end-1] == ' ' {
		end--
	}
	return [2]int{start, end}
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

	border := m.theme.UserBorder
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
	if !m.tr.Awaiting && !m.sessions.open && !m.rollback.open && !m.modelPicker.open && !m.providerPicker.open && !m.webSearchPicker.open {
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

	// Overlays: sessions / model picker or approval card on top of the main chrome.
	if m.fileViewer.open {
		m.layoutFileViewer()
		return m.paintBackground(m.renderFileViewer())
	}
	if m.delegationDetail.open {
		m.layoutDelegationDetail()
		return m.paintBackground(m.renderDelegationDetail())
	}
	if m.sessions.open {
		overlay := m.renderSessionsOverlay()
		return m.paintBackground(lipgloss.Place(m.width, m.screenHeight(), lipgloss.Center, lipgloss.Center, overlay,
			lipgloss.WithWhitespaceChars(" "),
			lipgloss.WithWhitespaceBackground(lipgloss.Color("#000000")),
		))
	}
	if m.modelPicker.open {
		overlay := m.renderModelPickerOverlay()
		return m.paintBackground(lipgloss.Place(m.width, m.screenHeight(), lipgloss.Center, lipgloss.Center, overlay,
			lipgloss.WithWhitespaceChars(" "),
			lipgloss.WithWhitespaceBackground(lipgloss.Color("#000000")),
		))
	}
	if m.providerPicker.open {
		overlay := m.renderProviderPickerOverlay()
		return m.paintBackground(lipgloss.Place(m.width, m.screenHeight(), lipgloss.Center, lipgloss.Center, overlay,
			lipgloss.WithWhitespaceChars(" "),
			lipgloss.WithWhitespaceBackground(lipgloss.Color("#000000")),
		))
	}
	if m.webSearchPicker.open {
		overlay := m.renderWebSearchPickerOverlay()
		return m.paintBackground(lipgloss.Place(m.width, m.screenHeight(), lipgloss.Center, lipgloss.Center, overlay,
			lipgloss.WithWhitespaceChars(" "),
			lipgloss.WithWhitespaceBackground(lipgloss.Color("#000000")),
		))
	}
	if m.rollback.open {
		overlay := m.renderRollbackOverlay()
		return m.paintBackground(lipgloss.Place(m.width, m.screenHeight(), lipgloss.Center, lipgloss.Center, overlay,
			lipgloss.WithWhitespaceChars(" "),
			lipgloss.WithWhitespaceBackground(lipgloss.Color("#000000")),
		))
	}
	if m.tr.Awaiting {
		it := m.approvalItem()
		if it != nil && it.ApprovalKind == "plan" {
			// Plan approval: keep plan content visible, show compact options footer
			return m.paintBackground(lipgloss.JoinVertical(lipgloss.Left,
				m.renderHeader(),
				sep,
				m.vp.View(),
				sep,
				m.renderPlanApprovalFooter(),
				m.renderHints(),
			))
		}
		// Non-plan approvals keep the existing overlay behavior.
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

	// Session title shown centered between left and right sections.
	centerPlain := ""
	if m.tr != nil && m.tr.Title != "" {
		centerPlain = m.tr.Title
	}
	centerW := lipgloss.Width(centerPlain)

	// Compute available space for the center title.
	usedByEnds := leftW + rightW + 2 // minimum 1 space on each side of center
	availCenter := width - usedByEnds
	if availCenter < 0 {
		availCenter = 0
	}
	// Truncate center if needed.
	if centerW > availCenter {
		centerPlain = truncateToWidth(centerPlain, availCenter)
		centerW = lipgloss.Width(centerPlain)
	}

	// If total exceeds width, truncate left/right as before.
	totalNeeded := leftW + centerW + rightW + 2 // +2 for min gaps
	if totalNeeded > width {
		leftMax := width - rightW - centerW - 2
		if leftMax < 8 {
			rightPlain = truncateToWidth(rightPlain, max(0, width-9))
			rightW = lipgloss.Width(rightPlain)
			leftMax = width - rightW - centerW - 2
		}
		if leftMax < 1 {
			leftMax = 1
		}
		leftPlain = truncateToWidth(leftPlain, leftMax)
		leftW = lipgloss.Width(leftPlain)
	}

	left := m.renderHeaderLeft(leftPlain)
	right := th.Muted.Render(rightPlain)

	if centerPlain == "" {
		// No title: original two-column layout.
		gap := width - leftW - rightW
		if rightPlain != "" {
			if gap < 1 {
				gap = 1
			}
			return m.paintRow(left+strings.Repeat(" ", gap)+right, width)
		}
		return m.paintRow(left, width)
	}

	// Three-column layout: left ... center ... right
	center := th.Muted.Render(centerPlain)
	totalContent := leftW + centerW + rightW
	totalGap := width - totalContent
	if totalGap < 2 {
		totalGap = 2
	}
	leftGap := totalGap / 2
	rightGap := totalGap - leftGap
	return m.paintRow(left+strings.Repeat(" ", leftGap)+center+strings.Repeat(" ", rightGap)+right, width)
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
	var contextTokens int64
	if m.tr != nil {
		if m.tr.LastUsage != nil {
			usage = m.tr.LastUsage
		}
		contextTokens = m.tr.ContextTokens
	}

	// Context utilization: how full the model's context window is right now.
	// This is the metric that matters when coding (how much room is left before
	// compaction / truncation). Falls back to the latest turn's total tokens.
	if contextTokens <= 0 {
		contextTokens = usage.Total
	}

	if contextTokens <= 0 && usage.Total <= 0 {
		return "Context 0"
	}

	ctxPart := "Context " + formatTokenCount(contextTokens)
	if window := contextWindowFor(m.info.Model); window > 0 && contextTokens > 0 {
		pct := int(float64(contextTokens) / float64(window) * 100)
		if pct > 100 {
			pct = 100
		}
		ctxPart = fmt.Sprintf("Context %s/%s (%d%%)",
			formatTokenCount(contextTokens), formatTokenCount(window), pct)
	}

	if usage.Total <= 0 {
		return ctxPart
	}
	// Cumulative session usage is appended after the context figure but kept
	// short (total only) so the header stays on one line on narrow terminals;
	// the context figure is the primary, coding-relevant metric.
	return fmt.Sprintf("%s · Usage %s", ctxPart, formatTokenCount(usage.Total))
}

// contextWindowFor returns the approximate context-window size (in tokens) for a
// model name, or 0 when unknown. Matching is domain-agnostic: it keys off common
// family substrings in the model identifier rather than any single provider's
// catalog, so it works for local code mode across providers.
func contextWindowFor(model string) int64 {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return 0
	}
	// Ordered longest/most-specific first so e.g. "gpt-4o-mini" matches gpt-4o.
	families := []struct {
		match  string
		window int64
	}{
		{"claude", 200_000},
		{"gpt-5", 272_000},
		{"gpt-4.1", 1_047_576},
		{"gpt-4o", 128_000},
		{"gpt-4-turbo", 128_000},
		{"gpt-4", 128_000},
		{"o4", 200_000},
		{"o3", 200_000},
		{"o1", 200_000},
		{"gpt-3.5", 16_385},
		{"gemini-2.5", 1_048_576},
		{"gemini-1.5", 1_048_576},
		{"gemini", 1_048_576},
		{"llama-3", 128_000},
		{"llama", 128_000},
		{"mistral", 128_000},
		{"mixtral", 32_768},
		{"deepseek", 128_000},
		{"qwen", 128_000},
	}
	for _, f := range families {
		if strings.Contains(name, f.match) {
			return f.window
		}
	}
	return 0
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
		left := th.Status.Render(m.spin.View() + " " + first(m.tr.Status, "Working…"))
		if m.timerActive() && !m.tr.Awaiting {
			d := m.timerElapsed().Truncate(time.Second)
			right := th.Muted.Render(formatDuration(d))
			gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
			if gap < 1 {
				gap = 1
			}
			return m.paintRow(left+strings.Repeat(" ", gap)+right, m.width)
		}
		return m.paintRow(left, m.width)
	}
	if m.copyStatus != "" && time.Now().Before(m.copiedUntil) {
		return m.paintRow(th.Success.Render(m.copyStatus), m.width)
	}
	// Keep one row so chrome height is stable and painted with the app background.
	return m.paintRow("", m.width)
}

// renderDelegationItem draws an inline sub-task delegation tracker showing each
// delegated task with a status indicator, name, and elapsed/completed time.
// Returns an empty string when there are no tasks (defensive; shouldn't happen).
func (m model) renderDelegationItem(it events.Item, width int) string {
	tasks := it.DelegationTasks
	if len(tasks) == 0 {
		return ""
	}
	th := m.theme
	w := width
	if w < 30 {
		w = 30
	}

	// Header line.
	header := fmt.Sprintf(" Delegating %d tasks ", len(tasks))

	var lines []string
	for _, task := range tasks {
		var icon string
		var nameStyle lipgloss.Style
		var timeStr string

		switch task.Status {
		case "complete":
			icon = th.Success.Render("✓")
			nameStyle = th.Muted
			timeStr = task.Duration
		case "failed":
			icon = th.Error.Render("✗")
			nameStyle = th.Error
			timeStr = task.Duration
		default: // "running"
			icon = th.Brand.Render("●")
			nameStyle = th.Text
			if !task.StartedAt.IsZero() {
				elapsed := time.Since(task.StartedAt).Truncate(time.Second)
				timeStr = formatDuration(elapsed)
			}
		}

		// Truncate name to fit: icon(2) + name + status(10) + time(8) + spacing(6)
		maxName := w - 26
		if maxName < 8 {
			maxName = 8
		}
		name := task.Name
		if len(name) > maxName {
			name = name[:maxName-1] + "…"
		}

		// Build the row: "  ● task-name          running   12s"
		renderedName := nameStyle.Render(name)
		nameWidth := lipgloss.Width(renderedName)
		pad := maxName - nameWidth
		if pad < 0 {
			pad = 0
		}

		statusLabel := task.Status
		if len(statusLabel) > 8 {
			statusLabel = statusLabel[:8]
		}
		renderedStatus := th.Muted.Render(statusLabel)
		renderedTime := th.Muted.Render(timeStr)

		row := fmt.Sprintf("  %s %s%s  %s  %s",
			icon, renderedName, strings.Repeat(" ", pad), renderedStatus, renderedTime)
		lines = append(lines, row)

		// Show inline activity status for running tasks.
		if statusLine := delegationTaskStatusLine(task, w-6); statusLine != "" {
			lines = append(lines, "      "+th.Muted.Italic(true).Render(statusLine))
		}
	}

	return th.Brand.Render(header) + "\n" + strings.Join(lines, "\n") + "\n" +
		th.Muted.Italic(true).Render("    click task to expand details")
}

// delegationTaskStatusLine returns a short, human-friendly inline status for a
// running task's latest activity (e.g. "→ Reading main.go" or "→ thinking…").
func delegationTaskStatusLine(task events.DelegationTaskState, maxWidth int) string {
	if task.Status != "running" || len(task.Activity) == 0 {
		return ""
	}
	last := task.Activity[len(task.Activity)-1]
	var line string
	switch last.Type {
	case "tool_call":
		line = "→ " + delegationToolHint(last.ToolName, last.Args)
	case "tool_result":
		line = "→ " + render.ToolDisplayName(last.ToolName) + " done"
	case "text":
		t := strings.TrimSpace(last.Text)
		if idx := strings.IndexByte(t, '\n'); idx >= 0 {
			t = t[:idx]
		}
		if t == "" {
			return ""
		}
		line = "→ " + t
	default:
		return ""
	}
	if maxWidth > 0 && len([]rune(line)) > maxWidth {
		runes := []rune(line)
		line = string(runes[:maxWidth-1]) + "…"
	}
	return line
}

// delegationToolHint produces a human-friendly label for a tool call, including
// context from the args (file path, command, query) — e.g. "Editing pkg/app.go",
// "Running `kubectl get pods`", "Searching for kubernetes".
func delegationToolHint(toolName string, args map[string]any) string {
	name := strings.ToLower(toolName)
	path := delegationArgStr(args, "path", "file_path", "target_file", "file", "filename")
	command := delegationArgStr(args, "command", "cmd")
	query := delegationArgStr(args, "query", "pattern", "regex", "search")

	switch {
	case name == "shell_command" || name == "run_terminal_command" || name == "process_write":
		if command != "" {
			return "Running `" + delegationTruncate(command, 40) + "`"
		}
		return "Running command"
	case name == "edit_file" || name == "search_replace":
		if path != "" {
			return "Editing " + delegationTruncate(path, 48)
		}
		return "Editing file"
	case name == "write_file":
		if path != "" {
			return "Writing " + delegationTruncate(path, 48)
		}
		return "Writing file"
	case name == "read_file" || name == "read_pdf":
		if path != "" {
			return "Reading " + delegationTruncate(path, 48)
		}
		return "Reading file"
	case name == "file_tree" || name == "find_files" || name == "repo_map" ||
		name == "code_definition" || name == "code_references" || name == "list_dir":
		if path != "" {
			return "Exploring " + delegationTruncate(path, 48)
		}
		return "Exploring"
	case name == "grep_search" || name == "grep" || name == "search_tools" || name == "search_flows":
		if query != "" {
			return "Searching for \"" + delegationTruncate(query, 36) + "\""
		}
		return "Searching"
	case name == "web_search" || name == "perplexity_web_search":
		if query != "" {
			return "Searching web for \"" + delegationTruncate(query, 32) + "\""
		}
		return "Searching web"
	case name == "web_fetch" || name == "http_request":
		if path != "" {
			return "Fetching " + delegationTruncate(path, 48)
		}
		return "Fetching"
	case name == "browser_navigate":
		if path != "" {
			return "Navigating to " + delegationTruncate(path, 44)
		}
		return "Navigating"
	case strings.HasPrefix(name, "browser_"):
		return "Browsing"
	case strings.HasPrefix(name, "memory_"):
		if query != "" {
			return "Looking up \"" + delegationTruncate(query, 36) + "\""
		}
		return "Looking up memory"
	case name == "delegate_tasks":
		return "Delegating sub-tasks"
	case name == "announce_plan" || name == "update_plan":
		return "Planning"
	case name == "codegraph_explore":
		if query != "" {
			return "Exploring code for \"" + delegationTruncate(query, 32) + "\""
		}
		return "Exploring code graph"
	}
	// Fallback: use the display name with any available subject
	display := render.ToolDisplayName(toolName)
	if path != "" {
		return display + " " + delegationTruncate(path, 40)
	}
	if command != "" {
		return display + " `" + delegationTruncate(command, 38) + "`"
	}
	if query != "" {
		return display + " \"" + delegationTruncate(query, 38) + "\""
	}
	return display
}

func delegationArgStr(args map[string]any, keys ...string) string {
	if args == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func delegationTruncate(s string, max int) string {
	// Collapse whitespace and take first line only.
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	if len([]rune(s)) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max-1]) + "…"
}

// --- Delegation detail overlay ---

// delegationTaskAtLine maps a content line to a task row index within the
// delegation item. Header is line 0, each task is line 1+i. Returns -1 if the
// click is not on a task row.
func (m model) delegationTaskAtLine(line int, itemIdx int) int {
	for _, r := range m.hitRegions {
		if r.itemIdx == itemIdx && r.kind == events.ItemDelegation {
			offset := line - r.start
			// offset 0 = header line ("Delegating N tasks")
			if offset < 1 {
				return -1
			}
			tasks := m.tr.Items[itemIdx].DelegationTasks
			currentLine := 1 // start after header
			for i, task := range tasks {
				if offset == currentLine {
					return i
				}
				currentLine++ // task row
				// Running tasks with activity have an extra status line
				if task.Status == "running" && len(task.Activity) > 0 {
					if offset == currentLine {
						return i // clicking on status line selects the same task
					}
					currentLine++
				}
			}
			return -1
		}
	}
	return -1
}

func (m model) openDelegationDetail(itemIdx, taskIdx int) (tea.Model, tea.Cmd) {
	tasks := m.tr.Items[itemIdx].DelegationTasks
	if taskIdx < 0 || taskIdx >= len(tasks) {
		return m, nil
	}
	w := max(20, m.width-4)
	h := max(5, m.screenHeight()-4)
	vp := viewport.New(w, h)
	vp.SetContent(m.renderDelegationDetailContent(tasks[taskIdx], w))
	vp.GotoBottom()
	m.delegationDetail = delegationDetailState{
		open:     true,
		taskName: tasks[taskIdx].Name,
		taskIdx:  taskIdx,
		itemIdx:  itemIdx,
		vp:       vp,
	}
	return m, nil
}

func (m *model) layoutDelegationDetail() {
	if !m.delegationDetail.open {
		return
	}
	w := max(20, m.width-4)
	h := max(5, m.screenHeight()-4)
	m.delegationDetail.vp.Width = w
	m.delegationDetail.vp.Height = h
	// Refresh content from current task state.
	if m.delegationDetail.itemIdx >= 0 && m.delegationDetail.itemIdx < len(m.tr.Items) {
		tasks := m.tr.Items[m.delegationDetail.itemIdx].DelegationTasks
		if m.delegationDetail.taskIdx >= 0 && m.delegationDetail.taskIdx < len(tasks) {
			atBottom := m.delegationDetail.vp.AtBottom()
			m.delegationDetail.vp.SetContent(m.renderDelegationDetailContent(tasks[m.delegationDetail.taskIdx], w))
			if atBottom {
				m.delegationDetail.vp.GotoBottom()
			}
		}
	}
}

func (m model) handleDelegationDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.delegationDetail = delegationDetailState{}
		return m, nil
	case "ctrl+c":
		m.quitting = true
		m.cancel()
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.delegationDetail.vp, cmd = m.delegationDetail.vp.Update(msg)
	return m, cmd
}

func (m model) handleDelegationDetailMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseButtonWheelUp && msg.Button != tea.MouseButtonWheelDown {
		return m, nil
	}
	var cmd tea.Cmd
	m.delegationDetail.vp, cmd = m.delegationDetail.vp.Update(msg)
	return m, cmd
}

func (m model) renderDelegationDetail() string {
	if !m.delegationDetail.open {
		return ""
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderDelegationDetailHeader(),
		m.delegationDetail.vp.View(),
	)
}

func (m model) renderDelegationDetailHeader() string {
	th := m.theme
	w := max(20, m.width)
	name := m.delegationDetail.taskName
	left := "⬡ " + name
	right := "esc back · ↑↓ scroll"
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return m.paintRow(th.Header.Render(left)+strings.Repeat(" ", gap)+th.Muted.Render(right), w)
}

func (m model) renderDelegationDetailContent(task events.DelegationTaskState, width int) string {
	th := m.theme
	bodyWidth := width - 4
	if bodyWidth < 20 {
		bodyWidth = width
	}

	var b strings.Builder

	// Task info header.
	var statusIcon string
	switch task.Status {
	case "complete":
		statusIcon = th.Success.Render("✓")
	case "failed":
		statusIcon = th.Error.Render("✗")
	default:
		statusIcon = th.Brand.Render("●")
	}

	var timeStr string
	if task.Duration != "" {
		timeStr = task.Duration
	} else if !task.StartedAt.IsZero() {
		timeStr = formatDuration(time.Since(task.StartedAt).Truncate(time.Second))
	}

	b.WriteString(fmt.Sprintf("  %s %s  %s  %s\n",
		statusIcon,
		th.Text.Bold(true).Render(task.Name),
		th.Muted.Render(task.Status),
		th.Muted.Render(timeStr)))

	if task.Description != "" {
		b.WriteString("  " + th.Muted.Render(task.Description) + "\n")
	}
	b.WriteString("\n")

	// Activity log.
	if len(task.Activity) == 0 {
		b.WriteString("  " + th.Muted.Italic(true).Render("No activity yet…") + "\n")
		return b.String()
	}

	for _, act := range task.Activity {
		switch act.Type {
		case "tool_call":
			argSummary := summarizeToolArgs(act.Args, bodyWidth-20)
			b.WriteString(fmt.Sprintf("  %s %s\n",
				th.Brand.Render("●"),
				th.Text.Render(act.ToolName+"("+argSummary+")")))
		case "tool_result":
			resultStr := summarizeToolResult(act.Result, bodyWidth-10)
			b.WriteString(fmt.Sprintf("  %s %s\n",
				th.Success.Render("✓"),
				th.Muted.Render(act.ToolName)))
			if resultStr != "" {
				// Indent result preview.
				for _, line := range strings.Split(resultStr, "\n") {
					b.WriteString("      " + th.Muted.Render(line) + "\n")
				}
			}
		case "text":
			text := act.Text
			if len(text) > 200 {
				text = text[:197] + "…"
			}
			b.WriteString("  " + th.Text.Render(text) + "\n")
		}
	}

	return padBlock(b.String())
}

// summarizeToolArgs produces a compact one-line summary of tool arguments.
func summarizeToolArgs(args map[string]any, maxWidth int) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		v := args[k]
		s := fmt.Sprintf("%v", v)
		if len(s) > 40 {
			s = s[:37] + "…"
		}
		parts = append(parts, k+": "+s)
	}
	result := strings.Join(parts, ", ")
	if len(result) > maxWidth && maxWidth > 3 {
		result = result[:maxWidth-1] + "…"
	}
	return result
}

// summarizeToolResult produces a compact preview of a tool result.
func summarizeToolResult(result any, maxWidth int) string {
	if result == nil {
		return ""
	}
	s := fmt.Sprintf("%v", result)
	lines := strings.Split(s, "\n")
	if len(lines) > 3 {
		lines = append(lines[:3], "…")
	}
	for i, line := range lines {
		if len(line) > maxWidth && maxWidth > 3 {
			lines[i] = line[:maxWidth-1] + "…"
		}
	}
	return strings.Join(lines, "\n")
}
// and replaces markdown checkboxes with colored status indicators.
func (m model) renderPlanDocument(content string, width int) string {
	th := m.theme
	if width < 30 {
		width = 30
	}
	inner := width - 6 // left border + 2 padding + right border + 2 padding
	if inner < 20 {
		inner = 20
	}

	// Extract goal from content for the header.
	title := "Execution Plan"
	for _, line := range strings.SplitN(content, "\n", 10) {
		if strings.HasPrefix(line, "**Goal:**") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "**Goal:**"))
			if len(title) > inner-10 {
				title = title[:inner-13] + "…"
			}
			break
		}
	}

	// Render the interior markdown content (skipping the "# Execution Plan" header
	// since we show it in the frame border).
	body := content
	if strings.HasPrefix(body, "# Execution Plan\n") {
		body = strings.TrimPrefix(body, "# Execution Plan\n")
	}
	body = strings.TrimSpace(body)

	// Render interior as markdown.
	md := render.Markdown(body, inner, th.RenderStyles())
	if md == "" {
		md = th.Text.Width(inner).Render(body)
	}

	// Post-process: replace checkbox markers with colored status indicators.
	md = m.stylePlanCheckboxes(md)

	// Build the bordered frame.
	var b strings.Builder

	// Top border: ┌─ ✦ Title ─────────────────┐
	titleRendered := th.PlanHeader.Render(" ✦ " + title + " ")
	titleW := lipgloss.Width(titleRendered)
	topLineW := width - 2 - titleW // minus ┌ and ┐
	leftW := 1                      // one ─ before title
	rightW := topLineW - leftW
	if rightW < 0 {
		rightW = 0
	}
	b.WriteString(th.PlanBorder.Render("┌"+strings.Repeat("─", leftW)) +
		titleRendered +
		th.PlanBorder.Render(strings.Repeat("─", rightW)+"┐"))

	// Body rows with side borders.
	bodyLines := strings.Split(md, "\n")
	for _, line := range bodyLines {
		b.WriteByte('\n')
		lineW := lipgloss.Width(line)
		pad := inner - lineW
		if pad < 0 {
			pad = 0
			line = truncateToWidth(line, inner)
		}
		b.WriteString(th.PlanBorder.Render("│"))
		b.WriteString(th.Background.Render("  "))
		b.WriteString(line)
		b.WriteString(th.Background.Render(strings.Repeat(" ", pad)))
		b.WriteString(th.Background.Render("  "))
		b.WriteString(th.PlanBorder.Render("│"))
	}

	// Bottom border: └──────────────────────────┘
	b.WriteByte('\n')
	b.WriteString(th.PlanBorder.Render("└" + strings.Repeat("─", width-2) + "┘"))

	return b.String()
}

// stylePlanCheckboxes replaces plain markdown checkbox markers in rendered plan
// text with colored status indicators for visual clarity.
func (m model) stylePlanCheckboxes(rendered string) string {
	th := m.theme
	// Replace status markers: [x]=complete, [~]=running, [ ]=pending, [!]=failed
	rendered = strings.ReplaceAll(rendered, "[x]", th.Success.Render("[✓]"))
	rendered = strings.ReplaceAll(rendered, "[X]", th.Success.Render("[✓]"))
	rendered = strings.ReplaceAll(rendered, "[~]", th.Brand.Render("[●]"))
	rendered = strings.ReplaceAll(rendered, "[ ]", th.PlanMuted.Render("[○]"))
	rendered = strings.ReplaceAll(rendered, "[!]", th.Error.Render("[✗]"))
	return rendered
}

// planDocumentContentSpan returns the [start,end) rune-column range of real
// content within a rendered plan document line, excluding the border characters
// and interior padding. Used for drag-to-copy selection.
func planDocumentContentSpan(_ int, plain string) [2]int {
	runes := []rune(plain)
	first := -1
	last := -1
	for i, r := range runes {
		if r == '│' {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	// Border/decoration rows (top/bottom) have no vertical bars or only corners.
	if first == -1 || last <= first {
		return [2]int{0, 0}
	}
	start := first + 1
	end := last
	// Trim interior padding.
	for start < end && runes[start] == ' ' {
		start++
	}
	for end > start && runes[end-1] == ' ' {
		end--
	}
	return [2]int{start, end}
}

// formatDuration renders a duration as a compact human-readable string:
// "3s", "1m 23s", "1h 5m 12s".
func formatDuration(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	s = s % 60
	if m < 60 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := m / 60
	m = m % 60
	return fmt.Sprintf("%dh %dm %ds", h, m, s)
}

// ---------------------------------------------------------------------------
// Timer helpers — pause/resume across HITL approval boundaries
// ---------------------------------------------------------------------------

// timerElapsed returns the total elapsed execution time for the current logical
// turn, combining any accumulated time from prior segments with the current
// running segment (if the timer is actively ticking).
func (m model) timerElapsed() time.Duration {
	if !m.turnStartedAt.IsZero() {
		return m.timerAccumulated + time.Since(m.turnStartedAt)
	}
	return m.timerAccumulated
}

// timerPause freezes the timer without losing accumulated time. Called when a
// HITL approval suspends execution. No-op if the timer is already paused.
func (m *model) timerPause() {
	if !m.turnStartedAt.IsZero() {
		m.timerAccumulated += time.Since(m.turnStartedAt)
		m.turnStartedAt = time.Time{}
	}
}

// timerResume restarts the clock from the paused state, preserving accumulated
// time. Called when the user approves a HITL request and execution continues.
func (m *model) timerResume() {
	m.turnStartedAt = time.Now()
}

// timerReset zeros both timer fields. Used at the true end of a turn and on cancel.
func (m *model) timerReset() {
	m.turnStartedAt = time.Time{}
	m.timerAccumulated = 0
}

// timerStart begins a brand-new timer for a fresh turn, clearing any prior
// accumulated time.
func (m *model) timerStart() {
	m.turnStartedAt = time.Now()
	m.timerAccumulated = 0
}

// timerRunning reports whether the timer is actively ticking (not paused).
func (m model) timerRunning() bool {
	return !m.turnStartedAt.IsZero()
}

// timerActive reports whether a logical turn timer is in progress — either
// actively running or paused with accumulated time from a prior segment.
func (m model) timerActive() bool {
	return !m.turnStartedAt.IsZero() || m.timerAccumulated > 0
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
	label := m.composerModeLabel()

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
	var color lipgloss.Color
	if m.graphPlanMode {
		color = lipgloss.Color("172") // amber — plan mode accent
	} else if m.planMode {
		color = lipgloss.Color("172")
	} else if m.askMode {
		color = lipgloss.Color("114") // green/teal — research mode
	} else {
		// Normal mode: use the neutral composer border color, not the brand accent.
		color = lipgloss.Color("246")
		if m.info.Mode == "platform" {
			color = lipgloss.Color("39") // platform uses cyan even in normal mode
		}
	}
	return lipgloss.NewStyle().Foreground(color).Background(lipgloss.Color("#000000"))
}

// composerModeLabel returns the text shown in the composer bottom border.
// In dual-mode it prefixes the backend type; in single mode it shows just the
// plan-mode label (matching existing behavior).
func (m model) composerModeLabel() string {
	if m.info.Mode == "platform" {
		if m.planMode {
			return "Platform · Plan"
		}
		return "Platform"
	}
	sub := "Normal"
	if m.graphPlanMode {
		sub = "Plan"
	} else if m.planMode {
		sub = "Plan"
	} else if m.askMode {
		sub = "Ask"
	}
	if m.dualMode {
		return "Code · " + sub
	}
	return sub
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

// cancelInFlightTurn aborts the active RunTurn when one is streaming. Returns
// handled=true when a turn was cancelled. Callers that want quit-on-idle
// (ctrl+c) check handled and fall through to quit; Esc leaves the app running.
func (m model) cancelInFlightTurn() (tea.Model, tea.Cmd, bool) {
	if !m.tr.Streaming || m.turnCancel == nil {
		return m, nil, false
	}
	m.turnCancel()
	m.turnCancel = nil
	m.timerReset()
	m.tr.Streaming = false
	m.tr.Status = ""
	m.tr.Apply(events.NewSystem("Turn cancelled."))
	m.refreshViewport()
	return m, nil, true
}

func (m model) renderHints() string {
	th := m.theme
	if m.tr.Awaiting {
		return th.Hint.Render("y approve  ·  n deny  ·  1/2 select  ·  esc deny")
	}
	if m.tr.Streaming {
		return m.paintRow(th.Hint.Render("esc cancel  ·  ↑↓ scroll  ·  ctrl+c cancel"), m.width)
	}
	if m.slash.active {
		return th.Hint.Render("↑↓ select  ·  enter run  ·  tab next  ·  esc close")
	}
	if m.files.active {
		return th.Hint.Render("↑↓ select  ·  enter attach file  ·  tab next  ·  esc close")
	}

	// Build context-aware hints depending on mode and dual-mode availability.
	var full, short string
	switch {
	case m.dualMode && m.info.Mode == "platform":
		full = "Enter send  ·  / commands  ·  shift+tab plan  ·  ctrl+\\ code  ·  ctrl+l sessions  ·  shift+enter newline  ·  ctrl+c quit"
		short = "Enter send  ·  / commands  ·  shift+tab plan  ·  ctrl+\\ code"
	case m.dualMode:
		full = "Enter send  ·  / commands  ·  @ files  ·  shift+tab plan  ·  ctrl+\\ platform  ·  shift+enter newline  ·  ctrl+c quit"
		short = "Enter send  ·  / commands  ·  shift+tab plan  ·  ctrl+\\ platform"
	default:
		full = "Enter send  ·  / commands  ·  @ files  ·  shift+tab plan  ·  shift+enter newline  ·  ctrl+l sessions  ·  ctrl+c quit"
		short = "Enter send  ·  / commands  ·  @ files  ·  shift+tab plan"
	}

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
