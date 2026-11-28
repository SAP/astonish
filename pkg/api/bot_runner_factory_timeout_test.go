package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotChatRuntimeAdapterFactoryDependencyTimeout(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	nextCtx, nextReq, nextSessions := botChatTestRequest(t)
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 50 * time.Millisecond
	var cleaned atomic.Int64
	var factoryCalls int
	var factoryContextErr error
	llm := NewMockLLM(TextTurn("Fresh run completed."))
	adapter, err := NewBotChatRuntimeAdapter(func(factoryCtx context.Context, request bot.RunRequest) (*BotChatResources, error) {
		factoryCalls++
		if request.Run.ID == nextReq.Run.ID {
			return botChatTestFactory(t, nextSessions, llm, nil, &cleaned)(factoryCtx, request)
		}
		httpReq, err := http.NewRequestWithContext(factoryCtx, http.MethodGet, server.URL+"/private-dependency?token=private-token", nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(httpReq)
		if response != nil {
			response.Body.Close()
		}
		factoryContextErr = factoryCtx.Err()
		if err == nil {
			t.Error("dependency unexpectedly responded")
			return nil, nil
		}
		return &BotChatResources{Cleanup: func() { cleaned.Add(1) }}, fmt.Errorf("initialize dependency: %w", err)
	})
	if err != nil {
		t.Fatal(err)
	}
	var events int
	result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { events++; return nil })
	select {
	case <-entered:
	default:
		t.Fatal("HTTP dependency was not exercised")
	}
	if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || factoryContextErr != nil {
		t.Fatalf("want dependency deadline with healthy run context: err=%v parent=%v factory=%v", err, ctx.Err(), factoryContextErr)
	}
	var dependencyErr *url.Error
	if !errors.As(err, &dependencyErr) || !dependencyErr.Timeout() {
		t.Fatalf("lost HTTP timeout cause: %v", err)
	}
	if result.Status != store.RunFailed || result.Failure == nil || result.Failure.Class != bot.ErrorPermanent || !result.Failure.TerminalForAttempt {
		t.Errorf("dependency timeout mislabeled: result=%+v err=%v", result, err)
	}
	for _, text := range []string{result.Report.Summary, err.Error()} {
		if !strings.Contains(strings.ToLower(text), "dependency") || !strings.Contains(strings.ToLower(text), "timed out") {
			t.Errorf("missing meaningful dependency timeout explanation: %q", text)
		}
		for _, private := range []string{server.URL, "private-dependency", "private-token", "interrupted"} {
			if strings.Contains(text, private) {
				t.Errorf("unsafe or misleading timeout explanation: %q", text)
			}
		}
	}
	if result.Usage != (store.BotUsage{}) || events != 0 || len(llm.Calls) != 0 || cleaned.Load() != 1 || factoryCalls != 1 || len(adapter.active) != 0 {
		t.Fatalf("failed setup executed work, retried, or leaked resources: usage=%+v events=%d model=%d cleanup=%d factory=%d active=%d", result.Usage, events, len(llm.Calls), cleaned.Load(), factoryCalls, len(adapter.active))
	}
	next, err := adapter.Start(nextCtx, nextReq, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil || next.Status != store.RunSucceeded || next.Report.Summary != "Fresh run completed." || cleaned.Load() != 2 || factoryCalls != 2 || len(llm.Calls) != 1 || len(adapter.active) != 0 {
		t.Fatalf("fresh run affected by dependency timeout: result=%+v err=%v cleanup=%d factory=%d model=%d active=%d", next, err, cleaned.Load(), factoryCalls, len(llm.Calls), len(adapter.active))
	}
}

func TestBotChatRuntimeAdapterFactoryRunInterruption(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			parent, req, _ := botChatTestRequest(t)
			ctx, cancel := context.WithCancel(parent)
			defer cancel()
			want := context.Canceled
			var cleaned atomic.Int64
			adapter, err := NewBotChatRuntimeAdapter(func(factoryCtx context.Context, _ bot.RunRequest) (*BotChatResources, error) {
				if mode == "cancel" {
					cancel()
				}
				<-factoryCtx.Done()
				return &BotChatResources{Cleanup: func() { cleaned.Add(1) }}, fmt.Errorf("factory stopped: %w", factoryCtx.Err())
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "deadline" {
				// Exercise the adapter-owned deadline, not a dependency deadline.
				req.Version.Budget.MaxRuntimeSeconds = 1
				want = context.DeadlineExceeded
			}
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if !errors.Is(err, want) || result.Status != store.RunInterrupted || result.Failure == nil || result.Failure.Class != bot.ErrorInterrupted || !result.Failure.TerminalForAttempt {
				t.Fatalf("true run interruption lost: result=%+v err=%v", result, err)
			}
			if result.Usage != (store.BotUsage{}) || cleaned.Load() != 1 || len(adapter.active) != 0 {
				t.Fatalf("interrupted setup leaked usage or resources: usage=%+v cleanup=%d active=%d", result.Usage, cleaned.Load(), len(adapter.active))
			}
		})
	}
}
