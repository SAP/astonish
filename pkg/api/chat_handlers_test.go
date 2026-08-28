package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/skills"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

type apiSkillStore struct {
	skills []store.Skill
}

func (s *apiSkillStore) LoadAll(context.Context) ([]store.Skill, error) { return s.skills, nil }
func (s *apiSkillStore) Get(_ context.Context, name string) (*store.Skill, error) {
	for i := range s.skills {
		if strings.EqualFold(s.skills[i].Name, name) {
			return &s.skills[i], nil
		}
	}
	return nil, nil
}
func (s *apiSkillStore) Save(context.Context, *store.Skill) error    { return nil }
func (s *apiSkillStore) Delete(context.Context, string) error        { return nil }
func (s *apiSkillStore) List(context.Context) ([]store.Skill, error) { return s.skills, nil }
func (s *apiSkillStore) UpdateValidationStatus(context.Context, string, string, string) error {
	return nil
}
func (s *apiSkillStore) ListFiles(context.Context, string) ([]store.SkillFile, error) {
	return nil, nil
}
func (s *apiSkillStore) GetFile(context.Context, string, string, string) (*store.SkillFile, error) {
	return nil, nil
}
func (s *apiSkillStore) SaveFile(context.Context, string, *store.SkillFile) error { return nil }
func (s *apiSkillStore) DeleteFile(context.Context, string, string, string) error { return nil }

func TestChatManagerEnsureReadyTimingLogs(t *testing.T) {
	tests := []struct {
		name     string
		manager  *ChatManager
		wantErr  string
		contains []string
	}{
		{
			name: "cold success",
			manager: &ChatManager{initFn: func(context.Context) (*StudioChatComponents, error) {
				return &StudioChatComponents{}, nil
			}},
			contains: []string{"msg=\"studio chat initialized\"", "component=studio-chat", "elapsed="},
		},
		{
			name: "cold failure",
			manager: &ChatManager{initFn: func(context.Context) (*StudioChatComponents, error) {
				return nil, errors.New("initialization failed")
			}},
			wantErr:  "initialization failed",
			contains: []string{"msg=\"studio chat initialization failed\"", "component=studio-chat", "elapsed=", "error=\"initialization failed\""},
		},
		{
			name: "cached is silent",
			manager: &ChatManager{components: &StudioChatComponents{}, initFn: func(context.Context) (*StudioChatComponents, error) {
				t.Fatal("cached components must not call initFn")
				return nil, nil
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			defer slog.SetDefault(old)

			err := tt.manager.ensureReady(context.Background())
			if tt.wantErr == "" && err != nil {
				t.Fatalf("ensureReady() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("ensureReady() error = %v, want %q", err, tt.wantErr)
			}
			got := buf.String()
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("log %q does not contain %q", got, want)
				}
			}
			if len(tt.contains) == 0 && got != "" {
				t.Errorf("cached ensureReady() logged %q", got)
			}
		})
	}
}

func TestBuildMergedSkillIndex_FilesystemBaseAndCaseInsensitiveLaterWins(t *testing.T) {
	filesystem := []skills.Skill{
		{Name: "filesystem-only", Description: "filesystem survives"},
		{Name: "Shared", Description: "filesystem overridden"},
	}
	platform := &apiSkillStore{skills: []store.Skill{{Name: "SHARED", Description: "platform overridden"}}}
	org := &apiSkillStore{skills: []store.Skill{{Name: "shared", Description: "org overridden"}}}
	team := &apiSkillStore{skills: []store.Skill{{Name: "sHaReD", Description: "team wins"}}}

	result := buildMergedSkillIndex(context.Background(), filesystem, platform, org, team)
	if !strings.Contains(result, "filesystem survives") || !strings.Contains(result, "team wins") {
		t.Fatalf("merged index missing base or winning entry:\n%s", result)
	}
	for _, overridden := range []string{"filesystem overridden", "platform overridden", "org overridden"} {
		if strings.Contains(result, overridden) {
			t.Errorf("overridden entry %q should not be rendered", overridden)
		}
	}
	if count := strings.Count(strings.ToLower(result), "**shared**"); count != 1 {
		t.Errorf("expected one rendered shared entry, got %d", count)
	}
}

