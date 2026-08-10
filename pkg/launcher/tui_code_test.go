package launcher

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

// captureEmit returns an emit closure that mirrors the runtime bridge in
// RunTurn: it marshals the payload, wraps it as a client.SSEEvent, runs it
// through the shared mapSSEToEvents translator, and appends the results.
func captureEmit(out *[]events.Event, debug bool) func(string, map[string]any) {
	return func(eventType string, data map[string]any) {
		raw, err := json.Marshal(data)
		if err != nil {
			return
		}
		sev := &client.SSEEvent{Type: eventType, Data: string(raw)}
		*out = append(*out, mapSSEToEvents(sev, debug)...)
	}
}

// TestForceHostExecution_DisablesSandbox is the core code-mode invariant:
// after RunCodeTUI prepares the config, the sandbox must be off so tools run
// directly on the host filesystem.
func TestForceHostExecution_DisablesSandbox(t *testing.T) {
	cfg := &config.AppConfig{}
	// Default: nil Enabled means "enabled".
	if !sandbox.IsSandboxEnabled(&cfg.Sandbox) {
		t.Fatal("precondition: expected sandbox enabled by default")
	}
	forceHostExecution(cfg)
	if sandbox.IsSandboxEnabled(&cfg.Sandbox) {
		t.Fatal("code mode must disable the sandbox for host execution")
	}
}

func TestForceHostExecution_NilSafe(t *testing.T) {
	// Must not panic on nil.
	forceHostExecution(nil)
}

// TestRedirectLogsForTUI_SilencesStdlibLog verifies that while the TUI is
// running, stray log.Printf output (e.g. ADK's "unknown agent" warnings) is
// kept off the terminal, and that the previous writer is restored afterward.
func TestRedirectLogsForTUI_SilencesStdlibLog(t *testing.T) {
	// Install a known sink so we can detect writes before/after redirection.
	var before bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&before)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	restore := redirectLogsForTUI(false)

	// This mimics ADK's runner writing to the standard logger mid-TUI.
	log.Printf("Event from an unknown agent: chat, event id: abc123")
	if before.Len() != 0 {
		t.Fatalf("log output leaked to the pre-TUI sink: %q", before.String())
	}

	restore()

	// After restore, logging goes back to our sink.
	log.Printf("after restore")
	if !strings.Contains(before.String(), "after restore") {
		t.Fatalf("expected logging restored after TUI, got %q", before.String())
	}
}

// collectEvents runs the localAgentBackend's emit closure equivalent: it feeds
// a (type, data) payload through the same shared translator used at runtime and
// returns the resulting events. We drive processStateDelta directly since it is
// the pure translation surface.
func newTestBackend() *localAgentBackend {
	return &localAgentBackend{
		sessionSvc: common.NewAutoInitService(adksession.InMemoryService()),
		appConfig:  &config.AppConfig{},
	}
}

func TestProcessStateDelta_Approval(t *testing.T) {
	b := newTestBackend()
	var got []events.Event
	emit := captureEmit(&got, b.debug)

	b.processStateDelta(map[string]any{
		"approval_options": []string{"Yes", "No"},
		"approval_tool":    "shell_command",
		"approval_args":    map[string]any{"command": "rm -rf /tmp/test"},
	}, emit)

	if len(got) != 1 || got[0].Kind != events.KindApproval {
		t.Fatalf("expected one approval event, got %+v", got)
	}
	if got[0].ToolName != "shell_command" {
		t.Errorf("tool name = %q", got[0].ToolName)
	}
	if got[0].Args == nil {
		t.Fatal("expected Args to be non-nil")
	}
	if cmd, ok := got[0].Args["command"].(string); !ok || cmd != "rm -rf /tmp/test" {
		t.Errorf("Args[command] = %v, want %q", got[0].Args["command"], "rm -rf /tmp/test")
	}
}

func TestProcessStateDelta_AutoApproved(t *testing.T) {
	b := newTestBackend()
	var got []events.Event
	emit := captureEmit(&got, b.debug)

	b.processStateDelta(map[string]any{
		"auto_approved": true,
		"approval_tool": "edit_file",
	}, emit)

	found := false
	for _, ev := range got {
		if ev.Kind == events.KindAutoApproved {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an auto_approved event, got %+v", got)
	}
}

func TestProcessStateDelta_SpinnerThinking(t *testing.T) {
	b := newTestBackend()
	var got []events.Event
	emit := captureEmit(&got, b.debug)

	b.processStateDelta(map[string]any{"_spinner_text": "Compiling…"}, emit)

	found := false
	for _, ev := range got {
		// The shared translator maps a "thinking" payload to a status event.
		if ev.Kind == events.KindStatus && ev.Text == "Compiling…" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a status event with spinner text, got %+v", got)
	}
}

func TestReadArtifactContent_ReadsHostFile(t *testing.T) {
	dir := t.TempDir()
	rel := "notes.txt"
	want := "hello from host"
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(want), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	b := newTestBackend()
	b.workingDir = dir

	got, err := b.ReadArtifactContent(context.Background(), "", rel)
	if err != nil {
		t.Fatalf("ReadArtifactContent: %v", err)
	}
	if got.Content != want {
		t.Errorf("content = %q, want %q", got.Content, want)
	}
	if got.Path != rel {
		t.Errorf("path = %q, want %q", got.Path, rel)
	}
}

func TestReadArtifactContent_EmptyPathErrors(t *testing.T) {
	b := newTestBackend()
	if _, err := b.ReadArtifactContent(context.Background(), "", "  "); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestListProviders_FromConfig(t *testing.T) {
	b := newTestBackend()
	b.appConfig.Providers = map[string]config.ProviderConfig{
		"openai":    {},
		"anthropic": {},
	}
	names, err := b.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 providers, got %v", names)
	}
	// Sorted output.
	if names[0] != "anthropic" || names[1] != "openai" {
		t.Errorf("expected sorted [anthropic openai], got %v", names)
	}
}

func TestInfo_ReportsCodeMode(t *testing.T) {
	b := newTestBackend()
	b.provider = "openai"
	b.model = "gpt-4o"
	b.autoApprove = true
	b.configured = true
	info := b.Info()
	if info.Mode != "code" {
		t.Errorf("Mode = %q, want code", info.Mode)
	}
	if info.Provider != "openai" || info.Model != "gpt-4o" {
		t.Errorf("provider/model = %q/%q", info.Provider, info.Model)
	}
	if !info.AutoApprove {
		t.Error("expected AutoApprove true")
	}
	// A configured backend must not show the "no model" hint.
	for _, n := range info.Notices {
		if strings.Contains(n, "/model") {
			t.Errorf("configured backend should not hint /model, got notice: %q", n)
		}
	}
}

// TestInfo_UnconfiguredHintsModelPicker verifies code mode opens without a
// model and nudges the user toward /model.
func TestInfo_UnconfiguredHintsModelPicker(t *testing.T) {
	b := newTestBackend() // configured defaults to false
	info := b.Info()
	found := false
	for _, n := range info.Notices {
		if strings.Contains(n, "/model") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a /model hint in notices, got %v", info.Notices)
	}
}

// TestRunTurn_UnconfiguredGuidesUser verifies that starting a turn without a
// configured model does not attempt generation; it emits a guidance system
// message and ends the turn cleanly.
func TestRunTurn_UnconfiguredGuidesUser(t *testing.T) {
	b := newTestBackend() // not configured
	ch, err := b.RunTurn(context.Background(), "hello", backend.TurnOptions{})
	if err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}
	var got []events.Event
	for ev := range ch {
		got = append(got, ev)
	}
	if len(got) != 1 || got[0].Kind != events.KindSystem {
		t.Fatalf("expected a single system guidance event, got %+v", got)
	}
	if !strings.Contains(got[0].Text, "/model") {
		t.Errorf("expected guidance to mention /model, got %q", got[0].Text)
	}
}

// TestSessionLifecycle exercises the in-process session plumbing against a real
// ADK in-memory session service: create, list, delete, and reset. This is the
// host-side session machinery code mode relies on (no platform, no HTTP).
func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend()

	// No active session initially.
	if got, _ := b.ListSessions(ctx); len(got) != 0 {
		t.Fatalf("expected no sessions initially, got %v", got)
	}

	id, isNew, err := b.ensureSession(ctx)
	if err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	if id == "" || !isNew {
		t.Fatalf("expected a new session id, got id=%q isNew=%v", id, isNew)
	}

	// A second call reuses the same session.
	id2, isNew2, err := b.ensureSession(ctx)
	if err != nil {
		t.Fatalf("ensureSession(2): %v", err)
	}
	if id2 != id || isNew2 {
		t.Fatalf("expected reuse of %q, got id=%q isNew=%v", id, id2, isNew2)
	}

	sessions, err := b.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != id {
		t.Fatalf("expected the active session listed, got %v", sessions)
	}

	// Empty history for a fresh session.
	hist, err := b.LoadHistory(ctx)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(hist) != 0 {
		t.Fatalf("expected empty history, got %v", hist)
	}

	// NewSession clears the active session without deleting stored state.
	b.NewSession()
	if b.sessionID != "" {
		t.Fatalf("NewSession should clear the active id, got %q", b.sessionID)
	}

	// Delete removes the underlying session and is idempotent on the active id.
	if err := b.DeleteSession(ctx, id); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
}

func TestProviderTypes_Catalog(t *testing.T) {
	b := newTestBackend()
	types := b.ProviderTypes()
	if len(types) == 0 {
		t.Fatal("expected a non-empty provider type catalog")
	}
	byID := map[string]backend.ProviderTypeInfo{}
	for _, ty := range types {
		byID[ty.ID] = ty
	}
	// openai must require an api_key field.
	openai, ok := byID["openai"]
	if !ok {
		t.Fatal("expected openai in catalog")
	}
	foundKey := false
	for _, f := range openai.Fields {
		if f.Key == "api_key" && f.Secret {
			foundKey = true
		}
	}
	if !foundKey {
		t.Error("openai should have a secret api_key field")
	}
	// ollama should not require an api_key (local provider).
	ollama, ok := byID["ollama"]
	if !ok {
		t.Fatal("expected ollama in catalog")
	}
	for _, f := range ollama.Fields {
		if f.Key == "api_key" {
			t.Error("ollama should not require an api_key")
		}
	}
}

