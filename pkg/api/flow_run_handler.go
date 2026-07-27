package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// FlowRunRequest represents the request body for POST /api/agents/{name}/run.
type FlowRunRequest struct {
	Params   map[string]string `json:"params,omitempty"`
	Provider string            `json:"provider,omitempty"`
	Model    string            `json:"model,omitempty"`
}

// FlowRunHandler handles POST /api/agents/{name}/run.
// It runs a flow headlessly with pre-provided parameters and streams output as SSE.
//
// SSE events emitted:
//
//	event: text   data: {"text": "..."}
//	event: node   data: {"node": "node_name"}
//	event: error  data: {"error": "..."}
//	event: done   data: {"result": "ok"}
func FlowRunHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Extract agent name from URL path: /api/agents/{name}/run
	parts := splitPath(r.URL.Path)
	// Expected: ["api", "agents", "{name}", "run"]
	if len(parts) < 4 {
		respondError(w, http.StatusBadRequest, "missing agent name")
		return
	}
	agentName := parts[2] // agents/{name}/run

	// "team:" prefix signals the user explicitly selected the team version.
	forceTeam := strings.HasPrefix(agentName, "team:")
	if forceTeam {
		agentName = strings.TrimPrefix(agentName, "team:")
	}

	// Parse request body
	var req FlowRunRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	SendSSE(w, flusher, "ping", map[string]string{"status": "connected"})

	ctx := r.Context()

	// 1. Load Agent Config
	var cfg *config.AgentConfig
	var cfgErr error

	if svc := store.FromRequest(r); svc != nil && (svc.PersonalFlows != nil || svc.Flows != nil) {
		// Platform mode: load from flow store (personal-first, team fallback).
		// When forceTeam is set, skip personal and resolve from team only.
		var yamlContent string
		var found bool
		if !forceTeam && svc.PersonalFlows != nil {
			if y, err := svc.PersonalFlows.GetFlow(r.Context(), agentName); err == nil && y != "" {
				yamlContent = y
				found = true
			}
		}
		if !found && svc.Flows != nil {
			if y, err := svc.Flows.GetFlow(r.Context(), agentName); err == nil {
				yamlContent = y
				found = true
			}
		}
		if !found {
			SendErrorSSE(w, flusher, fmt.Sprintf("agent not found: %s", agentName))
			return
		}
		cfg, cfgErr = config.LoadAgentFromBytes([]byte(yamlContent))
	} else {
		// Personal mode: load from filesystem.
		agentPath, _, findErr := findAgentPath(agentName)
		if findErr != nil {
			SendErrorSSE(w, flusher, fmt.Sprintf("agent not found: %s", agentName))
			return
		}
		cfg, cfgErr = config.LoadAgent(agentPath)
	}
	if cfgErr != nil {
		SendErrorSSE(w, flusher, fmt.Sprintf("failed to parse agent config: %v", cfgErr))
		return
	}

	// 2. Determine Provider/Model
	appCfg := effectiveAppConfig(r)
	injectProviderSecrets(appCfg)
	ctx = withRuntimeNetworkPolicyContext(ctx, r, appCfg)
	ctx = withRuntimeSandboxContext(ctx, r)

	providerName := req.Provider
	if providerName == "" {
		providerName = appCfg.General.DefaultProvider
	}
	// Normalize provider name
	for id, displayName := range provider.ProviderDisplayNames {
		if displayName == providerName {
			providerName = id
			break
		}
	}
	if providerName == "" {
		providerName = "gemini"
	}

	modelName := req.Model
	if modelName == "" {
		modelName = appCfg.General.DefaultModel
	}

	// 3. Initialize LLM
	llm, err := provider.GetProvider(ctx, providerName, modelName, appCfg)
	if err != nil {
		SendErrorSSE(w, flusher, fmt.Sprintf("failed to initialize provider: %v", err))
		return
	}

	// 4. Initialize Tools
	internalTools, err := tools.GetInternalTools()
	if err != nil {
		SendErrorSSE(w, flusher, fmt.Sprintf("failed to initialize tools: %v", err))
		return
	}

	if credTools, credErr := tools.GetCredentialTools(); credErr == nil {
		internalTools = append(internalTools, credTools...)
	}
	if processTools, procErr := tools.GetProcessTools(); procErr == nil {
		internalTools = append(internalTools, processTools...)
	}
	if browserTools, browserErr := tools.GetBrowserTools(GetBrowserManager()); browserErr == nil {
		internalTools = append(internalTools, browserTools...)
	}

	// Wrap tools with sandbox if enabled
	sessionID := fmt.Sprintf("flow-run-%s-%d", agentName, nowUnixNano())
	sm := GetSessionManager()

	result, sbErr := sandbox.SetupFlowSandbox(appCfg, internalTools)
	if sbErr != nil {
		SendErrorSSE(w, flusher, fmt.Sprintf("sandbox setup failed: %v", sbErr))
		return
	}
	internalTools = result.Tools
	if result.Cleanup != nil {
		defer result.Cleanup()
	}

	// Initialize MCP servers needed for this flow
	// In platform mode, query team, org, and platform MCP stores
	var teamMCPStore, orgMCPStore, platformMCPStore store.MCPServerStore
	if svc := store.FromRequest(r); svc != nil && svc.Mode == store.ModePlatform {
		teamMCPStore = svc.TeamMCPServers
		orgMCPStore = svc.MCPServers
		platformMCPStore = svc.PlatformMCPServers
	}
	requiredServers := getRequiredMCPServers(cfg, teamMCPStore, orgMCPStore, platformMCPStore)

	var mcpToolsets []tool.Toolset
	if len(requiredServers) > 0 {
		_, mcpToolsets = sm.GetOrCreateMCPManager(ctx, sessionID, requiredServers, teamMCPStore, orgMCPStore, platformMCPStore)
	}

	// 5. Create Agent
	astonishAgent := agent.NewAstonishAgentWithToolsets(cfg, llm, internalTools, mcpToolsets)
	astonishAgent.DebugMode = false
	astonishAgent.IsWebMode = true // Disable ANSI colors
	astonishAgent.AutoApprove = true
	astonishAgent.SessionService = session.InMemoryService()

	// Wire credential store for {{CREDENTIAL:...}} placeholder resolution.
	// File-based store (personal mode) + context-injected PG store (platform mode).
	if cs := tools.GetCredentialStore(); cs != nil {
		astonishAgent.Redactor = cs.Redactor()
		astonishAgent.CredentialStore = cs
		astonishAgent.PendingSecrets = credentials.NewPendingVault(cs.Redactor())
		ctx = credentials.WithRedactor(ctx, cs.Redactor())
	}
	// Platform mode: inject PG-backed credential store into context so
	// BeforeToolCallback can resolve credentials from the database.
	if svc := store.FromRequest(r); svc != nil && (svc.PersonalCredentials != nil || svc.Credentials != nil) {
		merged := store.NewMergedCredentialStore(svc.PersonalCredentials, svc.Credentials)
		ctx = store.WithCredentialStore(ctx, merged)
		if tc := store.TenantContextFrom(ctx); tc != nil {
			slog.Debug("flow credential store injected", "agent", agentName, "org", tc.OrgSlug, "team", tc.TeamSlug, "has_user", tc.UserID != "", "has_personal_credentials", svc.PersonalCredentials != nil, "has_team_credentials", svc.Credentials != nil)
		} else {
			slog.Debug("flow credential store injected", "agent", agentName, "has_personal_credentials", svc.PersonalCredentials != nil, "has_team_credentials", svc.Credentials != nil)
		}
		// Wire agent-level resolver as fallback
		astonishAgent.CredentialStore = credentials.NewStoreAdapter(merged)
		if astonishAgent.PendingSecrets == nil {
			astonishAgent.PendingSecrets = credentials.NewPendingVault(nil)
		}
		// Hydrate redactor from PG store so credential values are masked in SSE
		if astonishAgent.Redactor == nil {
			astonishAgent.Redactor = credentials.NewRedactor()
			ctx = credentials.WithRedactor(ctx, astonishAgent.Redactor)
		}
		astonishAgent.Redactor.HydrateFromStore(merged)
	}

	adkAgent, err := adkagent.New(adkagent.Config{
		Name:        "astonish_headless",
		Description: cfg.Description,
		Run:         astonishAgent.Run,
	})
	if err != nil {
		SendErrorSSE(w, flusher, fmt.Sprintf("failed to create agent: %v", err))
		return
	}

	// 6. Create Session
	sessionService := astonishAgent.SessionService
	resp, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName: "astonish",
		UserID:  sessionID,
	})
	if err != nil {
		SendErrorSSE(w, flusher, fmt.Sprintf("failed to create session: %v", err))
		return
	}
	sess := resp.Session

	// Overlap OpenShell/Incus cold start with runner setup + first LLM node.
	// Still destroyed by result.Cleanup at end of this run (isolation preserved).
	sandbox.WarmFlowSession(ctx, internalTools, sess.ID())

	// 7. Create Runner
	rnr, err := runner.New(runner.Config{
		AppName:        "astonish",
		Agent:          adkAgent,
		SessionService: sessionService,
	})
	if err != nil {
		SendErrorSSE(w, flusher, fmt.Sprintf("failed to create runner: %v", err))
		return
	}

	// 8. Run flow headlessly with SSE streaming
	SendSSE(w, flusher, "status", map[string]string{"status": "running"})
	runFlowHeadlessSSE(ctx, w, flusher, rnr, sessionID, sess.ID(), cfg, req.Params)
}

