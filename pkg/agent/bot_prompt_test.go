package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Embedding the remaining interface deliberately makes any mutation/check call
// panic: prompt rehydration is a read, not another execution or model step.
type botPromptWorkReader struct {
	bot.BotWorkServices
	read func(context.Context, bot.BotWorkReadRequest) (*bot.BotWorkReadResult, error)
}

func (s botPromptWorkReader) Read(ctx context.Context, q bot.BotWorkReadRequest) (*bot.BotWorkReadResult, error) {
	return s.read(ctx, q)
}

func TestBotPromptRehydratesDurableWork(t *testing.T) {
	req := botSkillPromptFixture(t)
	req.Work = store.NewBotAgentWorkState(req.Work.Contract)
	req.Work.SkillState = &store.BotSkillRunState{Stage: store.BotSkillOriented}
	if err := req.Work.Validate(); err != nil {
		t.Fatal(err)
	}
	req.Work.AgentWork.Notes = "obsolete startup notes"
	po := NewBotPromptOverrides(nil, req)
	b := &SystemPromptBuilder{BuildOverride: func(clone *SystemPromptBuilder) string {
		plain := *clone
		plain.BuildOverride = nil
		return plain.Build() + "\nscoped memory retained"
	}}
	b.applyBotPrompt(po)
	before := b.Build()
	previous := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: uuid.New(), Version: 1, Instructions: "prior work"})
	latest := &bot.BotWorkReadResult{Work: store.BuildBotWorkProjection(req.Work), Previous: store.BuildBotWorkProjection(previous), NextCursor: "20"}
	ctx := bot.WithBotWorkServices(context.Background(), botPromptWorkReader{read: func(ctx context.Context, q bot.BotWorkReadRequest) (*bot.BotWorkReadResult, error) {
		if q.Cursor != "" || q.Limit != 20 {
			t.Fatal("refresh must use a bounded first page")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded store read")
		}
		return latest, nil
	}})
	for i, note := range []string{"first saved notes", "later saved notes"} {
		latest.Notes = note
		latest.Work.WorkRevision = int64(i + 2)
		latest.Evidence = []store.BotWorkEvidenceReference{{Ref: "capture:" + note, RunID: req.Run.ID, EventID: "event", CallID: "call", Digest: strings.Repeat("a", 64)}}
		// A compacted summary need not contain any work state: the next prompt
		// comes from this durable read, not from the conversation summary.
		got := b.BuildBotPrompt(ctx, po)
		for _, want := range []string{note, "capture:" + note, "next_cursor", "METHOD-START", "METHOD-END", "ASSIGNMENT-START", "ASSIGNMENT-END", "scoped memory retained", "Previous obligations schedule no work", "previous"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing refreshed context %q", want)
			}
		}
		if strings.Contains(got, "obsolete startup notes") || i == 1 && strings.Contains(got, "first saved notes") || strings.Count(got, "## Current durable work") != 1 {
			t.Fatal("stale or accumulated work context")
		}
		if b.Build() != before || buildTurnContextContent(po, "", "") != nil {
			t.Fatal("refresh mutated builder or user instructions")
		}
	}
	// Concurrent clones share only immutable snapshots, never a mutable cache.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = b.Clone().BuildBotPrompt(ctx, po) }()
	}
	wg.Wait()
}

