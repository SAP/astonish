package astonish

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/flowstore"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/SAP/astonish/pkg/ui"
	"google.golang.org/adk/session"
)

func handleFlowsCommand(args []string) error {
	if len(args) < 1 || args[0] == "--help" || args[0] == "-h" {
		printFlowsUsage()
		return nil
	}

	// Remote mode: delegate list and run to API
	if client.IsRemoteMode() {
		return handleFlowsRemote(args)
	}

	switch args[0] {
	case "run":
		return handleRunCommand(args[1:])
	case "list":
		return handleListCommand()
	case "show":
		return handleShowCommand(args[1:])
	case "edit":
		return handleEditCommand(args[1:])
	case "import":
		return handleImportCommand(args[1:])
	case "remove":
		return handleRemoveCommand(args[1:])
	case "store":
		return handleStoreCommand(args[1:])
	default:
		return fmt.Errorf("unknown flows command: %s", args[0])
	}
}

func printFlowsUsage() {
	fmt.Println("usage: astonish flows [-h] {run,list,show,edit,import,remove,store} ...")
	fmt.Println("")
	fmt.Println("Design and run AI flows - powerful automation workflows")
	fmt.Println("powered by LLMs with visual design and CLI execution.")
	fmt.Println("")
	fmt.Println("commands:")
	fmt.Println("  run                 Execute a flow")
	fmt.Println("  list                List available flows")
	fmt.Println("  show                Visualize flow structure")
	fmt.Println("  edit                Edit a flow YAML file")
	fmt.Println("  import              Import a flow from a local YAML file")
	fmt.Println("  remove              Remove a flow")
	fmt.Println("  store               Browse and install flows from stores")
	fmt.Println("")
	fmt.Println("options:")
	fmt.Println("  -h, --help          Show this help message")
}

