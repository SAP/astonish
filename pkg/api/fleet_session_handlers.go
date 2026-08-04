package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// fleetSessionRegistry is the global registry for active fleet sessions.
var (
	fleetSessionRegistry     *fleet.SessionRegistry
	fleetSessionRegistryOnce sync.Once

	// fleetFileStore is a standalone FileStore for fleet session persistence.
	// Set during daemon startup so fleet sessions can persist transcripts and
	// session metadata even when the ChatManager hasn't been lazily initialized
	// (which only happens when someone opens the Studio UI).
	fleetFileStore *session.FileStore
)

// excludedFleetTools lists tools that are excluded from fleet sessions because
// they require pool-based sandbox infrastructure (NodeClientPool) that fleet
// sessions don't have. Fleet sessions use a single dedicated LazyNodeClient,
// so pool-switching tools are non-functional and confuse agents into attempting
// environment switches that don't exist in fleet context.
var excludedFleetTools = map[string]bool{
	"use_sandbox_template":   true,
	"list_sandbox_templates": true,
}

// SetFleetSessionStore sets the FileStore used for fleet session persistence.
// Must be called during daemon startup before any fleet sessions are created.
func SetFleetSessionStore(fs *session.FileStore) {
	fleetFileStore = fs
}

// getFleetFileStore returns the FileStore for fleet persistence, trying the
// dedicated fleet store first, then falling back to the ChatManager's store.
func getFleetFileStore() *session.FileStore {
	if fleetFileStore != nil {
		return fleetFileStore
	}
	// Fallback: try the ChatManager (works when Studio UI has been opened)
	cm := GetChatManager()
	if cm != nil {
		return cm.fileStore()
	}
	return nil
}

// getFleetSessionRegistry returns the global fleet session registry (singleton).
func getFleetSessionRegistry() *fleet.SessionRegistry {
	fleetSessionRegistryOnce.Do(func() {
		fleetSessionRegistry = fleet.NewSessionRegistry()
	})
	return fleetSessionRegistry
}

// GetFleetSessionRegistry returns the global fleet session registry (exported for cross-package use).
func GetFleetSessionRegistry() *fleet.SessionRegistry {
	return getFleetSessionRegistry()
}

// FleetStartRequest is the request body for POST /api/studio/fleet/start.
type FleetStartRequest struct {
	FleetKey string `json:"fleet_key"`
	PlanKey  string `json:"plan_key,omitempty"` // alternative to fleet_key: start from a fleet plan
	Message  string `json:"message,omitempty"`  // optional initial message from user
}

// FleetMessageRequest is the request body for POST /api/studio/fleet/sessions/{id}/message.
type FleetMessageRequest struct {
	Message string `json:"message"`
}

// FleetSessionResult contains the result of creating a fleet session.
// Used by both the HTTP handler and channel commands (e.g., Telegram /fleet).
type FleetSessionResult struct {
	Session   *fleet.FleetSession
	FleetKey  string
	FleetName string
	Agents    []map[string]interface{}

	// SetOnMessagePosted allows the caller to compose an additional callback
	// on top of the existing transcript callback. The provided function receives
	// every message posted to the fleet channel (agent, customer, and system).
	SetOnMessagePosted func(fn func(msg fleet.Message))

	// SetOnSessionDone allows the caller to register a callback for session completion.
	// Composes with any existing done callback (e.g., plan activator).
	SetOnSessionDone func(fn func(sessionID string, sessionErr error))
}

