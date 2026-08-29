package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/a2aclient"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/apps"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/openshell"
	"github.com/SAP/astonish/pkg/scheduler"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/skills"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// ChatAttachment represents a file attached to a chat message.
type ChatAttachment struct {
	Filename string `json:"filename"`
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // base64-encoded file content
}

// StudioChatRequest is the request body for POST /api/studio/chat.
type StudioChatRequest struct {
	SessionID        string           `json:"sessionId,omitempty"`
	Message          string           `json:"message"`
	Attachments      []ChatAttachment `json:"attachments,omitempty"` // file attachments (base64)
	AutoApprove      bool             `json:"autoApprove,omitempty"`
	Debug            bool             `json:"debug,omitempty"`            // reserved for future debug streaming
	SystemContext    string           `json:"systemContext,omitempty"`    // per-turn system instructions (not shown to user)
	PlanMode         bool             `json:"planMode,omitempty"`         // per-turn plan-mode gate: refuse mutating tools + delegate_tasks
	PinnedToolGroups []string         `json:"pinnedToolGroups,omitempty"` // tool groups to always inject (wizard sessions)
	Provider         string           `json:"provider,omitempty"`         // per-request provider override (pre-chat picker)
	Model            string           `json:"model,omitempty"`            // per-request model override (pre-chat picker)
	MemoryScope      string           `json:"memoryScope,omitempty"`      // per-session memory scope: "team" (default) or "personal"
}

// StudioSessionResponse is a single session in list responses.
type StudioSessionResponse struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
	MessageCount int    `json:"messageCount"`
	FleetKey     string `json:"fleetKey,omitempty"`
	FleetName    string `json:"fleetName,omitempty"`
	IssueNumber  int    `json:"issueNumber,omitempty"`
	Repo         string `json:"repo,omitempty"`
}

// StudioSessionDetailResponse is the response for GET /api/studio/sessions/{id}.
type StudioSessionDetailResponse struct {
	StudioSessionResponse
	Messages      []StudioMessage       `json:"messages"`
	FleetMessages []FleetMessageSummary `json:"fleetMessages,omitempty"`
	Artifacts     []ArtifactInfo        `json:"artifacts,omitempty"`  // files generated during the session
	TotalUsage    *UsageSummary         `json:"totalUsage,omitempty"` // cumulative token usage across all LLM calls
}

// UsageSummary holds cumulative token usage for a session, derived from
// the UsageMetadata attached to each LLM response in the session transcript.
type UsageSummary struct {
	InputTokens  int32 `json:"inputTokens"`
	OutputTokens int32 `json:"outputTokens"`
	TotalTokens  int32 `json:"totalTokens"`
}

// ArtifactInfo describes a file artifact produced during a session.
type ArtifactInfo struct {
	Path     string `json:"path"`     // absolute file path on disk
	FileName string `json:"fileName"` // basename (e.g., "report.md")
	FileType string `json:"fileType"` // human-readable type (e.g., "Markdown", "Python")
	ToolName string `json:"toolName"` // "write_file" or "edit_file"

	// IsReport flags artifacts the agent explicitly signaled as reports via
	// an ```astonish-report fence in the same turn. Only artifacts with
	// IsReport=true AND fileType="Markdown" AND emitted in the last turn
	// are eligible for inline EmbeddedFileViewer rendering on the frontend;
	// everything else falls back to the compact ArtifactCard.
	IsReport bool `json:"isReport,omitempty"`

	// ReportTitle is the optional title carried in the astonish-report
	// fence's frontmatter (e.g., "Q4 Revenue Analysis"). May be empty even
	// when IsReport is true; the frontend falls back to the file basename.
	ReportTitle string `json:"reportTitle,omitempty"`
}

// FleetMessageSummary is a fleet message returned when loading fleet session history.
type FleetMessageSummary struct {
	ID        string         `json:"id,omitempty"`
	Sender    string         `json:"sender"`
	Text      string         `json:"text"`
	Mentions  []string       `json:"mentions,omitempty"`
	Timestamp string         `json:"timestamp,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// StudioMessage is a simplified message for the frontend.
type StudioMessage struct {
	Type       string      `json:"type"`                 // user, agent, tool_call, tool_result, image, subtask_execution, plan, distill_preview, distill_saved, app_preview, system
	Content    string      `json:"content,omitempty"`    // text content
	ToolName   string      `json:"toolName,omitempty"`   // for tool_call/tool_result
	ToolID     string      `json:"toolId,omitempty"`     // for tool_call/tool_result (FunctionCall.ID / FunctionResponse.ID)
	ToolArgs   interface{} `json:"toolArgs,omitempty"`   // for tool_call
	ToolResult interface{} `json:"toolResult,omitempty"` // for tool_result

	// image fields — model-returned or reconstructed InlineData images
	Data     string `json:"data,omitempty"`     // base64-encoded image bytes
	MimeType string `json:"mimeType,omitempty"` // e.g. image/png

	// subtask_execution fields — populated for delegate_tasks history reconstruction
	Events []SubTaskEventMsg `json:"events,omitempty"` // lifecycle + activity events
	Tasks  []SubTaskInfoMsg  `json:"tasks,omitempty"`  // task plan (names + descriptions)
	Status string            `json:"status,omitempty"` // complete, partial, error

	// plan fields — populated for announce_plan history reconstruction
	Goal  string        `json:"goal,omitempty"`  // plan title
	Steps []PlanStepMsg `json:"steps,omitempty"` // plan steps with status

	// distill_preview fields — populated for flow distillation review
	YAML        string   `json:"yaml,omitempty"`        // generated flow YAML
	FlowName    string   `json:"flowName,omitempty"`    // suggested flow name
	Description string   `json:"description,omitempty"` // flow description
	Tags        []string `json:"tags,omitempty"`        // flow tags
	Explanation string   `json:"explanation,omitempty"` // human-readable explanation

	// distill_saved fields
	FilePath   string `json:"filePath,omitempty"`   // saved file path
	RunCommand string `json:"runCommand,omitempty"` // suggested run command

	// tutorial_blueprint_preview / approved fields
	BlueprintTitle  string `json:"blueprintTitle,omitempty"`
	BlueprintSuite  string `json:"blueprintSuite,omitempty"`
	BlueprintYAML   string `json:"blueprintYaml,omitempty"`
	BlueprintScenes any    `json:"blueprintScenes,omitempty"`
	DrillYAML       string `json:"drillYaml,omitempty"`
	DrillName       string `json:"drillName,omitempty"`

	// tutorial_scene_slideshow fields — post-run scene navigator
	TutorialTitle  string `json:"tutorialTitle,omitempty"`
	TutorialSuite  string `json:"tutorialSuite,omitempty"`
	TutorialDrill  string `json:"tutorialDrill,omitempty"`
	ManifestPath   string `json:"manifestPath,omitempty"`
	TutorialScenes any    `json:"tutorialScenes,omitempty"`

	// app_preview fields — populated for generative UI app previews
	AppCode    string `json:"code,omitempty"`    // JSX source code
	AppTitle   string `json:"title,omitempty"`   // app title (extracted from component name)
	AppVersion int    `json:"version,omitempty"` // version number (increments on refinement)
	AppID      string `json:"appId,omitempty"`   // stable UUID for cross-turn matching

	// file attachments on user messages — populated when session history is loaded
	Attachments []AttachmentInfo `json:"attachments,omitempty"`

	// docs_update fields — persisted slide-card metadata.
	DocsUpdate *DocsUpdatePayload `json:"docsUpdate,omitempty"`

	// chat_question fields — populated for a generic inline questionnaire card
	// (yes/no or single-select, with optional per-option thumbnails). After the
	// user answers, Studio unmounts the picker and keeps the prompt as an agent
	// bubble; the chosen label re-enters the agent loop as an ordinary user
	// message (no dedicated endpoint). Reload infers answered from that following
	// user message so the selectable card is not remounted.
	QuestionID   string               `json:"questionId,omitempty"`
	QuestionKind string               `json:"questionKind,omitempty"` // "yesno" | "select"
	Prompt       string               `json:"prompt,omitempty"`
	Options      []ChatQuestionOption `json:"options,omitempty"`
}

// ChatQuestionOption is one selectable answer in a chat_question card. For a
// yes/no question Options is empty (the card renders fixed Yes/No buttons).
type ChatQuestionOption struct {
	ID          string                 `json:"id"`
	Label       string                 `json:"label"`
	Description string                 `json:"description,omitempty"`
	Thumbnail   *ChatQuestionThumbnail `json:"thumbnail,omitempty"`
}

// ChatQuestionThumbnail describes an optional visual preview for a select option.
// For slides it carries the archetype ast-slide MARKUP (rendered live in a scaled
// sandboxed iframe by the frontend); for a plain image it carries an assetRef the
// existing asset plumbing resolves at render time. Never carries data: bytes.
type ChatQuestionThumbnail struct {
	Kind     string            `json:"kind"` // "slides-archetype" | "image"
	Markup   string            `json:"markup,omitempty"`
	AssetRef string            `json:"assetRef,omitempty"`
	Theme    map[string]string `json:"theme,omitempty"`
	Template string            `json:"template,omitempty"` // template name; frontend resolves asset-refs from it at render time (never data: bytes)
}

type DocsUpdatePayload struct {
	Type           string                    `json:"type"`
	DeckSlug       string                    `json:"deckSlug"`
	Action         string                    `json:"action"`
	SlideIndex     *int                      `json:"slideIndex,omitempty"`
	TotalSlides    int                       `json:"totalSlides,omitempty"`
	Title          string                    `json:"title,omitempty"`
	DeckTitle      string                    `json:"deckTitle,omitempty"`
	Description    string                    `json:"description,omitempty"`
	SchemaVersion  int                       `json:"schemaVersion,omitempty"`
	Validation     DocsValidationSummary     `json:"validation,omitempty"`
	PPTXCapability DocsPPTXCapabilitySummary `json:"pptxCapability,omitempty"`
}

type DocsValidationSummary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
}

type DocsPPTXCapabilitySummary struct {
	Native      int `json:"native"`
	Vector      int `json:"vector"`
	Raster      int `json:"raster"`
	Unsupported int `json:"unsupported"`
}

// AttachmentInfo describes a file attachment in session history display.
type AttachmentInfo struct {
	Filename string `json:"filename"`
	MimeType string `json:"mimeType"`
	Size     int    `json:"size"`           // byte count of the decoded file
	Data     string `json:"data,omitempty"` // base64 data (included for images to render thumbnails)
}

// SubTaskInfoMsg describes a single task in a delegation plan (for history reconstruction).
type SubTaskInfoMsg struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SubTaskEventMsg is a synthetic event reconstructed from persisted delegate_tasks data.
type SubTaskEventMsg struct {
	Type     string `json:"type"`                // delegation_start, delegation_complete, task_start, task_complete, task_failed, task_text
	TaskName string `json:"task_name,omitempty"` // which task this event belongs to
	Status   string `json:"status,omitempty"`    // for delegation_complete
	Duration string `json:"duration,omitempty"`  // for task_complete/task_failed
	Error    string `json:"error,omitempty"`     // for task_failed
	Text     string `json:"text,omitempty"`      // for task_text (sub-agent result output)
}

// PlanStepMsg describes a step in the execution plan (for history reconstruction).
type PlanStepMsg struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"` // pending, running, complete, failed
}

