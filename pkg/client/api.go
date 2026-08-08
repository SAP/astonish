package client

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// --- Session API ---

// SessionMeta represents a chat session summary.
type SessionMeta struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	MessageCount int    `json:"messageCount"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
	ParentID     string `json:"parentId,omitempty"`
	FleetKey     string `json:"fleetKey,omitempty"`
	FleetName    string `json:"fleetName,omitempty"`
}

// ListSessions returns all chat sessions.
func (c *Client) ListSessions() ([]SessionMeta, error) {
	var sessions []SessionMeta
	if err := c.DoJSON("GET", "/api/studio/sessions", nil, &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

// GetSession returns the details of a single session (untyped map).
func (c *Client) GetSession(id string) (map[string]any, error) {
	var detail map[string]any
	if err := c.DoJSON("GET", fmt.Sprintf("/api/studio/sessions/%s", id), nil, &detail); err != nil {
		return nil, err
	}
	return detail, nil
}

// StudioMessage is a simplified chat message from GET /api/studio/sessions/{id}.
// Mirrors pkg/api.StudioMessage fields needed by the terminal app.
type StudioMessage struct {
	Type       string `json:"type"`
	Content    string `json:"content,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	ToolArgs   any    `json:"toolArgs,omitempty"`
	ToolResult any    `json:"toolResult,omitempty"`
}

// UsageSummary is cumulative token usage from a Studio session detail response.
type UsageSummary struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	TotalTokens  int64 `json:"totalTokens"`
}

// SessionDetail is the typed session payload used by the TUI resume path.
type SessionDetail struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	MessageCount int             `json:"messageCount"`
	CreatedAt    string          `json:"createdAt"`
	UpdatedAt    string          `json:"updatedAt"`
	Messages     []StudioMessage `json:"messages"`
	Artifacts    []ArtifactInfo  `json:"artifacts,omitempty"`
	TotalUsage   *UsageSummary   `json:"totalUsage,omitempty"`
}

// ArtifactInfo describes a generated file returned by the Studio session API.
type ArtifactInfo struct {
	Path        string `json:"path"`
	FileName    string `json:"fileName"`
	FileType    string `json:"fileType"`
	ToolName    string `json:"toolName"`
	IsReport    bool   `json:"isReport,omitempty"`
	ReportTitle string `json:"reportTitle,omitempty"`
}

// GetSessionDetail returns typed session history for the terminal chat app.
func (c *Client) GetSessionDetail(id string) (*SessionDetail, error) {
	var detail SessionDetail
	if err := c.DoJSON("GET", fmt.Sprintf("/api/studio/sessions/%s", id), nil, &detail); err != nil {
		return nil, err
	}
	if detail.ID == "" {
		detail.ID = id
	}
	return &detail, nil
}

// TraceOpts holds query parameters for the session trace endpoint.
type TraceOpts struct {
	Recursive bool
	ToolsOnly bool
	Verbose   bool
	LastN     int
}

// GetSessionTrace returns the chronological trace of a session.
func (c *Client) GetSessionTrace(id string, opts TraceOpts) (map[string]any, error) {
	path := fmt.Sprintf("/api/studio/sessions/%s/trace?", id)
	params := ""
	if opts.Recursive {
		params += "recursive=true&"
	}
	if opts.ToolsOnly {
		params += "tools_only=true&"
	}
	if opts.Verbose {
		params += "verbose=true&"
	}
	if opts.LastN > 0 {
		params += fmt.Sprintf("last_n=%d&", opts.LastN)
	}
	// Trim trailing & or ?
	url := path + params
	if url[len(url)-1] == '&' || url[len(url)-1] == '?' {
		url = url[:len(url)-1]
	}

	var trace map[string]any
	if err := c.DoJSON("GET", url, nil, &trace); err != nil {
		return nil, err
	}
	return trace, nil
}

// DeleteSession deletes a chat session.
func (c *Client) DeleteSession(id string) error {
	resp, err := c.Do("DELETE", fmt.Sprintf("/api/studio/sessions/%s", id), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseErrorResponse(resp)
	}
	return nil
}

// --- Flow API ---

// FlowMeta represents a flow/agent summary.
type FlowMeta struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`
	Scope       string `json:"scope,omitempty"`
}

// ListFlows returns all available flows/agents.
func (c *Client) ListFlows() ([]FlowMeta, error) {
	var resp struct {
		Agents []FlowMeta `json:"agents"`
	}
	if err := c.DoJSON("GET", "/api/agents", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Agents, nil
}

// RunFlow starts a headless flow execution and returns an SSE stream.
func (c *Client) RunFlow(flowName string, params map[string]string) (*SSEStream, error) {
	body := map[string]any{}
	if len(params) > 0 {
		body["params"] = params
	}
	return c.SSE("POST", fmt.Sprintf("/api/agents/%s/run", flowName), body)
}

// --- Flow Interactive API ---

// FlowChatRequest represents a message to send to a specific flow agent via /api/chat.
type FlowChatRequest struct {
	AgentID     string `json:"agentId"`
	SessionID   string `json:"sessionId,omitempty"`
	Message     string `json:"message"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	AutoApprove bool   `json:"autoApprove,omitempty"`
	CLIMode     bool   `json:"cliMode,omitempty"`
	Debug       bool   `json:"debug,omitempty"`
}

