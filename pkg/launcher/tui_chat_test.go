package launcher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/skills"
	"github.com/SAP/astonish/pkg/tui/backend"
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

func TestMapSSEToEvents_ChatQuestionYesNo(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{
		Type: "chat_question",
		Data: `{"questionId":"q1","kind":"yesno","prompt":"Ship it?"}`,
	}, false)
	if len(evs) != 1 || evs[0].Kind != events.KindText {
		t.Fatalf("chat_question yesno: %+v", evs)
	}
	text := evs[0].Text
	for _, want := range []string{"Ship it?", "1. Yes", "2. No", "Reply with the number"} {
		if !strings.Contains(text, want) {
			t.Fatalf("chat_question yesno missing %q in %q", want, text)
		}
	}
}

func TestMapSSEToEvents_ChatQuestionSelect(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{
		Type: "chat_question",
		Data: `{"questionId":"q2","kind":"select","prompt":"Pick a theme","options":[{"id":"a","label":"Aurora","description":"Cool blues"},{"id":"b","label":"Ember"}]}`,
	}, false)
	if len(evs) != 1 || evs[0].Kind != events.KindText {
		t.Fatalf("chat_question select: %+v", evs)
	}
	text := evs[0].Text
	for _, want := range []string{"Pick a theme", "1. Aurora", "Cool blues", "2. Ember", "Reply with the number"} {
		if !strings.Contains(text, want) {
			t.Fatalf("chat_question select missing %q in %q", want, text)
		}
	}
}

func TestMapSSEToEvents_ArtifactMetadata(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{
		Type: "artifact",
		Data: `{"path":"/tmp/report.md","fileName":"report.md","fileType":"Markdown","toolName":"write_file","isReport":true,"reportTitle":"Quarterly Report"}`,
	}, false)
	if len(evs) != 1 || evs[0].Kind != events.KindArtifact || evs[0].Artifact == nil {
		t.Fatalf("artifact: %+v", evs)
	}
	artifact := evs[0].Artifact
	if artifact.Path != "/tmp/report.md" || artifact.FileName != "report.md" || artifact.FileType != "Markdown" || !artifact.IsReport || artifact.ReportTitle != "Quarterly Report" {
		t.Fatalf("artifact metadata: %+v", artifact)
	}
}

func TestMapSSEToEvents_ReportMarker(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{
		Type: "report_marker",
		Data: `{"path":"/tmp/report.md","title":"Quarterly Report"}`,
	}, false)
	if len(evs) != 1 || evs[0].Kind != events.KindReportMarker || evs[0].Artifact == nil {
		t.Fatalf("report marker: %+v", evs)
	}
	if evs[0].Artifact.Path != "/tmp/report.md" || !evs[0].Artifact.IsReport || evs[0].Artifact.ReportTitle != "Quarterly Report" {
		t.Fatalf("report marker artifact: %+v", evs[0].Artifact)
	}
}

func TestMapSSEToEvents_DocsUpdateIsStudioOnly(t *testing.T) {
	evs := mapSSEToEvents(&client.SSEEvent{
		Type: "docs_update",
		Data: `{"type":"slides","deckSlug":"migration","action":"slide_written","slideIndex":2,"totalSlides":3}`,
	}, false)
	if len(evs) != 0 {
		t.Fatalf("terminal client must ignore docs_update SSE, got %+v", evs)
	}
}

func TestStudioMessagesToHistorySkipsDocsUpdate(t *testing.T) {
	hist := studioMessagesToHistory([]client.StudioMessage{{
		Type: "docs_update",
	}})
	if len(hist) != 0 {
		t.Fatalf("terminal history must skip Studio-only docs_update, got %+v", hist)
	}
}