// toAgentAttachments converts API ChatAttachments to agent.Attachment values.
func toAgentAttachments(apiAtts []ChatAttachment) []agent.Attachment {
	out := make([]agent.Attachment, len(apiAtts))
	for i, a := range apiAtts {
		out[i] = agent.Attachment{
			Filename: a.Filename,
			MimeType: a.MimeType,
			Data:     a.Data,
		}
	}
	return out
}

// studioUserContent builds the ADK user turn for a Studio chat request.
// Attachments alone (empty caption) still produce a user message so the
// conversation does not end on an assistant turn.
func studioUserContent(msg string, atts []ChatAttachment) *genai.Content {
	msg = strings.TrimSpace(msg)
	if len(atts) > 0 {
		return agent.NewTimestampedUserContentWithAttachments(msg, toAgentAttachments(atts))
	}
	if msg == "" {
		return nil
	}
	return agent.NewTimestampedUserContent(msg)
}

func chatFilesFromAttachments(apiAtts []ChatAttachment) []store.ChatFile {
	if len(apiAtts) == 0 {
		return nil
	}
	out := make([]store.ChatFile, 0, len(apiAtts))
	for _, a := range apiAtts {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(a.Data))
		if err != nil || len(raw) == 0 {
			continue
		}
		out = append(out, store.ChatFile{
			Filename: strings.TrimSpace(a.Filename),
			MimeType: strings.TrimSpace(a.MimeType),
			Data:     raw,
		})
	}
	return out
}

// StudioChatComponents holds the wired components needed by Studio chat handlers.
// Set via SetStudioChatInitFunc from the launcher package to avoid import cycles.
type StudioChatComponents struct {
	ChatAgent         *agent.ChatAgent
	LLM               model.LLM
	SwappableLLM      *provider.SwappableLLM // for hot-swap on model change (nil = hot-swap disabled)
	SessionService    session.Service
	ProviderName      string
	ModelName         string
	Compactor         *persistentsession.Compactor
	InternalToolCount int
	MemoryActive      bool
	SandboxEnabled    bool
	StartupNotices    []string
	ShutdownSandbox   func() // stops containers without destroying (for daemon shutdown)
	Cleanup           func()
	SandboxPool       sandbox.ToolNodePool // for adaptive InvalidateSandboxClient
	// FilesystemSkills is a copy of the exact filesystem skill slice used to
	// initialize this runtime. Platform DB scopes overlay it at request time.
	FilesystemSkills []skills.Skill
}

// ChatManager manages a singleton chat agent for Studio chat.
type ChatManager struct {
	mu         sync.Mutex
	components *StudioChatComponents
	initFn     func(ctx context.Context) (*StudioChatComponents, error)

	// preWarmCtxFn builds a context suitable for PreWarm. Set by the daemon
	// at startup so that Reset() can automatically trigger background re-init.
	preWarmCtxFn func() context.Context

	// llmPool caches LLM instances keyed by provider+model. Used in platform
	// mode to resolve per-request LLMs based on team/org/platform hierarchy
	// without creating a new HTTP client for every message.
	llmPool *provider.Pool

	// active holds cancel functions for in-flight SSE streams keyed by session ID.
	active map[string]context.CancelFunc
	amu    sync.Mutex
}

const (
	studioChatAppName = "astonish"
	studioChatUserID  = "studio_user"
)

// globalChatManager is the singleton for Studio chat.
var globalChatManager *ChatManager
var chatManagerOnce sync.Once

// GetChatManager returns the singleton ChatManager.
func GetChatManager() *ChatManager {
	chatManagerOnce.Do(func() {
		globalChatManager = &ChatManager{
			active: make(map[string]context.CancelFunc),
		}
	})
	return globalChatManager
}

// SetStudioChatInitFunc sets the factory function used to initialize the chat agent.
// Called from the launcher package to avoid import cycles.
func SetStudioChatInitFunc(fn func(ctx context.Context) (*StudioChatComponents, error)) {
	GetChatManager().initFn = fn
}

// SetPreWarmContextFunc sets a function that builds a context suitable for
// background PreWarm calls. This enables Reset() to automatically start
// re-initialization in the background so the next request doesn't pay the
// full initialization cost.
func SetPreWarmContextFunc(fn func() context.Context) {
	GetChatManager().preWarmCtxFn = fn
}

// SetLLMPool sets the shared LLM pool on the ChatManager. In platform mode,
// the pool is used for per-request LLM resolution (team/org/platform hierarchy)
// instead of using the singleton SwappableLLM for all requests.
func SetLLMPool(pool *provider.Pool) {
	GetChatManager().llmPool = pool
}

// InvalidateLLMPool drops all cached LLM instances so the next request creates
// fresh ones. Call this when provider configurations change (API keys, etc.).
func (cm *ChatManager) InvalidateLLMPool() {
	if cm.llmPool != nil {
		cm.llmPool.Invalidate()
	}
}

// Reset tears down the current chat agent so the next request re-initializes
// with fresh config from disk. This is called when settings change (provider,
// model, MCP, etc.) to ensure new chats pick up the updated configuration.
func (cm *ChatManager) Reset() {
	// Cancel all in-flight SSE streams first.
	cm.amu.Lock()
	for sid, cancel := range cm.active {
		cancel()
		delete(cm.active, sid)
	}
	cm.amu.Unlock()

	// Stop all background runners
	getChatRunnerRegistry().StopAll()

	cm.mu.Lock()
	if cm.components != nil {
		if cm.components.Cleanup != nil {
			cm.components.Cleanup()
		}
		cm.components = nil
	}
	cm.mu.Unlock()

	// Trigger background re-initialization so the next request is fast.
	// This avoids the user having to wait for full init on their first message
	// after a settings change.
	if cm.preWarmCtxFn != nil {
		go func() {
			ctx := cm.preWarmCtxFn()
			if err := cm.PreWarm(ctx); err != nil {
				slog.Warn("background pre-warm after reset failed (will retry on next request)", "error", err)
			} else {
				slog.Info("chat agent re-initialized after settings change")
			}
		}()
	}
}

// ShutdownContainers stops sandbox containers without destroying them.
// Used during graceful daemon shutdown — containers are preserved so sessions
// can reconnect after restart. Unlike Reset(), this does not tear down the
// chat agent or destroy containers.
func (cm *ChatManager) ShutdownContainers() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.components != nil && cm.components.ShutdownSandbox != nil {
		cm.components.ShutdownSandbox()
	}
}

// HotSwapLLM replaces the LLM provider/model without destroying the rest of
// the chat infrastructure (tools, MCP servers, sandbox, ToolIndex, etc.).
// This is called when only the model/provider has changed, avoiding the full
// teardown-and-rebuild cycle that causes the first-message delay.
//
// Returns nil on successful swap. Returns an error if the swap cannot be
// performed — the error message will contain "credential" when the target
// provider has no credentials configured, so callers (e.g. PATCH /model)
// can surface a 409 + credentialsAvailable=false banner.
func (cm *ChatManager) HotSwapLLM(ctx context.Context, providerName, modelName string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.components == nil {
		slog.Warn("[hot-swap] FAILED: components is nil (chat agent not initialized yet)")
		return fmt.Errorf("hot-swap unavailable: chat agent not initialized")
	}
	if cm.components.SwappableLLM == nil {
		slog.Warn("[hot-swap] FAILED: SwappableLLM is nil (factory didn't create it)")
		return fmt.Errorf("hot-swap unavailable: SwappableLLM not initialized")
	}

	// Load the effective app config for provider resolution.
	// Detect platform mode from the request context (store.Services presence).
	isPlatform := store.FromContext(ctx) != nil
	appCfg := EffectiveAppConfigFromContext(ctx, isPlatform)

	slog.Info("[hot-swap] creating new LLM provider",
		"provider", providerName, "model", modelName, "isPlatform", isPlatform)

	// Create the new LLM. GetProvider fails when the resolved provider has no
	// credential configured — surface that as an error containing "credential"
	// so PATCH /model can respond with credentialsAvailable=false.
	newLLM, err := provider.GetProvider(ctx, providerName, modelName, appCfg)
	if err != nil {
		slog.Warn("[hot-swap] FAILED: GetProvider error",
			"provider", providerName, "model", modelName, "error", err)
		return fmt.Errorf("credential lookup failed for provider %q: %w", providerName, err)
	}

	// Swap the underlying LLM — all closures (compactor, reflector, etc.)
	// that reference the SwappableLLM will automatically use the new one.
	cm.components.SwappableLLM.Swap(newLLM)

	// Update metadata
	cm.components.ProviderName = providerName
	cm.components.ModelName = modelName

	// Update compactor with new context window size
	if cm.components.Compactor != nil {
		contextWindow := provider.ResolveContextWindowCached(ctx, providerName, modelName, appCfg)
		cm.components.Compactor.SetContextWindow(contextWindow)
	}

	slog.Info("[hot-swap] SUCCESS — LLM swapped without Reset()",
		"provider", providerName, "model", modelName)
	return nil
}

// PreWarm initializes the chat agent in the background so the first request is fast.
// In platform mode, the provided context must carry store.Services for config resolution.
// Safe to call concurrently with incoming requests — ensureReady is mutex-protected.
func (cm *ChatManager) PreWarm(ctx context.Context) error {
	return cm.ensureReady(ctx)
}

// ensureReady lazily initializes the ChatAgent on first use.
func (cm *ChatManager) ensureReady(ctx context.Context) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.components != nil {
		return nil
	}
	if cm.initFn == nil {
		return fmt.Errorf("studio chat not initialized: no init function set")
	}

	started := time.Now()
	components, err := cm.initFn(ctx)
	if err != nil {
		slog.Warn("studio chat initialization failed", "component", "studio-chat", "elapsed", time.Since(started).Round(time.Millisecond), "error", err)
		return err
	}

	cm.components = components
	slog.Info("studio chat initialized", "component", "studio-chat", "elapsed", time.Since(started).Round(time.Millisecond))
	return nil
}

// registerStream stores a cancel function for an active SSE stream.
func (cm *ChatManager) registerStream(sessionID string, cancel context.CancelFunc) {
	cm.amu.Lock()
	defer cm.amu.Unlock()
	if prev, ok := cm.active[sessionID]; ok {
		prev()
	}
	cm.active[sessionID] = cancel
}

// unregisterStream removes the cancel function for a session.
func (cm *ChatManager) unregisterStream(sessionID string) {
	cm.amu.Lock()
	defer cm.amu.Unlock()
	delete(cm.active, sessionID)
}

// cancelStream cancels an active stream for a session.
func (cm *ChatManager) cancelStream(sessionID string) {
	cm.amu.Lock()
	defer cm.amu.Unlock()
	if cancel, ok := cm.active[sessionID]; ok {
		cancel()
		delete(cm.active, sessionID)
	}
}

// fileStore returns the FileStore from the components, or nil.
func (cm *ChatManager) fileStore() *persistentsession.FileStore {
	if cm.components == nil {
		return nil
	}
	fs, _ := cm.components.SessionService.(*persistentsession.FileStore)
	return fs
}