// StartFleetSessionFromPlan creates, registers, and starts a fleet session from a plan key.
// The session is started in a background goroutine. The returned result includes
// SetOnMessagePosted and SetOnSessionDone functions that compose with the existing
// transcript callbacks (rather than replacing them), allowing callers (e.g., Telegram)
// to add forwarding logic on top.
// If initialMessage is non-empty, it is posted as the first customer message.
// teamSlug scopes the session to a team (empty for personal mode).
// sessionStore is the PG-backed session store for platform mode (nil for personal mode).
// mcpStores provides platform MCP server stores for sandbox tool injection (nil for personal mode).
// fleetPlanStore is the DB-backed plan store for platform mode (nil uses the file-based registry).
func StartFleetSessionFromPlan(planKey, initialMessage, userID, teamSlug string, sessionStore store.SessionStore, mcpStores *store.MCPServerStores, teamTemplate string, fleetPlanStore store.FleetPlanStore, fleetStores *FleetStores) (*FleetSessionResult, error) {
	// Resolve plan from the provided store (platform mode) or the global registry (personal mode)
	var plan *fleet.FleetPlan
	if fleetPlanStore != nil {
		planAny, ok := fleetPlanStore.GetPlan(context.TODO(), planKey)
		if !ok {
			return nil, fmt.Errorf("fleet plan %q not found", planKey)
		}
		plan, ok = planAny.(*fleet.FleetPlan)
		if !ok {
			return nil, fmt.Errorf("fleet plan %q has unexpected type: %T", planKey, planAny)
		}
	} else {
		if fleetPlanRegistryVar == nil {
			return nil, fmt.Errorf("fleet plan system not initialized")
		}
		var ok bool
		plan, ok = fleetPlanRegistryVar.GetPlan(planKey)
		if !ok {
			return nil, fmt.Errorf("fleet plan %q not found", planKey)
		}
	}

	subAgentMgr := tools.GetSubAgentManager()
	if subAgentMgr == nil {
		return nil, fmt.Errorf("sub-agent system not initialized")
	}

	fleetCfg := &plan.FleetConfig
	channel := fleet.NewChatChannel(planKey)
	fleetSession := fleet.NewFleetSession(planKey, fleetCfg, channel, subAgentMgr)
	fleetSession.Plan = plan
	fleetSession.TeamSlug = teamSlug

	// Pre-initialize the session context so that the session appears as
	// "running" immediately (PostHumanMessage checks fs.ctx != nil).
	// Run() will reuse this context instead of creating its own.
	// Enrich with session store so child sub-agent sessions are persisted
	// to PostgreSQL (making tool executions visible in session traces).
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	if sessionStore != nil {
		sessionCtx = store.WithSessionService(sessionCtx, sessionStore)
		if userID != "" {
			sessionCtx = store.WithUserID(sessionCtx, userID)
		}
	}
	// Inject tenant-scoped stores (FlowStore, DrillReportStore, CredentialStore,
	// SkillStores, etc.) so fleet sub-agents can access team drills, credentials,
	// and other platform-mode resources during execution.
	sessionCtx = fleetStores.InjectIntoContextForPlan(sessionCtx, plan)
	fleetSession.InitContext(sessionCtx, sessionCancel)

	// Resolve per-session workspace directory. Each session gets its own
	// isolated workspace (via git clone --local from the base) under the workspaces dir.
	// The base workspace (~/astonish_projects/<repo-name>/) is where the wizard
	// cloned the repo and generated AGENTS.md.
	baseDir := plan.ResolveWorkspaceDir()
	var workspaceDir string
	if fleetCfg.ProjectContext != nil || plan.ResolveProjectSource() != nil {
		if wsDir, wsErr := config.GetWorkspacesDir(); wsErr == nil {
			workspaceDir = fleet.ResolveSessionWorkspaceDir(
				wsDir, fleetSession.ID, "" /* chat sessions use short session ID */)
			if err := fleet.SetupSessionWorkspace(workspaceDir, plan.ResolveProjectSource(), baseDir); err != nil {
				slog.Warn("could not set up workspace", "component", "fleet", "workspace", workspaceDir, "error", err)
				workspaceDir = "" // fall back to legacy behavior
			}
		}
		// Fall back to the legacy shared workspace if no workspace dir available
		if workspaceDir == "" {
			workspaceDir = baseDir
			if workspaceDir != "" {
				workspaceDir = filepath.Clean(workspaceDir)
				if !filepath.IsAbs(workspaceDir) || strings.Contains(workspaceDir, "..") {
					slog.Warn("workspace dir is not absolute or contains traversal, ignoring", "component", "fleet", "workspace", workspaceDir)
					workspaceDir = ""
				} else if err := os.MkdirAll(workspaceDir, 0755); err != nil {
					slog.Warn("could not create workspace", "component", "fleet", "workspace", workspaceDir, "error", err)
					workspaceDir = ""
				}
			}
		}
	}
	fleetSession.WorkspaceDir = workspaceDir

	var credStore store.CredentialStore
	if fleetStores != nil {
		credStore = fleetStores.Credentials
	}
	if err := wireFleetSandbox(fleetSession, plan, credStore, mcpStores, teamTemplate); err != nil {
		return nil, fmt.Errorf("cannot start fleet session: %w", err)
	}

	// Register in session registry and persist metadata
	registry := getFleetSessionRegistry()
	registry.Register(fleetSession)
	persistFleetSessionMeta(fleetSession, fleetCfg, userID, 0, "", sessionStore)
	wireFleetTranscript(fleetSession, userID, sessionStore)

	// Capture the transcript callback so we can compose additional callbacks on top.
	transcriptCallback := fleetSession.OnMessagePosted
	existingDoneCallback := fleetSession.OnSessionDone

	// Auto-start: set ResumeTarget to the entry point agent so Run()
	// activates it immediately on the first iteration without needing
	// a message in the channel. This avoids posting fake customer messages.
	entryPoint := fleetCfg.GetEntryPoint()
	if entryPoint != "" {
		fleetSession.ResumeTarget = entryPoint
	}

	// Start in background. Post the real initial message (if any) and run
	// the session loop immediately. Project context (AGENTS.md) is loaded
	// instantly from the base workspace where the wizard generated it.
	go func() {
		defer func() {
			registry.Unregister(fleetSession.ID)
			slog.Info("session removed from registry", "component", "fleet", "session_id", fleetSession.ID)
		}()

		// Load project context (AGENTS.md) from the base workspace.
		// The wizard generated it during plan creation; no regeneration needed.
		if baseDir != "" && fleetCfg.ProjectContext != nil {
			pc := fleet.LoadProjectContextFile(baseDir, fleetCfg.ProjectContext)
			if pc != "" {
				fleetSession.ProjectContext = pc
				slog.Info("project context loaded from base", "component", "fleet", "session_id", fleetSession.ID, "bytes", len(pc))
			}
		}

		// Post the real initial customer message if the user provided one.
		// This goes into the channel so the entry point agent sees it in
		// its thread context when activated via ResumeTarget.
		// MemoryKeys are stamped here so the message is scoped to the entry
		// point agent's memory (not globally visible to all agents).
		// The Run() loop handles transcript persistence via notifyMessagePosted.
		if initialMessage != "" {
			msg := fleet.Message{
				Sender:     "customer",
				Text:       initialMessage,
				MemoryKeys: []string{entryPoint},
			}
			if err := channel.PostMessage(context.Background(), msg); err != nil {
				slog.Error("failed to post initial message", "component", "fleet", "error", err)
			} else {
				fleetSession.DeliverToMailbox(sessionCtx, msg, []string{entryPoint})
			}
		}

		if err := fleetSession.Run(sessionCtx); err != nil {
			slog.Error("session error", "component", "fleet", "session_id", fleetSession.ID, "error", err)
		}
	}()

	return &FleetSessionResult{
		Session:   fleetSession,
		FleetKey:  planKey,
		FleetName: plan.Name,
		Agents:    buildAgentList(fleetCfg),
		// SetOnMessagePosted composes the caller's callback with the existing
		// transcript callback. Both are called for every message.
		SetOnMessagePosted: func(fn func(msg fleet.Message)) {
			fleetSession.OnMessagePosted = func(msg fleet.Message) {
				if transcriptCallback != nil {
					transcriptCallback(msg)
				}
				if fn != nil {
					fn(msg)
				}
			}
		},
		// SetOnSessionDone composes the caller's callback with any existing done callback.
		SetOnSessionDone: func(fn func(sessionID string, sessionErr error)) {
			fleetSession.OnSessionDone = func(sessionID string, sessionErr error) {
				if existingDoneCallback != nil {
					existingDoneCallback(sessionID, sessionErr)
				}
				if fn != nil {
					fn(sessionID, sessionErr)
				}
			}
		},
	}, nil
}

