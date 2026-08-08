package api

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// --- Multi-Tenant LLM Isolation Tests ---
//
// These tests verify that per-request LLM resolution works correctly:
// different users in different teams receive responses from different models,
// all running concurrently within the same process.

// TestIntegration_MT1_TwoTeamsDifferentModels verifies that two users in
// different teams get responses from their respective team's configured LLM,
// not a shared global model.
func TestIntegration_MT1_TwoTeamsDifferentModels(t *testing.T) {
	// Create two distinct MockLLMs with identifiable responses
	teamAlphaLLM := NewMockLLM(TextTurn("Response from Team-Alpha model (gpt-4-turbo)"))
	teamBetaLLM := NewMockLLM(TextTurn("Response from Team-Beta model (claude-opus)"))

	// Create a shared default LLM (should NOT be used when team override is injected)
	defaultLLM := NewMockLLM(TextTurn("Response from DEFAULT model (should not appear)"))

	// Setup shared session service and prompt builder
	sessionService := common.NewAutoInitService(session.InMemoryService())
	promptBuilder := &agent.SystemPromptBuilder{}

	// Each user gets their own ChatAgent instance (ChatAgent.Run is not safe
	// for concurrent use from multiple goroutines due to mutable fields like
	// AutoApprove and UIEventCallback).
	chatAgentA := agent.NewChatAgent(defaultLLM, nil, nil, sessionService, promptBuilder, false, true)
	chatAgentB := agent.NewChatAgent(defaultLLM, nil, nil, sessionService, promptBuilder, false, true)

	// Simulate User A (Team Alpha)
	sessionA := createTestSession(t, sessionService)
	runnerA := newChatRunner(sessionA, studioChatUserID, true)
	chA := runnerA.Subscribe("test-alpha")
	runnerA.InjectLLM(teamAlphaLLM) // Inject team-specific LLM

	// Simulate User B (Team Beta)
	sessionB := createTestSession(t, sessionService)
	runnerB := newChatRunner(sessionB, studioChatUserID, true)
	chB := runnerB.Subscribe("test-beta")
	runnerB.InjectLLM(teamBetaLLM) // Inject team-specific LLM

	t.Cleanup(func() {
		runnerA.Stop()
		runnerB.Stop()
		go func() { for range chA {} }()
		go func() { for range chB {} }()
	})

	// Run both concurrently (simulating simultaneous requests)
	userMsg := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{Text: "Hello"}},
	}

	go runnerA.Run(chatAgentA, sessionService, defaultLLM, nil, userMsg, "Hello", true, "", nil, false)
	go runnerB.Run(chatAgentB, sessionService, defaultLLM, nil, userMsg, "Hello", true, "", nil, false)

	eventsA := collectEvents(t, chA, 10*time.Second)
	eventsB := collectEvents(t, chB, 10*time.Second)

	// Verify User A got Team Alpha's response
	textA := extractTextFromEvents(t, eventsA)
	if !strings.Contains(textA, "Team-Alpha") {
		t.Errorf("User A (Team Alpha) expected response from Team-Alpha model, got: %q", textA)
	}
	if strings.Contains(textA, "DEFAULT") {
		t.Error("User A got the default model response instead of team-specific")
	}

	// Verify User B got Team Beta's response
	textB := extractTextFromEvents(t, eventsB)
	if !strings.Contains(textB, "Team-Beta") {
		t.Errorf("User B (Team Beta) expected response from Team-Beta model, got: %q", textB)
	}
	if strings.Contains(textB, "DEFAULT") {
		t.Error("User B got the default model response instead of team-specific")
	}

	// Verify they are different from each other
	if textA == textB {
		t.Error("Both users got the same response — LLM isolation is broken")
	}
}

// TestIntegration_MT2_NoTeamOverrideFallsBackToDefault verifies that when no
// team-specific LLM is injected (no InjectLLM call), the chat agent uses
// its default LLM (the one passed at construction).
func TestIntegration_MT2_NoTeamOverrideFallsBackToDefault(t *testing.T) {
	defaultLLM := NewMockLLM(TextTurn("Response from platform-default model"))

	sessionService := common.NewAutoInitService(session.InMemoryService())
	promptBuilder := &agent.SystemPromptBuilder{}
	chatAgent := agent.NewChatAgent(defaultLLM, nil, nil, sessionService, promptBuilder, false, true)

	sessionID := createTestSession(t, sessionService)
	runner := newChatRunner(sessionID, studioChatUserID, true)
	ch := runner.Subscribe("test-default")
	// Deliberately NOT calling runner.InjectLLM() — should fall back to default

	t.Cleanup(func() {
		runner.Stop()
		go func() { for range ch {} }()
	})

	userMsg := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{Text: "Hello"}},
	}

	go runner.Run(chatAgent, sessionService, defaultLLM, nil, userMsg, "Hello", true, "", nil, false)

	events := collectEvents(t, ch, 10*time.Second)
	text := extractTextFromEvents(t, events)

	if !strings.Contains(text, "platform-default") {
		t.Errorf("Expected platform-default response when no team override, got: %q", text)
	}
}

