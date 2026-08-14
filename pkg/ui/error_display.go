package ui

import (
	"fmt"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"
)

var (
	// --- COLORS ---
	colorRed    = lipgloss.Color("196")
	colorOrange = lipgloss.Color("#FFA500")
	colorYellow = lipgloss.Color("226")
	colorWhite  = lipgloss.Color("252")
	colorGrey   = lipgloss.Color("240")

	// --- STYLES ---

	// 1. Retry Badge
	retryBadgeStyle = lipgloss.NewStyle().
			Foreground(colorOrange).
			Bold(true)

	retryMessageStyle = lipgloss.NewStyle().
				Foreground(colorGrey).
				PaddingLeft(1)

	// 2. Error Components
	headerStyle = lipgloss.NewStyle().
			Foreground(colorRed).
			Bold(true)

	// Indentation wrapper
	indentStyle = lipgloss.NewStyle().
			PaddingLeft(3)

	// Section Text Styles
	reasonTextStyle = lipgloss.NewStyle().
			Foreground(colorWhite)

	suggestionTitleStyle = lipgloss.NewStyle().
				Foreground(colorYellow).
				Bold(true)

	suggestionTextStyle = lipgloss.NewStyle().
				Foreground(colorYellow)

	rawErrorTitleStyle = lipgloss.NewStyle().
				Foreground(colorGrey).
				Bold(true)

	rawErrorTextStyle = lipgloss.NewStyle().
				Foreground(colorGrey)
)

// RenderRetryBadge: Clean text-only line
func RenderRetryBadge(attempt, maxRetries int, oneLiner string) string {
	badge := retryBadgeStyle.Render(fmt.Sprintf("⟳ Retry %d/%d:", attempt, maxRetries))
	message := retryMessageStyle.Render(oneLiner)
	return lipgloss.JoinHorizontal(lipgloss.Left, badge, message)
}

// RenderErrorBox: Pure lipgloss implementation with dynamic wrapping
func RenderErrorBox(title, reason, suggestion, originalError string) string {
	// 1. Calculate Width (Crucial for fixing line breaks)
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		width = 80 // Fallback
	}
	// Content width = Terminal Width - Indent(3) - Safety Margin(2)
	contentWidth := width - 5

	// 2. Header (Now Indented!)
	// We render the red text first, then wrap it in the indentation style
	rawHeader := headerStyle.Render(fmt.Sprintf("✕ %s", title))
	header := indentStyle.Render(rawHeader)

	// 3. Build Body Blocks
	var bodyBlocks []string

	// Helper to add a blank line spacer
	addSpacer := func() {
		if len(bodyBlocks) > 0 {
			bodyBlocks = append(bodyBlocks, "")
		}
	}

	// Reason
	if reason != "" {
		// Setting Width() forces lipgloss to wrap, preserving the indentation on new lines
		bodyBlocks = append(bodyBlocks, reasonTextStyle.Width(contentWidth).Render(reason))
	}

	// Suggestion
	if suggestion != "" {
		addSpacer()
		bodyBlocks = append(bodyBlocks,
			suggestionTitleStyle.Render("Suggestion:"),
			suggestionTextStyle.Width(contentWidth).Render(suggestion),
		)
	}

	// Raw Error
	if originalError != "" {
		addSpacer()
		bodyBlocks = append(bodyBlocks,
			rawErrorTitleStyle.Render("Raw Error:"),
			rawErrorTextStyle.Width(contentWidth).Render(strings.TrimSpace(originalError)),
		)
	}

	// 4. Assemble
	// Join all body blocks vertically
	bodyContent := lipgloss.JoinVertical(lipgloss.Left, bodyBlocks...)

	// Apply indentation to the whole body
	indentedBody := indentStyle.Render(bodyContent)

	// Join Header + Indented Body
	return fmt.Sprintf("\n%s\n%s\n", header, indentedBody)
}

func RenderMaxRetriesBox(attempts int, originalError string) string {
	return RenderErrorBox(
		"Max Retries Reached",
		fmt.Sprintf("Stopped after %d attempts. The error persisted.", attempts),
		"",
		originalError,
	)
}
