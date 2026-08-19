package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/a2aclient"
	"github.com/SAP/astonish/pkg/apps"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

// loadAppFromStore loads an app from the request-scoped store and converts
// the map[string]any result to a *apps.VisualApp for DataSource resolution.
func loadAppFromStore(r *http.Request, name string) (*apps.VisualApp, error) {
	svc := store.FromRequest(r)
	if svc == nil {
		return nil, fmt.Errorf("store not available")
	}

	slug := apps.Slugify(name)

	var raw any
	var err error

	// Try personal first, then team
	if svc.PersonalApps != nil {
		raw, err = svc.PersonalApps.Load(r.Context(), slug)
	}
	if (raw == nil || err != nil) && svc.Apps != nil {
		raw, err = svc.Apps.Load(r.Context(), slug)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load app %q: %w", name, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("app %q not found", name)
	}

	// Convert map[string]any → *apps.VisualApp via JSON round-trip.
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize app %q: %w", name, err)
	}
	var app apps.VisualApp
	if err := json.Unmarshal(data, &app); err != nil {
		return nil, fmt.Errorf("failed to deserialize app %q: %w", name, err)
	}
	return &app, nil
}

// maxResponseBodySize is the maximum number of bytes the HTTP data proxy will
// read from an external response. This prevents out-of-memory conditions if
// a sandboxed app (accidentally or maliciously) points to a huge resource.
const maxResponseBodySize = 10 << 20 // 10 MB

// hardBlockedIPNets are never accepted as URL IP literals for Apps HTTP
// (loopback / link-local / metadata). Soft-private hosts are enforced by
// sandbox L7 + Network Policy PreSeed, not Studio dial.
var hardBlockedIPNets []*net.IPNet

func init() {
	for _, cidr := range []string{
		"0.0.0.0/8",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"::1/128",
		"fe80::/10",
	} {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("bad CIDR in hardBlockedIPNets: %s: %v", cidr, err))
		}
		hardBlockedIPNets = append(hardBlockedIPNets, ipNet)
	}
}

