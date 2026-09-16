package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/a2aserver"
	"github.com/SAP/astonish/pkg/channels"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

type a2aTestValidator struct {
	principal execution.Principal
	err       error
	calls     int
}

func (v *a2aTestValidator) ValidateBearer(_ context.Context, _ string, _ string, scopes []string, surface execution.Surface) (execution.Principal, error) {
	v.calls++
	if len(scopes) != 1 || scopes[0] != a2aScope || surface != execution.SurfaceA2A {
		return execution.Principal{}, context.Canceled
	}
	return v.principal, v.err
}

type a2aTestDispatcher struct{}

func (a2aTestDispatcher) dispatch(ctx context.Context, _ channels.InboundMessage, reply func(context.Context, channels.OutboundMessage) error) error {
	return reply(ctx, channels.OutboundMessage{Text: "done"})
}

type a2aStreamDispatcher struct{}

type a2aDuplicateStreamDispatcher struct{}

func (a2aDuplicateStreamDispatcher) dispatch(ctx context.Context, _ channels.InboundMessage, reply func(context.Context, channels.OutboundMessage) error) error {
	if sink := channels.ProgressSinkFromContext(ctx); sink != nil {
		sink.Progress("final")
	}
	return reply(ctx, channels.OutboundMessage{Text: "final"})
}

type a2aTestMCPStore struct {
	servers []store.MCPServer
}

func (s *a2aTestMCPStore) List(context.Context) ([]store.MCPServer, error) { return s.servers, nil }
func (s *a2aTestMCPStore) Get(context.Context, string) (*store.MCPServer, error) {
	return nil, context.Canceled
}
func (s *a2aTestMCPStore) Save(context.Context, *store.MCPServer) error { return nil }
func (s *a2aTestMCPStore) Delete(context.Context, string) error         { return nil }
func (s *a2aTestMCPStore) UpdateCachedTools(context.Context, string, json.RawMessage) error {
	return nil
}

type a2aTestAgentStore struct {
	agents []store.A2AAgent
}

func (s *a2aTestAgentStore) List(context.Context) ([]store.A2AAgent, error) { return s.agents, nil }
func (s *a2aTestAgentStore) Get(context.Context, string) (*store.A2AAgent, error) {
	return nil, context.Canceled
}
func (s *a2aTestAgentStore) Save(context.Context, *store.A2AAgent) error { return nil }
func (s *a2aTestAgentStore) Delete(context.Context, string) error        { return nil }
func (s *a2aTestAgentStore) UpdateCachedCard(context.Context, string, json.RawMessage, json.RawMessage) error {
	return nil
}

func boolPtr(value bool) *bool { return &value }

func (a2aStreamDispatcher) dispatch(ctx context.Context, _ channels.InboundMessage, reply func(context.Context, channels.OutboundMessage) error) error {
	if sink := channels.ProgressSinkFromContext(ctx); sink != nil {
		sink.Progress("activity")
	}
	return reply(ctx, channels.OutboundMessage{Text: "final"})
}

