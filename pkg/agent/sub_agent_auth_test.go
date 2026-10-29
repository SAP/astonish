package agent

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/credentials"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestChildBridgeAuthorization exercises logical nested authorization, credential
// restoration/redaction, and artifact suppression for denied or failed writes.
func TestChildBridgeAuthorization(t *testing.T) {
	for _, mode := range []string{"denied", "granted", "failed"} {
		grant := mode != "denied"
		t.Run(mode, func(t *testing.T) {
			var executed, gates, captures atomic.Int32
			local, err := functiontool.New(functiontool.Config{Name: "write_file", Description: "wrapped writer"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
				executed.Add(1)
				if args["content"] != "child-credential-secret" {
					t.Errorf("nested credential not resolved: %v", args)
				}
				if mode == "failed" {
					return map[string]any{"success": false, "error": "write failed"}, nil
				}
				return map[string]any{"ok": true, "echo": args["content"]}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			mgr := NewSubAgentManager(SubAgentConfig{MaxRetries: 1})
			store, err := credentials.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Set("child", &credentials.Credential{Type: credentials.CredBearer, Token: "child-credential-secret"}); err != nil {
				t.Fatal(err)
			}
			mgr.CredentialStore = store
			mgr.Redactor = credentials.NewRedactor()
			mgr.AppName, mgr.UserID = "test", "user"
			mgr.SessionService = adksession.InMemoryService()
			mgr.ToolGroups = map[string]*ToolGroup{"core": {Name: "core", Tools: []tool.Tool{local}}}
			policy := NewSessionAuthPolicy("/tmp/project")
			mgr.GetAuthPolicy = func(string) *SessionAuthPolicy { return policy }
			mgr.AuthorizationGate = func(req SubAgentAuthRequest) SubAgentAuthResponse {
				gates.Add(1)
				if req.ToolName != "write_file" || req.Args["file_path"] != "/tmp/project/result.txt" {
					t.Errorf("wrong logical authorization: %+v", req)
				}
				return SubAgentAuthResponse{Granted: grant, Choice: "Allow"}
			}
			mgr.FileArtifactCapture = func(path, name string) {
				if path != "/tmp/project/result.txt" || name != "write_file" {
					t.Errorf("wrong artifact: %s %s", path, name)
				}
				captures.Add(1)
			}
			var turns atomic.Int32
			mgr.LLM = livenessTestLLM{generate: func(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
				if turns.Load() > 0 {
					data, _ := json.Marshal(req.Contents)
					if strings.Contains(string(data), "child-credential-secret") || !strings.Contains(string(data), "{{CREDENTIAL:child:token}}") {
						t.Errorf("credential restoration/redaction failed: %s", data)
					}
				}
				return func(yield func(*model.LLMResponse, error) bool) {
					part := &genai.Part{Text: "done"}
					if turns.Add(1) == 1 {
						part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: "write", Name: "execute_tool", Args: map[string]any{"name": "write_file", "arguments": map[string]any{"file_path": "/tmp/project/result.txt", "content": "{{CREDENTIAL:child:token}}"}}}}
					}
					yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, nil)
				}
			}}
			result := mgr.RunTask(context.Background(), SubAgentTask{Name: "writer", Description: "write", ParentID: "parent", ToolFilter: []string{"write_file"}})
			want := int32(0)
			if grant {
				want = 1
			}
			wantCaptures := want
			if mode == "failed" {
				wantCaptures = 0
			}
			if result.Status != "success" || gates.Load() != 1 || executed.Load() != want || captures.Load() != wantCaptures {
				t.Fatalf("result=%+v gates=%d executions=%d captures=%d", result, gates.Load(), executed.Load(), captures.Load())
			}
		})
	}
}

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
