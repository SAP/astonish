package launcher

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type synthesisJourneyModel struct {
	t           *testing.T
	calls       int
	reserve     *botSynthesisReserve
	retainKnown bool
}

func (*synthesisJourneyModel) Name() string { return "synthesis-journey" }
func (m *synthesisJourneyModel) GenerateContent(_ context.Context, q *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		text := ""
		for _, c := range q.Contents {
			for _, p := range c.Parts {
				text += p.Text
			}
		}
		if m.retainKnown && m.calls == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "read_file", Args: map[string]any{}}}}}}, nil)
			return
		}
		if m.calls == 1 || (m.retainKnown && m.calls == 2) {
			if strings.Contains(text, "Synthesis reserve has begun") {
				m.t.Fatal("premature synthesis")
			}
			if m.reserve != nil {
				m.reserve.reserveTime = time.Until(m.reserve.meter.Deadline()) - 100*time.Millisecond
			}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "read_file", Args: map[string]any{}}}}}}, nil)
			return
		}
		if !strings.Contains(text, "Synthesis reserve has begun") || len(q.Config.Tools) != 0 {
			m.t.Fatal("missing final-only request")
		}
		if m.reserve != nil {
			seen := false
			for _, c := range q.Contents {
				for _, p := range c.Parts {
					if p.FunctionResponse != nil && p.FunctionResponse.Response["status"] == "source_unavailable" {
						seen = true
					}
				}
			}
			// The helper-only test supplies its own textual receipt; the full
			// adapter must carry the actual function response into synthesis.
			if !seen && !strings.Contains(text, "Read interrupted; no source result established.") {
				m.t.Fatal("source-unavailable receipt lost before synthesis")
			}
		}
		if m.retainKnown {
			known := false
			for _, c := range q.Contents {
				for _, p := range c.Parts {
					if p.FunctionResponse != nil && p.FunctionResponse.Response["content"] == "Controlled observation: 17." {
						known = true
					}
				}
			}
			if !known {
				m.t.Fatal("exact established read result lost before synthesis")
			}
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Controlled observation: 17. Additional source unavailable.", "model"), TurnComplete: true}, nil)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Source unavailable; no findings established.", "model"), TurnComplete: true}, nil)
	}
}

type synthesisBlockingRead struct {
	botRunnableTool
	calls int
}

