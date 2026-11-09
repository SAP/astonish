package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
)

func TestBotBudgetClassification(t *testing.T) {
	for _, dimension := range []string{store.BotQuotaRunTokens, store.BotQuotaTotalTokens} {
		cause := &store.BotMandateQuotaError{Dimension: dimension, Used: 2, Requested: 9, Limit: 10}
		result, err := botChatFailure(bot.ErrorPermanent, errors.Join(errors.New("wrapper must stay private"), fmt.Errorf("quota: %w", cause)))
		var got *store.BotMandateQuotaError
		if !errors.As(err, &got) || got != cause || !errors.Is(err, store.ErrBotForbidden) || result.Failure.Class != bot.ErrorBudget || result.Report.Failure == nil {
			t.Fatalf("lost classification %+v %v", result, err)
		}
		if strings.Contains(result.Report.Failure.TechnicalDetails, "private") {
			t.Fatal("wrapper leaked")
		}
	}
	result, _ := botChatFailure(bot.ErrorPermanent, errors.New(bot.ErrBudgetExceeded.Error()))
	if result.Failure.Class != bot.ErrorPermanent || result.Report.Failure != nil {
		t.Fatal("text classified as budget")
	}
}

func TestBotBudgetFirstReservation(t *testing.T) {
	ctx, req, ss := botChatTestRequest(t)
	limit := int64(71)
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &limit}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req.Budget = meter
	llm := NewMockLLM(TextTurn("must not execute"))
	budgeted, err := provider.NewBudgetLLM(llm, meter, provider.BudgetLLMOptions{MaxOutputTokens: 10, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 80, nil }})
	if err != nil {
		t.Fatal(err)
	}
	var cleaned atomic.Int64
	adapter, _ := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, budgeted, nil, &cleaned))
	var events []bot.RuntimeEvent
	result, err := adapter.Start(ctx, req, func(_ context.Context, e bot.RuntimeEvent) error { events = append(events, e); return nil })
	if !errors.Is(err, bot.ErrBudgetExceeded) || result.Status != store.RunFailed || result.Failure.Class != bot.ErrorBudget {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, text := range []string{"run budget: 71 tokens", "Used or reserved: 0 tokens", "80 tokens"} {
		if !strings.Contains(result.Report.Summary, text) {
			t.Errorf("missing %q: %s", text, result.Report.Summary)
		}
	}
	for _, e := range events {
		if e.Type == "error" {
			t.Errorf("raw error: %+v", e)
		}
	}
	stored, err := ss.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	for e := range stored.Session.Events().All() {
		if e.Content != nil {
			for _, p := range e.Content.Parts {
				if strings.Contains(p.Text, "[Error:") {
					t.Errorf("persisted raw error %s", p.Text)
				}
			}
		}
	}
	if len(llm.Calls) != 0 || meter.Usage().InputTokens != 0 || meter.Usage().OutputTokens != 0 {
		t.Fatal("denied reservation claimed usage")
	}
	if len(events) == 0 || events[len(events)-1].Type != "done" {
		t.Fatal("missing done")
	}
}