// TestProviderAdmin_AddListRemove exercises the full add → list → remove cycle
// against an isolated config directory (XDG_CONFIG_HOME) and asserts the change
// is persisted to config.yaml — the file-only, no-DB model of code mode.
func TestProviderAdmin_AddListRemove(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	ctx := context.Background()

	b := newTestBackend()

	// Initially empty.
	insts, err := b.ListProviderInstances(ctx)
	if err != nil {
		t.Fatalf("ListProviderInstances: %v", err)
	}
	if len(insts) != 0 {
		t.Fatalf("expected no providers, got %v", insts)
	}

	// Add an openai instance.
	if err := b.AddProvider(ctx, "my-openai", "openai", map[string]string{"api_key": "sk-test-123"}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	// In-memory reflects the addition, with the api_key stored in config (plaintext).
	if got := b.appConfig.Providers["my-openai"]; got == nil || got["type"] != "openai" || got["api_key"] != "sk-test-123" {
		t.Fatalf("unexpected in-memory provider config: %+v", got)
	}

	insts, err = b.ListProviderInstances(ctx)
	if err != nil {
		t.Fatalf("ListProviderInstances after add: %v", err)
	}
	if len(insts) != 1 || insts[0].Name != "my-openai" || insts[0].Type != "openai" {
		t.Fatalf("expected one openai instance, got %v", insts)
	}

	// Persisted to config.yaml on disk.
	saved, err := config.LoadAppConfig()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if p := saved.Providers["my-openai"]; p == nil || p["api_key"] != "sk-test-123" {
		t.Fatalf("provider not persisted to config.yaml: %+v", p)
	}

	// Required-field validation.
	if err := b.AddProvider(ctx, "bad", "openai", map[string]string{}); err == nil {
		t.Error("expected error when api_key is missing")
	}

	// Removing clears the default if it pointed at the removed instance.
	b.appConfig.General.DefaultProvider = "my-openai"
	b.appConfig.General.DefaultModel = "gpt-4o"
	if err := b.RemoveProvider(ctx, "my-openai"); err != nil {
		t.Fatalf("RemoveProvider: %v", err)
	}
	if b.appConfig.Providers["my-openai"] != nil {
		t.Error("provider should be removed from memory")
	}
	if b.appConfig.General.DefaultProvider != "" || b.appConfig.General.DefaultModel != "" {
		t.Error("default provider/model should be cleared when the default is removed")
	}

	// Removing a non-existent provider errors.
	if err := b.RemoveProvider(ctx, "nope"); err == nil {
		t.Error("expected error removing unknown provider")
	}
}

// --- Session persistence & per-directory scoping (code mode) ---

// newFileStoreBackend builds a localAgentBackend backed by a real on-disk
// FileStore scoped to the given userID, mirroring how RunCodeTUI wires it.
func newFileStoreBackend(t *testing.T, dir, userID string) *localAgentBackend {
	t.Helper()
	fs, err := persistentsession.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return &localAgentBackend{
		sessionSvc: common.NewAutoInitService(fs),
		fileStore:  fs,
		userID:     userID,
		appConfig:  &config.AppConfig{},
	}
}

// seedSession creates a code-mode session for the backend's user and appends a
// user message, returning the session ID. It also sets a title the way RunTurn
// does, so ListSessions has something meaningful to show.
func seedSession(t *testing.T, b *localAgentBackend, text string) string {
	t.Helper()
	ctx := context.Background()
	resp, err := b.sessionSvc.Create(ctx, &adksession.CreateRequest{
		AppName: codeAppName,
		UserID:  b.effectiveUserID(),
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	sess := resp.Session
	ev := &adksession.Event{
		ID:     "e1",
		Author: "user",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText(text, genai.RoleUser),
		},
		Timestamp: time.Now(),
	}
	if err := b.sessionSvc.AppendEvent(ctx, sess, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if title := deriveSessionTitle(text); title != "" {
		if err := b.fileStore.SetSessionTitle(ctx, sess.ID(), title); err != nil {
			t.Fatalf("SetSessionTitle: %v", err)
		}
	}
	return sess.ID()
}

// TestEmitEstimatedContext_ReportsWhenNoProviderUsage verifies the header
// fallback: when the provider returns no usage metadata, code mode estimates
// the context fill from the session contents and emits an (estimated) usage
// event so the header shows real utilization instead of "Context 0".
func TestEmitEstimatedContext_ReportsWhenNoProviderUsage(t *testing.T) {
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	ctx := context.Background()
	// Seed a session with a sizable message so the estimate is clearly > 0.
	sessionID := seedSession(t, b, strings.Repeat("some project analysis text ", 200))

	var got []events.Event
	emit := captureEmit(&got, b.debug)
	b.emitEstimatedContext(ctx, sessionID, emit)

	var usageEv *events.Event
	for i := range got {
		if got[i].Kind == events.KindUsage {
			usageEv = &got[i]
		}
	}
	if usageEv == nil || usageEv.Usage == nil {
		t.Fatalf("expected an estimated usage event, got %+v", got)
	}
	if !usageEv.Usage.Estimated {
		t.Error("expected the usage event to be marked Estimated")
	}
	if usageEv.Usage.Input <= 0 {
		t.Errorf("expected a positive estimated context size, got %d", usageEv.Usage.Input)
	}
}

// countUsageEvents returns how many KindUsage events are present in evs.
func countUsageEvents(evs []events.Event) int {
	n := 0
	for i := range evs {
		if evs[i].Kind == events.KindUsage {
			n++
		}
	}
	return n
}

// TestMaybeEmitEstimatedContext_EmitsMidTurn verifies that the throttled
// mid-turn estimator emits an updated context reading between tool steps, so
// the header's "Context" figure advances during a long turn instead of only at
// turn end.
func TestMaybeEmitEstimatedContext_EmitsMidTurn(t *testing.T) {
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	ctx := context.Background()
	sessionID := seedSession(t, b, strings.Repeat("some project analysis text ", 200))

	var got []events.Event
	emit := captureEmit(&got, b.debug)

	// First call fires immediately (lastCtxEstimate is zero).
	b.maybeEmitEstimatedContext(ctx, sessionID, emit)
	if n := countUsageEvents(got); n != 1 {
		t.Fatalf("first maybeEmitEstimatedContext: usage events = %d, want 1", n)
	}
	if !got[len(got)-1].Usage.Estimated {
		t.Error("mid-turn estimate should be marked Estimated")
	}
}

// TestMaybeEmitEstimatedContext_Throttles verifies that back-to-back mid-turn
// estimates within contextEstimateInterval do not re-scan the session or emit
// a second event — a many-tool turn stays cheap.
func TestMaybeEmitEstimatedContext_Throttles(t *testing.T) {
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	ctx := context.Background()
	sessionID := seedSession(t, b, strings.Repeat("some project analysis text ", 200))

	var got []events.Event
	emit := captureEmit(&got, b.debug)

	// Rapid successive calls: only the first should emit.
	for i := 0; i < 5; i++ {
		b.maybeEmitEstimatedContext(ctx, sessionID, emit)
	}
	if n := countUsageEvents(got); n != 1 {
		t.Fatalf("throttled maybeEmitEstimatedContext: usage events = %d, want 1", n)
	}

	// After the interval elapses, a further call emits again.
	b.mu.Lock()
	b.lastCtxEstimate = time.Now().Add(-2 * contextEstimateInterval)
	b.mu.Unlock()
	b.maybeEmitEstimatedContext(ctx, sessionID, emit)
	if n := countUsageEvents(got); n != 2 {
		t.Fatalf("post-interval maybeEmitEstimatedContext: usage events = %d, want 2", n)
	}
}

// TestResumeSession_PopulatesContextTokens verifies that resuming a session
// estimates its context occupancy up front, so Info().ContextTokens is > 0
// immediately — the header shows real utilization on load rather than
// "Context 0" until the next turn.
func TestResumeSession_PopulatesContextTokens(t *testing.T) {
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	ctx := context.Background()
	sessionID := seedSession(t, b, strings.Repeat("some project analysis text ", 200))

	// Before resume, no context is known.
	if got := b.Info().ContextTokens; got != 0 {
		t.Fatalf("ContextTokens before resume = %d, want 0", got)
	}

	if _, err := b.ResumeSession(ctx, sessionID); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}

	info := b.Info()
	if info.ContextTokens <= 0 {
		t.Fatalf("ContextTokens after resume = %d, want > 0", info.ContextTokens)
	}
	if !info.IsResumed {
		t.Error("expected IsResumed=true after resume")
	}
}

// appendEvent appends a single ADK event with the given content to the session.
func appendEvent(t *testing.T, b *localAgentBackend, sess adksession.Session, author string, content *genai.Content) {
	t.Helper()
	ev := &adksession.Event{
		Author:      author,
		LLMResponse: adkmodel.LLMResponse{Content: content},
		Timestamp:   time.Now(),
	}
	if err := b.sessionSvc.AppendEvent(context.Background(), sess, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
}

// getSession fetches the mutable session handle for appending events.
func getSession(t *testing.T, b *localAgentBackend, id string) adksession.Session {
	t.Helper()
	resp, err := b.sessionSvc.Get(context.Background(), &adksession.GetRequest{
		AppName:   codeAppName,
		UserID:    b.effectiveUserID(),
		SessionID: id,
	})
	if err != nil || resp == nil || resp.Session == nil {
		t.Fatalf("Get session: %v", err)
	}
	return resp.Session
}

// TestLoadHistory_AnnouncePlanReconstructsPlanDocument verifies that an
// announce_plan call/response pair in the session is reconstructed into a
// rendered plan document (agent text starting with "# Execution Plan"), matching
// the synthetic text event emitted during live execution — instead of leaking
// the announce_plan tool into the activity fold.
func TestLoadHistory_AnnouncePlanReconstructsPlanDocument(t *testing.T) {
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	ctx := context.Background()

	id := seedSession(t, b, "build a feature")
	sess := getSession(t, b, id)

	// announce_plan FunctionCall (role model).
	callContent := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{
				ID:   "call-1",
				Name: "announce_plan",
				Args: map[string]any{
					"goal": "Build the feature",
					"steps": []any{
						map[string]any{"name": "phase-one", "description": "Do the first thing"},
						map[string]any{"name": "phase-two", "description": "Do the second thing"},
					},
				},
			},
		}},
	}
	appendEvent(t, b, sess, "chat", callContent)

	// announce_plan FunctionResponse (role user).
	respContent := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{
				ID:       "call-1",
				Name:     "announce_plan",
				Response: map[string]any{"status": "ok"},
			},
		}},
	}
	appendEvent(t, b, sess, "chat", respContent)

	entries, err := b.loadHistory(ctx, id)
	if err != nil {
		t.Fatalf("loadHistory: %v", err)
	}

	var planText string
	for _, e := range entries {
		if e.Kind == "tool_call" && e.ToolName == "announce_plan" {
			t.Errorf("announce_plan leaked as tool_call in history")
		}
		if e.Kind == "tool_result" && e.ToolName == "announce_plan" {
			t.Errorf("announce_plan leaked as tool_result in history")
		}
		if e.Kind == "agent" && strings.HasPrefix(e.Text, "# Execution Plan") {
			planText = e.Text
		}
	}
	if planText == "" {
		t.Fatalf("expected reconstructed plan document in history; entries: %+v", entries)
	}
	if !strings.Contains(planText, "Build the feature") {
		t.Errorf("plan document missing goal; got:\n%s", planText)
	}
	if !strings.Contains(planText, "phase-one") || !strings.Contains(planText, "phase-two") {
		t.Errorf("plan document missing phases; got:\n%s", planText)
	}
}