// StudioChatHandler handles POST /api/studio/chat with SSE streaming.
// The agent runs in a background goroutine (decoupled from the HTTP request
// lifecycle) so navigating away or closing the browser tab does NOT kill the
// running task. The SSE connection acts as a viewer/subscriber — disconnecting
// just removes the subscriber while the agent keeps working. Reconnecting
// (via StudioChatStreamHandler) replays missed events and resumes live streaming.
func StudioChatHandler(w http.ResponseWriter, r *http.Request) {
	var req StudioChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	userID := effectiveUserID(r)

	cm := GetChatManager()
	if err := cm.ensureReady(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
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

	comp := cm.components
	chatAgent := comp.ChatAgent

	// Resolve session service per-request:
	// Platform mode: regular chat uses personal sessions (private-first);
	// fleet sub-sessions use team sessions (shared).
	// Personal mode falls back to the singleton file-based session service.
	var sessionService session.Service
	var titleSetter SessionTitleSetter
	if svc := store.FromRequest(r); svc != nil && svc.PersonalSessions != nil {
		sessionService = svc.PersonalSessions
		titleSetter = svc.PersonalSessions
	} else if svc := store.FromRequest(r); svc != nil && svc.Sessions != nil {
		// Fallback: if personal sessions not wired (shouldn't happen), use team
		sessionService = svc.Sessions
		titleSetter = svc.Sessions
	} else {
		sessionService = comp.SessionService
		titleSetter = cm.fileStore() // may be nil in edge cases
	}

	// Handle slash commands server-side (these are lightweight, no background runner needed)
	msg := strings.TrimSpace(req.Message)

	// /fleet [task]: open fleet dialog, optionally pre-populated with the task
	if msg == "/fleet" || strings.HasPrefix(msg, "/fleet ") {
		task := strings.TrimSpace(strings.TrimPrefix(msg, "/fleet"))
		SendSSE(w, flusher, "fleet_redirect", map[string]interface{}{
			"task": task,
		})
		SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
		return
	}

	// /drill: start a drill suite creation conversation
	if msg == "/drill" || strings.HasPrefix(msg, "/drill ") {
		hint := strings.TrimSpace(strings.TrimPrefix(msg, "/drill"))
		eventData := map[string]interface{}{
			"hint":                 hint,
			"wizard_system_prompt": tools.GetDrillWizardPrompt(),
			"pinned_tool_groups":   []string{"drill"},
		}
		SendSSE(w, flusher, "drill_redirect", eventData)
		SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
		return
	}

	// /tutorial-drill-add <suite> (alias: /tutorial-add): add to an existing tutorial suite only.
	// Match longer prefixes before /tutorial-drill and /tutorial.
	if suiteName, ok := tutorialDrillAddSuite(msg); ok {
		suiteName, suiteContext, err := resolveTutorialAddContext(r.Context(), flowStoreFromRequest(r), suiteName)
		if err != nil {
			SendSSE(w, flusher, "error", map[string]interface{}{"error": err.Error()})
			SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
			return
		}
		eventData := map[string]interface{}{
			"suite_name":           suiteName,
			"wizard_kind":          "tutorial",
			"wizard_system_prompt": tools.GetTutorialAddPrompt(suiteName, suiteContext),
			"pinned_tool_groups":   []string{"drill"},
		}
		SendSSE(w, flusher, "drill_add_redirect", eventData)
		SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
		return
	}

	// /tutorial-drill [hint] (alias: /tutorial): start tutorial drill creation.
	if hint, ok := tutorialDrillHint(msg); ok {
		eventData := map[string]interface{}{
			"hint":                 hint,
			"wizard_kind":          "tutorial",
			"wizard_system_prompt": tools.GetTutorialWizardPrompt(),
			"pinned_tool_groups":   []string{"drill"},
		}
		SendSSE(w, flusher, "drill_redirect", eventData)
		SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
		return
	}

	// /drill-add <suite>: add new drills to an existing suite
	if strings.HasPrefix(msg, "/drill-add ") {
		suiteName := strings.TrimSpace(strings.TrimPrefix(msg, "/drill-add"))
		_, wizardPrompt, err := resolveDrillAddWizard(r.Context(), flowStoreFromRequest(r), suiteName)
		if err != nil {
			SendSSE(w, flusher, "error", map[string]interface{}{"error": err.Error()})
			SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
			return
		}
		eventData := map[string]interface{}{
			"suite_name":           suiteName,
			"wizard_system_prompt": wizardPrompt,
			"pinned_tool_groups":   []string{"drill"},
		}
		SendSSE(w, flusher, "drill_add_redirect", eventData)
		SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
		return
	}

	// /fleet-plan: start a fleet plan creation conversation
	if msg == "/fleet-plan" || strings.HasPrefix(msg, "/fleet-plan ") {
		rawHint := strings.TrimSpace(strings.TrimPrefix(msg, "/fleet-plan"))
		draftID := ""
		hint := rawHint
		if idx := strings.Index(rawHint, " --draft="); idx >= 0 {
			hint = strings.TrimSpace(rawHint[:idx])
			draftID = strings.TrimSpace(rawHint[idx+len(" --draft="):])
		}
		if draftID == "" {
			draftID = strings.TrimSpace(r.URL.Query().Get("draft_id"))
		}
		eventData := map[string]interface{}{
			"hint": hint,
		}
		if hint != "" && draftID == "" {
			if newDraftID, err := ensureSetupDraft(r, hint, ""); err == nil {
				draftID = newDraftID
			}
		}
		if draftID != "" {
			eventData["setup_draft_id"] = draftID
		}
		if hint != "" {
			profileKey, desc, prompt, pinned, currentStepID, currentStepTitle, err := ResolveSetupWizardContext(r, hint, draftID)
			if err == nil && prompt != "" {
				if desc != "" {
					eventData["wizard_description"] = desc
				}
				eventData["wizard_system_prompt"] = prompt
				if profileKey != "" {
					eventData["setup_profile_key"] = profileKey
				}
				if len(pinned) > 0 {
					eventData["pinned_tool_groups"] = pinned
				}
				if currentStepID != "" {
					eventData["current_setup_step"] = currentStepID
					eventData["current_setup_step_title"] = currentStepTitle
				}
			}
		}
		SendSSE(w, flusher, "fleet_plan_redirect", eventData)
		SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
		return
	}

	if strings.HasPrefix(msg, "/") {
		// Use r.Context() for slash commands since they're lightweight
		handleSlashCommand(r, w, flusher, cm, sessionService, msg, userID, req.SessionID)
		return
	}

	// Intercept tutorial blueprint approval / cancel / revise intents.
	// Approve emits the approved card then rewrites msg so ChatRunner continues
	// refine → validate_drill → save_drill in the same SSE stream.
	if req.SessionID != "" && chatAgent.HasPendingTutorialBlueprint(req.SessionID) {
		handled, rewrite := handleTutorialBlueprintIntent(r, w, flusher, chatAgent, sessionService, userID, req.SessionID, msg)
		if handled {
			return
		}
		if rewrite != "" {
			msg = rewrite
		}
	}

	// Intercept messages for pending distill review (interactive modification loop).
	if req.SessionID != "" && chatAgent.HasPendingDistillReview(req.SessionID) {
		intent := chatAgent.ClassifyDistillReviewIntent(r.Context(), msg)

		switch intent {
		case agent.DistillIntentSave:
			// Save the flow — persist a clean user message
			userText := msg
			if msg == "__distill_save__" {
				userText = "Save Flow"
			}
			persistSessionMessage(r.Context(), sessionService, userID, req.SessionID, "user", userText)

			// Enrich context with composite FlowStore (personal-first) so
			// SaveDistillReview persists to the user's personal store.
			saveCtx := r.Context()
			if svc := store.FromRequest(r); svc != nil && (svc.PersonalFlows != nil || svc.Flows != nil) {
				saveCtx = store.WithFlowStore(saveCtx, store.NewCompositeFlowStore(svc.PersonalFlows, svc.Flows))
			}

			filePath, runCmd, err := chatAgent.SaveDistillReview(saveCtx, req.SessionID)
			if err != nil {
				errText := fmt.Sprintf("Failed to save flow: %v", err)
				SendSSE(w, flusher, "text", map[string]interface{}{"text": errText})
				persistSessionMessage(r.Context(), sessionService, userID, req.SessionID, "model", errText)
			} else {
				SendSSE(w, flusher, "distill_saved", map[string]interface{}{
					"filePath":   filePath,
					"runCommand": runCmd,
				})
				persistDistillSaved(r.Context(), sessionService, userID, req.SessionID, filePath, runCmd)
			}
			SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
			return

		case agent.DistillIntentCancel:
			persistSessionMessage(r.Context(), sessionService, userID, req.SessionID, "user", msg)
			chatAgent.CancelDistillReview(req.SessionID)
			responseText := "Distill review cancelled."
			SendSSE(w, flusher, "text", map[string]interface{}{"text": responseText})
			persistSessionMessage(r.Context(), sessionService, userID, req.SessionID, "model", responseText)
			SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
			return

		case agent.DistillIntentTestRun:
			persistSessionMessage(r.Context(), sessionService, userID, req.SessionID, "user", msg)

			if chatAgent.FlowRunner == nil {
				errText := "Test run is not available — the flow runner is not configured in this environment."
				SendSSE(w, flusher, "text", map[string]interface{}{"text": errText})
				persistSessionMessage(r.Context(), sessionService, userID, req.SessionID, "model", errText)
				SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
				return
			}

			SendSSE(w, flusher, "text", map[string]interface{}{"text": "Running test execution with the original parameters...\n\n"})

			// Inject PG credential store into dry-run context so that
			// BeforeToolCallback in the flow agent can resolve {{CREDENTIAL:...}}
			// placeholders in tool arguments (e.g., shell_command).
			dryCtx := r.Context()
			if svc := store.FromRequest(r); svc != nil && (svc.PersonalCredentials != nil || svc.Credentials != nil) {
				dryCtx = store.WithCredentialStore(dryCtx, store.NewMergedCredentialStore(svc.PersonalCredentials, svc.Credentials))
			}
			dryResult, dryErr := chatAgent.DryRunDistilledFlow(dryCtx, req.SessionID)

			var analysisText string
			if dryErr != nil {
				analysisText = fmt.Sprintf("**Test run failed to start:** %v\n\nThe flow could not be executed. This may indicate a configuration issue. You can say \"fix it\" and I'll attempt to correct the flow, or describe specific changes you'd like.", dryErr)
			} else {
				// Use LLM to intelligently evaluate the result
				assessment := chatAgent.EvaluateDryRunResult(r.Context(), req.SessionID, dryResult)

				if dryResult.Success {
					output := strings.TrimSpace(dryResult.Output)
					if output != "" && len(output) > 3000 {
						output = output[:3000] + "\n... (truncated)"
					}
					if output != "" {
						analysisText = fmt.Sprintf("**Test run completed.**\n\n**Output:**\n```\n%s\n```\n\n%s", output, assessment)
					} else {
						analysisText = fmt.Sprintf("**Test run completed.**\n\n%s", assessment)
					}
				} else {
					output := strings.TrimSpace(dryResult.Output)
					if len(output) > 2000 {
						output = output[:2000] + "\n... (truncated)"
					}
					analysisText = fmt.Sprintf("**Test run failed.**\n\n%s", assessment)
					if output != "" {
						analysisText += fmt.Sprintf("\n\n**Partial output:**\n```\n%s\n```", output)
					}
				}
			}

			SendSSE(w, flusher, "text", map[string]interface{}{"text": analysisText})
			persistSessionMessage(r.Context(), sessionService, userID, req.SessionID, "model", analysisText)
			SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
			return

		default: // DistillIntentModify
			persistSessionMessage(r.Context(), sessionService, userID, req.SessionID, "user", msg)
			SendSSE(w, flusher, "text", map[string]interface{}{"text": "Modifying flow...\n"})
			review, err := chatAgent.ModifyDistillReview(r.Context(), req.SessionID, msg)
			if err != nil {
				errText := fmt.Sprintf("Failed to modify flow: %v\nYou can try another change, type `save` to save as-is, or `cancel` to abort.", err)
				SendSSE(w, flusher, "text", map[string]interface{}{"text": errText})
				persistSessionMessage(r.Context(), sessionService, userID, req.SessionID, "model", errText)
			} else {
				SendSSE(w, flusher, "distill_preview", map[string]interface{}{
					"yaml":        review.YAML,
					"flowName":    review.FlowName,
					"description": review.Description,
					"tags":        review.Tags,
					"explanation": review.Explanation,
				})
				persistDistillPreview(r.Context(), sessionService, userID, req.SessionID, review)
			}
			SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
			return
		}
	}

	// Lazy reconstruction of active app state from session history.
	// Handles the case where the server restarted and in-memory state was lost.
	if req.SessionID != "" && !chatAgent.HasActiveApp(req.SessionID) {
		getResp, err := sessionService.Get(r.Context(), &session.GetRequest{
			AppName:   studioChatAppName,
			UserID:    userID,
			SessionID: req.SessionID,
		})
		if err == nil && getResp != nil && getResp.Session != nil {
			if app := reconstructActiveApp(getResp.Session.Events()); app != nil {
				chatAgent.SetActiveApp(req.SessionID, app)
			}
		}
	}

	// Intercept messages for active app refinement (iterative visual app loop).
	// Unlike distill (which fully intercepts), "refine" falls through to normal
	// agent flow with context injection; only "save" is handled as early-return.
	if req.SessionID != "" && chatAgent.HasActiveApp(req.SessionID) {
		llmFunc := makeLLMFuncFromModel(comp.LLM)
		appIntent := chatAgent.ClassifyAppIntent(r.Context(), msg, llmFunc)

		switch appIntent.Intent {
		case agent.AppIntentSave, agent.AppIntentDone:
			// User wants to save the app — persist to disk, clear state, acknowledge
			activeApp := chatAgent.GetActiveApp(req.SessionID)
			userText := msg
			if msg == "__app_save__" || msg == "__app_done__" || strings.HasPrefix(msg, "__app_save__:") {
				userText = "Save"
			}
			persistSessionMessage(r.Context(), sessionService, userID, req.SessionID, "user", userText)

			// Save the app to disk
			var savedPath, savedName string
			if activeApp != nil {
				// Use custom name from LLM classification if provided, otherwise fall back to auto-title
				appName := activeApp.Title
				if appIntent.SaveName != "" {
					appName = appIntent.SaveName
				}
				savedApp := &apps.VisualApp{
					Name:        appName,
					Description: appName,
					Code:        activeApp.Code,
					Version:     activeApp.Version,
					SessionID:   req.SessionID,
				}
				var saveErr error
				// Save to personal store first, team store as fallback.
				if svc := store.FromRequest(r); svc != nil && svc.PersonalApps != nil {
					savedPath, saveErr = svc.PersonalApps.Save(r.Context(), savedApp)
				} else if svc := store.FromRequest(r); svc != nil && svc.Apps != nil {
					savedPath, saveErr = svc.Apps.Save(r.Context(), savedApp)
				} else {
					saveErr = fmt.Errorf("app store not available")
				}
				if saveErr != nil {
					slog.Error("failed to save app", "error", saveErr)
				}
				savedName = apps.Slugify(appName)
			}

			chatAgent.ClearActiveApp(req.SessionID)

			responseText := "App saved! You can find it in the Apps tab, or continue chatting."
			if savedPath == "" {
				responseText = "App refinement complete. You can continue chatting or start a new app."
			}
			SendSSE(w, flusher, "text", map[string]interface{}{"text": responseText})
			SendSSE(w, flusher, "app_saved", map[string]interface{}{
				"name": savedName,
				"path": savedPath,
			})
			persistSessionMessage(r.Context(), sessionService, userID, req.SessionID, "model", responseText)
			SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
			return

		case agent.AppIntentRefine:
			// Inject current app source into system context so the LLM
			// knows it's refining an existing component, then fall through
			// to the normal agent flow.
			activeApp := chatAgent.GetActiveApp(req.SessionID)
			if activeApp != nil {
				refinementCtx := agent.BuildAppRefinementContext(activeApp)
				if req.SystemContext != "" {
					req.SystemContext = req.SystemContext + "\n\n" + refinementCtx
				} else {
					req.SystemContext = refinementCtx
				}
				// Record the modification request in the active app history
				chatAgent.RecordAppModification(req.SessionID, msg)
			}
			// Fall through to normal agent flow

		case agent.AppIntentUnrelated:
			// Clear active app context and proceed normally
			chatAgent.ClearActiveApp(req.SessionID)
			// Fall through to normal agent flow
		}
	}

	// Create or resume session
	sessionID := req.SessionID
	isNew := false
	if sessionID == "" {
		resp, err := sessionService.Create(r.Context(), &session.CreateRequest{
			AppName: studioChatAppName,
			UserID:  userID,
		})
		if err != nil {
			SendErrorSSE(w, flusher, fmt.Sprintf("Failed to create session: %v", err))
			return
		}
		sessionID = resp.Session.ID()
		isNew = true
	}

	// Prepare user message. Image-only sends (paste, no caption) must still
	// be a user turn — Bedrock rejects a request whose last message is
	// assistant ("does not support assistant message prefill").
	userMsg := studioUserContent(msg, req.Attachments)

	// Seed ActiveApp from system context when opening a saved app for refinement.
	// This avoids making the LLM re-emit the component on the first turn.
	var seededAppPreview map[string]any
	if isNew && req.SystemContext != "" {
		if code, title := extractAppFromSystemContext(req.SystemContext); code != "" {
			appID := uuid.New().String()
			activeApp := &agent.ActiveApp{
				AppID:    appID,
				Title:    title,
				Code:     code,
				Versions: []string{},
				Version:  1,
			}
			chatAgent.SetActiveApp(sessionID, activeApp)
			persistAppPreview(r.Context(), sessionService, userID, sessionID, code, title, 1, appID)
			seededAppPreview = map[string]any{
				"code":    code,
				"title":   title,
				"version": 1,
				"appId":   appID,
			}
		}
	}

	// Launch background runner — the agent runs independently of this HTTP request.
	runner := newChatRunner(sessionID, userID, isNew)
	runner.titleWaitTimeout = 30 * time.Second // wait for title refine before closing SSE
	if files := chatFilesFromAttachments(req.Attachments); len(files) > 0 {
		runner.InjectChatFiles(files)
	}

	// Inject tenant-scoped credential store into the runner context so that
	// credential tools (list_credentials, resolve_credential, etc.) can access
	// the correct store for this team/org in platform mode.
	// The BeforeToolCallback in chat_agent_run.go also checks this context value
	// for credential placeholder substitution ({{CREDENTIAL:...}} tokens),
	// falling back to the agent's file-based CredentialStore field.
	//
	// In platform mode, we inject a merged store (personal-first, team-fallback)
	// so the LLM resolves the user's personal credentials first, then team creds.
	// Writes from chat always go to the personal store.
	if svc := injectRequestDocsStores(runner, r); svc != nil {
		if svc.PersonalCredentials != nil || svc.Credentials != nil {
			merged := store.NewMergedCredentialStore(svc.PersonalCredentials, svc.Credentials)
			runner.InjectCredentialStore(merged)

			// Hydrate the shared Redactor from the PG-backed credential store.
			// This ensures the Redactor knows about all credential values for
			// this user's session — critical for tool output redaction and
			// preventing secret leakage into session history.
			if chatAgent.Redactor != nil {
				chatAgent.Redactor.HydrateFromStore(merged)
			}
		}
	}

	// Inject the Redactor into the runner context so that memory_save can
	// call Placeholderize() to replace raw credential values with actionable
	// {{CREDENTIAL:name:field}} tokens before persisting to memory.
	if chatAgent.Redactor != nil {
		runner.InjectRedactor(chatAgent.Redactor)
	}

	// Inject tenant-scoped memory stores into the runner context so that
	// memory_search, memory_save tools, and the KnowledgeSearch callback can
	// use the PG-backed stores (team + three-tier) in platform mode.
	if svc := store.FromRequest(r); svc != nil {
		memStore := svc.Memory
		memoryScope := store.MemoryScopeTeam
		// Determine memory scope: body field takes precedence, then header (deprecated fallback).
		// Default is "team" — personal mode must be explicitly requested per-session.
		requestedScope := req.MemoryScope
		if requestedScope == "" {
			requestedScope = r.Header.Get("X-Astonish-Memory-Mode") // deprecated: prefer body field
		}
		// If personal memory mode is active, the memory_save tool should
		// write to the user's personal store instead of team.
		// The ThreeTierSearcher remains unchanged (always searches all tiers).
		if requestedScope == "personal" && svc.TenantRouter != nil {
			if pu := GetPlatformUser(r); pu != nil {
				if orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug); err == nil {
					memStore = orgStore.ForUser(pu.ID).Memories()
					memoryScope = store.MemoryScopePersonal
				}
			}
		}
		runner.InjectMemoryStoresWithScope(memStore, memoryScope, svc.MemorySearcher)
		if svc.TenantRouter != nil {
			if pu := GetPlatformUser(r); pu != nil {
				if orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug); err == nil {
					runner.InjectMemoryStoresByScope(store.MemoryStoresByScope{
						Personal: orgStore.ForUser(pu.ID).Memories(),
						Team:     svc.Memory,
						Org:      orgStore.OrgMemories(),
					})
				}
				canDeleteTeamMemory := CanManageTeam(r, pu)
				canDeleteOrgMemory := CanManageOrg(pu)
				runner.InjectMemoryDeleteAuthorizer(func(_ context.Context, entry *store.MemorySearchResult, scope string) error {
					canDelete := false
					switch scope {
					case string(store.MemoryScopePersonal):
						canDelete = true
					case string(store.MemoryScopeTeam):
						canDelete = entry.CreatedBy == pu.ID || canDeleteTeamMemory
					case string(store.MemoryScopeOrg):
						canDelete = entry.CreatedBy == pu.ID || canDeleteOrgMemory
					}
					if canDelete {
						return nil
					}
					return fmt.Errorf("permission denied: you can only delete memories you created or that you have admin access to")
				})
			}
		}

		// Inject the cross-session memory merge function so that memory_save
		// performs dedup/merge instead of blind inserts. This requires the
		// PlatformReflector (which provides the LLM for merge calls).
		if chatAgent.PlatformReflector != nil {
			runner.InjectMemorySaveOrMerge(chatAgent.PlatformReflector.MemorySaveOrMergeFunc())
		}
	}

	// Inject composite flow store (personal-first, team-fallback) into the runner
	// context so that run_flow, search_flows, etc. resolve personal flows first
	// and save to personal. Users promote to team explicitly.
	if svc := store.FromRequest(r); svc != nil && (svc.PersonalFlows != nil || svc.Flows != nil) {
		runner.InjectFlowStore(store.NewCompositeFlowStore(svc.PersonalFlows, svc.Flows))
	}

	// Inject team-only flow store for drill tools (drills are team-scoped artifacts).
	if svc := store.FromRequest(r); svc != nil && svc.Flows != nil {
		runner.InjectTeamFlowStore(svc.Flows)
	}

	// Inject tenant-scoped drill report store into the runner context so that
	// the run_drill tool can persist execution results to the database.
	if svc := store.FromRequest(r); svc != nil && svc.DrillReports != nil {
		runner.InjectDrillReportStore(svc.DrillReports)
	}

	// DB-backed skills are a platform-only runtime capability. Overlay them on
	// the exact filesystem slice used to initialize this component set.
	if svc := store.FromRequest(r); svc != nil && svc.Mode == store.ModePlatform {
		runner.InjectSkillStores(svc.PlatformSkills, svc.Skills, svc.TeamSkills)
		runner.InjectSkillIndex(buildMergedSkillIndex(r.Context(), comp.FilesystemSkills, svc.PlatformSkills, svc.Skills, svc.TeamSkills))
	}

	// Inject tenant-scoped MCP server stores into the runner context so that
	// the chat agent can resolve MCP server configs from platform, org, and
	// team stores. Platform-tier servers (e.g. standard servers like Tavily
	// installed at scope=platform) cascade down into every org/team chat.
	if svc := store.FromRequest(r); svc != nil && (svc.PlatformMCPServers != nil || svc.MCPServers != nil || svc.TeamMCPServers != nil) {
		runner.InjectMCPServerStores(svc.PlatformMCPServers, svc.MCPServers, svc.TeamMCPServers)
	}

	// Inject tenant-scoped A2A agent stores so the chat agent can resolve
	// A2A agents from platform, org, and team stores (same cascade pattern as MCP).
	if svc := store.FromRequest(r); svc != nil && (svc.PlatformA2AAgents != nil || svc.A2AAgents != nil || svc.TeamA2AAgents != nil) {
		runner.InjectA2AAgentStores(svc.PlatformA2AAgents, svc.A2AAgents, svc.TeamA2AAgents)
	}

	// Per-request MCP tool groups: the singleton agent is often pre-warmed for
	// a default team without this team's MCP servers / cached tools. Attach
	// LazyMCP groups from the request stores so search_tools and main-thread
	// injection see the real catalog (e.g. mcp:email / send_email).
	{
		var pool sandbox.ToolNodePool
		if cm.components != nil {
			pool = cm.components.SandboxPool
		}
		debug := false
		if chatAgent != nil {
			debug = chatAgent.DebugMode
		}
		if groups := buildRequestMCPToolGroups(r, pool, debug); len(groups) > 0 {
			runner.InjectRequestMCPGroups(groups)
		}
	}

	// Per-request A2A agent tools: load from tenant stores and inject as a
	// request-scoped tool group so search_tools / dynamic injection see them.
	if svc := store.FromRequest(r); svc != nil {
		a2aStores := &store.A2AAgentStores{
			Platform: svc.PlatformA2AAgents,
			Org:      svc.A2AAgents,
			Team:     svc.TeamA2AAgents,
		}
		if a2aTools := a2aclient.GetA2AToolsFromStores(r.Context(), a2aStores); len(a2aTools) > 0 {
			a2aGroups := map[string]*agent.ToolGroup{
				"a2a": {
					Name:        "a2a",
					Description: "Remote A2A agent skills (invoke external agents via the A2A protocol)",
					Tools:       a2aTools,
				},
			}
			runner.InjectRequestMCPGroups(a2aGroups)

			// Build a brief listing for the system prompt so the LLM knows
			// A2A agents are available without needing search_tools first.
			var toolNames []string
			for _, t := range a2aTools {
				toolNames = append(toolNames, t.Name())
			}
			runner.InjectA2AToolsPrompt(toolNames)
		}
	}

	// Per-request web search: platform General/WebSearchTool must apply to every
	// user, not only the browser session that first pre-warmed the singleton agent.
	// Also late-bind the Perplexity tool if it was configured after agent init.
	if appCfg := effectiveAppConfig(r); appCfg != nil {
		applyPerRequestWebSearch(runner, chatAgent, appCfg)
	}

	// Inject tenant-scoped network policy stores into the runner context so that
	// denial detection can auto-approve/deny based on configured rules rather
	// than always prompting the user.
	if svc := store.FromRequest(r); svc != nil && (svc.PlatformNetworkPolicies != nil || svc.NetworkPolicies != nil || svc.TeamNetworkPolicies != nil) {
		runner.InjectNetworkPolicyStores(svc.PlatformNetworkPolicies, svc.NetworkPolicies, svc.TeamNetworkPolicies)
	}

	// Inject gateway config for auto-approve (policy-allowed endpoints bypass the dialog).
	if appCfg := effectiveAppConfig(r); appCfg != nil && appCfg.Sandbox.OpenShell.GatewayAddr != "" {
		runner.InjectGatewayConfig(openshell.GRPCClientConfig{
			Addr: appCfg.Sandbox.OpenShell.GatewayAddr,
			TLS:  appCfg.Sandbox.OpenShell.OpenShellGatewayTLS(),
		})
	}

	// Inject team + personal scheduler stores so schedule_job can create
	// personal jobs (default) or team jobs, and list/update across both lanes.
	if svc := store.FromRequest(r); svc != nil {
		if svc.Scheduler != nil {
			runner.InjectSchedulerStore(svc.Scheduler)
		}
		if svc.PersonalScheduler != nil {
			runner.InjectPersonalSchedulerStore(svc.PersonalScheduler)
		}
	}

	// Inject tenant-scoped fleet stores into the runner context so that
	// fleet tools (save_fleet_plan, list_fleets) can read/write from the DB.
	if svc := store.FromRequest(r); svc != nil && (svc.FleetTemplates != nil || svc.FleetPlans != nil) {
		runner.InjectFleetStores(svc.FleetTemplates, svc.FleetPlans)
	}

	// Inject setup stores, including API fallback stores when DB-backed stores
	// are unavailable, so setup tools update the same drafts created by handlers.
	if svc := store.FromRequest(r); svc != nil {
		runner.InjectFleetSetupStores(getSetupProfileStore(svc), getSetupDraftStore(svc))
	}

	// Inject the team's custom sandbox template so that chat containers use
	// the team's pre-configured image rather than always falling back to @base.
	if svc := store.FromRequest(r); svc != nil && svc.Settings != nil {
		if settings, err := svc.Settings.Get(r.Context()); err == nil && settings.TemplateName != "" {
			runner.InjectSandboxTemplate(settings.TemplateName)
			// On K8s, resolve the template name → layer chain via the
			// platform DB so the pod gets a real layer chain (not the
			// human-readable template name which is not a valid layer ID).
			if chain := resolveTemplateLayerChain(r.Context(), settings.TemplateName); len(chain) > 0 {
				runner.InjectSandboxLayerChain(chain)
			}
			// On OpenShell, resolve the template name → custom image ref.
			if img := resolveTemplateImage(r.Context(), settings.TemplateName); img != "" {
				runner.InjectSandboxImage(img)
			}
		}
	}

	// Resolve @base configuration layer. When the admin has run Configure
	// Base Sandbox, the resulting delta layer is stored as @base's
	// top_layer_id. Sessions must include it in their chain so they see
	// the installed tools. Only applies when no team-template chain was
	// already injected (team templates include @base in their own chain).
	if chain := resolveBaseLayerChain(r.Context()); len(chain) > 0 {
		runner.InjectSandboxLayerChainIfEmpty(chain)
	}
	// On OpenShell, resolve @base's custom image if set by admin.
	if img := resolveBaseImage(r.Context()); img != "" {
		runner.InjectSandboxImageIfEmpty(img)
	}

	// Inject per-team disabled tool list so the agent filters them out.
	if svc := store.FromRequest(r); svc != nil && svc.Settings != nil {
		if settings, err := svc.Settings.Get(r.Context()); err == nil && len(settings.DisabledTools) > 0 {
			runner.InjectDisabledTools(settings.DisabledTools)
		}
	}

	// Inject the per-request session service so that sub-agents (delegate_tasks)
	// create child sessions in the correct store (pgstore PersonalSessions in
	// platform mode) rather than the factory-time default (FileStore).
	if svc := store.FromRequest(r); svc != nil && svc.PersonalSessions != nil {
		runner.InjectSessionService(svc.PersonalSessions)
	}

	// Inject the effective user ID so sub-agents create child sessions with the
	// correct user_id (UUID in platform mode). Without this, the SubAgentManager
	// would use its factory-time default ("console_user") which fails pgstore's
	// UUID column constraint.
	runner.InjectUserID(userID)

	// Inject org/team slugs so tools can resolve team membership in platform mode.
	if tc := store.TenantContextFrom(r.Context()); tc != nil {
		runner.InjectTenantSlugs(tc.OrgSlug, tc.TeamSlug)
	}

	// Inject RunJobFunc so schedule_job test execution works in platform mode.
	// Uses the same credential injection as cron for the job's scope so
	// test_first cannot succeed with personal creds then fail on team-only cron.
	if reqSvc := store.FromRequest(r); reqSvc != nil && (reqSvc.Scheduler != nil || reqSvc.PersonalScheduler != nil) {
		runner.InjectRunJobFunc(func(ctx context.Context, jobID string) (string, error) {
			storeJob, _, jobScope := findSchedulerJob(ctx, reqSvc, jobID)
			if storeJob == nil {
				return "", fmt.Errorf("job %q not found", jobID)
			}
			storeJob.Scope = jobScope
			job := storeJobToSchedulerJob(storeJob)
			execCtx := buildSchedulerExecContext(ctx, reqSvc, jobScope, storeJob)

			if exec := GetExecutor(); exec != nil {
				return exec.Execute(execCtx, job)
			}

			cm := GetChatManager()
			if cm.components == nil || cm.components.ChatAgent == nil {
				return "", fmt.Errorf("chat agent not initialized — cannot test-execute job")
			}
			localExec := &scheduler.Executor{
				ChatAgent:      cm.components.ChatAgent,
				SessionService: cm.components.SessionService,
				ProviderName:   cm.components.ProviderName,
				ModelName:      cm.components.ModelName,
				RunHeadless:    GetRunHeadlessFunc(),
				ReadSessionFile: func(_, path string) ([]byte, error) {
					return os.ReadFile(path)
				},
			}
			// Match daemon SetGatewayConfig so SessionBridge can PreSeed/AutoApprove.
			if appCfg := effectiveAppConfig(r); appCfg != nil && appCfg.Sandbox.OpenShell.GatewayAddr != "" {
				cfg := openshell.GRPCClientConfig{
					Addr: appCfg.Sandbox.OpenShell.GatewayAddr,
					TLS:  appCfg.Sandbox.OpenShell.OpenShellGatewayTLS(),
				}
				localExec.GatewayConfig = &cfg
			}
			if appCfg := effectiveAppConfig(r); appCfg != nil {
				cfg := appCfg
				localExec.DestroySandbox = func(ctx context.Context, sessionID string) error {
					return sandbox.DestroySessionEverywhere(ctx, cfg, sessionID, nil)
				}
			}
			if cm.components != nil && cm.components.SandboxPool != nil {
				pool := cm.components.SandboxPool
				localExec.InvalidateSandboxClient = func(sessionID string) {
					pool.Remove(sessionID)
				}
			}
			return localExec.Execute(execCtx, job)
		})
	}

	// If we seeded an app preview, emit it through the runner so the frontend
	// shows the AppPreviewCard immediately (before the LLM responds).
	if seededAppPreview != nil {
		runner.emitEvent("app_preview", seededAppPreview)
	}

	// Per-request LLM resolution: In platform mode, resolve the effective
	// provider/model based on the team → org → platform hierarchy and inject
	// it into the runner context. This ensures each team uses their configured
	// model regardless of what other teams/admins have set.
	if svc := store.FromRequest(r); svc != nil && cm.llmPool != nil {
		appCfg := provider.ResolveEffectiveConfig(r.Context(), svc.PlatformSettings, svc.OrgSettings, svc.Settings)
		var userDefault provider.UserDefaultSettings
		if svc.PersonalSettings != nil {
			if ps, psErr := svc.PersonalSettings.Get(r.Context()); psErr == nil {
				userDefault = ps
			}
		}
		appCfg = provider.ApplyUserDefault(appCfg, userDefault)

		// Resolve per-session pin: request-body override for the first message,
		// otherwise the stored pin (personal-first, team fallback — studio chat
		// sessions live in the personal store).
		pinProvider, pinModel := req.Provider, req.Model
		if isNew && (pinProvider != "" || pinModel != "") {
			if err := persistSessionPin(r, sessionID, pinProvider, pinModel); err != nil {
				slog.Warn("failed to persist pre-chat session pin",
					"session", sessionID, "provider", pinProvider, "model", pinModel, "error", err)
			}
		} else if pinProvider == "" && pinModel == "" && !isNew {
			pinProvider, pinModel = readSessionPin(r, sessionID)
		}
		appCfg = provider.ApplyProviderOverride(appCfg, pinProvider, pinModel)

		if appCfg.General.DefaultProvider != "" {
			resolvedLLM, llmErr := cm.llmPool.Get(r.Context(), appCfg.General.DefaultProvider, appCfg.General.DefaultModel, appCfg)
			if llmErr != nil {
				slog.Warn("per-request LLM resolution failed, using default",
					"provider", appCfg.General.DefaultProvider,
					"model", appCfg.General.DefaultModel,
					"error", llmErr)
			} else {
				runner.InjectLLM(resolvedLLM)
				slog.Debug("per-request LLM injected",
					"provider", appCfg.General.DefaultProvider,
					"model", appCfg.General.DefaultModel)
			}
		}
	}

	registry := getChatRunnerRegistry()
	registry.Register(sessionID, runner)

	// Also register the runner's cancel in the ChatManager so the stop endpoint
	// (POST /api/studio/sessions/{id}/stop) can cancel the background runner.
	cm.registerStream(sessionID, runner.Stop)

	go func() {
		defer registry.Unregister(sessionID)
		defer cm.unregisterStream(sessionID)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("chat runner panic recovered",
					"session", sessionID,
					"panic", fmt.Sprintf("%v", r))
				runner.EmitPanicError(fmt.Sprintf("Internal error: %v", r))
			}
		}()
		runner.Run(chatAgent, sessionService, comp.LLM, titleSetter, userMsg, msg, req.AutoApprove, req.SystemContext, req.PinnedToolGroups, req.PlanMode)
	}()

	// Become an SSE viewer: subscribe to the runner and forward events to the browser.
	streamRunnerEvents(w, flusher, r.Context(), runner)
}