// FleetStartHandler handles POST /api/studio/fleet/start.
// It creates a new fleet session, starts the message loop in a background goroutine,
// and returns the session info as JSON. The client should then connect to the
// SSE stream endpoint to receive real-time events.
func FleetStartHandler(w http.ResponseWriter, r *http.Request) {
	var req FleetStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.FleetKey == "" && req.PlanKey == "" {
		respondError(w, http.StatusBadRequest, "fleet_key or plan_key is required")
		return
	}

	// Resolve session store for platform mode.
	// Fleet sessions are team-scoped (shared across team members), so we
	// use the team Sessions store rather than PersonalSessions.
	var handlerSessionStore store.SessionStore
	var handlerMCPStores *store.MCPServerStores
	if svc := store.FromRequest(r); svc != nil {
		if svc.Sessions != nil {
			handlerSessionStore = svc.Sessions
		}
		if svc.Mode == store.ModePlatform && (svc.PlatformMCPServers != nil || svc.MCPServers != nil || svc.TeamMCPServers != nil) {
			handlerMCPStores = &store.MCPServerStores{
				Platform: svc.PlatformMCPServers,
				Org:      svc.MCPServers,
				Team:     svc.TeamMCPServers,
			}
		}
	}

	// If starting from a plan, use the shared helper
	if req.PlanKey != "" {
		// Resolve team template from team settings (platform mode)
		handlerTeamTemplate := ""
		if svc := store.FromRequest(r); svc != nil && svc.Settings != nil {
			if settings, err := svc.Settings.Get(r.Context()); err == nil && settings != nil {
				handlerTeamTemplate = settings.TemplateName
			}
		}
		// Resolve fleet plan store for platform mode
		var handlerFleetPlanStore store.FleetPlanStore
		if svc := store.FromRequest(r); svc != nil && svc.FleetPlans != nil {
			handlerFleetPlanStore = svc.FleetPlans
		}
		// Build tenant-scoped stores for the fleet session context
		var handlerFleetStores *FleetStores
		if svc := store.FromRequest(r); svc != nil {
			handlerFleetStores = FleetStoresFromServices(svc)
		}
		result, err := StartFleetSessionFromPlan(req.PlanKey, req.Message, effectiveUserID(r), effectiveTeamSlug(r), handlerSessionStore, handlerMCPStores, handlerTeamTemplate, handlerFleetPlanStore, handlerFleetStores)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"session_id": result.Session.ID,
			"fleet_key":  result.FleetKey,
			"fleet_name": result.FleetName,
			"agents":     result.Agents,
		})
		return
	}

	// Start from a regular fleet template
	var cfg *fleet.FleetConfig
	if svc := store.FromRequest(r); svc != nil && svc.FleetTemplates != nil {
		cfgAny, found := svc.FleetTemplates.GetFleet(r.Context(), req.FleetKey)
		if !found {
			respondError(w, http.StatusNotFound, fmt.Sprintf("Fleet %q not found", req.FleetKey))
			return
		}
		var cfgOk bool
		cfg, cfgOk = cfgAny.(*fleet.FleetConfig)
		if !cfgOk {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("Fleet %q has unexpected type", req.FleetKey))
			return
		}
	} else {
		fleetReg := GetFleetRegistry()
		if fleetReg == nil {
			respondError(w, http.StatusServiceUnavailable, "Fleet system not initialized")
			return
		}
		var found bool
		cfg, found = fleetReg.GetFleet(req.FleetKey)
		if !found {
			respondError(w, http.StatusNotFound, fmt.Sprintf("Fleet %q not found", req.FleetKey))
			return
		}
	}

	subAgentMgr := tools.GetSubAgentManager()
	if subAgentMgr == nil {
		respondError(w, http.StatusServiceUnavailable, "Sub-agent system not initialized (sub-agents must be enabled)")
		return
	}

	channel := fleet.NewChatChannel(req.FleetKey)
	fleetSession := fleet.NewFleetSession(req.FleetKey, cfg, channel, subAgentMgr)
	fleetSession.TeamSlug = effectiveTeamSlug(r)

	// Build tenant-scoped stores for the fleet session context (must capture
	// before the goroutine starts since the request context won't be available).
	var directFleetStores *FleetStores
	if svc := store.FromRequest(r); svc != nil {
		directFleetStores = FleetStoresFromServices(svc)
	}

	registry := getFleetSessionRegistry()
	registry.Register(fleetSession)
	persistFleetSessionMeta(fleetSession, cfg, effectiveUserID(r), 0, "", handlerSessionStore)
	wireFleetTranscript(fleetSession, effectiveUserID(r), handlerSessionStore)

	go func() {
		defer func() {
			registry.Unregister(fleetSession.ID)
			slog.Info("session removed from registry", "component", "fleet", "session_id", fleetSession.ID)
		}()
		// Enrich context with session store so child sub-agent sessions
		// are persisted to PostgreSQL (tool executions visible in traces).
		runCtx := context.Background()
		if handlerSessionStore != nil {
			runCtx = store.WithSessionService(runCtx, handlerSessionStore)
			uid := effectiveUserID(r)
			if uid != "" {
				runCtx = store.WithUserID(runCtx, uid)
			}
		}
		// Inject tenant-scoped stores so fleet sub-agents can access team
		// drills, credentials, and other platform-mode resources.
		runCtx = directFleetStores.InjectIntoContextForPlan(runCtx, fleetSession.Plan)
		if err := fleetSession.Run(runCtx); err != nil {
			slog.Error("session error", "component", "fleet", "session_id", fleetSession.ID, "error", err)
		}
	}()

	if req.Message != "" {
		entryPoint := cfg.GetEntryPoint()
		initialMsg := fleet.Message{
			Sender:     "customer",
			Text:       req.Message,
			MemoryKeys: []string{entryPoint},
		}
		if err := channel.PostMessage(context.Background(), initialMsg); err != nil {
			slog.Error("failed to post initial message", "component", "fleet", "error", err)
		} else {
			mailCtx := directFleetStores.InjectIntoContextForPlan(context.Background(), fleetSession.Plan)
			fleetSession.DeliverToMailbox(mailCtx, initialMsg, []string{entryPoint})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"session_id": fleetSession.ID,
		"fleet_key":  req.FleetKey,
		"fleet_name": cfg.Name,
		"agents":     buildAgentList(cfg),
	})
}

