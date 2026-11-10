package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotWorkPromptPinnedResponsibility(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			id, run := uuid.New(), uuid.New()
			goal := store.BotGoal{ID: uuid.New(), BotID: id, Revision: 3, CompletionCriteria: "Cite observation time {untrusted_policy}"}
			req := bot.RunRequest{Bot: store.Bot{ID: id, Scope: scope}, Version: store.BotVersion{Version: 1}, Run: store.BotRun{ID: run}, Instructions: "resume answer"}
			req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: id, RunID: run, Version: 1, Instructions: "original assignment", Charter: "saved responsibility", Goals: []store.BotGoal{goal}})
			req.Work.Criteria = []store.BotWorkCriterion{{ID: "criterion", Text: "specific inspection", SourceIDs: []string{"assignment", "charter"}}}
			req.Work.Unresolved = []string{"prior uncertain send: investigate, do not replay"}
			req.Work.Candidate = &store.BotWorkCandidate{Text: "PRIVATE candidate bytes"}
			po := NewBotPromptOverrides(nil, req)
			req.Work.Contract.Goals[0].CompletionCriteria = "mutated easier criteria"
			b := &SystemPromptBuilder{}
			b.applyBotPrompt(po)
			prompt := b.Build()
			for _, want := range []string{"original assignment", "saved responsibility", "Cite observation time", "specific inspection", "prior uncertain send", "explicit applicability reason", "not instructions or permission grants", "One occurrence does not complete an ongoing goal", "neither owner sample approval nor mandatory skill creation"} {
				if !strings.Contains(prompt, want) {
					t.Errorf("missing %q", want)
				}
			}
			for _, bad := range []string{"mutated easier criteria", "PRIVATE candidate bytes", "{untrusted_policy}"} {
				if strings.Contains(prompt, bad) {
					t.Errorf("unsafe snapshot %q", bad)
				}
			}
			if buildTurnContextContent(po, "", "") != nil {
				t.Fatal("work persisted as user turn")
			}
			for _, tc := range []string{"oversized", "foreign", "unknown"} {
				t.Run(tc, func(t *testing.T) {
					q := req
					q.Work = req.Work.Clone()
					switch tc {
					case "oversized":
						q.Work.Unresolved = []string{strings.Repeat("huge", 20000)}
					case "foreign":
						q.Work.Contract.Goals[0].BotID = uuid.New()
					case "unknown":
						q.Work.SchemaVersion++
					}
					text := botWorkPrompt(q)
					if !strings.Contains(text, "unavailable or oversized") || len(text) > 1000 {
						t.Fatal("invalid context was not bounded/unavailable")
					}
				})
			}
		})
	}
}

