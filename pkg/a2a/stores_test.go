package a2a

import (
	"testing"
)

func TestStoresIssuerCRUD(t *testing.T) {
	store := NewInMemoryIssuerStore()

	// Initially empty.
	if got := store.List(); len(got) != 0 {
		t.Fatalf("expected empty list, got %d items", len(got))
	}

	// Create an issuer.
	iss := TrustedIssuer{
		ID:        "iss-1",
		Name:      "Test IdP",
		Issuer:    "https://idp.example.com",
		JWKSURL:   "https://idp.example.com/.well-known/jwks.json",
		Audience:  "astonish",
		UserClaim: "email",
		OrgID:     "org-1",
	}
	if err := store.Create(iss); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// List returns the issuer.
	list := store.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 issuer, got %d", len(list))
	}
	if list[0].ID != "iss-1" {
		t.Fatalf("expected ID iss-1, got %s", list[0].ID)
	}

	// Duplicate ID returns error.
	if err := store.Create(iss); err == nil {
		t.Fatal("expected error on duplicate ID, got nil")
	}

	// GetByIssuer finds it.
	found, err := store.GetByIssuer("https://idp.example.com")
	if err != nil {
		t.Fatalf("GetByIssuer failed: %v", err)
	}
	if found.ID != "iss-1" {
		t.Fatalf("expected ID iss-1, got %s", found.ID)
	}

	// GetByIssuer with unknown URL returns error.
	_, err = store.GetByIssuer("https://unknown.example.com")
	if err == nil {
		t.Fatal("expected error for unknown issuer, got nil")
	}

	// Delete removes it.
	if err := store.Delete("iss-1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("expected empty list after delete, got %d items", len(got))
	}

	// Delete non-existent returns error.
	if err := store.Delete("iss-1"); err == nil {
		t.Fatal("expected error on delete of non-existent, got nil")
	}
}

func TestStoresAgentCRUD(t *testing.T) {
	store := NewInMemoryAgentAllowStore()

	// Initially empty.
	if got := store.List(); len(got) != 0 {
		t.Fatalf("expected empty list, got %d items", len(got))
	}

	// Create an agent.
	agent := AllowedAgent{
		ID:        "agent-1",
		Name:      "Test Agent",
		ActorSub:  "svc://test-agent",
		IssuerID:  "iss-1",
		OrgID:     "org-1",
		RateLimit: 100,
		MaxTasks:  10,
		Enabled:   true,
	}
	if err := store.Create(agent); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// List returns the agent.
	list := store.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(list))
	}
	if list[0].ID != "agent-1" {
		t.Fatalf("expected ID agent-1, got %s", list[0].ID)
	}

	// Duplicate ID returns error.
	if err := store.Create(agent); err == nil {
		t.Fatal("expected error on duplicate ID, got nil")
	}

	// GetByActorSub finds it.
	found, err := store.GetByActorSub("svc://test-agent")
	if err != nil {
		t.Fatalf("GetByActorSub failed: %v", err)
	}
	if found.ID != "agent-1" {
		t.Fatalf("expected ID agent-1, got %s", found.ID)
	}

	// GetByActorSub with unknown returns error.
	_, err = store.GetByActorSub("svc://unknown")
	if err == nil {
		t.Fatal("expected error for unknown actor_sub, got nil")
	}

	// Delete removes it.
	if err := store.Delete("agent-1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("expected empty list after delete, got %d items", len(got))
	}

	// Delete non-existent returns error.
	if err := store.Delete("agent-1"); err == nil {
		t.Fatal("expected error on delete of non-existent, got nil")
	}
}
