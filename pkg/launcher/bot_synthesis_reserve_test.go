package launcher

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

func TestBotSynthesisReserveAccountingIgnoresHistoricCaps(t *testing.T) {
	limit := int64(1)
	spec := store.BudgetSpec{MaxRuntimeSeconds: 1, MaxTokens: &limit, MaxToolCalls: &limit, MaxModelCostMicros: &limit}
	meter, err := bot.RestoreRunAccounting(spec, time.Now(), store.BotUsage{InputTokens: 50000, OutputTokens: 50000, ToolCalls: 13, RuntimeNanos: int64(6 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	r := newBotSynthesisReserve(meter, spec)
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("Continue the assignment", "user")}, Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "web_fetch"}}}}, MaxOutputTokens: 16384}}
	out, final, err := r.prepare(context.Background(), req, nil)
	if err != nil || final || len(out.Config.Tools) != 1 || out.Config.MaxOutputTokens != req.Config.MaxOutputTokens {
		t.Fatalf("historic caps triggered synthesis: final=%v err=%v request=%+v", final, err, out)
	}
	if len(out.Contents) != len(req.Contents) {
		t.Fatal("accounting-only execution received budget stopping guidance")
	}
	base, _ := bot.WithToolOutcomes(context.Background())
	ctx := botFactoryHandoffContext{base: base, functionCallID: "accounted-read"}
	binding := &botFactoryBinding{synthesisReserve: r}
	target := &accountingSynthesisProbe{}
	if result, err := binding.runSynthesisTool(ctx, target, nil); err != nil || result["status"] != "completed" || target.calls != 1 {
		t.Fatalf("accounting blocked tool dispatch: result=%v calls=%d err=%v", result, target.calls, err)
	}
	if outcome, ok := bot.TakeToolOutcome(base, ctx.FunctionCallID()); ok {
		t.Fatalf("accounting minted nonexecution receipt: %+v", outcome)
	}
	r.authorityDeadline = time.Now().Add(time.Minute)
	out, final, err = r.prepare(context.Background(), req, nil)
	if err != nil || final || len(out.Contents) != len(req.Contents)+1 {
		t.Fatalf("authority guidance missing: final=%v err=%v request=%+v", final, err, out)
	}
	guidance := out.Contents[len(out.Contents)-1].Parts[0].Text
	if !strings.Contains(guidance, "authority deadline") || strings.Contains(guidance, "tokens") || strings.Contains(guidance, "budget") {
		t.Fatalf("authority guidance implied execution quota: %q", guidance)
	}
	r.reserveTime = 20 * time.Second
	r.now = func() time.Time { return r.authorityDeadline.Add(-10 * time.Second) }
	out, final, err = r.prepare(context.Background(), req, nil)
	if err != nil || !final || len(out.Config.Tools) != 0 {
		t.Fatalf("accounting bypassed authority synthesis: final=%v err=%v request=%+v", final, err, out)
	}
	if strings.Contains(out.Contents[len(out.Contents)-1].Parts[0].Text, "budget") {
		t.Fatal("authority-only final synthesis resurrected budget guidance")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.prepare(canceled, req, nil); err != context.Canceled {
		t.Fatalf("cancellation bypassed: %v", err)
	}
}

type accountingSynthesisProbe struct {
	botRunnableTool
	calls int
}

func (*accountingSynthesisProbe) Name() string { return "web_fetch" }
func (p *accountingSynthesisProbe) Run(_ tool.Context, _ any) (map[string]any, error) {
	p.calls++
	return map[string]any{"status": "completed"}, nil
}

func TestBotSynthesisReserveAuthorityDeadline(t *testing.T) {
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := newBotSynthesisReserve(meter, store.BudgetSpec{MaxRuntimeSeconds: 300})
	now := time.Now()
	r.authorityDeadline = now.Add(100 * time.Second)
	r.reserveTime = 20 * time.Second
	r.now = func() time.Time { return now }
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("Research", "user")}, Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "web_fetch"}}}}}}
	if _, final, err := r.prepare(context.Background(), req, nil); err != nil || final {
		t.Fatalf("premature authority reserve final=%v err=%v", final, err)
	}
	now = r.authorityDeadline.Add(-10 * time.Second)
	out, final, err := r.prepare(context.Background(), req, nil)
	if err != nil || !final || len(out.Config.Tools) != 0 {
		t.Fatalf("authority reserve not enforced final=%v err=%v out=%+v", final, err, out)
	}
}

func TestBotSynthesisReserveResearchThenFinal(t *testing.T) {
	limit := int64(100000)
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300, MaxTokens: &limit}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := newBotSynthesisReserve(meter, store.BudgetSpec{MaxRuntimeSeconds: 300, MaxTokens: &limit})
	now := time.Now()
	r.now = func() time.Time { return now }
	request := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("Research the assignment", "user")}, Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "web_fetch"}}}}}}
	research, final, err := r.prepare(context.Background(), request, nil)
	if err != nil || final || len(research.Config.Tools) != 1 {
		t.Fatalf("research=%v final=%v err=%v", research, final, err)
	}
	if !strings.Contains(research.Contents[len(research.Contents)-1].Parts[0].Text, "remaining") {
		t.Fatal("model cannot see remaining budget")
	}
	now = meter.Deadline().Add(-r.reserveTime / 2)
	answer, final, err := r.prepare(context.Background(), request, nil)
	if err != nil || !final || len(answer.Config.Tools) != 0 || len(answer.Tools) != 0 {
		t.Fatalf("final=%v err=%v request=%+v", final, err, answer)
	}
	if len(request.Contents) != 1 || len(request.Config.Tools) != 1 {
		t.Fatal("mutated caller request")
	}
	now = now.Add(-time.Minute)
	if _, final, _ = r.prepare(context.Background(), request, nil); !final {
		t.Fatal("synthesis transition reversed")
	}
}

func TestBotSynthesisReserveTokenPressureAndCancellation(t *testing.T) {
	limit := int64(10000)
	spec := store.BudgetSpec{MaxRuntimeSeconds: 300, MaxTokens: &limit}
	meter, err := bot.RestoreRunBudget(spec, time.Now(), store.BotUsage{InputTokens: 5000, ToolCalls: 1})
	if err != nil {
		t.Fatal(err)
	}
	r := newBotSynthesisReserve(meter, spec)
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("Answer with retained evidence", "user")}}
	_, final, err := r.prepare(context.Background(), req, nil)
	if err != nil || !final {
		t.Fatalf("token reserve not protected final=%v err=%v", final, err)
	}
	if usage := meter.Usage(); usage.InputTokens != 5000 || usage.OutputTokens != 0 || usage.CostMicros != nil {
		t.Fatalf("fabricated usage: %+v", usage)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.prepare(canceled, req, nil); err != context.Canceled {
		t.Fatalf("cancellation bypassed: %v", err)
	}
}