// testEvents implements session.Events for testing.
type testEvents []*session.Event

func (e testEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, ev := range e {
			if !yield(ev) {
				return
			}
		}
	}
}
func (e testEvents) Len() int                { return len(e) }
func (e testEvents) At(i int) *session.Event { return e[i] }

// helper to build a text event.
func textEvent(invocationID, role, text string) *session.Event {
	return &session.Event{
		InvocationID: invocationID,
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role:  role,
				Parts: []*genai.Part{{Text: text}},
			},
		},
	}
}

func TestEventsToMessages_ModelInlineImage(t *testing.T) {
	t.Parallel()
	png := []byte{0x89, 0x50, 0x4e, 0x47}
	events := testEvents{
		{
			InvocationID: "inv-img",
			LLMResponse: model.LLMResponse{
				Content: &genai.Content{
					Role: "model",
					Parts: []*genai.Part{{
						InlineData: &genai.Blob{MIMEType: "image/png", Data: png},
					}},
				},
			},
		},
	}
	msgs := eventsToMessages(events, nil)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Type != "image" {
		t.Fatalf("type=%q, want image", msgs[0].Type)
	}
	if msgs[0].MimeType != "image/png" {
		t.Errorf("mimeType=%q", msgs[0].MimeType)
	}
	if msgs[0].Data == "" {
		t.Error("expected base64 data")
	}
}

func TestEventsToMessages_CoalescesSameInvocation(t *testing.T) {
	// Two model text parts in the same invocation should be coalesced.
	events := testEvents{
		textEvent("inv-1", "model", "Hello "),
		textEvent("inv-1", "model", "world"),
	}

	msgs := eventsToMessages(events, nil)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "Hello world" {
		t.Errorf("expected coalesced content 'Hello world', got %q", msgs[0].Content)
	}
	if msgs[0].Type != "agent" {
		t.Errorf("expected type 'agent', got %q", msgs[0].Type)
	}
}

func TestEventsToMessages_NoCoalesceAcrossInvocations(t *testing.T) {
	// Two user messages in different invocations must NOT be coalesced.
	// This is the bug fix for session d2255947 where consecutive user
	// messages (with no model response between them) were merged on reload.
	events := testEvents{
		textEvent("inv-1", "user", "first message"),
		textEvent("inv-2", "user", "second message"),
		textEvent("inv-3", "user", "third message"),
	}

	msgs := eventsToMessages(events, nil)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 separate messages, got %d: %+v", len(msgs), msgs)
	}
	for i, want := range []string{"first message", "second message", "third message"} {
		if msgs[i].Content != want {
			t.Errorf("message[%d]: expected %q, got %q", i, want, msgs[i].Content)
		}
		if msgs[i].Type != "user" {
			t.Errorf("message[%d]: expected type 'user', got %q", i, msgs[i].Type)
		}
	}
}

func TestEventsToMessages_MixedInvocations(t *testing.T) {
	// Normal conversation flow: user → model (same inv), user → model (new inv).
	events := testEvents{
		textEvent("inv-1", "user", "question 1"),
		textEvent("inv-1", "model", "answer 1"),
		textEvent("inv-2", "user", "question 2"),
		textEvent("inv-2", "model", "answer 2"),
	}

	msgs := eventsToMessages(events, nil)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(msgs), msgs)
	}
	expected := []struct {
		typ     string
		content string
	}{
		{"user", "question 1"},
		{"agent", "answer 1"},
		{"user", "question 2"},
		{"agent", "answer 2"},
	}
	for i, want := range expected {
		if msgs[i].Type != want.typ {
			t.Errorf("message[%d]: expected type %q, got %q", i, want.typ, msgs[i].Type)
		}
		if msgs[i].Content != want.content {
			t.Errorf("message[%d]: expected content %q, got %q", i, want.content, msgs[i].Content)
		}
	}
}

