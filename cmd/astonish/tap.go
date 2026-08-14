package astonish

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/SAP/astonish/pkg/flowstore"
)

// handleTapCommand handles the unified tap commands
func handleTapCommand(args []string) error {
	if len(args) == 0 {
		printTapUsage()
		return nil
	}

	switch args[0] {
	case "add":
		return handleTapAddCommand(args[1:])
	case "list":
		return handleTapListCommand()
	case "remove":
		return handleTapRemoveCommand(args[1:])
	case "update":
		return handleTapUpdateCommand()
	default:
		printTapUsage()
		return fmt.Errorf("unknown tap command: %s", args[0])
	}
}

func printTapUsage() {
	fmt.Println("usage: astonish tap <command> [arguments]")
	fmt.Println("")
	fmt.Println("Manage extension repositories (taps) that provide flows and MCP servers.")
	fmt.Println("")
	fmt.Println("commands:")
	fmt.Println("  add <repo> [--as <alias>]   Add a tap repository")
	fmt.Println("  list                        List all taps")
	fmt.Println("  remove <name>               Remove a tap")
	fmt.Println("  update                      Update all tap manifests")
	fmt.Println("")
	fmt.Println("examples:")
	fmt.Println("  astonish tap add SAP/astonish-flows")
	fmt.Println("  astonish tap add github.enterprise.com/team/extensions --as team")
	fmt.Println("  astonish tap list")
	fmt.Println("  astonish tap remove team")
}

func handleTapAddCommand(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: astonish tap add <repo> [--as <alias>]")
		fmt.Println("")
		fmt.Println("examples:")
		fmt.Println("  astonish tap add owner                           # adds owner/astonish-flows")
		fmt.Println("  astonish tap add owner/custom-repo")
		fmt.Println("  astonish tap add github.enterprise.com/owner     # enterprise GitHub")
		fmt.Println("  astonish tap add github.enterprise.com/owner/repo --as myalias")
		return nil
	}

	urlArg, alias := parseTapAddArgs(args)

	store, err := flowstore.NewStore()
	if err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	fmt.Printf("Adding tap: %s\n", urlArg)
	if alias != "" {
		fmt.Printf("  with alias: %s\n", alias)
	}

	name, err := store.AddTap(urlArg, alias)
	if err != nil {
		return fmt.Errorf("failed to add tap: %w", err)
	}

	// Style for success
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	fmt.Println(successStyle.Render(fmt.Sprintf("✓ Added tap '%s' successfully", name)))
	fmt.Println("")
	fmt.Println("Tip: Run 'astonish flows store list' to see available flows from this tap")
	fmt.Println("     Run 'astonish tools store list' to see available MCP servers from this tap")

	return nil
}

func handleTapListCommand() error {
	store, err := flowstore.NewStore()
	if err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	taps := store.GetAllTaps()

	if len(taps) == 0 {
		fmt.Println("No taps configured.")
		fmt.Println("")
		fmt.Println("Add one with: astonish tap add <repo>")
		return nil
	}

	// Styles
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	nameStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	urlStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	officialBadge := lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Render("[official]")

	fmt.Println(headerStyle.Render("TAPS"))
	fmt.Println(strings.Repeat("─", 60))

	for _, tap := range taps {
		badge := ""
		if tap.Name == flowstore.OfficialStoreName {
			badge = " " + officialBadge
		}
		fmt.Printf("  %s%s\n", nameStyle.Render(tap.Name), badge)
		fmt.Printf("    %s\n", urlStyle.Render(tap.URL))
	}

	fmt.Println("")
	fmt.Printf("Total: %d tap(s)\n", len(taps))

	return nil
}

func handleTapRemoveCommand(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: astonish tap remove <name>")
		return nil
	}

	name := args[0]

	store, err := flowstore.NewStore()
	if err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	if err := store.RemoveTap(name); err != nil {
		return fmt.Errorf("failed to remove tap: %w", err)
	}

	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	fmt.Println(successStyle.Render(fmt.Sprintf("✓ Removed tap '%s'", name)))

	return nil
}

func handleTapUpdateCommand() error {
	store, err := flowstore.NewStore()
	if err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	fmt.Println("Updating tap manifests...")
	fmt.Println("")

	err = store.UpdateAllManifests()
	if err != nil {
		fmt.Println("")
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
		fmt.Println(errorStyle.Render(fmt.Sprintf("Warning: %v", err)))
	}

	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	fmt.Println("")
	fmt.Println(successStyle.Render("✓ Manifests updated"))

	return nil
}