// TestLoadHistory_EditFilePromotedToFileDiff verifies that an edit_file
// call/response carrying verification_context is promoted to a main-thread
// ItemFileDiff on resume (not left as a raw args key-value dump in the fold),
// matching live-session rendering.
func TestLoadHistory_EditFilePromotedToFileDiff(t *testing.T) {
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	ctx := context.Background()

	id := seedSession(t, b, "edit a file")
	sess := getSession(t, b, id)

	appendEvent(t, b, sess, "chat", &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{
				ID:   "call-edit",
				Name: "edit_file",
				Args: map[string]any{
					"path":       "main.go",
					"old_string": "foo",
					"new_string": "bar",
				},
			},
		}},
	})
	appendEvent(t, b, sess, "chat", &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{
				ID:   "call-edit",
				Name: "edit_file",
				Response: map[string]any{
					"success":              true,
					"path":                 "main.go",
					"verification_context": "@@ -1 +1 @@\n-foo\n+bar",
				},
			},
		}},
	})

	entries, err := b.loadHistory(ctx, id)
	if err != nil {
		t.Fatalf("loadHistory: %v", err)
	}

	msgs := make([]events.HistoryMsg, 0, len(entries))
	for _, e := range entries {
		msgs = append(msgs, events.HistoryMsg{
			Kind: e.Kind, Text: e.Text, ToolName: e.ToolName,
			ToolID: e.ToolID, Args: e.Args, Result: e.Result,
		})
	}
	tr := events.NewTranscript()
	tr.LinearThread = true
	tr.LoadHistory(msgs)

	foundDiff := false
	for _, it := range tr.Items {
		if it.Kind == events.ItemFileDiff && it.DiffVerification != "" {
			foundDiff = true
		}
	}
	if !foundDiff {
		kinds := make([]string, len(tr.Items))
		for i, it := range tr.Items {
			kinds[i] = string(it.Kind)
		}
		t.Fatalf("expected ItemFileDiff for edit_file on resume; got kinds: %v", kinds)
	}
}

// TestResumeSession_EmptySessionNoContext verifies an empty resumed session
// reports zero context (no phantom utilization).
func TestResumeSession_EmptySessionNoContext(t *testing.T) {
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	ctx := context.Background()
	resp, err := b.sessionSvc.Create(ctx, &adksession.CreateRequest{
		AppName: codeAppName,
		UserID:  b.effectiveUserID(),
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	if _, err := b.ResumeSession(ctx, resp.Session.ID()); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if got := b.Info().ContextTokens; got != 0 {
		t.Fatalf("ContextTokens for empty session = %d, want 0", got)
	}
}

// TestEmitUsage_ReportsRealMetadata verifies real provider usage is emitted and
// signals that the estimate fallback should be skipped.
func TestEmitUsage_ReportsRealMetadata(t *testing.T) {
	b := newTestBackend()
	var got []events.Event
	emit := captureEmit(&got, b.debug)

	ev := &adksession.Event{
		LLMResponse: adkmodel.LLMResponse{
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     1000,
				CandidatesTokenCount: 200,
				TotalTokenCount:      1200,
			},
		},
	}
	if !b.emitUsage(ev, emit) {
		t.Fatal("expected emitUsage to report true for real metadata")
	}
	if len(got) != 1 || got[0].Kind != events.KindUsage || got[0].Usage.Total != 1200 {
		t.Fatalf("unexpected usage event: %+v", got)
	}
	if got[0].Usage.Estimated {
		t.Error("real metadata must not be marked Estimated")
	}

	// Missing metadata → false, nothing emitted.
	got = nil
	if b.emitUsage(&adksession.Event{}, emit) {
		t.Error("expected emitUsage false when no metadata")
	}
	if len(got) != 0 {
		t.Errorf("expected no event without metadata, got %+v", got)
	}
}

// TestCodeUserIDForDir_ScopesPerDirectory verifies distinct directories map to
// distinct (stable) user IDs, and empty falls back to the base user.
func TestCodeUserIDForDir_ScopesPerDirectory(t *testing.T) {
	a1 := codeUserIDForDir("/home/me/projectA")
	a2 := codeUserIDForDir("/home/me/projectA")
	bDir := codeUserIDForDir("/home/me/projectB")

	if a1 != a2 {
		t.Fatalf("expected stable user ID for same dir, got %q vs %q", a1, a2)
	}
	if a1 == bDir {
		t.Fatalf("expected distinct user IDs for different dirs, both %q", a1)
	}
	if !strings.HasPrefix(a1, codeUserID+"_") {
		t.Fatalf("expected derived id prefixed with base user, got %q", a1)
	}
	if got := codeUserIDForDir(""); got != codeUserID {
		t.Fatalf("empty dir should fall back to base user, got %q", got)
	}
}

// TestListSessions_PersistsAcrossRestart verifies a session written by one
// backend is listed by a fresh backend over the same directory — the core
// "session must be maintained" requirement.
func TestListSessions_PersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	userID := codeUserIDForDir("/work/projectA")

	b1 := newFileStoreBackend(t, dir, userID)
	id := seedSession(t, b1, "Refactor the auth module")

	// Simulate a restart: brand-new backend over the same on-disk store.
	b2 := newFileStoreBackend(t, dir, userID)
	sessions, err := b2.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 persisted session, got %d: %+v", len(sessions), sessions)
	}
	if sessions[0].ID != id {
		t.Fatalf("expected session %q, got %q", id, sessions[0].ID)
	}
	if sessions[0].Title != "Refactor the auth module" {
		t.Fatalf("expected derived title, got %q", sessions[0].Title)
	}
	if sessions[0].MessageCount == 0 {
		t.Fatalf("expected non-zero message count")
	}
}

// TestListSessions_ScopedPerWorkingDir verifies `/sessions` in one directory
// does not list sessions created in another directory (same on-disk store).
func TestListSessions_ScopedPerWorkingDir(t *testing.T) {
	dir := t.TempDir()
	userA := codeUserIDForDir("/work/projectA")
	userB := codeUserIDForDir("/work/projectB")

	ba := newFileStoreBackend(t, dir, userA)
	idA := seedSession(t, ba, "Project A task")

	bb := newFileStoreBackend(t, dir, userB)
	seedSession(t, bb, "Project B task")

	// Backend scoped to A must only see A's session.
	got, err := ba.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 1 || got[0].ID != idA {
		t.Fatalf("expected only project A session, got %+v", got)
	}
	if got[0].Title != "Project A task" {
		t.Fatalf("unexpected title for A: %q", got[0].Title)
	}
}

// TestResumeSession_LoadsHistory verifies loading a persisted session returns
// its transcript and switches the active session ID.
func TestResumeSession_LoadsHistory(t *testing.T) {
	dir := t.TempDir()
	userID := codeUserIDForDir("/work/projectA")

	b1 := newFileStoreBackend(t, dir, userID)
	id := seedSession(t, b1, "Explain the sandbox model")

	b2 := newFileStoreBackend(t, dir, userID)
	hist, err := b2.ResumeSession(context.Background(), id)
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if len(hist) == 0 {
		t.Fatalf("expected non-empty history on resume")
	}
	if hist[0].Kind != "user" || hist[0].Text != "Explain the sandbox model" {
		t.Fatalf("unexpected first history entry: %+v", hist[0])
	}
	if b2.sessionID != id {
		t.Fatalf("expected active session %q after resume, got %q", id, b2.sessionID)
	}
	if !b2.resumed {
		t.Fatalf("expected resumed=true after ResumeSession")
	}
}