func TestEventsToMessages_StripsTimestamp(t *testing.T) {
	// User messages with timestamp prefix should have it stripped.
	events := testEvents{
		textEvent("inv-1", "user", "[2026-03-20 14:30:05 UTC]\nHello"),
	}

	msgs := eventsToMessages(events, nil)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "Hello" {
		t.Errorf("expected timestamp stripped, got %q", msgs[0].Content)
	}
}

func TestEventsToMessages_ErrorEventRendersAsAgent(t *testing.T) {
	// A persisted error event (model role with "[Error: ...]" text) should
	// render as an "agent" type message so it shows up in the chat UI.
	events := testEvents{
		textEvent("inv-1", "user", "do something"),
		textEvent("", "model", "[Error: unexpected end of JSON input]"),
	}

	msgs := eventsToMessages(events, nil)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[1].Type != "agent" {
		t.Errorf("expected error message type 'agent', got %q", msgs[1].Type)
	}
	if msgs[1].Content != "[Error: unexpected end of JSON input]" {
		t.Errorf("expected error content preserved, got %q", msgs[1].Content)
	}
}

func TestEventsToMessages_NilContentSkipped(t *testing.T) {
	// Events with nil Content should be silently skipped.
	events := testEvents{
		{InvocationID: "inv-1", LLMResponse: model.LLMResponse{Content: nil}},
		textEvent("inv-1", "model", "hello"),
	}

	msgs := eventsToMessages(events, nil)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "hello" {
		t.Errorf("expected 'hello', got %q", msgs[0].Content)
	}
}

func TestEventsToMessages_ToolCallBreaksCoalescing(t *testing.T) {
	// A tool call between two model text events in the same invocation
	// should result in separate text messages (tool_call in between).
	events := testEvents{
		textEvent("inv-1", "model", "Let me check."),
		{
			InvocationID: "inv-1",
			LLMResponse: model.LLMResponse{
				Content: &genai.Content{
					Role: "model",
					Parts: []*genai.Part{{
						FunctionCall: &genai.FunctionCall{
							Name: "shell_command",
							Args: map[string]any{"command": "ls"},
						},
					}},
				},
			},
		},
		textEvent("inv-1", "model", "Here are the files."),
	}

	msgs := eventsToMessages(events, nil)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Type != "agent" || msgs[0].Content != "Let me check." {
		t.Errorf("message[0]: unexpected %+v", msgs[0])
	}
	if msgs[1].Type != "tool_call" || msgs[1].ToolName != "shell_command" {
		t.Errorf("message[1]: unexpected %+v", msgs[1])
	}
	if msgs[2].Type != "agent" || msgs[2].Content != "Here are the files." {
		t.Errorf("message[2]: unexpected %+v", msgs[2])
	}
}

func TestTryParseAppPreviewMessage_WithAppID(t *testing.T) {
	text := `[app_preview]{"code":"function App() { return <div>hi</div> }","title":"My App","version":1,"appId":"uuid-123"}`
	msg := tryParseAppPreviewMessage(text)
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.Type != "app_preview" {
		t.Errorf("expected type app_preview, got %q", msg.Type)
	}
	if msg.AppID != "uuid-123" {
		t.Errorf("expected appId uuid-123, got %q", msg.AppID)
	}
	if msg.AppVersion != 1 {
		t.Errorf("expected version 1, got %d", msg.AppVersion)
	}
}

func TestTryParseAppPreviewMessage_WithoutAppID(t *testing.T) {
	// Backward compatibility: old format without appId
	text := `[app_preview]{"code":"function Old() {}","title":"Old App","version":2}`
	msg := tryParseAppPreviewMessage(text)
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.AppID != "" {
		t.Errorf("expected empty appId, got %q", msg.AppID)
	}
	if msg.AppVersion != 2 {
		t.Errorf("expected version 2, got %d", msg.AppVersion)
	}
}