func normalizeIP(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

// isHardBlockedIP reports whether the IP is loopback, link-local, or metadata.
func isHardBlockedIP(ip net.IP) bool {
	ip = normalizeIP(ip)
	for _, ipNet := range hardBlockedIPNets {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// appDataRequest is the JSON body for POST /api/apps/data.
// sourceId uses convention-based routing:
//   - "mcp:<serverName>/<toolName>"      → invoke MCP tool
//   - "a2a:<agentName>"                  → send message to remote A2A agent
//   - "http:<METHOD>:<url>"              → make HTTP request (no auth)
//   - "http:<METHOD>:<url>@<credential>" → make HTTP request with credential auth
//   - "static:<key>"                     → return static data from app config
type appDataRequest struct {
	SourceID  string         `json:"sourceId"`
	Args      map[string]any `json:"args"`
	RequestID string         `json:"requestId"`
	AppName   string         `json:"appName,omitempty"` // Optional: for saved app data source lookups
}

// appDataResponse is the JSON response for data/action endpoints.
type appDataResponse struct {
	RequestID string `json:"requestId"`
	Data      any    `json:"data,omitempty"`
	Error     string `json:"error,omitempty"`
}

// AppDataHandler handles one-shot data requests from the sandboxed iframe.
// The parent page relays postMessage data_requests here, and the response
// flows back: Go → parent → postMessage → iframe.
//
// POST /api/apps/data
func AppDataHandler(w http.ResponseWriter, r *http.Request) {
	var req appDataRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, appDataResponse{
			Error: "invalid request body",
		})
		return
	}

	if req.SourceID == "" {
		respondJSON(w, http.StatusBadRequest, appDataResponse{
			RequestID: req.RequestID,
			Error:     "sourceId is required",
		})
		return
	}

	slog.Debug("app data request", "sourceId", req.SourceID, "requestId", req.RequestID)

	data, err := resolveDataSource(r, req.SourceID, req.Args, req.AppName)
	if err != nil {
		slog.Debug("app data request failed", "sourceId", req.SourceID, "error", err)
		respondJSON(w, http.StatusOK, appDataResponse{
			RequestID: req.RequestID,
			Error:     err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, appDataResponse{
		RequestID: req.RequestID,
		Data:      data,
	})
}

// AppActionHandler handles action requests (mutations) from the sandboxed iframe.
// Similar to AppDataHandler but semantically for write operations.
//
// POST /api/apps/action
func AppActionHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ActionID  string         `json:"actionId"`
		Payload   map[string]any `json:"payload"`
		RequestID string         `json:"requestId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, appDataResponse{
			Error: "invalid request body",
		})
		return
	}

	if req.ActionID == "" {
		respondJSON(w, http.StatusBadRequest, appDataResponse{
			RequestID: req.RequestID,
			Error:     "actionId is required",
		})
		return
	}

	slog.Debug("app action request", "actionId", req.ActionID, "requestId", req.RequestID)

	// Actions use the same routing convention as data sources
	data, err := resolveDataSource(r, req.ActionID, req.Payload, "")
	if err != nil {
		respondJSON(w, http.StatusOK, appDataResponse{
			RequestID: req.RequestID,
			Error:     err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, appDataResponse{
		RequestID: req.RequestID,
		Data:      data,
	})
}

// AppStreamHandler provides SSE streaming for saved apps with data sources
// that have a refresh interval. The parent page connects to this and forwards
// data_update events to the iframe.
//
// GET /api/apps/{name}/stream?sourceId=X
func AppStreamHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	sourceID := r.URL.Query().Get("sourceId")

	if sourceID == "" {
		respondError(w, http.StatusBadRequest, "sourceId query parameter is required")
		return
	}

	// Load the app to get data source config (for interval)
	app, err := loadAppFromStore(r, name)
	if err != nil {
		respondError(w, http.StatusNotFound, "app not found: "+err.Error())
		return
	}

	// Find the data source config
	var interval time.Duration
	var dsArgs map[string]any
	for _, ds := range app.DataSources {
		if ds.ID == sourceID {
			if ds.Interval != "" {
				interval, _ = time.ParseDuration(ds.Interval)
			}
			dsArgs = ds.Config
			break
		}
	}

	// Default to 30s if no interval configured
	if interval <= 0 {
		interval = 30 * time.Second
	}

	// Minimum interval of 5s to prevent abuse
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}

	// Set up SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	slog.Debug("app stream started", "app", name, "sourceId", sourceID, "interval", interval)

	// Send initial data immediately
	data, err := resolveDataSource(r, sourceID, dsArgs, name)
	sendStreamEvent(w, flusher, sourceID, data, err)

	// Poll at the configured interval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			slog.Debug("app stream ended (client disconnected)", "app", name, "sourceId", sourceID)
			return
		case <-ticker.C:
			data, err := resolveDataSource(r, sourceID, dsArgs, name)
			sendStreamEvent(w, flusher, sourceID, data, err)
		}
	}
}

// sendStreamEvent writes a single SSE event with data_update payload.
func sendStreamEvent(w http.ResponseWriter, flusher http.Flusher, sourceID string, data any, err error) {
	evt := map[string]any{
		"sourceId": sourceID,
	}
	if err != nil {
		evt["error"] = err.Error()
	} else {
		evt["data"] = data
	}
	jsonBytes, _ := json.Marshal(evt)
	fmt.Fprintf(w, "event: data_update\ndata: %s\n\n", jsonBytes)
	flusher.Flush()
}

// resolveDataSource routes a sourceId to the appropriate backend:
//   - "mcp:<server>/<tool>"           → MCP tool invocation
//   - "http:<METHOD>:<url>"           → server-side HTTP request (no auth)
//   - "http:<METHOD>:<url>@<cred>"    → server-side HTTP request with credential auth
//   - "static:<key>"                  → static data from app's DataSource config
//
// If sourceId doesn't match any convention, it tries to find a matching
// DataSource in the saved app (if appName is provided).
func resolveDataSource(r *http.Request, sourceID string, args map[string]any, appName string) (any, error) {
	// Convention-based routing
	if strings.HasPrefix(sourceID, "mcp:") {
		return resolveMCPSource(r, sourceID[4:], args)
	}
	if strings.HasPrefix(sourceID, "a2a:") {
		return resolveA2ASource(r, sourceID[4:], args)
	}
	if strings.HasPrefix(sourceID, "http:") {
		return resolveHTTPSource(r, sourceID[5:], args)
	}
	if strings.HasPrefix(sourceID, "static:") {
		return resolveStaticSource(r, sourceID[7:], appName)
	}

	// Fallback: look up in saved app's DataSources config
	if appName != "" {
		return resolveAppDataSource(r, sourceID, args, appName)
	}

	return nil, fmt.Errorf("unknown source format: %q (expected mcp:<server>/<tool>, a2a:<agent>, http:<METHOD>:<url>, or static:<key>)", sourceID)
}

// resolveMCPSource invokes an MCP tool inside a sandbox container.
// serverTool format: "<serverName>/<toolName>"
//
// Security: stdio MCP servers are ALWAYS executed inside a per-user sandbox
// container (never on the host). SSE/remote servers connect over the network.
func resolveMCPSource(r *http.Request, serverTool string, args map[string]any) (any, error) {
	parts := strings.SplitN(serverTool, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid MCP source format: expected 'server/tool', got %q", serverTool)
	}
	serverName := parts[0]
	toolName := parts[1]

	// Load server config from DB (3-tier platform merge)
	mcpCfg := loadMCPConfigForRequest(r)
	serverCfg, ok := mcpCfg.MCPServers[serverName]
	if !ok {
		return nil, fmt.Errorf("MCP server %q not configured", serverName)
	}

	// SSE/streamable-http servers: direct network connection (no local exec).
	// Stdio servers must stay sandboxed even if a stale URL field is present.
	if !mcpServerUsesSandbox(serverCfg) {
		return invokeMCPToolSSE(r.Context(), serverName, toolName, serverCfg, args)
	}

	// Stdio servers: MUST run inside a sandbox container
	appCfg, _ := config.LoadAppConfig()
	if appCfg == nil || !sandbox.IsSandboxEnabled(&appCfg.Sandbox) {
		return nil, fmt.Errorf("MCP server %q requires sandbox mode (stdio transport cannot run on host)", serverName)
	}

	userID := effectiveUserID(r)
	return invokeMCPToolInContainer(r, userID, serverName, toolName, serverCfg, args)
}

// resolveA2ASource sends a message to a remote A2A agent and returns the response.
// agentName is the configured name of the A2A agent (as stored in platform/org/team settings).
//
// Expected args:
//   - "message" (string, required): The message to send to the agent
//   - "context_id" (string, optional): Conversation context ID for multi-turn interactions
func resolveA2ASource(r *http.Request, agentName string, args map[string]any) (any, error) {
	if agentName == "" {
		return nil, fmt.Errorf("invalid A2A source: agent name is required (format: a2a:<agentName>)")
	}

	message, _ := args["message"].(string)
	if message == "" {
		return nil, fmt.Errorf("A2A source %q: 'message' argument is required", agentName)
	}

	ctx := r.Context()

	// Load A2A config using the same 3-tier cascade as the chat agent.
	var cfg *a2aclient.A2AClientConfig
	var err error

	if svc := store.FromRequest(r); svc != nil && svc.Mode == store.ModePlatform {
		cfg, err = a2aclient.LoadA2AAgentConfig(ctx, true)
	} else {
		cfg, err = a2aclient.LoadA2AAgentConfig(ctx, false)
	}
	if err != nil {
		return nil, fmt.Errorf("A2A source: failed to load config: %w", err)
	}
	if cfg == nil || len(cfg.Agents) == 0 {
		return nil, fmt.Errorf("A2A source: no A2A agents configured")
	}

	agentCfg, ok := cfg.Agents[agentName]
	if !ok {
		available := make([]string, 0, len(cfg.Agents))
		for name := range cfg.Agents {
			available = append(available, name)
		}
		return nil, fmt.Errorf("A2A agent %q not found (available: %v)", agentName, available)
	}

	// Resolve credentials for the A2A client
	var resolver credentials.CredentialResolver
	if credStore := effectiveCredentialStore(r); credStore != nil {
		resolver = credentials.NewStoreAdapter(credStore)
	}

	client := a2aclient.NewClient(agentCfg, resolver)

	// Fetch agent card to detect protocol version
	card, err := client.FetchAgentCard(ctx)
	if err != nil {
		return nil, fmt.Errorf("A2A agent %q: failed to fetch agent card: %w", agentName, err)
	}
	if pv := card.DetectProtocolVersion(); pv != "" {
		client.SetProtocolVersion(pv)
	}

	// Build and send the message
	contextID, _ := args["context_id"].(string)
	params := a2a.SendMessageParams{
		Message: a2a.Message{
			Role:  "user",
			Parts: []a2a.Part{a2a.TextPart{Text: message}},
		},
	}
	if contextID != "" {
		params.Configuration = &a2a.TaskConfig{
			ContextID: contextID,
		}
	}

	slog.Debug("app A2A invoke", "agent", agentName)
	task, err := client.SendMessage(ctx, params)
	if err != nil {
		// Return error in the data payload (not as an HTTP error) so the app can handle it
		return map[string]any{
			"status":   "error",
			"response": err.Error(),
		}, nil
	}

	// Extract response using the shared helper
	response := a2aclient.ExtractResponse(task)

	result := map[string]any{
		"status":   string(task.Status.State),
		"response": response,
		"task_id":  task.ID,
	}
	if len(task.Artifacts) > 0 {
		artifacts := make([]any, 0, len(task.Artifacts))
		for _, artifact := range task.Artifacts {
			artifacts = append(artifacts, map[string]any{
				"name":        artifact.Name,
				"description": artifact.Description,
				"index":       artifact.Index,
			})
		}
		result["artifacts"] = artifacts
	}

	return result, nil
}

// invokeMCPToolInContainer runs an MCP tool inside a per-user sandbox container.
// The container is created on first use and destroyed after idle timeout.
func invokeMCPToolInContainer(r *http.Request, userID, serverName, toolName string, serverCfg config.MCPServerConfig, args map[string]any) (any, error) {
	ctx := r.Context()
	backend, syntheticSessionID, _, cleanup, err := ensureAppSandboxSession(ctx, r, userID)
	if err != nil {
		return nil, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	resolvedCfg, err := mcp.ResolveMCPServerConfig(serverCfg, credentialResolverForRequest(r))
	if err != nil {
		return nil, fmt.Errorf("MCP server %q credential resolution: %w", serverName, err)
	}

	transport, stderrBuf := sandbox.NewBackendMCPTransport(backend, syntheticSessionID, resolvedCfg)

	toolset, err := mcptoolset.New(mcptoolset.Config{
		Transport: transport,
	})
	if err != nil {
		transport.Close()
		msg := fmt.Sprintf("failed to start MCP server %q in sandbox", serverName)
		return nil, newMCPSandboxDiagnosticsError(msg, err, stderrBuf, transport.StdoutDiagnostics, resolvedCfg)
	}

	toolCtx := &appMCPToolContext{Context: ctx}
	tools, err := toolset.Tools(toolCtx)
	if err != nil {
		transport.Close()
		msg := fmt.Sprintf("failed to list tools from MCP server %q", serverName)
		return nil, newMCPSandboxDiagnosticsError(msg, err, stderrBuf, transport.StdoutDiagnostics, serverCfg)
	}

	var targetTool tool.Tool
	for _, t := range tools {
		if declTool, ok := t.(interface {
			Declaration() *genai.FunctionDeclaration
		}); ok {
			decl := declTool.Declaration()
			if decl != nil && decl.Name == toolName {
				targetTool = t
				break
			}
		}
	}
	if targetTool == nil {
		transport.Close()
		available := make([]string, 0, len(tools))
		for _, t := range tools {
			if declTool, ok := t.(interface {
				Declaration() *genai.FunctionDeclaration
			}); ok {
				if decl := declTool.Declaration(); decl != nil {
					available = append(available, decl.Name)
				}
			}
		}
		return nil, fmt.Errorf("tool %q not found on MCP server %q (available: %v)", toolName, serverName, available)
	}

	runner, ok := targetTool.(interface {
		Run(tool.Context, any) (map[string]any, error)
	})
	if !ok {
		transport.Close()
		return nil, fmt.Errorf("tool %q on %q does not implement Run", toolName, serverName)
	}

	slog.Debug("app MCP invoke", "server", serverName, "tool", toolName, "session", syntheticSessionID)
	result, err := runner.Run(toolCtx, args)
	transport.Close()
	if err != nil {
		return nil, fmt.Errorf("MCP tool %q returned error: %w", toolName, err)
	}

	appMCPIdleTracker.touch(syntheticSessionID)
	return result, nil
}

// invokeMCPToolSSE invokes an MCP tool on a remote SSE server (no local exec).
func invokeMCPToolSSE(ctx context.Context, serverName, toolName string, serverCfg config.MCPServerConfig, args map[string]any) (any, error) {
	if serverCfg.URL == "" {
		return nil, fmt.Errorf("MCP server %q has SSE transport but no URL configured", serverName)
	}

	transport := &mcpsdk.SSEClientTransport{
		Endpoint: serverCfg.URL,
	}

	toolset, err := mcptoolset.New(mcptoolset.Config{
		Transport: transport,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SSE MCP server %q at %s: %w", serverName, serverCfg.URL, err)
	}

	// Get tools
	toolCtx := &appMCPToolContext{Context: ctx}
	tools, err := toolset.Tools(toolCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to list tools from SSE server %q: %w", serverName, err)
	}

	// Find and invoke
	var targetTool tool.Tool
	for _, t := range tools {
		if declTool, ok := t.(interface {
			Declaration() *genai.FunctionDeclaration
		}); ok {
			decl := declTool.Declaration()
			if decl != nil && decl.Name == toolName {
				targetTool = t
				break
			}
		}
	}
	if targetTool == nil {
		return nil, fmt.Errorf("tool %q not found on SSE server %q", toolName, serverName)
	}

	runner, ok := targetTool.(interface {
		Run(tool.Context, any) (map[string]any, error)
	})
	if !ok {
		return nil, fmt.Errorf("tool %q on %q does not implement Run", toolName, serverName)
	}

	slog.Debug("app MCP SSE invoke", "server", serverName, "tool", toolName, "url", serverCfg.URL)
	result, err := runner.Run(toolCtx, args)
	if err != nil {
		return nil, fmt.Errorf("SSE MCP tool %q returned error: %w", toolName, err)
	}
	return result, nil
}

// appMCPToolContext is a minimal tool.Context for programmatic MCP tool invocation
// from app data requests. All optional methods return zero values — this is
// sufficient for most MCP tools which only need the embedded context.Context.
type appMCPToolContext struct {
	context.Context
}

func (c *appMCPToolContext) Actions() *session.EventActions       { return &session.EventActions{} }
func (c *appMCPToolContext) Branch() string                       { return "" }
func (c *appMCPToolContext) AgentName() string                    { return "app-data-proxy" }
func (c *appMCPToolContext) AppName() string                      { return "astonish" }
func (c *appMCPToolContext) Artifacts() agent.Artifacts           { return nil }
func (c *appMCPToolContext) FunctionCallID() string               { return "" }
func (c *appMCPToolContext) InvocationID() string                 { return "" }
func (c *appMCPToolContext) SessionID() string                    { return "" }
func (c *appMCPToolContext) UserID() string                       { return "" }
func (c *appMCPToolContext) UserContent() *genai.Content          { return nil }
func (c *appMCPToolContext) ReadonlyState() session.ReadonlyState { return nil }
func (c *appMCPToolContext) State() session.State                 { return nil }
func (c *appMCPToolContext) SearchMemory(_ context.Context, _ string) (*memory.SearchResponse, error) {
	return nil, nil
}
func (c *appMCPToolContext) RequestConfirmation(_ string, _ any) error            { return nil }
func (c *appMCPToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

// credentialSuffixRe matches a @credential-name suffix at the end of a URL.
// Only matches simple names (alphanumeric, dash, underscore) after the last @,
// ensuring it doesn't conflict with @ in URLs (e.g., user:pass@host).
var credentialSuffixRe = regexp.MustCompile(`@([a-zA-Z][a-zA-Z0-9_-]*)$`)

// extractHTTPBodyAndHeaders separates the HTTP body payload from custom headers
// in the args map. LLM-generated apps use two conventions:
//
// Structured: { headers: { "X-Custom": "v" }, body: { messages: [...] } }
//
//	→ body = args["body"], headers from args["headers"]
//
// Flat: { query: "SELECT ..." } or { query: "...", headers: { "X-Key": "v" } }
//
//	→ body = args (minus "headers"), headers from args["headers"]
//
// For non-body methods (GET, DELETE, etc.) bodyData is always nil.
func extractHTTPBodyAndHeaders(method string, args map[string]any) (bodyData any, headers map[string]string) {
	headers = make(map[string]string)

	if args != nil {
		if h, ok := args["headers"].(map[string]any); ok {
			for k, v := range h {
				if s, ok := v.(string); ok {
					headers[k] = s
				}
			}
		}
	}

	if method != "POST" && method != "PUT" && method != "PATCH" {
		return nil, headers
	}
	if args == nil {
		return nil, headers
	}

	if b, ok := args["body"]; ok {
		return b, headers
	}

	if _, hasHeaders := args["headers"]; hasHeaders {
		clean := make(map[string]any, len(args)-1)
		for k, v := range args {
			if k != "headers" {
				clean[k] = v
			}
		}
		return clean, headers
	}
	return args, headers
}

// resolveHTTPSource makes an HTTP request via the App sandbox (curl Exec).
// Studio resolves credentials and builds the request; dial/TLS/Network Policy
// run inside the sandbox (OpenShell L7 + cert_bundles).
// spec format: "<METHOD>:<url>" or "<METHOD>:<url>@<credential-name>"
func resolveHTTPSource(r *http.Request, spec string, args map[string]any) (any, error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid HTTP source format: expected 'METHOD:url', got %q", spec)
	}
	method := strings.ToUpper(parts[0])
	rawURL := parts[1]

	var credentialName string
	if m := credentialSuffixRe.FindStringSubmatchIndex(rawURL); m != nil {
		credentialName = rawURL[m[2]:m[3]]
		rawURL = rawURL[:m[0]]
	}

	if err := validateAppHTTPURL(rawURL); err != nil {
		return nil, err
	}

	var credStore store.CredentialStore
	if credentialName != "" {
		if r != nil {
			credStore = effectiveCredentialStore(r)
		}
		if credStore == nil {
			return nil, fmt.Errorf("credential store is not available — cannot resolve credential %q", credentialName)
		}
	}

	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}

	doRequest := func() (int, []byte, error) {
		bodyData, customHeaders := extractHTTPBodyAndHeaders(method, args)

		headers := map[string]string{
			"Accept": "application/json",
		}
		var bodyBytes []byte
		if bodyData != nil {
			var err error
			bodyBytes, err = json.Marshal(bodyData)
			if err != nil {
				return 0, nil, fmt.Errorf("failed to marshal request body: %w", err)
			}
			headers["Content-Type"] = "application/json"
		}
		for k, v := range customHeaders {
			headers[k] = v
		}
		if credentialName != "" {
			headerKey, headerValue, err := credStore.Resolve(ctx, credentialName)
			if err != nil {
				return 0, nil, fmt.Errorf("failed to resolve credential %q: %w", credentialName, err)
			}
			headers[headerKey] = headerValue
		}
		return appHTTPFetch(ctx, r, method, rawURL, headers, bodyBytes)
	}

	status, respBody, err := doRequest()
	if err != nil {
		return nil, err
	}

	if status == http.StatusUnauthorized && credentialName != "" {
		credStore.InvalidateToken(ctx, credentialName)
		status, respBody, err = doRequest()
		if err != nil {
			return nil, err
		}
	}

	if status >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", status, string(respBody))
	}

	var result any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return string(respBody), nil
	}
	return result, nil
}

// resolveStaticSource returns static data from the app's DataSource config.
func resolveStaticSource(r *http.Request, key string, appName string) (any, error) {
	if appName == "" {
		return nil, fmt.Errorf("static source %q requires an app name", key)
	}

	app, err := loadAppFromStore(r, appName)
	if err != nil {
		return nil, fmt.Errorf("failed to load app %q: %w", appName, err)
	}

	for _, ds := range app.DataSources {
		if ds.ID == key && ds.Type == "static" {
			return ds.Config, nil
		}
	}

	return nil, fmt.Errorf("static data source %q not found in app %q", key, appName)
}

// resolveAppDataSource resolves a sourceId by looking up the app's DataSource config.
// This handles the case where sourceId is a logical name (e.g. "sales_data") and
// the actual routing info is in the saved app's definition.
func resolveAppDataSource(r *http.Request, sourceID string, args map[string]any, appName string) (any, error) {
	app, err := loadAppFromStore(r, appName)
	if err != nil {
		return nil, fmt.Errorf("failed to load app %q: %w", appName, err)
	}

	for _, ds := range app.DataSources {
		if ds.ID == sourceID {
			// Merge config args with request args (request args take precedence)
			mergedArgs := make(map[string]any)
			for k, v := range ds.Config {
				mergedArgs[k] = v
			}
			for k, v := range args {
				mergedArgs[k] = v
			}

			// Route based on the data source type
			switch ds.Type {
			case "mcp_tool":
				server, _ := ds.Config["server"].(string)
				tool, _ := ds.Config["tool"].(string)
				if server == "" || tool == "" {
					return nil, fmt.Errorf("mcp_tool data source %q missing server or tool config", sourceID)
				}
				// Remove server/tool from args — they're routing info, not tool args
				delete(mergedArgs, "server")
				delete(mergedArgs, "tool")
				return resolveMCPSource(r, server+"/"+tool, mergedArgs)

			case "http_api":
				urlStr, _ := ds.Config["url"].(string)
				method, _ := ds.Config["method"].(string)
				if urlStr == "" {
					return nil, fmt.Errorf("http_api data source %q missing url config", sourceID)
				}
				if method == "" {
					method = "GET"
				}
				return resolveHTTPSource(r, method+":"+urlStr, mergedArgs)

			case "a2a_agent":
				agentName, _ := ds.Config["agent"].(string)
				if agentName == "" {
					return nil, fmt.Errorf("a2a_agent data source %q missing agent config", sourceID)
				}
				// Remove agent from args — it's routing info, not a message arg
				delete(mergedArgs, "agent")
				return resolveA2ASource(r, agentName, mergedArgs)

			case "static":
				return ds.Config, nil

			default:
				return nil, fmt.Errorf("unknown data source type %q for %q", ds.Type, sourceID)
			}
		}
	}

	return nil, fmt.Errorf("data source %q not found in app %q", sourceID, appName)
}