func handleRunCommand(args []string) error {
	// Load config first
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		// Just warn, don't fail, maybe first run
		fmt.Printf("Warning: Failed to load config: %v\n", err)
		appCfg = &config.AppConfig{}
	}

	runCmd := flag.NewFlagSet("run", flag.ExitOnError)
	providerName := runCmd.String("provider", appCfg.General.DefaultProvider, "LLM provider (gemini, openai, sap_ai_core)")
	modelName := runCmd.String("model", appCfg.General.DefaultModel, "Model name")
	useBrowser := runCmd.Bool("browser", false, "Launch with embedded web browser UI")
	port := runCmd.Int("port", 8080, "Port for web server (only used with --browser)")
	debugMode := runCmd.Bool("debug", false, "Enable debug mode to show tool inputs and responses")
	autoApprove := runCmd.Bool("auto-approve", false, "Automatically approve all tool executions")

	var params stringArray
	runCmd.Var(&params, "p", "Parameter to pass to the agent in key=value format (can be used multiple times)")

	// Pre-process args to allow positional agent name to be anywhere
	// We extract the first non-flag argument as the agent name
	var agentName string
	var flagArgs []string

	skipNext := false
	for _, arg := range args {
		if skipNext {
			flagArgs = append(flagArgs, arg)
			skipNext = false
			continue
		}

		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			// Check if it's a flag that takes an argument and doesn't use =
			if !strings.Contains(arg, "=") {
				name := strings.TrimLeft(arg, "-")
				if name == "provider" || name == "model" || name == "port" || name == "p" || name == "param" {
					skipNext = true
				}
			}
		} else {
			if agentName == "" {
				agentName = arg
			} else {
				// Extra positional args, keep them (flag.Parse will likely stop or error)
				flagArgs = append(flagArgs, arg)
			}
		}
	}

	if err := runCmd.Parse(flagArgs); err != nil {
		return fmt.Errorf("failed to parse flags: %w", err)
	}

	// Parse parameters
	parameters := make(map[string]string)
	for _, p := range params {
		parts := strings.SplitN(p, "=", 2)
		if len(parts) == 2 {
			parameters[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		} else {
			fmt.Printf("Warning: Ignoring malformed parameter: %s (missing '=')\n", p)
		}
	}

	// If provider is still empty, default to gemini
	if *providerName == "" {
		*providerName = "gemini"
	}

	// Set up provider credentials from the encrypted credential store.
	// After migration, secrets are scrubbed from config.yaml and stored
	// only in the credential store. InjectProviderSecretsToConfig re-hydrates
	// the in-memory config map so GetProvider() can read them.
	configDir, configDirErr := config.GetConfigDir()
	if configDirErr == nil {
		if cs, csErr := credentials.Open(configDir); csErr == nil {
			tools.SetCredentialStore(cs)
			config.SetInstalledSecretGetter(cs.GetSecret)
			config.InjectProviderSecretsToConfig(appCfg, cs.GetSecret)
			config.SetupAllProviderEnvFromStore(appCfg, cs.GetSecret)
		} else {
			config.SetupAllProviderEnv(appCfg)
		}
	} else {
		config.SetupAllProviderEnv(appCfg)
	}

	// Load MCP config and set environment variables from all servers
	// This allows internal tools to access configuration defined for MCP servers (e.g. GITHUB_HOST)
	if mcpCfg, err := config.LoadMCPConfig(); err == nil {
		for _, server := range mcpCfg.MCPServers {
			for k, v := range server.Env {
				if v != "" {
					os.Setenv(k, v)
				}
			}
		}
	}

	if agentName == "" {
		// Fallback to NArg check if somehow it ended up there
		if runCmd.NArg() > 0 {
			agentName = runCmd.Arg(0)
		} else {
			fmt.Println("Usage: astonish flows run [flags] <flow_name>")
			runCmd.PrintDefaults()
			return fmt.Errorf("no agent name provided")
		}
	}

	// Try to find the agent file
	// 1. Check if it's a full path or in current dir
	agentPath := agentName
	if _, err := os.Stat(agentPath); os.IsNotExist(err) {
		// 2. Check with .yaml extension
		agentPath = fmt.Sprintf("%s.yaml", agentName)
		if _, err := os.Stat(agentPath); os.IsNotExist(err) {
			// 3. Check in standard system agents directory (legacy)
			agentsDir, err := config.GetAgentsDir()
			if err == nil {
				sysAgentPath := filepath.Join(agentsDir, fmt.Sprintf("%s.yaml", agentName))
				if _, err := os.Stat(sysAgentPath); err == nil {
					agentPath = sysAgentPath
					goto Found
				}
			}

			// 4. Check in new flows directory
			flowsDir, err := flowstore.GetFlowsDir()
			if err == nil {
				flowPath := filepath.Join(flowsDir, fmt.Sprintf("%s.yaml", agentName))
				if _, err := os.Stat(flowPath); err == nil {
					agentPath = flowPath
					goto Found
				}
			}

			// 5. Check in store cache (for installed flows)
			store, err := flowstore.NewStore()
			if err == nil {
				// Parse the flow reference first
				tapName, flowName := parseFlowRef(agentName)

				// Check installed flows for this specific tap
				if path, ok := store.GetInstalledFlowPath(tapName, flowName); ok {
					agentPath = path
					goto Found
				}

				// Try to fetch from store
				// - Bare names (no /) only check official store
				// - Prefixed names (tap/flow) check specific tap
				fmt.Printf("Flow not found locally, checking %s store...\n", tapName)
				if err := store.InstallFlow(tapName, flowName); err == nil {
					if path, ok := store.GetInstalledFlowPath(tapName, flowName); ok {
						fmt.Printf("✓ Downloaded from %s store\n", tapName)
						agentPath = path
						goto Found
					}
				}
			}

			// 7. Check in local dev path (fallback)
			agentPath = fmt.Sprintf("agents/%s.yaml", agentName)
			if _, err := os.Stat(agentPath); os.IsNotExist(err) {
				return fmt.Errorf("flow not found: %s\nTip: Run 'astonish flows store list' to see available flows", agentName)
			}
		}
	}

Found:

	cfg, err := config.LoadAgent(agentPath)
	if err != nil {
		return fmt.Errorf("failed to load agent: %w", err)
	}

	ctx := context.Background()

	// Create the base session service and wrap it to fix state initialization bug
	baseService := session.InMemoryService()
	safeService := NewAutoInitService(baseService)

	// Choose launcher based on --browser flag
	if *useBrowser {
		// Use simple web launcher with chat-only UI
		return launcher.RunSimpleWeb(ctx, &launcher.SimpleWebConfig{
			AgentConfig:    cfg,
			ProviderName:   *providerName,
			ModelName:      *modelName,
			SessionService: safeService,
			Port:           *port,
			AutoApprove:    *autoApprove,
		})
	}

	// Use our custom console launcher
	return launcher.RunConsole(ctx, &launcher.ConsoleConfig{
		AgentConfig:    cfg,
		AppConfig:      appCfg,
		ProviderName:   *providerName,
		ModelName:      *modelName,
		SessionService: safeService,
		DebugMode:      *debugMode,
		AutoApprove:    *autoApprove,
		Parameters:     parameters,
	})
}

// stringArray implements flag.Value interface for multiple string flags
type stringArray []string

func (i *stringArray) String() string {
	return strings.Join(*i, ", ")
}

func (i *stringArray) Set(value string) error {
	*i = append(*i, value)
	return nil
}