func setupA2ARouter(t *testing.T, principal execution.Principal, err error) *mux.Router {
	t.Helper()
	store := a2a.NewInMemoryTaskStore(time.Hour)
	t.Cleanup(store.Close)
	service, newErr := a2aserver.New(a2aserver.Config{TaskStore: store, BaseURL: "http://example.test", Dispatcher: a2aTestDispatcher{}.dispatch})
	if newErr != nil {
		t.Fatal(newErr)
	}
	SetA2AService(service)
	t.Cleanup(func() { SetA2AService(nil) })
	router := mux.NewRouter()
	RegisterA2ARoutes(router, &a2aTestValidator{principal: principal, err: err}, nil, nil)
	return router
}
func oauthA2APrincipal(scopes ...string) execution.Principal {
	return execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceA2A, Subject: "user", OrgSlug: "org", TeamSlug: "team", Scopes: scopes, Authenticated: true}
}
func a2aRequest(t *testing.T) []byte {
	t.Helper()
	params, _ := json.Marshal(a2a.SendMessageParams{Message: a2a.Message{Role: "user", Parts: []a2a.Part{a2a.TextPart{Text: "hello"}}}})
	body, err := json.Marshal(a2a.JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "message/send", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestA2AHandlerAcceptsV1SendMessage(t *testing.T) {
	router := setupA2ARouter(t, oauthA2APrincipal(a2aScope), nil)
	body := []byte(`{"jsonrpc":"2.0","id":"send","method":"SendMessage","params":{"message":{"role":"ROLE_USER","messageId":"message-1","parts":[{"text":"Hello, what can you do?"}]}}}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	router.ServeHTTP(w, req)

	var response struct {
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
		Result  struct {
			Task struct {
				Kind   string `json:"kind"`
				Status struct {
					State   string `json:"state"`
					Message *struct {
						Kind      string `json:"kind"`
						Role      string `json:"role"`
						MessageID string `json:"messageId"`
						Parts     []struct {
							Kind string `json:"kind"`
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"message,omitempty"`
				} `json:"status"`
				History []struct {
					Kind  string `json:"kind"`
					Role  string `json:"role"`
					Parts []struct {
						Kind string `json:"kind"`
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"history"`
				Artifacts []struct {
					ArtifactID string `json:"artifactId"`
					Parts      []struct {
						Kind string `json:"kind"`
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"artifacts"`
				AgentID string `json:"agentId"`
				Created string `json:"createdAt"`
				Updated string `json:"updatedAt"`
			} `json:"task"`
		} `json:"result"`
		Error *a2a.JSONRPCError `json:"error,omitempty"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("SendMessage returned JSON-RPC error: %+v", response.Error)
	}
	task := response.Result.Task
	if task.Kind != "task" {
		t.Fatalf("result task kind = %q, want task: %s", task.Kind, w.Body.String())
	}
	if task.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("status state = %q", task.Status.State)
	}
	if task.Status.Message != nil {
		t.Fatalf("v1 completed status repeated the response: %#v", task.Status.Message)
	}
	if len(task.History) != 1 || len(task.History[0].Parts) == 0 || task.History[0].Parts[0].Text != "Hello, what can you do?" {
		t.Fatalf("v1 request history was not normalized: %#v", task.History)
	}
	if task.History[0].Kind != "message" || task.History[0].Role != "ROLE_USER" {
		t.Fatalf("invalid v1 history message: %#v", task.History[0])
	}
	if len(task.Artifacts) != 1 || task.Artifacts[0].ArtifactID == "" || len(task.Artifacts[0].Parts) == 0 || task.Artifacts[0].Parts[0].Kind != "text" || task.Artifacts[0].Parts[0].Text != "done" {
		t.Fatalf("invalid v1 response artifact: %#v", task.Artifacts)
	}
	if got := strings.Count(w.Body.String(), `"text":"done"`); got != 1 {
		t.Fatalf("v1 response text appears %d times, want once: %s", got, w.Body.String())
	}
	if task.AgentID != "" || task.Created != "" || task.Updated != "" {
		t.Fatalf("internal task fields leaked in v1 response: %s", w.Body.String())
	}
}

func TestSSEHeartbeatWritesCommentFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var frames []string
	stop := startSSEHeartbeat(ctx, time.Millisecond, func(frame string) {
		mu.Lock()
		defer mu.Unlock()
		frames = append(frames, frame)
	})
	t.Cleanup(stop)

	time.Sleep(10 * time.Millisecond)
	stop()
	mu.Lock()
	defer mu.Unlock()
	if len(frames) == 0 {
		t.Fatal("heartbeat emitted no frames")
	}
	for _, frame := range frames {
		if frame != ": keepalive\n\n" {
			t.Fatalf("heartbeat frame = %q", frame)
		}
	}
}

func TestA2AHandlerStreamsMessageStream(t *testing.T) {
	store := a2a.NewInMemoryTaskStore(time.Hour)
	t.Cleanup(store.Close)
	service, err := a2aserver.New(a2aserver.Config{TaskStore: store, BaseURL: "http://example.test", Dispatcher: a2aStreamDispatcher{}.dispatch})
	if err != nil {
		t.Fatal(err)
	}
	SetA2AService(service)
	t.Cleanup(func() { SetA2AService(nil) })
	router := mux.NewRouter()
	RegisterA2ARoutes(router, &a2aTestValidator{principal: oauthA2APrincipal(a2aScope)}, nil, nil)

	params, err := json.Marshal(a2a.SendMessageParams{Message: a2a.Message{Role: "user", Parts: []a2a.Part{a2a.TextPart{Text: "hello"}}}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(a2a.JSONRPCRequest{JSONRPC: "2.0", ID: "stream", Method: "message/stream", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Accept", "text/event-stream")
	router.ServeHTTP(w, req)

	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	frames := strings.Split(strings.TrimSpace(w.Body.String()), "\n\n")
	if len(frames) != 4 {
		t.Fatalf("got %d SSE frames, want 4: %s", len(frames), w.Body.String())
	}
	// Every frame's JSON-RPC result must be a one-key A2A event union
	// (statusUpdate / task / message / artifactUpdate) so the official
	// a2a-go/v2 client can decode it. A bare status/task object is dropped.
	decodeFrame := func(frame string) map[string]json.RawMessage {
		t.Helper()
		payload := strings.TrimPrefix(strings.TrimSpace(frame), "data: ")
		var envelope struct {
			Result map[string]json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
			t.Fatalf("frame is not JSON-RPC: %s (%v)", frame, err)
		}
		if len(envelope.Result) != 1 {
			t.Fatalf("frame result is not a single-key event union: %s", frame)
		}
		return envelope.Result
	}
	firstKeys := decodeFrame(frames[0])
	if _, ok := firstKeys["statusUpdate"]; !ok {
		t.Fatalf("first frame is not a statusUpdate event: %s", frames[0])
	}
	if !strings.Contains(frames[0], `"state":"TASK_STATE_WORKING"`) || strings.Contains(frames[0], `"message"`) {
		t.Fatalf("first frame is not content-free working status: %s", frames[0])
	}
	if _, ok := decodeFrame(frames[1])["statusUpdate"]; !ok {
		t.Fatalf("second frame is not a statusUpdate event: %s", frames[1])
	}
	if !strings.Contains(frames[1], `"activity"`) || !strings.Contains(frames[1], `"state":"TASK_STATE_WORKING"`) {
		t.Fatalf("second frame is not working activity: %s", frames[1])
	}
	if _, ok := decodeFrame(frames[2])["artifactUpdate"]; !ok {
		t.Fatalf("third frame is not an artifactUpdate event: %s", frames[2])
	}
	if !strings.Contains(frames[2], `"name":"response"`) || !strings.Contains(frames[2], `"text":"final"`) || !strings.Contains(frames[2], `"lastChunk":true`) {
		t.Fatalf("third frame does not contain the final response artifact: %s", frames[2])
	}
	lastKeys := decodeFrame(frames[3])
	if _, ok := lastKeys["statusUpdate"]; !ok {
		t.Fatalf("final frame is not a statusUpdate event: %s", frames[3])
	}
	if !strings.Contains(frames[3], `"state":"TASK_STATE_COMPLETED"`) || !strings.Contains(frames[3], `"final":true`) || strings.Contains(frames[3], `"message"`) {
		t.Fatalf("final frame is not content-free terminal status: %s", frames[3])
	}
	if got := strings.Count(w.Body.String(), `"text":"final"`); got != 1 {
		t.Fatalf("final answer appears %d times, want once: %s", got, w.Body.String())
	}
	for _, frame := range frames {
		if _, ok := decodeFrame(frame)["task"]; ok {
			t.Fatalf("stream contains redundant full task snapshot: %s", frame)
		}
	}
}

func TestA2AHandlerStreamForwardsProgressMatchingFinalArtifact(t *testing.T) {
	store := a2a.NewInMemoryTaskStore(time.Hour)
	t.Cleanup(store.Close)
	service, err := a2aserver.New(a2aserver.Config{TaskStore: store, BaseURL: "http://example.test", Dispatcher: a2aDuplicateStreamDispatcher{}.dispatch})
	if err != nil {
		t.Fatal(err)
	}
	SetA2AService(service)
	t.Cleanup(func() { SetA2AService(nil) })
	router := mux.NewRouter()
	RegisterA2ARoutes(router, &a2aTestValidator{principal: oauthA2APrincipal(a2aScope)}, nil, nil)

	body := []byte(`{"jsonrpc":"2.0","id":"dedupe","method":"SendStreamingMessage","params":{"message":{"role":"ROLE_USER","messageId":"message-1","parts":[{"kind":"text","text":"hello"}]}}}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Accept", "text/event-stream")
	router.ServeHTTP(w, req)

	frames := strings.Split(strings.TrimSpace(w.Body.String()), "\n\n")
	if len(frames) != 4 {
		t.Fatalf("got %d SSE frames, want working/progress/artifact/completed: %s", len(frames), w.Body.String())
	}
	if got := strings.Count(w.Body.String(), `"text":"final"`); got != 2 {
		t.Fatalf("separately emitted progress and artifact texts appear %d times, want twice: %s", got, w.Body.String())
	}
	if strings.Contains(frames[0], `"message"`) || !strings.Contains(frames[1], `"statusUpdate"`) || !strings.Contains(frames[2], `"artifactUpdate"`) || !strings.Contains(frames[3], `"final":true`) {
		t.Fatalf("unexpected unfiltered stream sequence: %s", w.Body.String())
	}
}

func TestA2AHandlerAcceptsV1SendStreamingMessage(t *testing.T) {
	store := a2a.NewInMemoryTaskStore(time.Hour)
	t.Cleanup(store.Close)
	service, err := a2aserver.New(a2aserver.Config{TaskStore: store, BaseURL: "http://example.test", Dispatcher: a2aStreamDispatcher{}.dispatch})
	if err != nil {
		t.Fatal(err)
	}
	SetA2AService(service)
	t.Cleanup(func() { SetA2AService(nil) })
	router := mux.NewRouter()
	RegisterA2ARoutes(router, &a2aTestValidator{principal: oauthA2APrincipal(a2aScope)}, nil, nil)

	body := []byte(`{"jsonrpc":"2.0","id":"stream-v1","method":"SendStreamingMessage","params":{"message":{"role":"ROLE_USER","messageId":"message-1","parts":[{"kind":"text","text":"hello"}]}}}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Accept", "text/event-stream")
	router.ServeHTTP(w, req)

	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream; body: %s", got, w.Body.String())
	}
	frames := strings.Split(strings.TrimSpace(w.Body.String()), "\n\n")
	if len(frames) != 4 {
		t.Fatalf("got %d SSE frames, want 4: %s", len(frames), w.Body.String())
	}
	if !strings.Contains(frames[0], `"statusUpdate"`) || strings.Contains(frames[0], `"text":"hello"`) {
		t.Fatalf("first v1 stream frame is not content-free working status: %s", frames[0])
	}
	if !strings.Contains(frames[2], `"artifactUpdate"`) || !strings.Contains(frames[2], `"text":"final"`) {
		t.Fatalf("v1 stream artifact frame did not contain the result: %s", frames[2])
	}
	if !strings.Contains(frames[3], `"statusUpdate"`) || !strings.Contains(frames[3], `"final":true`) || strings.Contains(frames[3], `"text":"final"`) {
		t.Fatalf("final v1 stream frame is not content-free terminal status: %s", frames[3])
	}
	if got := strings.Count(w.Body.String(), `"text":"final"`); got != 1 {
		t.Fatalf("v1 final answer appears %d times, want once: %s", got, w.Body.String())
	}
}

func TestA2AAgentCardHandlerIsPublic(t *testing.T) {
	router := setupA2ARouter(t, oauthA2APrincipal(a2aScope), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var card a2a.AgentCard
	if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if !card.Capabilities.SupportsAuthenticatedExtendedCard {
		t.Fatal("public card does not advertise authenticated extended card")
	}
	if len(card.Skills) != 0 {
		t.Fatalf("public card leaked tenant skills: %#v", card.Skills)
	}
}

func TestA2AHandlerAuthenticatedExtendedCard(t *testing.T) {
	serviceStore := a2a.NewInMemoryTaskStore(time.Hour)
	t.Cleanup(serviceStore.Close)
	service, err := a2aserver.New(a2aserver.Config{TaskStore: serviceStore, BaseURL: "http://example.test", Dispatcher: a2aTestDispatcher{}.dispatch})
	if err != nil {
		t.Fatal(err)
	}
	SetA2AService(service)
	t.Cleanup(func() { SetA2AService(nil) })

	cachedTools := json.RawMessage(`[{"name":"send_email","description":"Sends email"},{"name":"search_mail","description":"Searches mail"}]`)
	cachedSkills := json.RawMessage(`[{"id":"inventory","name":"Inventory","description":"Lists devices","examples":["List devices per site."]}]`)
	services := &store.Services{
		Mode:           store.ModePlatform,
		TeamMCPServers: &a2aTestMCPStore{servers: []store.MCPServer{{Name: "email", Enabled: boolPtr(true), CachedTools: cachedTools}}},
		TeamA2AAgents: &a2aTestAgentStore{agents: []store.A2AAgent{
			{Name: "Inventory Agent", Enabled: boolPtr(true), CachedSkills: cachedSkills},
			{Name: "Disabled Agent", Enabled: boolPtr(false), CachedSkills: cachedSkills},
		}},
	}
	principal, err := execution.WithPrincipal(context.Background(), oauthA2APrincipal(a2aScope))
	if err != nil {
		t.Fatal(err)
	}
	principal = store.WithServices(principal, services)
	body := []byte(`{"jsonrpc":"2.0","id":"card","method":"agent/getAuthenticatedExtendedCard","params":{}}`)
	w := httptest.NewRecorder()
	A2AHandler(w, httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(body)).WithContext(principal))

	var response struct {
		Result a2a.AgentCard     `json:"result"`
		Error  *a2a.JSONRPCError `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("extended card error: %#v", response.Error)
	}
	ids := make(map[string]bool)
	for _, skill := range response.Result.Skills {
		ids[skill.ID] = true
	}
	if !ids["mcp-email"] || !ids["a2a-inventory-agent"] {
		t.Fatalf("extended card skill ids = %#v", ids)
	}
	if ids["a2a-disabled-agent"] {
		t.Fatalf("disabled agent was advertised: %#v", ids)
	}
	if !response.Result.Capabilities.SupportsAuthenticatedExtendedCard {
		t.Fatal("extended card flag is false")
	}
	if strings.Contains(w.Body.String(), "CREDENTIAL") || strings.Contains(w.Body.String(), `"command"`) {
		t.Fatalf("extended card leaked implementation data: %s", w.Body.String())
	}
}
func TestA2AHandlerAuthenticatedExtendedCardDiffersPerTenant(t *testing.T) {
	serviceStore := a2a.NewInMemoryTaskStore(time.Hour)
	t.Cleanup(serviceStore.Close)
	service, err := a2aserver.New(a2aserver.Config{TaskStore: serviceStore, BaseURL: "http://example.test", Dispatcher: a2aTestDispatcher{}.dispatch})
	if err != nil {
		t.Fatal(err)
	}
	SetA2AService(service)
	t.Cleanup(func() { SetA2AService(nil) })

	fetchIDs := func(serverName, toolName string) map[string]bool {
		t.Helper()
		cachedTools, err := json.Marshal([]map[string]string{{"name": toolName, "description": "Tenant tool"}})
		if err != nil {
			t.Fatal(err)
		}
		services := &store.Services{
			Mode: store.ModePlatform,
			TeamMCPServers: &a2aTestMCPStore{servers: []store.MCPServer{
				{Name: serverName, Enabled: boolPtr(true), CachedTools: cachedTools},
			}},
		}
		ctx, err := execution.WithPrincipal(context.Background(), oauthA2APrincipal(a2aScope))
		if err != nil {
			t.Fatal(err)
		}
		ctx = store.WithServices(ctx, services)
		body := []byte(`{"jsonrpc":"2.0","id":"card","method":"agent/getAuthenticatedExtendedCard","params":{}}`)
		w := httptest.NewRecorder()
		A2AHandler(w, httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(body)).WithContext(ctx))
		var response struct {
			Result a2a.AgentCard `json:"result"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		ids := make(map[string]bool)
		for _, skill := range response.Result.Skills {
			ids[skill.ID] = true
		}
		return ids
	}

	teamA := fetchIDs("email", "send_email")
	teamB := fetchIDs("inventory", "list_devices")
	if !teamA["mcp-email"] || teamA["mcp-inventory"] {
		t.Fatalf("team A skills = %#v", teamA)
	}
	if !teamB["mcp-inventory"] || teamB["mcp-email"] {
		t.Fatalf("team B skills = %#v", teamB)
	}
}

func TestA2AHandlerUnknownMethod(t *testing.T) {
	router := setupA2ARouter(t, oauthA2APrincipal(a2aScope), nil)
	body := []byte(`{"jsonrpc":"2.0","id":"unknown","method":"agent/notReal","params":{}}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	router.ServeHTTP(w, req)
	var response a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != a2a.ErrCodeMethodNotFound {
		t.Fatalf("unknown method response = %s", w.Body.String())
	}
}

func TestA2AHandlerRequiresA2AScope(t *testing.T) {
	router := setupA2ARouter(t, oauthA2APrincipal(string(execution.CapabilityChat)), nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(a2aRequest(t)))
	req.Header.Set("Authorization", "Bearer token")
	router.ServeHTTP(w, req)
	var response a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Error == nil || response.Error.Code != a2a.ErrCodeForbidden {
		t.Fatalf("expected insufficient-scope JSON-RPC error, got status=%d body=%s", w.Code, w.Body.String())
	}
}
func TestA2AHandlerAllowsExactA2AScope(t *testing.T) {
	router := setupA2ARouter(t, oauthA2APrincipal(a2aScope), nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(a2aRequest(t)))
	req.Header.Set("Authorization", "Bearer token")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
}
func TestA2AHandlerRejectsInvalidBearerBeforeDispatch(t *testing.T) {
	router := setupA2ARouter(t, oauthA2APrincipal(a2aScope), context.Canceled)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(a2aRequest(t)))
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", w.Code)
	}
}

func TestA2APushConfigurationRejectsSameAgentAcrossTenants(t *testing.T) {
	store := a2a.NewInMemoryTaskStore(time.Hour)
	t.Cleanup(store.Close)
	service, err := a2aserver.New(a2aserver.Config{TaskStore: store, Dispatcher: a2aTestDispatcher{}.dispatch})
	if err != nil {
		t.Fatal(err)
	}
	SetA2AService(service)
	t.Cleanup(func() { SetA2AService(nil) })
	task, err := service.SendMessage(context.Background(), a2aserver.Identity{AgentID: "same-agent", OrgID: "org-a", TeamID: "team-a"}, a2a.SendMessageParams{Message: a2a.Message{Role: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceA2A, Subject: "same-agent", OrgSlug: "org-b", TeamSlug: "team-b", Scopes: []string{a2aScope}, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(a2a.SetPushNotificationParams{TaskID: task.ID, Config: a2a.PushNotificationConfig{URL: "https://example.com/hook"}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(a2a.JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "pushNotification/set", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	A2AHandler(w, httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(body)).WithContext(principal))
	var response a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Error == nil || response.Error.Code != a2a.ErrCodeTaskNotFound {
		t.Fatalf("expected cross-tenant task-not-found, got %s", w.Body.String())
	}
}

func TestA2APushConfigurationRejectsCrossPrincipalAccess(t *testing.T) {
	store := a2a.NewInMemoryTaskStore(time.Hour)
	t.Cleanup(store.Close)
	service, err := a2aserver.New(a2aserver.Config{TaskStore: store, Dispatcher: a2aTestDispatcher{}.dispatch})
	if err != nil {
		t.Fatal(err)
	}
	SetA2AService(service)
	t.Cleanup(func() { SetA2AService(nil) })
	task, err := service.SendMessage(context.Background(), a2aserver.Identity{AgentID: "owner"}, a2a.SendMessageParams{Message: a2a.Message{Role: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceA2A, Subject: "other", OrgSlug: "org", TeamSlug: "team", Scopes: []string{a2aScope}, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"pushNotification/set", "pushNotification/get", "pushNotification/delete"} {
		var params any = a2a.GetTaskParams{TaskID: task.ID}
		if method == "pushNotification/set" {
			params = a2a.SetPushNotificationParams{TaskID: task.ID, Config: a2a.PushNotificationConfig{URL: "https://example.com/hook"}}
		}
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(a2a.JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: raw})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		A2AHandler(w, httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(body)).WithContext(principal))
		var response a2a.JSONRPCResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Error == nil || response.Error.Code != a2a.ErrCodeTaskNotFound {
			t.Fatalf("%s: expected task-not-found, got %s", method, w.Body.String())
		}
	}
}
