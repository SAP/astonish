package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

const localSkillsOnlyNotice = "/skills is available only in local code mode."

// skillsPickerState holds the /skills overlay state.
type skillsPickerState struct {
	open    bool
	loading bool
	err     string
	items   []backend.SkillSummary
	cursor  int
}

// skillsLoadedMsg is returned by the async load command.
type skillsLoadedMsg struct {
	items []backend.SkillSummary
	err   error
}

func (m model) localSkillsCap() backend.LocalSkillsBackend {
	if sb, ok := m.backend.(backend.LocalSkillsBackend); ok {
		return sb
	}
	return nil
}

// openSkillsPicker opens the /skills overlay and kicks off the async load.
func (m model) openSkillsPicker() (tea.Model, tea.Cmd) {
	capability := m.localSkillsCap()
	if capability == nil {
		m.tr.Apply(events.NewSystem(localSkillsOnlyNotice))
		m.refreshViewport()
		return m, nil
	}
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil
	}
	m.skillsPicker = skillsPickerState{open: true, loading: true}
	m.slash = slashCompletion{}
	m.files = fileCompletion{}
	m.ta.Reset()
	return m, m.loadSkillsCmd()
}

func (m model) loadSkillsCmd() tea.Cmd {
	capability := m.localSkillsCap()
	return func() tea.Msg {
		if capability == nil {
			return skillsLoadedMsg{err: fmt.Errorf("local skills not available")}
		}
		items, err := capability.ListLocalSkills(context.Background())
		return skillsLoadedMsg{items: items, err: err}
	}
}

func (m model) applySkillsLoaded(msg skillsLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.skillsPicker.open {
		return m, nil
	}
	m.skillsPicker.loading = false
	if msg.err != nil {
		m.skillsPicker.err = msg.err.Error()
	} else {
		m.skillsPicker.items = msg.items
		m.skillsPicker.cursor = 0
	}
	return m, nil
}

func (m model) handleSkillsPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc", "ctrl+c":
		m.skillsPicker = skillsPickerState{}
		return m, nil
	case "up", "k":
		if m.skillsPicker.cursor > 0 {
			m.skillsPicker.cursor--
		}
		return m, nil
	case "down", "j":
		if m.skillsPicker.cursor < len(m.skillsPicker.items)-1 {
			m.skillsPicker.cursor++
		}
		return m, nil
	}
	return m, nil
}

func (m model) renderSkillsPickerOverlay() string {
	th := m.theme
	w := m.width
	if w < 40 {
		w = 40
	}
	h := m.height - 4
	if h < 8 {
		h = 8
	}

	var body strings.Builder
	body.WriteString(th.Header.Render("Skills") + th.Muted.Render("  ↑↓ move  esc close") + "\n\n")

	if m.skillsPicker.loading {
		body.WriteString(th.Muted.Render("Loading…"))
	} else if m.skillsPicker.err != "" {
		body.WriteString(th.Error.Render(m.skillsPicker.err))
	} else if len(m.skillsPicker.items) == 0 {
		body.WriteString(th.Muted.Render("No skills loaded. Add a skill folder to ~/.config/astonish/skills/."))
	} else {
		maxRows := h - 4
		if maxRows < 3 {
			maxRows = 3
		}
		start := 0
		if m.skillsPicker.cursor >= maxRows {
			start = m.skillsPicker.cursor - maxRows + 1
		}
		end := start + maxRows
		if end > len(m.skillsPicker.items) {
			end = len(m.skillsPicker.items)
		}
		for i := start; i < end; i++ {
			skill := m.skillsPicker.items[i]
			status := "eligible"
			statusStyle := th.Success
			if !skill.Eligible {
				status = "unavailable"
				statusStyle = th.Muted
			}
			src := first(skill.Source, "user")
			mark := "  "
			nameStyle := th.Text
			if i == m.skillsPicker.cursor {
				mark = "› "
				nameStyle = th.Brand
			}
			// Name line
			namePart := nameStyle.Render(mark + skill.Name)
			tagPart := th.Muted.Render(" ["+src+"] ") + statusStyle.Render(status)
			body.WriteString(namePart + tagPart + "\n")
			// Description line (indented)
			desc := skill.Description
			if desc != "" {
				maxDesc := w - 6
				if maxDesc < 20 {
					maxDesc = 20
				}
				descRunes := []rune(desc)
				if len(descRunes) > maxDesc {
					desc = string(descRunes[:maxDesc-1]) + "…"
				}
				body.WriteString(th.Muted.Render("    "+desc) + "\n")
			}
			// Missing requirements (if any)
			if len(skill.Missing) > 0 {
				body.WriteString(th.Error.Render("    Missing: "+strings.Join(skill.Missing, ", ")) + "\n")
			}
		}
	}

	box := th.InputBorderFocus.
		Width(w-2).
		MaxHeight(h).
		Padding(1, 2).
		Render(body.String())
	return m.paintCompletionPopup(box, w-2)
}

// formatSkillSummaries is kept for use in tests.
func formatSkillSummaries(summaries []backend.SkillSummary) string {
	if len(summaries) == 0 {
		return "No local skills are loaded."
	}
	var out strings.Builder
	out.WriteString("Local skills:")
	for _, skill := range summaries {
		status := "eligible"
		if !skill.Eligible {
			status = "unavailable"
		}
		fmt.Fprintf(&out, "\n\n- %s [%s, %s] — %s", skill.Name, first(skill.Source, "unknown"), status, skill.Description)
		if len(skill.Missing) > 0 {
			fmt.Fprintf(&out, "\n  Missing: %s", strings.Join(skill.Missing, ", "))
		}
	}
	return out.String()
}

// renderSkillsOverlayStyle returns a placed overlay string for use in viewContent.
func (m model) renderSkillsOverlay() string {
	overlay := m.renderSkillsPickerOverlay()
	return m.paintBackground(lipgloss.Place(m.width, m.screenHeight(), lipgloss.Center, lipgloss.Center, overlay,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("#000000"))),
	))
}
