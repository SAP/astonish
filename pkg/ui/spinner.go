package ui

import (
	"fmt"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type SpinnerModel struct {
	spinner  spinner.Model
	text     string
	quitting bool
}

// SpinnerTextMsg is sent to update the spinner's display text.
type SpinnerTextMsg struct {
	Text string
}

func NewSpinner(text string) SpinnerModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	return SpinnerModel{spinner: s, text: text}
}

func (m SpinnerModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m SpinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Allow quitting with standard keys if needed, though usually controlled by parent
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
	case tea.QuitMsg:
		m.quitting = true
		return m, tea.Quit
	case SpinnerTextMsg:
		m.text = msg.Text
		return m, nil
	}

	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

func (m SpinnerModel) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	return tea.NewView(fmt.Sprintf("%s %s", m.spinner.View(), m.text))

}
