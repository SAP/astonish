package api

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
)

func TestBotChatRuntimeAdapterFreshRequestResources(t *testing.T) {
	ctxA, reqA, ssA := botChatTestRequest(t)
	ctxB, reqB, ssB := botChatTestRequest(t)
	var cleaned atomic.Int64
	var mu sync.Mutex
	var agents []*agent.ChatAgent
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	factory := func(ctx context.Context, req bot.RunRequest) (*BotChatResources, error) {
		ss := ssA
		if req.Run.ID == reqB.Run.ID {
			ss = ssB
		}
		resources, err := botChatTestFactory(t, ss, NewMockLLM(TextTurn(req.Instructions)), nil, &cleaned)(ctx, req)
		mu.Lock()
		agents = append(agents, resources.Agent)
		mu.Unlock()
		ready <- struct{}{}
		<-release
		return resources, err
	}
	adapter, _ := NewBotChatRuntimeAdapter(factory)
	reqA.Instructions, reqB.Instructions = "answer A", "answer B"
	var wg sync.WaitGroup
	wg.Go(func() {
		result, err := adapter.Start(ctxA, reqA, func(context.Context, bot.RuntimeEvent) error { return nil })
		if err != nil || result.Report.Summary != "answer A" {
			t.Errorf("A: %+v %v", result, err)
		}
	})
	wg.Go(func() {
		result, err := adapter.Start(ctxB, reqB, func(context.Context, bot.RuntimeEvent) error { return nil })
		if err != nil || result.Report.Summary != "answer B" {
			t.Errorf("B: %+v %v", result, err)
		}
	})
	<-ready
	<-ready
	close(release)
	wg.Wait()
	if len(agents) != 2 || agents[0] == agents[1] || agents[0].SystemPrompt == agents[1].SystemPrompt || cleaned.Load() != 2 {
		t.Fatal("request resources were reused or leaked")
	}
}

func TestBotChatRuntimeAdapterCancelAndResumeFailClosed(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	entered := make(chan struct{})
	adapter, _ := NewBotChatRuntimeAdapter(func(ctx context.Context, _ bot.RunRequest) (*BotChatResources, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	done := make(chan error, 1)
	go func() {
		_, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
		done <- err
	}()
	<-entered
	stale := req.Run.Lease
	stale.Fence++
	if err := adapter.Cancel(ctx, stale); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("stale cancellation: %v", err)
	}
	if err := adapter.Cancel(context.Background(), req.Run.Lease); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("anonymous cancellation: %v", err)
	}
	if err := adapter.Cancel(ctx, req.Run.Lease); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel outcome: %v", err)
	}
	result, err := adapter.Resume(ctx, req, nil)
	if !errors.Is(err, store.ErrBotUnsupported) || result.Status == store.RunSucceeded {
		t.Fatalf("unsafe resume: %+v %v", result, err)
	}
}

func TestBotChatRuntimeAdapterDoesNotCreateMissingSession(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	var cleaned atomic.Int64
	ss := session.InMemoryService()
	llm := NewMockLLM(TextTurn("must not run"))
	adapter, _ := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, llm, nil, &cleaned))
	result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err == nil || result.Status == store.RunSucceeded || len(llm.Calls) != 0 || cleaned.Load() != 1 {
		t.Fatalf("result=%+v err=%v calls=%d cleanup=%d", result, err, len(llm.Calls), cleaned.Load())
	}
}
