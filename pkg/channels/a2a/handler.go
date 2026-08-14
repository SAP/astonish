package a2achan

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/channels"
)

// HandleSendMessage processes an incoming A2A message/send request.
// It normalizes the A2A message to an InboundMessage, dispatches to the
// ChatAgent via the channel handler, and waits for the response.
func (c *A2AChannel) HandleSendMessage(
	ctx context.Context,
	agent *a2a.RegisteredAgent,
	params a2a.SendMessageParams,
) (*a2a.Task, error) {
	if c.handler == nil {
		return nil, fmt.Errorf("a2a channel not started")
	}

	// Resolve identity
	var metadata map[string]any
	if params.Configuration != nil {
		metadata = params.Configuration.Metadata
	}
	identity, err := a2a.ResolveIdentity(agent, metadata)
	if err != nil {
		return nil, fmt.Errorf("identity resolution failed: %w", err)
	}

	// Determine context ID
	contextID := ""
	if params.Configuration != nil {
		contextID = params.Configuration.ContextID
	}
	if contextID == "" {
		contextID = NewContextID()
	}

	// Create task in store
	task := c.config.TaskStore.Create(agent.ID, contextID)

	// Transition to working
	_ = c.config.TaskStore.UpdateState(task.ID, a2a.TaskStateWorking, &params.Message)

	// Build session key
	userID := ""
	if identity.IsPropagated {
		userID = identity.ExternalID
	}
	sessionKey := SessionKey(agent.ID, userID, contextID)

	// Normalize A2A message to InboundMessage
	inbound := channels.InboundMessage{
		ID:         task.ID,
		ChannelID:  "a2a",
		SenderID:   identity.ExternalID,
		SenderName: agent.Name,
		ChatID:     contextID,
		ChatType:   channels.ChatTypeDirect,
		ThreadID:   sessionKey,
		Text:       NormalizePartsToText(params.Message.Parts),
		Timestamp:  time.Now(),
		Raw:        params.Message, // preserve full message for advanced use
	}

	// Set routing hint if identity has org/team info
	if identity.OrgSlug != "" {
		inbound.RoutingHint = &channels.RoutingHint{
			OrgSlug:  identity.OrgSlug,
			TeamSlug: identity.TeamSlug,
		}
	}

	// Check if this should return immediately (async mode)
	if params.Configuration != nil && params.Configuration.ReturnImmediately {
		// Fire and forget — dispatch in background
		go func() {
			if handleErr := c.handler(ctx, inbound); handleErr != nil {
				c.logger.Printf("[a2a] async handler error for task %s: %v", task.ID, handleErr)
				_ = c.config.TaskStore.UpdateState(task.ID, a2a.TaskStateFailed, &a2a.Message{
					Role:  "agent",
					Parts: []a2a.Part{a2a.TextPart{Text: handleErr.Error()}},
				})
			}
		}()
		// Return task in submitted/working state
		updatedTask, _ := c.config.TaskStore.Get(task.ID)
		return updatedTask, nil
	}

	// Synchronous mode: register pending response and wait
	respCh := c.RegisterPending(task.ID)
	defer c.UnregisterPending(task.ID)

	// Dispatch to ChatAgent
	if handleErr := c.handler(ctx, inbound); handleErr != nil {
		_ = c.config.TaskStore.UpdateState(task.ID, a2a.TaskStateFailed, &a2a.Message{
			Role:  "agent",
			Parts: []a2a.Part{a2a.TextPart{Text: handleErr.Error()}},
		})
		updatedTask, _ := c.config.TaskStore.Get(task.ID)
		return updatedTask, nil
	}

	// Wait for response from Send() callback
	select {
	case resp := <-respCh:
		// Update task with completed response
		responseMsg := &a2a.Message{
			Role:  "agent",
			Parts: []a2a.Part{a2a.TextPart{Text: resp.Text}},
		}
		_ = c.config.TaskStore.UpdateState(task.ID, a2a.TaskStateCompleted, responseMsg)

		// Add response as artifact
		_ = c.config.TaskStore.AddArtifact(task.ID, a2a.Artifact{
			Name:      "response",
			Parts:     []a2a.Part{a2a.TextPart{Text: resp.Text}},
			LastChunk: true,
		})

	case <-ctx.Done():
		_ = c.config.TaskStore.UpdateState(task.ID, a2a.TaskStateCanceled, nil)
	case <-time.After(5 * time.Minute):
		_ = c.config.TaskStore.UpdateState(task.ID, a2a.TaskStateFailed, &a2a.Message{
			Role:  "agent",
			Parts: []a2a.Part{a2a.TextPart{Text: "request timed out"}},
		})
	}

	updatedTask, _ := c.config.TaskStore.Get(task.ID)
	return updatedTask, nil
}

// HandleGetTask returns a task, validating that the requesting agent owns it.
func (c *A2AChannel) HandleGetTask(agent *a2a.RegisteredAgent, taskID string) (*a2a.Task, error) {
	task, err := c.config.TaskStore.Get(taskID)
	if err != nil {
		return nil, err
	}
	if task.AgentID != agent.ID {
		return nil, fmt.Errorf("task %s not found", taskID) // don't reveal existence
	}
	return task, nil
}

// HandleCancelTask cancels a task, validating ownership.
func (c *A2AChannel) HandleCancelTask(agent *a2a.RegisteredAgent, taskID string) error {
	task, err := c.config.TaskStore.Get(taskID)
	if err != nil {
		return err
	}
	if task.AgentID != agent.ID {
		return fmt.Errorf("task %s not found", taskID)
	}
	return c.config.TaskStore.Cancel(taskID)
}

// NormalizePartsToText extracts text content from A2A message parts.
func NormalizePartsToText(parts []a2a.Part) string {
	var texts []string
	for _, p := range parts {
		switch v := p.(type) {
		case a2a.TextPart:
			if v.Text != "" {
				texts = append(texts, v.Text)
			}
		case a2a.DataPart:
			// Serialize structured data as context
			texts = append(texts, fmt.Sprintf("[data: %v]", v.Data))
		case a2a.FilePart:
			if v.Name != "" {
				texts = append(texts, fmt.Sprintf("[file: %s]", v.Name))
			}
		}
	}
	return strings.Join(texts, "\n")
}