// persistFleetSessionMeta adds the fleet session to the persistent session index
// so it appears in the sidebar alongside regular chat sessions.
// If sessionStore is non-nil (platform mode), it writes to the PostgreSQL store.
// Otherwise, it writes to the file-based session store.
func persistFleetSessionMeta(fs *fleet.FleetSession, fleetCfg *fleet.FleetConfig, userID string, issueNumber int, repo string, sessionStore store.SessionStore) {
	// When sandbox is enabled, WorkspaceDir is a container-internal path
	// (e.g., "/root/astonish"). Do NOT persist it — CleanupSessionWorkspace
	// would run os.RemoveAll on that path ON THE HOST, potentially destroying
	// the host project directory. Sandbox sessions have no host-side workspace;
	// container cleanup handles everything.
	workspaceDir := fs.WorkspaceDir
	if fs.SandboxTools != nil {
		workspaceDir = ""
	}

	now := time.Now()

	// Platform mode: write to the PG-backed SessionStore.
	if sessionStore != nil {
		meta := store.SessionMeta{
			ID:           fs.ID,
			AppName:      studioChatAppName,
			UserID:       userID,
			CreatedAt:    now,
			UpdatedAt:    now,
			Title:        fmt.Sprintf("Fleet: %s", fleetCfg.Name),
			FleetKey:     fs.FleetKey,
			FleetName:    fleetCfg.Name,
			IssueNumber:  issueNumber,
			Repo:         repo,
			WorkspaceDir: workspaceDir,
		}
		if err := sessionStore.AddSessionMeta(context.TODO(), meta); err != nil {
			slog.Warn("could not persist fleet session meta to store", "component", "fleet", "error", err)
		}
		return
	}

	// Personal mode: write to the file-based session store.
	fileStore := getFleetFileStore()
	if fileStore == nil {
		slog.Warn("no file store available, cannot persist fleet session meta", "component", "fleet", "session_id", fs.ID)
		return
	}

	meta := session.SessionMeta{
		ID:           fs.ID,
		AppName:      studioChatAppName,
		UserID:       userID,
		CreatedAt:    now,
		UpdatedAt:    now,
		Title:        fmt.Sprintf("Fleet: %s", fleetCfg.Name),
		FleetKey:     fs.FleetKey,
		FleetName:    fleetCfg.Name,
		IssueNumber:  issueNumber,
		Repo:         repo,
		WorkspaceDir: workspaceDir,
	}

	if err := fileStore.AddSessionMeta(meta); err != nil {
		slog.Warn("could not persist fleet session meta", "component", "fleet", "error", err)
	}
}

// updateFleetSessionMeta updates the message count and timestamp for a fleet session in the index.
// If sessionStore is non-nil (platform mode), it updates the PG store.
func updateFleetSessionMeta(sessionID string, messageCount int, sessionStore store.SessionStore) {
	if sessionStore != nil {
		if err := sessionStore.UpdateSessionMeta(context.TODO(), sessionID, func(meta *store.SessionMeta) {
			meta.MessageCount = messageCount
			meta.UpdatedAt = time.Now()
		}); err != nil {
			slog.Warn("failed to update fleet session metadata in store", "session_id", sessionID, "error", err)
		}
		return
	}

	fileStore := getFleetFileStore()
	if fileStore == nil {
		return
	}

	if err := fileStore.UpdateSessionMeta(sessionID, func(meta *session.SessionMeta) {
		meta.MessageCount = messageCount
		meta.UpdatedAt = time.Now()
	}); err != nil {
		slog.Warn("failed to update fleet session metadata", "session_id", sessionID, "error", err)
	}
}

// wireFleetTranscript creates a JSONL transcript file for a fleet session
// and wires up the OnMessagePosted callback to persist messages to it.
// This makes fleet sessions visible via `astonish sessions show <id>`.
// If sessionStore is non-nil (platform mode), events are persisted to the PG
// store instead of a JSONL file on disk.
func wireFleetTranscript(fs *fleet.FleetSession, userID string, sessionStore store.SessionStore) {
	if sessionStore != nil {
		wireFleetTranscriptPG(fs, sessionStore)
		return
	}

	fileStore := getFleetFileStore()
	if fileStore == nil {
		slog.Warn("no file store available, cannot create transcript", "component", "fleet", "session_id", fs.ID)
		return
	}

	// Create the transcript file in the same location as regular session transcripts
	transcriptPath := filepath.Join(fileStore.BaseDir(), studioChatAppName, userID, fs.ID+".jsonl")
	transcript := session.NewTranscript(transcriptPath)

	if err := transcript.WriteHeader(fs.ID); err != nil {
		slog.Warn("could not create fleet transcript", "component", "fleet", "error", err)
		return
	}

	// Wire the callback so every message posted to the channel gets persisted
	var invocationCounter int
	fs.OnMessagePosted = func(msg fleet.Message) {
		invocationCounter++
		event := fleetMessageToEvent(msg, invocationCounter)
		if err := transcript.AppendEvent(event); err != nil {
			slog.Warn("could not persist fleet message", "component", "fleet", "error", err)
		}
	}
}

// wireFleetTranscriptPG wires the OnMessagePosted callback to persist fleet
// messages as session_events rows in PostgreSQL.
func wireFleetTranscriptPG(fs *fleet.FleetSession, sessionStore store.SessionStore) {
	var invocationCounter int
	fs.OnMessagePosted = func(msg fleet.Message) {
		invocationCounter++
		event := fleetMessageToEvent(msg, invocationCounter)

		// Persist the event via the store's AppendEvent method. This requires
		// a session object that the store recognizes. For PG stores that need
		// a *pgSession, we use the raw event insertion path instead.
		if err := sessionStore.AppendFleetEvent(context.TODO(), fs.ID, event); err != nil {
			slog.Warn("could not persist fleet event to store", "component", "fleet", "session_id", fs.ID, "error", err)
		}

		// Also update the message count in the session metadata.
		updateFleetSessionMeta(fs.ID, invocationCounter, sessionStore)
	}
}

