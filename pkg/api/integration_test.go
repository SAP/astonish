package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider/llmerror"
	"github.com/SAP/astonish/pkg/provider/streaming"
	astonishsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// --- Test Infrastructure ---

// integrationTestEnv holds the environment for a single integration test.
type integrationTestEnv struct {
	Runner         *ChatRunner
	EventCh        <-chan ChatEvent
	SessionService session.Service
	ChatAgent      *agent.ChatAgent
	MockLLM        *MockLLM
}

// setupIntegrationTest creates a minimal ChatRunner environment with a MockLLM.
// It wires the mock into a ChatAgent and returns everything needed to run and
// assert against the event stream. A session is pre-created in the service so
// the ADK runner can find it by ID.
func setupIntegrationTest(t *testing.T, mockLLM *MockLLM, tools []tool.Tool) *integrationTestEnv {
	t.Helper()

	sessionService := common.NewAutoInitService(session.InMemoryService())
	promptBuilder := &agent.SystemPromptBuilder{}
	chatAgent := agent.NewChatAgent(mockLLM, tools, nil, sessionService, promptBuilder, false, true)

	// Create the session in the service first (mirrors StudioChatHandler behavior)
	ctx := context.Background()
	createResp, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName: studioChatAppName,
		UserID:  studioChatUserID,
	})
	if err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}
	sessionID := createResp.Session.ID()

	runner := newChatRunner(sessionID, studioChatUserID, studioChatAppName, true)
	ch := runner.Subscribe("test")

	t.Cleanup(func() {
		runner.Stop()
		// Drain any remaining events to avoid goroutine leaks
		go func() {
			for range ch {
			}
		}()
	})

	return &integrationTestEnv{
		Runner:         runner,
		EventCh:        ch,
		SessionService: sessionService,
		ChatAgent:      chatAgent,
		MockLLM:        mockLLM,
	}
}

// runAndCollect starts the ChatRunner in a goroutine and collects all events
// until the runner completes (channel closed) or the timeout expires.
// An optional timeout can be provided; defaults to 10 seconds.
func runAndCollect(t *testing.T, env *integrationTestEnv, msg string, timeout ...time.Duration) []ChatEvent {
	t.Helper()

	to := 10 * time.Second
	if len(timeout) > 0 {
		to = timeout[0]
	}

	userMsg := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{Text: msg}},
	}

	go env.Runner.Run(
		env.ChatAgent,
		env.SessionService,
		env.MockLLM,
		nil, // titleSetter — not needed for integration tests
		userMsg,
		msg,
		true,  // autoApprove
		"",    // systemContext
		nil,   // pinnedToolGroups
		false, // planMode
	)

	return collectEvents(t, env.EventCh, to)
}

// collectEvents reads from the event channel until it closes or the timeout fires.
func collectEvents(t *testing.T, ch <-chan ChatEvent, timeout time.Duration) []ChatEvent {
	t.Helper()
	var events []ChatEvent
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-timer.C:
			t.Fatalf("timed out after %v waiting for events (collected %d so far)", timeout, len(events))
			return events
		}
	}
}

// --- Assertion Helpers ---

// eventsOfType returns all events matching the given type.
func eventsOfType(events []ChatEvent, eventType string) []ChatEvent {
	var result []ChatEvent
	for _, ev := range events {
		if ev.Type == eventType {
			result = append(result, ev)
		}
	}
	return result
}

// assertHasEvent asserts at least one event of the given type exists and returns the first.
func assertHasEvent(t *testing.T, events []ChatEvent, eventType string) ChatEvent {
	t.Helper()
	for _, ev := range events {
		if ev.Type == eventType {
			return ev
		}
	}
	t.Fatalf("expected event type %q but none found in %d events: %v",
		eventType, len(events), eventTypes(events))
	return ChatEvent{}
}

// assertNoEvent asserts no event of the given type exists.
func assertNoEvent(t *testing.T, events []ChatEvent, eventType string) {
	t.Helper()
	for _, ev := range events {
		if ev.Type == eventType {
			t.Fatalf("expected no event of type %q but found: %+v", eventType, ev.Data)
		}
	}
}

// assertEventSequence asserts that the given event types appear in order
// (not necessarily contiguously) within the events slice.
func assertEventSequence(t *testing.T, events []ChatEvent, expectedTypes ...string) {
	t.Helper()
	idx := 0
	for _, ev := range events {
		if idx < len(expectedTypes) && ev.Type == expectedTypes[idx] {
			idx++
		}
	}
	if idx != len(expectedTypes) {
		t.Fatalf("event sequence mismatch: expected %v in order, but only matched %d/%d\nactual types: %v",
			expectedTypes, idx, len(expectedTypes), eventTypes(events))
	}
}

// assertEventData asserts a specific key=value in an event's Data map.
func assertEventData(t *testing.T, ev ChatEvent, key string, expected any) {
	t.Helper()
	val, ok := ev.Data[key]
	if !ok {
		t.Fatalf("event %q missing data key %q; data: %v", ev.Type, key, ev.Data)
	}
	// Compare as strings for simplicity (handles int/float/string differences)
	got := toString(val)
	want := toString(expected)
	if got != want {
		t.Errorf("event %q data[%q] = %v, want %v", ev.Type, key, val, expected)
	}
}

// assertFirstAndLast asserts the first event is "session" and the last is "done".
func assertFirstAndLast(t *testing.T, events []ChatEvent) {
	t.Helper()
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (session + done), got %d", len(events))
	}
	if events[0].Type != "session" {
		t.Errorf("first event should be 'session', got %q", events[0].Type)
	}
	if events[len(events)-1].Type != "done" {
		t.Errorf("last event should be 'done', got %q", events[len(events)-1].Type)
	}
}

// eventTypes returns a slice of event type strings for debugging.
func eventTypes(events []ChatEvent) []string {
	types := make([]string, len(events))
	for i, ev := range events {
		types[i] = ev.Type
	}
	return types
}

// toString converts a value to string for comparison.
func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return intToStr(val)
	case int32:
		return intToStr(int(val))
	case int64:
		return intToStr(int(val))
	case float64:
		// JSON numbers come back as float64
		if val == float64(int(val)) {
			return intToStr(int(val))
		}
		return floatToStr(val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func intToStr(v int) string {
	s := make([]byte, 0, 20)
	if v < 0 {
		s = append(s, '-')
		v = -v
	}
	if v == 0 {
		return "0"
	}
	digits := make([]byte, 0, 20)
	for v > 0 {
		digits = append(digits, byte('0'+v%10))
		v /= 10
	}
	for i := len(digits) - 1; i >= 0; i-- {
		s = append(s, digits[i])
	}
	return string(s)
}

func floatToStr(v float64) string {
	// Simple float to string - use fmt for complex cases
	return toString(int(v))
}

// --- Mock Tool Factory ---

// mockToolArgs is a generic args type for mock tools.
type mockToolArgs struct {
	Input map[string]any `json:"input,omitempty"`
}

// mockToolResult is a generic result type for mock tools.
type mockToolResult struct {
	Output map[string]any `json:"output,omitempty"`
}

// newMockTool creates an ADK-compatible tool using functiontool.New.
// The tool accepts any JSON args and returns the provided result map.
func newMockTool(name, description string, result map[string]any) tool.Tool {
	handler := func(_ tool.Context, _ mockToolArgs) (mockToolResult, error) {
		return mockToolResult{Output: result}, nil
	}
	t, err := functiontool.New(functiontool.Config{
		Name:        name,
		Description: description,
	}, handler)
	if err != nil {
		panic(fmt.Sprintf("failed to create mock tool %q: %v", name, err))
	}
	return t
}

// newMockErrorTool creates an ADK-compatible tool that always returns an error.
// This tests error propagation from tool execution → runner → SSE event stream.
func newMockErrorTool(name, description, errMsg string) tool.Tool {
	handler := func(_ tool.Context, _ mockToolArgs) (mockToolResult, error) {
		return mockToolResult{}, fmt.Errorf("%s", errMsg)
	}
	t, err := functiontool.New(functiontool.Config{
		Name:        name,
		Description: description,
	}, handler)
	if err != nil {
		panic(fmt.Sprintf("failed to create mock error tool %q: %v", name, err))
	}
	return t
}

// newMockPanicTool creates an ADK-compatible tool that panics during execution.
// This tests that panics in tool execution are caught and converted to error events.
func newMockPanicTool(name, description, panicMsg string) tool.Tool {
	handler := func(_ tool.Context, _ mockToolArgs) (mockToolResult, error) {
		panic(panicMsg)
	}
	t, err := functiontool.New(functiontool.Config{
		Name:        name,
		Description: description,
	}, handler)
	if err != nil {
		panic(fmt.Sprintf("failed to create mock panic tool %q: %v", name, err))
	}
	return t
}

// runAndCollectWithApprove is like runAndCollect but allows setting autoApprove to false.
func runAndCollectWithApprove(t *testing.T, env *integrationTestEnv, msg string, autoApprove bool, systemContext string, timeout ...time.Duration) []ChatEvent {
	t.Helper()

	to := 10 * time.Second
	if len(timeout) > 0 {
		to = timeout[0]
	}

	userMsg := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{Text: msg}},
	}

	go env.Runner.Run(
		env.ChatAgent,
		env.SessionService,
		env.MockLLM,
		nil, // titleSetter
		userMsg,
		msg,
		autoApprove,
		systemContext,
		nil,   // pinnedToolGroups
		false, // planMode
	)

	return collectEvents(t, env.EventCh, to)
}