// TestResumeSession_ToolIDPropagation verifies that loadHistory propagates
// FunctionCall.ID and FunctionResponse.ID into HistoryEntry.ToolID, so the TUI
// transcript can correctly pair tool calls with their results on session resume.
// Without this, sessions with multiple same-name tools (e.g. several shell_command
// calls) show results as failed/mismatched after resume.
func TestResumeSession_ToolIDPropagation(t *testing.T) {
	dir := t.TempDir()
	userID := codeUserIDForDir("/work/projectToolID")

	b := newFileStoreBackend(t, dir, userID)
	ctx := context.Background()

	// Create a session and seed events with multiple tool calls sharing the same name.
	resp, err := b.sessionSvc.Create(ctx, &adksession.CreateRequest{
		AppName: codeAppName,
		UserID:  b.effectiveUserID(),
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	sess := resp.Session

	// 1. User message
	ev1 := &adksession.Event{
		ID:     "e1",
		Author: "user",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("run git status and git log", genai.RoleUser),
		},
	}
	if err := b.sessionSvc.AppendEvent(ctx, sess, ev1); err != nil {
		t.Fatalf("AppendEvent user: %v", err)
	}

	// 2. Model response with two tool calls (same tool name, different IDs)
	ev2 := &adksession.Event{
		ID:     "e2",
		Author: "model",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{
					{
						FunctionCall: &genai.FunctionCall{
							ID:   "call_001",
							Name: "shell_command",
							Args: map[string]any{"command": "git status"},
						},
					},
					{
						FunctionCall: &genai.FunctionCall{
							ID:   "call_002",
							Name: "shell_command",
							Args: map[string]any{"command": "git log --oneline -5"},
						},
					},
				},
			},
		},
	}
	if err := b.sessionSvc.AppendEvent(ctx, sess, ev2); err != nil {
		t.Fatalf("AppendEvent tool_calls: %v", err)
	}

	// 3. Tool results (user role, FunctionResponse parts)
	ev3 := &adksession.Event{
		ID:     "e3",
		Author: "user",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "user",
				Parts: []*genai.Part{
					{
						FunctionResponse: &genai.FunctionResponse{
							ID:       "call_001",
							Name:     "shell_command",
							Response: map[string]any{"stdout": "On branch main\nnothing to commit"},
						},
					},
					{
						FunctionResponse: &genai.FunctionResponse{
							ID:       "call_002",
							Name:     "shell_command",
							Response: map[string]any{"stdout": "abc1234 Initial commit"},
						},
					},
				},
			},
		},
	}
	if err := b.sessionSvc.AppendEvent(ctx, sess, ev3); err != nil {
		t.Fatalf("AppendEvent tool_results: %v", err)
	}

	// 4. Model final response
	ev4 := &adksession.Event{
		ID:     "e4",
		Author: "model",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("Here are the results.", genai.RoleModel),
		},
	}
	if err := b.sessionSvc.AppendEvent(ctx, sess, ev4); err != nil {
		t.Fatalf("AppendEvent agent: %v", err)
	}

	// Resume session and verify ToolID propagation.
	b2 := newFileStoreBackend(t, dir, userID)
	hist, err := b2.ResumeSession(ctx, sess.ID())
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}

	// Collect tool_call and tool_result entries.
	var calls, results []backend.HistoryEntry
	for _, h := range hist {
		switch h.Kind {
		case "tool_call":
			calls = append(calls, h)
		case "tool_result":
			results = append(results, h)
		}
	}

	if len(calls) != 2 {
		t.Fatalf("expected 2 tool_call entries, got %d; hist=%+v", len(calls), hist)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 tool_result entries, got %d; hist=%+v", len(results), hist)
	}

	// Verify ToolIDs are propagated on tool_call entries.
	if calls[0].ToolID != "call_001" {
		t.Errorf("call[0].ToolID = %q, want %q", calls[0].ToolID, "call_001")
	}
	if calls[1].ToolID != "call_002" {
		t.Errorf("call[1].ToolID = %q, want %q", calls[1].ToolID, "call_002")
	}

	// Verify ToolIDs are propagated on tool_result entries.
	if results[0].ToolID != "call_001" {
		t.Errorf("result[0].ToolID = %q, want %q", results[0].ToolID, "call_001")
	}
	if results[1].ToolID != "call_002" {
		t.Errorf("result[1].ToolID = %q, want %q", results[1].ToolID, "call_002")
	}

	// Verify tool names are still correct.
	for i, c := range calls {
		if c.ToolName != "shell_command" {
			t.Errorf("call[%d].ToolName = %q, want %q", i, c.ToolName, "shell_command")
		}
	}
	for i, r := range results {
		if r.ToolName != "shell_command" {
			t.Errorf("result[%d].ToolName = %q, want %q", i, r.ToolName, "shell_command")
		}
	}
}

// TestResumeSession_ApprovalSupersededToolCalls verifies that FunctionCalls
// superseded by the approval flow (no matching FunctionResponse because the
// tool was re-issued with a new ID after user approval) are excluded from
// the resumed history. Without this, sessions that went through approval show
// a phantom extra "failed" tool execution after resume.
func TestResumeSession_ApprovalSupersededToolCalls(t *testing.T) {
	dir := t.TempDir()
	userID := codeUserIDForDir("/work/projectApproval")

	b := newFileStoreBackend(t, dir, userID)
	ctx := context.Background()

	// Create a session simulating the approval flow:
	// 1. User message
	// 2. Model emits FunctionCall (pre-approval, ID="call_pre")
	// 3. System event sets awaiting_approval=true
	// 4. User says "Allow"
	// 5. Model emits FunctionCall (post-approval, ID="call_post")
	// 6. Tool result for call_post
	// 7. Model final text
	resp, err := b.sessionSvc.Create(ctx, &adksession.CreateRequest{
		AppName: codeAppName,
		UserID:  b.effectiveUserID(),
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	sess := resp.Session

	// 1. User message
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e1",
		Author: "user",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("run ls /tmp", genai.RoleUser),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 2. Model FunctionCall (pre-approval)
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e2",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "call_pre",
						Name: "shell_command",
						Args: map[string]any{"command": "ls /tmp"},
					},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 3. System event with awaiting_approval=true
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e3",
		Author: "astonish_code",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("shell_command wants to access files outside the project", genai.RoleModel),
		},
		Actions: adksession.EventActions{
			StateDelta: map[string]any{
				"awaiting_approval": true,
				"approval_tool":     "shell_command",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 4. User approves
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e4",
		Author: "user",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("Allow", genai.RoleUser),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 5. Model FunctionCall (post-approval, new ID)
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e5",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "call_post",
						Name: "shell_command",
						Args: map[string]any{"command": "ls /tmp"},
					},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 6. Tool result for call_post
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e6",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "user",
				Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{
						ID:       "call_post",
						Name:     "shell_command",
						Response: map[string]any{"stdout": "file1 file2"},
					},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 7. Model final text
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e7",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("Here are the files in /tmp.", genai.RoleModel),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Resume from a fresh backend and verify:
	// - The pre-approval FunctionCall (call_pre) is NOT in history
	// - Only one tool_call (call_post) and one tool_result appear
	b2 := newFileStoreBackend(t, dir, userID)
	hist, err := b2.ResumeSession(ctx, sess.ID())
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}

	var calls, results []backend.HistoryEntry
	for _, h := range hist {
		switch h.Kind {
		case "tool_call":
			calls = append(calls, h)
		case "tool_result":
			results = append(results, h)
		}
	}

	if len(calls) != 1 {
		t.Fatalf("expected 1 tool_call (post-approval only), got %d; calls=%+v", len(calls), calls)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 tool_result, got %d; results=%+v", len(results), results)
	}
	if calls[0].ToolID != "call_post" {
		t.Errorf("tool_call ToolID = %q, want %q", calls[0].ToolID, "call_post")
	}
	if results[0].ToolID != "call_post" {
		t.Errorf("tool_result ToolID = %q, want %q", results[0].ToolID, "call_post")
	}

	// Verify no "interrupted" error in results
	if m, ok := results[0].Result.(map[string]any); ok {
		if _, hasErr := m["error"]; hasErr {
			t.Errorf("tool_result should not have error, got: %v", m["error"])
		}
	}
}

// TestResumeSession_ApprovalSupersededToolCalls_ContentField verifies the
// approval supersession detection when FunctionCalls are stored in the Content
// field (not LLMResponse.Content). This tests the unified field access fix.
func TestResumeSession_ApprovalSupersededToolCalls_ContentField(t *testing.T) {
	dir := t.TempDir()
	userID := codeUserIDForDir("/work/projectApproval2")

	b := newFileStoreBackend(t, dir, userID)
	ctx := context.Background()

	resp, err := b.sessionSvc.Create(ctx, &adksession.CreateRequest{
		AppName: codeAppName,
		UserID:  b.effectiveUserID(),
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	sess := resp.Session

	// 1. User message
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e1",
		Author: "user",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("write hello to file", genai.RoleUser),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 2. Model FunctionCall (pre-approval) — stored normally
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e2",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "call_pre",
						Name: "write_file",
						Args: map[string]any{"file_path": "/tmp/hello.txt", "content": "hello"},
					},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 3. Approval prompt with awaiting_approval=true
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e3",
		Author: "astonish_code",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("write_file wants to access /tmp/hello.txt", genai.RoleModel),
		},
		Actions: adksession.EventActions{
			StateDelta: map[string]any{
				"awaiting_approval": true,
				"approval_tool":     "write_file",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 4. User approves ("Always Allow")
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e4",
		Author: "user",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("Always Allow", genai.RoleUser),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 5. Model re-issues FunctionCall (post-approval, new ID)
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e5",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "call_post",
						Name: "write_file",
						Args: map[string]any{"file_path": "/tmp/hello.txt", "content": "hello"},
					},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 6. Tool result for call_post
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e6",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "user",
				Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{
						ID:       "call_post",
						Name:     "write_file",
						Response: map[string]any{"success": true, "path": "/tmp/hello.txt"},
					},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 7. Model final text
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e7",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("Done! File written.", genai.RoleModel),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Resume and verify.
	b2 := newFileStoreBackend(t, dir, userID)
	hist, err := b2.ResumeSession(ctx, sess.ID())
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}

	var calls, results, systems []backend.HistoryEntry
	for _, h := range hist {
		switch h.Kind {
		case "tool_call":
			calls = append(calls, h)
		case "tool_result":
			results = append(results, h)
		case "system":
			systems = append(systems, h)
		}
	}

	// The pre-approval call must NOT appear.
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool_call (post-approval only), got %d; calls=%+v", len(calls), calls)
	}
	if calls[0].ToolID != "call_post" {
		t.Errorf("tool_call ToolID = %q, want %q", calls[0].ToolID, "call_post")
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 tool_result, got %d; results=%+v", len(results), results)
	}
	if results[0].ToolID != "call_post" {
		t.Errorf("tool_result ToolID = %q, want %q", results[0].ToolID, "call_post")
	}

	// The "Always Allow" user message should be reclassified as a system entry.
	found := false
	for _, s := range systems {
		if strings.Contains(s.Text, "Approval:") && strings.Contains(s.Text, "Always Allow") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected system entry with 'Approval: Always Allow', got systems=%+v", systems)
	}

	// Ensure no user entries contain approval text.
	for _, h := range hist {
		if h.Kind == "user" && (h.Text == "Always Allow" || h.Text == "Allow") {
			t.Errorf("approval response should NOT appear as user entry: %+v", h)
		}
	}
}

