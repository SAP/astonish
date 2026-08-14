package astonish

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"charm.land/lipgloss/v2"
	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/flowstore"
	"github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/mcpstore"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// minimalReadonlyContext implements agent.ReadonlyContext for tool listing
type minimalReadonlyContext struct {
	context.Context
}

func (m *minimalReadonlyContext) AgentName() string                    { return "tools-list" }
func (m *minimalReadonlyContext) AppName() string                      { return "astonish" }
func (m *minimalReadonlyContext) UserContent() *genai.Content          { return nil }
func (m *minimalReadonlyContext) InvocationID() string                 { return "" }
func (m *minimalReadonlyContext) ReadonlyState() session.ReadonlyState { return nil }
func (m *minimalReadonlyContext) UserID() string                       { return "" }
func (m *minimalReadonlyContext) SessionID() string                    { return "" }
func (m *minimalReadonlyContext) Branch() string                       { return "" }

func handleToolsCommand(args []string) error {
	if len(args) < 1 || args[0] == "-h" || args[0] == "--help" {
		printToolsUsage()
		return nil
	}

	switch args[0] {
	case "list":
		return handleToolsListCommand(args[1:])
	case "search":
		return handleToolsSearchCommand(args[1:])
	case "edit":
		return handleToolsEditCommand()
	case "store":
		return handleToolsStoreCommand(args[1:])
	case "servers":
		return handleToolsServersCommand(args[1:])
	case "enable":
		return handleToolsEnableCommand(args[1:])
	case "disable":
		return handleToolsDisableCommand(args[1:])
	case "refresh":
		return handleToolsRefreshCommand(args[1:])
	default:
		return fmt.Errorf("unknown tools command: %s", args[0])
	}
}

func printToolsUsage() {
	fmt.Println("usage: astonish tools [-h] {list,search,edit,store,servers,enable,disable,refresh} ...")
	fmt.Println("")
	fmt.Println("positional arguments:")
	fmt.Println("  {list,search,edit,store,servers,enable,disable,refresh}")
	fmt.Println("                        Tools management commands")
	fmt.Println("    list                List available tools (internal + MCP)")
	fmt.Println("    search <query>      Semantic search across the tool index (use '*' to list all)")
	fmt.Println("    edit                Edit MCP configuration")
	fmt.Println("    store               Browse and install MCP servers from the store")
	fmt.Println("    servers             List MCP servers with their enabled/disabled status")
	fmt.Println("    enable <name>       Enable an MCP server")
	fmt.Println("    disable <name>      Disable an MCP server")
	fmt.Println("    refresh             Refresh the tools cache (connects to all MCP servers)")
	fmt.Println("")
	fmt.Println("options:")
	fmt.Println("  -h, --help            show this help message and exit")
}

// handleToolsServersCommand lists all MCP servers with their enabled/disabled status
func handleToolsServersCommand(args []string) error {
	serversCmd := flag.NewFlagSet("servers", flag.ExitOnError)
	jsonOutput := serversCmd.Bool("json", false, "Output in JSON format")

	if err := serversCmd.Parse(args); err != nil {
		return fmt.Errorf("failed to parse flags: %w", err)
	}

	mcpConfig, err := config.LoadMCPConfig()
	if err != nil {
		return fmt.Errorf("failed to load MCP config: %w", err)
	}

	type ServerInfo struct {
		Name      string `json:"name"`
		Transport string `json:"transport"`
		Command   string `json:"command,omitempty"`
		URL       string `json:"url,omitempty"`
		Enabled   bool   `json:"enabled"`
	}

	servers := make([]ServerInfo, 0, len(mcpConfig.MCPServers))
	for name, cfg := range mcpConfig.MCPServers {
		transport := cfg.Transport
		if transport == "" {
			transport = "stdio"
		}
		servers = append(servers, ServerInfo{
			Name:      name,
			Transport: transport,
			Command:   cfg.Command,
			URL:       cfg.URL,
			Enabled:   cfg.IsEnabled(),
		})
	}

	// Sort by name
	sort.Slice(servers, func(i, j int) bool {
		return servers[i].Name < servers[j].Name
	})

	if *jsonOutput {
		data, err := json.MarshalIndent(servers, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal servers to JSON: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	// Styled output
	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("63")).
		Bold(true).
		PaddingBottom(1)

	enabledStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("42"))

	disabledStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240"))

	nameStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("205")).
		Bold(true)

	fmt.Println(headerStyle.Render("MCP SERVERS"))

	if len(servers) == 0 {
		fmt.Println("  No MCP servers configured.")
		fmt.Println("  Run 'astonish tools store' to browse and install servers.")
		return nil
	}

	// Find max name length for alignment
	maxLen := 0
	for _, srv := range servers {
		if len(srv.Name) > maxLen {
			maxLen = len(srv.Name)
		}
	}

	for _, srv := range servers {
		status := enabledStyle.Render("✓")
		statusText := enabledStyle.Render("(enabled)")
		style := nameStyle
		if !srv.Enabled {
			status = disabledStyle.Render("✗")
			statusText = disabledStyle.Render("(disabled)")
			style = disabledStyle
		}

		padding := strings.Repeat(" ", maxLen-len(srv.Name)+1)
		fmt.Printf("  %s %s%s%s  %s\n", status, style.Render(srv.Name), padding, srv.Transport, statusText)
	}

	return nil
}

