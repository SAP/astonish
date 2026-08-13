package a2a

import "fmt"

// IdentityResult holds the resolved identity for an A2A request.
type IdentityResult struct {
	// ChannelType is always "a2a".
	ChannelType string
	// ExternalID is the identifier used for PlatformResolver lookup.
	// Either the propagated user's email/ID or the agent's linked user ID.
	ExternalID string
	// OrgSlug is set when the agent has a linked org (used as routing hint).
	OrgSlug string
	// TeamSlug is set when the agent has a linked team (used as routing hint).
	TeamSlug string
	// IsPropagated indicates whether identity was propagated from the client.
	IsPropagated bool
}

// ResolveIdentity determines the effective user identity for an A2A request.
//
// Resolution priority:
//  1. Propagated identity from message metadata (if agent allows it)
//  2. Agent's linked user ID (default identity for this agent)
//
// The returned IdentityResult provides the values needed for
// PlatformResolver.ResolveChannelUserWithHint.
func ResolveIdentity(agent *RegisteredAgent, metadata map[string]any) (*IdentityResult, error) {
	if agent == nil {
		return nil, fmt.Errorf("agent is required")
	}

	result := &IdentityResult{
		ChannelType: "a2a",
		OrgSlug:     agent.LinkedOrgSlug,
		TeamSlug:    agent.LinkedTeamSlug,
	}

	// Check for identity propagation in metadata
	if agent.AllowIdentityPropagation && metadata != nil {
		if identity, ok := extractIdentityFromMetadata(metadata); ok {
			result.ExternalID = identity
			result.IsPropagated = true
			return result, nil
		}
	}

	// Fall back to agent's linked user
	if agent.LinkedUserID != "" {
		result.ExternalID = agent.LinkedUserID
		return result, nil
	}

	return nil, fmt.Errorf("no identity available: agent %q has no linked user and no identity was propagated", agent.Name)
}

// extractIdentityFromMetadata looks for identity propagation fields in message metadata.
// Supports nested path: metadata.extensions.identity.user_email or metadata.extensions.identity.user_id
// Also supports flat: metadata.user_email or metadata.user_id
func extractIdentityFromMetadata(metadata map[string]any) (string, bool) {
	// Try nested path: extensions.identity.user_email
	if extensions, ok := metadata["extensions"].(map[string]any); ok {
		if identity, ok := extensions["identity"].(map[string]any); ok {
			if email, ok := identity["user_email"].(string); ok && email != "" {
				return email, true
			}
			if userID, ok := identity["user_id"].(string); ok && userID != "" {
				return userID, true
			}
		}
	}

	// Try flat path
	if email, ok := metadata["user_email"].(string); ok && email != "" {
		return email, true
	}
	if userID, ok := metadata["user_id"].(string); ok && userID != "" {
		return userID, true
	}

	return "", false
}