// TestListSessions_NoAutoResumeOnStart verifies a fresh backend does not adopt
// a prior session as active: startup is always a clean slate (sessionID empty)
// even though prior sessions remain listable.
func TestListSessions_NoAutoResumeOnStart(t *testing.T) {
	dir := t.TempDir()
	userID := codeUserIDForDir("/work/projectA")

	b1 := newFileStoreBackend(t, dir, userID)
	seedSession(t, b1, "Earlier conversation")

	b2 := newFileStoreBackend(t, dir, userID)
	if b2.sessionID != "" {
		t.Fatalf("fresh backend must not auto-resume; sessionID=%q", b2.sessionID)
	}
	if b2.resumed {
		t.Fatalf("fresh backend must not be marked resumed")
	}
	// But the prior session is still discoverable via /sessions.
	sessions, err := b2.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected prior session listed, got %d", len(sessions))
	}
}

// TestDeleteSession_RemovesFromList verifies deletion drops the session from
// the persisted listing.
// TestDeleteSession_RemovesPlanFileSidecar verifies that deleting a code-mode
// session also removes its per-session PLAN.md sidecar (see planFilePath), so
// announced plans do not accumulate on disk after the session is gone.
func TestDeleteSession_RemovesPlanFileSidecar(t *testing.T) {
	dir := t.TempDir()
	userID := codeUserIDForDir("/work/projectPlan")
	b := newFileStoreBackend(t, dir, userID)

	id := seedSession(t, b, "Session with a plan")

	planPath := b.planFilePath(id)
	if planPath == "" {
		t.Fatalf("expected a non-empty plan file path")
	}
	if err := os.WriteFile(planPath, []byte("# Plan\n- [ ] step\n"), 0o644); err != nil {
		t.Fatalf("write PLAN.md sidecar: %v", err)
	}
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("sidecar should exist before delete: %v", err)
	}

	if err := b.DeleteSession(context.Background(), id); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	if _, err := os.Stat(planPath); !os.IsNotExist(err) {
		t.Fatalf("expected PLAN.md sidecar to be removed, stat err = %v", err)
	}
}

// TestDeleteSession_RemovesFromList covers session deletion removing the entry
// from the list. See TestDeleteSession_RemovesPlanFileSidecar for sidecar
// TestDeleteSession_RemovesFromList verifies that deleting a session removes it
// from the list and performs proper cleanup.
func TestDeleteSession_RemovesFromList(t *testing.T) {
	dir := t.TempDir()
	userID := codeUserIDForDir("/work/projectA")
	b := newFileStoreBackend(t, dir, userID)

	id := seedSession(t, b, "Throwaway session")
	if err := b.DeleteSession(context.Background(), id); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	sessions, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected empty list after delete, got %+v", sessions)
	}
}

// TestListSessions_SortByLastMessageDescending verifies that a session which
// receives a new message jumps to the top of the list regardless of its original
// creation order. This is the core UX requirement: the session with the newest
// activity must appear first.
func TestListSessions_SortByLastMessageDescending(t *testing.T) {
	dir := t.TempDir()
	userID := codeUserIDForDir("/work/project")
	b := newFileStoreBackend(t, dir, userID)
	ctx := context.Background()

	// Create two sessions with a small delay between them.
	idOlder := seedSession(t, b, "Older session")
	time.Sleep(10 * time.Millisecond) // ensure different timestamps
	idNewer := seedSession(t, b, "Newer session")

	// Initially, newer session should be first.
	sessions, err := b.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	if sessions[0].ID != idNewer {
		t.Fatalf("expected newer session first, got %q", sessions[0].ID)
	}

	// Now send a new message to the older session — it should jump to the top.
	time.Sleep(10 * time.Millisecond)
	resp, gErr := b.sessionSvc.Get(ctx, &adksession.GetRequest{
		AppName:   codeAppName,
		UserID:    b.effectiveUserID(),
		SessionID: idOlder,
	})
	if gErr != nil {
		t.Fatalf("Get session: %v", gErr)
	}
	ev := &adksession.Event{
		ID:     "e2",
		Author: "user",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("new message in old session", genai.RoleUser),
		},
		Timestamp: time.Now(),
	}
	if err := b.sessionSvc.AppendEvent(ctx, resp.Session, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	// Re-list: older session (now with the most recent message) should be first.
	sessions, err = b.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions after update: %v", err)
	}
	if sessions[0].ID != idOlder {
		t.Fatalf("expected older session (with latest message) to be first, got %q (wanted %q)", sessions[0].ID, idOlder)
	}
}

// TestDeriveSessionTitle covers trimming, first-line extraction, and truncation.
func TestDeriveSessionTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"Simple title", "Simple title"},
		{"  Trim me  ", "Trim me"},
		{"First line\nsecond line", "First line"},
		{strings.Repeat("x", 80), strings.Repeat("x", 60) + "…"},
	}
	for _, c := range cases {
		if got := deriveSessionTitle(c.in); got != c.want {
			t.Errorf("deriveSessionTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCleanCodeSessionTitle verifies thinking-tag stripping and truncation.
func TestCleanCodeSessionTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"  Simple Title  ", "Simple Title"},
		{"<think>reasoning here</think>Actual Title", "Actual Title"},
		{"<thinking>some thought</thinking>Real Title", "Real Title"},
		{"\"Quoted Title\"", "Quoted Title"},
		{"`backtick title`", "backtick title"},
		{strings.Repeat("a", 100), strings.Repeat("a", 77) + "..."},
	}
	for _, c := range cases {
		if got := cleanCodeSessionTitle(c.in); got != c.want {
			t.Errorf("cleanCodeSessionTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestGenerateCodeSessionTitle_UpdatesTitle verifies that the LLM title
// generation writes a refined title to the session store when the LLM succeeds.
func TestGenerateCodeSessionTitle_UpdatesTitle(t *testing.T) {
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	ctx := context.Background()

	// Create a session with a provisional title.
	sessionID := seedSession(t, b, "Fix the authentication bug in login handler")

	// Verify provisional title is set.
	title, err := b.fileStore.GetSessionTitle(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSessionTitle: %v", err)
	}
	if title != "Fix the authentication bug in login handler" {
		t.Fatalf("expected provisional title, got %q", title)
	}

	// Call generateCodeSessionTitle with a mock LLM that returns a refined title.
	var emittedTitle string
	generateCodeSessionTitle(
		&mockTitleLLM{response: "Auth Bug Fix in Login Handler"},
		b.fileStore,
		sessionID,
		"Fix the authentication bug in login handler",
		"Fix the authentication bug in login handler",
		func(title string) { emittedTitle = title },
	)

	// Verify the refined title was persisted.
	refined, err := b.fileStore.GetSessionTitle(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSessionTitle after refine: %v", err)
	}
	if refined != "Auth Bug Fix in Login Handler" {
		t.Fatalf("expected refined title %q, got %q", "Auth Bug Fix in Login Handler", refined)
	}
	if emittedTitle != "Auth Bug Fix in Login Handler" {
		t.Fatalf("expected emitted title %q, got %q", "Auth Bug Fix in Login Handler", emittedTitle)
	}
}

// TestGenerateCodeSessionTitle_NoOpOnNilLLM verifies title generation does
// nothing when no LLM is available (e.g. unconfigured provider).
func TestGenerateCodeSessionTitle_NoOpOnNilLLM(t *testing.T) {
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	ctx := context.Background()

	sessionID := seedSession(t, b, "Some task message")
	original, _ := b.fileStore.GetSessionTitle(ctx, sessionID)

	// nil LLM should be a no-op.
	generateCodeSessionTitle(nil, b.fileStore, sessionID, "Some task message", original, nil)

	after, _ := b.fileStore.GetSessionTitle(ctx, sessionID)
	if after != original {
		t.Fatalf("title should not change with nil LLM: got %q, want %q", after, original)
	}
}

// mockTitleLLM is a minimal LLM implementation for title generation tests.
type mockTitleLLM struct {
	response string
}

func (m *mockTitleLLM) GenerateContent(_ context.Context, _ *adkmodel.LLMRequest, _ bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		yield(&adkmodel.LLMResponse{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: m.response}},
			},
		}, nil)
	}
}

func (m *mockTitleLLM) Name() string { return "mock-title" }

// TestCodeSessionsDir_IsolatedFromStudio verifies code sessions live under a
// dedicated "code" subdirectory so they never mix with Studio/chat sessions.
func TestCodeSessionsDir_IsolatedFromStudio(t *testing.T) {
	appCfg := &config.AppConfig{}
	appCfg.Sessions.BaseDir = t.TempDir()

	dir, err := codeSessionsDir(appCfg)
	if err != nil {
		t.Fatalf("codeSessionsDir: %v", err)
	}
	if filepath.Base(dir) != "code" {
		t.Fatalf("expected code subdirectory, got %q", dir)
	}
	studioDir, err := config.GetSessionsDir(&appCfg.Sessions)
	if err != nil {
		t.Fatalf("GetSessionsDir: %v", err)
	}
	if dir == studioDir {
		t.Fatalf("code sessions dir must differ from studio sessions dir (%q)", dir)
	}
}

// seedUserEvent appends a user-authored text event to a session via the store.
func seedUserEvent(t *testing.T, store *persistentsession.FileStore, sess adksession.Session, id, text string) {
	t.Helper()
	ev := &adksession.Event{
		ID:     id,
		Author: "user",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText(text, genai.RoleUser),
		},
	}
	if err := store.AppendEvent(context.Background(), sess, ev); err != nil {
		t.Fatalf("AppendEvent(%s): %v", id, err)
	}
}