// --- Real HTTP/SSE memory acceptance infrastructure ---

// Freeze provider-visible JSON at entry, before ADK mutates pointers on later
// tool rounds. Runtime Tools executors are not JSON; retain their names separately.
// MockLLM remains unchanged for existing runner integration tests.
type memoryCapturingLLM struct {
	Script    *MockLLM
	mu        sync.Mutex
	requests  [][]byte
	toolNames [][]string
}

func (m *memoryCapturingLLM) Name() string { return m.Script.Name() }
func (m *memoryCapturingLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	data, err := json.Marshal(req)
	if err != nil {
		return func(yield func(*model.LLMResponse, error) bool) { yield(nil, fmt.Errorf("capture request: %w", err)) }
	}
	names := make([]string, 0, len(req.Tools))
	for name := range req.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	m.mu.Lock()
	m.requests = append(m.requests, data)
	m.toolNames = append(m.toolNames, names)
	m.mu.Unlock()
	return m.Script.GenerateContent(ctx, req, stream)
}

// Requests returns independent deep copies, never mutable provider pointers.
func (m *memoryCapturingLLM) Requests(t *testing.T) []*model.LLMRequest {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.LLMRequest, len(m.requests))
	for i, data := range m.requests {
		out[i] = &model.LLMRequest{}
		if err := json.Unmarshal(data, out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// Preparation freezes source results; parallel category searches and subsequent
// source replacement never share mutable slices with the current invocation.
type memoryAcceptanceRetriever struct {
	mu      sync.Mutex
	results map[string][]agent.KnowledgeSearchResult
	errors  map[string]error
	queries []string
}
type memoryAcceptancePrepared struct {
	query   string
	results []agent.KnowledgeSearchResult
}

func (p *memoryAcceptancePrepared) SemanticQuery() string           { return p.query }
func (p *memoryAcceptancePrepared) Embedding() ([]float32, uintptr) { return nil, 0 }
func (p *memoryAcceptancePrepared) Search(ctx context.Context, max int, min float64, category string) ([]agent.KnowledgeSearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []agent.KnowledgeSearchResult
	for _, result := range p.results {
		if result.Score < min || (category != "" && result.Category != category) {
			continue
		}
		out = append(out, result)
		if len(out) == max {
			break
		}
	}
	return out, nil
}
func (r *memoryAcceptanceRetriever) Set(query string, results []agent.KnowledgeSearchResult, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results[query] = append([]agent.KnowledgeSearchResult(nil), results...)
	r.errors[query] = err
}
func (r *memoryAcceptanceRetriever) Prepare(ctx context.Context, semantic, keyword string) (agent.PreparedKnowledgeRetrieval, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, semantic)
	if err := r.errors[semantic]; err != nil {
		return nil, err
	}
	return &memoryAcceptancePrepared{query: semantic, results: append([]agent.KnowledgeSearchResult(nil), r.results[semantic]...)}, nil
}

type memoryAcceptanceIdentity string

const (
	memoryIdentityAdmin    memoryAcceptanceIdentity = "admin"
	memoryIdentityNonadmin memoryAcceptanceIdentity = "nonadmin"
	memoryIdentityDenied   memoryAcceptanceIdentity = "denied"
	memoryIdentityForeign  memoryAcceptanceIdentity = "foreign"
)

// Do not t.Parallel: Studio managers/backend are globals. Native HTTP handlers,
// ChatAgent, ChatRunner and SQLite persistence run unchanged. Router/URL are
// reusable by the later SPA/browser harness; authentication is test-only.
type memoryHTTPAcceptance struct {
	URL       string
	Server    *httptest.Server
	Router    *mux.Router
	Client    *http.Client
	Store     *entstore.Store
	Services  *store.Services
	ChatAgent *agent.ChatAgent
	LLM       *memoryCapturingLLM
	Retriever *memoryAcceptanceRetriever
	UserID    string
	OrgSlug   string
	TeamSlug  string
	cfg       entstore.Config
	tools     []tool.Tool
	mu        sync.RWMutex // excludes HTTP requests during store replacement
}

func setupMemoryHTTPAcceptance(t *testing.T, script *MockLLM, tools []tool.Tool) *memoryHTTPAcceptance {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	oldManager := GetChatManager() // initialize Once before replacing the pointer
	oldRegistry := getChatRunnerRegistry()
	oldBackend := platformBackendInstance
	env := &memoryHTTPAcceptance{
		UserID: "318035a3-9f1b-460c-b02a-fcfba15d82a1", OrgSlug: "memory-acceptance", TeamSlug: "general",
		cfg:   entstore.Config{DSN: "file:" + filepath.Join(dir, "platform.db"), DataDir: dir},
		tools: tools, LLM: &memoryCapturingLLM{Script: script},
		Retriever: &memoryAcceptanceRetriever{results: make(map[string][]agent.KnowledgeSearchResult), errors: make(map[string]error)},
	}
	globalChatManager = &ChatManager{active: make(map[string]context.CancelFunc)}
	globalChatRunnerRegistry = &chatRunnerRegistry{runners: make(map[string]*ChatRunner)}
	t.Cleanup(func() {
		env.stopRunners(t)
		if env.Server != nil {
			env.Server.Close()
		}
		if env.Store != nil {
			if err := env.Store.Close(); err != nil {
				t.Error(err)
			}
		}
		globalChatManager = oldManager
		globalChatRunnerRegistry = oldRegistry
		platformBackendInstance = oldBackend
	})
	env.open(t)
	env.Router = mux.NewRouter()
	env.Router.HandleFunc("/api/studio/chat", StudioChatHandler).Methods(http.MethodPost)
	env.Router.HandleFunc("/api/studio/sessions", StudioSessionsHandler).Methods(http.MethodGet)
	env.Router.HandleFunc("/api/studio/sessions/{id}", StudioSessionHandler).Methods(http.MethodGet)
	env.Router.HandleFunc("/api/studio/sessions/{id}/status", StudioChatStatusHandler).Methods(http.MethodGet)
	env.Router.HandleFunc("/api/studio/sessions/{id}/stream", StudioChatStreamHandler).Methods(http.MethodGet)
	env.Router.HandleFunc("/api/studio/sessions/{id}/knowledge-debug", StudioKnowledgeDebugHandler).Methods(http.MethodGet)
	env.Router.HandleFunc("/api/platform/admin/sessions/{id}/knowledge-debug", PlatformAdminKnowledgeDebugHandler).Methods(http.MethodGet)
	env.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.mu.RLock()
		defer env.mu.RUnlock()
		identity := memoryAcceptanceIdentity(r.Header.Get("X-Memory-Test-Identity"))
		if identity == "" {
			identity = memoryIdentityAdmin
		}
		if identity == memoryIdentityDenied {
			env.Router.ServeHTTP(w, r)
			return
		}
		user, org, role := env.UserID, env.OrgSlug, "superadmin"
		switch identity {
		case memoryIdentityAdmin:
		case memoryIdentityNonadmin:
			role = ""
		case memoryIdentityForeign:
			user, org, role = "d22c7284-d091-4c20-a451-f379280e0b34", "foreign-org", ""
		default:
			http.Error(w, "invalid test identity", http.StatusUnauthorized)
			return
		}
		orgStore, err := env.Store.ForOrg(org)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Only scoped persistence: no external catalogs, sandbox or model pool.
		svc := *env.Services
		svc.PersonalSessions = orgStore.ForUser(user).Sessions()
		svc.Sessions = orgStore.ForTeam(env.TeamSlug).Sessions()
		ctx := store.WithServices(r.Context(), &svc)
		ctx = store.WithTenantContext(ctx, &store.TenantContext{OrgSlug: org, TeamSlug: env.TeamSlug, UserID: user})
		ctx = WithPlatformUser(ctx, &PlatformUser{ID: user, OrgSlug: org, TeamSlug: env.TeamSlug, PlatformRole: role, Role: "member"})
		ctx, err = execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: user, OrgSlug: org, TeamSlug: env.TeamSlug, Authenticated: true})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		env.Router.ServeHTTP(w, r.WithContext(ctx))
	}))
	env.URL = env.Server.URL
	env.Client = env.Server.Client()
	env.Client.Timeout = 20 * time.Second // whole response deadline, including SSE
	return env
}
func (env *memoryHTTPAcceptance) open(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	svc, db, err := entstore.NewPlatformServices(ctx, env.cfg)
	if err != nil {
		t.Fatalf("open acceptance SQLite: %v", err)
	}
	env.Services, env.Store = svc, db
	// Explicitly disable every standard external MCP server, including keyless
	// defaults. This is fixture DB configuration, not a production test hook.
	env.Services.PlatformMCPServers = db.PlatformMCPServers()
	disabled := false
	for _, standard := range config.GetStandardServers() {
		if standard.Kind == "model" {
			continue
		}
		if err := env.Services.PlatformMCPServers.Save(ctx, &store.MCPServer{Name: standard.ID, Transport: "stdio", Enabled: &disabled}); err != nil {
			t.Fatal(err)
		}
	}
	platformBackendInstance = db // no unrelated SetPlatformBackend sandbox side effects
	org, err := db.Organizations().GetBySlug(ctx, env.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	if org == nil {
		org = &store.Organization{ID: "ecc43aa0-c540-4d93-9228-36cf553f658f", Name: "Memory acceptance", Slug: env.OrgSlug, Status: "active"}
		if err := db.Organizations().Create(ctx, org); err != nil {
			t.Fatal(err)
		}
		if err := db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
			t.Fatal(err)
		}
		foreignOrg := &store.Organization{ID: "0235a29b-7997-4a36-9368-0d77a6d2c0ef", Name: "Foreign acceptance", DBName: "foreign-acceptance", Slug: "foreign-org", Status: "active"}
		if err := db.Organizations().Create(ctx, foreignOrg); err != nil {
			t.Fatal(err)
		}
		if err := db.ProvisionOrg(ctx, foreignOrg.ID, foreignOrg.Slug); err != nil {
			t.Fatal(err)
		}
		foreignUser := &store.User{ID: "d22c7284-d091-4c20-a451-f379280e0b34", Email: "foreign@example.invalid", DisplayName: "Foreign user", Status: "active"}
		if err := db.Users().Create(ctx, foreignUser); err != nil {
			t.Fatal(err)
		}
		if err := db.Organizations().AddMember(ctx, foreignUser.ID, foreignOrg.ID, "member"); err != nil {
			t.Fatal(err)
		}
		user := &store.User{ID: env.UserID, Email: "memory-acceptance@example.invalid", DisplayName: "Memory acceptance", PlatformRole: "superadmin", Status: "active"}
		if err := db.Users().Create(ctx, user); err != nil {
			t.Fatal(err)
		}
		env.UserID = user.ID
		if err := db.Organizations().AddMember(ctx, env.UserID, org.ID, "owner"); err != nil {
			t.Fatal(err)
		}
	}
	orgStore, err := db.ForOrg(env.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	env.Services.PersonalSessions = orgStore.ForUser(env.UserID).Sessions()
	env.Services.Sessions = orgStore.ForTeam(env.TeamSlug).Sessions()
	env.ChatAgent = agent.NewChatAgent(env.LLM, env.tools, nil, env.Services.PersonalSessions, &agent.SystemPromptBuilder{}, false, true)
	env.ChatAgent.KnowledgeRetrieval = env.Retriever.Prepare
	SetStudioChatInitFunc(func(context.Context) (*StudioChatComponents, error) {
		return &StudioChatComponents{
			ChatAgent: env.ChatAgent, SessionService: env.Services.PersonalSessions,
			// ChatAgent has the model; nil auxiliary LLM disables title refinement.
			LLM: nil, ProviderName: "acceptance", ModelName: env.LLM.Name(),
			InternalToolCount: len(env.tools), MemoryActive: true, SandboxEnabled: false,
		}, nil
	})
}
func (env *memoryHTTPAcceptance) stopRunners(t *testing.T) {
	t.Helper()
	registry := getChatRunnerRegistry()
	registry.mu.RLock()
	var runners []*ChatRunner
	for _, runner := range registry.runners {
		runners = append(runners, runner)
		runner.Stop()
	}
	registry.mu.RUnlock()
	deadline := time.Now().Add(10 * time.Second)
	for _, runner := range runners {
		for !runner.IsDone() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if !runner.IsDone() {
			t.Errorf("runner %s did not stop before store close", runner.SessionID)
		}
	}
	registry.StopAll()
}

// Reopen keeps URL/source/script but recreates DB handles, agent and manager.
// Consume all active turns before calling; this never rewrites stored transcripts.
func (env *memoryHTTPAcceptance) Reopen(t *testing.T) {
	t.Helper()
	env.stopRunners(t)
	env.mu.Lock()
	defer env.mu.Unlock()
	if err := env.Store.Close(); err != nil {
		t.Fatal(err)
	}
	env.Store = nil
	globalChatManager = &ChatManager{active: make(map[string]context.CancelFunc)}
	globalChatRunnerRegistry = &chatRunnerRegistry{runners: make(map[string]*ChatRunner)}
	env.open(t)
}
func (env *memoryHTTPAcceptance) Request(t *testing.T, method, path string, body any, identity memoryAcceptanceIdentity) *http.Response {
	t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, env.URL+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Memory-Test-Identity", string(identity))
	resp, err := env.Client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}
func consumeMemoryAcceptanceSSE(t *testing.T, resp *http.Response) []ChatEvent {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected SSE 200, got %s: %s", resp.Status, data)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	var events []ChatEvent
	var eventType string
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if eventType != "" {
				ev := ChatEvent{Type: eventType}
				if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &ev.Data); err != nil {
					t.Fatalf("SSE %s: %v", eventType, err)
				}
				events = append(events, ev)
				if eventType == "done" {
					return events
				}
			}
			eventType, data = "", nil
		} else if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	t.Fatalf("SSE ended before framed done (events=%v, error=%v)", eventTypes(events), scanner.Err())
	return nil
}
func (env *memoryHTTPAcceptance) Turn(t *testing.T, sessionID, message string, debug bool) []ChatEvent {
	t.Helper()
	return consumeMemoryAcceptanceSSE(t, env.Request(t, http.MethodPost, "/api/studio/chat", StudioChatRequest{SessionID: sessionID, Message: message, Debug: debug, AutoApprove: true}, memoryIdentityAdmin))
}
func (env *memoryHTTPAcceptance) Detail(t *testing.T, sessionID string) StudioSessionDetailResponse {
	t.Helper()
	resp := env.Request(t, http.MethodGet, "/api/studio/sessions/"+sessionID, nil, memoryIdentityAdmin)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("detail: %s: %s", resp.Status, data)
	}
	var detail StudioSessionDetailResponse
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	return detail
}
func (env *memoryHTTPAcceptance) Knowledge(t *testing.T, sessionID, invocationID string) map[string]any {
	t.Helper()
	resp := env.Request(t, http.MethodGet, "/api/studio/sessions/"+sessionID+"/knowledge-debug?invocationId="+invocationID, nil, memoryIdentityAdmin)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("debug: %s", resp.Status)
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestMemoryHTTPAcceptance_LifetimeAndDebug(t *testing.T) {
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(
		ToolCallTurn("acceptance_echo", map[string]any{}), TextTurn("Answer A."),
		TextTurn("Answer B."), TextTurn("Answer C."), TextTurn("Answer D."),
	), []tool.Tool{newMockTool("acceptance_echo", "Local echo", map[string]any{"ok": true})})
	env.Retriever.Set("turn A", []agent.KnowledgeSearchResult{{Path: "a.md", Snippet: "MEMORY_A", Score: .99}}, nil)
	env.Retriever.Set("turn B", []agent.KnowledgeSearchResult{{Path: "b.md", Snippet: "MEMORY_B", Score: .99}}, nil)
	var sessionID string
	var ids []string
	var saved []map[string]any
	for i, query := range []string{"turn A", "turn B", "turn C"} {
		events := env.Turn(t, sessionID, query, true)
		assertNoEvent(t, events, "error")
		sessionID = toString(assertHasEvent(t, events, "session").Data["sessionId"])
		id := toString(assertHasEvent(t, events, "text").Data["invocationId"])
		ids = append(ids, id)
		data := env.Knowledge(t, sessionID, id)
		knowledge, ok := data["knowledge"].(map[string]any)
		if !ok || knowledge["dispatched"] != true {
			t.Fatalf("turn %d dispatch snapshot: %#v", i, data)
		}
		if knowledge["invocation_id"] != id || knowledge["capture_status"] != "captured" {
			t.Fatalf("wrong capture attribution: %#v", knowledge)
		}
		snapshot, ok := knowledge["snapshot"].(map[string]any)
		if !ok {
			t.Fatalf("missing snapshot: %#v", knowledge)
		}
		text := toString(snapshot["text"])
		if i < 2 && !strings.Contains(text, []string{"MEMORY_A", "MEMORY_B"}[i]) {
			t.Fatalf("wrong text: %s; knowledge=%#v queries=%v", text, knowledge, env.Retriever.queries)
		}
		if i == 2 && (text != "" || knowledge["result_count"] != float64(0)) {
			t.Fatalf("empty retrieval not explicit: %#v", knowledge)
		}
		saved = append(saved, data)
		visible, _ := json.Marshal(events)
		if strings.Contains(string(visible), "MEMORY_") || strings.Contains(string(visible), "_knowledge_injection") {
			t.Fatal("memory leaked into SSE")
		}
	}
	requests := env.LLM.Requests(t)
	if len(requests) != 4 {
		t.Fatalf("requests=%d, want 4", len(requests))
	}
	for i, req := range requests {
		data, _ := json.Marshal(req.Contents)
		text := string(data)
		wantA, wantB := 0, 0
		if i < 2 {
			wantA = 1
		}
		if i == 2 {
			wantB = 1
		}
		if strings.Count(text, "MEMORY_A") != wantA || strings.Count(text, "MEMORY_B") != wantB {
			t.Fatalf("request %d replay: %s", i, text)
		}
		if i < 3 && !strings.Contains(req.Contents[len(req.Contents)-1].Parts[0].Text, []string{"MEMORY_A", "MEMORY_A", "MEMORY_B"}[i]) {
			t.Fatal("memory is not at tail")
		}
		if !reflect.DeepEqual(req.Config.SystemInstruction, requests[0].Config.SystemInstruction) || !reflect.DeepEqual(req.Config.Tools, requests[0].Config.Tools) {
			t.Fatal("core/tool declarations changed")
		}
	}
	visible, _ := json.Marshal(env.Detail(t, sessionID).Messages)
	if strings.Contains(string(visible), "MEMORY_") {
		t.Fatal("memory leaked into restored history")
	}
	env.Retriever.Set("turn A", nil, nil)
	env.Retriever.Set("turn B", nil, nil)
	env.Reopen(t)
	for i, id := range ids {
		if !reflect.DeepEqual(saved[i], env.Knowledge(t, sessionID, id)) {
			t.Fatalf("snapshot %d changed on reopen", i)
		}
	}
	if env.Knowledge(t, sessionID, "unknown")["knowledge"] != nil {
		t.Fatal("unknown invocation inherited another turn")
	}
	events := env.Turn(t, sessionID, "turn D", false)
	assertNoEvent(t, events, "error")
	id := toString(assertHasEvent(t, events, "text").Data["invocationId"])
	knowledge := env.Knowledge(t, sessionID, id)["knowledge"].(map[string]any)
	if knowledge["capture_status"] != "disabled" || knowledge["snapshot"] != nil {
		t.Fatalf("debug-off capture: %#v", knowledge)
	}
	last := env.LLM.Requests(t)
	data, _ := json.Marshal(last[len(last)-1])
	if strings.Contains(string(data), "MEMORY_") {
		t.Fatal("reopened invocation replayed memory")
	}
	t.Log("executed 8/8 lifetime cases: A tool rounds, B replacement, C empty, frozen core/tools, SSE/history isolation, immutable reopen, unknown invocation, debug-off resume; no compaction triggered")
}

