package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotPriorRunProviderBoundPrompt(t *testing.T) {
	id, runID := uuid.New(), uuid.New()
	req := bot.RunRequest{Bot: store.Bot{ID: id}, Run: store.BotRun{BotID: id, ID: runID, Trigger: store.TriggerCron, TriggerID: uuid.New(), CreatedAt: time.Now()}, Version: store.BotVersion{Version: 1}}
	req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: id, RunID: runID, Version: 1, Instructions: "Current pinned assignment"})
	prior := &bot.BotPriorRunContext{BotID: id, RunID: uuid.New(), TriggerID: req.Run.TriggerID, CreatedAt: req.Run.CreatedAt.Add(-time.Minute), OutcomeAt: req.Run.CreatedAt.Add(-time.Second), Status: store.RunFailed, FailureKind: store.BotReportFailureOperational}
	reads := 0
	ctx := bot.WithBotWorkServices(context.Background(), botPromptWorkReader{read: func(context.Context, bot.BotWorkReadRequest) (*bot.BotWorkReadResult, error) {
		reads++
		return &bot.BotWorkReadResult{Work: store.BuildBotWorkProjection(req.Work), PriorRun: prior}, nil
	}})
	ctx = WithPromptOverrides(ctx, NewBotPromptOverrides(nil, req))
	sessions := session.InMemoryService()
	llm := &ADKMockModel{Responses: []*genai.Content{genai.NewContentFromText("done", genai.RoleModel)}}
	chat := NewChatAgent(llm, nil, nil, sessions, &SystemPromptBuilder{}, false, false)
	root, err := adkagent.New(adkagent.Config{Name: "chat", Run: chat.Run})
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: "prior-run", Agent: root, SessionService: sessions, AutoCreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range r.Run(ctx, "owner", "new-session", genai.NewContentFromText("Current pinned assignment", genai.RoleUser), adkagent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if reads == 0 || len(llm.Requests) != 1 {
		t.Fatalf("not automatically refreshed: reads=%d calls=%d", reads, len(llm.Requests))
	}
	prompt := llm.Requests[0].Config.SystemInstruction.Parts[0].Text
	for _, want := range []string{prior.RunID.String(), `"prior_run"`, `"status":"failed"`, `"failure_kind":"operational"`, "distinct newly admitted run", "not a retry or replay", "current pinned assignment", "Do not replay uncertain writes or sends"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("provider prompt missing %q", want)
		}
	}
	for _, c := range llm.Requests[0].Contents {
		for _, part := range c.Parts {
			if strings.Contains(part.Text, prior.RunID.String()) {
				t.Fatal("prior metadata leaked into user-visible conversation")
			}
		}
	}
}

func TestBotPriorRunPromptRejectsForeignMetadata(t *testing.T) {
	id, runID := uuid.New(), uuid.New()
	req := bot.RunRequest{Bot: store.Bot{ID: id}, Run: store.BotRun{BotID: id, ID: runID, Trigger: store.TriggerCron, TriggerID: uuid.New(), CreatedAt: time.Now()}, Version: store.BotVersion{Version: 1}}
	req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: id, RunID: runID, Version: 1, Instructions: "Current assignment"})
	po := NewBotPromptOverrides(nil, req)
	b := &SystemPromptBuilder{}
	b.applyBotPrompt(po)
	prior := &bot.BotPriorRunContext{BotID: id, RunID: uuid.New(), TriggerID: uuid.New(), CreatedAt: req.Run.CreatedAt.Add(-time.Minute), OutcomeAt: req.Run.CreatedAt.Add(-time.Second), Status: store.RunFailed}
	ctx := bot.WithBotWorkServices(context.Background(), botPromptWorkReader{read: func(context.Context, bot.BotWorkReadRequest) (*bot.BotWorkReadResult, error) {
		return &bot.BotWorkReadResult{Work: store.BuildBotWorkProjection(req.Work), PriorRun: prior}, nil
	}})
	prompt := b.BuildBotPrompt(ctx, po)
	if strings.Contains(prompt, prior.RunID.String()) || !strings.Contains(prompt, "Working context is unavailable") || !strings.Contains(prompt, "Current assignment") {
		t.Fatalf("foreign=%v unavailable=%v assignment=%v", strings.Contains(prompt, prior.RunID.String()), strings.Contains(prompt, "Working context is unavailable"), strings.Contains(prompt, "Current assignment"))
	}
}