// runFlowHeadlessSSE executes a flow in headless mode, streaming text output as SSE events.
// It auto-approves all tool calls and injects params into input nodes.
func runFlowHeadlessSSE(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	rnr *runner.Runner,
	userID string,
	sessID string,
	cfg *config.AgentConfig,
	params map[string]string,
) {
	var userMsg *genai.Content
	var currentNodeName string

	for {
		isInputNode := false
		waitingForInput := false
		waitingForApproval := false
		suppressStreaming := false
		var userMessageFields []string
		nodeJustChanged := false

		for event, err := range rnr.Run(ctx, userID, sessID, userMsg, adkagent.RunConfig{}) {
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				SendErrorSSE(w, flusher, fmt.Sprintf("agent error: %v", err))
				SendSSE(w, flusher, "done", map[string]string{"result": "error"})
				return
			}

			nodeJustChanged = false

			// Process state delta
			if event.Actions.StateDelta != nil {
				// Node transition
				if node, ok := event.Actions.StateDelta["current_node"].(string); ok {
					if node != currentNodeName {
						nodeJustChanged = true
						currentNodeName = node
						suppressStreaming = false
						userMessageFields = nil
						isInputNode = false

						SendSSE(w, flusher, "node", map[string]string{"node": currentNodeName})

						// Determine node type for streaming control
						for _, n := range cfg.Nodes {
							if n.Name == currentNodeName {
								switch n.Type {
								case "input":
									isInputNode = true
									suppressStreaming = true
								case "output":
									suppressStreaming = false
								default:
									if len(n.UserMessage) > 0 {
										suppressStreaming = true
										userMessageFields = n.UserMessage
									} else if len(n.OutputModel) > 0 {
										suppressStreaming = true
									}
								}
								break
							}
						}
					}
				}

				// Approval — auto-approve
				if awaitingVal, ok := event.Actions.StateDelta["awaiting_approval"]; ok {
					if awaiting, ok := awaitingVal.(bool); ok && awaiting {
						waitingForApproval = true
					}
				}
				if autoApprovedVal, ok := event.Actions.StateDelta["auto_approved"]; ok {
					if auto, ok := autoApprovedVal.(bool); ok && auto {
						waitingForApproval = false
					}
				}

				// Input node waiting
				if inputVal, ok := event.Actions.StateDelta["input_options"]; ok && inputVal != nil {
					waitingForInput = true
				}

				// Capture user_message fields for output
				if len(userMessageFields) > 0 && suppressStreaming && !nodeJustChanged {
					for _, field := range userMessageFields {
						if val, ok := event.Actions.StateDelta[field]; ok {
							var text string
							if s, ok := val.(string); ok {
								text = s
							} else {
								text = fmt.Sprintf("%v", val)
							}
							SendSSE(w, flusher, "text", map[string]string{"text": text + "\n"})
						}
					}
				}
			}

			// Stream LLM text
			if event.LLMResponse.Content != nil {
				for _, part := range event.LLMResponse.Content.Parts {
					if part.Text != "" && !suppressStreaming {
						SendSSE(w, flusher, "text", map[string]string{"text": part.Text})
					}
				}
			}
		}

		// Handle end of turn
		if currentNodeName == "END" {
			break
		}

		// Handle input node — inject parameter
		if waitingForInput || isInputNode {
			if params != nil {
				if val, ok := params[currentNodeName]; ok {
					userMsg = agent.NewTimestampedUserContent(val)
					continue
				}
			}
			// No parameter for this input node
			SendErrorSSE(w, flusher, fmt.Sprintf("input node %q requires a value but no parameter was provided (available params: %s)",
				currentNodeName, strings.Join(mapKeys(params), ", ")))
			SendSSE(w, flusher, "done", map[string]string{"result": "error"})
			return
		}

		// Handle approval — always approve
		if waitingForApproval {
			userMsg = agent.NewTimestampedUserContent("Yes")
			continue
		}

		// Agent completed without needing input — done
		break
	}

	SendSSE(w, flusher, "done", map[string]string{"result": "ok"})
}

// mapKeys returns the keys of a map as a slice.
func mapKeys(m map[string]string) []string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// nowUnixNano returns the current time in unix nanoseconds.
// Extracted to allow easy testing/mocking if needed.
func nowUnixNano() int64 {
	return time.Now().UnixNano()
}