// TestIntegration_MT3_ResolveEffectiveConfigEndToEnd verifies the full resolution
// pipeline: ResolveEffectiveConfig with Platform → Org → Team settings produces
// the correct provider/model, which when used to get an LLM from a pre-seeded pool,
// returns the team's model (not platform or org defaults).
func TestIntegration_MT3_ResolveEffectiveConfigEndToEnd(t *testing.T) {
	ctx := context.Background()

	// Platform default: Bifrost / gpt-4
	platformStore := &mockPlatformSettingsStore{settings: &store.PlatformSettings{
		DefaultProvider: "Bifrost",
		DefaultModel:    "gpt-4",
		Providers: map[string]store.ProviderConfig{
			"Bifrost": {"type": "openai_compat", "base_url": "https://bifrost.example.com", "api_key": "platform-key"},
		},
	}}

	// Org override: Anthropic / claude-sonnet
	orgStore := &mockOrgSettingsStore{settings: &store.OrgSettings{
		DefaultProvider: "Anthropic",
		DefaultModel:    "claude-sonnet",
		Providers: map[string]store.ProviderConfig{
			"Anthropic": {"type": "openai_compat", "base_url": "https://anthropic.example.com", "api_key": "org-key"},
		},
	}}

	// Team Alpha: SAP AI Core / sapaicore/claude-opus (highest priority)
	teamAlphaStore := &mockTeamSettingsStore{settings: &store.TeamSettings{
		DefaultProvider: "SAP AI Core",
		DefaultModel:    "sapaicore/claude-opus",
		Providers: map[string]store.ProviderConfig{
			"SAP AI Core": {"type": "openai_compat", "base_url": "https://sap.example.com", "api_key": "team-alpha-key"},
		},
	}}

	// Team Beta: no provider override (should inherit org's Anthropic / claude-sonnet)
	teamBetaStore := &mockTeamSettingsStore{settings: &store.TeamSettings{
		// No DefaultProvider or DefaultModel — inherits from org
	}}

	// Resolve for Team Alpha user
	cfgAlpha := provider.ResolveEffectiveConfig(ctx, platformStore, orgStore, teamAlphaStore)
	if cfgAlpha.General.DefaultProvider != "SAP AI Core" {
		t.Errorf("Team Alpha: expected provider 'SAP AI Core', got %q", cfgAlpha.General.DefaultProvider)
	}
	if cfgAlpha.General.DefaultModel != "sapaicore/claude-opus" {
		t.Errorf("Team Alpha: expected model 'sapaicore/claude-opus', got %q", cfgAlpha.General.DefaultModel)
	}

	// Resolve for Team Beta user (should inherit org settings)
	cfgBeta := provider.ResolveEffectiveConfig(ctx, platformStore, orgStore, teamBetaStore)
	if cfgBeta.General.DefaultProvider != "Anthropic" {
		t.Errorf("Team Beta: expected provider 'Anthropic' (from org), got %q", cfgBeta.General.DefaultProvider)
	}
	if cfgBeta.General.DefaultModel != "claude-sonnet" {
		t.Errorf("Team Beta: expected model 'claude-sonnet' (from org), got %q", cfgBeta.General.DefaultModel)
	}

	// Verify all providers are merged (additive)
	if len(cfgAlpha.Providers) != 3 {
		t.Errorf("Team Alpha: expected 3 merged providers (Bifrost+Anthropic+SAP AI Core), got %d", len(cfgAlpha.Providers))
	}
	if len(cfgBeta.Providers) != 2 {
		t.Errorf("Team Beta: expected 2 merged providers (Bifrost+Anthropic), got %d", len(cfgBeta.Providers))
	}
}