func handleListCommand() error {
	type AgentInfo struct {
		Name        string
		Description string
		Source      string // "local", "official", or tap name
	}

	var localAgents []AgentInfo
	var officialAgents []AgentInfo
	tapAgents := make(map[string][]AgentInfo) // tapName -> agents

	// Helper to scan directory for local agents
	scanDir := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		seen := make(map[string]bool)
		for _, entry := range entries {
			if !entry.IsDir() && filepath.Ext(entry.Name()) == ".yaml" {
				name := entry.Name()[:len(entry.Name())-5]
				if seen[name] {
					continue
				}
				seen[name] = true
				path := filepath.Join(dir, entry.Name())
				cfg, err := config.LoadAgent(path)
				if err != nil {
					continue
				}
				localAgents = append(localAgents, AgentInfo{
					Name:        name,
					Description: cfg.Description,
					Source:      "local",
				})
			}
		}
	}

	// 1. Scan System Directory
	if sysDir, err := config.GetAgentsDir(); err == nil {
		scanDir(sysDir)
	}

	// 2. Scan Local Directory
	scanDir("agents")

	// 3. Scan User Flows Directory
	if flowsDir, err := flowstore.GetFlowsDir(); err == nil {
		scanDir(flowsDir)
	}

	// 4. Scan Store (installed flows)
	if store, err := flowstore.NewStore(); err == nil {
		for _, tap := range store.GetAllTaps() {
			storeDir := store.GetStoreDir()
			tapDir := filepath.Join(storeDir, tap.Name)
			entries, err := os.ReadDir(tapDir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
					continue
				}
				if entry.Name() == "manifest.yaml" {
					continue
				}
				name := entry.Name()[:len(entry.Name())-5]
				path := filepath.Join(tapDir, entry.Name())
				cfg, err := config.LoadAgent(path)
				desc := "(Installed from store)"
				if err == nil && cfg.Description != "" {
					desc = cfg.Description
				}

				info := AgentInfo{
					Name:        name,
					Description: desc,
					Source:      tap.Name,
				}

				if tap.Name == flowstore.OfficialStoreName {
					officialAgents = append(officialAgents, info)
				} else {
					tapAgents[tap.Name] = append(tapAgents[tap.Name], info)
				}
			}
		}
	}

	// Styles
	sectionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("63")).
		Bold(true)

	nameStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("205")).
		Bold(true)

	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))

	storeStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).
		Bold(true)

	tapStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("42")).
		Bold(true)

	// Print sections
	printSection := func(title string, agents []AgentInfo, style lipgloss.Style) {
		if len(agents) == 0 {
			return
		}

		// Sort by name
		sort.Slice(agents, func(i, j int) bool {
			return agents[i].Name < agents[j].Name
		})

		fmt.Println(style.Render(fmt.Sprintf("\n%s (%d)", title, len(agents))))

		for _, a := range agents {
			fmt.Printf("  %s\n", nameStyle.Render(a.Name))
			if a.Description != "" {
				fmt.Printf("    %s\n", descStyle.Render(a.Description))
			}
		}
	}

	// Check if anything exists
	totalCount := len(localAgents) + len(officialAgents)
	for _, t := range tapAgents {
		totalCount += len(t)
	}

	if totalCount == 0 {
		fmt.Println("No flows found.")
		fmt.Println("\nTip: Run 'astonish flows store list' to browse available flows")
		return nil
	}

	fmt.Println(lipgloss.NewStyle().Bold(true).Render("AVAILABLE FLOWS"))

	// Print Local first
	printSection("📁 LOCAL", localAgents, sectionStyle)

	// Print Official Store
	printSection("🏪 OFFICIAL STORE", officialAgents, storeStyle)

	// Print Custom Taps
	var tapNames []string
	for tapName := range tapAgents {
		tapNames = append(tapNames, tapName)
	}
	sort.Strings(tapNames)
	for _, tapName := range tapNames {
		printSection("📦 "+tapName, tapAgents[tapName], tapStyle)
	}

	fmt.Println()
	return nil
}

func handleShowCommand(args []string) error {
	if len(args) < 1 {
		fmt.Println("Usage: astonish flows show <flow_name>")
		return fmt.Errorf("no flow name provided")
	}

	agentName := args[0]
	// Reuse the logic to find the agent file (duplicated from handleRunCommand for now, could be refactored)
	// 1. Check if it's a full path or in current dir
	agentPath := agentName
	if _, err := os.Stat(agentPath); os.IsNotExist(err) {
		// 2. Check with .yaml extension
		agentPath = fmt.Sprintf("%s.yaml", agentName)
		if _, err := os.Stat(agentPath); os.IsNotExist(err) {
			// 3. Check in standard system agents directory
			agentsDir, err := config.GetAgentsDir()
			if err == nil {
				sysAgentPath := filepath.Join(agentsDir, fmt.Sprintf("%s.yaml", agentName))
				if _, err := os.Stat(sysAgentPath); err == nil {
					agentPath = sysAgentPath
					goto Found
				}
			}

			// 4. Check in flows directory
			flowsDir, err := flowstore.GetFlowsDir()
			if err == nil {
				flowPath := filepath.Join(flowsDir, fmt.Sprintf("%s.yaml", agentName))
				if _, err := os.Stat(flowPath); err == nil {
					agentPath = flowPath
					goto Found
				}
			}

			// 5. Check in local dev path (fallback)
			agentPath = fmt.Sprintf("agents/%s.yaml", agentName)
			if _, err := os.Stat(agentPath); os.IsNotExist(err) {
				return fmt.Errorf("agent file not found: %s", agentName)
			}
		}
	}

Found:
	cfg, err := config.LoadAgent(agentPath)
	if err != nil {
		return fmt.Errorf("failed to load agent: %w", err)
	}

	ui.RenderCharmFlow(cfg)
	return nil
}

