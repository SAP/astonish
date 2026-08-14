package a2a

import "testing"

func TestResolveIdentity_PropagatedEmail(t *testing.T) {
	agent := &RegisteredAgent{
		Name:                     "TestAgent",
		LinkedUserID:             "agent-user",
		AllowIdentityPropagation: true,
	}
	metadata := map[string]any{
		"extensions": map[string]any{
			"identity": map[string]any{
				"user_email": "alice@example.com",
			},
		},
	}

	result, err := ResolveIdentity(agent, metadata)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExternalID != "alice@example.com" {
		t.Fatalf("expected 'alice@example.com', got %q", result.ExternalID)
	}
	if !result.IsPropagated {
		t.Fatal("expected IsPropagated=true")
	}
}

func TestResolveIdentity_PropagationDisabled(t *testing.T) {
	agent := &RegisteredAgent{
		Name:                     "TestAgent",
		LinkedUserID:             "agent-user",
		AllowIdentityPropagation: false, // disabled
	}
	metadata := map[string]any{
		"extensions": map[string]any{
			"identity": map[string]any{
				"user_email": "alice@example.com",
			},
		},
	}

	result, err := ResolveIdentity(agent, metadata)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should fall back to agent's linked user since propagation is disabled
	if result.ExternalID != "agent-user" {
		t.Fatalf("expected 'agent-user', got %q", result.ExternalID)
	}
	if result.IsPropagated {
		t.Fatal("expected IsPropagated=false")
	}
}

func TestResolveIdentity_FallbackToLinkedUser(t *testing.T) {
	agent := &RegisteredAgent{
		Name:                     "TestAgent",
		LinkedUserID:             "agent-user",
		LinkedOrgSlug:            "myorg",
		LinkedTeamSlug:           "myteam",
		AllowIdentityPropagation: true,
	}

	// No identity in metadata
	result, err := ResolveIdentity(agent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExternalID != "agent-user" {
		t.Fatalf("expected 'agent-user', got %q", result.ExternalID)
	}
	if result.OrgSlug != "myorg" {
		t.Fatalf("expected org 'myorg', got %q", result.OrgSlug)
	}
	if result.TeamSlug != "myteam" {
		t.Fatalf("expected team 'myteam', got %q", result.TeamSlug)
	}
}

func TestResolveIdentity_NoIdentityAvailable(t *testing.T) {
	agent := &RegisteredAgent{
		Name: "TestAgent",
		// No linked user, no propagation
	}

	_, err := ResolveIdentity(agent, nil)
	if err == nil {
		t.Fatal("expected error when no identity available")
	}
}

func TestResolveIdentity_FlatMetadata(t *testing.T) {
	agent := &RegisteredAgent{
		Name:                     "TestAgent",
		LinkedUserID:             "agent-user",
		AllowIdentityPropagation: true,
	}
	metadata := map[string]any{
		"user_email": "bob@example.com",
	}

	result, err := ResolveIdentity(agent, metadata)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExternalID != "bob@example.com" {
		t.Fatalf("expected 'bob@example.com', got %q", result.ExternalID)
	}
}

func TestResolveIdentity_NilAgent(t *testing.T) {
	_, err := ResolveIdentity(nil, nil)
	if err == nil {
		t.Fatal("expected error for nil agent")
	}
}
