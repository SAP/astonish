package ui

import (
	"fmt"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ParallelModel holds the state of the parallel execution UI
type ParallelModel struct {
	totalItems  int
	processed   int
	activeCount int
	nodeName    string
	spinner     spinner.Model
	progress    progress.Model
	width       int
	done        bool
	lastLog     string
}

// ItemFinishedMsg signals that a worker has finished an item
type ItemFinishedMsg struct{}

// ActiveCountMsg signals an update to the number of active workers
type ActiveCountMsg int

// NewParallelProgram creates a new tea.Program for the parallel progress UI
func NewParallelProgram(total int, nodeName string) *tea.Program {
	model := initialParallelModel(total, nodeName)
	return tea.NewProgram(model)
}

func initialParallelModel(total int, nodeName string) ParallelModel {
	p := progress.New(
		progress.WithDefaultBlend(),
		progress.WithWidth(20), // Keep it compact
		progress.WithoutPercentage(),
	)
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("63")) // Purple

	return ParallelModel{
		totalItems: total,
		nodeName:   nodeName,
		spinner:    s,
		progress:   p,
	}
}

func (m ParallelModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m ParallelModel) View() tea.View {
	if m.done {
		// Final clean state replacing the progress bar
		check := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).SetString("✓")
		text := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		return tea.NewView(fmt.Sprintf("%s %s\n", check, text.Render(fmt.Sprintf("%s (%d items processed)", m.nodeName, m.totalItems))))
	}

	// While running
	spin := m.spinner.View()
	bar := m.progress.View()

	// Percentage
	percent := 0.0
	if m.totalItems > 0 {
		percent = float64(m.processed) / float64(m.totalItems)
	}
	percentStr := fmt.Sprintf("%.0f%%", percent*100)

	// Counts: "(6/12)"
	countStr := fmt.Sprintf("(%d/%d)", m.processed, m.totalItems)

	// Active: "• 5 active"
	activeStr := fmt.Sprintf("• %d active", m.activeCount)

	// Styles
	// Increased width to 40 to accommodate longer node names without wrapping
	nodeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true).Width(40).Align(lipgloss.Left)
	percentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Width(5).Align(lipgloss.Right)
	countStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).PaddingLeft(1)
	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).PaddingLeft(1)

	// Truncate node name if it exceeds width to prevent wrapping
	displayName := m.nodeName
	if len(displayName) > 38 {
		displayName = displayName[:37] + "…"
	}

	// Format: ⣻  add_review_comment  [██████░░░░░░]  50%  (6/12) • 5 active
	view := fmt.Sprintf("%s %s %s %s %s %s",
		spin,
		nodeStyle.Render(displayName),
		bar,
		percentStyle.Render(percentStr),
		countStyle.Render(countStr),
		activeStyle.Render(activeStr),
	)

	if m.lastLog != "" {
		logStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)
		// Truncate log if too long
		log := m.lastLog
		if len(log) > 80 {
			log = log[:77] + "..."
		}
		view += "\n  " + logStyle.Render(log)
	}

	return tea.NewView(view)
}

// ItemLogMsg signals a log message from a worker
type ItemLogMsg string

func (m ParallelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case ItemFinishedMsg:
		m.processed++
		if m.processed >= m.totalItems {
			m.done = true
			return m, tea.Quit
		}
		// Update progress bar
		cmd := m.progress.SetPercent(float64(m.processed) / float64(m.totalItems))
		return m, cmd

	case ActiveCountMsg:
		m.activeCount = int(msg)
		return m, nil

	case ItemLogMsg:
		m.lastLog = string(msg)
		return m, nil

	// Required for the progress bar animation
	case progress.FrameMsg:
		var cmd tea.Cmd
		m.progress, cmd = m.progress.Update(msg)
		return m, cmd
	}
	return m, nil
}