func TestReconstructActiveApp(t *testing.T) {
	events := testEvents{
		textEvent("inv-1", "model", `[app_preview]{"code":"function V1() {}","title":"App","version":1,"appId":"uuid-abc"}`),
		textEvent("inv-2", "model", `[app_preview]{"code":"function V2() {}","title":"App","version":2,"appId":"uuid-abc"}`),
	}
	app := reconstructActiveApp(events)
	if app == nil {
		t.Fatal("expected non-nil active app")
	}
	if app.AppID != "uuid-abc" {
		t.Errorf("expected appId uuid-abc, got %q", app.AppID)
	}
	if app.Version != 2 {
		t.Errorf("expected version 2, got %d", app.Version)
	}
	if app.Code != "function V2() {}" {
		t.Errorf("expected V2 code, got %q", app.Code)
	}
	if len(app.Versions) != 1 {
		t.Fatalf("expected 1 version in history, got %d", len(app.Versions))
	}
	if app.Versions[0] != "function V1() {}" {
		t.Errorf("expected V1 in history, got %q", app.Versions[0])
	}
}

func TestReconstructActiveApp_NoAppPreviews(t *testing.T) {
	events := testEvents{
		textEvent("inv-1", "model", "Hello world"),
	}
	app := reconstructActiveApp(events)
	if app != nil {
		t.Errorf("expected nil, got %+v", app)
	}
}

func TestExtractAppFromSystemContext(t *testing.T) {
	tests := []struct {
		name      string
		ctx       string
		wantCode  string
		wantTitle string
	}{
		{
			name:      "valid refinement context",
			ctx:       "## Active App Refinement\n\nSome text.\n\n### Current Source Code\n\n```jsx\nfunction WeatherApp() {\n  return <div>Hello</div>\n}\nexport default WeatherApp\n```\n",
			wantCode:  "function WeatherApp() {\n  return <div>Hello</div>\n}\nexport default WeatherApp",
			wantTitle: "Weather App",
		},
		{
			name:      "no refinement marker",
			ctx:       "Some random system context without the marker",
			wantCode:  "",
			wantTitle: "",
		},
		{
			name:      "refinement marker but no code block",
			ctx:       "## Active App Refinement\n\nNo code here.",
			wantCode:  "",
			wantTitle: "",
		},
		{
			name:      "empty system context",
			ctx:       "",
			wantCode:  "",
			wantTitle: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, title := extractAppFromSystemContext(tt.ctx)
			if code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
		})
	}
}

func TestStripAppFrontmatter(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantCode  string
		wantTitle string
	}{
		{
			name:      "no frontmatter",
			input:     "function App() { return <div>hello</div> }",
			wantCode:  "function App() { return <div>hello</div> }",
			wantTitle: "",
		},
		{
			name:      "title only frontmatter",
			input:     "title: My Dashboard\n---\nfunction App() { return <div>hello</div> }",
			wantCode:  "function App() { return <div>hello</div> }",
			wantTitle: "My Dashboard",
		},
		{
			name:      "title and description frontmatter",
			input:     "title: Sales Report\ndescription: A sales report dashboard\n---\nfunction SalesReport() { return <div /> }",
			wantCode:  "function SalesReport() { return <div /> }",
			wantTitle: "Sales Report",
		},
		{
			name:      "separator in code but no title",
			input:     "const x = 'a'\n---\nconst y = 'b'",
			wantCode:  "const x = 'a'\n---\nconst y = 'b'",
			wantTitle: "",
		},
		{
			name:      "empty code after frontmatter",
			input:     "title: Empty\n---\n",
			wantCode:  "",
			wantTitle: "Empty",
		},
		{
			name:      "no separator at all",
			input:     "title: Not Frontmatter Because No Separator",
			wantCode:  "title: Not Frontmatter Because No Separator",
			wantTitle: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, title := stripAppFrontmatter(tt.input)
			if code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
		})
	}
}

// pinnedTeamDataStore overrides mockTeamDataStore.SessionPin to return a fixed pin.
type pinnedTeamDataStore struct {
	*mockTeamDataStore
	pin *store.SessionPin
}

