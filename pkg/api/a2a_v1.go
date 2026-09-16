package api

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/a2a"
)

type a2aV1SendMessageParams struct {
	Message struct {
		Role     string            `json:"role"`
		Parts    []json.RawMessage `json:"parts"`
		Metadata map[string]any    `json:"metadata,omitempty"`
	} `json:"message"`
	Configuration *a2a.TaskConfig `json:"configuration,omitempty"`
}

func decodeA2AV1SendMessageParams(data json.RawMessage) (a2a.SendMessageParams, error) {
	var wire a2aV1SendMessageParams
	if err := json.Unmarshal(data, &wire); err != nil {
		return a2a.SendMessageParams{}, err
	}
	message := a2a.Message{Role: normalizeA2AV1Role(wire.Message.Role), Metadata: wire.Message.Metadata}
	for _, rawPart := range wire.Message.Parts {
		part, err := decodeA2AV1Part(rawPart)
		if err != nil {
			return a2a.SendMessageParams{}, err
		}
		message.Parts = append(message.Parts, part)
	}
	return a2a.SendMessageParams{Message: message, Configuration: wire.Configuration}, nil
}

func decodeA2AV1Part(rawPart json.RawMessage) (a2a.Part, error) {
	var part struct {
		Kind      string          `json:"kind"`
		Text      *string         `json:"text"`
		Data      json.RawMessage `json:"data"`
		MediaType string          `json:"mediaType"`
		File      *struct {
			Name     string `json:"name"`
			MimeType string `json:"mimeType"`
			URI      string `json:"uri"`
			Bytes    []byte `json:"bytes"`
		} `json:"file"`
	}
	if err := json.Unmarshal(rawPart, &part); err != nil {
		return nil, fmt.Errorf("invalid message part: %w", err)
	}

	kind := strings.ToLower(strings.TrimSpace(part.Kind))
	if kind == "" {
		present := 0
		if part.Text != nil {
			kind = "text"
			present++
		}
		if part.Data != nil {
			kind = "data"
			present++
		}
		if part.File != nil {
			kind = "file"
			present++
		}
		if present != 1 {
			return nil, fmt.Errorf("message part without kind must contain exactly one of text, data, or file")
		}
	}

	switch kind {
	case "text":
		if part.Text == nil {
			return nil, fmt.Errorf("text message part is missing text")
		}
		return a2a.TextPart{Text: *part.Text}, nil
	case "data":
		if part.Data == nil {
			return nil, fmt.Errorf("data message part is missing data")
		}
		var value map[string]any
		if err := json.Unmarshal(part.Data, &value); err != nil {
			return nil, fmt.Errorf("invalid data message part: %w", err)
		}
		return a2a.DataPart{MimeType: part.MediaType, Data: value}, nil
	case "file":
		if part.File == nil {
			return nil, fmt.Errorf("file message part is missing file")
		}
		return a2a.FilePart{
			Name:     part.File.Name,
			MimeType: part.File.MimeType,
			URI:      part.File.URI,
			Bytes:    part.File.Bytes,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported message part kind %q", part.Kind)
	}
}

func normalizeA2AV1Role(role string) string {
	switch strings.ToUpper(role) {
	case "ROLE_USER":
		return "user"
	case "ROLE_AGENT":
		return "agent"
	default:
		return strings.ToLower(role)
	}
}

func encodeA2AV1Task(task *a2a.Task) map[string]any {
	result := map[string]any{
		"kind":      "task",
		"id":        task.ID,
		"contextId": task.ContextID,
		"status": map[string]any{
			"state":     encodeA2AV1State(task.Status.State),
			"timestamp": task.Status.Timestamp,
		},
	}
	status := result["status"].(map[string]any)
	responseInArtifacts := task.Status.State == a2a.TaskStateCompleted && len(task.Artifacts) > 0
	if task.Status.Message != nil && !responseInArtifacts {
		status["message"] = encodeA2AV1Message(*task.Status.Message, task.ID+"-status")
	}
	if len(task.History) > 0 {
		history := make([]map[string]any, 0, len(task.History))
		for i, message := range task.History {
			if responseInArtifacts && strings.EqualFold(message.Role, "agent") {
				continue
			}
			history = append(history, encodeA2AV1Message(message, fmt.Sprintf("%s-history-%d", task.ID, i)))
		}
		if len(history) > 0 {
			result["history"] = history
		}
	}
	if len(task.Artifacts) > 0 {
		artifacts := make([]map[string]any, 0, len(task.Artifacts))
		for i, artifact := range task.Artifacts {
			item := map[string]any{
				"artifactId": fmt.Sprintf("%s-artifact-%d", task.ID, i),
				"parts":      encodeA2AV1Parts(artifact.Parts),
			}
			if artifact.Name != "" {
				item["name"] = artifact.Name
			}
			if artifact.Description != "" {
				item["description"] = artifact.Description
			}
			artifacts = append(artifacts, item)
		}
		result["artifacts"] = artifacts
	}
	if task.Metadata != nil {
		result["metadata"] = task.Metadata
	}
	return result
}

func encodeA2AV1StatusUpdate(taskID, contextID string, status a2a.TaskStatus, final bool) map[string]any {
	statusMap := map[string]any{
		"state":     encodeA2AV1State(status.State),
		"timestamp": status.Timestamp,
	}
	if status.Message != nil {
		statusMap["message"] = encodeA2AV1Message(*status.Message, taskID+"-status")
	}
	update := map[string]any{
		"taskId": taskID,
		"status": statusMap,
	}
	if contextID != "" {
		update["contextId"] = contextID
	}
	if final {
		update["final"] = true
	}
	return map[string]any{"statusUpdate": update}
}

func encodeA2AV1ArtifactUpdate(taskID, contextID string, artifact a2a.Artifact, index int) map[string]any {
	item := map[string]any{
		"artifactId": fmt.Sprintf("%s-artifact-%d", taskID, index),
		"parts":      encodeA2AV1Parts(artifact.Parts),
	}
	if artifact.Name != "" {
		item["name"] = artifact.Name
	}
	if artifact.Description != "" {
		item["description"] = artifact.Description
	}
	update := map[string]any{
		"taskId":   taskID,
		"artifact": item,
	}
	if contextID != "" {
		update["contextId"] = contextID
	}
	if artifact.Append {
		update["append"] = true
	}
	if artifact.LastChunk {
		update["lastChunk"] = true
	}
	return map[string]any{"artifactUpdate": update}
}

func encodeA2AV1Message(message a2a.Message, messageID string) map[string]any {
	result := map[string]any{
		"kind":      "message",
		"messageId": messageID,
		"role":      encodeA2AV1Role(message.Role),
		"parts":     encodeA2AV1Parts(message.Parts),
	}
	if message.Metadata != nil {
		result["metadata"] = message.Metadata
	}
	return result
}

func encodeA2AV1Parts(parts []a2a.Part) []map[string]any {
	result := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch value := part.(type) {
		case a2a.TextPart:
			result = append(result, map[string]any{"kind": "text", "text": value.Text})
		case a2a.DataPart:
			item := map[string]any{"kind": "data", "data": value.Data}
			if value.MimeType != "" {
				item["mediaType"] = value.MimeType
			}
			result = append(result, item)
		case a2a.FilePart:
			file := map[string]any{}
			if value.Name != "" {
				file["name"] = value.Name
			}
			if value.URI != "" {
				file["uri"] = value.URI
			}
			if value.MimeType != "" {
				file["mimeType"] = value.MimeType
			}
			result = append(result, map[string]any{"kind": "file", "file": file})
		}
	}
	return result
}

func encodeA2AV1Role(role string) string {
	if strings.EqualFold(role, "user") || strings.EqualFold(role, "ROLE_USER") {
		return "ROLE_USER"
	}
	return "ROLE_AGENT"
}

func encodeA2AV1State(state a2a.TaskState) string {
	return "TASK_STATE_" + strings.ToUpper(string(state))
}
