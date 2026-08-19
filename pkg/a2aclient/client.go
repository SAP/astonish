package a2aclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/credentials"
)

// StreamEvent represents an event received from an SSE stream.
type StreamEvent struct {
	Type           string                       // "status_update" or "artifact_update"
	StatusUpdate   *a2a.TaskStatusUpdateEvent   `json:"statusUpdate,omitempty"`
	ArtifactUpdate *a2a.TaskArtifactUpdateEvent `json:"artifactUpdate,omitempty"`
	Error          error                        `json:"-"`
}

// Client is an HTTP client for communicating with a remote A2A agent.
type Client struct {
	httpClient *http.Client
	config     A2AAgentConfig
	resolver   credentials.CredentialResolver
	requestID  atomic.Int64
	invocation atomic.Pointer[invocationConfig]
}

type invocationConfig struct {
	endpointURL     string
	protocolVersion string
}

// SetProtocolVersion sets the A2A protocol version detected from the agent card.
func (c *Client) SetProtocolVersion(v string) {
	for {
		current := c.invocation.Load()
		next := &invocationConfig{endpointURL: c.config.URL, protocolVersion: v}
		if current != nil {
			next.endpointURL = current.endpointURL
		}
		if c.invocation.CompareAndSwap(current, next) {
			return
		}
	}
}

// ApplyAgentCard atomically applies the invocation endpoint and protocol version
// selected from an agent card. Discovery continues to use the configured URL.
func (c *Client) ApplyAgentCard(card *a2a.AgentCard) error {
	selected, err := card.SelectCompatibleInterface()
	if err != nil {
		return fmt.Errorf("a2aclient: failed to select agent interface: %w", err)
	}

	endpointURL := selected.URL
	if endpointURL == "" {
		endpointURL = c.config.URL
	}
	c.invocation.Store(&invocationConfig{
		endpointURL:     endpointURL,
		protocolVersion: selected.ProtocolVersion,
	})
	return nil
}

func (c *Client) invocationConfig() invocationConfig {
	if selected := c.invocation.Load(); selected != nil {
		return *selected
	}
	return invocationConfig{endpointURL: c.config.URL}
}

// isV1 returns true if the remote agent uses A2A v1.0 protocol.
func (c *Client) isV1() bool {
	return isV1Protocol(c.invocationConfig().protocolVersion)
}

func isV1Protocol(v string) bool {
	return v == "1.0" || v == "1" || strings.HasPrefix(v, "1.")
}

const agentCardPath = "/.well-known/agent-card.json"

// NormalizeBaseURL returns the canonical A2A agent base URL.
// It removes trailing slashes and a terminal agent-card well-known path.
func NormalizeBaseURL(rawURL string) string {
	baseURL := strings.TrimRight(rawURL, "/")
	baseURL = strings.TrimSuffix(baseURL, agentCardPath)
	return strings.TrimRight(baseURL, "/")
}

// AgentCardURL returns the agent-card well-known URL for an A2A agent URL.
func AgentCardURL(rawURL string) string {
	return NormalizeBaseURL(rawURL) + agentCardPath
}

// NewClient creates a new A2A client for the given agent configuration.
func NewClient(cfg A2AAgentConfig, resolver credentials.CredentialResolver) *Client {
	cfg.URL = NormalizeBaseURL(cfg.URL)
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		httpClient: &http.Client{Timeout: timeout},
		config:     cfg,
		resolver:   resolver,
	}
}

// FetchAgentCard retrieves the agent card from the well-known endpoint.
func (c *Client) FetchAgentCard(ctx context.Context) (*a2a.AgentCard, error) {
	url := AgentCardURL(c.config.URL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("a2aclient: failed to create request: %w", err)
	}

	if err := c.resolveAuthHeaders(req); err != nil {
		return nil, fmt.Errorf("a2aclient: failed to resolve auth: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("a2aclient: failed to fetch agent card: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("a2aclient: agent card fetch returned status %d: %s", resp.StatusCode, string(body))
	}

	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		return nil, fmt.Errorf("a2aclient: failed to decode agent card: %w", err)
	}

	return &card, nil
}

// SendMessage sends a message to the remote agent and returns the resulting task.
func (c *Client) SendMessage(ctx context.Context, params a2a.SendMessageParams) (*a2a.Task, error) {
	invocation := c.invocationConfig()
	v1 := isV1Protocol(invocation.protocolVersion)
	method := "message/send"
	var rpcParams any = params

	if v1 {
		method = "SendMessage"
		rpcParams = c.buildV1Params(params)
	}

	resp, err := c.doJSONRPCAt(ctx, invocation, method, rpcParams)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("a2aclient: RPC error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	// Marshal result back to JSON then unmarshal into Task
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("a2aclient: failed to marshal result: %w", err)
	}

	var task a2a.Task
	if v1 {
		task, err = parseV1TaskResponse(resultBytes)
		if err != nil {
			return nil, err
		}
	} else {
		if err := json.Unmarshal(resultBytes, &task); err != nil {
			return nil, fmt.Errorf("a2aclient: failed to decode task: %w", err)
		}
	}

	// Normalize v1.0 task states to v0.3 equivalents
	task.Status.State = normalizeTaskState(task.Status.State)

	return &task, nil
}

