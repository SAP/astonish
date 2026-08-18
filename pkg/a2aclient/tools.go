package a2aclient

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/SAP/astonish/pkg/a2a"
)

// nonAlphanumericRe matches any character that is not a letter, digit, or underscore.
var nonAlphanumericRe = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// A2ATool represents a tool generated from an A2A agent's skill.
type A2ATool struct {
	name        string
	description string
	agentName   string
	skillID     string
	client      *Client
}

// Name returns the sanitized tool name.
func (t *A2ATool) Name() string {
	return t.name
}

// Description returns the tool description.
func (t *A2ATool) Description() string {
	return t.description
}

// Run executes the tool by sending a message to the remote A2A agent.
func (t *A2ATool) Run(ctx context.Context, args map[string]any) (map[string]any, error) {
	message, _ := args["message"].(string)
	if message == "" {
		return nil, fmt.Errorf("a2aclient: 'message' argument is required")
	}

	contextID, _ := args["context_id"].(string)

	params := a2a.SendMessageParams{
		Message: a2a.Message{
			Role:  "user",
			Parts: []a2a.Part{a2a.TextPart{Text: message}},
			Metadata: map[string]any{
				"skill_id": t.skillID,
			},
		},
	}

	if contextID != "" {
		params.Configuration = &a2a.TaskConfig{
			ContextID: contextID,
		}
	}

	task, err := t.client.SendMessage(ctx, params)
	if err != nil {
		return map[string]any{
			"status":   "error",
			"response": err.Error(),
		}, err
	}

	// Extract text response from task
	response := extractResponse(task)

	// Collect artifacts
	artifacts := make([]any, 0, len(task.Artifacts))
	for _, artifact := range task.Artifacts {
		artifacts = append(artifacts, map[string]any{
			"name":        artifact.Name,
			"description": artifact.Description,
			"index":       artifact.Index,
		})
	}

	return map[string]any{
		"status":    string(task.Status.State),
		"response":  response,
		"task_id":   task.ID,
		"artifacts": artifacts,
	}, nil
}

// extractResponse extracts the text response from a task's status message or history.
func extractResponse(task *a2a.Task) string {
	// First check the status message
	if task.Status.Message != nil {
		for _, part := range task.Status.Message.Parts {
			if tp, ok := part.(a2a.TextPart); ok {
				return tp.Text
			}
		}
	}

	// Then check artifacts
	for _, artifact := range task.Artifacts {
		for _, part := range artifact.Parts {
			if tp, ok := part.(a2a.TextPart); ok {
				return tp.Text
			}
		}
	}

	// Fall back to history (last agent message)
	for i := len(task.History) - 1; i >= 0; i-- {
		if task.History[i].Role == "agent" {
			for _, part := range task.History[i].Parts {
				if tp, ok := part.(a2a.TextPart); ok {
					return tp.Text
				}
			}
		}
	}

	return ""
}

// GenerateTools creates A2ATool instances from an agent card's skills.
// If the card has no skills, a single generic tool is generated.
func GenerateTools(agentName string, card *a2a.AgentCard, client *Client) []*A2ATool {
	if card == nil {
		return nil
	}

	if len(card.Skills) == 0 {
		// Generate a single generic tool for the agent
		toolName := sanitizeToolName("a2a_" + agentName)
		description := fmt.Sprintf("Send a message to the %s agent", agentName)
		if card.Description != "" {
			description = card.Description
		}

		return []*A2ATool{
			{
				name:        toolName,
				description: description,
				agentName:   agentName,
				skillID:     "",
				client:      client,
			},
		}
	}

	tools := make([]*A2ATool, 0, len(card.Skills))
	for _, skill := range card.Skills {
		toolName := sanitizeToolName("a2a_" + agentName + "_" + skill.ID)
		description := skill.Description
		if description == "" {
			description = skill.Name
		}
		if card.Description != "" {
			description = description + " (via " + card.Description + ")"
		}

		tools = append(tools, &A2ATool{
			name:        toolName,
			description: description,
			agentName:   agentName,
			skillID:     skill.ID,
			client:      client,
		})
	}

	return tools
}

// sanitizeToolName replaces non-alphanumeric characters with underscores and lowercases the result.
func sanitizeToolName(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumericRe.ReplaceAllString(s, "_")
	// Remove consecutive underscores
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	// Trim trailing underscores
	s = strings.TrimRight(s, "_")
	return s
}