func handleEditCommand(args []string) error {
	if len(args) < 1 {
		fmt.Println("Usage: astonish flows edit <flow_name>")
		return fmt.Errorf("no agent name provided")
	}

	agentName := args[0]

	// Find the agent file path (reusing same logic as run/flow)
	agentPath := agentName
	if _, err := os.Stat(agentPath); os.IsNotExist(err) {
		// Check with .yaml extension
		agentPath = fmt.Sprintf("%s.yaml", agentName)
		if _, err := os.Stat(agentPath); os.IsNotExist(err) {
			// Check in standard system agents directory
			agentsDir, err := config.GetAgentsDir()
			if err == nil {
				sysAgentPath := filepath.Join(agentsDir, fmt.Sprintf("%s.yaml", agentName))
				if _, err := os.Stat(sysAgentPath); err == nil {
					agentPath = sysAgentPath
					goto Found
				}
			}

			// Check in flows directory
			flowsDir, err := flowstore.GetFlowsDir()
			if err == nil {
				flowPath := filepath.Join(flowsDir, fmt.Sprintf("%s.yaml", agentName))
				if _, err := os.Stat(flowPath); err == nil {
					agentPath = flowPath
					goto Found
				}
			}

			// Check in local dev path (fallback)
			agentPath = fmt.Sprintf("agents/%s.yaml", agentName)
			if _, err := os.Stat(agentPath); os.IsNotExist(err) {
				return fmt.Errorf("agent file not found: %s", agentName)
			}
		}
	}

Found:
	fmt.Printf("Opening %s in editor...\n", agentPath)
	return openInEditor(agentPath)
}

func handleImportCommand(args []string) error {
	if len(args) < 1 {
		fmt.Println("usage: astonish flows import <file.yaml>")
		fmt.Println("       astonish flows import <file.yaml> [--as <name>]")
		return fmt.Errorf("no file specified")
	}

	sourcePath := args[0]

	// Check if source file exists
	if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
		return fmt.Errorf("file not found: %s", sourcePath)
	}

	// Validate it's a YAML file
	if !strings.HasSuffix(strings.ToLower(sourcePath), ".yaml") && !strings.HasSuffix(strings.ToLower(sourcePath), ".yml") {
		return fmt.Errorf("file must be a YAML file (.yaml or .yml)")
	}

	// Validate it's a valid flow config
	if _, err := config.LoadAgent(sourcePath); err != nil {
		return fmt.Errorf("invalid flow file: %w", err)
	}

	// Get the flows directory
	flowsDir, err := flowstore.GetFlowsDir()
	if err != nil {
		return fmt.Errorf("failed to get flows directory: %w", err)
	}

	// Create flows directory if it doesn't exist
	if err := os.MkdirAll(flowsDir, 0755); err != nil {
		return fmt.Errorf("failed to create flows directory: %w", err)
	}

	// Determine destination name
	destName := filepath.Base(sourcePath)

	// Check for --as flag
	for i := 1; i < len(args); i++ {
		if args[i] == "--as" && i+1 < len(args) {
			destName = args[i+1]
			if !strings.HasSuffix(destName, ".yaml") && !strings.HasSuffix(destName, ".yml") {
				destName += ".yaml"
			}
			break
		}
	}

	destPath := filepath.Join(flowsDir, destName)

	// Check if destination already exists
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("flow already exists: %s\nUse a different name with --as <name>", destName)
	}

	// Read source file
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Write to destination
	if err := os.WriteFile(destPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	flowName := strings.TrimSuffix(destName, filepath.Ext(destName))
	fmt.Printf("✓ Imported flow: %s\n", flowName)
	fmt.Printf("  Run with: astonish flows run %s\n", flowName)
	return nil
}

func handleRemoveCommand(args []string) error {
	if len(args) < 1 {
		fmt.Println("usage: astonish flows remove <flow_name>")
		return fmt.Errorf("no flow name specified")
	}

	flowName := args[0]

	// Remove .yaml extension if provided
	flowName = strings.TrimSuffix(flowName, ".yaml")
	flowName = strings.TrimSuffix(flowName, ".yml")

	// Search for the flow in various locations
	var flowPath string
	var flowSource string

	// 1. Check in flows directory (primary location for imported flows)
	if flowsDir, err := flowstore.GetFlowsDir(); err == nil {
		path := filepath.Join(flowsDir, flowName+".yaml")
		if _, err := os.Stat(path); err == nil {
			flowPath = path
			flowSource = "flows"
		}
	}

	// 2. Check in agents directory (legacy)
	if flowPath == "" {
		if agentsDir, err := config.GetAgentsDir(); err == nil {
			path := filepath.Join(agentsDir, flowName+".yaml")
			if _, err := os.Stat(path); err == nil {
				flowPath = path
				flowSource = "agents"
			}
		}
	}

	// 3. Check if it's an installed store flow
	if flowPath == "" {
		if store, err := flowstore.NewStore(); err == nil {
			tapName, name := parseFlowRef(flowName)
			if path, ok := store.GetInstalledFlowPath(tapName, name); ok {
				flowPath = path
				flowSource = "store"
			}
		}
	}

	if flowPath == "" {
		return fmt.Errorf("flow not found: %s", flowName)
	}

	// Confirm deletion
	fmt.Printf("Remove flow '%s' from %s? [y/N]: ", flowName, flowSource)
	var response string
	fmt.Scanln(&response)
	if strings.ToLower(response) != "y" {
		fmt.Println("Cancelled.")
		return nil
	}

	// Delete the file
	if err := os.Remove(flowPath); err != nil {
		return fmt.Errorf("failed to remove flow: %w", err)
	}

	// Remove the corresponding knowledge doc so the vector store
	// watcher picks up the deletion and cleans up indexed chunks.
	if memDir, err := config.GetMemoryDir(nil); err == nil {
		docPath := filepath.Join(memDir, "flows", flowName+".md")
		os.Remove(docPath) // best-effort — may not exist
	}

	fmt.Printf("✓ Removed flow: %s\n", flowName)
	return nil
}