// parseV1TaskResponse parses a v1.0 task response which has a different part format.
// v1.0 parts use field presence as discriminator (no "type" field):
//
//	{"text": "..."} for text parts
//	{"data": {...}, "mediaType": "..."} for data parts
func parseV1TaskResponse(data []byte) (a2a.Task, error) {
	// v1.0 wraps in {"task": {...}}
	var raw struct {
		Task json.RawMessage `json:"task"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return a2a.Task{}, fmt.Errorf("a2aclient: failed to parse v1 response envelope: %w", err)
	}

	taskData := raw.Task
	if taskData == nil {
		// Not wrapped — try direct
		taskData = data
	}

	// Parse the task with raw parts handling
	var taskRaw struct {
		ID        string `json:"id"`
		ContextID string `json:"contextId"`
		Status    struct {
			State     a2a.TaskState `json:"state"`
			Timestamp string        `json:"timestamp"`
			Message   *struct {
				Role  string            `json:"role"`
				Parts []json.RawMessage `json:"parts"`
			} `json:"message,omitempty"`
		} `json:"status"`
		Artifacts []struct {
			ArtifactID  string            `json:"artifactId"`
			Name        string            `json:"name"`
			Description string            `json:"description,omitempty"`
			Parts       []json.RawMessage `json:"parts"`
			Index       int               `json:"index"`
		} `json:"artifacts"`
		History []struct {
			Role  string            `json:"role"`
			Parts []json.RawMessage `json:"parts"`
		} `json:"history"`
	}

	if err := json.Unmarshal(taskData, &taskRaw); err != nil {
		return a2a.Task{}, fmt.Errorf("a2aclient: failed to parse v1 task: %w", err)
	}

	task := a2a.Task{
		ID:        taskRaw.ID,
		ContextID: taskRaw.ContextID,
		Status: a2a.TaskStatus{
			State: taskRaw.Status.State,
		},
	}

	// Parse status message parts
	if taskRaw.Status.Message != nil {
		msg := &a2a.Message{Role: taskRaw.Status.Message.Role}
		msg.Parts = parseV1Parts(taskRaw.Status.Message.Parts)
		task.Status.Message = msg
	}

	// Parse artifacts
	for _, artRaw := range taskRaw.Artifacts {
		art := a2a.Artifact{
			Name:        artRaw.Name,
			Description: artRaw.Description,
			Index:       artRaw.Index,
			Parts:       parseV1Parts(artRaw.Parts),
		}
		task.Artifacts = append(task.Artifacts, art)
	}

	// Parse history
	for _, histRaw := range taskRaw.History {
		msg := a2a.Message{
			Role:  histRaw.Role,
			Parts: parseV1Parts(histRaw.Parts),
		}
		task.History = append(task.History, msg)
	}

	return task, nil
}

// parseV1Parts converts v1.0 raw JSON parts into typed Part instances.
// v1.0 uses field presence: {"text": "..."} or {"data": {...}, "mediaType": "..."}.
func parseV1Parts(rawParts []json.RawMessage) []a2a.Part {
	var parts []a2a.Part
	for _, raw := range rawParts {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			continue
		}

		if textRaw, ok := fields["text"]; ok {
			var text string
			if err := json.Unmarshal(textRaw, &text); err == nil {
				parts = append(parts, a2a.TextPart{Text: text})
			}
		} else if dataRaw, ok := fields["data"]; ok {
			var data map[string]any
			if err := json.Unmarshal(dataRaw, &data); err == nil {
				dp := a2a.DataPart{Data: data}
				if mtRaw, ok := fields["mediaType"]; ok {
					var mt string
					if json.Unmarshal(mtRaw, &mt) == nil {
						dp.MimeType = mt
					}
				}
				parts = append(parts, dp)
			}
		}
	}
	return parts
}

// buildV1Params transforms v0.3 SendMessageParams into v1.0 format.
// v1.0 requires: messageId in message, role as ROLE_USER/ROLE_AGENT,
// parts as [{text: "..."}, {data: {...}}], acceptedOutputModes in configuration.
func (c *Client) buildV1Params(params a2a.SendMessageParams) map[string]any {
	// Transform role
	role := params.Message.Role
	switch role {
	case "user":
		role = "ROLE_USER"
	case "agent":
		role = "ROLE_AGENT"
	}

	// Build parts from the original message
	var parts []map[string]any
	for _, part := range params.Message.Parts {
		switch p := part.(type) {
		case a2a.TextPart:
			if p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text})
			}
		case a2a.DataPart:
			m := map[string]any{"data": p.Data}
			if p.MimeType != "" {
				m["mediaType"] = p.MimeType
			}
			parts = append(parts, m)
		}
	}

	// If no parts were generated, use empty
	if len(parts) == 0 {
		parts = []map[string]any{}
	}

	// Generate a unique messageId (required in v1.0)
	msgID := generateMessageID()

	msg := map[string]any{
		"role":      role,
		"messageId": msgID,
		"parts":     parts,
	}

	// Pass through metadata if present
	if params.Message.Metadata != nil && len(params.Message.Metadata) > 0 {
		msg["metadata"] = params.Message.Metadata
	}

	result := map[string]any{
		"message": msg,
	}

	// Add configuration
	cfg := map[string]any{
		"acceptedOutputModes": []string{"application/json", "text/plain"},
	}
	if params.Configuration != nil {
		if params.Configuration.ContextID != "" {
			cfg["contextId"] = params.Configuration.ContextID
		}
		if params.Configuration.TaskID != "" {
			cfg["taskId"] = params.Configuration.TaskID
		}
	}
	result["configuration"] = cfg

	return result
}

// generateMessageID creates a unique message ID for v1.0 requests.
func generateMessageID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("msg-%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// normalizeTaskState converts v1.0 uppercase task states to v0.3 lowercase equivalents.
func normalizeTaskState(s a2a.TaskState) a2a.TaskState {
	switch s {
	case "TASK_STATE_SUBMITTED":
		return a2a.TaskStateSubmitted
	case "TASK_STATE_WORKING":
		return a2a.TaskStateWorking
	case "TASK_STATE_COMPLETED":
		return a2a.TaskStateCompleted
	case "TASK_STATE_FAILED":
		return a2a.TaskStateFailed
	case "TASK_STATE_CANCELED":
		return a2a.TaskStateCanceled
	case "TASK_STATE_INPUT_REQUIRED":
		return a2a.TaskStateInputRequired
	case "TASK_STATE_AUTH_REQUIRED":
		return a2a.TaskStateAuthRequired
	case "TASK_STATE_REJECTED":
		return a2a.TaskStateRejected
	default:
		return s // already v0.3 or unknown
	}
}

// SendMessageStream sends a message and returns a channel of SSE events.
// TODO(a2a-v1): Apply v1.0 transforms (method name, headers, params) when streaming is wired.
func (c *Client) SendMessageStream(ctx context.Context, params a2a.SendMessageParams) (<-chan StreamEvent, error) {
	reqBody, err := c.buildJSONRPCBody("message/stream", params)
	if err != nil {
		return nil, err
	}

	url := c.invocationConfig().endpointURL
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("a2aclient: failed to create stream request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	if err := c.resolveAuthHeaders(req); err != nil {
		return nil, fmt.Errorf("a2aclient: failed to resolve auth: %w", err)
	}

	// Use a client without timeout for streaming
	streamClient := &http.Client{}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("a2aclient: stream request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("a2aclient: stream returned status %d: %s", resp.StatusCode, string(body))
	}

	ch := make(chan StreamEvent, 16)
	go c.readSSEStream(ctx, resp.Body, ch)

	return ch, nil
}

// GetTask retrieves a task by ID from the remote agent.
func (c *Client) GetTask(ctx context.Context, taskID string) (*a2a.Task, error) {
	params := a2a.GetTaskParams{TaskID: taskID}
	resp, err := c.doJSONRPC(ctx, "tasks/get", params)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("a2aclient: RPC error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("a2aclient: failed to marshal result: %w", err)
	}

	var task a2a.Task
	if c.isV1() {
		task, err = parseV1TaskResponse(resultBytes)
		if err != nil {
			return nil, err
		}
	} else {
		if err := json.Unmarshal(resultBytes, &task); err != nil {
			return nil, fmt.Errorf("a2aclient: failed to decode task: %w", err)
		}
	}

	// Normalize v1.0 task states to v0.3 equivalents
	task.Status.State = normalizeTaskState(task.Status.State)

	return &task, nil
}

// CancelTask cancels a task by ID on the remote agent.
func (c *Client) CancelTask(ctx context.Context, taskID string) error {
	params := a2a.CancelTaskParams{TaskID: taskID}
	resp, err := c.doJSONRPC(ctx, "tasks/cancel", params)
	if err != nil {
		return err
	}

	if resp.Error != nil {
		return fmt.Errorf("a2aclient: RPC error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	return nil
}

// doJSONRPC performs a JSON-RPC 2.0 call to the remote agent.
func (c *Client) doJSONRPC(ctx context.Context, method string, params any) (*a2a.JSONRPCResponse, error) {
	return c.doJSONRPCAt(ctx, c.invocationConfig(), method, params)
}

func (c *Client) doJSONRPCAt(ctx context.Context, invocation invocationConfig, method string, params any) (*a2a.JSONRPCResponse, error) {
	reqBody, err := c.buildJSONRPCBody(method, params)
	if err != nil {
		return nil, err
	}

	url := invocation.endpointURL
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("a2aclient: failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if isV1Protocol(invocation.protocolVersion) {
		req.Header.Set("A2A-Version", "1.0")
	}

	if err := c.resolveAuthHeaders(req); err != nil {
		return nil, fmt.Errorf("a2aclient: failed to resolve auth: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("a2aclient: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("a2aclient: server returned status %d: %s", resp.StatusCode, string(body))
	}

	var rpcResp a2a.JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("a2aclient: failed to decode response: %w", err)
	}

	return &rpcResp, nil
}

// buildJSONRPCBody constructs the JSON-RPC request body.
func (c *Client) buildJSONRPCBody(method string, params any) ([]byte, error) {
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("a2aclient: failed to marshal params: %w", err)
	}

	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.requestID.Add(1),
		Method:  method,
		Params:  paramsBytes,
	}

	body, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("a2aclient: failed to marshal request: %w", err)
	}

	return body, nil
}

// resolveAuthHeaders applies authentication and custom headers to the request.
func (c *Client) resolveAuthHeaders(req *http.Request) error {
	// Apply credential-based auth if configured
	if c.config.CredentialName != "" && c.resolver != nil {
		headerKey, headerValue, err := c.resolver.Resolve(c.config.CredentialName)
		if err != nil {
			return fmt.Errorf("failed to resolve credential %q: %w", c.config.CredentialName, err)
		}
		if headerKey != "" && headerValue != "" {
			req.Header.Set(headerKey, headerValue)
		}
	}

	// Apply additional custom headers
	for key, value := range c.config.Headers {
		req.Header.Set(key, value)
	}

	return nil
}

// readSSEStream reads Server-Sent Events from the response body and sends them to the channel.
func (c *Client) readSSEStream(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	var eventType string
	var dataLines []string

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			ch <- StreamEvent{Error: ctx.Err()}
			return
		default:
		}

		line := scanner.Text()

		if line == "" {
			// Empty line = end of event
			if len(dataLines) > 0 {
				data := strings.Join(dataLines, "\n")
				event := c.parseSSEEvent(eventType, data)
				ch <- event
			}
			eventType = ""
			dataLines = nil
			continue
		}

		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- StreamEvent{Error: fmt.Errorf("a2aclient: SSE stream error: %w", err)}
	}
}

// parseSSEEvent parses an SSE event into a StreamEvent.
func (c *Client) parseSSEEvent(eventType, data string) StreamEvent {
	// Try to parse as JSON-RPC response first
	var rpcResp struct {
		Result json.RawMessage   `json:"result"`
		Error  *a2a.JSONRPCError `json:"error,omitempty"`
	}

	if err := json.Unmarshal([]byte(data), &rpcResp); err != nil {
		return StreamEvent{Error: fmt.Errorf("a2aclient: failed to parse SSE data: %w", err)}
	}

	if rpcResp.Error != nil {
		return StreamEvent{Error: fmt.Errorf("a2aclient: RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)}
	}

	// Determine event type from the event field or from the result content
	switch eventType {
	case "status_update":
		var update a2a.TaskStatusUpdateEvent
		if err := json.Unmarshal(rpcResp.Result, &update); err != nil {
			return StreamEvent{Error: fmt.Errorf("a2aclient: failed to parse status update: %w", err)}
		}
		return StreamEvent{Type: "status_update", StatusUpdate: &update}

	case "artifact_update":
		var update a2a.TaskArtifactUpdateEvent
		if err := json.Unmarshal(rpcResp.Result, &update); err != nil {
			return StreamEvent{Error: fmt.Errorf("a2aclient: failed to parse artifact update: %w", err)}
		}
		return StreamEvent{Type: "artifact_update", ArtifactUpdate: &update}

	default:
		// Try to detect from content
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rpcResp.Result, &raw); err == nil {
			if _, ok := raw["status"]; ok {
				var update a2a.TaskStatusUpdateEvent
				if err := json.Unmarshal(rpcResp.Result, &update); err == nil {
					return StreamEvent{Type: "status_update", StatusUpdate: &update}
				}
			}
			if _, ok := raw["artifact"]; ok {
				var update a2a.TaskArtifactUpdateEvent
				if err := json.Unmarshal(rpcResp.Result, &update); err == nil {
					return StreamEvent{Type: "artifact_update", ArtifactUpdate: &update}
				}
			}
		}
		return StreamEvent{Error: fmt.Errorf("a2aclient: unknown SSE event type: %s", eventType)}
	}
}