// TestRollback_ListAndRestore drives the code-mode rollback capability end to
// end against real file and checkpoint stores: it seeds two user turns, records
// a file snapshot for the second turn, then rolls back to the second turn and
// verifies both chat truncation and file restoration.
func TestRollback_ListAndRestore(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	workDir := t.TempDir()

	fileStore, err := persistentsession.NewFileStore(baseDir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	checkpoints, err := persistentsession.NewCheckpointStore(baseDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore: %v", err)
	}

	resp, err := fileStore.Create(ctx, &adksession.CreateRequest{AppName: codeAppName, UserID: codeUserID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess := resp.Session
	sessionID := sess.ID()

	// Turn at event index 0.
	seedUserEvent(t, fileStore, sess, "u0", "first request")
	// Turn at event index 1: this turn modifies a file.
	seedUserEvent(t, fileStore, sess, "u1", "second request")

	target := filepath.Join(workDir, "out.txt")
	if err := os.WriteFile(target, []byte("before turn 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkpoints.BeginTurn(sessionID, 1)
	if err := checkpoints.Capture(sessionID, 1, target); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if err := os.WriteFile(target, []byte("after turn 1"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := &localAgentBackend{
		sessionSvc:  common.NewAutoInitService(fileStore),
		fileStore:   fileStore,
		checkpoints: checkpoints,
		appConfig:   &config.AppConfig{},
		sessionID:   sessionID,
		workingDir:  workDir,
	}

	points, err := b.ListRollbackPoints(ctx)
	if err != nil {
		t.Fatalf("ListRollbackPoints: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("got %d rollback points, want 2", len(points))
	}
	want0 := sessionID + ":0"
	want1 := sessionID + ":1"
	if points[0].ID != want0 || points[1].ID != want1 {
		t.Fatalf("point IDs = %q,%q want %q,%q", points[0].ID, points[1].ID, want0, want1)
	}
	if points[1].FileCount != 1 {
		t.Errorf("point[1].FileCount = %d, want 1", points[1].FileCount)
	}
	if points[0].Label != "first request" {
		t.Errorf("point[0].Label = %q", points[0].Label)
	}

	// Roll back to the second user message (index 1): chat keeps only event 0,
	// and the file returns to its pre-turn-1 content. Use the session-scoped ID.
	entries, err := b.RollbackTo(ctx, want1)
	if err != nil {
		t.Fatalf("RollbackTo: %v", err)
	}

	got, _ := os.ReadFile(target)
	if string(got) != "before turn 1" {
		t.Errorf("file after rollback = %q, want %q", got, "before turn 1")
	}
	// History should contain only the first user message.
	var userTexts []string
	for _, e := range entries {
		if e.Kind == "user" {
			userTexts = append(userTexts, e.Text)
		}
	}
	if len(userTexts) != 1 || userTexts[0] != "first request" {
		t.Errorf("history user texts = %v, want [first request]", userTexts)
	}

	// The session on disk is truncated to a single event.
	getResp, err := fileStore.Get(ctx, &adksession.GetRequest{AppName: codeAppName, UserID: codeUserID, SessionID: sessionID})
	if err != nil {
		t.Fatalf("Get after rollback: %v", err)
	}
	if n := getResp.Session.Events().Len(); n != 1 {
		t.Errorf("session events after rollback = %d, want 1", n)
	}
}

// TestRollback_NoSessionReturnsNil verifies rollback listing is a safe no-op
// when there is no active session.
func TestRollback_NoSessionReturnsNil(t *testing.T) {
	b := newTestBackend()
	points, err := b.ListRollbackPoints(context.Background())
	if err != nil {
		t.Fatalf("ListRollbackPoints: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("expected no rollback points, got %d", len(points))
	}
	if _, err := b.RollbackTo(context.Background(), "0"); err == nil {
		t.Fatal("RollbackTo with no session should error")
	}
}

// TestAgentAttachmentsFromBackend verifies that pasted-image / file payloads
// (raw bytes on backend.Attachment) are converted to base64 agent.Attachment
// values so RunTurn can forward them as InlineData parts. This defends the
// code-mode regression where pasted images were inserted into the composer but
// silently dropped before reaching the model.
func TestAgentAttachmentsFromBackend(t *testing.T) {
	raw := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	got := agentAttachmentsFromBackend([]backend.Attachment{
		{Filename: "shot.png", MimeType: "image/png", Data: raw},
		{Filename: "empty.png", MimeType: "image/png", Data: nil}, // skipped
	})

	if len(got) != 1 {
		t.Fatalf("attachments = %d, want 1 (empty payload must be skipped)", len(got))
	}
	if got[0].Filename != "shot.png" || got[0].MimeType != "image/png" {
		t.Fatalf("metadata not propagated: %+v", got[0])
	}
	decoded, err := base64.StdEncoding.DecodeString(got[0].Data)
	if err != nil {
		t.Fatalf("data is not valid base64: %v", err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Fatalf("round-trip mismatch: got %v, want %v", decoded, raw)
	}
}

// TestAgentAttachmentsFromBackend_Empty verifies a nil/empty attachment slice
// yields nil so RunTurn falls back to the plain text user message.
func TestAgentAttachmentsFromBackend_Empty(t *testing.T) {
	if got := agentAttachmentsFromBackend(nil); got != nil {
		t.Fatalf("expected nil for no attachments, got %+v", got)
	}
}

func TestParseRollbackPointID(t *testing.T) {
	owner, idx, err := parseRollbackPointID("abc-123:7", "active")
	if err != nil || owner != "abc-123" || idx != 7 {
		t.Fatalf("got owner=%q idx=%d err=%v", owner, idx, err)
	}
	owner, idx, err = parseRollbackPointID("3", "active")
	if err != nil || owner != "active" || idx != 3 {
		t.Fatalf("bare: owner=%q idx=%d err=%v", owner, idx, err)
	}
	if _, _, err := parseRollbackPointID("bad", "a"); err == nil {
		t.Fatal("expected error for non-numeric bare id")
	}
}

// TestMaybeCompactToChild_CreatesChildAndPreservesParent verifies the
// "compaction = new session with summary" model: parent transcript intact,
// child seeded with a shorter history, active session switched to the child.
func TestMaybeCompactToChild_CreatesChildAndPreservesParent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fileStore, err := persistentsession.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := fileStore.Create(ctx, &adksession.CreateRequest{AppName: codeAppName, UserID: codeUserID})
	if err != nil {
		t.Fatal(err)
	}
	parentID := resp.Session.ID()
	// Seed a large history so ShouldCompact triggers on a tiny window.
	for i := 0; i < 30; i++ {
		seedUserEvent(t, fileStore, resp.Session, fmt.Sprintf("e%d", i), strings.Repeat("x", 200)+" msg "+fmt.Sprint(i))
	}

	// Minimal ChatAgent with a real Compactor (truncation path — no LLM).
	comp := persistentsession.NewCompactor(100) // tiny window
	comp.PreserveRecent = 2
	chatAgent := &agent.ChatAgent{Compactor: comp}

	b := &localAgentBackend{
		sessionSvc: common.NewAutoInitService(fileStore),
		fileStore:  fileStore,
		result:     &ChatFactoryResult{ChatAgent: chatAgent},
		sessionID:  parentID,
		appConfig:  &config.AppConfig{},
	}

	var notices []string
	emit := func(typ string, data map[string]any) {
		if typ == "system" {
			if s, ok := data["content"].(string); ok {
				notices = append(notices, s)
			}
		}
	}
	// Active session ID stays the same; full history is archived under ParentID.
	activeID := b.maybeCompactToChild(ctx, parentID, emit)
	if activeID != parentID {
		t.Fatalf("active session id should stay %q after archive-and-replace, got %q", parentID, activeID)
	}
	if b.sessionID != parentID {
		t.Fatalf("backend sessionID = %q want %q", b.sessionID, parentID)
	}

	// Active (tip) is now short.
	tipGet, err := fileStore.Get(ctx, &adksession.GetRequest{AppName: codeAppName, UserID: codeUserID, SessionID: parentID})
	if err != nil {
		t.Fatal(err)
	}
	if n := tipGet.Session.Events().Len(); n >= 30 {
		t.Fatalf("tip events = %d, want compacted (< 30)", n)
	}
	meta, err := fileStore.GetSessionMeta(parentID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ParentID == "" {
		t.Fatal("tip should have ParentID pointing at the archive")
	}
	archiveID := meta.ParentID

	// Archive still has the full history.
	archGet, err := fileStore.Get(ctx, &adksession.GetRequest{AppName: codeAppName, UserID: codeUserID, SessionID: archiveID})
	if err != nil {
		t.Fatal(err)
	}
	if n := archGet.Session.Events().Len(); n < 30 {
		t.Fatalf("archive events = %d, want >= 30", n)
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "Compacted context") {
		t.Fatalf("expected compaction notice, got %v", notices)
	}

	// Resume from the archive root resolves to the tip (same id as parentID).
	b2 := &localAgentBackend{
		sessionSvc: common.NewAutoInitService(fileStore),
		fileStore:  fileStore,
		appConfig:  &config.AppConfig{},
	}
	if _, err := b2.ResumeSession(ctx, archiveID); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if b2.sessionID != parentID {
		t.Fatalf("resume tip = %q want active tip %q", b2.sessionID, parentID)
	}

	// Rollback to a pre-compaction turn on the archive reactivates the archive
	// and drops the compacted tip (option A).
	if _, err := b2.RollbackTo(ctx, archiveID+":1"); err != nil {
		t.Fatalf("RollbackTo archive:1: %v", err)
	}
	if b2.sessionID != archiveID {
		t.Fatalf("after rollback active = %q want archive %q", b2.sessionID, archiveID)
	}
	if _, err := fileStore.GetSessionMeta(parentID); err == nil {
		t.Fatal("expected compacted tip deleted after rollback to archive turn")
	}
	archGet, err = fileStore.Get(ctx, &adksession.GetRequest{AppName: codeAppName, UserID: codeUserID, SessionID: archiveID})
	if err != nil {
		t.Fatal(err)
	}
	if n := archGet.Session.Events().Len(); n != 1 {
		t.Fatalf("archive events after rollback = %d want 1", n)
	}
}

// TestCompact_RunsImmediately verifies /compact creates a child session now
// rather than deferring to the next user message.
// --- Plan approval E2E ---

// TestPlanApprovalE2E_EventPipelineCarriesDocFields is an end-to-end test for
// the plan approval data pipeline:
//
//  1. A plan_approval payload (with context/what-not-to-do/verification fields)
//     is emitted through the same emit→captureEmit→mapSSEToEvents path used
//     at runtime in driveTurn.
//  2. The resulting events.Event has Kind=KindApproval, ApprovalKind="plan",
//     and all three doc fields populated.
//  3. The event is applied to a Transcript: Awaiting is set and the approval
//     Item carries the doc fields so renderPlanApprovalOverlay can show context.
//
// This test defends the full pipeline from the emit call in driveTurn through
// the SSE translator to the TUI transcript — the path that was unit-tested at
// each seam but never driven end-to-end.
func TestPlanApprovalE2E_EventPipelineCarriesDocFields(t *testing.T) {
	b := newTestBackend()
	var got []events.Event
	emit := captureEmit(&got, b.debug)

	// Simulate what driveTurn emits when planAnnounced=true and the active plan
	// carries doc-level narrative sections.
	emit("plan_approval", map[string]any{
		"plan_announced":    true,
		"plan_context":      "We are migrating the auth module from JWT to OAuth2.",
		"plan_what_not_to_do": "Do not touch the legacy session store.",
		"plan_verification": "Run go test ./pkg/auth/...",
	})

	// Exactly one event should be produced.
	if len(got) != 1 {
		t.Fatalf("expected 1 event from plan_approval, got %d: %+v", len(got), got)
	}
	ev := got[0]

	// Must be an approval event with plan kind.
	if ev.Kind != events.KindApproval {
		t.Fatalf("event kind = %v, want KindApproval", ev.Kind)
	}
	if ev.ApprovalKind != "plan" {
		t.Fatalf("ApprovalKind = %q, want \"plan\"", ev.ApprovalKind)
	}
	if ev.ToolName != "announce_plan" {
		t.Fatalf("ToolName = %q, want \"announce_plan\"", ev.ToolName)
	}

	// Doc fields must survive the emit→SSE→translate pipeline.
	if ev.PlanContext != "We are migrating the auth module from JWT to OAuth2." {
		t.Fatalf("PlanContext = %q", ev.PlanContext)
	}
	if ev.PlanWhatNotToDo != "Do not touch the legacy session store." {
		t.Fatalf("PlanWhatNotToDo = %q", ev.PlanWhatNotToDo)
	}
	if ev.PlanVerification != "Run go test ./pkg/auth/..." {
		t.Fatalf("PlanVerification = %q", ev.PlanVerification)
	}

	// Options must include the three plan choices.
	wantOpts := []string{"Approve & implement", "Request changes", "Decline"}
	if len(ev.Options) != len(wantOpts) {
		t.Fatalf("Options = %v, want %v", ev.Options, wantOpts)
	}
	for i, want := range wantOpts {
		if ev.Options[i] != want {
			t.Errorf("Options[%d] = %q, want %q", i, ev.Options[i], want)
		}
	}

	// Apply to a Transcript and verify the Item carries the doc fields so the
	// approval overlay can render context — this is the step the TUI takes.
	tr := events.NewTranscript()
	tr.Apply(ev)

	if !tr.Awaiting {
		t.Fatal("Transcript should be Awaiting after plan approval event")
	}
	if tr.ApprovalIdx < 0 || tr.ApprovalIdx >= len(tr.Items) {
		t.Fatalf("ApprovalIdx = %d, Items len = %d", tr.ApprovalIdx, len(tr.Items))
	}
	item := tr.Items[tr.ApprovalIdx]
	if item.ApprovalKind != "plan" {
		t.Fatalf("Item.ApprovalKind = %q, want \"plan\"", item.ApprovalKind)
	}
	if item.PlanContext != "We are migrating the auth module from JWT to OAuth2." {
		t.Fatalf("Item.PlanContext = %q", item.PlanContext)
	}
	if item.PlanWhatNotToDo != "Do not touch the legacy session store." {
		t.Fatalf("Item.PlanWhatNotToDo = %q", item.PlanWhatNotToDo)
	}
	if item.PlanVerification != "Run go test ./pkg/auth/..." {
		t.Fatalf("Item.PlanVerification = %q", item.PlanVerification)
	}
}

// TestPlanApprovalE2E_EmptyDocFieldsStillProducesApprovalEvent verifies that
// a plan_approval payload without doc fields (older server / no context set)
// still produces a valid approval event with empty doc fields — the fallback
// path must not break.
func TestPlanApprovalE2E_EmptyDocFieldsStillProducesApprovalEvent(t *testing.T) {
	b := newTestBackend()
	var got []events.Event
	emit := captureEmit(&got, b.debug)

	emit("plan_approval", map[string]any{"plan_announced": true})

	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	ev := got[0]
	if ev.Kind != events.KindApproval || ev.ApprovalKind != "plan" {
		t.Fatalf("event = %+v", ev)
	}
	if ev.PlanContext != "" || ev.PlanWhatNotToDo != "" || ev.PlanVerification != "" {
		t.Fatalf("expected empty doc fields for bare plan_approval: context=%q whatNotToDo=%q verification=%q",
			ev.PlanContext, ev.PlanWhatNotToDo, ev.PlanVerification)
	}
}

// --- Session resume E2E ---

// TestSessionResumeE2E_FullHistoryToTranscript is an end-to-end test for the
// session resume path from disk to rendered transcript items:
//
//  1. A session with a user message, agent response, tool call+result, and a
//     second agent message is written to a file-backed store under a realistic
//     working-directory-scoped user ID.
//  2. A brand-new backend (simulating a process restart) opens the same store,
//     calls ResumeSession, and receives the HistoryEntry slice.
//  3. The HistoryEntry slice is fed into events.Transcript.LoadHistory (the
//     same call the TUI makes on session resume) and the resulting Items are
//     verified: user bubble, activity fold, final agent bubble all present.
//  4. Backend state (sessionID, resumed flag, title, context tokens) is checked.
//
// This test defends the full restart → /sessions → pick → history loads path.
func TestSessionResumeE2E_FullHistoryToTranscript(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	userID := codeUserIDForDir("/work/myproject")

	// --- Phase 1: create a session and seed a realistic turn ---
	b1 := newFileStoreBackend(t, dir, userID)
	resp, err := b1.sessionSvc.Create(ctx, &adksession.CreateRequest{
		AppName: codeAppName,
		UserID:  b1.effectiveUserID(),
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	sess := resp.Session

	appendEv := func(ev *adksession.Event) {
		t.Helper()
		if err := b1.sessionSvc.AppendEvent(ctx, sess, ev); err != nil {
			t.Fatalf("AppendEvent %q: %v", ev.ID, err)
		}
	}

	// User message.
	appendEv(&adksession.Event{
		ID:     "e1",
		Author: "user",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("Add a README to the project", genai.RoleUser),
		},
		Timestamp: time.Now(),
	})

	// Agent interim text.
	appendEv(&adksession.Event{
		ID:     "e2",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("I will create a README.md for you.", genai.RoleModel),
		},
	})

	// Tool call: write_file.
	appendEv(&adksession.Event{
		ID:     "e3",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "call_wf01",
						Name: "write_file",
						Args: map[string]any{"path": "README.md", "content": "# My Project\n"},
					},
				}},
			},
		},
	})

	// Tool result.
	appendEv(&adksession.Event{
		ID:     "e4",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "user",
				Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{
						ID:       "call_wf01",
						Name:     "write_file",
						Response: map[string]any{"success": true, "path": "README.md"},
					},
				}},
			},
		},
	})

	// Agent final text.
	appendEv(&adksession.Event{
		ID:     "e5",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("README.md has been created.", genai.RoleModel),
		},
	})

	// Persist title (mirrors what RunTurn does).
	title := "Add a README to the project"
	if err := b1.fileStore.SetSessionTitle(ctx, sess.ID(), title); err != nil {
		t.Fatalf("SetSessionTitle: %v", err)
	}
	sessionID := sess.ID()

	// --- Phase 2: simulate restart — fresh backend over the same store ---
	b2 := newFileStoreBackend(t, dir, userID)

	// Verify the session is listed (what /sessions shows).
	sessions, err := b2.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions after restart: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session in list, got %d", len(sessions))
	}
	if sessions[0].ID != sessionID {
		t.Fatalf("listed session ID = %q, want %q", sessions[0].ID, sessionID)
	}
	if sessions[0].Title != title {
		t.Fatalf("listed title = %q, want %q", sessions[0].Title, title)
	}

	// Resume (what the TUI does when the user picks a session from /sessions).
	hist, err := b2.ResumeSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}

	// Backend state must be updated.
	if b2.sessionID != sessionID {
		t.Fatalf("sessionID after resume = %q, want %q", b2.sessionID, sessionID)
	}
	if !b2.resumed {
		t.Fatal("resumed flag should be true after ResumeSession")
	}
	if b2.title != title {
		t.Fatalf("title after resume = %q, want %q", b2.title, title)
	}
	if b2.contextTokens <= 0 {
		t.Fatal("contextTokens should be > 0 after resuming a non-empty session")
	}

	// History must contain all five visible entries in order.
	wantKinds := []string{"user", "agent", "tool_call", "tool_result", "agent"}
	if len(hist) != len(wantKinds) {
		t.Fatalf("history len = %d, want %d; entries = %+v", len(hist), len(wantKinds), hist)
	}
	for i, want := range wantKinds {
		if hist[i].Kind != want {
			t.Errorf("hist[%d].Kind = %q, want %q", i, hist[i].Kind, want)
		}
	}
	if hist[0].Text != "Add a README to the project" {
		t.Errorf("hist[0].Text = %q", hist[0].Text)
	}
	if hist[2].ToolName != "write_file" || hist[2].ToolID != "call_wf01" {
		t.Errorf("tool_call: name=%q id=%q", hist[2].ToolName, hist[2].ToolID)
	}
	if hist[3].ToolName != "write_file" || hist[3].ToolID != "call_wf01" {
		t.Errorf("tool_result: name=%q id=%q", hist[3].ToolName, hist[3].ToolID)
	}
	if hist[4].Text != "README.md has been created." {
		t.Errorf("final agent text = %q", hist[4].Text)
	}

	// --- Phase 3: feed history through Transcript.LoadHistory ---
	// This is exactly what the TUI does after ResumeSession returns.
	tr := events.NewTranscript()
	tr.LinearThread = true // code mode uses linear thread
	entries := make([]events.HistoryMsg, 0, len(hist))
	for _, h := range hist {
		var art *events.Artifact
		if h.Artifact != nil {
			art = h.Artifact
		}
		entries = append(entries, events.HistoryMsg{
			Kind:     h.Kind,
			Text:     h.Text,
			ToolName: h.ToolName,
			ToolID:   h.ToolID,
			Args:     h.Args,
			Result:   h.Result,
			Artifact: art,
		})
	}
	tr.LoadHistory(entries)

	// Transcript must not be streaming after history load.
	if tr.Streaming {
		t.Fatal("Streaming should be false after LoadHistory")
	}

	// Count item kinds.
	kindCounts := map[events.ItemKind]int{}
	for _, it := range tr.Items {
		kindCounts[it.Kind]++
	}

	// Must have at least one user bubble, one activity fold, and at least one
	// agent bubble — the three visual pillars of a resumed code session.
	if kindCounts[events.ItemUser] == 0 {
		t.Errorf("no user items in transcript: %v", kindCounts)
	}
	if kindCounts[events.ItemActivity] == 0 {
		t.Errorf("no activity items in transcript: %v", kindCounts)
	}
	if kindCounts[events.ItemAgent] == 0 {
		t.Errorf("no agent items in transcript: %v", kindCounts)
	}

	// The user bubble must contain the original message.
	found := false
	for _, it := range tr.Items {
		if it.Kind == events.ItemUser && strings.Contains(it.Content, "Add a README") {
			found = true
			break
		}
	}
	if !found {
		t.Error("user bubble with original message not found in resumed transcript")
	}

	// The activity fold must reference write_file.
	found = false
	for _, it := range tr.Items {
		if it.Kind != events.ItemActivity {
			continue
		}
		for _, step := range it.Steps {
			if step.Name == "write_file" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("write_file step not found in any activity fold of resumed transcript")
	}
}