type memoryTransientModel struct{ calls int }

func (m *memoryTransientModel) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls == 1 {
			yield(nil, &llmerror.LLMError{StatusCode: 503, Retryable: true, RetryAfter: time.Millisecond})
			return
		}
		yield(turnToResponse(TextTurn("Recovered.")), nil)
	}
}

func TestMemoryHTTPAcceptance_DebugEquivalenceAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		script       *MockLLM
		retrievalErr error
		wantCalls    int
	}{
		{"provider error", NewMockLLM(ErrorTurn("invalid_request", "deterministic rejection")), nil, 1},
		{"retry", &MockLLM{funcLLM: &memoryTransientModel{}}, nil, 2},
		{"retrieval failure", NewMockLLM(TextTurn("unused")), fmt.Errorf("retrieval unavailable"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupMemoryHTTPAcceptance(t, tc.script, nil)
			env.Retriever.Set("failure check", []agent.KnowledgeSearchResult{{Path: "failure.md", Snippet: "MEMORY_FAILURE", Score: .99}}, tc.retrievalErr)
			events := env.Turn(t, "", "failure check", true)
			if tc.name != "retry" {
				assertHasEvent(t, events, "error")
			} else {
				assertEventData(t, assertHasEvent(t, events, "text"), "text", "Recovered.")
			}
			requests := env.LLM.Requests(t)
			if len(requests) != tc.wantCalls {
				t.Fatalf("provider calls=%d want=%d", len(requests), tc.wantCalls)
			}
			for _, req := range requests {
				data, _ := json.Marshal(req.Contents)
				if strings.Count(string(data), "MEMORY_FAILURE") != 1 {
					t.Fatal("missing or repeated retry memory")
				}
			}
			sid := toString(assertHasEvent(t, events, "session").Data["sessionId"])
			transcript, err := env.Services.PersonalSessions.ReadTranscriptEvents(context.Background(), studioChatAppName, env.UserID, sid)
			if err != nil {
				t.Fatal(err)
			}
			var invocation string
			for _, ev := range transcript {
				if ev.Actions.StateDelta["_knowledge_injection"] != nil {
					invocation = ev.InvocationID
				}
			}
			if tc.wantCalls > 0 {
				k := env.Knowledge(t, sid, invocation)["knowledge"].(map[string]any)
				if k["dispatched"] != true {
					t.Fatal("provider dispatch not recorded on error/retry")
				}
			} else if invocation != "" {
				t.Fatal("retrieval failure fabricated injection")
			}
		})
	}
	t.Run("debug equivalence", func(t *testing.T) {
		env := setupMemoryHTTPAcceptance(t, NewMockLLM(TextTurn("Same."), TextTurn("Same.")), nil)
		env.Retriever.Set("same", []agent.KnowledgeSearchResult{{Path: "same.md", Snippet: "MEMORY_SAME", Score: .99}}, nil)
		env.Turn(t, "", "same", false)
		env.Turn(t, "", "same", true)
		requests := env.LLM.Requests(t)
		if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
			t.Fatal("Debug changed model input")
		}
	})
}