// SendFlowMessage sends a message to a flow execution session and returns an SSE stream.
// This is used for interactive multi-turn flow execution via POST /api/chat.
func (c *Client) SendFlowMessage(req *FlowChatRequest) (*SSEStream, error) {
	return c.SSE("POST", "/api/chat", req)
}

// --- Chat API ---

// EffectiveProvidersResponse contains the merged provider defaults used by Studio.
type EffectiveProvidersResponse struct {
	DefaultProvider string                       `json:"default_provider"`
	DefaultModel    string                       `json:"default_model"`
	Providers       map[string]map[string]string `json:"providers,omitempty"`
	// ProviderSources tags each provider by origin ("local" for config.yaml,
	// "platform" for the DB cascade) and whether it is read-only. Optional;
	// absent on older servers.
	ProviderSources map[string]ProviderSource `json:"provider_sources,omitempty"`
}

// ProviderSource describes the origin of a provider instance in the effective view.
type ProviderSource struct {
	Source   string `json:"source"`
	ReadOnly bool   `json:"read_only"`
}

// GetEffectiveProviders returns the platform-resolved provider defaults for the
// current org/team context.
func (c *Client) GetEffectiveProviders() (*EffectiveProvidersResponse, error) {
	var resp EffectiveProvidersResponse
	if err := c.DoJSON("GET", "/api/settings/providers/effective", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ProviderModelsResponse is returned by GET /api/providers/{id}/models.
type ProviderModelsResponse struct {
	Provider string   `json:"provider"`
	Models   []string `json:"models"`
}

// ListProviderModels returns model IDs available for a configured provider instance.
func (c *Client) ListProviderModels(providerID string) ([]string, error) {
	var resp ProviderModelsResponse
	path := fmt.Sprintf("/api/providers/%s/models", url.PathEscape(providerID))
	if err := c.DoJSON("GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Models, nil
}

// ChatAttachment is a base64 file payload for multimodal chat turns.
type ChatAttachment struct {
	Filename string `json:"filename"`
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // base64-encoded file content
}

// ChatRequest represents a message to send to the chat.
type ChatRequest struct {
	SessionID     string           `json:"sessionId,omitempty"`
	Message       string           `json:"message"`
	AutoApprove   bool             `json:"autoApprove,omitempty"`
	Debug         bool             `json:"debug,omitempty"`
	SystemContext string           `json:"systemContext,omitempty"`
	PlanMode      bool             `json:"planMode,omitempty"`
	Attachments   []ChatAttachment `json:"attachments,omitempty"`
	// Provider/Model pin a model for this turn (and new sessions).
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// SendChatMessage sends a chat message and returns an SSE stream of events.
func (c *Client) SendChatMessage(req *ChatRequest) (*SSEStream, error) {
	return c.SSE("POST", "/api/studio/chat", req)
}

// GetArtifactContent returns the plain-text content for a generated file artifact.
func (c *Client) GetArtifactContent(path, sessionID string) (string, error) {
	params := url.Values{}
	params.Set("path", path)
	if sessionID != "" {
		params.Set("session", sessionID)
	}
	resp, err := c.Do("GET", "/api/studio/artifacts/content?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", parseErrorResponse(resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read artifact content: %w", err)
	}
	return string(data), nil
}

// NetworkDenial describes a blocked outbound connection returned by Studio.
type NetworkDenial struct {
	ChunkID        string `json:"chunk_id"`
	Host           string `json:"host"`
	Port           uint32 `json:"port"`
	Binary         string `json:"binary,omitempty"`
	Rationale      string `json:"rationale,omitempty"`
	SecurityNotes  string `json:"security_notes,omitempty"`
	BroaderPattern string `json:"broader_pattern,omitempty"`
}

// NetworkDenialsResponse is returned by the network-denials polling endpoint.
type NetworkDenialsResponse struct {
	Denials     []NetworkDenial `json:"denials"`
	SandboxName string          `json:"sandbox_name"`
	Error       string          `json:"error,omitempty"`
}

// GetNetworkDenials returns pending network grants for a session.
func (c *Client) GetNetworkDenials(sessionID string) (*NetworkDenialsResponse, error) {
	var resp NetworkDenialsResponse
	path := fmt.Sprintf("/api/studio/sessions/%s/network-denials", url.PathEscape(sessionID))
	if err := c.DoJSON("GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ApproveNetworkGrant approves a specific draft policy chunk.
func (c *Client) ApproveNetworkGrant(sessionID, chunkID, sandboxName string) error {
	body := map[string]string{"chunk_id": chunkID, "sandbox_name": sandboxName}
	path := fmt.Sprintf("/api/studio/sessions/%s/network-grants/approve", url.PathEscape(sessionID))
	return c.DoJSON("POST", path, body, nil)
}

// ApproveNetworkGrantBroader approves a host/port pattern for the session sandbox.
func (c *Client) ApproveNetworkGrantBroader(sessionID, host string, port uint32, sandboxName string) error {
	body := map[string]any{"host": host, "port": port, "sandbox_name": sandboxName}
	path := fmt.Sprintf("/api/studio/sessions/%s/network-grants/approve-broader", url.PathEscape(sessionID))
	return c.DoJSON("POST", path, body, nil)
}

// DenyNetworkGrant rejects a draft policy chunk. Empty chunk IDs are accepted
// by the server for stdout-derived denials and simply acknowledge the prompt.
func (c *Client) DenyNetworkGrant(sessionID, chunkID, sandboxName, reason string) error {
	body := map[string]string{"chunk_id": chunkID, "sandbox_name": sandboxName}
	if reason != "" {
		body["reason"] = reason
	}
	path := fmt.Sprintf("/api/studio/sessions/%s/network-grants/deny", url.PathEscape(sessionID))
	return c.DoJSON("POST", path, body, nil)
}

// GetSessionStatus checks if a session has an active runner.
func (c *Client) GetSessionStatus(sessionID string) (bool, error) {
	var status struct {
		Running bool `json:"running"`
	}
	if err := c.DoJSON("GET", fmt.Sprintf("/api/studio/sessions/%s/status", sessionID), nil, &status); err != nil {
		return false, err
	}
	return status.Running, nil
}

// ReconnectSession connects to an active session's SSE stream.
func (c *Client) ReconnectSession(sessionID string) (*SSEStream, error) {
	return c.SSE("GET", fmt.Sprintf("/api/studio/sessions/%s/stream", sessionID), nil)
}

// SessionModelStatusResponse mirrors the server response for
// GET /api/studio/sessions/{id}/model-status.
type SessionModelStatusResponse struct {
	PinnedProvider       string   `json:"pinnedProvider"`
	PinnedModel          string   `json:"pinnedModel"`
	EffectiveProvider    string   `json:"effectiveProvider"`
	EffectiveModel       string   `json:"effectiveModel"`
	CredentialsAvailable bool     `json:"credentialsAvailable"`
	AvailableProviders   []string `json:"availableProviders,omitempty"`
}

// PatchSessionModelResponse mirrors the server response for
// PATCH /api/studio/sessions/{id}/model.
type PatchSessionModelResponse struct {
	PinnedProvider       string `json:"pinnedProvider"`
	PinnedModel          string `json:"pinnedModel"`
	EffectiveProvider    string `json:"effectiveProvider"`
	EffectiveModel       string `json:"effectiveModel"`
	CredentialsAvailable bool   `json:"credentialsAvailable"`
}

// GetSessionModelStatus reads the resolved provider/model for a session,
// including cascade defaults when no explicit pin is set.
func (c *Client) GetSessionModelStatus(sessionID string) (*SessionModelStatusResponse, error) {
	var resp SessionModelStatusResponse
	path := fmt.Sprintf("/api/studio/sessions/%s/model-status", url.PathEscape(sessionID))
	if err := c.DoJSON("GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// PatchSessionModel sets or clears the per-session model pin on the remote
// server. Empty strings for both provider and model clear the pin (cascade
// falls back to user-default → team → org → platform).
func (c *Client) PatchSessionModel(sessionID, provider, model string) (*PatchSessionModelResponse, error) {
	body := map[string]string{"provider": provider, "model": model}
	var resp PatchSessionModelResponse
	path := fmt.Sprintf("/api/studio/sessions/%s/model", sessionID)
	if err := c.DoJSON("PATCH", path, body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// StopSession stops a running session.
func (c *Client) StopSession(sessionID string) error {
	resp, err := c.Do("POST", fmt.Sprintf("/api/studio/sessions/%s/stop", sessionID), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseErrorResponse(resp)
	}
	return nil
}

// --- Org/Team API ---

// OrgInfo represents an organization.
type OrgInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Role string `json:"role"`
}

// TeamInfo represents a team.
type TeamInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// ListOrgs returns the organizations the user belongs to.
func (c *Client) ListOrgs() ([]OrgInfo, error) {
	var resp struct {
		Orgs      []OrgInfo `json:"orgs"`
		ActiveOrg string    `json:"active_org"`
	}
	if err := c.DoJSON("GET", "/api/orgs", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Orgs, nil
}

// ListTeams returns the teams in the current org.
func (c *Client) ListTeams() ([]TeamInfo, error) {
	var resp struct {
		Teams []TeamInfo `json:"teams"`
	}
	if err := c.DoJSON("GET", "/api/teams", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Teams, nil
}

// SwitchOrg changes the active org by re-authenticating.
func (c *Client) SwitchOrg(orgSlug string) error {
	resp, err := c.Do("POST", "/api/auth/switch-org", map[string]string{"org": orgSlug})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseErrorResponse(resp)
	}
	return nil
}

// --- Scheduler API ---

// SchedulerJob represents a scheduled job.
type SchedulerJob struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Enabled  bool   `json:"enabled"`
	FlowName string `json:"flow_name"`
}

// ListSchedulerJobs returns all scheduler jobs.
func (c *Client) ListSchedulerJobs() ([]SchedulerJob, error) {
	var jobs []SchedulerJob
	if err := c.DoJSON("GET", "/api/scheduler/jobs", nil, &jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

// --- Fleet API ---

// FleetPlan represents a fleet plan.
type FleetPlan struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// ListFleetPlans returns all fleet plans.
func (c *Client) ListFleetPlans() ([]FleetPlan, error) {
	var plans []FleetPlan
	if err := c.DoJSON("GET", "/api/fleet-plans", nil, &plans); err != nil {
		return nil, err
	}
	return plans, nil
}

// --- Drill API ---

// DrillSuite represents a drill test suite.
type DrillSuite struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ListDrillSuites returns all drill suites.
func (c *Client) ListDrillSuites() ([]DrillSuite, error) {
	var suites []DrillSuite
	if err := c.DoJSON("GET", "/api/drills", nil, &suites); err != nil {
		return nil, err
	}
	return suites, nil
}

// InstallSkillResponse matches the server response from POST /api/skills/install
type InstallSkillResponse struct {
	Status           string `json:"status"`
	Name             string `json:"name"`
	Scope            string `json:"scope"`
	FilesSaved       int    `json:"files_saved"`
	Version          string `json:"version,omitempty"`
	Description      string `json:"description,omitempty"`
	ValidationStatus string `json:"validation_status,omitempty"`
}

// InstallSkill tells the remote server to download and install a skill from ClawHub
// into the user's current team (or org) scope. The server performs the download.
func (c *Client) InstallSkill(input string) (*InstallSkillResponse, error) {
	body := map[string]string{"input": input}
	var resp InstallSkillResponse
	if err := c.DoJSON("POST", "/api/skills/install", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SkillInfo is a minimal view for CLI listing.
type SkillInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Source      string   `json:"source"`
	Scope       string   `json:"scope,omitempty"`
	Eligible    bool     `json:"eligible"`
	Missing     []string `json:"missing,omitempty"`
	Editable    bool     `json:"editable"`
}

// ListSkillsResponse from GET /api/skills
type ListSkillsResponse struct {
	Skills      []SkillInfo `json:"skills"`
	IsTeamAdmin bool        `json:"is_team_admin"`
	IsOrgAdmin  bool        `json:"is_org_admin"`
}

// ListSkills fetches the merged list of skills visible to the current user
// (platform + org + team).
func (c *Client) ListSkills() (*ListSkillsResponse, error) {
	var resp ListSkillsResponse
	if err := c.DoJSON("GET", "/api/skills", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SkillContentResponse mirrors the server response for a skill's full content.
type SkillContentResponse struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Content     string `json:"content"`
	RawFile     string `json:"raw_file"`
	Editable    bool   `json:"editable"`
}

// GetSkillContent fetches the full SKILL.md (and metadata) for a skill.
func (c *Client) GetSkillContent(name string) (*SkillContentResponse, error) {
	var resp SkillContentResponse
	path := fmt.Sprintf("/api/skills/%s/content", url.PathEscape(name))
	if err := c.DoJSON("GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateSkillRequest for POST /api/skills
type CreateSkillRequest struct {
	Name  string `json:"name"`
	Scope string `json:"scope,omitempty"`
}

// CreateSkill creates a new empty skill in the platform (team or org scope).
func (c *Client) CreateSkill(name string, scope string) error {
	body := CreateSkillRequest{Name: name, Scope: scope}
	return c.DoJSON("POST", "/api/skills", body, nil)
}

// --- Utility ---

// Ping checks if the remote server is reachable and authenticated.
func (c *Client) Ping() error {
	resp, err := c.Do("GET", "/api/auth/me", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("not authenticated")
	}
	if resp.StatusCode >= 400 {
		return parseErrorResponse(resp)
	}
	return nil
}
