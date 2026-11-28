package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

const continuationHTTPYAML = `description: retained HTTP flow
nodes:
  - name: seed
    type: input
    prompt: Seed
  - name: answer
    type: input
    prompt: Answer
  - name: done
    type: output
    user_message: [continued original HTTP flow]
flow:
  - from: START
    to: seed
  - from: seed
    to: answer
  - from: answer
    to: done
  - from: done
    to: END
`

type continuationHTTPPool struct{ catalogTestPool }

func (*continuationHTTPPool) GetOrCreate(string) sandbox.ToolNodeClient { return nil }
func (*continuationHTTPPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return nil
}
func (*continuationHTTPPool) GetOrCreateWithImage(string, string, []string, string) sandbox.ToolNodeClient {
	return nil
}
func (*continuationHTTPPool) SetSessionScope(string, string, string) {}

type continuationHTTPModel struct{ calls atomic.Int32 }

func (*continuationHTTPModel) Name() string { return "offline-continuation" }
func (m *continuationHTTPModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if len(req.Tools) == 0 {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Flow continuation", "model"), TurnComplete: true}, nil)
			return
		}
		call := m.calls.Add(1)
		if call == 1 || call == 3 {
			params := map[string]any{"seed": "original child state"}
			if call == 3 {
				params = map[string]any{"answer": "owner answer"}
			}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprint(call), Name: "execute_tool", Args: map[string]any{"name": "run_flow", "arguments": map[string]any{"flow_name": "flow", "parameters": params}}}}}}}, nil)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Flow turn returned.", "model"), TurnComplete: true}, nil)
	}
}

// Two real HTTP/SSE requests through StudioChatHandler and the production shared
// constructor; only authentication/model/compute transport are local fixtures.
func TestStudioChatRuntimeFactoryFlowContinuationHTTP(t *testing.T) {
	t.Run("resume", func(t *testing.T) { testStudioChatFlowContinuationHTTP(t, false) })
	t.Run("delete_paused", func(t *testing.T) { testStudioChatFlowContinuationHTTP(t, true) })
}

