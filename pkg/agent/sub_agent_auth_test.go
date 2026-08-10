package agent

import (
	"sync"
	"testing"
	"time"
)

// TestSubAgentAuthGate_SafeToolPassesThrough verifies that safe tools
// (read_file, grep_search) don't trigger the authorization gate.
func TestSubAgentAuthGate_SafeToolPassesThrough(t *testing.T) {
	gateCalled := false
	gate := func(req SubAgentAuthRequest) SubAgentAuthResponse {
		gateCalled = true
		return SubAgentAuthResponse{Granted: false, Choice: "Deny"}
	}

	policy := NewSessionAuthPolicy("/tmp/project")

	// Simulate what the sub-agent BeforeToolCallback does for a safe tool.
	toolName := "read_file"
	if !RequiresToolAuthorization(toolName, false) {
		// Safe tool — gate should not be called.
	} else {
		gate(SubAgentAuthRequest{ToolName: toolName})
	}

	if gateCalled {
		t.Fatal("gate should not be called for safe tools")
	}
	_ = policy // policy not needed for safe tools
}

// TestSubAgentAuthGate_ToolAlreadyAuthorized verifies that when the policy
// has an active "Always Allow" grant, the gate is not invoked.
func TestSubAgentAuthGate_ToolAlreadyAuthorized(t *testing.T) {
	gateCalled := false
	gate := func(req SubAgentAuthRequest) SubAgentAuthResponse {
		gateCalled = true
		return SubAgentAuthResponse{Granted: false, Choice: "Deny"}
	}

	policy := NewSessionAuthPolicy("/tmp/project")
	policy.GrantAllToolsSession() // Simulate "Always Allow" from a previous prompt.

	toolName := "shell_command"
	if RequiresToolAuthorization(toolName, false) {
		if policy.ToolAuthorized(toolName) {
			// Already authorized — gate should not be called.
		} else {
			gate(SubAgentAuthRequest{ToolName: toolName})
		}
	}

	if gateCalled {
		t.Fatal("gate should not be called when tools are already authorized")
	}
}

// TestSubAgentAuthGate_ToolDenied verifies that when the gate returns denied,
// the tool gets a denial result.
func TestSubAgentAuthGate_ToolDenied(t *testing.T) {
	gate := func(req SubAgentAuthRequest) SubAgentAuthResponse {
		return SubAgentAuthResponse{Granted: false, Choice: "Deny"}
	}

	policy := NewSessionAuthPolicy("/tmp/project")
	toolName := "shell_command"

	var result map[string]any
	if RequiresToolAuthorization(toolName, false) && !policy.ToolAuthorized(toolName) {
		resp := gate(SubAgentAuthRequest{
			Kind:     "tool",
			ToolName: toolName,
		})
		if !resp.Granted {
			result = map[string]any{
				"status": "authorization_denied",
				"error":  AuthorizationDeniedMessage(toolName),
			}
		}
	}

	if result == nil {
		t.Fatal("expected denial result")
	}
	if result["status"] != "authorization_denied" {
		t.Fatalf("expected authorization_denied status, got %v", result["status"])
	}
}

// TestSubAgentAuthGate_ToolGrantedOnce verifies that a one-shot grant works
// and is consumed on use.
func TestSubAgentAuthGate_ToolGrantedOnce(t *testing.T) {
	policy := NewSessionAuthPolicy("/tmp/project")
	toolName := "shell_command"

	// Grant once.
	policy.GrantToolOnce(toolName)

	// First check: should be authorized.
	if !policy.ToolAuthorized(toolName) {
		t.Fatal("expected tool to be authorized after GrantToolOnce")
	}

	// Second check: grant consumed, should NOT be authorized.
	if policy.ToolAuthorized(toolName) {
		t.Fatal("expected tool grant to be consumed after first use")
	}
}

// TestSubAgentAuthGate_FolderOutOfScope verifies that paths outside the
// project root trigger the folder gate.
func TestSubAgentAuthGate_FolderOutOfScope(t *testing.T) {
	gateCalled := false
	var gateReq SubAgentAuthRequest
	gate := func(req SubAgentAuthRequest) SubAgentAuthResponse {
		gateCalled = true
		gateReq = req
		return SubAgentAuthResponse{Granted: true, Choice: "Allow"}
	}

	policy := NewSessionAuthPolicy("/tmp/project")
	args := map[string]any{"path": "/etc/passwd"}

	outside := policy.OutOfScopePaths(args)
	if len(outside) > 0 {
		gate(SubAgentAuthRequest{
			Kind:            "folder",
			ToolName:        "read_file",
			Args:            args,
			OutOfScopePaths: outside,
		})
	}

	if !gateCalled {
		t.Fatal("gate should be called for out-of-scope paths")
	}
	if gateReq.Kind != "folder" {
		t.Fatalf("expected folder kind, got %q", gateReq.Kind)
	}
	if len(gateReq.OutOfScopePaths) == 0 {
		t.Fatal("expected out-of-scope paths in request")
	}
}

// TestSubAgentAuthGate_NilGatePassesThrough verifies that when
// AuthorizationGate is nil (platform mode), all tools pass through.
func TestSubAgentAuthGate_NilGatePassesThrough(t *testing.T) {
	mgr := NewSubAgentManager(SubAgentConfig{})
	// AuthorizationGate is nil by default.
	if mgr.AuthorizationGate != nil {
		t.Fatal("AuthorizationGate should be nil by default")
	}
	if mgr.GetAuthPolicy != nil {
		t.Fatal("GetAuthPolicy should be nil by default")
	}
	// When nil, the sub-agent BeforeToolCallback block is skipped entirely
	// (guarded by `if m.AuthorizationGate != nil && m.GetAuthPolicy != nil`).
}

// TestSubAgentAuthGate_ConcurrentRequests verifies that multiple sub-agents
// can serialize their auth requests without deadlock.
func TestSubAgentAuthGate_ConcurrentRequests(t *testing.T) {
	// Simulate the channel-based gate: requests are serialized through a
	// single-buffered channel pair.
	reqCh := make(chan SubAgentAuthRequest, 1)
	respCh := make(chan SubAgentAuthResponse, 1)

	gate := func(req SubAgentAuthRequest) SubAgentAuthResponse {
		reqCh <- req
		return <-respCh
	}

	const numAgents = 5
	var wg sync.WaitGroup
	results := make([]SubAgentAuthResponse, numAgents)

	// Launch concurrent sub-agents.
	for i := range numAgents {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = gate(SubAgentAuthRequest{
				TaskName: "agent",
				Kind:     "tool",
				ToolName: "shell_command",
			})
		}(i)
	}

	// Simulate the TUI responding to each request sequentially.
	go func() {
		for range numAgents {
			<-reqCh // read the request
			respCh <- SubAgentAuthResponse{Granted: true, Choice: "Allow"}
		}
	}()

	// Wait with timeout to detect deadlock.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All agents completed.
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: concurrent sub-agent auth requests did not complete")
	}

	for i, r := range results {
		if !r.Granted {
			t.Fatalf("agent %d: expected granted, got denied", i)
		}
	}
}

// TestNormalizeAuthChoice verifies the exported NormalizeAuthChoice function.
func TestNormalizeAuthChoice(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Allow", "once"},
		{"Always Allow", "broad2"},
		{"Deny", "deny"},
		{"1", "once"},
		{"2", "broad2"},
		{"3", "deny"},
		{"y", "once"},
		{"n", "deny"},
		{"yes", "once"},
		{"no", "deny"},
	}
	for _, tt := range tests {
		got := NormalizeAuthChoice(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeAuthChoice(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