func (p *pinnedTeamDataStore) SessionPin(_ context.Context, _ string) (*store.SessionPin, error) {
	return p.pin, nil
}

// pinnedOrgDataStore.ForTeam returns the test's pinnedTeamDataStore.
type pinnedOrgDataStore struct {
	*mockOrgDataStore
	teamStore store.TeamDataStore
}

func (p *pinnedOrgDataStore) ForTeam(_ string) store.TeamDataStore {
	return p.teamStore
}

// credSecretStore is a mockCredentialStore variant with configurable secrets.
type credSecretStore struct {
	mockCredentialStore
	secrets map[string]string
}

func (c *credSecretStore) GetSecret(_ context.Context, key string) string {
	return c.secrets[key]
}

// pinnedTenantRouter injects a specific OrgDataStore for any org slug.
type pinnedTenantRouter struct {
	override store.OrgDataStore
}

func (p *pinnedTenantRouter) ForOrg(_ string) (store.OrgDataStore, error) {
	return p.override, nil
}

func (p *pinnedTenantRouter) ProvisionOrg(_ context.Context, _, _ string) error { return nil }
func (p *pinnedTenantRouter) DecommissionOrg(_ context.Context, _ string) error { return nil }

func buildModelStatusRequest(svc *store.Services, pu *PlatformUser, sessionID string) *http.Request {
	r := httptest.NewRequest("GET", "/api/studio/sessions/"+sessionID+"/model-status", nil)
	ctx := store.WithServices(r.Context(), svc)
	if pu != nil {
		ctx = WithPlatformUser(ctx, pu)
	}
	r = r.WithContext(ctx)
	return mux.SetURLVars(r, map[string]string{"id": sessionID})
}

func newModelStatusServices(pin *store.SessionPin, teamProviders map[string]store.ProviderConfig, defaultProvider, defaultModel string, secrets map[string]string) *store.Services {
	teamMock := newMockTeamDataStore()
	pinned := &pinnedTeamDataStore{mockTeamDataStore: teamMock, pin: pin}
	orgMock := newMockOrgDataStore()
	orgWrap := &pinnedOrgDataStore{mockOrgDataStore: orgMock, teamStore: pinned}
	return &store.Services{
		Mode:         store.ModePlatform,
		TenantRouter: &pinnedTenantRouter{override: orgWrap},
		Settings: &mockTeamSettingsStore{settings: &store.TeamSettings{
			DefaultProvider: defaultProvider,
			DefaultModel:    defaultModel,
			Providers:       teamProviders,
		}},
		Credentials: &credSecretStore{secrets: secrets},
	}
}

// Given a session pinned to a provider with a credential,
// When GET model-status is called,
// Then response reports pin, effective=pin, credentialsAvailable=true,
// availableProviders lists both providers sorted lexicographically.
func TestSessionModelStatus_Pinned(t *testing.T) {
	t.Parallel()

	svc := newModelStatusServices(
		&store.SessionPin{Provider: "openai", Model: "gpt-5"},
		map[string]store.ProviderConfig{
			"openai":    {"type": "openai"},
			"anthropic": {"type": "anthropic"},
		},
		"anthropic", "claude-sonnet-4.5",
		map[string]string{"openai": "sk-...", "anthropic": "sk-ant-..."},
	)
	pu := &PlatformUser{ID: "u1", OrgSlug: "acme", TeamSlug: "eng"}
	r := buildModelStatusRequest(svc, pu, "sess-1")
	w := httptest.NewRecorder()

	GetSessionModelStatusHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp SessionModelStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.PinnedProvider != "openai" || resp.PinnedModel != "gpt-5" {
		t.Errorf("pin = %s/%s, want openai/gpt-5", resp.PinnedProvider, resp.PinnedModel)
	}
	if resp.EffectiveProvider != "openai" || resp.EffectiveModel != "gpt-5" {
		t.Errorf("effective = %s/%s, want openai/gpt-5", resp.EffectiveProvider, resp.EffectiveModel)
	}
	if !resp.CredentialsAvailable {
		t.Error("credentialsAvailable = false, want true (pin has credential)")
	}
	if len(resp.AvailableProviders) != 2 ||
		resp.AvailableProviders[0] != "anthropic" || resp.AvailableProviders[1] != "openai" {
		t.Errorf("availableProviders = %v, want [anthropic openai]", resp.AvailableProviders)
	}
}