// fleetMessageToEvent converts a fleet message to an ADK session event
// so it can be stored in the JSONL transcript and read by `sessions show`.
//
// MemoryKeys are encoded into the InvocationID field as a suffix:
// "fleet-turn-N|mem:architect,po". This piggybacks on an existing free-form
// string field to avoid schema changes to the ADK Event struct.
// eventsToFleetMessages in fleet_recover.go parses it back out.
//
// For backward compatibility, old ThreadKey values are also accepted and
// converted to MemoryKeys on read (see eventsToFleetMessages).
func fleetMessageToEvent(msg fleet.Message, invocationNum int) *adksession.Event {
	// Map fleet sender to ADK role
	role := genai.RoleModel
	author := msg.Sender
	if msg.Sender == "customer" {
		role = genai.RoleUser
		author = "user"
	}

	content := &genai.Content{
		Role: role,
		Parts: []*genai.Part{
			genai.NewPartFromText(msg.Text),
		},
	}

	invocationID := fmt.Sprintf("fleet-turn-%d", invocationNum)
	if len(msg.MemoryKeys) > 0 {
		invocationID += "|mem:" + strings.Join(msg.MemoryKeys, ",")
	} else if msg.ThreadKey != "" {
		// Backward compat: old code may still set ThreadKey
		invocationID += "|thread:" + msg.ThreadKey
	}

	return &adksession.Event{
		LLMResponse: adkmodel.LLMResponse{
			Content: content,
		},
		ID:           msg.ID,
		Timestamp:    msg.Timestamp,
		InvocationID: invocationID,
		Author:       author,
	}
}

// FleetMessageHandler handles POST /api/studio/fleet/sessions/{id}/message.
// It posts a human message to an active fleet session.
func FleetMessageHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]

	var req FleetMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Message == "" {
		respondError(w, http.StatusBadRequest, "message is required")
		return
	}

	registry := getFleetSessionRegistry()
	if err := registry.PostHumanMessage(sessionID, req.Message); err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
	})
}

// FleetSessionStatusHandler handles GET /api/studio/fleet/sessions/{id}.
// Returns the current state of a fleet session.
func FleetSessionStatusHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]

	registry := getFleetSessionRegistry()
	fs := registry.Get(sessionID)
	if fs == nil {
		respondError(w, http.StatusNotFound, "Fleet session not found")
		return
	}

	state, activeAgent := fs.GetState()

	// Get thread history
	thread, err := fs.Channel.GetThread(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to get thread: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"session_id":   fs.ID,
		"fleet_key":    fs.FleetKey,
		"fleet_name":   fs.FleetConfig.Name,
		"state":        string(state),
		"active_agent": activeAgent,
		"messages":     thread,
		"agents":       buildAgentList(fs.FleetConfig),
	})
}

// FleetSessionsListHandler handles GET /api/studio/fleet/sessions.
// Returns active fleet sessions from the in-memory registry.
// Used by StudioChat and SessionTrace to check if a session is currently running.
func FleetSessionsListHandler(w http.ResponseWriter, r *http.Request) {
	registry := getFleetSessionRegistry()
	teamSlug := effectiveTeamSlug(r)
	sessions := registry.ListForTeam(teamSlug)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"sessions": sessions,
	})
}

// FleetSessionsHistoryHandler handles GET /api/studio/fleet/sessions/history.
// Returns persisted fleet sessions from the team Sessions store (platform mode)
// or the file store (personal mode). This is the endpoint that FleetView uses
// to display all fleet sessions (both active and completed).
func FleetSessionsHistoryHandler(w http.ResponseWriter, r *http.Request) {
	userID := effectiveUserID(r)

	// Platform mode: read persisted fleet sessions from the team Sessions store.
	if svc := store.FromRequest(r); svc != nil && svc.Sessions != nil {
		// Fleet sessions are team-level resources — list ALL sessions regardless
		// of creator (pass empty userID to skip the user filter). The FleetKey
		// filter below ensures only fleet sessions are returned.
		metas, err := svc.Sessions.ListSessionMetas(r.Context(), studioChatAppName, "")
		if err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("listing fleet sessions: %v", err))
			return
		}

		// Filter to fleet sessions only (those with a fleet key).
		sessions := make([]StudioSessionResponse, 0, len(metas))
		for _, m := range metas {
			if m.FleetKey == "" {
				continue
			}
			sessions = append(sessions, storeMetaToResponse(m))
		}

		// Sort by updated_at descending (most recent first).
		sort.Slice(sessions, func(i, j int) bool {
			return sessions[i].UpdatedAt > sessions[j].UpdatedAt
		})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
		return
	}

	// Personal mode: read from file store.
	cm := GetChatManager()
	if err := cm.ensureReady(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("initializing chat manager: %v", err))
		return
	}
	fs := cm.fileStore()
	if fs == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]StudioSessionResponse{})
		return
	}

	metas, err := fs.ListSessionMetas(studioChatAppName, userID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("listing fleet sessions: %v", err))
		return
	}

	sessions := make([]StudioSessionResponse, 0, len(metas))
	for _, m := range metas {
		if m.FleetKey == "" {
			continue
		}
		sessions = append(sessions, StudioSessionResponse{
			ID:           m.ID,
			Title:        m.Title,
			CreatedAt:    m.CreatedAt.Format(time.RFC3339),
			UpdatedAt:    m.UpdatedAt.Format(time.RFC3339),
			MessageCount: m.MessageCount,
			FleetKey:     m.FleetKey,
			FleetName:    m.FleetName,
			IssueNumber:  m.IssueNumber,
			Repo:         m.Repo,
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

// FleetSessionStopHandler handles POST /api/studio/fleet/sessions/{id}/stop.
func FleetSessionStopHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]

	registry := getFleetSessionRegistry()
	fs := registry.Get(sessionID)
	if fs == nil {
		respondError(w, http.StatusNotFound, "Fleet session not found")
		return
	}

	fs.Stop()
	fs.Cleanup() // destroy sandbox container + clean session registry
	registry.Unregister(sessionID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
	})
}