func TestMemoryHTTPAcceptance_CompactionCompatibility(t *testing.T) {
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(TextTurn("old one"), TextTurn("old two"), TextTurn("new answer")), nil)
	first := env.Turn(t, "", "old question", false)
	sid := toString(assertHasEvent(t, first, "session").Data["sessionId"])
	env.Turn(t, sid, "older question", false)
	compactor := astonishsession.NewCompactor(1000000)
	compactor.PreserveRecent = 1
	var summarized, persisted int
	compactor.LLM = func(_ context.Context, prompt string) (string, error) {
		summarized++
		if strings.Contains(prompt, "MEMORY_COMPACTION") {
			t.Error("temporary memory entered summarizer")
		}
		return "Earlier ordinary conversation.", nil
	}
	compactor.SetPersistCompacted(func(_ context.Context, _ string, contents []*genai.Content) error {
		persisted++
		data, _ := json.Marshal(contents)
		if strings.Contains(string(data), "MEMORY_COMPACTION") {
			t.Error("temporary memory entered compacted persistence")
		}
		return nil
	})
	env.ChatAgent.Compactor = compactor
	compactor.ForceNextCompaction()
	env.Retriever.Set("new question", []agent.KnowledgeSearchResult{{Path: "current.md", Snippet: "MEMORY_COMPACTION", Score: .99}}, nil)
	events := env.Turn(t, sid, "new question", true)
	assertNoEvent(t, events, "error")
	if summarized != 1 || persisted != 1 {
		t.Fatalf("summarized/persisted = %d/%d", summarized, persisted)
	}
	requests := env.LLM.Requests(t)
	data, _ := json.Marshal(requests[len(requests)-1].Contents)
	if strings.Count(string(data), "MEMORY_COMPACTION") != 1 {
		t.Fatal("current suffix lost or repeated after compaction")
	}
}

func TestMemoryHTTPAcceptance_LegacyAndPrepared(t *testing.T) {
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(TextTurn("old"), TextTurn("current")), nil)
	first := env.Turn(t, "", "old", false)
	sid := toString(assertHasEvent(t, first, "session").Data["sessionId"])
	ctx := context.Background()
	got, err := env.Services.PersonalSessions.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: env.UserID, SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	legacy := session.NewEvent("old")
	legacy.ID = "turn-context-legacy"
	legacy.Author = "user"
	legacy.Content = genai.NewContentFromText(agent.RenderExternalTurnContext(&agent.PromptOverrides{SessionContext: "KEEP_LEGACY_TASK"}, "", "**old.md**\nMEMORY_LEGACY"), "user")
	legacy.Actions.StateDelta = map[string]any{"_astonish_turn_context": true}
	if err := env.Services.PersonalSessions.AppendEvent(ctx, got.Session, legacy); err != nil {
		t.Fatal(err)
	}
	prepared := session.NewEvent("prepared-only")
	prepared.Author = "system"
	prepared.Actions.StateDelta = map[string]any{"_knowledge_injection": map[string]any{"version": 1, "invocation_id": "prepared-only", "capture_status": "captured", "dispatched": false, "type": "none", "result_count": 0}}
	if err := env.Services.PersonalSessions.AppendEvent(ctx, got.Session, prepared); err != nil {
		t.Fatal(err)
	}
	k := env.Knowledge(t, sid, "prepared-only")["knowledge"].(map[string]any)
	if k["dispatched"] != false {
		t.Fatal("prepared-only record claims dispatch")
	}
	events := env.Turn(t, sid, "current", true)
	assertNoEvent(t, events, "error")
	requests := env.LLM.Requests(t)
	data, _ := json.Marshal(requests[len(requests)-1].Contents)
	if strings.Contains(string(data), "MEMORY_LEGACY") || !strings.Contains(string(data), "KEEP_LEGACY_TASK") {
		t.Fatal("legacy projection lost task or retained memory")
	}
}