// Given a session pinned to a provider with NO credential,
// When GET model-status is called,
// Then pin+effective still reflect the pin, credentialsAvailable=false —
// soft fallback (DECISION-3), no error.
func TestSessionModelStatus_PinnedMissingCred(t *testing.T) {
	t.Parallel()

	svc := newModelStatusServices(
		&store.SessionPin{Provider: "openai", Model: "gpt-5"},
		map[string]store.ProviderConfig{
			"openai":    {"type": "openai"},
			"anthropic": {"type": "anthropic"},
		},
		"anthropic", "claude-sonnet-4.5",
		map[string]string{"anthropic": "sk-ant-..."},
	)
	pu := &PlatformUser{ID: "u1", OrgSlug: "acme", TeamSlug: "eng"}
	r := buildModelStatusRequest(svc, pu, "sess-2")
	w := httptest.NewRecorder()

	GetSessionModelStatusHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (soft fallback, not error): %s", w.Code, w.Body.String())
	}
	var resp SessionModelStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.PinnedProvider != "openai" {
		t.Errorf("pinnedProvider = %s, want openai", resp.PinnedProvider)
	}
	if resp.EffectiveProvider != "openai" {
		t.Errorf("effectiveProvider = %s, want openai", resp.EffectiveProvider)
	}
	if resp.CredentialsAvailable {
		t.Error("credentialsAvailable = true, want false (openai has no credential)")
	}
	if len(resp.AvailableProviders) != 2 ||
		resp.AvailableProviders[0] != "anthropic" || resp.AvailableProviders[1] != "openai" {
		t.Errorf("availableProviders = %v, want [anthropic openai] (full resolved set, same as pre-chat)", resp.AvailableProviders)
	}
}

// Given a session with no pin,
// When GET model-status is called,
// Then pin fields are empty, effective=team default, credentialsAvailable=true.
func TestSessionModelStatus_NoPins(t *testing.T) {
	t.Parallel()

	svc := newModelStatusServices(
		nil,
		map[string]store.ProviderConfig{"anthropic": {"type": "anthropic"}},
		"anthropic", "claude-sonnet-4.5",
		map[string]string{"anthropic": "sk-ant-..."},
	)
	pu := &PlatformUser{ID: "u1", OrgSlug: "acme", TeamSlug: "eng"}
	r := buildModelStatusRequest(svc, pu, "sess-3")
	w := httptest.NewRecorder()

	GetSessionModelStatusHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp SessionModelStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.PinnedProvider != "" || resp.PinnedModel != "" {
		t.Errorf("pin = %s/%s, want empty", resp.PinnedProvider, resp.PinnedModel)
	}
	if resp.EffectiveProvider != "anthropic" || resp.EffectiveModel != "claude-sonnet-4.5" {
		t.Errorf("effective = %s/%s, want anthropic/claude-sonnet-4.5",
			resp.EffectiveProvider, resp.EffectiveModel)
	}
	if !resp.CredentialsAvailable {
		t.Error("credentialsAvailable = false, want true (unpinned = healthy)")
	}
	if len(resp.AvailableProviders) != 1 || resp.AvailableProviders[0] != "anthropic" {
		t.Errorf("availableProviders = %v, want [anthropic]", resp.AvailableProviders)
	}
}

type recordingTeamDataStore struct {
	*mockTeamDataStore
	setCalls []struct{ SessionID, Provider, Model string }
	setErr   error
}

func (r *recordingTeamDataStore) SetSessionPin(_ context.Context, sessionID, provider, model string) error {
	if r.setErr != nil {
		return r.setErr
	}
	r.setCalls = append(r.setCalls, struct{ SessionID, Provider, Model string }{sessionID, provider, model})
	return nil
}

