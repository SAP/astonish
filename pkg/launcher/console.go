package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"regexp"
	"strings"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/SAP/astonish/pkg/ui"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"

	tea "charm.land/bubbletea/v2"
)

// ConsoleConfig contains configuration for the console launcher
type ConsoleConfig struct {
	AgentConfig    *config.AgentConfig
	AppConfig      *config.AppConfig
	ProviderName   string
	ModelName      string
	SessionService session.Service
	DebugMode      bool
	AutoApprove    bool
	Parameters     map[string]string
}

// minimalReadonlyContext implements agent.ReadonlyContext for tool discovery
type minimalReadonlyContext struct {
	context.Context
}

func (m *minimalReadonlyContext) AgentName() string                    { return "cache-refresh" }
func (m *minimalReadonlyContext) AppName() string                      { return "astonish" }
func (m *minimalReadonlyContext) UserContent() *genai.Content          { return nil }
func (m *minimalReadonlyContext) InvocationID() string                 { return "" }
func (m *minimalReadonlyContext) ReadonlyState() session.ReadonlyState { return nil }
func (m *minimalReadonlyContext) UserID() string                       { return "" }
func (m *minimalReadonlyContext) SessionID() string                    { return "" }
func (m *minimalReadonlyContext) Branch() string                       { return "" }

// getRequiredMCPServersFromConfig extracts MCP server names needed for the flow
// by matching tools_selection entries against the persistent tools cache
func getRequiredMCPServersFromConfig(ctx context.Context, agentCfg *config.AgentConfig, verbose bool) []string {
	// Load MCP config first to get server names
	mcpCfg, err := config.LoadMCPConfig()
	if err != nil || len(mcpCfg.MCPServers) == 0 {
		return nil
	}

	// Collect all required tools/servers from the flow
	toolsNeeded := make(map[string]bool)
	hasToolsEnabled := false

	for _, node := range agentCfg.Nodes {
		// If node has tools: true, it potentially needs MCP servers
		if node.Tools {
			hasToolsEnabled = true
		}
		for _, toolName := range node.ToolsSelection {
			toolsNeeded[toolName] = true
		}
	}

	// If no tools enabled at all, no servers needed
	if !hasToolsEnabled && len(toolsNeeded) == 0 {
		return nil
	}

	// If tools: true but no tools_selection, we need all servers
	if hasToolsEnabled && len(toolsNeeded) == 0 {
		var servers []string
		for serverName := range mcpCfg.MCPServers {
			servers = append(servers, serverName)
		}
		return servers
	}

	// Load persistent cache for tool→server lookup
	persistentCache, cacheLoadErr := cache.LoadCache()
	if cacheLoadErr != nil {
		slog.Warn("failed to load persistent cache", "error", cacheLoadErr)
	}

	// Validate checksums and refresh any changed servers (synchronously for CLI)
	needsRefresh, removed := cache.ValidateChecksums(verbose)

	// Remove stale servers from cache
	for _, serverName := range removed {
		cache.RemoveServer(serverName)
	}

	// Refresh changed servers synchronously
	if len(needsRefresh) > 0 {
		if verbose {
			slog.Info("refreshing servers", "component", "cache", "count", len(needsRefresh), "servers", needsRefresh)
		}
		refreshServers(ctx, mcpCfg, needsRefresh, verbose)
		// Reload cache after refresh
		var cacheErr error
		persistentCache, cacheErr = cache.LoadCache()
		if cacheErr != nil {
			slog.Warn("failed to reload persistent cache", "error", cacheErr)
		}
	}

	requiredServers := make(map[string]bool)

	for toolName := range toolsNeeded {
		// Try 1: Check if server name is directly in tools_selection
		if _, isServer := mcpCfg.MCPServers[toolName]; isServer {
			requiredServers[toolName] = true
			continue
		}

		// Try 2: Check if tool is prefixed with server name (server-name.tool-name)
		foundPrefix := false
		for serverName := range mcpCfg.MCPServers {
			if strings.HasPrefix(toolName, serverName+".") {
				requiredServers[serverName] = true
				foundPrefix = true
				break
			}
		}
		if foundPrefix {
			continue
		}

		// Try 3: Look up bare tool name in persistent cache
		if serverName := cache.GetServerForTool(toolName); serverName != "" {
			requiredServers[serverName] = true
			slog.Info("tool found in persistent cache", "component", "cache", "tool", toolName, "server", serverName)
			continue
		}

		// Tool not found in cache - this shouldn't happen if cache is up to date
		// Fall back to including all servers
		slog.Warn("tool not found in persistent cache, loading all servers", "component", "cache", "tool", toolName)
		for serverName := range mcpCfg.MCPServers {
			requiredServers[serverName] = true
		}
		break
	}

	// If cache was empty but we have tools_selection, load all servers
	if persistentCache == nil || len(persistentCache.Tools) == 0 {
		if len(toolsNeeded) > 0 {
			for serverName := range mcpCfg.MCPServers {
				requiredServers[serverName] = true
			}
		}
	}

	// Convert to slice
	var servers []string
	for serverName := range requiredServers {
		servers = append(servers, serverName)
	}
	return servers
}