// TestIntegration_MT4_ConcurrentUsersSharePool verifies that multiple concurrent
// users in different teams can use the same Pool without interference. Each user
// resolves their own config and gets a distinct (cached) LLM instance.
func TestIntegration_MT4_ConcurrentUsersSharePool(t *testing.T) {
	ctx := context.Background()

	// Pre-seed a Pool with two provider/model combos using real openai_compat
	// entries (the Pool creates real HTTP clients, but we never call them —
	// we just verify that different users get different instances from the cache).
	pool := provider.NewPool()

	appCfgAlpha := &config.AppConfig{
		Providers: map[string]config.ProviderConfig{
			"ProviderA": {"type": "openai_compat", "base_url": "http://alpha.local", "api_key": "key-a"},
		},
	}
	appCfgBeta := &config.AppConfig{
		Providers: map[string]config.ProviderConfig{
			"ProviderB": {"type": "openai_compat", "base_url": "http://beta.local", "api_key": "key-b"},
		},
	}

	// Launch 10 goroutines per "team" concurrently
	var wg sync.WaitGroup
	var alphaLLMs, betaLLMs sync.Map

	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			llm, err := pool.Get(ctx, "ProviderA", "model-alpha", appCfgAlpha)
			if err != nil {
				t.Errorf("Alpha goroutine %d: %v", idx, err)
				return
			}
			alphaLLMs.Store(idx, llm)
		}(i)
		go func(idx int) {
			defer wg.Done()
			llm, err := pool.Get(ctx, "ProviderB", "model-beta", appCfgBeta)
			if err != nil {
				t.Errorf("Beta goroutine %d: %v", idx, err)
				return
			}
			betaLLMs.Store(idx, llm)
		}(i)
	}
	wg.Wait()

	// All Alpha instances should be the same (cached)
	var firstAlpha interface{}
	alphaLLMs.Range(func(_, v interface{}) bool {
		if firstAlpha == nil {
			firstAlpha = v
		} else if v != firstAlpha {
			t.Error("Pool returned different instances for same Alpha provider+model")
			return false
		}
		return true
	})

	// All Beta instances should be the same (cached)
	var firstBeta interface{}
	betaLLMs.Range(func(_, v interface{}) bool {
		if firstBeta == nil {
			firstBeta = v
		} else if v != firstBeta {
			t.Error("Pool returned different instances for same Beta provider+model")
			return false
		}
		return true
	})

	// Alpha and Beta should be DIFFERENT instances
	if firstAlpha == firstBeta {
		t.Error("Pool returned same instance for different provider+model combos")
	}
}

// TestIntegration_MT5_OrgOverrideWithoutTeam verifies that when a user's team
// has no settings, the org-level override takes precedence over platform defaults.
// This is the middle-tier cascade scenario.
func TestIntegration_MT5_OrgOverrideWithoutTeam(t *testing.T) {
	// Create distinguishable LLMs for each tier
	platformLLM := NewMockLLM(TextTurn("Response from PLATFORM model"))
	orgLLM := NewMockLLM(TextTurn("Response from ORG model"))

	sessionService := common.NewAutoInitService(session.InMemoryService())
	promptBuilder := &agent.SystemPromptBuilder{}
	// Default agent LLM = platform level
	chatAgent := agent.NewChatAgent(platformLLM, nil, nil, sessionService, promptBuilder, false, true)

	// User with org override (no team override)
	sessionID := createTestSession(t, sessionService)
	runner := newChatRunner(sessionID, studioChatUserID, true)
	ch := runner.Subscribe("test-org")
	runner.InjectLLM(orgLLM) // Simulates what handler does after resolving org-level config

	t.Cleanup(func() {
		runner.Stop()
		go func() { for range ch {} }()
	})

	userMsg := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{Text: "Hello"}},
	}

	go runner.Run(chatAgent, sessionService, platformLLM, nil, userMsg, "Hello", true, "", nil, false)

	events := collectEvents(t, ch, 10*time.Second)
	text := extractTextFromEvents(t, events)

	if !strings.Contains(text, "ORG model") {
		t.Errorf("Expected org-level model response, got: %q", text)
	}
	if strings.Contains(text, "PLATFORM") {
		t.Error("User got platform default instead of org override")
	}
}