// handleToolsEnableCommand enables an MCP server
func handleToolsEnableCommand(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: astonish tools enable <server-name>")
	}

	serverName := args[0]

	if err := config.SetMCPServerEnabled(serverName, true); err != nil {
		// If server not found, show available servers
		if strings.Contains(err.Error(), "not found") {
			names, namesErr := config.GetMCPServerNames()
			if namesErr != nil {
				slog.Warn("failed to get MCP server names", "error", namesErr)
			}
			if len(names) > 0 {
				return fmt.Errorf("%w\nAvailable servers: %s", err, strings.Join(names, ", "))
			}
		}
		return err
	}

	successStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("42")).
		Bold(true)

	fmt.Println(successStyle.Render(fmt.Sprintf("✓ Server '%s' enabled", serverName)))
	return nil
}

// handleToolsDisableCommand disables an MCP server
func handleToolsDisableCommand(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: astonish tools disable <server-name>")
	}

	serverName := args[0]

	if err := config.SetMCPServerEnabled(serverName, false); err != nil {
		// If server not found, show available servers
		if strings.Contains(err.Error(), "not found") {
			names, namesErr := config.GetMCPServerNames()
			if namesErr != nil {
				slog.Warn("failed to get MCP server names", "error", namesErr)
			}
			if len(names) > 0 {
				return fmt.Errorf("%w\nAvailable servers: %s", err, strings.Join(names, ", "))
			}
		}
		return err
	}

	successStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("220")).
		Bold(true)

	fmt.Println(successStyle.Render(fmt.Sprintf("✗ Server '%s' disabled", serverName)))
	return nil
}
func handleToolsListCommand(args []string) error {
	listCmd := flag.NewFlagSet("list", flag.ExitOnError)
	jsonOutput := listCmd.Bool("json", false, "Output in JSON format")

	if err := listCmd.Parse(args); err != nil {
		return fmt.Errorf("failed to parse flags: %w", err)
	}

	// Define tool info structure
	type ToolInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Source      string `json:"source"`
	}

	ctx := context.Background()

	// Get internal tools
	internalTools, err := tools.GetInternalTools()
	if err != nil {
		return fmt.Errorf("failed to get internal tools: %w", err)
	}

	// Get MCP servers and their tools
	mcpManager, err := mcp.NewManager()
	type MCPServerInfo struct {
		Name  string
		Tools []ToolInfo
	}
	var mcpServers []MCPServerInfo

	if err == nil {
		if err := mcpManager.InitializeToolsets(ctx); err == nil {
			// Get named toolsets with their server names
			namedToolsets := mcpManager.GetNamedToolsets()

			// Create minimal context for fetching tools
			minimalCtx := &minimalReadonlyContext{Context: ctx}

			// Iterate toolsets with their proper names
			for _, namedToolset := range namedToolsets {
				serverInfo := MCPServerInfo{
					Name:  namedToolset.Name,
					Tools: []ToolInfo{},
				}

				// Fetch tools from this toolset
				mcpTools, err := namedToolset.Toolset.Tools(minimalCtx)
				if err == nil {
					for _, tool := range mcpTools {
						serverInfo.Tools = append(serverInfo.Tools, ToolInfo{
							Name:        tool.Name(),
							Description: tool.Description(),
							Source:      namedToolset.Name,
						})
					}
				}
				// Sort tools within server
				sort.Slice(serverInfo.Tools, func(i, j int) bool {
					return serverInfo.Tools[i].Name < serverInfo.Tools[j].Name
				})

				mcpServers = append(mcpServers, serverInfo)
			}
			// Sort servers by name
			sort.Slice(mcpServers, func(i, j int) bool {
				return mcpServers[i].Name < mcpServers[j].Name
			})
		}
	}

	// Combine all tools for JSON output or max length calculation
	allTools := make([]ToolInfo, 0)
	var maxLen int

	// Add internal tools
	for _, tool := range internalTools {
		t := ToolInfo{
			Name:        tool.Name(),
			Description: tool.Description(),
			Source:      "Internal",
		}
		allTools = append(allTools, t)
		if len(t.Name) > maxLen {
			maxLen = len(t.Name)
		}
	}

	// Add MCP tools
	for _, server := range mcpServers {
		allTools = append(allTools, server.Tools...)
		for _, tool := range server.Tools {
			if len(tool.Name) > maxLen {
				maxLen = len(tool.Name)
			}
		}
	}

	if *jsonOutput {
		// JSON output
		data, err := json.MarshalIndent(allTools, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal tools to JSON: %w", err)
		}
		fmt.Println(string(data))
	} else {
		// Styles
		headerStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("63")). // Purple
			Bold(true).
			PaddingBottom(1)

		sectionStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("220")). // Yellow
			Bold(true).
			PaddingTop(1)

		nameStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("205")). // Pinkish
			Bold(true)

		descStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")) // White/Grey

		fmt.Println(headerStyle.Render("AVAILABLE TOOLS"))

		// Internal Tools
		if len(internalTools) > 0 {
			fmt.Println(sectionStyle.Render("Internal Tools"))

			// Sort internal tools
			sortedInternal := make([]tool.Tool, len(internalTools))
			copy(sortedInternal, internalTools)
			sort.Slice(sortedInternal, func(i, j int) bool {
				return sortedInternal[i].Name() < sortedInternal[j].Name()
			})

			for _, tool := range sortedInternal {
				paddedName := fmt.Sprintf("%-*s", maxLen+4, tool.Name())
				row := lipgloss.JoinHorizontal(lipgloss.Left,
					nameStyle.Render(paddedName),
					descStyle.Render(tool.Description()),
				)
				fmt.Println(row)
			}
		}

		// MCP Servers
		if len(mcpServers) > 0 {
			for _, server := range mcpServers {
				fmt.Println(sectionStyle.Render(fmt.Sprintf("MCP Server: %s", server.Name)))

				if len(server.Tools) > 0 {
					for _, tool := range server.Tools {
						paddedName := fmt.Sprintf("%-*s", maxLen+4, tool.Name)
						row := lipgloss.JoinHorizontal(lipgloss.Left,
							nameStyle.Render(paddedName),
							descStyle.Render(tool.Description),
						)
						fmt.Println(row)
					}
				} else {
					fmt.Println(descStyle.Render("  (no tools available)"))
				}
			}
		}

		// Summary footer
		totalMCPTools := 0
		for _, server := range mcpServers {
			totalMCPTools += len(server.Tools)
		}

		footerStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")). // Dark Grey
			PaddingTop(1)

		fmt.Println(footerStyle.Render(fmt.Sprintf("Total: %d internal tools, %d MCP servers with %d tools",
			len(internalTools), len(mcpServers), totalMCPTools)))
	}

	return nil
}

