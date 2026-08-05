package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	}, emit)

	if len(got) != 1 || got[0].Kind != events.KindApproval {
		t.Fatalf("expected one approval event, got %+v", got)
	}
	if got[0].ToolName != "shell_command" {
		t.Errorf("tool name = %q", got[0].ToolName)
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
	if points[0].ID != "0" || points[1].ID != "1" {
		t.Fatalf("point IDs = %q,%q want 0,1", points[0].ID, points[1].ID)
	}
	if points[1].FileCount != 1 {
		t.Errorf("point[1].FileCount = %d, want 1", points[1].FileCount)
	}
	if points[0].Label != "first request" {
		t.Errorf("point[0].Label = %q", points[0].Label)
	}

	// Roll back to the second user message (index 1): chat keeps only event 0,
	// and the file returns to its pre-turn-1 content.
	entries, err := b.RollbackTo(ctx, "1")
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