// refreshServers initializes the given servers and updates the cache
func refreshServers(ctx context.Context, mcpCfg *config.MCPConfig, servers []string, verbose bool) {
	mcpManager, err := mcp.NewManager()
	if err != nil {
		if verbose {
			slog.Warn("failed to create mcp manager for refresh", "component", "cache", "error", err)
		}
		return
	}

	for _, serverName := range servers {
		namedToolset, err := mcpManager.InitializeSingleToolset(ctx, serverName)
		if err != nil {
			if verbose {
				slog.Warn("failed to initialize server", "component", "cache", "server", serverName, "error", err)
			}
			continue
		}

		// Get tools from this server
		minimalCtx := &minimalReadonlyContext{Context: ctx}
		mcpTools, err := namedToolset.Toolset.Tools(minimalCtx)
		if err != nil {
			if verbose {
				slog.Warn("failed to get tools from server", "component", "cache", "server", serverName, "error", err)
			}
			continue
		}

		// Update persistent cache
		persistentTools := make([]cache.ToolEntry, 0, len(mcpTools))
		for _, t := range mcpTools {
			persistentTools = append(persistentTools, cache.ToolEntry{
				Name:        t.Name(),
				Description: t.Description(),
				Source:      serverName,
				InputSchema: common.ExtractToolInputSchema(t),
			})
		}
		serverCfg := mcpCfg.MCPServers[serverName]
		checksum := cache.ComputeServerChecksum(serverCfg.Command, serverCfg.Args, serverCfg.Env)
		cache.AddServerTools(serverName, persistentTools, checksum)
		if verbose {
			slog.Info("refreshed server", "component", "cache", "server", serverName, "tools", len(persistentTools))
		}
	}

	if err := cache.SaveCache(); err != nil {
		if verbose {
			slog.Warn("failed to save persistent cache after refresh", "component", "cache", "error", err)
		}
	}
}