// StudioChatStreamHandler handles GET /api/studio/sessions/{id}/stream.
// It reconnects to an active background chat runner, replaying missed events
// and then streaming live events. If no runner is active, it returns 404.
func StudioChatStreamHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]
	if sessionID == "" {
		respondError(w, http.StatusBadRequest, "session ID required")
		return
	}

	registry := getChatRunnerRegistry()
	runner := registry.Get(sessionID)
	if runner == nil {
		respondError(w, http.StatusNotFound, "no active runner for session")
		return
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

	streamRunnerEvents(w, flusher, r.Context(), runner)
}

// StudioChatStatusHandler handles GET /api/studio/sessions/{id}/status.
// Returns the current state of a session's background runner.
func StudioChatStatusHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]
	if sessionID == "" {
		respondError(w, http.StatusBadRequest, "session ID required")
		return
	}

	registry := getChatRunnerRegistry()
	runner := registry.Get(sessionID)

	resp := map[string]interface{}{
		"sessionId": sessionID,
		"running":   runner != nil && !runner.IsDone(),
	}
	if runner != nil {
		resp["eventCount"] = runner.EventCount()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// streamRunnerEvents subscribes to a ChatRunner and streams events as SSE to the client.
// It first replays buffered history (catch-up), then streams live events.
// Returns when the runner completes or the HTTP client disconnects.
func streamRunnerEvents(w http.ResponseWriter, flusher http.Flusher, httpCtx context.Context, runner *ChatRunner) {
	subscriberID := fmt.Sprintf("sse-%d", time.Now().UnixNano())

	// Subscribe BEFORE reading history to avoid missing events between
	// history read and subscription (same pattern as fleet sessions).
	eventCh := runner.Subscribe(subscriberID)
	defer runner.Unsubscribe(subscriberID)

	// Replay buffered history (catch-up for reconnecting clients)
	seen := make(map[string]bool)
	history := runner.GetHistory()
	for _, event := range history {
		seen[event.ID] = true
		SendSSE(w, flusher, event.Type, event.Data)
	}

	// If the runner is already done and we've replayed all history, we're done.
	if runner.IsDone() {
		return
	}

	// Stream live events
	for {
		select {
		case <-httpCtx.Done():
			// Browser disconnected — the runner keeps going, we just stop streaming.
			return
		case event, ok := <-eventCh:
			if !ok {
				// Channel closed — runner is done
				return
			}
			// Deduplicate events that were in both history and live stream
			if seen[event.ID] {
				continue
			}
			SendSSE(w, flusher, event.Type, event.Data)
		}
	}
}

// handleSlashCommand processes slash commands and sends results as SSE events.
// The *http.Request is required so /status can resolve the per-session model
// (personal pin → user default → team/org/platform cascade) instead of the
// process-global ChatManager.ProviderName/ModelName.
func handleSlashCommand(r *http.Request, w io.Writer, flusher http.Flusher, cm *ChatManager, sessionService session.Service, cmd, userID, sessionID string) {
	ctx := r.Context()
	comp := cm.components
	chatAgent := comp.ChatAgent

	switch {
	case cmd == "/help":
		SendSSE(w, flusher, "system", map[string]interface{}{
			"content": "**Available commands:**\n" +
				"- `/status` — Show current provider, model, tools, and memory status\n" +
				"- `/new` — Start a fresh conversation\n" +
				"- `/compact` — Show context window usage and compaction status\n" +
				"- `/distill` — Distill the last task into a reusable flow\n" +
				"- `/fleet [task]` — Start a fleet session with an autonomous agent team\n" +
				"- `/fleet-plan [hint]` — Create a reusable fleet plan through guided conversation\n" +
				"- `/drill [hint]` — Create a drill suite with guided wizard\n" +
				"- `/drill-add <suite>` — Add new drills to an existing suite\n" +
				"- `/tutorial-drill [hint]` — Create a tutorial (narrated) drill with guided wizard\n" +
				"- `/tutorial-drill-add <suite>` — Add tutorial drills to an existing *tutorial* suite\n" +
				"- `/help` — Show this help message",
		})

	case cmd == "/status":
		// Per-session effective model (same cascade as GET .../model-status).
		// Never read cm.components.ProviderName/ModelName — those are process-global
		// and can reflect another user's last HotSwapLLM.
		effProvider, effModel, pinnedProvider, _, _ := resolveSessionEffectiveModel(r, sessionID)
		if effProvider == "" {
			effProvider = comp.ProviderName
		}
		if effModel == "" {
			effModel = comp.ModelName
		}
		status := fmt.Sprintf("**Status**\n- Provider: `%s`\n- Model: `%s`\n", effProvider, effModel)
		if pinnedProvider != "" {
			status += "- Session pin: active\n"
		}
		if comp.Compactor != nil {
			est, win := comp.Compactor.TokenUsage()
			pct := float64(0)
			if win > 0 {
				pct = float64(est) / float64(win) * 100
			}
			status += fmt.Sprintf("- Context: %d / %d tokens (%.0f%%)\n", est, win, pct)
		}
		status += fmt.Sprintf("- Tools: %d internal\n", comp.InternalToolCount)
		if comp.SandboxEnabled {
			status += "- Sandbox: enabled\n"
		} else {
			status += "- Sandbox: disabled\n"
		}
		if comp.MemoryActive {
			status += "- Memory: active\n"
		} else {
			status += "- Memory: disabled\n"
		}
		if chatAgent.FlowRegistry != nil {
			entries := chatAgent.FlowRegistry.Entries()
			status += fmt.Sprintf("- Flows: %d saved\n", len(entries))
		}
		if sessionID != "" {
			shortID := persistentsession.SafeShortID(sessionID, 16)
			status += fmt.Sprintf("- Session: `%s`", shortID)
		}
		SendSSE(w, flusher, "system", map[string]interface{}{"content": status})

	case cmd == "/compact":
		if comp.Compactor == nil {
			SendSSE(w, flusher, "system", map[string]interface{}{"content": "Compaction is disabled."})
		} else {
			// Studio still uses the shared BeforeModelCallback path (no
			// child-session rewrite). Force the next model call to compact
			// and report current window usage. Code-mode /compact runs
			// immediately via CompactionBackend.Compact.
			comp.Compactor.ForceNextCompaction()
			est, win := comp.Compactor.TokenUsage()
			pct := float64(0)
			if win > 0 {
				pct = float64(est) / float64(win) * 100
			}
			msg := fmt.Sprintf("**Context Window**\n- Tokens: %d / %d (%.0f%%)\n- Threshold: %.0f%%\n- Compactions: %d\n\nCompaction armed for the next model call in this chat.",
				est, win, pct, comp.Compactor.Threshold*100, comp.Compactor.CompactionCount())
			SendSSE(w, flusher, "system", map[string]interface{}{"content": msg})
		}

	case cmd == "/new":
		resp, err := sessionService.Create(ctx, &session.CreateRequest{
			AppName: studioChatAppName,
			UserID:  userID,
		})
		if err != nil {
			SendErrorSSE(w, flusher, fmt.Sprintf("Failed to create session: %v", err))
		} else {
			SendSSE(w, flusher, "new_session", map[string]interface{}{
				"sessionId": resp.Session.ID(),
			})
		}

	case cmd == "/distill":
		// Persist the user's /distill command to the session
		persistSessionMessage(ctx, sessionService, userID, sessionID, "user", "/distill")

		if sessionID == "" {
			responseText := "No active session to distill."
			SendSSE(w, flusher, "text", map[string]interface{}{"text": responseText})
			persistSessionMessage(ctx, sessionService, userID, sessionID, "model", responseText)
		} else {
			// Enrich context with SessionService and FlowStore so that
			// PreviewDistill can reconstruct traces from PG (platform mode)
			// and DistillToReview/SaveDistillReview can save to PG FlowStore.
			distillCtx := ctx
			if svc := store.FromContext(ctx); svc != nil {
				if svc.PersonalSessions != nil {
					distillCtx = store.WithSessionService(distillCtx, svc.PersonalSessions)
				}
				if svc.PersonalFlows != nil || svc.Flows != nil {
					distillCtx = store.WithFlowStore(distillCtx, store.NewCompositeFlowStore(svc.PersonalFlows, svc.Flows))
				}
			}

			ds := agent.DistillSession{
				SessionID: sessionID,
				AppName:   studioChatAppName,
				UserID:    userID,
			}
			// Identify traces and immediately run distillation (no confirmation step)
			_, err := chatAgent.PreviewDistill(distillCtx, ds)
			if err != nil {
				responseText := fmt.Sprintf("Cannot distill: %v", err)
				SendSSE(w, flusher, "text", map[string]interface{}{"text": responseText})
				persistSessionMessage(ctx, sessionService, userID, sessionID, "model", responseText)
			} else {
				// Run distillation and send preview directly
				review, distillErr := chatAgent.DistillToReview(distillCtx, ds, func(text string) {
					SendSSE(w, flusher, "text", map[string]interface{}{"text": text})
				})
				if distillErr != nil {
					errText := fmt.Sprintf("Distillation failed: %v", distillErr)
					SendSSE(w, flusher, "text", map[string]interface{}{"text": errText})
					persistSessionMessage(ctx, sessionService, userID, sessionID, "model", errText)
				} else {
					SendSSE(w, flusher, "distill_preview", map[string]interface{}{
						"yaml":        review.YAML,
						"flowName":    review.FlowName,
						"description": review.Description,
						"tags":        review.Tags,
						"explanation": review.Explanation,
					})
					persistDistillPreview(ctx, sessionService, userID, sessionID, review)

					// Offer to test-run the flow (conversational — like the scheduler workflow)
					if chatAgent.FlowRunner != nil {
						offerText := "\n**Should we run a test before saving?**"
						SendSSE(w, flusher, "text", map[string]interface{}{"text": offerText})
						persistSessionMessage(ctx, sessionService, userID, sessionID, "model", offerText)
					}
				}
			}
		}

	default:
		SendSSE(w, flusher, "system", map[string]interface{}{
			"content": fmt.Sprintf("Unknown command: `%s`. Type `/help` for available commands.", cmd),
		})
	}
	SendSSE(w, flusher, "done", map[string]interface{}{"done": true})
}

// makeLLMFuncFromModel wraps an ADK model.LLM into a simple prompt→response function
// suitable for lightweight classification calls (e.g. app refinement intent).
func makeLLMFuncFromModel(llm model.LLM) func(ctx context.Context, prompt string) (string, error) {
	if llm == nil {
		return nil
	}
	return func(ctx context.Context, prompt string) (string, error) {
		req := &model.LLMRequest{
			Contents: []*genai.Content{
				{
					Parts: []*genai.Part{{Text: prompt}},
					Role:  "user",
				},
			},
		}
		var text string
		for resp, err := range llm.GenerateContent(ctx, req, false) {
			if err != nil {
				return text, err
			}
			if resp.Content != nil {
				for _, p := range resp.Content.Parts {
					if p.Text != "" {
						text += p.Text
					}
				}
			}
		}
		return text, nil
	}
}

// refinementCodeBlockRe matches the fenced code block after "### Current Source Code"
// in the system context injected by the "Improve with AI" flow.
var refinementCodeBlockRe = regexp.MustCompile("(?s)### Current Source Code\\s*```jsx\\s*\\n(.+?)\\n```")

// extractAppFromSystemContext detects the "## Active App Refinement" marker
// in the system context and extracts the app's current code and title.
// Returns ("", "") if the system context does not contain a refinement payload.
func extractAppFromSystemContext(systemContext string) (code, title string) {
	if !strings.Contains(systemContext, "## Active App Refinement") {
		return "", ""
	}
	matches := refinementCodeBlockRe.FindStringSubmatch(systemContext)
	if len(matches) < 2 {
		return "", ""
	}
	code = strings.TrimSpace(matches[1])
	if code == "" {
		return "", ""
	}
	title = extractComponentTitle(code)
	return code, title
}

// buildMergedSkillIndex overlays platform, org, and team DB skills on the
// filesystem base. Names are matched case-insensitively and later scopes win.
func buildMergedSkillIndex(ctx context.Context, filesystem []skills.Skill, platformStore, orgStore, teamStore store.SkillStore) string {
	all := append([]skills.Skill(nil), filesystem...)

	for _, tier := range []struct {
		name  string
		store store.SkillStore
	}{
		{name: "platform", store: platformStore},
		{name: "org", store: orgStore},
		{name: "team", store: teamStore},
	} {
		if tier.store == nil {
			continue
		}
		stored, err := tier.store.LoadAll(ctx)
		if err != nil {
			continue
		}
		for _, s := range stored {
			all = append(all, skills.Skill{
				Name:        s.Name,
				Description: s.Description,
				OS:          s.OS,
				RequireBins: s.RequireBins,
				RequireEnv:  s.RequireEnv,
				Source:      tier.name,
			})
		}
	}

	seen := make(map[string]struct{}, len(all))
	deduped := make([]skills.Skill, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		name := strings.ToLower(strings.TrimSpace(all[i].Name))
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		deduped = append(deduped, all[i])
	}
	for i, j := 0, len(deduped)-1; i < j; i, j = i+1, j-1 {
		deduped[i], deduped[j] = deduped[j], deduped[i]
	}
	return skills.BuildSkillIndex(deduped)
}

// readSessionPin returns the persisted provider/model pin for a session, or
// empty strings when unpinned/unavailable. Personal-first, team fallback —
// mirrors readAppPin. Studio chat sessions live in the personal store; fleet
// sub-sessions live in the team store.
func readSessionPin(r *http.Request, sessionID string) (pinnedProvider, pinnedModel string) {
	svc := store.FromRequest(r)
	if svc == nil || svc.TenantRouter == nil {
		return "", ""
	}
	pu := GetPlatformUser(r)
	if pu == nil || pu.OrgSlug == "" {
		return "", ""
	}
	orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
	if err != nil || orgStore == nil {
		return "", ""
	}
	if pu.ID != "" {
		if personalStore := orgStore.ForUser(pu.ID); personalStore != nil {
			if pin, perr := personalStore.SessionPin(r.Context(), sessionID); perr == nil && pin != nil {
				return pin.Provider, pin.Model
			}
		}
	}
	if pu.TeamSlug != "" {
		if teamStore := orgStore.ForTeam(pu.TeamSlug); teamStore != nil {
			if pin, terr := teamStore.SessionPin(r.Context(), sessionID); terr == nil && pin != nil {
				return pin.Provider, pin.Model
			}
		}
	}
	return "", ""
}

// persistSessionPin writes a provider/model pin to the store that owns the
// session (personal-first via PersonalSessions meta, then team Sessions,
// then probe SessionPin). Returns nil when persistence is skipped (no tenant
// context). Returns an error when the session cannot be found or the write fails.
func persistSessionPin(r *http.Request, sessionID, provider, model string) error {
	svc := store.FromRequest(r)
	if svc == nil || svc.TenantRouter == nil {
		return nil
	}
	pu := GetPlatformUser(r)
	if pu == nil || pu.OrgSlug == "" {
		return nil
	}
	orgStore, err := svc.TenantRouter.ForOrg(pu.OrgSlug)
	if err != nil || orgStore == nil {
		return fmt.Errorf("tenant not accessible")
	}

	scope := ""
	if svc.PersonalSessions != nil {
		if meta, mErr := svc.PersonalSessions.GetSessionMeta(r.Context(), sessionID); mErr == nil && meta != nil {
			scope = "personal"
		}
	}
	if scope == "" && svc.Sessions != nil {
		if meta, mErr := svc.Sessions.GetSessionMeta(r.Context(), sessionID); mErr == nil && meta != nil {
			scope = "team"
		}
	}
	// Probe pin stores when session meta isn't wired (unit tests / edge cases).
	if scope == "" && pu.ID != "" {
		if personalStore := orgStore.ForUser(pu.ID); personalStore != nil {
			if _, pErr := personalStore.SessionPin(r.Context(), sessionID); pErr == nil {
				scope = "personal"
			}
		}
	}
	if scope == "" && pu.TeamSlug != "" {
		if teamStore := orgStore.ForTeam(pu.TeamSlug); teamStore != nil {
			if _, tErr := teamStore.SessionPin(r.Context(), sessionID); tErr == nil {
				scope = "team"
			}
		}
	}
	// Brand-new studio chat sessions: PersonalSessions owns them even before
	// meta probes succeed in some race windows — prefer personal when the
	// user context is present and PersonalSessions is wired.
	if scope == "" && svc.PersonalSessions != nil && pu.ID != "" && orgStore.ForUser(pu.ID) != nil {
		scope = "personal"
	}
	if scope == "" && pu.TeamSlug != "" && orgStore.ForTeam(pu.TeamSlug) != nil {
		scope = "team"
	}

	var setErr error
	switch scope {
	case "personal":
		setErr = orgStore.ForUser(pu.ID).SetSessionPin(r.Context(), sessionID, provider, model)
	case "team":
		setErr = orgStore.ForTeam(pu.TeamSlug).SetSessionPin(r.Context(), sessionID, provider, model)
	default:
		return fmt.Errorf("session %q not found", sessionID)
	}
	return setErr
}

// sessionProviderHasCredential reports whether a provider can be selected in
// the model picker. Checks team credentials, personal credentials, and
// embedded secrets in the resolved provider config (api_key etc.).
func sessionProviderHasCredential(ctx context.Context, svc *store.Services, name string, pCfg config.ProviderConfig) bool {
	if svc == nil {
		return true
	}
	hasCredStore := svc.Credentials != nil || svc.PersonalCredentials != nil
	if !hasCredStore {
		return true
	}
	if svc.Credentials != nil && svc.Credentials.GetSecret(ctx, name) != "" {
		return true
	}
	if svc.PersonalCredentials != nil && svc.PersonalCredentials.GetSecret(ctx, name) != "" {
		return true
	}
	for _, key := range []string{"api_key", "apiKey", "token", "access_token"} {
		if v := pCfg[key]; v != "" {
			return true
		}
	}
	return false
}

// applyPerRequestWebSearch publishes the cascade-selected web tools into the
// runner prompt and ensures the Perplexity tool is registered when selected.
// This closes the gap where a singleton ChatAgent pre-warmed before platform
// web settings were saved (or with a different tenant context) would leave
// some browser sessions without web search while others worked.
func applyPerRequestWebSearch(runner *ChatRunner, _ *agent.ChatAgent, appCfg *config.AppConfig) {
	if runner == nil || appCfg == nil {
		return
	}

	searchOK, searchServer, searchTool := IsWebSearchConfiguredWith(appCfg)
	extractOK, extractServer, extractTool := IsWebExtractConfiguredWith(appCfg)

	searchName := ""
	searchAvailable := false
	if searchOK && searchTool != "" {
		searchTool = strings.ReplaceAll(searchTool, "-", "_")
		if searchServer == "perplexity" {
			if appCfg.PerplexityWebSearch.Provider != "" && appCfg.PerplexityWebSearch.Model != "" {
				searchAvailable = true
				searchName = searchTool
			}
		} else if searchServer != "" {
			// MCP standard web search (Tavily/Brave/Firecrawl): advertise when
			// selected. Tool presence is handled by MCP merge/filter + ToolIndex.
			searchAvailable = true
			searchName = searchTool
		}
	}

	extractName := ""
	extractAvailable := false
	if extractOK && extractTool != "" {
		extractAvailable = true
		extractName = strings.ReplaceAll(extractTool, "-", "_")
		_ = extractServer
	}

	runner.InjectWebSearchPrompt(searchAvailable, searchName, extractAvailable, extractName)

	// Perplexity is request-scoped because the singleton serves multiple tenants.
	if searchAvailable && searchServer == "perplexity" {
		if t, err := tools.NewPerplexityWebSearchTool(appCfg, provider.GetProvider); err != nil {
			slog.Warn("failed to create request-scoped perplexity_web_search tool", "error", err)
		} else {
			runner.InjectRequestTools(t)
		}
	}
}

// resolveSessionEffectiveModel returns the effective provider/model for a
// studio chat session after the full cascade (platform → org → team →
// user-default → session-pin). Empty sessionID skips the pin lookup.
// Shared by GET .../model-status and the /status slash command so both
// surfaces agree with the per-request InjectLLM path.
func resolveSessionEffectiveModel(r *http.Request, sessionID string) (effectiveProvider, effectiveModel, pinnedProvider, pinnedModel string, providers map[string]config.ProviderConfig) {
	svc := store.FromRequest(r)
	ctx := r.Context()
	if sessionID != "" {
		pinnedProvider, pinnedModel = readSessionPin(r, sessionID)
	}
	var resolved = &config.AppConfig{}
	if svc != nil {
		resolved = provider.ResolveEffectiveConfig(ctx, svc.PlatformSettings, svc.OrgSettings, svc.Settings)
	}
	var userDefault provider.UserDefaultSettings
	if svc != nil && svc.PersonalSettings != nil {
		if ps, psErr := svc.PersonalSettings.Get(ctx); psErr == nil {
			userDefault = ps
		}
	}
	resolved = provider.ApplyUserDefault(resolved, userDefault)
	resolved = provider.ApplyProviderOverride(resolved, pinnedProvider, pinnedModel)
	return resolved.General.DefaultProvider, resolved.General.DefaultModel, pinnedProvider, pinnedModel, resolved.Providers
}

// SessionModelStatusResponse is the payload for GET /api/studio/sessions/{id}/model-status.
//
// It reports the current per-session pin (if any), the effective provider/model
// after the full cascade (platform → org → team → user-default → app-pin →
// session-pin) is applied, whether a credential exists for the pinned provider
// (soft-fallback signal — see DECISION-3 in the per-chat-app-model-pin notepad),
// and the list of providers that have credentials on the current team.
type SessionModelStatusResponse struct {
	PinnedProvider       string   `json:"pinnedProvider"`
	PinnedModel          string   `json:"pinnedModel"`
	EffectiveProvider    string   `json:"effectiveProvider"`
	EffectiveModel       string   `json:"effectiveModel"`
	CredentialsAvailable bool     `json:"credentialsAvailable"`
	AvailableProviders   []string `json:"availableProviders"`
}

// GetSessionModelStatusHandler handles GET /api/studio/sessions/{id}/model-status.
//
// Returns the current session's pin state, the effective provider/model after the
// overlay chain runs (see provider.ApplyProviderOverride), whether credentials
// exist for the pinned provider (soft-fallback signal — no error when the pin
// references a provider without a credential), and the list of provider names
// visible to the current team (the resolved cfg.Providers map).
//
// Credential handling never exposes values — only the boolean
// `credentialsAvailable` is returned. In personal mode (no TenantRouter, no
// authenticated platform user) the pin fields are empty and the effective values
// come from the resolved cascade only.
func GetSessionModelStatusHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]
	if sessionID == "" {
		respondError(w, http.StatusBadRequest, "session ID required")
		return
	}

	svc := store.FromRequest(r)

	effectiveProvider, effectiveModel, pinnedProvider, pinnedModel, resolvedProviders := resolveSessionEffectiveModel(r, sessionID)

	// Credential check: soft fallback. Unpinned = healthy (true). Pinned = true
	// only when a credential (team, personal, or embedded) exists for the pin.
	credentialsAvailable := true
	if pinnedProvider != "" {
		pCfg := resolvedProviders[pinnedProvider]
		credentialsAvailable = sessionProviderHasCredential(r.Context(), svc, pinnedProvider, pCfg)
	}

	// Available providers = all resolved cfg.Providers names (same set as
	// GET /api/settings/providers/effective). Credential presence is reported
	// separately via credentialsAvailable so the picker stays in sync with the
	// pre-chat list. Sorted lexicographically for stable JSON.
	names := make([]string, 0, len(resolvedProviders))
	for name := range resolvedProviders {
		names = append(names, name)
	}
	sort.Strings(names)
	respondJSON(w, http.StatusOK, SessionModelStatusResponse{
		PinnedProvider:       pinnedProvider,
		PinnedModel:          pinnedModel,
		EffectiveProvider:    effectiveProvider,
		EffectiveModel:       effectiveModel,
		CredentialsAvailable: credentialsAvailable,
		AvailableProviders:   names,
	})
}

