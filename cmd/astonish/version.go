package astonish

import (
	"fmt"

	"charm.land/lipgloss/v2"
	"github.com/SAP/astonish/pkg/version"
)

func init() {
	// Register our version with pkg/version so the API can access it
	// Only register if version is not "dev" (i.e., was set via ldflags)
	// This allows both old CI (setting cmd/astonish.Version) and new CI (setting pkg/version.Version) to work
	if Version != "dev" {
		version.SetCmdVersion(Version)
	}
}

// Version is set at build time via -ldflags (for backward compat with CI)
var Version = "dev"

// Version information
const (
	Name   = "Astonish AI Companion"
	Author = "Rafael Schardosin Silva"
	GitHub = "https://github.com/SAP/astonish"
)

// ASCII Logo with colors using lipgloss
var asciiLogo = `
    ___         __              _      __
   /   |  _____/ /_____  ____  (_)____/ /_
  / /| | / ___/ __/ __ \/ __ \/ / ___/ __ \
 / ___ |(__  ) /_/ /_/ / / / / (__  ) / / /
/_/  |_/____/\__/\____/_/ /_/_/____/_/ /_/
`

func printVersion() {
	// Styles
	logoStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("205")). // Pink/Magenta
		Bold(true)

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("63")). // Purple
		Bold(true)

	valueStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")) // White/Grey

	linkStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")). // Blue
		Underline(true)

	// Print logo
	fmt.Println(logoStyle.Render(asciiLogo))
	fmt.Println()

	// Print version info
	fmt.Println(labelStyle.Render(Name))
	fmt.Printf("%s %s\n", labelStyle.Render("Version:"), valueStyle.Render(version.GetVersion()))
	fmt.Printf("%s %s\n", labelStyle.Render("Author:"), valueStyle.Render(Author))
	fmt.Printf("%s %s\n", labelStyle.Render("GitHub:"), linkStyle.Render(GitHub))
	fmt.Println()
}