// RunConsole runs the agent in console mode with agent-controlled flow
func RunConsole(ctx context.Context, cfg *ConsoleConfig) error {
	// Suppress default logger (used by ADK for "unknown agent" warnings)
	// Only suppress if NOT in debug mode
	if !cfg.DebugMode {
		log.SetOutput(io.Discard)
	}

	// Initialize LLM
	if cfg.DebugMode {
		fmt.Println("Initializing LLM provider...")
		provider.SetDebugMode(true)
	}
	llm, err := provider.GetProvider(ctx, cfg.ProviderName, cfg.ModelName, cfg.AppConfig)
	if err != nil {
		fmt.Printf("ERROR: Failed to initialize provider '%s' with model '%s': %v\n", cfg.ProviderName, cfg.ModelName, err)
		return fmt.Errorf("failed to initialize provider: %w", err)
	}
	if cfg.DebugMode {
		fmt.Printf("✓ Provider initialized: %s (model: %s)\n", cfg.ProviderName, cfg.ModelName)
	}

	// Initialize internal tools
	if cfg.DebugMode {
		fmt.Println("Initializing internal tools...")
	}
	internalTools, err := tools.GetInternalTools()
	if err != nil {
		fmt.Printf("ERROR: Failed to initialize tools: %v\n", err)
		return fmt.Errorf("failed to initialize internal tools: %w", err)
	}

	// Register credential tools (resolve_credential, etc.)
	credTools, credErr := tools.GetCredentialTools()
	if credErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create credential tools", "error", credErr)
		}
	} else {
		internalTools = append(internalTools, credTools...)
	}

	// Register process management tools (process_start, process_write, etc.)
	processTools, procErr := tools.GetProcessTools()
	if procErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create process tools", "error", procErr)
		}
	} else {
		internalTools = append(internalTools, processTools...)
	}

	// Register browser automation tools
	browserMgr := browser.NewManager(browserConfigFromApp(cfg.AppConfig))
	wireBrowserContainerCallbacks(browserMgr)
	browserTools, browserErr := tools.GetBrowserTools(browserMgr)
	if browserErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create browser tools", "error", browserErr)
		}
	} else {
		internalTools = append(internalTools, browserTools...)
	}
	defer browserMgr.Cleanup()

	// Wrap tools with sandbox if enabled (container isolation for file/shell/network tools)
	internalTools, sandboxCleanup, sandboxErr := setupFlowSandbox(cfg.AppConfig, internalTools, cfg.DebugMode)
	if sandboxErr != nil {
		return fmt.Errorf("sandbox is enabled but the runtime is not available: %w\n\nTo disable sandbox, set 'sandbox.enabled: false' in ~/.config/astonish/config.yaml", sandboxErr)
	}
	defer sandboxCleanup()

	if cfg.DebugMode {
		fmt.Printf("✓ Internal tools initialized: %d tools available\n", len(internalTools))
	}

	// Initialize MCP tools - only servers needed for this flow
	if cfg.DebugMode {
		fmt.Println("Initializing MCP servers...")
	}

	// Extract required MCP servers from flow config (validates cache and refreshes if needed)
	requiredServers := getRequiredMCPServersFromConfig(ctx, cfg.AgentConfig, cfg.DebugMode)

	var mcpManager *mcp.Manager
	var mcpToolsets []tool.Toolset

	if len(requiredServers) > 0 {
		var err error
		mcpManager, err = mcp.NewManager()
		if err != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create mcp manager", "error", err)
			}
		} else {
			if err := mcpManager.InitializeSelectiveToolsets(ctx, requiredServers); err != nil {
				if cfg.DebugMode {
					slog.Warn("failed to initialize mcp toolsets", "error", err)
				}
			} else {
				mcpToolsets = mcpManager.GetToolsets()
				if cfg.DebugMode {
					fmt.Printf("✓ MCP servers initialized: %d/%d server(s) needed for this flow\n", len(mcpToolsets), len(requiredServers))
				}
			}
		}
	} else {
		if cfg.DebugMode {
			fmt.Println("✓ No MCP servers needed for this flow")
		}
	}

	// Ensure MCP cleanup when run completes
	if mcpManager != nil {
		defer mcpManager.Cleanup()
	}

	// Create session service
	sessionService := cfg.SessionService
	if sessionService == nil {
		if cfg.AppConfig != nil && cfg.AppConfig.Sessions.Storage == "file" {
			sessDir, dirErr := config.GetSessionsDir(&cfg.AppConfig.Sessions)
			if dirErr != nil {
				return fmt.Errorf("failed to resolve sessions directory: %w", dirErr)
			}
			fileStore, fsErr := persistentsession.NewFileStore(sessDir)
			if fsErr != nil {
				return fmt.Errorf("failed to create file session store: %w", fsErr)
			}
			sessionService = fileStore
		} else {
			sessionService = session.InMemoryService()
		}
	}

	// Create Astonish agent with internal tools
	// MCP toolsets will be passed directly to llmagent when creating nodes
	if cfg.DebugMode {
		fmt.Println("Creating agent...")
	}
	astonishAgent := agent.NewAstonishAgentWithToolsets(cfg.AgentConfig, llm, internalTools, mcpToolsets)
	astonishAgent.DebugMode = cfg.DebugMode
	astonishAgent.AutoApprove = cfg.AutoApprove
	astonishAgent.SessionService = sessionService

	// Wire credential redactor and store for placeholder substitution
	if cs := tools.GetCredentialStore(); cs != nil {
		astonishAgent.Redactor = cs.Redactor()
		astonishAgent.CredentialStore = cs
		astonishAgent.PendingSecrets = credentials.NewPendingVault(cs.Redactor())
		// Inject Redactor into context so memory_save can Placeholderize()
		ctx = credentials.WithRedactor(ctx, cs.Redactor())
		// Attach proactive secret scanner
		if cfg.AppConfig == nil || cfg.AppConfig.Security.IsSecretScannerEnabled() {
			scanner := credentials.NewSecretScanner()
			if cfg.AppConfig != nil {
				sc := cfg.AppConfig.Security.SecretScanner
				if sc.EntropyThreshold > 0 {
					scanner.EntropyThreshold = sc.EntropyThreshold
				}
				if sc.MinTokenLength > 0 {
					scanner.MinTokenLength = sc.MinTokenLength
				}
			}
			astonishAgent.PendingSecrets.Scanner = scanner
		}
	}

	// Create ADK agent wrapper
	adkAgent, err := adkagent.New(adkagent.Config{
		Name:        "astonish_agent",
		Description: cfg.AgentConfig.Description,
		Run:         astonishAgent.Run,
	})
	if err != nil {
		fmt.Printf("ERROR: Failed to create ADK agent: %v\n", err)
		return fmt.Errorf("failed to create ADK agent: %w", err)
	}
	if cfg.DebugMode {
		fmt.Println("✓ Agent created")
	}

	// Create session
	if cfg.DebugMode {
		fmt.Println("Creating session...")
	}
	userID, appName := "console_user", "astonish"
	resp, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName: appName,
		UserID:  userID,
	})
	if err != nil {
		fmt.Printf("ERROR: Failed to create session: %v\n", err)
		return fmt.Errorf("failed to create session: %w", err)
	}
	if cfg.DebugMode {
		fmt.Println("✓ Session created")
	}

	sess := resp.Session
	sandbox.WarmFlowSession(ctx, internalTools, sess.ID())

	// Create runner
	if cfg.DebugMode {
		fmt.Println("Creating runner...")
	}
	r, err := runner.New(runner.Config{
		AppName:        appName,
		Agent:          adkAgent,
		SessionService: sessionService,
	})
	if err != nil {
		fmt.Printf("ERROR: Failed to create runner: %v\n", err)
		return fmt.Errorf("failed to create runner: %w", err)
	}
	if cfg.DebugMode {
		fmt.Println("✓ Runner created")
	}

	// Colors defined in colors.go (package level)

	// Start with empty message to let agent initialize and show first prompt
	var userMsg *genai.Content

	// Track current node to determine visibility across turns
	var currentNodeName string

	// Buffer for handling fragmented streaming output
	var lineBuffer string
	var textBuffer strings.Builder // Buffer for accumulating text to be rendered as markdown
	var inToolBlock bool
	var inToolBox bool

	// Spinner state
	var spinnerProgram *tea.Program
	var spinnerDone chan struct{}
	var currentSpinnerText string

	stopSpinner := func(markDone bool, success bool) {
		if spinnerProgram != nil {
			spinnerProgram.Quit()
			if spinnerDone != nil {
				<-spinnerDone
			}
			spinnerProgram = nil
			spinnerDone = nil

			if markDone && currentSpinnerText != "" {
				if success {
					fmt.Printf("✓ %s\n", currentSpinnerText)
				} else {
					fmt.Printf("%s✕%s %s\n", ColorRed, ColorReset, currentSpinnerText)
				}
			}
			currentSpinnerText = ""
		}
	}

	startSpinner := func(text string) {
		stopSpinner(true, true) // Mark previous spinner as done before starting new one
		currentSpinnerText = text
		spinnerDone = make(chan struct{})
		model := ui.NewSpinner(text)
		spinnerProgram = tea.NewProgram(model, tea.WithInput(nil))
		go func() {
			spinnerProgram.Run()
			close(spinnerDone)
		}()
	}

	for {
		// Reset state flags at start of turn
		inToolBox = false
		inToolBlock = false

		// Run the agent
		// Only print the AI prefix if we are actually going to print something from the AI
		aiPrefixPrinted := false

		// State tracking for input nodes
		isInputNode := false
		isOutputNode := false
		waitingForInput := false
		waitingForApproval := false
		var approvalOptions []string
		var inputOptions []string
		isAutoApproved := false

		// Declare suppression variables here so they are accessible throughout the loop and after
		suppressStreaming := false
		var userMessageFields []string

		// seenPartialText filters out aggregated text events that duplicate
		// already-streamed partial chunks (ADK partial vs aggregate dedup).
		seenPartialText := false
		nodeJustChanged := false          // Flag to skip userMessage processing on initial node change event
		turnHadUserMessageFields := false // Track if any node in this turn had userMessageFields (persists across node changes)

		for event, err := range r.Run(ctx, userID, sess.ID(), userMsg, adkagent.RunConfig{
			StreamingMode: adkagent.StreamingModeSSE,
		}) {
			if err != nil {
				fmt.Printf("\nERROR: %v\n", err)
				return err
			}

			// Reset the nodeJustChanged flag at start of each event
			// It will be set to true only if this event triggers a node change
			nodeJustChanged = false

			// Debug logging for tool calls and responses
			if cfg.DebugMode && event.LLMResponse.Content != nil {
				// Flush text buffer before debug output
				if textBuffer.Len() > 0 {
					rendered := ui.SmartRender(textBuffer.String())
					if !aiPrefixPrinted {
						fmt.Printf("\n%sAgent:%s\n", ColorGreen, ColorReset)
						aiPrefixPrinted = true
					}
					fmt.Print(rendered)
					textBuffer.Reset()
				}

				for _, part := range event.LLMResponse.Content.Parts {
					if part.FunctionCall != nil {
						argsJSON, _ := json.MarshalIndent(part.FunctionCall.Args, "", "  ")
						slog.Debug("tool call", "tool", part.FunctionCall.Name, "args", string(argsJSON))
					}
					if part.FunctionResponse != nil {
						respJSON, _ := json.MarshalIndent(part.FunctionResponse.Response, "", "  ")
						slog.Debug("tool response", "tool", part.FunctionResponse.Name, "result", string(respJSON))
					}
				}
			}

			// Check if we should suppress streaming output based on UserMessage OR OutputModel config
			// suppressStreaming is now declared outside the loop
			// suppressStreaming is now declared outside the loop and updated in the node change block below.

			// Check for user_message display marker - this indicates user_message text will be in this event
			if event.Actions.StateDelta != nil {
				if _, hasMarker := event.Actions.StateDelta["_user_message_display"]; hasMarker {
					// Stop spinner and print Agent: prefix before the user_message content
					stopSpinner(true, true)
					if !aiPrefixPrinted {
						fmt.Printf("\n%sAgent:%s\n", ColorGreen, ColorReset)
						aiPrefixPrinted = true
					}
					// Note: Do NOT set suppressStreaming = false here
					// The text will be printed via the StateDelta-based mechanism (lines 539+)
					// which handles formatting properly
				}

				// Check for error_message display marker - these MUST be shown even if streaming is suppressed
				if _, hasMarker := event.Actions.StateDelta["_error_message_display"]; hasMarker {
					// This is an error message that must be displayed
					// Temporarily disable suppression for this event only
					suppressStreaming = false
					stopSpinner(true, false)

					// Check if this is a processing info message (no Agent: prefix)
					if _, isProcessingInfo := event.Actions.StateDelta["_processing_info"]; !isProcessingInfo {
						if !aiPrefixPrinted {
							fmt.Printf("\n%sAgent:%s\n", ColorGreen, ColorReset)
							aiPrefixPrinted = true
						}
					}
				}

				// Check for spinner text update
				if spinnerText, ok := event.Actions.StateDelta["_spinner_text"].(string); ok {
					startSpinner(spinnerText)
				}

				// Check for Retry Info
				if retryInfoVal, ok := event.Actions.StateDelta["_retry_info"]; ok {
					if retryInfo, ok := retryInfoVal.(map[string]any); ok {
						stopSpinner(false, true)

						// Extract fields
						var attempt, maxRetries int

						if a, ok := retryInfo["attempt"].(int); ok {
							attempt = a
						} else if a, ok := retryInfo["attempt"].(float64); ok {
							attempt = int(a)
						}

						if m, ok := retryInfo["max_retries"].(int); ok {
							maxRetries = m
						} else if m, ok := retryInfo["max_retries"].(float64); ok {
							maxRetries = int(m)
						}

						reason := retryInfo["reason"].(string)

						// Render badge
						badge := ui.RenderRetryBadge(attempt, maxRetries, reason)

						// Print with indentation (3 spaces) - no leading newline
						fmt.Printf("   %s\n", badge)
					}
				}

				// Check for Failure Info
				if failureInfoVal, ok := event.Actions.StateDelta["_failure_info"]; ok {
					if failureInfo, ok := failureInfoVal.(map[string]any); ok {
						stopSpinner(true, false)

						// Extract fields
						title := failureInfo["title"].(string)
						reason := failureInfo["reason"].(string)
						originalError := failureInfo["original_error"].(string)
						suggestion, _ := failureInfo["suggestion"].(string) // Optional

						// Render error box
						box := ui.RenderErrorBox(title, reason, suggestion, originalError)

						// Print directly (no SmartRender - we want raw ANSI codes to pass through)
						fmt.Print(box)
					}
				}
			}

			// Update current node from StateDelta if present
			if event.Actions.StateDelta != nil {
				if node, ok := event.Actions.StateDelta["current_node"].(string); ok {
					// Only process if node actually changed
					if node != currentNodeName {
						// Mark that we're in a node change event - skip userMessage processing on this event
						nodeJustChanged = true

						// FIRST: Compute new node settings BEFORE any flush decisions
						currentNodeName = node

						// Store OLD suppression state for buffer handling
						wasSupressing := suppressStreaming

						// Reset and re-evaluate suppression for the NEW node
						suppressStreaming = false
						userMessageFields = nil
						isInputNode = false
						isOutputNode = false
						isParallel := false
						isSilent := false
						hasUserMessage := false
						hasOutputModel := false
						isAutoApproved = false

						for _, n := range cfg.AgentConfig.Nodes {
							if n.Name == currentNodeName {
								if n.Type == "input" {
									isInputNode = true
									suppressStreaming = true
								} else if n.Type == "output" {
									isOutputNode = true
									suppressStreaming = false
								} else {
									if n.Parallel != nil {
										isParallel = true
									}
									if n.Silent {
										isSilent = true
									}

									hasUserMessage = len(n.UserMessage) > 0
									hasOutputModel = len(n.OutputModel) > 0

									if hasUserMessage {
										suppressStreaming = true
										userMessageFields = n.UserMessage
										turnHadUserMessageFields = true // Remember this turn had user_message
									} else if hasOutputModel {
										suppressStreaming = true
									}
								}
								if cfg.DebugMode {
									slog.Debug("node changed", "node", currentNodeName, "suppressStreaming", suppressStreaming, "isParallel", isParallel)
								}
								break
							}
						}

						// NOW flush buffers if the PREVIOUS node wasn't suppressing
						if !wasSupressing {
							// Stop spinner before printing flush content
							stopSpinner(true, true)

							// Flush lineBuffer
							if lineBuffer != "" {
								rendered := ui.SmartRender(lineBuffer)
								fmt.Print(rendered)
								if !strings.HasSuffix(rendered, "\n") {
									fmt.Println()
								}
								lineBuffer = ""
							}

							// Flush textBuffer
							if textBuffer.Len() > 0 {
								rendered := ui.SmartRender(textBuffer.String())
								if rendered != "" {
									if !aiPrefixPrinted {
										fmt.Printf("\n%sAgent:%s\n", ColorGreen, ColorReset)
										aiPrefixPrinted = true
									}
									fmt.Print(rendered)
								}
								textBuffer.Reset()
							}
						} else {
							// If we were suppressing, clear BOTH buffers to prevent leakage
							// The userMessage block handles display from StateDelta
							lineBuffer = ""
							textBuffer.Reset()
						}

						// Manage Spinner for new node
						if isInputNode || isParallel || isSilent {
							stopSpinner(true, true)
						} else {
							startSpinner(fmt.Sprintf("Processing %s...", currentNodeName))
						}
					} else if spinnerProgram == nil && event.LLMResponse.Content == nil {
						// Node hasn't changed, but spinner was stopped (e.g. by tool box)
						// Restart it if we are not about to print content
						startSpinner(fmt.Sprintf("Processing %s...", currentNodeName))
					}
				}

				// Check for approval state
				if awaitingVal, ok := event.Actions.StateDelta["awaiting_approval"]; ok {
					if awaiting, ok := awaitingVal.(bool); ok && awaiting {
						waitingForApproval = true
					}
				}

				// Check for auto-approval - handle IMMEDIATELY
				if autoApprovedVal, ok := event.Actions.StateDelta["auto_approved"]; ok {
					if auto, ok := autoApprovedVal.(bool); ok && auto {
						// Stop spinner before printing
						stopSpinner(true, true)

						// Print the tool info from the event content
						// Note: formatToolApprovalRequest already returns ANSI-formatted output
						// from ui.RenderToolBox, so we print it directly without SmartRender
						if event.LLMResponse.Content != nil {
							for _, part := range event.LLMResponse.Content.Parts {
								if part.Text != "" {
									fmt.Print(part.Text)
								}
							}
						}

						// Print Auto-Approved Badge
						fmt.Println(ui.RenderStatusBadge("Auto Approved", true))

						// Simulate "Yes" selection
						userMsg = agent.NewTimestampedUserContent("Yes") // Reset flags
						waitingForApproval = false
						isAutoApproved = false

						continue
					}
				}

				// Check for approval options
				if optsVal, ok := event.Actions.StateDelta["approval_options"]; ok {
					if opts, ok := optsVal.([]string); ok {
						approvalOptions = opts
					} else if optsInterface, ok := optsVal.([]interface{}); ok {
						for _, v := range optsInterface {
							approvalOptions = append(approvalOptions, fmt.Sprintf("%v", v))
						}
					}
				}

				// Check for input options
				if optsVal, ok := event.Actions.StateDelta["input_options"]; ok {
					if opts, ok := optsVal.([]string); ok {
						inputOptions = opts
					} else if optsInterface, ok := optsVal.([]interface{}); ok {
						for _, v := range optsInterface {
							inputOptions = append(inputOptions, fmt.Sprintf("%v", v))
						}
					}
				}

				// Check for UserMessage fields in StateDelta and print them if found
				// Only run this if we're in user_message mode (suppressStreaming with userMessageFields)
				// IMPORTANT: Skip on events that just triggered a node change - wait for actual LLM response
				if len(userMessageFields) > 0 && suppressStreaming && !nodeJustChanged {
					// Check if any fields are present in this event
					hasUserMessageContent := false
					for _, field := range userMessageFields {
						if _, ok := event.Actions.StateDelta[field]; ok {
							hasUserMessageContent = true
							break
						}
					}

					// If we have user_message content, ensure Agent prefix is printed first
					if hasUserMessageContent {
						// Stop spinner before printing
						stopSpinner(true, true)

						// Print Agent prefix if not already printed
						if !aiPrefixPrinted {
							fmt.Printf("\n%sAgent:%s\n", ColorGreen, ColorReset)
							aiPrefixPrinted = true
						}

						// Now print each field
						for _, field := range userMessageFields {
							if val, ok := event.Actions.StateDelta[field]; ok {
								// Format the value for display
								var displayStr string
								switch v := val.(type) {
								case string:
									displayStr = v
								default:
									// Use YAML-like formatting for everything else
									displayStr = ui.FormatAsYamlLike(v, 0)
								}

								// Render and print immediately
								if displayStr != "" {
									// For output nodes, we want to ensure it's visible
									// If it's a list, render it nicely
									if strings.Contains(displayStr, "\n- ") {
										fmt.Printf("\n%s\n", displayStr)
									} else {
										// Standard output
										rendered := ui.SmartRender(displayStr)
										fmt.Print(rendered)
										// Ensure newline at end if not present
										if !strings.HasSuffix(rendered, "\n") {
											fmt.Println()
										}
									}
								}
							}
						}

						// CRITICAL: Clear text buffer to prevent duplicate display
						// Since we've already displayed the user_message content from StateDelta,
						// we don't want the text content from the event to be displayed again
						textBuffer.Reset()
						lineBuffer = ""
					}
				}
			}

			if event.LLMResponse.Content == nil {
				continue
			}

			// Extract text from response, filtering out aggregated duplicates.
			chunk := ""
			for _, p := range event.LLMResponse.Content.Parts {
				if p.Text != "" {
					if event.LLMResponse.Partial {
						seenPartialText = true
						chunk += p.Text
					} else if !seenPartialText {
						chunk += p.Text
					} else {
						seenPartialText = false
					}
				}
				if p.FunctionCall != nil || p.FunctionResponse != nil {
					seenPartialText = false
				}
			}

			if chunk != "" {
				lineBuffer += chunk

				// Process complete lines
				for {
					newlineIdx := strings.Index(lineBuffer, "\n")
					if newlineIdx == -1 {
						break
					}

					line := lineBuffer[:newlineIdx+1] // Include newline
					lineBuffer = lineBuffer[newlineIdx+1:]

					// Check for Tool Block Start (Internal XML)
					if strings.Contains(line, "<tool_use>") {
						inToolBlock = true
					}

					// Check for Tool Box Start (Visual UI)
					if strings.Contains(line, "╭") {
						inToolBox = true
						// FLUSH TEXT BUFFER - show any text that came BEFORE the tool box
						// This captures the LLM's greeting/explanation message
						if textBuffer.Len() > 0 {
							rendered := ui.SmartRender(textBuffer.String())
							// Only print Agent: if there's actual content to show
							if rendered != "" {
								stopSpinner(true, true)
								if !aiPrefixPrinted {
									fmt.Printf("\n%sAgent:%s\n", ColorGreen, ColorReset)
									aiPrefixPrinted = true
								}
								fmt.Print(rendered)
								// Add newline if not present
								if !strings.HasSuffix(rendered, "\n") {
									fmt.Println()
								}
							}
							textBuffer.Reset()
						}
					}

					shouldPrint := false
					isSystemMsg := false

					if inToolBlock {
						shouldPrint = false
					} else if inToolBox {
						isSystemMsg = true
						shouldPrint = true
						// Manual coloring removed - relying on lipgloss styles from agent
					} else if strings.Contains(line, "--- Node") {
						isSystemMsg = true
						shouldPrint = true
						// Colorize Node Header
						line = strings.ReplaceAll(line, "--- Node", fmt.Sprintf("%s--- Node", ColorCyan))
						line = strings.ReplaceAll(line, " ---", fmt.Sprintf(" ---%s", ColorReset))
					} else if strings.Contains(line, "[ℹ️ Info]") {
						isSystemMsg = true
						shouldPrint = true
						// Colorize Info
						line = strings.ReplaceAll(line, "[ℹ️ Info]", fmt.Sprintf("%s[ℹ️ Info]%s", ColorBlue, ColorReset))
					} else if strings.Contains(line, "Do you approve this execution?") {
						isSystemMsg = true
						shouldPrint = true
						waitingForApproval = true
					} else if strings.Contains(line, "> Yes") || strings.Contains(line, "  No") {
						isSystemMsg = true
						shouldPrint = true
					} else {
						// Regular LLM Output: Check suppression
						// For output_model/user_message nodes, we suppress completely
						// The user_message mechanism will handle proper output display
						if !suppressStreaming || isInputNode {
							shouldPrint = true
						}
						// When suppressing, do NOT buffer text - prevents double output
						// The _user_message_display event will handle display properly
					}

					// Check for Tool Block End
					if strings.Contains(line, "</tool_use>") {
						inToolBlock = false
					}

					// Check for Tool Box End
					if strings.Contains(line, "╰") {
						inToolBox = false
					}

					if shouldPrint {
						if isSystemMsg {
							// Stop spinner before printing system message (tool box, approval, etc.)
							// Mark as NOT done (false) since we're just pausing for system output
							stopSpinner(false, true)

							// FLUSH TEXT BUFFER before printing system message
							if textBuffer.Len() > 0 {
								rendered := ui.SmartRender(textBuffer.String())
								if rendered != "" {
									if !aiPrefixPrinted {
										fmt.Printf("\n%sAgent:%s\n", ColorGreen, ColorReset)
									}
									fmt.Print(rendered)
								}
								textBuffer.Reset()
							}

							// Print System Message
							toPrint := line
							// If we are suppressing streaming, but the line contains a tool box start,
							// we should print any text BEFORE the tool box (greeting/explanation from LLM)
							// BUT we must preserve the ANSI color codes that immediately precede the box.
							if suppressStreaming && strings.Contains(line, "╭") {
								// Regex to find the tool box start and any immediately preceding ANSI color codes
								// This matches: (optional ANSI codes) followed by "╭"
								re := regexp.MustCompile(`((?:\x1b\[[0-9;]*m)*)╭`)
								loc := re.FindStringIndex(line)
								if loc != nil && loc[0] > 0 {
									// There's text BEFORE the tool box - this is likely a greeting from the LLM
									// Print it as regular AI output
									priorText := line[:loc[0]]
									if strings.TrimSpace(priorText) != "" {
										stopSpinner(true, true)
										if !aiPrefixPrinted {
											fmt.Printf("\n%sAgent:%s\n", ColorGreen, ColorReset)
										}
										fmt.Print(ui.SmartRender(priorText))
									}
									// loc[0] is the start of the match (including the ANSI codes)
									toPrint = line[loc[0]:]
								}
							}
							fmt.Print(toPrint)

							// Reset prefix state for next AI output (since we interrupted with system msg)
							aiPrefixPrinted = false
						} else {
							// Buffer regular text
							textBuffer.WriteString(line)

							// Flush immediately for streaming effect (only when not suppressing)
							if !suppressStreaming && textBuffer.Len() > 0 {
								// Stop spinner ONLY when we're actually going to print
								stopSpinner(true, true)

								var rendered string
								if isOutputNode {
									// For output nodes, bypass SmartRender to preserve formatting (e.g. JSON)
									rendered = textBuffer.String()
								} else {
									rendered = ui.SmartRender(textBuffer.String())
								}

								if rendered != "" {
									if !aiPrefixPrinted {
										fmt.Printf("\n%sAgent:%s\n", ColorGreen, ColorReset)
										aiPrefixPrinted = true
									}
									fmt.Print(rendered)
								}
								textBuffer.Reset()
							}
							// When suppressing, text stays in buffer until tool box appears
						}
					}
				}
			}

			// Check if we're at an input node
			if currentNodeName != "" {
				for _, node := range cfg.AgentConfig.Nodes {
					if node.Name == currentNodeName && node.Type == "input" {
						waitingForInput = true
						break
					}
				}
			}
		}

		// Flush any remaining content in lineBuffer
		if lineBuffer != "" {
			// Only flush if NOT suppressed OR if it's an input node (to capture prompt)
			if !suppressStreaming || isInputNode {
				textBuffer.WriteString(lineBuffer)
			}
			lineBuffer = ""
		}

		// Flush text buffer at the end of the turn
		// If we are waiting for input, we CAPTURE the text buffer as the prompt instead of printing it
		if waitingForInput || waitingForApproval {
			// Capture prompt from text buffer
			var title, description string
			if textBuffer.Len() > 0 {
				// SmartRender returns text with ANSI color codes. We need to strip them
				// to get clean text for the title/description and status badge.
				rawRendered := ui.SmartRender(textBuffer.String())
				ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*m`)
				cleanText := ansiRegex.ReplaceAllString(rawRendered, "")
				promptText := strings.TrimSpace(cleanText)

				parts := strings.SplitN(promptText, "\n", 2)
				if len(parts) > 0 {
					title = strings.TrimSpace(parts[0])
				}
				if len(parts) > 1 {
					// Custom cleaning to preserve indentation while removing leading/trailing empty lines
					// This fixes alignment issues when Glamour adds indentation to lists
					descLines := strings.Split(parts[1], "\n")
					start := 0
					for start < len(descLines) && strings.TrimSpace(descLines[start]) == "" {
						start++
					}
					end := len(descLines)
					for end > start && strings.TrimSpace(descLines[end-1]) == "" {
						end--
					}
					if start < end {
						description = strings.Join(descLines[start:end], "\n")
					}
				}

				// Clear text buffer as we've consumed it
				textBuffer.Reset()
			}

			// Stop spinner before showing input
			stopSpinner(true, true)

			// Check for CLI parameter override for input nodes
			if waitingForInput && cfg.Parameters != nil {
				if val, ok := cfg.Parameters[currentNodeName]; ok {
					// Print confirmation
					fmt.Printf("✓ Using provided value for '%s': %s\n", currentNodeName, val)

					// Create user message with provided value
					userMsg = agent.NewTimestampedUserContent(val)

					// Reset state and continue loop
					waitingForInput = false
					continue
				}
			}

			// Show input dialog
			if waitingForApproval {
				// Handle Auto-Approval
				if isAutoApproved {
					// Print the tool description (which contains the tool call details)
					if description != "" {
						fmt.Println(description)
					}

					// Print Auto-Approved Badge
					fmt.Println(ui.RenderStatusBadge("Auto Approved", true))

					// Simulate "Yes" selection
					userMsg = agent.NewTimestampedUserContent("Yes")

					// Reset state
					waitingForApproval = false
					approvalOptions = nil
					isAutoApproved = false
					continue
				}
				// Use approval options if available
				opts := []string{"Yes", "No"}
				if len(approvalOptions) > 0 {
					opts = approvalOptions
				}

				// Default title if empty
				if title == "" {
					title = "Approval Required"
				}

				selection, err := ui.ReadSelection(opts, title, description)
				if err != nil {
					return err
				}

				// Send selection back to agent
				if selection == "Yes" {
					fmt.Println(ui.RenderStatusBadge("Command approved", true))
				} else {
					fmt.Println(ui.RenderStatusBadge("Command rejected", false))
				}
				userMsg = agent.NewTimestampedUserContent(selection)
				continue

				// Reset state
				waitingForApproval = false
				approvalOptions = nil
			} else {
				// Regular input
				// Default title if empty
				if title == "" {
					title = "Input Required"
				}

				// Check if we have options for selection
				if len(inputOptions) > 0 {
					selection, err := ui.ReadSelection(inputOptions, title, description)
					if err != nil {
						return err
					}
					// Strip trailing colon from title for cleaner display
					displayTitle := strings.TrimSuffix(title, ":")
					fmt.Println(ui.RenderStatusBadge(fmt.Sprintf("%s: %s", displayTitle, selection), true))
					userMsg = agent.NewTimestampedUserContent(selection)
					continue
				} else {
					// Free text input
					input, err := ui.ReadInput(title, description)
					if err != nil {
						return err
					}
					// Strip trailing colon from title for cleaner display
					displayTitle := strings.TrimSuffix(title, ":")
					fmt.Println(ui.RenderStatusBadge(fmt.Sprintf("%s: %s", displayTitle, input), true))
					userMsg = agent.NewTimestampedUserContent(input)
					continue
				}

				// Reset state
				waitingForInput = false
				inputOptions = nil
			}
		} else {
			// Normal flush at end of turn
			if textBuffer.Len() > 0 {
				// Only print if not suppressed AND not a user_message node
				// For user_message nodes, the userMessage block handles display from StateDelta
				// turnHadUserMessageFields persists even after userMessageFields is reset on node change
				if !suppressStreaming && !turnHadUserMessageFields {
					stopSpinner(true, true)
					var rendered string
					if isOutputNode {
						rendered = textBuffer.String()
					} else {
						rendered = ui.SmartRender(textBuffer.String())
					}
					if rendered != "" {
						if !aiPrefixPrinted {
							fmt.Printf("\n%sAgent:%s\n", ColorGreen, ColorReset)
							aiPrefixPrinted = true
						}
						fmt.Print(rendered)
						if !strings.HasSuffix(rendered, "\n") {
							fmt.Println()
						}
					}
				}
				textBuffer.Reset()
			}
		}

		// If we broke out of the loop (e.g. END node), stop spinner
		if currentNodeName == "END" {
			stopSpinner(true, true)
			if cfg.DebugMode {
				slog.Debug("reached END node, exiting main loop")
			}
			break
		}

		// Agent completed without needing input
	}
	return nil
}
