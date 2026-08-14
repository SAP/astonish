// Package a2a implements the Agent-to-Agent (A2A) protocol v1.0 types and
// server-side infrastructure for Astonish's A2A channel adapter.
package a2a

import (
	"encoding/json"
	"time"
)

// TaskState represents the lifecycle state of an A2A task.
type TaskState string

const (
	TaskStateSubmitted     TaskState = "submitted"
	TaskStateWorking       TaskState = "working"
	TaskStateInputRequired TaskState = "input-required"
	TaskStateAuthRequired  TaskState = "auth-required"
	TaskStateCompleted     TaskState = "completed"
	TaskStateFailed        TaskState = "failed"
	TaskStateCanceled      TaskState = "canceled"
	TaskStateRejected      TaskState = "rejected"
)

// IsTerminal returns true if the state is a terminal state.
func (s TaskState) IsTerminal() bool {
	return s == TaskStateCompleted || s == TaskStateFailed ||
		s == TaskStateCanceled || s == TaskStateRejected
}

// ValidTransition checks whether transitioning from the current state to next is allowed.
func (s TaskState) ValidTransition(next TaskState) bool {
	if s.IsTerminal() {
		return false // cannot transition from terminal states
	}
	switch s {
	case TaskStateSubmitted:
		return next == TaskStateWorking || next == TaskStateRejected || next == TaskStateCanceled
	case TaskStateWorking:
		return next == TaskStateCompleted || next == TaskStateFailed ||
			next == TaskStateCanceled || next == TaskStateInputRequired ||
			next == TaskStateAuthRequired
	case TaskStateInputRequired, TaskStateAuthRequired:
		return next == TaskStateWorking || next == TaskStateCanceled || next == TaskStateFailed
	}
	return false
}

// Part is a content unit within a Message or Artifact.
type Part interface {
	partType() string
}

// TextPart contains plain text content.
type TextPart struct {
	Text string `json:"text"`
}

func (TextPart) partType() string { return "text" }

// FilePart references a file.
type FilePart struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
	Bytes    []byte `json:"bytes,omitempty"`
}

func (FilePart) partType() string { return "file" }

// DataPart contains structured JSON data.
type DataPart struct {
	MimeType string         `json:"mimeType,omitempty"`
	Data     map[string]any `json:"data"`
}

func (DataPart) partType() string { return "data" }

// Message is a communication turn between client and agent.
type Message struct {
	Role     string         `json:"role"` // "user" or "agent"
	Parts    []Part         `json:"parts"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// MarshalJSON implements custom JSON marshaling for Message to handle Part interface.
func (m Message) MarshalJSON() ([]byte, error) {
	parts := make([]json.RawMessage, 0, len(m.Parts))
	for _, p := range m.Parts {
		var raw []byte
		var err error
		switch v := p.(type) {
		case TextPart:
			raw, err = json.Marshal(struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: "text", Text: v.Text})
		case FilePart:
			raw, err = json.Marshal(struct {
				Type     string `json:"type"`
				Name     string `json:"name,omitempty"`
				MimeType string `json:"mimeType,omitempty"`
				URI      string `json:"uri,omitempty"`
			}{Type: "file", Name: v.Name, MimeType: v.MimeType, URI: v.URI})
		case DataPart:
			raw, err = json.Marshal(struct {
				Type     string         `json:"type"`
				MimeType string         `json:"mimeType,omitempty"`
				Data     map[string]any `json:"data"`
			}{Type: "data", MimeType: v.MimeType, Data: v.Data})
		}
		if err != nil {
			return nil, err
		}
		parts = append(parts, raw)
	}
	return json.Marshal(struct {
		Role     string            `json:"role"`
		Parts    []json.RawMessage `json:"parts"`
		Metadata map[string]any    `json:"metadata,omitempty"`
	}{Role: m.Role, Parts: parts, Metadata: m.Metadata})
}

// UnmarshalJSON implements custom JSON unmarshaling for Message to handle Part interface.
func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role     string            `json:"role"`
		Parts    []json.RawMessage `json:"parts"`
		Metadata map[string]any    `json:"metadata,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.Metadata = raw.Metadata
	m.Parts = make([]Part, 0, len(raw.Parts))
	for _, rawPart := range raw.Parts {
		var typeHolder struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawPart, &typeHolder); err != nil {
			return err
		}
		switch typeHolder.Type {
		case "text":
			var p TextPart
			if err := json.Unmarshal(rawPart, &p); err != nil {
				return err
			}
			m.Parts = append(m.Parts, p)
		case "file":
			var p FilePart
			if err := json.Unmarshal(rawPart, &p); err != nil {
				return err
			}
			m.Parts = append(m.Parts, p)
		case "data":
			var p DataPart
			if err := json.Unmarshal(rawPart, &p); err != nil {
				return err
			}
			m.Parts = append(m.Parts, p)
		default:
			// Unknown part type — store as text with the raw JSON
			m.Parts = append(m.Parts, TextPart{Text: string(rawPart)})
		}
	}
	return nil
}

