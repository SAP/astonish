// Package a2achan implements the A2A protocol channel adapter for Astonish.
package a2achan

import (
	"fmt"

	"github.com/google/uuid"
)

// SessionKey generates an Astonish session key for an A2A conversation.
//
// When identity is propagated (a real user), the session key uses the user ID
// so the same user has session continuity regardless of which agent acts on
// their behalf.
//
// When no identity propagation is active, the session key includes the agent ID
// to ensure different agents never share sessions.
func SessionKey(agentID, userID, contextID string) string {
	if userID != "" {
		return fmt.Sprintf("a2a:direct:%s:%s", userID, contextID)
	}
	return fmt.Sprintf("a2a:direct:%s:%s", agentID, contextID)
}

// NewContextID generates a new unique context ID for a new conversation.
func NewContextID() string {
	return uuid.New().String()
}
