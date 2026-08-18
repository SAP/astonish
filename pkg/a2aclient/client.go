package a2aclient

import (
	"bufio"
	"bytes"
	"context"
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
	Type           string                        // "status_update" or "artifact_update"
	StatusUpdate   *a2a.TaskStatusUpdateEvent    `json:"statusUpdate,omitempty"`
	ArtifactUpdate *a2a.TaskArtifactUpdateEvent  `json:"artifactUpdate,omitempty"`
	Error          error                         `json:"-"`
}

// Client is an HTTP client for communicating with a remote A2A agent.
type Client struct {
	httpClient *http.Client
	config     A2AAgentConfig
	resolver   credentials.CredentialResolver
	requestID  atomic.Int64
}

// NewClient creates a new A2A client for the given agent configuration.
func NewClient(cfg A2AAgentConfig, resolver credentials.CredentialResolver) *Client {
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
	url := strings.TrimRight(c.config.URL, "/") + "/.well-known/agent-card.json"

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
	resp, err := c.doJSONRPC(ctx, "message/send", params)
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
	if err := json.Unmarshal(resultBytes, &task); err != nil {
		return nil, fmt.Errorf("a2aclient: failed to decode task: %w", err)
	}

	return &task, nil
}

// SendMessageStream sends a message and returns a channel of SSE events.
func (c *Client) SendMessageStream(ctx context.Context, params a2a.SendMessageParams) (<-chan StreamEvent, error) {
	reqBody, err := c.buildJSONRPCBody("message/stream", params)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(c.config.URL, "/")
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
	if err := json.Unmarshal(resultBytes, &task); err != nil {
		return nil, fmt.Errorf("a2aclient: failed to decode task: %w", err)
	}

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
	reqBody, err := c.buildJSONRPCBody(method, params)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(c.config.URL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("a2aclient: failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

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
		Result json.RawMessage `json:"result"`
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