// TaskStatus holds the current state and optional message for a task.
type TaskStatus struct {
	State     TaskState `json:"state"`
	Message   *Message  `json:"message,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Artifact is output generated by the agent.
type Artifact struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Parts       []Part `json:"parts"`
	Index       int    `json:"index"`
	Append      bool   `json:"append,omitempty"`
	LastChunk   bool   `json:"lastChunk,omitempty"`
}

// PushNotificationConfig holds webhook configuration for async delivery.
type PushNotificationConfig struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

// Task is the fundamental unit of work in A2A.
type Task struct {
	ID                     string                  `json:"id"`
	ContextID              string                  `json:"contextId"`
	Status                 TaskStatus              `json:"status"`
	History                []Message               `json:"history,omitempty"`
	Artifacts              []Artifact              `json:"artifacts,omitempty"`
	Metadata               map[string]any          `json:"metadata,omitempty"`
	PushNotificationConfig *PushNotificationConfig `json:"pushNotificationConfig,omitempty"`
	AgentID                string                  `json:"agentId,omitempty"` // owning registered agent
	CreatedAt              time.Time               `json:"createdAt"`
	UpdatedAt              time.Time               `json:"updatedAt"`
}

// --- JSON-RPC Types ---

// JSONRPCRequest is a JSON-RPC 2.0 request envelope.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response envelope.
type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id"`
	Result  any           `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC 2.0 error.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC error codes.
const (
	ErrCodeParseError     = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
	// A2A-specific error codes
	ErrCodeTaskNotFound = -32001
	ErrCodeAuthRequired = -32002
	ErrCodeRateLimited  = -32003
	ErrCodeForbidden    = -32004
)

// --- A2A Method Params ---

// SendMessageParams holds parameters for the message/send and message/stream methods.
type SendMessageParams struct {
	Message       Message     `json:"message"`
	Configuration *TaskConfig `json:"configuration,omitempty"`
}

// TaskConfig holds optional configuration for task creation.
type TaskConfig struct {
	ContextID         string         `json:"contextId,omitempty"`
	TaskID            string         `json:"taskId,omitempty"` // for follow-up messages
	ReturnImmediately bool           `json:"returnImmediately,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"` // includes identity propagation fields
}

// GetTaskParams holds parameters for tasks/get.
type GetTaskParams struct {
	TaskID string `json:"taskId"`
}

// CancelTaskParams holds parameters for tasks/cancel.
type CancelTaskParams struct {
	TaskID string `json:"taskId"`
}

// SetPushNotificationParams holds parameters for pushNotification/set.
type SetPushNotificationParams struct {
	TaskID string                 `json:"taskId"`
	Config PushNotificationConfig `json:"pushNotificationConfig"`
}

// --- Agent Card Types ---

// AgentCard is the discovery document for an A2A agent.
type AgentCard struct {
	Name               string                    `json:"name"`
	Description        string                    `json:"description,omitempty"`
	URL                string                    `json:"url"`
	Version            string                    `json:"version,omitempty"`
	Provider           *AgentProvider            `json:"provider,omitempty"`
	Capabilities       *AgentCapabilities        `json:"capabilities,omitempty"`
	SecuritySchemes    map[string]SecurityScheme `json:"securitySchemes,omitempty"`
	Security           []map[string][]string     `json:"security,omitempty"`
	DefaultInputModes  []string                  `json:"defaultInputModes,omitempty"`
	DefaultOutputModes []string                  `json:"defaultOutputModes,omitempty"`
	Skills             []Skill                   `json:"skills,omitempty"`
}

// AgentProvider identifies the organization providing the agent.
type AgentProvider struct {
	Organization string `json:"organization"`
	URL          string `json:"url,omitempty"`
}

// AgentCapabilities declares what the agent supports.
type AgentCapabilities struct {
	Streaming              bool `json:"streaming"`
	PushNotifications      bool `json:"pushNotifications"`
	StateTransitionHistory bool `json:"stateTransitionHistory"`
}

// SecurityScheme describes an authentication method (OpenAPI-style).
type SecurityScheme struct {
	Type   string `json:"type"`             // "http", "apiKey"
	Scheme string `json:"scheme,omitempty"` // "bearer" for type=http
	In     string `json:"in,omitempty"`     // "header" for type=apiKey
	Name   string `json:"name,omitempty"`   // header name for type=apiKey
}

// Skill describes a specific capability of the agent.
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// --- SSE Event Types ---

// TaskStatusUpdateEvent is sent via SSE when a task's status changes.
type TaskStatusUpdateEvent struct {
	TaskID string     `json:"taskId"`
	Status TaskStatus `json:"status"`
}

// TaskArtifactUpdateEvent is sent via SSE when an artifact is produced.
type TaskArtifactUpdateEvent struct {
	TaskID   string   `json:"taskId"`
	Artifact Artifact `json:"artifact"`
}