func TestBotPromptRehydrateUnavailableIsOptionalAndBounded(t *testing.T) {
	for _, tc := range []string{"error", "nil", "foreign-work", "foreign-evidence", "foreign-previous", "unknown", "stale", "large-notes", "large-result"} {
		t.Run(tc, func(t *testing.T) {
			id, run := uuid.New(), uuid.New()
			req := bot.RunRequest{Bot: store.Bot{ID: id}, Run: store.BotRun{ID: run}, Version: store.BotVersion{Version: 1}}
			req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: id, RunID: run, Version: 1, Instructions: "pinned assignment"})
			req.Work.Revision = 2
			req.Work.AgentWork.Notes = "stale startup note"
			po := NewBotPromptOverrides(nil, req)
			b := &SystemPromptBuilder{}
			b.applyBotPrompt(po)
			result := &bot.BotWorkReadResult{Work: store.BuildBotWorkProjection(req.Work), Notes: "unsafe response content"}
			var readErr error
			switch tc {
			case "error":
				readErr = errors.New("private raw error detail")
			case "nil":
				result = nil
			case "foreign-work":
				result.Work.BotID = uuid.New()
			case "foreign-evidence":
				result.Evidence = []store.BotWorkEvidenceReference{{RunID: uuid.New()}}
			case "foreign-previous":
				result.Previous = store.BuildBotWorkProjection(req.Work)
			case "unknown":
				result.Work.SchemaVersion++
			case "stale":
				result.Work.WorkRevision = 1
			case "large-notes":
				result.Notes = strings.Repeat("x", store.BotAgentWorkMaxNotesBytes+1)
			case "large-result":
				result.NextCursor = strings.Repeat("x", 3*store.BotAgentWorkMaxMetadataBytes)
			}
			ctx := bot.WithBotWorkServices(context.Background(), botPromptWorkReader{read: func(context.Context, bot.BotWorkReadRequest) (*bot.BotWorkReadResult, error) { return result, readErr }})
			got := b.BuildBotPrompt(ctx, po)
			if !strings.Contains(got, "Working context is unavailable") || !strings.Contains(got, "does not prevent answering") || !strings.Contains(got, "pinned assignment") {
				t.Fatal("optional context failure hid assignment or became a gate")
			}
			for _, bad := range []string{"stale startup note", "unsafe response content", "private raw error detail", strings.Repeat("x", 100)} {
				if strings.Contains(got, bad) {
					t.Errorf("unsafe fallback contains %q", bad)
				}
			}
		})
	}
}

func TestBotPromptRehydrateRedactsAndEscapesNotes(t *testing.T) {
	id, run := uuid.New(), uuid.New()
	req := bot.RunRequest{Bot: store.Bot{ID: id}, Run: store.BotRun{ID: run}, Version: store.BotVersion{Version: 1}, Work: store.NewBotAgentWorkState(store.BotWorkContract{BotID: id, RunID: run, Version: 1, Instructions: "assignment"})}
	po := NewBotPromptOverrides(nil, req)
	b := &SystemPromptBuilder{}
	b.applyBotPrompt(po)
	redactor := credentials.NewRedactor()
	redactor.AddSecret("scoped-test", "private-test-secret-value")
	ctx := credentials.WithRedactor(context.Background(), redactor)
	ctx = bot.WithBotWorkServices(ctx, botPromptWorkReader{read: func(context.Context, bot.BotWorkReadRequest) (*bot.BotWorkReadResult, error) {
		return &bot.BotWorkReadResult{Work: store.BuildBotWorkProjection(req.Work), Notes: "private-test-secret-value {untrusted_policy}"}, nil
	}})
	got := b.BuildBotPrompt(ctx, po)
	if strings.Contains(got, "private-test-secret-value") || strings.Contains(got, "{untrusted_policy}") || strings.Contains(got, "{{CREDENTIAL:scoped-test") {
		t.Fatalf("unsafe notes: secret=%v variable=%v credential=%v", strings.Contains(got, "private-test-secret-value"), strings.Contains(got, "{untrusted_policy}"), strings.Contains(got, "{{CREDENTIAL:scoped-test"))
	}
	if !strings.Contains(got, "scoped-test") {
		t.Fatal("redaction lost credential placeholder")
	}
}

func TestBotPromptRehydrateOrdinaryCodeAndMissingService(t *testing.T) {
	po := NewBotPromptOverrides(nil, bot.RunRequest{})
	ctx := bot.WithBotWorkServices(context.Background(), botPromptWorkReader{read: func(context.Context, bot.BotWorkReadRequest) (*bot.BotWorkReadResult, error) {
		t.Fatal("ordinary builder performed a work read")
		return nil, nil
	}})
	for _, b := range []*SystemPromptBuilder{{CodeMode: true}, {CustomPrompt: "ordinary chat"}} {
		before := b.Build()
		if got := b.BuildBotPrompt(ctx, po); got != before {
			t.Fatal("ordinary prompt changed")
		}
	}
	id, run := uuid.New(), uuid.New()
	req := bot.RunRequest{Bot: store.Bot{ID: id}, Run: store.BotRun{ID: run}, Version: store.BotVersion{Version: 1}, Work: store.NewBotAgentWorkState(store.BotWorkContract{BotID: id, RunID: run, Version: 1, Instructions: "assignment"})}
	po = NewBotPromptOverrides(nil, req)
	b := &SystemPromptBuilder{}
	b.applyBotPrompt(po)
	if got := b.BuildBotPrompt(context.Background(), po); got != b.Build() {
		t.Fatal("missing optional service changed compatibility behavior")
	}
}