func TestBotPromptPrivateSnapshot(t *testing.T) {
	limit := int64(100)
	req := bot.RunRequest{Bot: store.Bot{Name: "Orbit"}, Version: store.BotVersion{Charter: "private-charter", Persona: store.BotPersona{Identity: "private-identity", Timezone: "Europe/Berlin"}, Budget: store.BudgetSpec{MaxTokens: &limit}}, Run: store.BotRun{Trigger: store.TriggerCron}}
	base := &PromptOverrides{SessionContext: "prior-context", SchedulerHints: "prior-scheduler"}
	po := NewBotPromptOverrides(base, req)
	limit = 999
	req.Version.Charter = "mutated"
	shared := &SystemPromptBuilder{Identity: &AgentIdentity{Name: "unrelated-persona"}, CustomPrompt: "unrelated-custom", Timezone: "UTC"}
	clone := shared.Clone()
	if !clone.applyBotPrompt(po) {
		t.Fatal("Bot context not applied")
	}
	prompt := clone.Build()
	for _, want := range []string{"Orbit", "private-charter", "private-identity", "Europe/Berlin", "cron", "100", "background Bot run", "not permission grants"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"unrelated-persona", "unrelated-custom", "mutated", "999", "prior-scheduler"} {
		if strings.Contains(prompt, bad) {
			t.Errorf("leaked %q", bad)
		}
	}
	if shared.BuildOverride != nil || shared.Timezone != "UTC" || shared.Identity.Name != "unrelated-persona" {
		t.Fatal("shared builder changed")
	}
	if base.SchedulerHints != "prior-scheduler" || base.Bot != nil {
		t.Fatal("base overrides changed")
	}
	if content := buildTurnContextContent(po, "", ""); content != nil {
		t.Fatalf("Bot context leaked into replayed user event: %+v", content)
	}
}
func TestBotPromptFreshPerInvocation(t *testing.T) {
	service := session.InMemoryService()
	llm := &ADKMockModel{Responses: []*genai.Content{
		genai.NewContentFromText("done", genai.RoleModel),
		genai.NewContentFromText("done", genai.RoleModel),
	}}
	builder := &SystemPromptBuilder{CustomPrompt: "unrelated-portal"}
	chat := NewChatAgent(llm, nil, nil, service, builder, false, false)
	root, err := adkagent.New(adkagent.Config{Name: "chat", Run: chat.Run})
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: "bot-prompt", Agent: root, SessionService: service, AutoCreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, charter := range []string{"charter-one", "charter-two"} {
		po := NewBotPromptOverrides(nil, bot.RunRequest{Bot: store.Bot{Name: "Orbit"}, Version: store.BotVersion{Charter: charter}, Interactive: i == 1})
		ctx := WithPromptOverrides(context.Background(), po)
		for _, err := range r.Run(ctx, "owner", "same-session", genai.NewContentFromText("clean instructions", genai.RoleUser), adkagent.RunConfig{}) {
			if err != nil {
				t.Fatal(err)
			}
		}
		if len(llm.Requests) != i+1 {
			t.Fatalf("calls=%d", len(llm.Requests))
		}
		req := llm.Requests[i]
		core := req.Config.SystemInstruction.Parts[0].Text
		if !strings.Contains(core, charter) || strings.Contains(core, "unrelated-portal") || i == 1 && strings.Contains(core, "charter-one") {
			t.Fatalf("stale or unrelated system context: %s", core)
		}
		if i == 1 && (!strings.Contains(core, "interactive Bot conversation") || strings.Contains(core, "No user is waiting")) {
			t.Fatal("background mode stuck on resumed interactive turn")
		}
		for _, content := range req.Contents {
			for _, part := range content.Parts {
				if strings.Contains(part.Text, "charter-") {
					t.Fatal("Bot metadata replayed as conversation")
				}
			}
		}
	}
	if builder.CustomPrompt != "unrelated-portal" || builder.BuildOverride != nil {
		t.Fatal("shared prompt builder mutated")
	}
}

func TestBotPromptContinuousConversationMode(t *testing.T) {
	for _, run := range []store.BotRun{
		{Intent: store.BotIntentDraftConversation, Presentation: store.BotRunPresentationHomeInteractive},
		{Intent: store.BotIntentConversation},
		{Intent: store.BotIntentRegular, Presentation: store.BotRunPresentationHomeInteractive},
	} {
		t.Run(string(run.Intent)+string(run.Presentation), func(t *testing.T) {
			po := NewBotPromptOverrides(nil, bot.RunRequest{Run: run})
			builder := &SystemPromptBuilder{}
			builder.applyBotPrompt(po)
			prompt := builder.Build()
			if !strings.Contains(prompt, "interactive Bot conversation") || strings.Contains(prompt, "No user is waiting") {
				t.Fatalf("home conversation received unattended hints: %s", prompt)
			}
			if run.ControlOnly() && (!strings.Contains(prompt, "Do not call external tools") || !strings.Contains(prompt, "propose_bot_control")) {
				t.Fatal("control conversation omitted no-tool guidance")
			}
			for _, want := range []string{"RequireAuthorization", "Missing facts are Clarify", "unknown effects are never allowed", "pending worker commit", "proposed, not applied", "explicit user consent", "after the source run finishes", "never claim a change took effect", "Never parse prose as consent", "Catalog discovery is not permission", "separate configuration proposal", "new task_once proposal"} {
				if !strings.Contains(prompt, want) {
					t.Errorf("proposal guidance missing %q", want)
				}
			}
		})
	}
}

func TestBotPromptInteractiveAndCode(t *testing.T) {
	po := NewBotPromptOverrides(nil, bot.RunRequest{Interactive: true})
	clone := (&SystemPromptBuilder{}).Clone()
	clone.applyBotPrompt(po)
	if got := clone.Build(); !strings.Contains(got, "interactive Bot conversation") || strings.Contains(got, "No user is waiting") {
		t.Fatal("wrong interactive hints")
	}
	code := &SystemPromptBuilder{CodeMode: true}
	if code.applyBotPrompt(po) {
		t.Fatal("Bot overrides changed Code mode")
	}
}