func handleToolsEditCommand() error {
	// Get MCP config path
	mcpConfigPath, err := config.GetMCPConfigPath()
	if err != nil {
		return fmt.Errorf("failed to get MCP config path: %w", err)
	}

	// Check if file exists, create if not
	if _, err := os.Stat(mcpConfigPath); os.IsNotExist(err) {
		// Create empty config
		emptyConfig := &config.MCPConfig{
			MCPServers: make(map[string]config.MCPServerConfig),
		}
		if err := config.SaveMCPConfig(emptyConfig); err != nil {
			return fmt.Errorf("failed to create MCP config file: %w", err)
		}
		fmt.Printf("Created new MCP config file at: %s\n", mcpConfigPath)
	}

	fmt.Printf("Opening MCP configuration at: %s\n", mcpConfigPath)
	return openInEditor(mcpConfigPath)
}

func handleToolsStoreCommand(args []string) error {
	// If no args or "install", run interactive installer
	if len(args) == 0 || args[0] == "install" {
		return handleToolsStoreInstall()
	}

	switch args[0] {
	case "list":
		return handleToolsStoreList()
	default:
		fmt.Println("usage: astonish tools store [list|install]")
		fmt.Println("")
		fmt.Println("commands:")
		fmt.Println("  list      List all available MCP servers (official + tapped)")
		fmt.Println("  install   Interactive MCP server installer (default)")
		return nil
	}
}

