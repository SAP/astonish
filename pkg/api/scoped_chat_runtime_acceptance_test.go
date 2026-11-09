package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// Runs the real HTTP handler, ChatAgent, runner, SQLite session persistence and
// history projection. Only the model and a deterministic native tool are fake.
func TestScopedChatRuntimeHTTPToolQuestionRestore(t *testing.T) {
	var observed execution.Principal
	probe, err := functiontool.New(functiontool.Config{Name: "ask_user", Description: "Ask a scoped question"}, func(ctx tool.Context, _ struct{}) (map[string]any, error) {
		observed, _ = execution.PrincipalFromContext(ctx)
		if store.FromContext(ctx) == nil || store.SessionServiceFromContext(ctx) == nil {
			t.Error("HTTP runtime did not bind scoped services and child-session store")
		}
		if store.UserIDFromContext(ctx) != observed.Subject {
			t.Error("lost HTTP user identity")
		}
		return map[string]any{"status": "ok", "kind": "text", "prompt": "Which tenant report?", "questionId": "scoped-question"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(ToolCallTurn("ask_user", map[string]any{}), TextTurn("Please specify a report.")), []tool.Tool{probe})
	events := env.Turn(t, "", "Ask for a report", false)
	assertHasEvent(t, events, "tool_call")
	assertHasEvent(t, events, "tool_result")
	question := assertHasEvent(t, events, "chat_question")
	assertEventData(t, question, "prompt", "Which tenant report?")
	assertHasEvent(t, events, "done")
	if observed.Subject != env.UserID || observed.OrgSlug != env.OrgSlug || observed.TeamSlug != env.TeamSlug {
		t.Fatalf("tool identity=%+v", observed)
	}
	sessionID := toString(assertHasEvent(t, events, "session").Data["sessionId"])
	detail := env.Detail(t, sessionID)
	for _, msg := range detail.Messages {
		if msg.QuestionID == "scoped-question" && msg.QuestionKind == "text" && msg.Prompt == "Which tenant report?" {
			return
		}
	}
	t.Fatalf("question missing from restored history: %#v", detail.Messages)
}

func TestScopedChatRuntimeChannelPrincipalPreservesContext(t *testing.T) {
	cached, err := json.Marshal([]map[string]any{{"name": "tenant_search", "description": "Scoped search", "inputSchema": map[string]any{"type": "object"}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	principal := execution.Principal{Kind: execution.PrincipalKindLocal, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, Subject: "channel-user", Authenticated: true}
	ctx, err = execution.WithPrincipal(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithServices(ctx, &store.Services{Mode: store.ModePlatform, PlatformMCPServers: &channelRuntimeMCPStore{servers: []store.MCPServer{{Name: "scoped-channel", Transport: "sse", URL: "https://example.invalid/mcp", Enabled: boolPtr(true), CachedTools: cached}}}})
	ctx = agent.WithPromptOverrides(ctx, &agent.PromptOverrides{SessionContext: "existing channel context"})
	bound := WithChannelRuntimeContext(ctx, nil)
	if agent.RequestMCPGroupsFromContext(bound)["mcp:scoped-channel"] == nil {
		t.Fatal("principal channel did not bind scoped catalog")
	}
	if p, ok := execution.PrincipalFromContext(bound); !ok || p.Subject != principal.Subject {
		t.Fatal("lost principal")
	}
	if agent.PromptOverridesFromContext(bound).SessionContext != "existing channel context" {
		t.Fatal("lost channel prompt context")
	}
	cancel()
	if bound.Err() != context.Canceled {
		t.Fatal("lost cancellation")
	}
}
