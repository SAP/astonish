package launcher

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
)

type progressDispatchProbe struct {
	began, settled int
	outcome        store.BotProgressDispatchOutcome
	evidence       string
	fail           error
}

func (p *progressDispatchProbe) Begin(context.Context, store.BotProgressDispatchKind, string, store.BotUsage) (*store.BotProgressDispatchReceipt, error) {
	p.began++
	if p.fail != nil {
		return nil, p.fail
	}
	return &store.BotProgressDispatchReceipt{DispatchID: uuid.New()}, nil
}
func (p *progressDispatchProbe) Settle(_ context.Context, _ uuid.UUID, o store.BotProgressDispatchOutcome, _ store.BotUsage, evidence string) error {
	p.evidence = evidence
	p.settled++
	p.outcome = o
	return nil
}
func (p *progressDispatchProbe) Boundary(context.Context) error { return nil }

type progressTestTool struct{ botRunnableTool }

func (progressTestTool) Name() string { return "unknown_write" }

func progressBindingForTest(p *progressDispatchProbe) *botFactoryBinding {
	ctx, _ := bot.WithToolOutcomes(context.Background())
	return &botFactoryBinding{owner: bot.WithProgressDispatch(ctx, p), progress: &botProgressBridge{}, req: bot.RunRequest{Version: store.BotVersion{Budget: store.BudgetSpec{RuntimePolicy: execution.NewBotProgressRuntimePolicy()}}}}
}
func TestBotProgressDispatchToolWaitsForPersistedBoundary(t *testing.T) {
	p := &progressDispatchProbe{}
	b := progressBindingForTest(p)
	// tool.Context metadata is needed only for receipt lookup.
	ctx := botBoundToolContext{Context: progressCallContext{base: b.owner}, owner: b.owner}
	_, err := b.progressTool(ctx, progressTestTool{}, map[string]any{}, func(c tool.Context, _ any) (map[string]any, error) {
		if p.began != 1 || p.settled != 0 {
			t.Fatal("execution before journal")
		}
		if _, ok := c.Deadline(); !ok {
			t.Fatal("no operation deadline")
		}
		if !errors.Is(b.progress.boundary(b.owner), bot.ErrProgressBoundaryUnsafe) {
			t.Fatal("live tool accepted as safe")
		}
		return map[string]any{"success": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.settled != 0 {
		t.Fatal("settled before persisted response")
	}
	if err = b.progress.boundary(b.owner); err != nil {
		t.Fatal(err)
	}
	if p.outcome != store.BotProgressDispatchUncertain {
		t.Fatal("response prose attested external effect")
	}
}

type progressCallContext struct {
	tool.Context
	base context.Context
}

func (c progressCallContext) Deadline() (time.Time, bool) { return c.base.Deadline() }
func (c progressCallContext) Done() <-chan struct{}       { return c.base.Done() }
func (c progressCallContext) Err() error                  { return c.base.Err() }
func (c progressCallContext) Value(k any) any             { return c.base.Value(k) }
func (progressCallContext) FunctionCallID() string        { return "call" }

type progressModelProbe struct {
	model.LLM
	p      *progressDispatchProbe
	called bool
}

func (p *progressModelProbe) GenerateContent(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(y func(*model.LLMResponse, error) bool) {
		p.called = true
		if p.p.began != 1 {
			panic("unrecorded provider")
		}
		y(&model.LLMResponse{}, nil)
	}
}
func TestBotProgressDispatchInteractiveNative(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
	f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
	f.req.Authorization = interactiveFactoryAuthorization{}
	f.req.Policy = nil
	f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
		return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &interactiveOutcomePool{node: &fileOutcomeNode{}}}, WorkspaceDir: t.TempDir(), VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
	}
	resources, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer resources.Cleanup()
	var target *interactiveBotTool
	for _, candidate := range resources.Agent.Tools {
		if candidate.Name() == "read_file" {
			target = candidate.(*interactiveBotTool)
		}
	}
	if target == nil {
		t.Fatal("missing native read")
	}
	p := &progressDispatchProbe{}
	target.binding.req.Version.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
	target.binding.owner = bot.WithProgressDispatch(target.binding.owner, p)
	target.binding.installProgressBridge()
	ctx := accountingToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: target.binding.owner, session: f.req.Run.SessionID.String()}}
	// Missing file still proves the pinned read completed, not an uncertain write.
	_, _ = target.Run(ctx, map[string]any{"path": "/nonexistent/progress-read"})
	if p.began != 1 || p.settled != 0 {
		t.Fatalf("premature/unrecorded dispatch: %+v", p)
	}
	if err := target.binding.progress.boundary(target.binding.owner); err != nil {
		t.Fatal(err)
	}
	if p.settled != 1 || p.outcome != store.BotProgressDispatchSettled {
		t.Fatalf("read classified as uncertain write: %+v", p)
	}
}