// Store command handlers

func handleStoreCommand(args []string) error {
	if len(args) < 1 || args[0] == "--help" || args[0] == "-h" {
		printStoreUsage()
		return nil
	}

	switch args[0] {
	case "list":
		return handleStoreListCommand(args[1:])
	case "install":
		return handleStoreInstallCommand(args[1:])
	case "uninstall":
		return handleStoreUninstallCommand(args[1:])
	case "update":
		return handleStoreUpdateCommand()
	case "search":
		return handleStoreSearchCommand(args[1:])
	default:
		return fmt.Errorf("unknown store command: %s", args[0])
	}
}

func printStoreUsage() {
	fmt.Println("usage: astonish flows store [-h] {list,install,uninstall,update,search} ...")
	fmt.Println("")
	fmt.Println("Browse and install flows from community repositories (taps).")
	fmt.Println("")
	fmt.Println("commands:")
	fmt.Println("  list                List all available flows from taps")
	fmt.Println("  install             Install a flow from a tap")
	fmt.Println("  uninstall           Remove an installed flow")
	fmt.Println("  update              Update all tap manifests")
	fmt.Println("  search              Search for flows")
	fmt.Println("")
	fmt.Println("options:")
	fmt.Println("  -h, --help          Show this help message")
	fmt.Println("")
	fmt.Println("Tip: Use 'astonish tap add <repo>' to add repositories")
}

func handleStoreListCommand(args []string) error {
	// Parse --tag / -t flags
	var tagFilters []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "--tag" || args[i] == "-t") && i+1 < len(args) {
			tagFilters = append(tagFilters, strings.ToLower(args[i+1]))
			i++ // Skip next arg
		}
	}

	store, err := flowstore.NewStore()
	if err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	// Update manifests
	fmt.Println("Fetching flow store manifests...")
	if err := store.UpdateAllManifests(); err != nil {
		fmt.Printf("Warning: %v\n", err)
	}

	flows := store.ListAllFlows()
	if len(flows) == 0 {
		fmt.Println("No flows found in stores.")
		fmt.Println("Tip: Make sure the store repositories have a valid manifest.yaml")
		return nil
	}

	// Filter by tags if specified (OR logic)
	if len(tagFilters) > 0 {
		var filtered []flowstore.Flow
		for _, f := range flows {
			if flowMatchesTags(f, tagFilters) {
				filtered = append(filtered, f)
			}
		}
		flows = filtered
		if len(flows) == 0 {
			fmt.Printf("No flows found matching tags: %s\n", strings.Join(tagFilters, ", "))
			return nil
		}
		fmt.Printf("Filtering by tags: %s\n\n", strings.Join(tagFilters, ", "))
	}

	// Group by tap
	byTap := make(map[string][]flowstore.Flow)
	for _, f := range flows {
		byTap[f.TapName] = append(byTap[f.TapName], f)
	}

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("63")).
		Bold(true)

	nameStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("205")).
		Bold(true)

	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))

	installedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("42"))

	tagStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39"))

	// Print official first, then others
	printTapFlows := func(tapName string, flows []flowstore.Flow, isOfficial bool) {
		var label string
		if isOfficial {
			label = "OFFICIAL STORE (use bare flow names)"
		} else {
			label = fmt.Sprintf("%s (use: %s/<flow>)", tapName, tapName)
		}
		fmt.Println(headerStyle.Render(label))

		for _, f := range flows {
			status := ""
			if f.Installed {
				status = installedStyle.Render(" [installed]")
			}

			// Show full name for community, bare name for official
			displayName := f.Name
			if !isOfficial {
				displayName = fmt.Sprintf("%s/%s", tapName, f.Name)
			}

			// Show tags if any
			tagDisplay := ""
			if len(f.Tags) > 0 {
				tagDisplay = tagStyle.Render(" [" + strings.Join(f.Tags, ", ") + "]")
			}

			fmt.Printf("  %s%s%s\n", nameStyle.Render(displayName), status, tagDisplay)
			fmt.Printf("    %s\n", descStyle.Render(f.Description))
		}
		fmt.Println()
	}

	// Official first
	if official, ok := byTap[flowstore.OfficialStoreName]; ok {
		printTapFlows(flowstore.OfficialStoreName, official, true)
	}

	// Then custom taps
	for tapName, tapFlows := range byTap {
		if tapName != flowstore.OfficialStoreName {
			printTapFlows(tapName, tapFlows, false)
		}
	}

	fmt.Println("Tip: Run 'astonish flows run <flow>' for official or '<tap>/<flow>' for community")
	fmt.Println("     Use --tag <tag> to filter by tag")
	return nil
}

// flowMatchesTags checks if a flow matches any of the given tags (OR logic)
func flowMatchesTags(f flowstore.Flow, tags []string) bool {
	for _, filterTag := range tags {
		for _, flowTag := range f.Tags {
			if strings.ToLower(flowTag) == filterTag {
				return true
			}
		}
	}
	return false
}