func handleToolsStoreList() error {
	// Load servers from taps
	store, err := flowstore.NewStore()
	if err != nil {
		return fmt.Errorf("failed to load flow store: %w", err)
	}
	if err := store.UpdateAllManifests(); err != nil {
		slog.Warn("failed to update manifests", "error", err)
	}
	tappedMCPs := store.ListAllMCPs()

	// Convert to mcpstore inputs
	var inputs []mcpstore.TappedMCPInput
	for _, mcp := range tappedMCPs {
		// Skip MCPs that have neither command nor URL (not installable)
		if mcp.Command == "" && mcp.URL == "" {
			continue
		}
		inputs = append(inputs, mcpstore.TappedMCPInput{
			ID:             mcp.ID,
			Name:           mcp.Name,
			Description:    mcp.Description,
			Author:         mcp.Author,
			GithubUrl:      mcp.GithubUrl,
			GithubStars:    mcp.GithubStars,
			RequiresApiKey: mcp.RequiresApiKey,
			Command:        mcp.Command,
			Args:           mcp.Args,
			Env:            mcp.Env,
			Tags:           mcp.Tags,
			Transport:      mcp.Transport,
			URL:            mcp.URL,
			TapName:        mcp.TapName,
		})
	}

	servers := mcpstore.ListServers(inputs)

	// Styles
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	nameStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	sourceStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	starsStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))

	// Group servers by source
	sourceGroups := make(map[string][]mcpstore.Server)
	for _, srv := range servers {
		// Skip servers without config or without command/URL (not installable)
		if srv.Config == nil || (srv.Config.Command == "" && srv.Config.URL == "") {
			continue
		}
		sourceGroups[srv.Source] = append(sourceGroups[srv.Source], srv)
	}

	for source, srvs := range sourceGroups {
		fmt.Println(headerStyle.Render(fmt.Sprintf("\n%s", strings.ToUpper(source))))
		fmt.Println(strings.Repeat("─", 60))

		for _, srv := range srvs {
			starsStr := ""
			if srv.GithubStars > 0 {
				if srv.GithubStars >= 1000 {
					starsStr = fmt.Sprintf(" ★%.1fk", float64(srv.GithubStars)/1000)
				} else {
					starsStr = fmt.Sprintf(" ★%d", srv.GithubStars)
				}
			}

			desc := srv.Description
			if len(desc) > 50 {
				desc = desc[:47] + "..."
			}

			fmt.Printf("  %s%s %s\n", nameStyle.Render(srv.Name), starsStyle.Render(starsStr), sourceStyle.Render("["+srv.Source+"]"))
			fmt.Printf("    %s\n", descStyle.Render(desc))
		}
	}

	fmt.Println("")
	fmt.Println("Tip: Run 'astonish tools store install' to install a server")
	fmt.Println("     Run 'astonish tap add <repo>' to add more repositories")

	return nil
}