func TestStudioDetailToHistoryIncludesArtifacts(t *testing.T) {
	hist := studioDetailToHistory(&client.SessionDetail{
		Messages:  []client.StudioMessage{{Type: "agent", Content: "done"}},
		Artifacts: []client.ArtifactInfo{{Path: "/tmp/report.md", FileName: "report.md", FileType: "Markdown", IsReport: true}},
	})
	if len(hist) != 2 {
		t.Fatalf("history len=%d want 2: %+v", len(hist), hist)
	}
	if hist[1].Kind != "artifact" || hist[1].Artifact == nil || hist[1].Artifact.Path != "/tmp/report.md" {
		t.Fatalf("artifact history entry: %+v", hist[1])
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

func TestPlatformBackendNewSessionResetsModelToCascadeDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/settings/providers/effective":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"default_provider": "cascade-provider",
				"default_model":    "cascade-model",
				"providers":        map[string]any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	b := &platformBackend{
		client:          client.NewWithConfig(srv.URL),
		sessionID:       "sess-custom",
		provider:        "openai",
		model:           "gpt-4o",
		pendingProvider: "openai",
		pendingModel:    "gpt-4o",
		resumed:         true,
	}

	b.NewSession()

	info := b.Info()
	if info.SessionID != "" {
		t.Fatalf("sessionID=%q, want empty", info.SessionID)
	}
	if info.Provider != "cascade-provider" || info.Model != "cascade-model" {
		t.Fatalf("model after /new = %s/%s, want cascade-provider/cascade-model", info.Provider, info.Model)
	}
	b.mu.Lock()
	pendingP, pendingM := b.pendingProvider, b.pendingModel
	b.mu.Unlock()
	if pendingP != "" || pendingM != "" {
		t.Fatalf("pending pin should be cleared, got %q/%q", pendingP, pendingM)
	}
}

func TestPlatformBackendResumeSessionLoadsSessionModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/studio/sessions/sess-a":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       "sess-a",
				"title":    "A",
				"messages": []any{},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/studio/sessions/sess-a/model-status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pinnedProvider":    "openai",
				"pinnedModel":       "gpt-4o",
				"effectiveProvider": "openai",
				"effectiveModel":    "gpt-4o",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/studio/sessions/sess-b":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       "sess-b",
				"title":    "B",
				"messages": []any{},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/studio/sessions/sess-b/model-status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pinnedProvider":    "",
				"pinnedModel":       "",
				"effectiveProvider": "cascade-provider",
				"effectiveModel":    "cascade-model",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	b := &platformBackend{
		client:   client.NewWithConfig(srv.URL),
		provider: "stale-provider",
		model:    "stale-model",
	}

	if _, err := b.ResumeSession(context.Background(), "sess-a"); err != nil {
		t.Fatalf("resume A: %v", err)
	}
	info := b.Info()
	if info.SessionID != "sess-a" {
		t.Fatalf("sessionID=%q", info.SessionID)
	}
	if info.Provider != "openai" || info.Model != "gpt-4o" {
		t.Fatalf("after resume A = %s/%s, want openai/gpt-4o", info.Provider, info.Model)
	}

	if _, err := b.ResumeSession(context.Background(), "sess-b"); err != nil {
		t.Fatalf("resume B: %v", err)
	}
	info = b.Info()
	if info.SessionID != "sess-b" {
		t.Fatalf("sessionID=%q", info.SessionID)
	}
	if info.Provider != "cascade-provider" || info.Model != "cascade-model" {
		t.Fatalf("after resume B = %s/%s, want cascade-provider/cascade-model", info.Provider, info.Model)
	}

	// Switching back to the custom-model session must restore its pin.
	if _, err := b.ResumeSession(context.Background(), "sess-a"); err != nil {
		t.Fatalf("resume A again: %v", err)
	}
	info = b.Info()
	if info.Provider != "openai" || info.Model != "gpt-4o" {
		t.Fatalf("after resume A again = %s/%s, want openai/gpt-4o", info.Provider, info.Model)
	}
}

func TestPlatformBackendDeleteActiveSessionResetsModelToCascade(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/studio/sessions/sess-custom":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/settings/providers/effective":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"default_provider": "cascade-provider",
				"default_model":    "cascade-model",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	b := &platformBackend{
		client:    client.NewWithConfig(srv.URL),
		sessionID: "sess-custom",
		provider:  "openai",
		model:     "gpt-4o",
	}
	if err := b.DeleteSession(context.Background(), "sess-custom"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	info := b.Info()
	if info.SessionID != "" {
		t.Fatalf("sessionID=%q", info.SessionID)
	}
	if info.Provider != "cascade-provider" || info.Model != "cascade-model" {
		t.Fatalf("after delete active = %s/%s", info.Provider, info.Model)
	}
}

func TestLazyCodeBackendForwardsLocalSkills(t *testing.T) {
	inner := &localAgentBackend{filesystemSkills: []skills.Skill{{Name: "local", Description: "Local", Source: "project"}}}
	b := &lazyCodeBackend{inner: inner}

	got, err := b.ListLocalSkills(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The picker merges BuiltinSkillsForCode() (which includes the on-demand
	// "slides" skill) with the filesystem skill, sorted case-insensitively.
	// generative-ui is excluded from code-mode builtins.
	if len(got) != 2 || got[0].Name != "local" || got[1].Name != "slides" {
		t.Fatalf("forwarded skills = %+v", got)
	}
	var _ backend.LocalSkillsBackend = b
}