func handleStoreInstallCommand(args []string) error {
	if len(args) < 1 {
		fmt.Println("usage: astonish flows store install <tap>/<flow>")
		fmt.Println("       astonish flows store install <flow>  (from official store)")
		return fmt.Errorf("no flow specified")
	}

	store, err := flowstore.NewStore()
	if err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	// Parse tap/flow
	tapName, flowName := parseFlowRef(args[0])

	fmt.Printf("Installing %s/%s...\n", tapName, flowName)
	if err := store.InstallFlow(tapName, flowName); err != nil {
		return fmt.Errorf("failed to install flow: %w", err)
	}

	// Show appropriate run command based on tap
	runName := flowName
	if tapName != flowstore.OfficialStoreName {
		runName = tapName + "/" + flowName
	}
	fmt.Printf("✓ Installed flow: %s\n", runName)
	fmt.Printf("  Run with: astonish flows run %s\n", runName)
	return nil
}

func handleStoreUninstallCommand(args []string) error {
	if len(args) < 1 {
		fmt.Println("usage: astonish flows store uninstall <tap>/<flow>")
		return fmt.Errorf("no flow specified")
	}

	store, err := flowstore.NewStore()
	if err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	// Parse tap/flow
	tapName, flowName := parseFlowRef(args[0])

	if err := store.UninstallFlow(tapName, flowName); err != nil {
		return err
	}

	fmt.Printf("✓ Uninstalled flow: %s\n", flowName)
	return nil
}

func handleStoreUpdateCommand() error {
	store, err := flowstore.NewStore()
	if err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	fmt.Println("Updating all store manifests...")
	if err := store.UpdateAllManifests(); err != nil {
		return err
	}

	fmt.Println("✓ All stores updated")
	return nil
}

func handleStoreSearchCommand(args []string) error {
	if len(args) < 1 {
		fmt.Println("usage: astonish flows store search <query>")
		return fmt.Errorf("no search query specified")
	}

	store, err := flowstore.NewStore()
	if err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	// Update manifests
	if err := store.UpdateAllManifests(); err != nil {
		fmt.Printf("Warning: %v\n", err)
	}

	query := strings.ToLower(strings.Join(args, " "))
	flows := store.ListAllFlows()

	var matches []flowstore.Flow
	for _, f := range flows {
		matched := false

		// Check name and description
		if strings.Contains(strings.ToLower(f.Name), query) ||
			strings.Contains(strings.ToLower(f.Description), query) {
			matched = true
		}

		// Check tags
		if !matched {
			for _, tag := range f.Tags {
				if strings.Contains(strings.ToLower(tag), query) {
					matched = true
					break
				}
			}
		}

		if matched {
			matches = append(matches, f)
		}
	}

	if len(matches) == 0 {
		fmt.Printf("No flows found matching '%s'\n", query)
		return nil
	}

	nameStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("205")).
		Bold(true)

	fmt.Printf("Flows matching '%s':\n\n", query)
	for _, f := range matches {
		fmt.Printf("  %s/%s\n", f.TapName, nameStyle.Render(f.Name))
		fmt.Printf("    %s\n", f.Description)
	}

	return nil
}

// parseFlowRef parses "tap/flow" or "flow" (defaults to official)
func parseFlowRef(ref string) (tapName, flowName string) {
	if strings.Contains(ref, "/") {
		parts := strings.SplitN(ref, "/", 2)
		return parts[0], parts[1]
	}
	return flowstore.OfficialStoreName, ref
}

// parseTapAddArgs parses the arguments for "tap add" command
// Supports two formats:
// 1. add <url> [--as <alias>]
// 2. add <alias> <url> (when second arg contains a dot, indicating a hostname)
func parseTapAddArgs(args []string) (urlArg, alias string) {
	if len(args) == 0 {
		return "", ""
	}

	urlArg = args[0]
	alias = ""

	// Check if second arg exists and looks like a URL (contains a dot)
	if len(args) >= 2 && strings.Contains(args[1], ".") {
		// Format: <alias> <url>
		alias = args[0]
		urlArg = args[1]
	} else {
		// Format: <url> [--as <alias>]
		for i := 1; i < len(args); i++ {
			if args[i] == "--as" && i+1 < len(args) {
				alias = args[i+1]
				break
			}
		}
	}

	return urlArg, alias
}

// --- Remote mode flow handlers ---

func handleFlowsRemote(args []string) error {
	switch args[0] {
	case "list":
		return handleFlowsListRemote()
	case "run":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish flows run <name>")
		}
		return handleFlowsRunRemote(args[1:])
	case "show", "edit", "import", "remove", "store":
		return fmt.Errorf("'flows %s' is not available in remote mode (use Studio UI)", args[0])
	default:
		return fmt.Errorf("unknown flows command: %s", args[0])
	}
}

