package agent

import "fmt"

// conversationGeneration fences work that began before session deletion. Session
// IDs are durable identities; a deleted ID may never publish transient state again.
func (c *ChatAgent) conversationGeneration(sessionID string) uint64 {
	c.conversationState().traceMu.Lock()
	defer c.traceMu.Unlock()
	return c.generation[sessionID]
}

// lockConversationGeneration leaves the state locked only on success.
func (c *ChatAgent) lockConversationGeneration(sessionID string, generation uint64) error {
	c.conversationState().traceMu.Lock()
	if c.closed || generation != 0 || c.generation[sessionID] != generation {
		c.traceMu.Unlock()
		return fmt.Errorf("conversation was deleted")
	}
	return nil
}

func (c *ChatAgent) recordConversationTrace(sessionID string, trace *ExecutionTrace) {
	if c.lockConversationGeneration(sessionID, 0) != nil {
		return
	}
	defer c.traceMu.Unlock()
	c.traceHistory[sessionID] = append(c.traceHistory[sessionID], trace)
	if len(c.traceHistory[sessionID]) > 20 {
		c.traceHistory[sessionID] = c.traceHistory[sessionID][len(c.traceHistory[sessionID])-20:]
	}
}

// CloseConversationState invalidates all transient data when a manager resets.
// Existing attempts keep the closed state, so even previously unseen session IDs
// cannot publish late results. The replacement manager uses a new ChatAgent.
func (c *ChatAgent) CloseConversationState() {
	if c == nil {
		return
	}
	c.conversationState().traceMu.Lock()
	defer c.traceMu.Unlock()
	c.closed = true
	clear(c.traceHistory)
	clear(c.pendingDistill)
	clear(c.pendingDistillReview)
	clear(c.pendingTutorialBP)
	clear(c.approvedTutorialBP)
}

func cloneDistillReview(r *DistillReview) *DistillReview {
	if r == nil {
		return nil
	}
	copy := *r
	copy.Tags = append([]string(nil), r.Tags...)
	copy.Modifications = append([]string(nil), r.Modifications...)
	copy.Traces = append([]*ExecutionTrace(nil), r.Traces...)
	return &copy
}