func TestCompact_RunsImmediately(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fileStore, err := persistentsession.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := fileStore.Create(ctx, &adksession.CreateRequest{AppName: codeAppName, UserID: codeUserID})
	if err != nil {
		t.Fatal(err)
	}
	parentID := resp.Session.ID()
	for i := 0; i < 30; i++ {
		seedUserEvent(t, fileStore, resp.Session, fmt.Sprintf("e%d", i), strings.Repeat("y", 200)+" "+fmt.Sprint(i))
	}

	comp := persistentsession.NewCompactor(100)
	comp.PreserveRecent = 2
	b := &localAgentBackend{
		sessionSvc: common.NewAutoInitService(fileStore),
		fileStore:  fileStore,
		result:     &ChatFactoryResult{ChatAgent: &agent.ChatAgent{Compactor: comp}},
		sessionID:  parentID,
		appConfig:  &config.AppConfig{},
	}

	status, err := b.Compact(ctx)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !strings.Contains(status, "Compacted context:") {
		t.Fatalf("expected immediate compaction status, got %q", status)
	}
	if strings.Contains(status, "next message") {
		t.Fatalf("must not defer compaction: %q", status)
	}
	// Session id stays the same; events are rewritten and full history archived.
	if b.sessionID != parentID {
		t.Fatalf("active session id should stay %q, got %q", parentID, b.sessionID)
	}
	tip, err := fileStore.Get(ctx, &adksession.GetRequest{AppName: codeAppName, UserID: codeUserID, SessionID: parentID})
	if err != nil {
		t.Fatal(err)
	}
	if n := tip.Session.Events().Len(); n >= 30 {
		t.Fatalf("after compact tip still has %d events", n)
	}
	if b.contextTokens <= 0 {
		t.Fatal("contextTokens should be updated after compact")
	}
}

