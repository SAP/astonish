package tui

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

type botFilesMode uint8

const (
	botFilesList botFilesMode = iota
	botFilesPreview
	botFilesSave
)

type botFileRow struct {
	entry store.BotFileEntry
	ref   client.BotFileRef
}
type botFilesState struct {
	mode                                       botFilesMode
	root                                       client.BotFileRef
	rows                                       []botFileRow
	next, cursor, content, destination, notice string
	published                                  bool
	scroll                                     int
	cancel                                     context.CancelFunc
}
type botFilesMsg struct {
	generation uint64
	request    uuid.UUID
	root       client.BotFileRef
	state      botFilesState
	err        error
}

func (m *model) cancelBotFiles() {
	if m.bots.files != nil && m.bots.files.cancel != nil {
		m.bots.files.cancel()
		m.bots.files.cancel = nil
	}
}
func (m model) openBotFiles() (tea.Model, tea.Cmd) {
	if _, ok := m.backend.(backend.BotFilesBackend); !ok || !m.botSelected() {
		m.tr.Apply(events.NewSystem("Select a remote Bot first; Bot files unavailable on this backend. /files remains local."))
		m.refreshViewport()
		return m, nil
	}
	m.cancelBotFiles()
	b := m.botCap().SelectedBot()
	m.bots = botsState{open: true, files: &botFilesState{root: client.BotFileRef{BotID: b.ID, HomeID: b.Home.ID, HomeSessionID: b.HomeSessionID}}}
	m.slash = slashCompletion{}
	return m.loadBotFiles()
}
func (m model) loadBotFiles() (tea.Model, tea.Cmd) {
	cap, ok := m.backend.(backend.BotFilesBackend)
	if !ok {
		return m, nil
	}
	m.cancelBotFiles()
	state := *m.bots.files
	ctx, cancel := context.WithCancel(m.ctx)
	state.cancel = cancel
	uiState := state
	m.bots.files = &uiState
	m.bots.loading = true
	m.bots.err = ""
	m.bots.requestID = uuid.New()
	gen, request := m.botGeneration, m.bots.requestID
	// Capture immutable publication references from this transcript; never paths
	// from ordinary Chat artifacts or private storage keys.
	var published []botFileRow
	if state.published {
		seen := map[uuid.UUID]bool{}
		for _, item := range m.tr.Items {
			if p := item.BotFilePublication; p != nil && p.BotID == state.root.BotID && p.HomeID == state.root.HomeID && p.HomeSessionID == state.root.HomeSessionID {
				for _, a := range p.Artifacts {
					if seen[a.ID] {
						continue
					}
					seen[a.ID] = true
					ref := state.root
					ref.Path = a.RelativePath
					ref.RunID = p.RunID
					ref.ReportSequence = p.ReportSequence
					ref.ArtifactID = a.ID
					ref.SHA256 = a.SHA256
					published = append(published, botFileRow{entry: store.BotFileEntry{Path: a.RelativePath, Kind: store.BotFileRegular, SizeBytes: a.SizeBytes, Preview: a.Preview}, ref: ref})
				}
			}
		}
	}
	return m, func() tea.Msg {
		out := botFilesMsg{generation: gen, request: request, root: state.root, state: state}
		switch state.mode {
		case botFilesPreview:
			if len(state.rows) == 0 {
				return out
			}
			var p *client.BotFilePreview
			p, out.err = cap.ReadBotFilePreview(ctx, state.rows[m.bots.cursor].ref)
			if out.err == nil && p != nil {
				out.state.content = p.Content
				if p.Truncated {
					out.state.content += "\n[Preview truncated; save for full file]"
				}
			}
		case botFilesSave:
			out.err = cap.DownloadBotFile(ctx, state.rows[m.bots.cursor].ref, state.destination)
			if out.err == nil {
				out.state.notice = "Saved to " + state.destination
				out.state.mode = botFilesList
			}
		default:
			out.state.rows = nil
			if state.published {
				out.state.rows = published
				break
			}
			var page *store.BotFilePage
			page, out.err = cap.ListBotFiles(ctx, state.root, state.cursor)
			if out.err == nil && page != nil {
				out.state.next = page.Next
				for _, e := range page.Entries {
					ref := state.root
					ref.Path = e.Path
					ref.Freshness = e.Freshness
					ref.SHA256 = e.SHA256
					out.state.rows = append(out.state.rows, botFileRow{entry: e, ref: ref})
				}
			}
		}
		return out
	}
}
func (m model) applyBotFiles(v botFilesMsg) (tea.Model, tea.Cmd) {
	if v.generation != m.botGeneration || !m.bots.open || m.bots.files == nil || v.request != m.bots.requestID || v.root != m.bots.files.root {
		return m, nil
	}
	b := m.botCap().SelectedBot()
	if b == nil || b.ID != v.root.BotID || b.Home.ID != v.root.HomeID || b.HomeSessionID != v.root.HomeSessionID {
		m.cancelBotFiles()
		m.bots = botsState{}
		return m, nil
	}
	m.bots.loading = false
	if v.err != nil {
		m.bots.err = botFilesError(v.err)
		var response *client.BotResponseError
		if errors.Is(v.err, store.ErrBotConflict) || (errors.As(v.err, &response) && (response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 404 || response.StatusCode == 409 || response.StatusCode == 410)) {
			m.cancelBotFiles()
			m.bots.files.rows = nil
			m.bots.files.content = ""
			m.bots.files.destination = ""
			m.bots.files.notice = ""
			m.bots.files.next = ""
			m.bots.files.mode = botFilesList
			m.bots.cursor = 0
		}
		return m, nil
	}
	m.bots.files = &v.state
	if v.state.mode == botFilesList {
		m.bots.cursor = 0
	}
	return m, nil
}
func botFilesError(err error) string {
	var e *client.BotResponseError
	if errors.As(err, &e) {
		switch e.StatusCode {
		case 401, 403:
			return "Permission unavailable. Sign in or check Bot access."
		case 404, 409, 410:
			return "Home unprepared, file missing or changed. Refresh to retry."
		case 415:
			return "Preview unsupported. Press d to save instead."
		case 501:
			return "Files unsupported by this backend."
		}
	}
	return "Files unavailable: " + err.Error() + " (r retries read-only)"
}
func (m model) handleBotFilesKey(key string) (tea.Model, tea.Cmd) {
	b := m.botCap().SelectedBot()
	root := m.bots.files.root
	if b == nil || b.ID != root.BotID || b.Home.ID != root.HomeID || b.HomeSessionID != root.HomeSessionID {
		m.cancelBotFiles()
		m.bots = botsState{}
		return m, nil
	}
	state := *m.bots.files
	m.bots.files = &state
	if key == "esc" || key == "ctrl+c" {
		m.cancelBotFiles()
		m.bots.requestID = uuid.New()
		m.bots.loading = false
		m.bots.err = ""
		if state.mode != botFilesList {
			state.mode = botFilesList
			state.content = ""
			state.destination = ""
			return m, nil
		}
		m.bots = botsState{}
		return m, nil
	}
	if m.bots.loading {
		return m, nil
	}
	if state.mode == botFilesSave {
		switch key {
		case "enter":
			if state.destination != "" {
				return m.loadBotFiles()
			}
		case "backspace":
			r := []rune(state.destination)
			if len(r) > 0 {
				state.destination = string(r[:len(r)-1])
			}
		default:
			if len([]rune(key)) == 1 && !unicode.IsControl([]rune(key)[0]) {
				state.destination += key
			}
		}
		return m, nil
	}
	switch key {
	case "r":
		state.mode = botFilesList
		state.cursor = ""
		return m.loadBotFiles()
	case "p":
		state.mode = botFilesList
		state.published = !state.published
		state.cursor = ""
		state.root.Path = ""
		return m.loadBotFiles()
	case "n":
		if state.mode == botFilesList && state.next != "" && !state.published {
			state.cursor = state.next
			return m.loadBotFiles()
		}
	case "backspace", "left":
		if state.mode == botFilesList && !state.published {
			state.root.Path = path.Dir(state.root.Path)
			if state.root.Path == "." {
				state.root.Path = ""
			}
			state.cursor = ""
			return m.loadBotFiles()
		}
	case "up", "k":
		if state.mode == botFilesPreview {
			state.scroll = max(0, state.scroll-1)
		} else {
			m.bots.cursor = max(0, m.bots.cursor-1)
		}
	case "down", "j":
		if state.mode == botFilesPreview {
			state.scroll++
		} else {
			m.bots.cursor = min(max(0, len(state.rows)-1), m.bots.cursor+1)
		}
	case "enter", "d":
		if len(state.rows) == 0 {
			return m, nil
		}
		row := state.rows[m.bots.cursor]
		if row.entry.Kind == store.BotFileDirectory {
			if key == "enter" {
				state.root.Path = row.entry.Path
				state.cursor = ""
				return m.loadBotFiles()
			}
			return m, nil
		}
		if key == "d" || row.entry.Preview != store.BotFilePreviewText {
			state.mode = botFilesSave
			state.destination = ""
			m.bots.err = ""
			return m, nil
		}
		state.mode = botFilesPreview
		state.scroll = 0
		return m.loadBotFiles()
	}
	return m, nil
}