func buildPatchModelRequest(t *testing.T, svc *store.Services, pu *PlatformUser, sessionID, provider, modelName string) *http.Request {
	t.Helper()
	body, err := json.Marshal(PatchSessionModelRequest{Provider: provider, Model: modelName})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PATCH", "/api/studio/sessions/"+sessionID+"/model", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	ctx := store.WithServices(r.Context(), svc)
	if pu != nil {
		ctx = WithPlatformUser(ctx, pu)
	}
	r = r.WithContext(ctx)
	return mux.SetURLVars(r, map[string]string{"id": sessionID})
}

func newPatchModelServices(recorder *recordingTeamDataStore, teamProviders map[string]store.ProviderConfig, defaultProvider, defaultModel string, secrets map[string]string) *store.Services {
	orgMock := newMockOrgDataStore()
	orgWrap := &pinnedOrgDataStore{mockOrgDataStore: orgMock, teamStore: recorder}
	return &store.Services{
		Mode:         store.ModePlatform,
		TenantRouter: &pinnedTenantRouter{override: orgWrap},
		Settings: &mockTeamSettingsStore{settings: &store.TeamSettings{
			DefaultProvider: defaultProvider,
			DefaultModel:    defaultModel,
			Providers:       teamProviders,
		}},
		Credentials: &credSecretStore{secrets: secrets},
	}
}

// Given a platform user PATCHes a session model with a valid provider+model,
// When the handler runs,
// Then the pin is persisted through TenantRouter → team store, the response
// reports the pin as both pinned and effective. The hot-swap may fail because
// the singleton ChatManager is not initialized in unit tests — that path is
// non-fatal and does not affect persistence.
func TestPatchSessionModel_HappyPath(t *testing.T) {
	t.Parallel()

	recorder := &recordingTeamDataStore{mockTeamDataStore: newMockTeamDataStore()}
	svc := newPatchModelServices(
		recorder,
		map[string]store.ProviderConfig{
			"openai":    {"type": "openai"},
			"anthropic": {"type": "anthropic"},
		},
		"anthropic", "claude-sonnet-4.5",
		map[string]string{"openai": "sk-...", "anthropic": "sk-ant-..."},
	)
	pu := &PlatformUser{ID: "u1", OrgSlug: "acme", TeamSlug: "eng"}
	r := buildPatchModelRequest(t, svc, pu, "sess-1", "openai", "gpt-5")
	w := httptest.NewRecorder()

	PatchSessionModelHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp PatchSessionModelResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.PinnedProvider != "openai" || resp.PinnedModel != "gpt-5" {
		t.Errorf("pin = %s/%s, want openai/gpt-5", resp.PinnedProvider, resp.PinnedModel)
	}
	if resp.EffectiveProvider != "openai" || resp.EffectiveModel != "gpt-5" {
		t.Errorf("effective = %s/%s, want openai/gpt-5", resp.EffectiveProvider, resp.EffectiveModel)
	}
	if len(recorder.setCalls) != 1 {
		t.Fatalf("setCalls = %d, want 1", len(recorder.setCalls))
	}
	if recorder.setCalls[0].SessionID != "sess-1" ||
		recorder.setCalls[0].Provider != "openai" ||
		recorder.setCalls[0].Model != "gpt-5" {
		t.Errorf("SetSessionPin recorded %+v, want sess-1/openai/gpt-5", recorder.setCalls[0])
	}
}