func TestMemoryHTTPAcceptance_TeamDebugScope(t *testing.T) {
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(), nil)
	ctx := context.Background()
	org, err := env.Store.ForOrg(env.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	if err := org.Teams().CreateTeam(ctx, &store.Team{Name: "General", Slug: env.TeamSlug, SchemaName: "team_general"}); err != nil {
		t.Fatal(err)
	}
	created, err := env.Services.Sessions.Create(ctx, &session.CreateRequest{AppName: studioChatAppName, UserID: env.UserID})
	if err != nil {
		t.Fatal(err)
	}
	sid := created.Session.ID()
	event := session.NewEvent("team-invocation")
	event.Author = "system"
	event.Actions.StateDelta = map[string]any{"_knowledge_injection": map[string]any{"version": 1, "invocation_id": "team-invocation", "capture_status": "disabled", "dispatched": true}}
	if err := env.Services.Sessions.AppendEvent(ctx, created.Session, event); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/studio/sessions/" + sid + "/knowledge-debug?invocationId=team-invocation",
		"/api/platform/admin/sessions/" + sid + "/knowledge-debug?org=" + env.OrgSlug + "&scope=team&team=" + env.TeamSlug + "&invocationId=team-invocation",
	} {
		resp := env.Request(t, http.MethodGet, path, nil, memoryIdentityAdmin)
		var data map[string]any
		err := json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || data["knowledge"] == nil {
			t.Fatalf("team debug: %s %#v %v", resp.Status, data, err)
		}
		denied := env.Request(t, http.MethodGet, path, nil, memoryIdentityForeign)
		denied.Body.Close()
		if denied.StatusCode != http.StatusForbidden {
			t.Fatalf("foreign debug access: %s", denied.Status)
		}
	}
}

func chatBrowserPrerequisites(t *testing.T) (dist, bin string) {
	t.Helper()
	if os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("explicit browser acceptance: set ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1")
	}
	var pkg struct {
		Version string `json:"version"`
	}
	data, err := os.ReadFile("../../web/package.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	dist, err = filepath.Abs(filepath.Join("../../web/dist", pkg.Version))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatalf("build Studio SPA before browser acceptance: %v", err)
	}
	bin = os.Getenv("ASTONISH_MEMORY_BROWSER_PATH")
	if bin == "" {
		var found bool
		bin, found = launcher.LookPath()
		if !found {
			t.Fatal("browser prerequisite missing; set ASTONISH_MEMORY_BROWSER_PATH")
		}
	}
	return dist, bin
}

func setupChatBrowserAcceptance(t *testing.T, env *memoryHTTPAcceptance, dist string, nonadmin, debugError *atomic.Bool) {
	t.Helper()
	env.Router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if debugError.Load() && strings.HasSuffix(r.URL.Path, "/knowledge-debug") {
				http.Error(w, "Controlled debug failure", http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	env.Router.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var fixture any
		switch r.URL.Path {
		case "/api/auth/setup-status":
			fixture = map[string]any{"initialized": true, "auth_mode": "builtin"}
		case "/api/auth/me":
			role := "superadmin"
			if nonadmin.Load() {
				role = "member"
			}
			fixture = map[string]any{"user": map[string]any{"id": env.UserID, "email": "memory@example.invalid", "display_name": "Memory acceptance", "role": "owner", "platform_role": role}, "org": map[string]any{"slug": env.OrgSlug, "name": "Memory acceptance"}, "team": env.TeamSlug}
		case "/api/orgs":
			fixture = map[string]any{"orgs": []any{}, "active_org": env.OrgSlug}
		case "/api/teams":
			fixture = []any{map[string]any{"slug": env.TeamSlug, "name": "General"}}
		case "/api/settings/status":
			fixture = map[string]any{"setupRequired": false}
		case "/api/version":
			fixture = map[string]any{"version": "dev"}
		case "/api/agents", "/api/tools":
			fixture = []any{}
		case "/api/settings/config":
			fixture = map[string]any{}
		case "/api/settings/providers/effective":
			fixture = map[string]any{"providers": map[string]any{}}
		}
		if fixture != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(fixture)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			t.Logf("unhandled browser bootstrap %s", r.URL.Path)
			http.Error(w, "fixture endpoint unavailable", 404)
			return
		}
		path := filepath.Join(dist, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dist, "index.html"))
	})
}

func launchChatAcceptanceBrowser(t *testing.T, bin, url string) *rod.Page {
	t.Helper()
	launch := launcher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
	control, err := launch.Launch()
	if err != nil {
		t.Fatalf("launch isolated browser: %v", err)
	}
	t.Cleanup(launch.Cleanup)
	browser := rod.New().ControlURL(control).MustConnect()
	t.Cleanup(func() { _ = browser.Close() })
	page := browser.MustPage(url).Timeout(30 * time.Second)
	return page.WithPanic(func(v any) {
		state, _ := page.CancelTimeout().Timeout(2 * time.Second).Eval(`() => Array.from(document.querySelectorAll('[data-testid="tool-activity-block"]')).map(e => e.outerHTML).join('\n')`)
		t.Fatalf("browser failure: %v\n%s\n%v", v, debug.Stack(), state)
	})
}