// FleetSessionStreamHandler handles GET /api/studio/fleet/sessions/{id}/stream.
// It opens an SSE stream for a fleet session, sending existing thread history
// followed by real-time messages. Supports connect/reconnect independently of
// the fleet session lifecycle.
func FleetSessionStreamHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]

	registry := getFleetSessionRegistry()
	fs := registry.Get(sessionID)
	if fs == nil {
		respondError(w, http.StatusNotFound, "Fleet session not found")
		return
	}

	// Resolve the session store for metadata updates (platform mode).
	// Fleet sessions live in the team Sessions store.
	var streamSessionStore store.SessionStore
	if svc := store.FromRequest(r); svc != nil && svc.Sessions != nil {
		streamSessionStore = svc.Sessions
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "Streaming unsupported")
		return
	}

	ctx := r.Context()

	var sseMu sync.Mutex
	safeSendSSE := func(eventType string, data interface{}) {
		sseMu.Lock()
		defer sseMu.Unlock()
		SendSSE(w, flusher, eventType, data)
	}

	// Send session info
	safeSendSSE("fleet_session", map[string]interface{}{
		"session_id": fs.ID,
		"fleet_key":  fs.FleetKey,
		"fleet_name": fs.FleetConfig.Name,
		"agents":     buildAgentList(fs.FleetConfig),
	})

	// Send current state
	state, activeAgent := fs.GetState()
	safeSendSSE("fleet_state", map[string]interface{}{
		"state":        string(state),
		"active_agent": activeAgent,
	})

	// Subscribe to new messages BEFORE reading history to avoid missing
	// messages posted between reading history and subscribing.
	subscriberID := uuid.New().String()
	subscribable, canSubscribe := fs.Channel.(fleet.Subscribable)
	var msgCh <-chan fleet.Message
	if canSubscribe {
		msgCh = subscribable.Subscribe(subscriberID)
		defer subscribable.Unsubscribe(subscriberID)
	}

	// Send existing thread history
	thread, err := fs.Channel.GetThread(ctx)
	if err == nil {
		for _, msg := range thread {
			safeSendSSE("fleet_message", map[string]interface{}{
				"id":          msg.ID,
				"sender":      msg.Sender,
				"text":        msg.Text,
				"memory_keys": msg.ResolveMemoryKeys(),
				"artifacts":   msg.Artifacts,
				"mentions":    msg.Mentions,
				"timestamp":   msg.Timestamp,
				"metadata":    msg.Metadata,
			})
		}
	}

	// Track seen message IDs to avoid duplicating messages that arrive
	// between the history read and the subscriber channel.
	seen := make(map[string]bool, len(thread))
	for _, msg := range thread {
		seen[msg.ID] = true
	}

	// Wire up state change callback for this viewer
	prevStateCallback := fs.OnStateChange
	prevAgentStartedCallback := fs.OnAgentStarted
	prevAgentFinishedCallback := fs.OnAgentFinished
	prevMailboxDeliveredCallback := fs.OnMailboxDelivered
	fs.OnStateChange = func(state fleet.SessionState, activeAgent string) {
		safeSendSSE("fleet_state", map[string]interface{}{
			"state":        string(state),
			"active_agent": activeAgent,
		})
		if prevStateCallback != nil {
			prevStateCallback(state, activeAgent)
		}
	}
	fs.OnAgentStarted = func(agentKey string, laneIndex int) {
		safeSendSSE("fleet_agent_started", map[string]interface{}{
			"session_id": sessionID,
			"agent":      agentKey,
			"lane_index": laneIndex,
		})
		if prevAgentStartedCallback != nil {
			prevAgentStartedCallback(agentKey, laneIndex)
		}
	}
	fs.OnAgentFinished = func(agentKey string, laneIndex int, duration time.Duration) {
		safeSendSSE("fleet_agent_finished", map[string]interface{}{
			"session_id":   sessionID,
			"agent":        agentKey,
			"lane_index":   laneIndex,
			"duration_ms":  duration.Milliseconds(),
			"duration_str": duration.String(),
		})
		if prevAgentFinishedCallback != nil {
			prevAgentFinishedCallback(agentKey, laneIndex, duration)
		}
	}
	fs.OnMailboxDelivered = func(recipient string, sender string) {
		safeSendSSE("fleet_mailbox_delivered", map[string]interface{}{
			"session_id": sessionID,
			"recipient":  recipient,
			"sender":     sender,
		})
		if prevMailboxDeliveredCallback != nil {
			prevMailboxDeliveredCallback(recipient, sender)
		}
	}
	prevTaskEventCallback := fs.OnTaskEvent
	fs.OnTaskEvent = func(event string, task store.FleetTask) {
		safeSendSSE(event, map[string]interface{}{
			"session_id":            sessionID,
			"task_id":               task.ID.String(),
			"title":                 task.Title,
			"status":                task.Status,
			"claimed_by":            task.ClaimedBy,
			"required_capabilities": task.RequiredCapabilities,
		})
		if prevTaskEventCallback != nil {
			prevTaskEventCallback(event, task)
		}
	}
	defer func() {
		fs.OnStateChange = prevStateCallback
		fs.OnAgentStarted = prevAgentStartedCallback
		fs.OnAgentFinished = prevAgentFinishedCallback
		fs.OnMailboxDelivered = prevMailboxDeliveredCallback
		fs.OnTaskEvent = prevTaskEventCallback
	}()

	// Also update the persistent meta with message count on each new message
	// (debounced via the subscriber loop below)

	// Stream new messages until disconnect or session done
	if msgCh == nil {
		// Not a ChatChannel, just wait for disconnect
		<-ctx.Done()
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-msgCh:
			if !ok {
				// Channel closed (fleet session ended)
				donePayload := map[string]interface{}{"done": true}
				if lastErr := fs.LastError(); lastErr != nil {
					donePayload["error"] = lastErr.Error()
				}
				safeSendSSE("fleet_done", donePayload)
				return
			}
			if seen[msg.ID] {
				continue // already sent as part of history
			}
			seen[msg.ID] = true
			safeSendSSE("fleet_message", map[string]interface{}{
				"id":          msg.ID,
				"sender":      msg.Sender,
				"text":        msg.Text,
				"memory_keys": msg.ResolveMemoryKeys(),
				"artifacts":   msg.Artifacts,
				"mentions":    msg.Mentions,
				"timestamp":   msg.Timestamp,
				"metadata":    msg.Metadata,
			})

			// Update persistent meta (message count)
			if thread, thErr := fs.Channel.GetThread(ctx); thErr == nil {
				updateFleetSessionMeta(sessionID, len(thread), streamSessionStore)
			}
		}
	}
}