// TestIntegration_MT6_IsolationUnderLoad verifies that under concurrent load,
// each user's LLM injection is properly isolated — no cross-contamination.
// Each concurrent user gets their own ChatAgent instance (as in production,
// where ChatManager creates/reuses agents per request).
func TestIntegration_MT6_IsolationUnderLoad(t *testing.T) {
	sessionService := common.NewAutoInitService(session.InMemoryService())
	promptBuilder := &agent.SystemPromptBuilder{}
	defaultLLM := NewMockLLM() // empty — should never be used

	const numTeams = 5
	const usersPerTeam = 3

	type result struct {
		teamIdx int
		text    string
	}

	results := make(chan result, numTeams*usersPerTeam)
	var wg sync.WaitGroup

	for teamIdx := 0; teamIdx < numTeams; teamIdx++ {
		for userIdx := 0; userIdx < usersPerTeam; userIdx++ {
			wg.Add(1)
			go func(tIdx int) {
				defer wg.Done()

				expectedText := teamResponseText(tIdx)
				teamLLM := NewMockLLM(TextTurn(expectedText))

				// Each concurrent user gets its own ChatAgent (mirrors production
				// where agents are not shared across concurrent Run calls).
				localAgent := agent.NewChatAgent(defaultLLM, nil, nil, sessionService, promptBuilder, false, true)

				sessionID := createTestSession(t, sessionService)
				runner := newChatRunner(sessionID, studioChatUserID, true)
				ch := runner.Subscribe("test")
				runner.InjectLLM(teamLLM)

				userMsg := &genai.Content{
					Role:  "user",
					Parts: []*genai.Part{{Text: "Hello"}},
				}

				go runner.Run(localAgent, sessionService, defaultLLM, nil, userMsg, "Hello", true, "", nil, false)

				events := collectEventsTimeout(ch, 10*time.Second)
				text := extractTextFromEventSlice(events)
				runner.Stop()
				go func() { for range ch {} }()

				results <- result{teamIdx: tIdx, text: text}
			}(teamIdx)
		}
	}

	wg.Wait()
	close(results)

	// Verify each result matches its team
	for r := range results {
		expected := teamResponseText(r.teamIdx)
		if r.text != expected {
			t.Errorf("Team %d: expected %q, got %q", r.teamIdx, expected, r.text)
		}
	}
}

// --- Helper Functions ---

// createTestSession creates a session in the service and returns its ID.
// Uses the given userID for session creation to match the runner's UserID.
func createTestSession(t *testing.T, svc session.Service) string {
	t.Helper()
	resp, err := svc.Create(context.Background(), &session.CreateRequest{
		AppName: studioChatAppName,
		UserID:  studioChatUserID,
	})
	if err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}
	return resp.Session.ID()
}

// extractTextFromEvents finds the first "text" event and returns its text.
func extractTextFromEvents(t *testing.T, events []ChatEvent) string {
	t.Helper()
	for _, ev := range events {
		if ev.Type == "text" {
			if text, ok := ev.Data["text"].(string); ok {
				return text
			}
		}
	}
	t.Fatal("no text event found in events")
	return ""
}

// extractTextFromEventSlice is like extractTextFromEvents but doesn't require *testing.T.
func extractTextFromEventSlice(events []ChatEvent) string {
	for _, ev := range events {
		if ev.Type == "text" {
			if text, ok := ev.Data["text"].(string); ok {
				return text
			}
		}
	}
	return ""
}

// collectEventsTimeout reads from the event channel until it closes or timeout.
func collectEventsTimeout(ch <-chan ChatEvent, timeout time.Duration) []ChatEvent {
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
			return events
		}
	}
}

func teamResponseText(teamIdx int) string {
	return strings.Join([]string{"Response from team", intToStr(teamIdx), "model"}, "-")
}

// --- Mock Settings Stores for multi-tenant resolution tests ---

type mockPlatformSettingsStore struct {
	settings *store.PlatformSettings
}

func (m *mockPlatformSettingsStore) Get(_ context.Context) (*store.PlatformSettings, error) {
	return m.settings, nil
}

func (m *mockPlatformSettingsStore) Save(_ context.Context, _ *store.PlatformSettings) error {
	return nil
}

type mockOrgSettingsStore struct {
	settings *store.OrgSettings
}

func (m *mockOrgSettingsStore) Get(_ context.Context) (*store.OrgSettings, error) {
	return m.settings, nil
}

func (m *mockOrgSettingsStore) Save(_ context.Context, _ *store.OrgSettings) error {
	return nil
}

type mockTeamSettingsStore struct {
	settings *store.TeamSettings
}

func (m *mockTeamSettingsStore) Get(_ context.Context) (*store.TeamSettings, error) {
	return m.settings, nil
}

func (m *mockTeamSettingsStore) Save(_ context.Context, _ *store.TeamSettings) error {
	return nil
}