func TestMemoryHTTPAcceptance_Browser(t *testing.T) {
	dist, bin := chatBrowserPrerequisites(t)
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(TextTurn("Browser baseline."), ToolCallTurn("acceptance_echo", map[string]any{}), TextTurn("Browser answer A."), TextTurn("Browser answer B."), TextTurn("Browser answer C.")), []tool.Tool{newMockTool("acceptance_echo", "Local echo", map[string]any{"ok": true})})
	first := env.Turn(t, "", "browser baseline", false)
	sid := toString(assertHasEvent(t, first, "session").Data["sessionId"])
	env.Retriever.Set("turn A", []agent.KnowledgeSearchResult{{Path: "a.md", Snippet: "MEMORY_A", Score: .99}}, nil)
	env.Retriever.Set("turn B", []agent.KnowledgeSearchResult{{Path: "b.md", Snippet: "MEMORY_B", Score: .99}}, nil)
	var nonadmin, debugError atomic.Bool
	setupChatBrowserAcceptance(t, env, dist, &nonadmin, &debugError)
	page := launchChatAcceptanceBrowser(t, bin, env.URL+"/#/chat/"+sid)
	page.MustElement(`[role="switch"][aria-label="Debug cache diagnostics"]`).MustClick()
	for _, turn := range []string{"A", "B", "C"} {
		page.MustElement(`[data-testid="chat-input"]`).MustInput("turn " + turn).MustType(input.Enter)
		page.MustElementR("span,p", "Browser answer "+turn+"\\.")
		page.MustWait(`() => !document.querySelector('[data-testid="chat-input"]').disabled`)
	}
	const debugButton = `button[title="Turn debug (Memory and Cache)"]`
	cases := 0
	var ids, texts []string
	for _, message := range env.Detail(t, sid).Messages {
		if message.Type == "agent" && strings.HasPrefix(message.Content, "Browser answer") {
			ids = append(ids, message.InvocationID)
			k := env.Knowledge(t, sid, message.InvocationID)["knowledge"].(map[string]any)
			if k["dispatched"] != true || k["capture_status"] != "captured" {
				t.Fatalf("browser Debug toggle did not capture: %#v", k)
			}
			texts = append(texts, k["snapshot"].(map[string]any)["text"].(string))
		}
	}
	if len(ids) != 3 {
		t.Fatalf("saved browser turns=%d", len(ids))
	}
	inspect := func() {
		buttons := page.MustElements(debugButton)
		if len(buttons) != 4 {
			t.Fatalf("debug turns=%d want 4", len(buttons))
		}
		for i := 0; i < 3; i++ {
			buttons[i+1].MustClick()
			panel := page.MustElement(`[data-testid="knowledge-debug-panel"]`)
			if i == 2 {
				panel.MustElementR("p", "No automatic memories injected for this turn\\.")
			} else {
				summary := panel.MustElement("summary")
				summary.MustFocus().MustType(input.Enter)
				pre := panel.MustElement(`[data-testid="injected-memory-text"]`)
				pre.MustVisible()
				if got := pre.MustProperty("textContent").String(); got != texts[i] {
					t.Fatalf("snapshot %d mismatch: %q", i, got)
				}
				summary.MustFocus().MustType(input.Space)
				page.MustWait(`() => !document.querySelector('[data-testid="knowledge-debug-panel"] details').open`)
			}
			if page.MustEval(`() => {const body=document.body.cloneNode(true); body.querySelectorAll('[data-testid="knowledge-debug-panel"]').forEach(e=>e.remove()); return body.textContent.includes('MEMORY_')}`).Bool() {
				t.Fatal("memory text outside Debug subtree")
			}
			buttons[i+1].MustClick()
			cases++
		}
	}
	inspect()
	page.MustElement(`[role="switch"][aria-label="Debug cache diagnostics"]`).MustClick()
	env.Retriever.Set("turn A", nil, nil)
	env.Retriever.Set("turn B", nil, nil)
	env.Reopen(t)
	page.MustReload().MustElementR("span,p", "Browser answer C\\.")
	page.MustWait(`() => document.querySelector('[aria-label="Debug cache diagnostics"]').getAttribute('aria-checked') === 'false'`)
	inspect()
	cases++ // capture off + SQLite/agent restart + source replacement
	for _, dark := range []bool{false, true} {
		page.MustEval(`dark => { document.documentElement.classList.toggle('dark',dark); document.documentElement.setAttribute('data-theme',dark?'dark':'light') }`, dark)
		page.MustElements(debugButton)[1].MustClick()
		panel := page.MustElement(`[data-testid="knowledge-debug-panel"]`)
		panel.MustElement("summary").MustClick()
		pre := panel.MustElement(`[data-testid="injected-memory-text"]`)
		if !pre.MustVisible() {
			t.Fatal("snapshot not visible")
		}
		if !pre.MustEval(`function() { const s=getComputedStyle(this); return s.whiteSpace==='pre-wrap' && s.color!==getComputedStyle(this.closest('details')).backgroundColor }`).Bool() {
			t.Fatal("snapshot contrast/whitespace styles")
		}
		page.MustElements(debugButton)[1].MustClick()
		cases++
	}
	debugError.Store(true)
	page.MustElements(debugButton)[1].MustClick()
	page.MustElementR(`[role="alert"]`, "Controlled debug failure")
	page.MustElements(debugButton)[1].MustClick()
	debugError.Store(false)
	cases++
	nonadmin.Store(true)
	page.MustReload().MustElementR("span,p", "Browser answer C\\.")
	if len(page.MustElements(debugButton)) != 0 || len(page.MustElements(`[aria-label="Debug cache diagnostics"]`)) != 0 {
		t.Fatal("nonadmin debug controls visible")
	}
	cases++
	requests := env.LLM.Requests(t)
	if len(requests) != 5 {
		t.Fatalf("provider requests=%d want 5", len(requests))
	}
	for i, req := range requests {
		data, _ := json.Marshal(req.Contents)
		a, b := 0, 0
		if i == 1 || i == 2 {
			a = 1
		}
		if i == 3 {
			b = 1
		}
		if strings.Count(string(data), "MEMORY_A") != a || strings.Count(string(data), "MEMORY_B") != b {
			t.Fatalf("browser request %d memory replay", i)
		}
		if !reflect.DeepEqual(req.Config.SystemInstruction, requests[0].Config.SystemInstruction) || !reflect.DeepEqual(req.Config.Tools, requests[0].Config.Tools) {
			t.Fatal("browser changed core/tools")
		}
	}
	cases++
	if cases != 12 {
		t.Fatalf("browser cases=%d want 12", cases)
	}
	t.Logf("executed %d/12 browser cases: live A/tool/B/empty, restored A/B/empty, capture-off store/agent reopen, light/dark, HTTP error, nonadmin exclusion, provider isolation; browser=%s; SPA=%s; scripted provider, no live model", cases, bin, dist)
}

// chronologyModel pauses at provider boundaries so browser assertions precede completion.
type chronologyModel struct {
	calls atomic.Int32
	gates [3]chan struct{}
}

func (m *chronologyModel) GenerateContent(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		n := int(m.calls.Add(1)) - 1
		if n == 0 {
			yield(turnToResponse(TextTurn("Chronology baseline.")), nil)
			return
		}
		if n > 3 {
			yield(turnToResponse(TextTurn("Unexpected extra generation.")), nil)
			return
		}
		text := []string{"**Chronology A**\n\n- first item\n- second item\n\n" + strings.Repeat("Full intermediate content. ", 20), "**Chronology B**\n\n```go\nfmt.Println(42)\n```", "Chronology final C."}[n-1]
		if !yield(&model.LLMResponse{Content: genai.NewContentFromText(text, "model"), Partial: true}, nil) {
			return
		}
		streaming.Report(ctx, streaming.Progress{Phase: streaming.PhaseToolArguments})
		select {
		case <-m.gates[n-1]:
		case <-ctx.Done():
			return
		case <-time.After(45 * time.Second):
			yield(nil, fmt.Errorf("chronology stage %d was not released", n))
			return
		}
		parts := []*genai.Part{{Text: text}}
		if n < 3 {
			parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("chronology-%d", n), Name: fmt.Sprintf("chronology_tool_%d", n), Args: map[string]any{}}})
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: parts}, TurnComplete: n == 3}, nil)
	}
}