// FleetSessionThreadsHandler handles GET /api/studio/fleet/sessions/{id}/threads.
// Returns a list of unique pairwise threads in a fleet session with summary info.
// Works for both active sessions (from in-memory channel) and completed sessions
// (from JSONL transcript).
func FleetSessionThreadsHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]

	var sessStore store.SessionStore
	if svc := store.FromRequest(r); svc != nil && svc.Sessions != nil {
		sessStore = svc.Sessions
	}

	messages, err := getFleetMessages(sessionID, effectiveUserID(r), r.Context(), sessStore)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	// Group messages by agent memory ownership and compute summaries.
	// Each agent gets a summary showing how many messages are in their memory.
	type agentMemorySummary struct {
		AgentKey     string   `json:"agent_key"`
		Participants []string `json:"participants"`
		MessageCount int      `json:"message_count"`
		FirstTS      string   `json:"first_timestamp,omitempty"`
		LastTS       string   `json:"last_timestamp,omitempty"`
	}

	agentMap := make(map[string]*agentMemorySummary)
	participantMap := make(map[string]map[string]bool) // agentKey -> set of senders in their memory

	for _, msg := range messages {
		keys := msg.ResolveMemoryKeys()
		if len(keys) == 0 {
			// System/global message — attribute to _system
			keys = []string{"_system"}
		}

		for _, key := range keys {
			ts, ok := agentMap[key]
			if !ok {
				ts = &agentMemorySummary{AgentKey: key}
				agentMap[key] = ts
				participantMap[key] = make(map[string]bool)
			}

			ts.MessageCount++
			participantMap[key][msg.Sender] = true

			tsStr := msg.Timestamp.Format("2006-01-02T15:04:05Z07:00")
			if ts.FirstTS == "" || tsStr < ts.FirstTS {
				ts.FirstTS = tsStr
			}
			if tsStr > ts.LastTS {
				ts.LastTS = tsStr
			}
		}
	}

	// Build participants lists
	for key, ts := range agentMap {
		pmap := participantMap[key]
		parts := make([]string, 0, len(pmap))
		for p := range pmap {
			parts = append(parts, p)
		}
		ts.Participants = parts
	}

	// Collect into a sorted list (system first, then alphabetical)
	threads := make([]agentMemorySummary, 0, len(agentMap))
	if sys, ok := agentMap["_system"]; ok {
		threads = append(threads, *sys)
	}
	for key, ts := range agentMap {
		if key != "_system" {
			threads = append(threads, *ts)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"threads": threads,
	})
}

// FleetSessionTasksHandler handles GET /api/studio/fleet/sessions/{id}/tasks.
func FleetSessionTasksHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]
	if sessionID == "" {
		respondError(w, http.StatusBadRequest, "session id is required")
		return
	}
	svc := store.FromRequest(r)
	if svc == nil || svc.FleetTaskBoard == nil {
		respondError(w, http.StatusServiceUnavailable, "Fleet task board store not available")
		return
	}
	statuses := r.URL.Query()["status"]
	tasks, err := svc.FleetTaskBoard.List(r.Context(), sessionID, statuses...)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

// FleetSessionMailboxHandler handles GET /api/studio/fleet/sessions/{id}/mailbox/{recipient}.
func FleetSessionMailboxHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionID := vars["id"]
	recipient := vars["recipient"]
	if sessionID == "" || recipient == "" {
		respondError(w, http.StatusBadRequest, "session id and recipient are required")
		return
	}
	svc := store.FromRequest(r)
	if svc == nil || svc.FleetMailbox == nil {
		respondError(w, http.StatusServiceUnavailable, "Fleet mailbox store not available")
		return
	}
	all, err := svc.FleetMailbox.ListForSession(r.Context(), sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	messages := make([]store.FleetMailboxMessage, 0, len(all))
	for _, msg := range all {
		if msg.Recipient == recipient {
			messages = append(messages, msg)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messages)
}

// FleetSessionMessagesHandler handles GET /api/studio/fleet/sessions/{id}/messages.
// Returns fleet-level conversation messages, optionally filtered by agent memory.
// Works for both active sessions (from in-memory channel) and completed sessions
// (from JSONL transcript).
//
// Query params:
//
//	agent=dev  - filter to messages in the given agent's memory (includes system messages)
func FleetSessionMessagesHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]

	var sessStore store.SessionStore
	if svc := store.FromRequest(r); svc != nil && svc.Sessions != nil {
		sessStore = svc.Sessions
	}

	messages, err := getFleetMessages(sessionID, effectiveUserID(r), r.Context(), sessStore)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	// Filter by agent memory if requested
	agentFilter := r.URL.Query().Get("agent")
	if agentFilter != "" {
		filtered := make([]fleet.Message, 0, len(messages))
		for _, msg := range messages {
			if msg.InAgentMemory(agentFilter) {
				filtered = append(filtered, msg)
			}
		}
		messages = filtered
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"messages": messages,
	})
}