func handleToolsStoreInstall() error {
	// Load servers from taps
	store, err := flowstore.NewStore()
	if err != nil {
		return fmt.Errorf("failed to load flow store: %w", err)
	}
	if err := store.UpdateAllManifests(); err != nil {
		slog.Warn("failed to update manifests", "error", err)
	}
	tappedMCPs := store.ListAllMCPs()

	// Convert to mcpstore inputs
	var inputs []mcpstore.TappedMCPInput
	for _, mcp := range tappedMCPs {
		// Skip MCPs that have neither command nor URL (not installable)
		if mcp.Command == "" && mcp.URL == "" {
			continue
		}
		inputs = append(inputs, mcpstore.TappedMCPInput{
			ID:             mcp.ID,
			Name:           mcp.Name,
			Description:    mcp.Description,
			Author:         mcp.Author,
			GithubUrl:      mcp.GithubUrl,
			GithubStars:    mcp.GithubStars,
			RequiresApiKey: mcp.RequiresApiKey,
			Command:        mcp.Command,
			Args:           mcp.Args,
			Env:            mcp.Env,
			Tags:           mcp.Tags,
			Transport:      mcp.Transport,
			URL:            mcp.URL,
			TapName:        mcp.TapName,
		})
	}

	servers := mcpstore.ListServers(inputs)

	if len(servers) == 0 {
		fmt.Println("No MCP servers available in store.")
		return nil
	}

	// Create options for selection
	options := make([]huh.Option[string], 0, len(servers))
	for _, srv := range servers {
		// Skip servers without config or without command/URL (not installable)
		if srv.Config == nil || (srv.Config.Command == "" && srv.Config.URL == "") {
			continue
		}

		// Format: Name (★ stars) - description (truncated)
		desc := srv.Description
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}

		starsStr := ""
		if srv.GithubStars > 0 {
			if srv.GithubStars >= 1000 {
				starsStr = fmt.Sprintf("★%.1fk", float64(srv.GithubStars)/1000)
			} else {
				starsStr = fmt.Sprintf("★%d", srv.GithubStars)
			}
		}

		// Add source indicator for tapped MCPs
		sourceIndicator := ""
		if srv.Source != "" && srv.Source != "official" {
			sourceIndicator = fmt.Sprintf(" [%s]", srv.Source)
		}

		label := fmt.Sprintf("%s%s (%s) - %s", srv.Name, sourceIndicator, starsStr, desc)
		options = append(options, huh.NewOption(label, srv.McpId))
	}

	if len(options) == 0 {
		fmt.Println("No installable MCP servers available.")
		return nil
	}

	// Server selection
	var selectedMcpId string
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select an MCP server to install").
				Description("Type to filter the list").
				Options(options...).
				Value(&selectedMcpId),
		),
	).Run()

	if err != nil {
		return fmt.Errorf("selection cancelled: %w", err)
	}

	// Find the selected server
	var selectedServer *mcpstore.Server
	for i := range servers {
		if servers[i].McpId == selectedMcpId {
			selectedServer = &servers[i]
			break
		}
	}
	if selectedServer == nil {
		return fmt.Errorf("failed to find selected server")
	}

	// Verify server has config
	if selectedServer.Config == nil {
		return fmt.Errorf("server '%s' has no configuration available", selectedServer.Name)
	}

	// Prepare env variables
	envVars := make(map[string]string)
	if selectedServer.Config != nil && len(selectedServer.Config.Env) > 0 {
		// Collect env var keys and create string pointers for each
		type envVarPair struct {
			Key   string
			Value string
		}
		var envPairs []envVarPair
		for key, defaultVal := range selectedServer.Config.Env {
			envPairs = append(envPairs, envVarPair{Key: key, Value: defaultVal})
		}

		// Create input form for each env variable
		var fields []huh.Field
		for i := range envPairs {
			fields = append(fields, huh.NewInput().
				Title(envPairs[i].Key).
				Description(fmt.Sprintf("Environment variable for %s", selectedServer.Name)).
				Value(&envPairs[i].Value))
		}

		if len(fields) > 0 {
			err = huh.NewForm(
				huh.NewGroup(fields...).
					Title(fmt.Sprintf("Configure %s", selectedServer.Name)),
			).Run()

			if err != nil {
				return fmt.Errorf("configuration cancelled: %w", err)
			}

			// Copy values back to map
			for _, pair := range envPairs {
				envVars[pair.Key] = pair.Value
			}
		}
	}

	// Load existing MCP config
	mcpConfig, err := config.LoadMCPConfig()
	if err != nil {
		// Create new config if it doesn't exist
		mcpConfig = &config.MCPConfig{
			MCPServers: make(map[string]config.MCPServerConfig),
		}
	}

	// Build server config
	serverConfig := config.MCPServerConfig{
		Command: selectedServer.Config.Command,
		Args:    selectedServer.Config.Args,
	}
	if len(envVars) > 0 {
		serverConfig.Env = envVars
	}

	// Use server name as key (sanitized)
	serverKey := selectedServer.Name
	mcpConfig.MCPServers[serverKey] = serverConfig

	// Save config
	if err := config.SaveMCPConfig(mcpConfig); err != nil {
		return fmt.Errorf("failed to save MCP config: %w", err)
	}

	// Success message
	successStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("42")). // Green
		Bold(true)

	fmt.Println(successStyle.Render(fmt.Sprintf("✓ %s installed successfully!", selectedServer.Name)))
	fmt.Printf("  Run 'astonish tools list' to see available tools.\n")

	return nil
}