func TestChatChronologyHTTPAcceptance_Baseline(t *testing.T) {
	dist, bin := chatBrowserPrerequisites(t)
	var executions atomic.Int32
	echo, err := functiontool.New(functiontool.Config{Name: "chronology_echo", Description: "Local counted echo"}, func(_ tool.Context, _ mockToolArgs) (mockToolResult, error) {
		executions.Add(1)
		return mockToolResult{Output: map[string]any{"value": "baseline-result"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(TextTurn("Chronology baseline."), TextTurn("Ordinary text answer."), ToolCallTurn("chronology_echo", map[string]any{}), TextTurn("Ordinary tool answer.")), []tool.Tool{echo})
	sid := toString(assertHasEvent(t, env.Turn(t, "", "bootstrap", false), "session").Data["sessionId"])
	var nonadmin, debugError atomic.Bool
	setupChatBrowserAcceptance(t, env, dist, &nonadmin, &debugError)
	page := launchChatAcceptanceBrowser(t, bin, env.URL+"/#/chat/"+sid)
	for _, text := range []string{"Ordinary text answer.", "Ordinary tool answer."} {
		page.MustElement(`[data-testid="chat-input"]`).MustInput("next").MustType(input.Enter)
		page.MustElementR("p", text)
		page.MustWait(`() => !document.querySelector('[data-testid="chat-input"]').disabled`)
	}
	inspect := func() {
		block := page.MustElement(`[data-testid="tool-activity-block"]`)
		block.MustElement("button").MustClick()
		page.MustElementR(`[data-testid="tool-activity-block"] button.w-full`, "chronology_echo").MustClick()
		page.MustElementR(`[data-testid="tool-activity-block"] pre`, "baseline-result")
	}
	inspect()
	if executions.Load() != 1 {
		t.Fatalf("tool executions=%d", executions.Load())
	}
	env.Reopen(t)
	page.MustReload().MustElementR("p", "Ordinary tool answer")
	inspect()
	t.Log("baseline: ordinary text, counted tool, args/result disclosure and SQLite/browser reload passed")
}

func TestChatChronologyHTTPAcceptance_Browser(t *testing.T) {
	dist, bin := chatBrowserPrerequisites(t)
	m := &chronologyModel{gates: [3]chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}}
	env := setupMemoryHTTPAcceptance(t, &MockLLM{funcLLM: m}, []tool.Tool{newMockTool("chronology_tool_1", "First local tool", map[string]any{"ok": true}), newMockTool("chronology_tool_2", "Second local tool", map[string]any{"ok": true})})
	sid := toString(assertHasEvent(t, env.Turn(t, "", "bootstrap", false), "session").Data["sessionId"])
	var nonadmin, debugError atomic.Bool
	setupChatBrowserAcceptance(t, env, dist, &nonadmin, &debugError)
	page := launchChatAcceptanceBrowser(t, bin, env.URL+"/#/chat/"+sid)
	page.MustElement(`[data-testid="chat-input"]`).MustInput("chronological work").MustType(input.Enter)
	page.MustElementR("strong", "Chronology A")
	close(m.gates[0])
	page.MustElementR("strong", "Chronology B")
	block := page.MustElement(`[data-testid="tool-activity-block"]`)
	block.MustElement("button").MustFocus().MustType(input.Enter)
	page.MustElementR(`[data-testid="tool-activity-block"] strong`, "Chronology A")
	if !block.MustEval(`function(){return this.textContent.includes('Full intermediate content. '.repeat(20).trim())}`).Bool() {
		t.Fatal("intermediate text truncated")
	}
	close(m.gates[1])
	page.MustElementR("p", "Chronology final C")
	block.MustElementR("strong", "Chronology B")
	// Reload while the runner is still active: replay must not duplicate content.
	page.MustReload().MustElementR("p", "Chronology final C")
	close(m.gates[2])
	page.MustWait(`() => !document.querySelector('[data-testid="chat-input"]').disabled`)
	inspect := func() {
		block := page.MustElement(`[data-testid="tool-activity-block"]`)
		if block.MustElement("button").MustAttribute("aria-expanded") == nil {
			t.Fatal("missing disclosure state")
		}
		if block.MustElement("button").MustProperty("ariaExpanded").String() != "true" {
			block.MustElement("button").MustClick()
		}
		page.MustElementR(`[data-testid="tool-activity-block"] strong`, "Chronology A")
		block.MustElementR("strong", "Chronology B")
		block.MustElement("li")
		block.MustElementR("pre", "fmt.Println")
		if !block.MustEval(`function(){const t=this.textContent;return t.indexOf('Chronology A')<t.indexOf('chronology_tool_1') && t.indexOf('chronology_tool_1')<t.indexOf('Chronology B') && t.indexOf('Chronology B')<t.indexOf('chronology_tool_2') && !t.includes('Chronology final C') && !t.includes('Preparing tool arguments')}`).Bool() {
			t.Fatal("activity order, final placement or status retention")
		}
		if page.MustEval(`() => document.body.textContent.includes('Preparing tool arguments')`).Bool() {
			t.Fatal("completed status retained")
		}
	}
	for _, theme := range []string{"light", "dark"} {
		page.MustEval(`(theme) => { document.documentElement.classList.toggle('dark', theme === 'dark'); document.documentElement.setAttribute('data-theme', theme) }`, theme)
		inspect()
		button := page.MustElement(`[data-testid="tool-activity-block"] > button`)
		button.MustFocus().MustType(input.Space)
		page.MustWait(`() => !document.querySelector('[data-testid="tool-activity-block"] strong')`)
		button.MustType(input.Enter)
		inspect()
	}
	before := env.Detail(t, sid)
	var order []string
	for _, msg := range before.Messages {
		if msg.Type == "agent" || msg.Type == "tool_call" {
			order = append(order, msg.Type+":"+msg.Content)
		}
	}
	if len(order) != 6 {
		t.Fatalf("persisted chronology=%v", order)
	}
	env.Reopen(t)
	page.MustReload().MustElementR("p", "Chronology final C")
	inspect()
	if !reflect.DeepEqual(before.Messages, env.Detail(t, sid).Messages) {
		t.Fatal("reopened history changed")
	}
	if m.calls.Load() != 4 {
		t.Fatalf("provider calls=%d", m.calls.Load())
	}
	t.Log("chronology: gated A/tool/B/tool/C, full Markdown, active reconnect and SQLite/agent reload passed")
}

func TestChatChronologyHTTPAcceptance_EdgeCases(t *testing.T) {
	dist, bin := chatBrowserPrerequisites(t)
	executed := 0
	for _, name := range []string{"tool-only", "parallel-same-name", "tool-failure"} {
		t.Run(name, func(t *testing.T) {
			var count atomic.Int32
			local, err := functiontool.New(functiontool.Config{Name: "edge_tool", Description: "Counted local tool"}, func(_ tool.Context, _ mockToolArgs) (mockToolResult, error) {
				count.Add(1)
				if name == "tool-failure" {
					return mockToolResult{}, fmt.Errorf("Error: EDGE_FAILURE")
				}
				return mockToolResult{Output: map[string]any{"ok": true}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			calls := ToolCallTurn("edge_tool", map[string]any{})
			want := int32(1)
			if name == "parallel-same-name" {
				calls.Parts = append(calls.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: "parallel-second", Name: "edge_tool", Args: map[string]any{}}})
				want = 2
			}
			env := setupMemoryHTTPAcceptance(t, NewMockLLM(TextTurn("Edge baseline."), calls, TextTurn("")), []tool.Tool{local})
			sid := toString(assertHasEvent(t, env.Turn(t, "", "bootstrap", false), "session").Data["sessionId"])
			var nonadmin, debugError atomic.Bool
			setupChatBrowserAcceptance(t, env, dist, &nonadmin, &debugError)
			page := launchChatAcceptanceBrowser(t, bin, env.URL+"/#/chat/"+sid)
			page.MustElement(`[data-testid="chat-input"]`).MustInput("execute edge case").MustType(input.Enter)
			page.MustElement(`[data-testid="tool-activity-block"]`)
			page.MustWait(`() => !document.querySelector('[title="Stop"]')`)
			inspect := func() {
				block := page.MustElement(`[data-testid="tool-activity-block"]`)
				block.MustElement("button").MustClick()
				if got := len(block.MustElements("button.w-full")); got != int(want) {
					t.Fatalf("tool rows=%d want=%d", got, want)
				}
				if len(block.MustElements("[data-message-index]")) != 0 {
					t.Fatal("invented tool-only answer")
				}
				if name == "tool-failure" {
					block.MustElementR("span", "failed")
				}
			}
			inspect()
			if count.Load() != want {
				t.Fatalf("executions=%d want=%d", count.Load(), want)
			}
			env.Reopen(t)
			page.MustReload().MustElementR("p", "Edge baseline")
			inspect()
			executed++
		})
	}
	t.Logf("executed %d browser edge cases through HTTP/SSE and restored SQLite history (3 when unfiltered)", executed)
}

func TestChatChronologyHTTPAcceptance_ActionableBoundary(t *testing.T) {
	dist, bin := chatBrowserPrerequisites(t)
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(TextTurn("Boundary baseline.")), nil)
	sid := toString(assertHasEvent(t, env.Turn(t, "", "bootstrap", false), "session").Data["sessionId"])
	// Isolate presentation of an actionable card using the existing SSE schema;
	// do not execute an approval or change production approval behavior.
	env.Router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/studio/chat" {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range []struct {
				kind string
				data map[string]any
			}{
				{"text", map[string]any{"text": "Before approval."}},
				{"tool_call", map[string]any{"name": "boundary_one", "args": map[string]any{}}},
				{"tool_result", map[string]any{"name": "boundary_one", "result": "ok"}},
				{"approval", map[string]any{"name": "boundary_permission", "args": map[string]any{}, "options": []string{"Allow", "Deny"}}},
				{"text", map[string]any{"text": "After approval."}},
				{"tool_call", map[string]any{"name": "boundary_two", "args": map[string]any{}}},
				{"tool_result", map[string]any{"name": "boundary_two", "result": "ok"}},
				{"text", map[string]any{"text": "Boundary final."}},
				{"done", map[string]any{}},
			} {
				data, _ := json.Marshal(event.data)
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.kind, data)
			}
		})
	})
	var nonadmin, debugError atomic.Bool
	setupChatBrowserAcceptance(t, env, dist, &nonadmin, &debugError)
	page := launchChatAcceptanceBrowser(t, bin, env.URL+"/#/chat/"+sid)
	page.MustElement(`[data-testid="chat-input"]`).MustInput("boundary fixture").MustType(input.Enter)
	page.MustElementR("p", "Boundary final")
	blocks := page.MustElements(`[data-testid="tool-activity-block"]`)
	if len(blocks) != 2 {
		t.Fatalf("approval must split activity: blocks=%d", len(blocks))
	}
	allow := page.MustElementR("button", "^Allow$")
	if allow.MustEval(`function(){return !!this.closest('[data-testid="tool-activity-block"]') || this.disabled}`).Bool() {
		t.Fatal("approval hidden inside activity or disabled")
	}
	for _, block := range blocks {
		block.MustElement("button").MustClick()
	}
	blocks[0].MustElementR("p", "Before approval")
	blocks[1].MustElementR("p", "After approval")
	if !allow.MustEval(`function(){const b=document.querySelectorAll('[data-testid="tool-activity-block"]');return !!(b[0].compareDocumentPosition(this)&Node.DOCUMENT_POSITION_FOLLOWING) && !!(this.compareDocumentPosition(b[1])&Node.DOCUMENT_POSITION_FOLLOWING)}`).Bool() {
		t.Fatal("approval order changed")
	}
	t.Log("executed 1/1 actionable-boundary browser case using scripted HTTP/SSE; approval stays actionable between two folds")
}

