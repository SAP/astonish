package launcher

import (
	"testing"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestMapSSEToEvents_TextAndTools(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{
		Type: "text",
		Data: `{"text":"Hello"}`,
	}, false)
	if len(evs) != 1 || evs[0].Kind != events.KindText || evs[0].Text != "Hello" {
		t.Fatalf("text: %+v", evs)
	}

	evs = mapSSEToEvents(&client.SSEEvent{
		Type: "tool_call",
		Data: `{"name":"edit_file","id":"1","args":{"path":"a.go"}}`,
	}, false)
	if len(evs) < 1 || evs[0].Kind != events.KindToolCall || evs[0].ToolName != "edit_file" {
		t.Fatalf("tool_call: %+v", evs)
	}
	if evs[0].Args["path"] != "a.go" {
		t.Fatalf("args: %+v", evs[0].Args)
	}

	evs = mapSSEToEvents(&client.SSEEvent{
		Type: "tool_result",
		Data: `{"name":"edit_file","id":"1","result":{"success":true}}`,
	}, false)
	if len(evs) != 1 || evs[0].Kind != events.KindToolResult {
		t.Fatalf("tool_result: %+v", evs)
	}
}

func TestMapSSEToEvents_SessionAndDone(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{
		Type: "session",
		Data: `{"sessionId":"abc-123"}`,
	}, false)
	if len(evs) != 1 || evs[0].SessionID != "abc-123" {
		t.Fatalf("session: %+v", evs)
	}

	evs = mapSSEToEvents(&client.SSEEvent{Type: "done", Data: `{}`}, false)
	if len(evs) != 1 || evs[0].Kind != events.KindDone {
		t.Fatalf("done: %+v", evs)
	}
}

func TestMapSSEToEvents_Approval(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{
		Type: "approval",
		Data: `{"tool":"shell_command","options":["Yes","No"]}`,
	}, false)
	if len(evs) != 1 || evs[0].Kind != events.KindApproval {
		t.Fatalf("approval: %+v", evs)
	}
	if evs[0].ToolName != "shell_command" {
		t.Fatalf("tool name: %q", evs[0].ToolName)
	}
}

func TestMapSSEToEvents_ErrorInfo(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{
		Type: "error_info",
		Data: `{"title":"Sandbox","reason":"timeout"}`,
	}, false)
	if len(evs) != 1 || evs[0].Kind != events.KindErrorInfo {
		t.Fatalf("error_info: %+v", evs)
	}
	if evs[0].ErrorTitle != "Sandbox" {
		t.Fatalf("title: %q", evs[0].ErrorTitle)
	}
}

func TestMapSSEToEvents_SoftDegrade(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{Type: "app_preview", Data: `{}`}, false)
	if len(evs) != 1 || evs[0].Kind != events.KindSystem {
		t.Fatalf("soft degrade: %+v", evs)
	}
}

func TestMapSSEToEvents_SkipToolBoxFrame(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{
		Type: "text",
		Data: `{"text":"╭ tool ╮"}`,
	}, false)
	if len(evs) != 0 {
		t.Fatalf("expected skip toolbox frame, got %+v", evs)
	}
}

func TestMapSSEToEvents_Usage(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{
		Type: "usage",
		Data: `{"input":10,"output":20,"total":30}`,
	}, false)
	if len(evs) != 1 || evs[0].Usage == nil || evs[0].Usage.Total != 30 {
		t.Fatalf("usage: %+v", evs)
	}
}

func TestMapSSEToEvents_UsageTokenFields(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{
		Type: "usage",
		Data: `{"input_tokens":100,"output_tokens":50,"total_tokens":150}`,
	}, false)
	if len(evs) != 1 || evs[0].Usage == nil {
		t.Fatalf("usage: %+v", evs)
	}
	if evs[0].Usage.Input != 100 || evs[0].Usage.Output != 50 || evs[0].Usage.Total != 150 {
		t.Fatalf("usage fields: %+v", evs[0].Usage)
	}
}

func TestMapSSEToEvents_NetworkDenialHint(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{
		Type: "network_denial_hint",
		Data: `{"session_id":"sess-1","sandbox_name":"sandbox-1","denials":[{"chunk_id":"chunk-1","host":"api.example.com","port":443,"binary":"/usr/bin/curl","rationale":"blocked","security_notes":"external","broader_pattern":"*.example.com"}]}`,
	}, false)
	if len(evs) != 1 || evs[0].Kind != events.KindNetworkDenial {
		t.Fatalf("network_denial_hint: %+v", evs)
	}
	if evs[0].SessionID != "sess-1" || evs[0].SandboxName != "sandbox-1" {
		t.Fatalf("metadata: %+v", evs[0])
	}
	if len(evs[0].NetworkDenials) != 1 {
		t.Fatalf("denials: %+v", evs[0].NetworkDenials)
	}
	d := evs[0].NetworkDenials[0]
	if d.ChunkID != "chunk-1" || d.Host != "api.example.com" || d.Port != 443 || d.BroaderPattern != "*.example.com" {
		t.Fatalf("denial: %+v", d)
	}
}