// Remote text is never allowed to execute terminal control sequences.
func botFileDisplay(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}
func (m model) renderBotFiles() string {
	state := m.bots.files
	var s strings.Builder
	title := "Bot files — retained Home"
	if state.published {
		title = "Bot files — immutable published files"
	}
	fmt.Fprintf(&s, "%s\n%s\n\n", title, botFileDisplay(state.root.Path))
	switch {
	case m.bots.loading:
		s.WriteString("Loading… (Esc cancels this read/save only)")
	case m.bots.err != "":
		s.WriteString(botFileDisplay(m.bots.err))
		if state.mode == botFilesSave {
			s.WriteString("\nSave to a new local destination:\n" + botFileDisplay(state.destination) + "\nEdit destination · Enter confirms · Esc cancels")
		}
	case state.mode == botFilesSave:
		s.WriteString("Save to a new local destination (never overwrite):\n" + botFileDisplay(state.destination) + "\nEnter confirms · Esc cancels")
	case state.mode == botFilesPreview:
		lines := strings.Split(botFileDisplay(state.content), "\n")
		start := min(state.scroll, max(0, len(lines)-1))
		end := min(len(lines), start+max(3, m.height-14))
		s.WriteString(strings.Join(lines[start:end], "\n"))
	default:
		if len(state.rows) == 0 {
			s.WriteString("No files.")
		}
		start := max(0, m.bots.cursor-3)
		for i := start; i < len(state.rows) && i < start+max(3, m.height-14); i++ {
			row := state.rows[i]
			mark := "  "
			if i == m.bots.cursor {
				mark = "> "
			}
			fmt.Fprintf(&s, "%s%s [%s, %d bytes]\n", mark, botFileDisplay(row.entry.Path), row.entry.Kind, row.entry.SizeBytes)
		}
		s.WriteString(botFileDisplay(state.notice))
	}
	s.WriteString("\n\n↑↓ select/scroll · Enter open · d save · r refresh · p working/published\nn next page · ← parent · Esc back/close — no Bot work starts")
	return lipgloss.NewStyle().Width(max(30, m.width-8)).Border(lipgloss.RoundedBorder()).Padding(1, 2).Render(s.String())
}