func TestMemoryHTTPAcceptance_Baseline(t *testing.T) {
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(
		TextTurn("Baseline hello."),
		ToolCallTurn("acceptance_echo", map[string]any{"input": map[string]any{"value": "hello"}}),
		TextTurn("Baseline tool completed."),
	), []tool.Tool{newMockTool("acceptance_echo", "Deterministic local echo", map[string]any{"value": "hello"})})
	cases := 0
	first := env.Turn(t, "", "baseline text", false)
	assertFirstAndLast(t, first)
	assertNoEvent(t, first, "error")
	assertEventSequence(t, first, "session", "text", "done")
	assertEventData(t, assertHasEvent(t, first, "text"), "text", "Baseline hello.")
	sessionID := toString(assertHasEvent(t, first, "session").Data["sessionId"])
	invocationA := toString(assertHasEvent(t, first, "text").Data["invocationId"])
	if sessionID == "" || invocationA == "" {
		t.Fatalf("missing text identity: %v", first)
	}
	cases++
	second := env.Turn(t, sessionID, "baseline tool", false)
	assertFirstAndLast(t, second)
	assertNoEvent(t, second, "error")
	assertEventSequence(t, second, "session", "tool_call", "tool_result", "text", "done")
	assertEventData(t, assertHasEvent(t, second, "session"), "sessionId", sessionID)
	assertEventData(t, assertHasEvent(t, second, "text"), "text", "Baseline tool completed.")
	invocationB := toString(assertHasEvent(t, second, "text").Data["invocationId"])
	if invocationB == "" || invocationB == invocationA {
		t.Fatalf("distinct turn identity missing: %q / %q", invocationA, invocationB)
	}
	cases++
	detail := env.Detail(t, sessionID)
	var visible []string
	for _, msg := range detail.Messages {
		visible = append(visible, msg.Type+":"+msg.Content)
		if msg.Type == "agent" && msg.InvocationID != invocationA && msg.InvocationID != invocationB {
			t.Fatalf("restored invocation: %+v", msg)
		}
	}
	want := []string{"user:baseline text", "agent:Baseline hello.", "user:baseline tool", "tool_call:", "tool_result:", "agent:Baseline tool completed."}
	if !reflect.DeepEqual(visible, want) {
		t.Fatalf("restored visible messages = %v, want %v", visible, want)
	}
	cases++
	// Completed runners are removed by the real handler. A reconnect now must
	// use restored history rather than inventing an active SSE stream.
	stream := env.Request(t, http.MethodGet, "/api/studio/sessions/"+sessionID+"/stream", nil, memoryIdentityAdmin)
	stream.Body.Close()
	if stream.StatusCode != http.StatusNotFound {
		t.Fatalf("completed stream: %s, want 404", stream.Status)
	}
	status := env.Request(t, http.MethodGet, "/api/studio/sessions/"+sessionID+"/status", nil, memoryIdentityAdmin)
	var state struct {
		Running   bool   `json:"running"`
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(status.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	status.Body.Close()
	if status.StatusCode != http.StatusOK || state.Running || state.SessionID != sessionID {
		t.Fatalf("status after done: %s %+v", status.Status, state)
	}
	cases++
	env.Reopen(t)
	if restored := env.Detail(t, sessionID); !reflect.DeepEqual(detail.Messages, restored.Messages) {
		t.Fatalf("reopened history differs: %+v", restored.Messages)
	}
	cases++
	requests := env.LLM.Requests(t)
	if len(requests) != 3 {
		t.Fatalf("provider calls = %d, want exactly 3 (no auxiliary calls)", len(requests))
	}
	before, _ := json.Marshal(requests[0])
	requests[0].Contents = nil
	after, _ := json.Marshal(env.LLM.Requests(t)[0])
	if !bytes.Equal(before, after) {
		t.Fatal("request snapshots share mutable state")
	}
	env.Retriever.mu.Lock()
	queries := len(env.Retriever.queries)
	env.Retriever.mu.Unlock()
	if queries != 2 {
		t.Fatalf("retrieval preparations = %d, want 2", queries)
	}
	cases++
	for _, check := range []struct {
		name     string
		path     string
		identity memoryAcceptanceIdentity
		want     int
	}{
		{"studio debug admin", "/api/studio/sessions/" + sessionID + "/knowledge-debug?invocationId=" + invocationA, memoryIdentityAdmin, http.StatusOK},
		{"platform debug admin", "/api/platform/admin/sessions/" + sessionID + "/knowledge-debug?org=" + env.OrgSlug + "&scope=personal&user=" + env.UserID + "&invocationId=" + invocationA, memoryIdentityAdmin, http.StatusOK},
		{"studio debug nonadmin", "/api/studio/sessions/" + sessionID + "/knowledge-debug?invocationId=" + invocationA, memoryIdentityNonadmin, http.StatusForbidden},
		{"studio debug denied", "/api/studio/sessions/" + sessionID + "/knowledge-debug?invocationId=" + invocationA, memoryIdentityDenied, http.StatusForbidden},
		{"foreign history", "/api/studio/sessions/" + sessionID, memoryIdentityForeign, http.StatusNotFound},
	} {
		resp := env.Request(t, http.MethodGet, check.path, nil, check.identity)
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != check.want {
			t.Fatalf("%s: status=%s body=%s error=%v", check.name, resp.Status, data, err)
		}
	}
	cases++
	if cases != 7 {
		t.Fatalf("executed %d baseline cases, want 7", cases)
	}
	t.Logf("executed %d/7 baseline cases: text, tool, persisted history/identities, completed-stream/status, SQLite reopen, frozen requests/retrieval, authorized/denied/scoped HTTP; no skipped prerequisites", cases)
}

func TestChildDiscoveryHTTPAcceptance(t *testing.T) {
	var executed atomic.Int32
	local, err := functiontool.New(functiontool.Config{Name: "child_http_echo", Description: "Allowed local wrapped echo"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
		executed.Add(1)
		return map[string]any{"evidence": "CHILD_EXECUTED", "value": args["value"]}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := tools.NewDelegateTasksTool()
	if err != nil {
		t.Fatal(err)
	}
	parent := NewMockLLM(ToolCallTurn("delegate_tasks", map[string]any{"tasks": []any{map[string]any{"name": "child", "task": "Discover and execute local echo", "tools": []any{"child_http_echo"}}}}), TextTurn("Delegated evidence received."))
	env := setupMemoryHTTPAcceptance(t, parent, []tool.Tool{delegate})
	child := NewMockLLM(ToolCallTurn("search_tools", map[string]any{"query": "*"}), ToolCallTurn("describe_tools", map[string]any{"names": []any{"child_http_echo"}}), ToolCallTurn("execute_tool", map[string]any{"name": "child_http_echo", "arguments": map[string]any{"value": "ok"}}), TextTurn("CHILD_EXECUTED"))
	mgr := agent.NewSubAgentManager(agent.SubAgentConfig{MaxRetries: 1})
	childCapture := &memoryCapturingLLM{Script: child}
	mgr.LLM = childCapture
	mgr.AppName = studioChatAppName
	mgr.UserID = env.UserID
	mgr.SessionService = env.Services.PersonalSessions
	mgr.ToolGroups = map[string]*agent.ToolGroup{"core": {Name: "core", Tools: []tool.Tool{local, newMockTool("forbidden_child_tool", "must not leak", nil)}}}
	mgr.SearchToolsFactory = tools.NewScopedSearchToolsTool
	previous := tools.GetSubAgentManager()
	tools.SetSubAgentManager(mgr)
	t.Cleanup(func() { tools.SetSubAgentManager(previous) })
	events := env.Turn(t, "", "Delegate the local check", false)
	assertNoEvent(t, events, "error")
	assertHasEvent(t, events, "done")
	if executed.Load() != 1 {
		t.Fatalf("child executions=%d events=%v", executed.Load(), events)
	}
	requests := childCapture.Requests(t)
	if len(requests) != 4 {
		t.Fatalf("child requests=%d", len(requests))
	}
	for i, req := range requests {
		declarations, _ := json.Marshal(req.Config.Tools)
		if strings.Contains(string(declarations), "forbidden_child_tool") {
			t.Fatal("forbidden declaration exposed")
		}
		if !reflect.DeepEqual(req.Config.Tools, requests[0].Config.Tools) {
			t.Fatal("declarations changed after discovery")
		}
		seen := map[string]bool{}
		for _, declared := range req.Config.Tools {
			for _, fn := range declared.FunctionDeclarations {
				if seen[fn.Name] {
					t.Fatalf("duplicate declaration %s", fn.Name)
				}
				seen[fn.Name] = true
			}
		}
		if i > 0 {
			data, _ := json.Marshal(req.Contents)
			if !strings.Contains(string(data), "child_http_echo") || strings.Contains(string(data), "forbidden_child_tool") {
				t.Fatal("discovery missing or outside-scope evidence exposed")
			}
		}
	}
	for _, expected := range []struct {
		index          int
		name, evidence string
	}{{1, "search_tools", "child_http_echo"}, {2, "describe_tools", "Allowed local wrapped echo"}, {3, "execute_tool", "CHILD_EXECUTED"}} {
		found := false
		for _, content := range requests[expected.index].Contents {
			for _, part := range content.Parts {
				if part.FunctionResponse != nil && part.FunctionResponse.Name == expected.name {
					data, _ := json.Marshal(part.FunctionResponse.Response)
					found = strings.Contains(string(data), expected.evidence)
				}
			}
		}
		if !found {
			t.Fatalf("missing %s result evidence", expected.name)
		}
	}
	parentRequests := env.LLM.Requests(t)
	if len(parentRequests) != 2 {
		t.Fatalf("parent calls=%d", len(parentRequests))
	}
	parentEvidence, _ := json.Marshal(parentRequests[1].Contents)
	if !strings.Contains(string(parentEvidence), "CHILD_EXECUTED") {
		t.Fatal("delegate result missing at parent")
	}
	last, _ := json.Marshal(requests[3].Contents)
	if !strings.Contains(string(last), "CHILD_EXECUTED") {
		t.Fatalf("execution evidence not returned: %s", last)
	}
	t.Log("HTTP POST/SSE -> actual delegate_tasks -> isolated child search/describe/execute -> counted wrapped tool -> parent result; fixed declarations, scoped discovery, no paid model")
}