func testStudioChatFlowContinuationHTTP(t *testing.T, deletePaused bool) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	ctx := t.Context()
	svc, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "continuation", Name: "Continuation", Status: "active"}
	if err := db.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := db.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	if err := db.Users().Create(ctx, &store.User{ID: owner, Email: "continuation@example.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Organizations().AddMember(ctx, owner, org.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "general", Name: "General", SchemaName: "team_general"}
	if err := ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	svc.PersonalSessions = ods.ForUser(owner).Sessions()
	svc.PersonalFlows = &productionFlowStore{yaml: continuationHTTPYAML}
	svc.PlatformMCPServers = db.PlatformMCPServers()
	disabled := false
	for _, standard := range config.GetStandardServers() {
		if standard.Kind != "model" {
			if err := svc.PlatformMCPServers.Save(ctx, &store.MCPServer{Name: standard.ID, Transport: "stdio", Enabled: &disabled}); err != nil {
				t.Fatal(err)
			}
		}
	}
	principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: "general", Authenticated: true}
	ownerCtx, _ := execution.WithPrincipal(ctx, principal)
	model := &continuationHTTPModel{}
	bootstrap := agent.NewChatAgent(model, nil, nil, svc.PersonalSessions, &agent.SystemPromptBuilder{}, false, true)
	factory := newStudioChatRuntimeFactory(ChatFactoryConfig{AppConfig: &config.AppConfig{}}, ChatCatalogResources{Pool: &continuationHTTPPool{}}, nil)
	var cleanups atomic.Int32
	var attempts atomic.Int32
	cm := api.GetChatManager()
	cm.Reset()
	api.SetLLMPool(nil)
	api.SetStudioChatInitFunc(func(context.Context) (*api.StudioChatComponents, error) {
		return &api.StudioChatComponents{ChatAgent: bootstrap, SessionService: svc.PersonalSessions, RuntimeFactory: func(ctx context.Context, req api.StudioChatRuntimeRequest) (*api.StudioChatRuntime, error) {
			runtime, err := factory(ctx, req)
			if err == nil {
				n := attempts.Add(1)
				original := runtime.Cleanup
				runtime.Cleanup = func() {
					original()
					if n == 1 {
						runtime.ChatAgent.SetPendingTutorialBlueprint(req.SessionID, &agent.TutorialBlueprintPending{YAML: "retained review"})
					} else if !runtime.ChatAgent.HasPendingTutorialBlueprint(req.SessionID) {
						t.Error("second HTTP attempt lost conversation data")
					}
					cleanups.Add(1)
				}
			}
			return runtime, err
		}}, nil
	})
	defer func() { cm.Reset(); api.SetStudioChatInitFunc(nil) }()
	router := mux.NewRouter()
	router.HandleFunc("/chat", api.StudioChatHandler).Methods("POST")
	router.HandleFunc("/sessions/{id}", api.StudioDeleteSessionHandler).Methods("DELETE")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principal
		if r.Header.Get("X-Foreign") != "" {
			p.Subject = uuid.NewString()
		}
		c, _ := execution.WithPrincipal(r.Context(), p)
		c = store.WithServices(c, svc)
		c = store.WithTenantContext(c, &store.TenantContext{OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, UserID: p.Subject})
		c = api.WithPlatformUser(c, &api.PlatformUser{ID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Role: "member"})
		router.ServeHTTP(w, r.WithContext(c))
	}))
	defer server.Close()
	server.Client().Timeout = 20 * time.Second
	request := func(method, path, body string, foreign bool) (int, string) {
		t.Helper()
		r, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if foreign {
			r.Header.Set("X-Foreign", "1")
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		b, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(b)
	}
	waitCleanup := func(n int32) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for cleanups.Load() < n && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if cleanups.Load() != n {
			t.Fatal("attempt not cleaned", cleanups.Load())
		}
	}
	status, first := request("POST", "/chat", `{"message":"start flow","autoApprove":true,"pinnedToolGroups":["flows"]}`, false)
	if status != 200 || !strings.Contains(first, `waiting_for_input`) || !strings.Contains(first, `answer`) {
		t.Fatalf("first turn %d: %s", status, first)
	}
	sessionID := ""
	for _, line := range strings.Split(first, "\n") {
		if strings.HasPrefix(line, "data: ") {
			var data map[string]any
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data)
			if id, ok := data["sessionId"].(string); ok {
				sessionID = id
				break
			}
		}
	}
	if sessionID == "" {
		t.Fatal("missing session event", first)
	}
	waitCleanup(1)
	if !bootstrap.HasPendingTutorialBlueprint(sessionID) {
		t.Fatal("attempt review data not retained by ChatManager")
	}
	status, deniedDelete := request("DELETE", "/sessions/"+sessionID, "", true)
	// Owner-scoped stores mask absent/foreign sessions as an idempotent 204.
	// Either that or an explicit denial must leave the owner's data intact.
	if (status != http.StatusNoContent && status < 400) || !bootstrap.HasPendingTutorialBlueprint(sessionID) {
		t.Fatalf("foreign delete changed conversation state %d: %s", status, deniedDelete)
	}
	status, wrongApp := request("DELETE", "/sessions/"+sessionID+"?app=astonish_code", "", false)
	if status < 400 || !bootstrap.HasPendingTutorialBlueprint(sessionID) {
		t.Fatalf("wrong-app delete changed conversation state %d: %s", status, wrongApp)
	}
	data, publish, err := cm.BeginContinuation(ownerCtx, "astonish", sessionID)
	retained, ok := data.(*scopedChatFlowContinuation)
	if err != nil || !ok || len(retained.sessions) != 1 {
		t.Fatalf("paused continuation lost after denied deletion: %T %v", data, err)
	}
	publish(retained)
	if deletePaused {
		status, body := request("DELETE", "/sessions/"+sessionID, "", false)
		if status != 204 {
			t.Fatalf("delete paused %d: %s", status, body)
		}
		if len(retained.sessions) != 0 || bootstrap.HasPendingTutorialBlueprint(sessionID) {
			t.Fatal("deleted paused flow or review retained")
		}
		data, finish, err := cm.BeginContinuation(ownerCtx, "astonish", sessionID)
		if err != nil || data != nil {
			t.Fatal("deleted continuation visible", err)
		}
		finish(nil)
		return
	}
	payload, _ := json.Marshal(api.StudioChatRequest{SessionID: sessionID, Message: "owner answer", AutoApprove: true, PinnedToolGroups: []string{"flows"}})
	status, foreign := request("POST", "/chat", string(payload), true)
	if status < 400 || model.calls.Load() != 2 {
		t.Fatalf("foreign owner reached execution %d %s", status, foreign)
	}
	status, second := request("POST", "/chat", string(payload), false)
	if status != 200 || !strings.Contains(second, "continued original HTTP flow") || strings.Contains(second, `waiting_for_input`) {
		t.Fatalf("second turn %d: %s", status, second)
	}
	waitCleanup(2)
	data, finish, err := cm.BeginContinuation(ownerCtx, "astonish", sessionID)
	if err != nil || data != nil {
		t.Fatal("completed child retained", err)
	}
	finish(nil)
	status, body := request("DELETE", "/sessions/"+sessionID, "", false)
	if status != 204 {
		t.Fatalf("delete %d: %s", status, body)
	}
	if bootstrap.HasPendingTutorialBlueprint(sessionID) {
		t.Fatal("deleted session review retained")
	}
}