// getFleetMessages returns all fleet messages for a session. It first checks
// the active session registry (in-memory), then falls back to reading the
// JSONL transcript (completed sessions).
func getFleetMessages(sessionID, userID string, ctx context.Context, sessionStore store.SessionStore) ([]fleet.Message, error) {
	// Try active session first (in-memory registry)
	registry := getFleetSessionRegistry()
	if fs := registry.Get(sessionID); fs != nil {
		thread, err := fs.Channel.GetThread(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get thread: %w", err)
		}
		return thread, nil
	}

	// Platform mode: read from PG store.
	if sessionStore != nil {
		events, err := sessionStore.ReadTranscriptEvents(ctx, studioChatAppName, userID, sessionID)
		if err != nil {
			return nil, fmt.Errorf("session %s not found: %w", sessionID, err)
		}
		messages := eventsToFleetMessages(events)
		if len(messages) == 0 {
			return nil, fmt.Errorf("session %s has no messages", sessionID)
		}
		return messages, nil
	}

	// Personal mode: fall back to JSONL transcript.
	fileStore := getFleetFileStore()
	if fileStore == nil {
		return nil, fmt.Errorf("session %s not found (no active session, no file store)", sessionID)
	}

	events, err := fileStore.ReadTranscriptEvents(studioChatAppName, userID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session %s not found: %w", sessionID, err)
	}

	messages := eventsToFleetMessages(events)
	if len(messages) == 0 {
		return nil, fmt.Errorf("session %s has no messages", sessionID)
	}

	return messages, nil
}

// createFleetMCPToolsets creates sandbox-wired MCP toolsets for a fleet session.
func createFleetMCPToolsets(backend sandbox.Backend, lazyNode *sandbox.LazyNodeClient, pool sandbox.ToolNodePool, mcpStores *store.MCPServerStores) []tool.Toolset {
	var mcpServers map[string]config.MCPServerConfig

	if mcpStores != nil {
		// Platform mode: build merged config from org + team stores
		mcpServers = make(map[string]config.MCPServerConfig)
		if mcpStores.Org != nil {
			orgServers, err := mcpStores.Org.List(context.TODO())
			if err == nil {
				for _, s := range orgServers {
					mcpServers[s.Name] = config.MCPServerConfig{
						Command:   s.Command,
						Args:      s.Args,
						Env:       s.Env,
						Transport: s.Transport,
						URL:       s.URL,
						Enabled:   s.Enabled,
					}
				}
			}
		}
		if mcpStores.Team != nil {
			teamServers, err := mcpStores.Team.List(context.TODO())
			if err == nil {
				for _, s := range teamServers {
					mcpServers[s.Name] = config.MCPServerConfig{
						Command:   s.Command,
						Args:      s.Args,
						Env:       s.Env,
						Transport: s.Transport,
						URL:       s.URL,
						Enabled:   s.Enabled,
					}
				}
			}
		}
	} else {
		// No MCP stores available — fleet operates without MCP tools
		return nil
	}

	if len(mcpServers) == 0 {
		return nil
	}

	// Load cached tools (personal mode only — platform uses DB CachedTools)
	if mcpStores == nil {
		if _, loadErr := cache.LoadCache(); loadErr != nil {
			return nil
		}
	}

	var toolsets []tool.Toolset
	for name, serverCfg := range mcpServers {
		if !serverCfg.IsEnabled() {
			continue
		}

		var cachedTools []cache.ToolEntry
		if mcpStores != nil {
			cachedTools = getPlatformCachedToolsFromStores(mcpStores, name)
		}
		if len(cachedTools) == 0 {
			continue
		}

		// Filter out excluded tools for standard servers (e.g., tavily_research)
		if excluded := config.GetExcludedTools(name); excluded != nil {
			filtered := make([]cache.ToolEntry, 0, len(cachedTools))
			for _, t := range cachedTools {
				if !excluded[t.Name] {
					filtered = append(filtered, t)
				}
			}
			cachedTools = filtered
			if len(cachedTools) == 0 {
				continue
			}
		}

		lt := agent.NewLazyMCPToolset(name, cachedTools, serverCfg, false)
		// Credential placeholders resolve at MCP process start from the
		// request/session context (credentials.ResolverFromContext).
		if lazyNode != nil {
			lt.SetSandboxClient(lazyNode, backend)
		} else if pool != nil {
			lt.SetSandboxPool(pool)
		}
		toolsets = append(toolsets, agent.NewSanitizedToolset(lt, false))
	}

	return toolsets
}

// getPlatformCachedToolsFromStores reads cached tools for a server from the platform
// MCP stores (team first, then org).
func getPlatformCachedToolsFromStores(mcpStores *store.MCPServerStores, serverName string) []cache.ToolEntry {
	if mcpStores.Team != nil {
		srv, err := mcpStores.Team.Get(context.TODO(), serverName)
		if err == nil && srv != nil && len(srv.CachedTools) > 0 {
			return parsePlatformCachedTools(srv.CachedTools)
		}
	}
	if mcpStores.Org != nil {
		srv, err := mcpStores.Org.Get(context.TODO(), serverName)
		if err == nil && srv != nil && len(srv.CachedTools) > 0 {
			return parsePlatformCachedTools(srv.CachedTools)
		}
	}
	return nil
}

// parsePlatformCachedTools parses CachedTools JSON into cache.ToolEntry slice.
func parsePlatformCachedTools(data json.RawMessage) []cache.ToolEntry {
	type toolEntry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	var entries []toolEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}
	result := make([]cache.ToolEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, cache.ToolEntry{
			Name:        e.Name,
			Description: e.Description,
			Source:      "",
		})
	}
	return result
}

// buildAgentList creates a list of agent descriptions for the frontend.
func buildAgentList(fleetCfg *fleet.FleetConfig) []map[string]interface{} {
	if fleetCfg == nil {
		return nil
	}

	agents := make([]map[string]interface{}, 0, len(fleetCfg.Agents))
	for key, agentCfg := range fleetCfg.Agents {
		entry := map[string]interface{}{
			"key":         key,
			"name":        agentCfg.Name,
			"description": agentCfg.Description,
			"mode":        agentCfg.GetMode(),
		}

		// Communication info
		if fleetCfg.Communication != nil {
			for _, node := range fleetCfg.Communication.Flow {
				if node.Role == key {
					entry["talks_to"] = node.TalksTo
					entry["entry_point"] = node.EntryPoint
					break
				}
			}
		}

		agents = append(agents, entry)
	}
	return agents
}