// TestResumeSession_ApprovalPromptTextSuppressed verifies that the pre-rendered
// ANSI tool box text stored in the approval prompt event (awaiting_approval=true)
// is NOT emitted as inline agent text on resume. During live execution this text
// was shown as a transient overlay; on resume it should be invisible — the tool
// call is already represented by the activity fold from the FunctionCall event.
func TestResumeSession_ApprovalPromptTextSuppressed(t *testing.T) {
	dir := t.TempDir()
	userID := codeUserIDForDir("/work/projectPromptSuppress")

	b := newFileStoreBackend(t, dir, userID)
	ctx := context.Background()

	resp, err := b.sessionSvc.Create(ctx, &adksession.CreateRequest{
		AppName: codeAppName,
		UserID:  b.effectiveUserID(),
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	sess := resp.Session

	// 1. User message
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e1",
		Author: "user",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("create a file", genai.RoleUser),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 2. Agent reasoning text
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e2",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("I'll create the file with write_file:", genai.RoleModel),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 3. FunctionCall (pre-approval)
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e3",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "call_pre",
						Name: "write_file",
						Args: map[string]any{"file_path": "/tmp/test.txt", "content": "hello"},
					},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 4. Approval prompt event — this is what RenderToolBox produces. The text
	// content is the pre-rendered ANSI purple box that must NOT appear on resume.
	approvalPromptText := "╭──────────────────────────────────────╮\n│ 🛠  write_file                        │\n│ file_path: /tmp/test.txt             │\n│ content: hello                       │\n╰──────────────────────────────────────╯"
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e4",
		Author: "astonish_code",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: approvalPromptText}},
			},
		},
		Actions: adksession.EventActions{
			StateDelta: map[string]any{
				"awaiting_approval": true,
				"approval_tool":     "write_file",
				"approval_options":  []string{"Yes", "No"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 5. User approves
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e5",
		Author: "user",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("Always Allow", genai.RoleUser),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 6. FunctionCall (post-approval, new ID)
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e6",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "call_post",
						Name: "write_file",
						Args: map[string]any{"file_path": "/tmp/test.txt", "content": "hello"},
					},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 7. Tool result
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e7",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "user",
				Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{
						ID:       "call_post",
						Name:     "write_file",
						Response: map[string]any{"status": "ok"},
					},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 8. Agent final text
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e8",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("Done! The file has been created.", genai.RoleModel),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Resume and verify
	b2 := newFileStoreBackend(t, dir, userID)
	hist, err := b2.ResumeSession(ctx, sess.ID())
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}

	// The approval prompt text must NOT appear anywhere in history
	for i, h := range hist {
		if strings.Contains(h.Text, "write_file") && strings.Contains(h.Text, "file_path") && h.Kind == "agent" {
			t.Errorf("entry[%d] (kind=%q) contains approval prompt tool box text that should be suppressed:\n%s", i, h.Kind, h.Text)
		}
		// More specific check for the ANSI box content
		if strings.Contains(h.Text, "╭") || strings.Contains(h.Text, "╰") {
			t.Errorf("entry[%d] (kind=%q) contains ANSI box border chars (from RenderToolBox) that should be suppressed:\n%s", i, h.Kind, h.Text)
		}
	}

	// The agent reasoning text SHOULD still be present
	foundReasoning := false
	for _, h := range hist {
		if h.Kind == "agent" && strings.Contains(h.Text, "I'll create the file") {
			foundReasoning = true
			break
		}
	}
	if !foundReasoning {
		t.Error("agent reasoning text should still be present in history")
	}

	// The final agent text SHOULD still be present
	foundFinal := false
	for _, h := range hist {
		if h.Kind == "agent" && strings.Contains(h.Text, "Done! The file has been created.") {
			foundFinal = true
			break
		}
	}
	if !foundFinal {
		t.Error("agent final text should still be present in history")
	}

	// Only one tool_call should appear (the post-approval one)
	var calls []backend.HistoryEntry
	for _, h := range hist {
		if h.Kind == "tool_call" {
			calls = append(calls, h)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool_call (post-approval), got %d", len(calls))
	}
	if calls[0].ToolID != "call_post" {
		t.Errorf("tool_call ToolID = %q, want %q", calls[0].ToolID, "call_post")
	}
}

// TestResumeSession_AutoApprovedPromptTextSuppressed verifies that auto_approved
// events (tools that bypass interactive approval) also have their RenderToolBox
// text suppressed on resume.
func TestResumeSession_AutoApprovedPromptTextSuppressed(t *testing.T) {
	dir := t.TempDir()
	userID := codeUserIDForDir("/work/projectAutoApproval")

	b := newFileStoreBackend(t, dir, userID)
	ctx := context.Background()

	resp, err := b.sessionSvc.Create(ctx, &adksession.CreateRequest{
		AppName: codeAppName,
		UserID:  b.effectiveUserID(),
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	sess := resp.Session

	// 1. User message
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e1",
		Author: "user",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("search for test", genai.RoleUser),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 2. Auto-approved visual event (tool box text with auto_approved=true)
	autoApprovedText := "╭──────────────────────────────────────╮\n│ 🛠  grep_search                       │\n│ pattern: test                        │\n╰──────────────────────────────────────╯"
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e2",
		Author: "astonish_code",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: autoApprovedText}},
			},
		},
		Actions: adksession.EventActions{
			StateDelta: map[string]any{
				"auto_approved": true,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 3. FunctionCall
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e3",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "call_1",
						Name: "grep_search",
						Args: map[string]any{"pattern": "test"},
					},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 4. Tool result
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e4",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "user",
				Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{
						ID:       "call_1",
						Name:     "grep_search",
						Response: map[string]any{"matches": 5},
					},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 5. Agent result text
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:     "e5",
		Author: "chat",
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText("Found 5 matches.", genai.RoleModel),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Resume and verify
	b2 := newFileStoreBackend(t, dir, userID)
	hist, err := b2.ResumeSession(ctx, sess.ID())
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}

	// The auto_approved tool box text must NOT appear in history
	for i, h := range hist {
		if strings.Contains(h.Text, "╭") || strings.Contains(h.Text, "╰") {
			t.Errorf("entry[%d] (kind=%q) contains ANSI box border chars that should be suppressed:\n%s", i, h.Kind, h.Text)
		}
	}

	// Agent result text should be present
	foundResult := false
	for _, h := range hist {
		if h.Kind == "agent" && strings.Contains(h.Text, "Found 5 matches") {
			foundResult = true
			break
		}
	}
	if !foundResult {
		t.Error("agent result text should still be present in history")
	}
}