func handleFlowsListRemote() error {
	c, err := client.New()
	if err != nil {
		return err
	}

	flows, err := c.ListFlows()
	if err != nil {
		return fmt.Errorf("list flows: %w", err)
	}

	if len(flows) == 0 {
		fmt.Println("No flows found.")
		return nil
	}

	// Group flows by scope
	var personalFlows []client.FlowMeta
	var teamFlows []client.FlowMeta

	for _, f := range flows {
		switch f.Scope {
		case "personal":
			personalFlows = append(personalFlows, f)
		case "team":
			teamFlows = append(teamFlows, f)
		default:
			// Fallback: treat as team if scope unknown
			teamFlows = append(teamFlows, f)
		}
	}

	// Styles (same as local handleListCommand)
	sectionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("63")).
		Bold(true)

	nameStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("205")).
		Bold(true)

	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))

	teamStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).
		Bold(true)

	printSection := func(title string, agents []client.FlowMeta, style lipgloss.Style) {
		if len(agents) == 0 {
			return
		}

		sort.Slice(agents, func(i, j int) bool {
			return agents[i].Name < agents[j].Name
		})

		fmt.Println(style.Render(fmt.Sprintf("\n%s (%d)", title, len(agents))))

		for _, a := range agents {
			fmt.Printf("  %s\n", nameStyle.Render(a.Name))
			if a.Description != "" {
				fmt.Printf("    %s\n", descStyle.Render(a.Description))
			}
		}
	}

	fmt.Println(lipgloss.NewStyle().Bold(true).Render("AVAILABLE FLOWS"))

	printSection("📁 PERSONAL", personalFlows, sectionStyle)
	printSection("👥 TEAM", teamFlows, teamStyle)

	fmt.Println()
	return nil
}

