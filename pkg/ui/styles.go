package ui

import (
	"fmt"
	"image/color"
	"sort"

	"charm.land/lipgloss/v2"
)

// RenderToolBox renders a styled box for tool execution approval.
func RenderToolBox(toolName string, args map[string]interface{}) string {
	// --- Styles ---
	borderColor := lipgloss.Color("63") // Purple

	// The outer box
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		BorderTop(true).
		BorderLeft(true).
		BorderRight(true).
		BorderBottom(true).
		Padding(0, 1). // Compact padding
		Width(60)

	// Style for the keys (e.g., "max_results:")
	keyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("244")). // Lighter Grey for better contrast
		Width(14).                         // Fixed width for alignment
		Align(lipgloss.Right).             // Right align looks cleaner for kv-pairs
		MarginRight(1)

	// Style for the values
	valStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")) // White/Light Grey

	// Style for numbers (optional pop of color)
	numberStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("208")) // Orange for numbers

	// --- Rendering Logic ---

	// 1. Sort the keys so they appear in a consistent order
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 2. Build the rows
	var rows []string
	for _, key := range keys {
		val := args[key]
		strVal := fmt.Sprintf("%v", val)

		// Truncate long values
		if len(strVal) > 200 {
			strVal = strVal[:197] + "..."
		}

		// Choose style based on type (simple heuristic)
		var renderedVal string
		switch val.(type) {
		case int, float64, float32:
			renderedVal = numberStyle.Render(strVal)
		default:
			renderedVal = valStyle.Render(strVal)
		}

		// Create the line: "      topic: news"
		row := lipgloss.JoinHorizontal(lipgloss.Left,
			keyStyle.Render(key+":"),
			renderedVal,
		)
		rows = append(rows, row)
	}

	// 3. Create the Header
	header := lipgloss.NewStyle().
		Foreground(borderColor).
		Bold(true).
		Render("🛠  " + toolName)

	// 4. Create a subtle divider
	divider := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false). // Top border only
		BorderForeground(lipgloss.Color("236")).                    // Very dark grey
		Width(58).                                                  // Match box width approx
		Padding(0)

	// 5. Join everything
	body := lipgloss.JoinVertical(lipgloss.Left, rows...)

	content := lipgloss.JoinVertical(lipgloss.Left,
		header,
		divider.String(), // The new divider
		body,
	)

	return boxStyle.Render(content) + "\n"
}

// RenderStatusBadge renders a styled status badge (e.g. "✓ Command approved")
func RenderStatusBadge(text string, success bool) string {
	var icon string
	var iconColor color.Color

	if success {
		icon = "✓"
		iconColor = lipgloss.Color("42") // Green
	} else {
		icon = "✗"
		iconColor = lipgloss.Color("196") // Red
	}

	checkStyle := lipgloss.NewStyle().Foreground(iconColor).SetString(icon)
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244")) // Lighter Grey text

	return checkStyle.String() + " " + textStyle.Render(text)
}