func (*synthesisBlockingRead) Name() string { return "read_file" }
func (*synthesisBlockingRead) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "read_file"}
}
func (s *synthesisBlockingRead) Run(ctx tool.Context, _ any) (map[string]any, error) {
	s.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

type synthesisWorkRepo struct {
	botFactoryTestRepo
	mu   sync.Mutex
	work *store.BotWorkState
}

func (r *synthesisWorkRepo) ListRuns(context.Context, store.BotID, store.BotPage) ([]store.BotRun, error) {
	return nil, nil
}

func (r *synthesisWorkRepo) LoadWork(_ context.Context, lease store.BotLease) (*store.BotWorkState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if lease != r.fixture.req.Run.Lease {
		return nil, fmt.Errorf("wrong work lease")
	}
	return r.work.Clone(), nil
}

func (r *synthesisWorkRepo) SaveWork(_ context.Context, update store.BotWorkUpdate) (*store.BotWorkState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if update.Lease != r.fixture.req.Run.Lease || update.ExpectedRevision != r.work.Revision {
		return nil, fmt.Errorf("wrong work lease or revision")
	}
	if err := store.ValidateBotWorkUpdate(r.work, &update.State); err != nil {
		return nil, err
	}
	r.work = update.State.Clone()
	r.work.Revision++
	return r.work.Clone(), nil
}

func TestBotSynthesisProductionAdapterFinalAnswer(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Run.Version, Instructions: f.req.Instructions, Charter: f.req.Version.Charter})
	f.req.Work.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	repo := &synthesisWorkRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}, work: f.req.Work.Clone()}
	f.services.Bots, f.services.PersonalBots = repo, repo
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.req.Budget = meter
	factory, err := NewBotChatAgentFactory(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw := &synthesisJourneyModel{t: t, retainKnown: true}
	readCalls := 0
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read a controlled unavailable source"}, func(ctx tool.Context, args map[string]any) (map[string]any, error) {
		readCalls++
		if readCalls == 1 {
			return map[string]any{"content": "Controlled observation: 17."}, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := api.NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*api.BotChatResources, error) {
		resources, err := factory(ctx, req)
		if err != nil {
			return nil, err
		}
		llm := resources.Agent.LLM.(*botFactoryLLM)
		raw.reserve = llm.binding.synthesisReserve
		boundRead, err := newBotBoundTool(read, llm.binding)
		if err != nil {
			return nil, err
		}
		resources.Agent.Tools = []tool.Tool{boundRead}
		llm.LLM = raw
		return resources, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.Text != "Controlled observation: 17. Additional source unavailable." {
		t.Fatalf("employee publication lost synthesis: %+v", result.Report.EmployeeMessage)
	}
	if err := result.Report.ValidateEmployeeMessage(result.Status, repo.work); err != nil {
		t.Fatalf("invalid employee publication: %v", err)
	}
	if result.Status != store.RunSucceeded || result.Report.Summary != "Controlled observation: 17. Additional source unavailable." {
		t.Fatalf("delivered result: %+v", result)
	}
	if repo.work.Candidate == nil || !repo.work.Candidate.ReadOnlyResult {
		t.Fatal("missing positively classified read-only candidate")
	}
	if raw.calls != 3 || readCalls != 2 {
		t.Fatalf("expected read then synthesis without reviewer: model=%d read=%d", raw.calls, readCalls)
	}
}
func TestBotSynthesisProductionFactoryReadThenAnswer(t *testing.T) {
	f := newBotFactoryFixture(t)
	limit := int64(100000)
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300, MaxTokens: &limit}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.req.Budget = meter
	f.req.Version.Budget.MaxTokens = nil
	got, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Cleanup()
	llm := got.Agent.LLM.(*botFactoryLLM)
	reserve := llm.binding.synthesisReserve
	if reserve.spec.MaxTokens == nil || *reserve.spec.MaxTokens != limit {
		t.Fatal("effective budget lost")
	}
	raw := &synthesisJourneyModel{t: t, reserve: reserve}
	llm.LLM = raw
	q := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("Read and summarize.", "user")}, Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "read_file"}}}}}}
	for response, err := range llm.GenerateContent(f.ctx, q, false) {
		if err != nil || !botResponseHasToolCall(response) {
			t.Fatalf("research: %v %v", response, err)
		}
	}
	if raw.calls != 1 {
		t.Fatal("extra upfront model calls")
	}
	reserve.reserveTime = time.Until(meter.Deadline()) - 20*time.Millisecond
	read := &synthesisBlockingRead{}
	ctx := botFactoryHandoffContext{base: context.Background(), session: f.req.Run.SessionID.String(), functionCallID: "read"}
	result, err := llm.binding.runSynthesisTool(ctx, read, map[string]any{})
	if err != nil || result["status"] != "source_unavailable" || read.calls != 1 {
		t.Fatalf("interruption: %v %v calls=%d", result, err, read.calls)
	}
	q.Contents = append(q.Contents, genai.NewContentFromText("Read interrupted; no source result established.", "user"))
	for response, err := range llm.GenerateContent(f.ctx, q, false) {
		if err != nil || response.Content.Parts[0].Text != "Source unavailable; no findings established." {
			t.Fatalf("answer: %v %v", response, err)
		}
	}
	result, err = llm.binding.runSynthesisTool(ctx, read, map[string]any{})
	if err != nil || result["status"] != "not_executed" || read.calls != 1 || raw.calls != 2 {
		t.Fatalf("late execution: %v %v", result, err)
	}
}