func handleFlowsRunRemote(args []string) error {
	flowName := args[0]

	// Parse flags from remaining args
	params := make(map[string]string)
	autoApprove := false
	debugMode := false
	for i := 1; i < len(args); i++ {
		if (args[i] == "-p" || args[i] == "--param") && i+1 < len(args) {
			kv := args[i+1]
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) == 2 {
				params[parts[0]] = parts[1]
			}
			i++
		} else if args[i] == "--auto-approve" {
			autoApprove = true
		} else if args[i] == "--debug" {
			debugMode = true
		}
	}

	c, err := client.New()
	if err != nil {
		return err
	}

	// Generate a session ID for multi-turn interaction
	sessionID := fmt.Sprintf("flow-cli-%d", nowUnix())

	// Track current node for param injection
	var currentNode string

	// Spinner management (line-oriented flow runner)
	var spinnerProgram *tea.Program
	var spinnerDone chan struct{}
	lineHasContent := false

	stopSpinner := func(markDone bool, success bool) {
		if spinnerProgram != nil {
			spinnerProgram.Quit()
			if spinnerDone != nil {
				<-spinnerDone
			}
			spinnerProgram = nil
			spinnerDone = nil
			if markDone {
				if success {
					fmt.Println(ui.RenderStatusBadge("Done", true))
				} else {
					fmt.Println(ui.RenderStatusBadge("Failed", false))
				}
			}
		}
	}

	startSpinner := func(text string) {
		stopSpinner(false, false)
		if lineHasContent {
			fmt.Print("\n")
			lineHasContent = false
		}
		spinnerDone = make(chan struct{})
		spinnerModel := ui.NewSpinner(text)
		spinnerProgram = tea.NewProgram(spinnerModel, tea.WithInput(nil))
		go func() {
			spinnerProgram.Run() //nolint:errcheck
			close(spinnerDone)
		}()
	}

	defer stopSpinner(false, false)

	startSpinner(fmt.Sprintf("Starting flow: %s...", flowName))

	// Multi-turn loop
	message := "" // First turn starts with empty message
	for {
		req := &client.FlowChatRequest{
			AgentID:     flowName,
			SessionID:   sessionID,
			Message:     message,
			AutoApprove: autoApprove,
			CLIMode:     true, // Get ANSI-rendered output (tool boxes, etc.)
			Debug:       debugMode,
		}

		stream, err := c.SendFlowMessage(req)
		if err != nil {
			stopSpinner(false, false)
			return fmt.Errorf("flow execution: %w", err)
		}

		// Process SSE events for this turn
		needsInput := false
		needsApproval := false
		flowDone := false
		agentPrefixPrinted := false
		inToolBox := false
		var inputPromptText strings.Builder // Capture text before input_request for prompt

		for {
			event, err := stream.Next()
			if err != nil {
				break
			}

			switch event.Type {
			case "node":
				var payload struct {
					Node   string `json:"node"`
					Type   string `json:"type"`
					Silent bool   `json:"silent"`
				}
				if jsonErr := parseJSON(event.Data, &payload); jsonErr == nil {
					currentNode = payload.Node
					if currentNode == "END" {
						stopSpinner(true, true)
						flowDone = true
					} else if payload.Type == "input" {
						// Input node — stop spinner, wait for prompt text
						stopSpinner(false, false)
					} else if !payload.Silent {
						startSpinner(fmt.Sprintf("Processing %s...", payload.Node))
					}
				}

			case "text":
				var payload struct {
					Text string `json:"text"`
				}
				if jsonErr := parseJSON(event.Data, &payload); jsonErr == nil {
					text := payload.Text

					// Detect tool box boundaries (ANSI-rendered from cliMode)
					if strings.Contains(text, "╭") {
						inToolBox = true
					}
					if inToolBox {
						stopSpinner(false, false)
						if !agentPrefixPrinted {
							fmt.Printf("\n%sAgent:%s\n", launcher.ColorGreen, launcher.ColorReset)
							agentPrefixPrinted = true
						}
						fmt.Print(text)
						lineHasContent = !strings.HasSuffix(text, "\n")
						if strings.Contains(text, "╰") {
							inToolBox = false
						}
					} else {
						// Regular text output
						stopSpinner(false, false)
						if !agentPrefixPrinted {
							fmt.Printf("\n%sAgent:%s\n", launcher.ColorGreen, launcher.ColorReset)
							agentPrefixPrinted = true
						}
						fmt.Print(text)
						lineHasContent = !strings.HasSuffix(text, "\n")
						// Also capture for input prompt
						inputPromptText.WriteString(text)
					}
				}

			case "input_request":
				var payload struct {
					Options []interface{} `json:"options"`
				}
				if jsonErr := parseJSON(event.Data, &payload); jsonErr == nil {
					needsInput = true
					// If we have options, check if this is actually a tool approval
					// (approval_options vs input_options both come as input_request)
					if len(payload.Options) > 0 {
						// Check if options look like approval (Yes/No pattern)
						firstOpt := ""
						if s, ok := payload.Options[0].(string); ok {
							firstOpt = s
						}
						if firstOpt == "Yes" || firstOpt == "No" {
							needsInput = false
							needsApproval = true
							for _, opt := range payload.Options {
								if s, ok := opt.(string); ok {
									_ = s // approvalOptions tracked below
								}
							}
						}
					}
				}

			case "error", "error_info":
				stopSpinner(false, false)
				var payload struct {
					Error  string `json:"error"`
					Title  string `json:"title"`
					Reason string `json:"reason"`
				}
				if jsonErr := parseJSON(event.Data, &payload); jsonErr == nil {
					msg := payload.Error
					if msg == "" {
						msg = payload.Reason
					}
					if payload.Title != "" {
						msg = payload.Title + ": " + msg
					}
					if msg != "" {
						fmt.Fprintf(os.Stderr, "\n%sError:%s %s\n", launcher.ColorRed, launcher.ColorReset, msg)
						lineHasContent = false
					}
				}

		case "done":
			// Turn complete

		case "debug":
			if debugMode {
				var payload struct {
					Type   string `json:"type"`
					Tool   string `json:"tool"`
					Args   string `json:"args"`
					Result string `json:"result"`
					// init fields
					Provider string `json:"provider"`
					Model    string `json:"model"`
					Agent    string `json:"agent"`
					Tools    int    `json:"tools"`
				}
				if jsonErr := parseJSON(event.Data, &payload); jsonErr == nil {
					stopSpinner(false, false)
					switch payload.Type {
					case "init":
						fmt.Fprintf(os.Stderr, "%s[debug] init: provider=%s model=%s agent=%s tools=%d%s\n",
							launcher.ColorGray, payload.Provider, payload.Model, payload.Agent, payload.Tools, launcher.ColorReset)
					case "tool_call":
						fmt.Fprintf(os.Stderr, "%s[debug] tool_call: %s %s%s\n",
							launcher.ColorGray, payload.Tool, payload.Args, launcher.ColorReset)
					case "tool_response":
						fmt.Fprintf(os.Stderr, "%s[debug] tool_response: %s %s%s\n",
							launcher.ColorGray, payload.Tool, payload.Result, launcher.ColorReset)
					default:
						fmt.Fprintf(os.Stderr, "%s[debug] %s%s\n",
							launcher.ColorGray, event.Data, launcher.ColorReset)
					}
					lineHasContent = false
				}
			}
		}
		}
		stream.Close()

		// Ensure newline after content
		if lineHasContent {
			fmt.Println()
			lineHasContent = false
		}

		// Flow reached END
		if flowDone {
			return nil
		}

		// Handle input request
		if needsInput {
			// Check if we have a pre-provided param for this node
			if val, ok := params[currentNode]; ok {
				fmt.Println(ui.RenderStatusBadge(fmt.Sprintf("Auto-filled %s = %s", currentNode, val), true))
				message = val
				continue
			}

			// Extract prompt title/description from captured text
			promptText := strings.TrimSpace(inputPromptText.String())
			title := currentNode
			description := ""
			if promptText != "" {
				lines := strings.SplitN(promptText, "\n", 2)
				title = strings.TrimSpace(lines[0])
				if len(lines) > 1 {
					description = strings.TrimSpace(lines[1])
				}
			}

			// Use huh input widget (same as local mode)
			input, readErr := ui.ReadInput(title, description)
			if readErr != nil {
				return fmt.Errorf("reading input: %w", readErr)
			}
			fmt.Println(ui.RenderStatusBadge(fmt.Sprintf("Input: %s", input), true))
			message = input
			continue
		}

		// Handle approval request
		if needsApproval {
			if autoApprove {
				fmt.Println(ui.RenderStatusBadge("Auto Approved", true))
				message = "Yes"
				continue
			}
			opts := []string{"Yes", "No"}
			selection, selErr := ui.ReadSelection(opts, "Approval Required", "")
			if selErr != nil {
				return selErr
			}
			if selection == "Yes" {
				fmt.Println(ui.RenderStatusBadge("Command approved", true))
			} else {
				fmt.Println(ui.RenderStatusBadge("Command rejected", false))
			}
			message = selection
			continue
		}

		// No input/approval needed and flow didn't end — we're done
		return nil
	}
}

func nowUnix() int64 {
	return time.Now().UnixNano()
}

func parseJSON(data string, dst any) error {
	return json.Unmarshal([]byte(data), dst)
}