// PatchSessionModelRequest is the payload for PATCH /api/studio/sessions/{id}/model.
//
// Empty strings clear the pin for that field (both empty ⇒ full clear, cascade
// falls back to user-default → team → org → platform).
type PatchSessionModelRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// PatchSessionModelResponse mirrors SessionModelStatusResponse minus the
// availableProviders list (the SPA already has that from GET).
type PatchSessionModelResponse struct {
	PinnedProvider       string `json:"pinnedProvider"`
	PinnedModel          string `json:"pinnedModel"`
	EffectiveProvider    string `json:"effectiveProvider"`
	EffectiveModel       string `json:"effectiveModel"`
	CredentialsAvailable bool   `json:"credentialsAvailable"`
}

// PatchSessionModelHandler handles PATCH /api/studio/sessions/{id}/model.
//
// Persists the per-session model pin (via TenantRouter → team store) and, in
// Studio mode, hot-swaps the live LLM on the singleton ChatManager. When a
// runner is currently streaming for this session, a `model_changed` SSE event
// is emitted so the SPA can refresh its status badge without polling.
//
// Missing-credential handling (DECISION-3): if the target provider has no
// credential, the pin still persists and HTTP 200 is returned with
// `credentialsAvailable:false`. The hot-swap is skipped so the current LLM
// keeps serving requests. Callers surface a soft banner rather than an error.
//
// Personal mode: when no TenantRouter/platform user is present, pin
// persistence is skipped silently (personal SQLite has no team-scoped pin
// store wired here); the hot-swap still runs so `astonish chat` can flip
// models mid-session.
//
// Contract references:
//   - .omo/notepads/per-chat-app-model-pin/decisions.md DECISION-3, DECISION-4
//   - docs/architecture/chat-rendering-pipeline.md — `model_changed` event
//   - pkg/api/AGENTS.md — tenant isolation, SSE event registration
func PatchSessionModelHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]
	if sessionID == "" || !validSessionID.MatchString(sessionID) {
		respondError(w, http.StatusBadRequest, "invalid session ID")
		return
	}

	var req PatchSessionModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Normalize (defense-in-depth against accidental whitespace).
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)

	svc := store.FromRequest(r)
	ctx := r.Context()

	// Persist the pin on the store that owns the session (personal-first for
	// studio chat, team for fleet). Personal mode (no tenant context) skips
	// silently — the hot-swap below still runs.
	if err := persistSessionPin(r, sessionID, req.Provider, req.Model); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			respondError(w, http.StatusNotFound, "session not found")
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "tenant not accessible") {
			respondError(w, http.StatusForbidden, "tenant not accessible")
			return
		}
		slog.Warn("[patch-model] SetSessionPin failed",
			"session", sessionID, "error", err)
		respondError(w, http.StatusInternalServerError, "failed to persist pin")
		return
	}

	// Attempt live hot-swap. DECISION-4 says Studio does true swap; a missing
	// credential is a soft error (pin persisted, swap skipped, HTTP 200).
	credentialsAvailable := true
	if req.Provider != "" || req.Model != "" {
		if err := GetChatManager().HotSwapLLM(ctx, req.Provider, req.Model); err != nil {
			if strings.Contains(err.Error(), "credential") {
				credentialsAvailable = false
				slog.Info("[patch-model] pin persisted, swap skipped (no credential)",
					"session", sessionID, "provider", req.Provider, "model", req.Model)
			} else {
				slog.Warn("[patch-model] hot-swap failed (non-fatal; next request re-inits)",
					"session", sessionID, "error", err)
			}
		}
	}

	// Recompute the effective cascade to return the post-pin resolution.
	var resolved = &config.AppConfig{}
	if svc != nil {
		resolved = provider.ResolveEffectiveConfig(ctx, svc.PlatformSettings, svc.OrgSettings, svc.Settings)
	}
	var patchUserDefault provider.UserDefaultSettings
	if svc != nil && svc.PersonalSettings != nil {
		if ps, psErr := svc.PersonalSettings.Get(ctx); psErr == nil {
			patchUserDefault = ps
		}
	}
	resolved = provider.ApplyUserDefault(resolved, patchUserDefault)
	resolved = provider.ApplyProviderOverride(resolved, req.Provider, req.Model)
	effectiveProvider := resolved.General.DefaultProvider
	effectiveModel := resolved.General.DefaultModel

	// Soft-fallback credential signal for the response (HotSwap may already
	// have set credentialsAvailable=false; also check stores/config).
	if credentialsAvailable && req.Provider != "" {
		pCfg := resolved.Providers[req.Provider]
		if !sessionProviderHasCredential(ctx, svc, req.Provider, pCfg) {
			credentialsAvailable = false
		}
	}

	// Emit SSE `model_changed` to the live runner (if any). Guarded: no
	// runner ⇒ silently skip (not every session has an active stream).
	if runner := getChatRunnerRegistry().Get(sessionID); runner != nil {
		runner.emitEvent("model_changed", map[string]any{
			"sessionId":            sessionID,
			"pinnedProvider":       req.Provider,
			"pinnedModel":          req.Model,
			"effectiveProvider":    effectiveProvider,
			"effectiveModel":       effectiveModel,
			"credentialsAvailable": credentialsAvailable,
		})
	}

	respondJSON(w, http.StatusOK, PatchSessionModelResponse{
		PinnedProvider:       req.Provider,
		PinnedModel:          req.Model,
		EffectiveProvider:    effectiveProvider,
		EffectiveModel:       effectiveModel,
		CredentialsAvailable: credentialsAvailable,
	})
}