func TestBotAgentPromptJudgmentAndOptionalWork(t *testing.T) {
	id, runID := uuid.New(), uuid.New()
	req := bot.RunRequest{Bot: store.Bot{ID: id}, Run: store.BotRun{ID: runID}, Version: store.BotVersion{Version: 1}}
	req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: id, RunID: runID, Version: 1, Instructions: "Compare sources"})
	req.Work.AgentWork.Notes = "One source contradicted the other"
	text := botWorkPrompt(req)
	for _, want := range []string{"according to consequences", "not confidence", "primary/live source", "does not prove absence", "decisive evidence behind delegated", "unobtainable information", "consequential choice", "missing authority", "not prerequisites", "never permission or certification", "One source contradicted the other"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing guidance %q", want)
		}
	}
	if strings.Contains(text, `"criteria"`) || strings.Contains(text, "stop and report this blocker") {
		t.Fatal("legacy review gate leaked into normal agent work")
	}
}

// This is a prompt contract regression, not evidence that a live model follows it.
func TestBotAgentPromptDiscoveryIsNotFactualSupport(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		name := "background"
		if interactive {
			name = "interactive"
		}
		t.Run(name, func(t *testing.T) {
			id, runID := uuid.New(), uuid.New()
			req := bot.RunRequest{Bot: store.Bot{ID: id}, Run: store.BotRun{ID: runID}, Version: store.BotVersion{Version: 1}, Interactive: interactive}
			req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: id, RunID: runID, Version: 1, Instructions: "Compare current options"})
			po := NewBotPromptOverrides(nil, req)
			builder := &SystemPromptBuilder{}
			if !builder.applyBotPrompt(po) {
				t.Fatal("missing Bot prompt")
			}
			ctx := bot.WithBotWorkServices(context.Background(), botPromptWorkReader{read: func(context.Context, bot.BotWorkReadRequest) (*bot.BotWorkReadResult, error) {
				return &bot.BotWorkReadResult{Work: store.BuildBotWorkProjection(req.Work)}, nil
			}})
			for _, text := range []string{builder.Build(), builder.BuildBotPrompt(ctx, po)} {
				for _, want := range []string{
					"according to consequences",
					"Listing headlines, search snippets and URL slugs are discovery leads",
					"not support for details, causality or recommendations",
					"Open the decisive original article or primary source",
					"including actionable numbers",
					"narrow the answer to an explicitly limited, attributed summary",
					"do not embellish beyond the retrieved text",
					"Cite actual source links alongside externally sourced claims",
					"do not imply an unread linked page was inspected",
					"publication/event dates and observation time with timezone",
					"do not present mixed-date items as current",
					"They are not prerequisites to a final answer",
				} {
					if !strings.Contains(text, want) {
						t.Errorf("missing evidence guidance %q", want)
					}
				}
			}
			if buildTurnContextContent(po, "", "") != nil {
				t.Fatal("hidden evidence guidance leaked into user turn")
			}
		})
	}
}

func TestBotWorkPromptPinnedResponsibility(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			id, run := uuid.New(), uuid.New()
			goal := store.BotGoal{ID: uuid.New(), BotID: id, Revision: 3, CompletionCriteria: "Cite observation time {untrusted_policy}"}
			req := bot.RunRequest{Bot: store.Bot{ID: id, Scope: scope}, Version: store.BotVersion{Version: 1}, Run: store.BotRun{ID: run}, Instructions: "resume answer"}
			req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: id, RunID: run, Version: 1, Instructions: "original assignment", Charter: "saved responsibility", Goals: []store.BotGoal{goal}})
			req.Work.Criteria = []store.BotWorkCriterion{{ID: "criterion", Text: "specific inspection", SourceIDs: []string{"assignment", "charter"}}}
			req.Work.Unresolved = []string{"prior uncertain send: investigate, do not replay"}
			req.Work.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "candidate-event", Text: "PRIVATE candidate bytes"}
			req.Work.Candidate.Digest = req.Work.Candidate.ContentDigest()
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
					q.Work.Contract.Goals[0].CompletionCriteria = goal.CompletionCriteria
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