// Given a PATCH targets a provider with no credential,
// When the handler runs,
// Then the pin still persists and the response is HTTP 200 (soft fallback
// per DECISION-3 — never hard-fail).
func TestPatchSessionModel_MissingCred(t *testing.T) {
	t.Parallel()

	recorder := &recordingTeamDataStore{mockTeamDataStore: newMockTeamDataStore()}
	svc := newPatchModelServices(
		recorder,
		map[string]store.ProviderConfig{
			"openai":    {"type": "openai"},
			"anthropic": {"type": "anthropic"},
		},
		"anthropic", "claude-sonnet-4.5",
		map[string]string{"anthropic": "sk-ant-..."},
	)
	pu := &PlatformUser{ID: "u1", OrgSlug: "acme", TeamSlug: "eng"}
	r := buildPatchModelRequest(t, svc, pu, "sess-2", "openai", "gpt-5")
	w := httptest.NewRecorder()

	PatchSessionModelHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (soft fallback): %s", w.Code, w.Body.String())
	}
	if len(recorder.setCalls) != 1 {
		t.Errorf("pin not persisted when credential missing (setCalls=%d)", len(recorder.setCalls))
	}
}

// Given a PATCH is received without a PlatformUser in context (personal mode),
// When the handler runs,
// Then no SetSessionPin call is made (persistence silently skipped) and the
// effective values still come from the resolved cascade + override.
func TestPatchSessionModel_NoTenantContextSkipsPersistence(t *testing.T) {
	t.Parallel()

	recorder := &recordingTeamDataStore{mockTeamDataStore: newMockTeamDataStore()}
	svc := newPatchModelServices(
		recorder,
		map[string]store.ProviderConfig{"anthropic": {"type": "anthropic"}},
		"anthropic", "claude-sonnet-4.5",
		map[string]string{"anthropic": "sk-ant-..."},
	)
	r := buildPatchModelRequest(t, svc, nil, "sess-3", "anthropic", "claude-opus-4")
	w := httptest.NewRecorder()

	PatchSessionModelHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", w.Code, w.Body.String())
	}
	if len(recorder.setCalls) != 0 {
		t.Errorf("SetSessionPin called without tenant context: %+v", recorder.setCalls)
	}
	var resp PatchSessionModelResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.EffectiveProvider != "anthropic" || resp.EffectiveModel != "claude-opus-4" {
		t.Errorf("effective = %s/%s, want anthropic/claude-opus-4",
			resp.EffectiveProvider, resp.EffectiveModel)
	}
}

// Given ChatManager.components holds a stale global provider/model, and the
// session is pinned to a different provider,
// When /status is handled,
// Then the SSE body reports the session pin — not the singleton metadata.
func TestSlashCommand_Status_ReflectsSessionPin(t *testing.T) {
	t.Parallel()

	svc := newModelStatusServices(
		&store.SessionPin{Provider: "openai", Model: "gpt-5"},
		map[string]store.ProviderConfig{
			"openai":    {"type": "openai"},
			"anthropic": {"type": "anthropic"},
		},
		"anthropic", "claude-sonnet-4.5",
		map[string]string{"openai": "sk-...", "anthropic": "sk-ant-..."},
	)
	pu := &PlatformUser{ID: "u1", OrgSlug: "acme", TeamSlug: "eng"}
	r := httptest.NewRequest("POST", "/api/studio/chat", nil)
	ctx := store.WithServices(r.Context(), svc)
	ctx = WithPlatformUser(ctx, pu)
	r = r.WithContext(ctx)

	cm := &ChatManager{
		components: &StudioChatComponents{
			// Deliberately wrong — the bug was reading these globals.
			ProviderName: "stale-global-provider",
			ModelName:    "stale-global-model",
			ChatAgent:    &agent.ChatAgent{},
		},
	}

	w := httptest.NewRecorder()
	handleSlashCommand(r, w, nil, cm, nil, "/status", "u1", "sess-1")

	body := w.Body.String()
	if !strings.Contains(body, "openai") || !strings.Contains(body, "gpt-5") {
		t.Fatalf("/status body missing session pin openai/gpt-5:\n%s", body)
	}
	if strings.Contains(body, "stale-global-provider") || strings.Contains(body, "stale-global-model") {
		t.Fatalf("/status still reports ChatManager singleton model:\n%s", body)
	}
	if !strings.Contains(body, "Session pin: active") {
		t.Fatalf("/status missing session-pin indicator:\n%s", body)
	}
}
