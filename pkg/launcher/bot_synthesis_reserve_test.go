package launcher

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

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
	research, final, err := r.prepare(context.Background(), request)
	if err != nil || final || len(research.Config.Tools) != 1 {
		t.Fatalf("research=%v final=%v err=%v", research, final, err)
	}
	if !strings.Contains(research.Contents[len(research.Contents)-1].Parts[0].Text, "remaining") {
		t.Fatal("model cannot see remaining budget")
	}
	now = meter.Deadline().Add(-r.reserveTime / 2)
	answer, final, err := r.prepare(context.Background(), request)
	if err != nil || !final || len(answer.Config.Tools) != 0 || len(answer.Tools) != 0 {
		t.Fatalf("final=%v err=%v request=%+v", final, err, answer)
	}
	if len(request.Contents) != 1 || len(request.Config.Tools) != 1 {
		t.Fatal("mutated caller request")
	}
	now = now.Add(-time.Minute)
	if _, final, _ = r.prepare(context.Background(), request); !final {
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
	_, final, err := r.prepare(context.Background(), req)
	if err != nil || !final {
		t.Fatalf("token reserve not protected final=%v err=%v", final, err)
	}
	if usage := meter.Usage(); usage.InputTokens != 5000 || usage.OutputTokens != 0 || usage.CostMicros != nil {
		t.Fatalf("fabricated usage: %+v", usage)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.prepare(canceled, req); err != context.Canceled {
		t.Fatalf("cancellation bypassed: %v", err)
	}
}
