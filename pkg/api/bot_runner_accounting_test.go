package api

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotChatRequestAcceptsZeroHistoricRuntime(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	req.Version.Budget.MaxRuntimeSeconds = 0
	sink := func(context.Context, bot.RuntimeEvent) error { return nil }
	if err := validateBotChatRequest(ctx, req, sink); err != nil {
		t.Fatalf("zero historic runtime rejected: %v", err)
	}
	req.Version.Budget.MaxRuntimeSeconds = -1
	if !errors.Is(validateBotChatRequest(ctx, req, sink), store.ErrBotInvalid) {
		t.Fatal("negative historic runtime accepted")
	}
	req.Version.Budget.MaxRuntimeSeconds = 0
	req.Run.Lease.ExpiresAt = time.Now().Add(-time.Second)
	if !errors.Is(validateBotChatRequest(ctx, req, sink), store.ErrBotConflict) {
		t.Fatal("zero historic runtime bypassed expired lease")
	}
}

func TestBotChatZeroHistoricRuntimeDeliversReport(t *testing.T) {
	ctx, req, sessions := botChatTestRequest(t)
	req.Version.Budget.MaxRuntimeSeconds = 0
	req.Run.Usage = store.BotUsage{InputTokens: 100000, OutputTokens: 200000, ToolCalls: 1000, RuntimeNanos: int64(24 * time.Hour)}
	accounting, err := bot.RestoreRunAccounting(req.Version.Budget, time.Now(), req.Run.Usage)
	if err != nil {
		t.Fatal(err)
	}
	req.Budget = accounting
	llm := NewMockLLM(TextTurn("Source observation retained."))
	var cleaned atomic.Int64
	adapter, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, sessions, llm, nil, &cleaned))
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil || !result.Status.Terminal() || !strings.Contains(result.Report.Summary, "Source observation retained.") || len(llm.Calls) != 1 || cleaned.Load() != 1 {
		t.Fatalf("result=%+v err=%v calls=%d cleanup=%d", result, err, len(llm.Calls), cleaned.Load())
	}
	usage := accounting.Usage()
	if usage.InputTokens < req.Run.Usage.InputTokens || usage.OutputTokens < req.Run.Usage.OutputTokens || usage.ToolCalls != req.Run.Usage.ToolCalls || usage.RuntimeNanos < req.Run.Usage.RuntimeNanos || usage.CostMicros != nil {
		t.Fatalf("historic accounting lost: %+v", usage)
	}
}