// handleToolsRefreshCommand connects to all MCP servers and refreshes the tools cache.
// This populates cached tool schemas needed for lazy loading in chat mode.
func handleToolsRefreshCommand(args []string) error {
	refreshCmd := flag.NewFlagSet("refresh", flag.ExitOnError)
	verbose := refreshCmd.Bool("v", false, "Verbose output")
	refreshCmd.Parse(args)

	ctx := context.Background()

	mcpCfg, err := config.LoadMCPConfig()
	if err != nil {
		return fmt.Errorf("failed to load MCP config: %w", err)
	}
	if mcpCfg == nil || len(mcpCfg.MCPServers) == 0 {
		fmt.Println("No MCP servers configured.")
		return nil
	}

	fmt.Println("Refreshing tools cache...")

	mcpManager, err := mcp.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create MCP manager: %w", err)
	}
	defer mcpManager.Cleanup()

	totalTools := 0
	successCount := 0
	failCount := 0

	for name, serverCfg := range mcpCfg.MCPServers {
		if !serverCfg.IsEnabled() {
			if *verbose {
				fmt.Printf("  [skip] %s (disabled)\n", name)
			}
			continue
		}

		fmt.Printf("  [....] %s", name)

		namedToolset, initErr := mcpManager.InitializeSingleToolset(ctx, name)
		if initErr != nil {
			fmt.Printf("\r  [FAIL] %s: %v\n", name, initErr)
			failCount++
			continue
		}

		minCtx := &minimalReadonlyContext{Context: ctx}
		mcpTools, toolErr := namedToolset.Toolset.Tools(minCtx)
		if toolErr != nil {
			fmt.Printf("\r  [FAIL] %s: %v\n", name, toolErr)
			failCount++
			continue
		}

		// Build cache entries with schemas
		entries := make([]cache.ToolEntry, 0, len(mcpTools))
		for _, t := range mcpTools {
			entries = append(entries, cache.ToolEntry{
				Name:        t.Name(),
				Description: t.Description(),
				Source:      name,
				InputSchema: common.ExtractToolInputSchema(t),
			})
		}

		checksum := cache.ComputeServerChecksum(serverCfg.Command, serverCfg.Args, serverCfg.Env)
		cache.AddServerTools(name, entries, checksum)

		fmt.Printf("\r  [ OK ] %s (%d tools)\n", name, len(mcpTools))
		totalTools += len(mcpTools)
		successCount++
	}

	if err := cache.SaveCache(); err != nil {
		return fmt.Errorf("failed to save cache: %w", err)
	}

	fmt.Printf("\nDone: %d servers refreshed, %d tools cached", successCount, totalTools)
	if failCount > 0 {
		fmt.Printf(", %d failed", failCount)
	}
	fmt.Println()

	return nil
}

// handleToolsSearchCommand performs a semantic search across the tool index.
// This command is no longer available — tool discovery is now handled by the
// platform backend (SQLite or PostgreSQL) and accessible via the daemon API.
func handleToolsSearchCommand(args []string) error {
	fmt.Println("The 'tools search' command is no longer available.")
	fmt.Println()
	fmt.Println("Tool discovery is now managed by the platform database backend.")
	fmt.Println("Use the Astonish Studio web interface or the search_tools tool")
	fmt.Println("during chat to search the tool index.")
	return nil
}