func TestBotProgressDispatchTrustedClassification(t *testing.T) {
	for _, receipt := range []bool{false, true} {
		p := &progressDispatchProbe{}
		b := progressBindingForTest(p)
		if !receipt {
			b.owner = agent.WithBotPolicyRuntime(b.owner, agent.BotPolicyRuntime{Classifier: BotCatalogueClassifier{}})
		}
		ctx := botBoundToolContext{Context: progressCallContext{base: b.owner}, owner: b.owner}
		_, _ = b.progressTool(ctx, progressNamedTool{name: "ask_user"}, nil, func(c tool.Context, _ any) (map[string]any, error) {
			if receipt {
				bot.RecordToolOutcome(c, c.FunctionCallID(), bot.ToolOutcome{Verified: true})
			}
			return nil, errors.New("transport returned an error")
		})
		if err := b.progress.boundary(b.owner); err != nil {
			t.Fatal(err)
		}
		if p.outcome != store.BotProgressDispatchSettled {
			t.Fatalf("receipt=%v outcome=%v", receipt, p.outcome)
		}
		wantEvidence := "trusted-read-dispatch:call"
		if receipt {
			wantEvidence = "tool-receipt:call"
		}
		if p.evidence != wantEvidence {
			t.Fatalf("receipt=%v evidence=%q want=%q", receipt, p.evidence, wantEvidence)
		}
	}
}

type progressNamedTool struct {
	botRunnableTool
	name string
}

func (t progressNamedTool) Name() string { return t.name }

func TestBotProgressDispatchCancellationDrainsChild(t *testing.T) {
	p := &progressDispatchProbe{}
	b := progressBindingForTest(p)
	lifetime, cancel := context.WithCancel(b.owner)
	ctx := botBoundToolContext{Context: progressCallContext{base: lifetime}, owner: b.owner}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_, _ = b.progressTool(ctx, progressTestTool{}, nil, func(c tool.Context, _ any) (map[string]any, error) {
			close(started)
			<-c.Done()
			<-release // a child must acknowledge cancellation before returning
			return nil, c.Err()
		})
	}()
	<-started
	cancel()
	if err := b.progress.boundary(b.owner); !errors.Is(err, bot.ErrProgressBoundaryUnsafe) {
		t.Errorf("live child accepted: %v", err)
	}
	select {
	case <-done:
		t.Error("returned while child still live")
	default:
	}
	close(release)
	<-done
	if err := b.progress.boundary(b.owner); err != nil {
		t.Fatal(err)
	}
	if p.outcome != store.BotProgressDispatchUncertain {
		t.Fatalf("cancellation proved nonexecution: %s", p.outcome)
	}
}

func TestBotProgressDispatchProviderFailClosed(t *testing.T) {
	p := &progressDispatchProbe{fail: store.ErrBotConflict}
	b := progressBindingForTest(p)
	inner := &progressModelProbe{p: p}
	llm := &botProgressLLM{LLM: inner, binding: b}
	for _, err := range llm.GenerateContent(b.owner, &model.LLMRequest{}, false) {
		if !errors.Is(err, store.ErrBotConflict) {
			t.Fatal(err)
		}
	}
	if inner.called {
		t.Fatal("provider dispatched despite journal failure")
	}
}
