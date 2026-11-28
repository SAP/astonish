package launcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBotSearchFirstConcurrentCancellation(t *testing.T) {
	run := uuid.New()
	ctx := context.Background()
	e, err := NewBotResearchEpisode(run, "parent", BotResearchOpenWeb, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.Begin(ctx, run, "parent")
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { _, err := e.Begin(waiting, run, "parent"); result <- err }()
	select {
	case err := <-result:
		t.Fatalf("concurrent search rejected instead of waiting: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := e.Begin(ctx, run, "child"); err == nil || e.State() != BotResearchSearching {
		t.Fatal("wrong binding altered active search")
	}
	otherResult := make(chan error, 1)
	go func() { _, err := e.Begin(ctx, run, "parent"); otherResult <- err }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled wait deadlocked")
	}
	select {
	case err := <-otherResult:
		if err == nil {
			t.Fatal("cancellation released another provider dispatch")
		}
	case <-time.After(time.Second):
		t.Fatal("interruption failed to wake queued search")
	}
	e.observe(ctx, first, 1, nil)
	if e.State() != BotResearchInterrupted || e.AllowDetail(ctx, run, "parent", "https://example.com") == nil {
		t.Fatal("late success unlocked canceled episode")
	}
	if _, err := e.Begin(ctx, run, "parent"); err == nil {
		t.Fatal("interrupted episode restarted")
	}
}

func TestBotSearchFirstConcurrentGeneration(t *testing.T) {
	run := uuid.New()
	ctx := context.Background()
	e, _ := NewBotResearchEpisode(run, "parent", BotResearchOpenWeb, nil)
	first, _ := e.Begin(ctx, run, "parent")
	result := make(chan uint64, 1)
	go func() {
		next, err := e.Begin(ctx, run, "parent")
		if err != nil {
			result <- 0
			return
		}
		result <- next
	}()
	select {
	case n := <-result:
		t.Fatalf("concurrent search did not wait: %d", n)
	case <-time.After(50 * time.Millisecond):
	}
	if e.AllowDetail(ctx, run, "parent", "https://example.com") == nil {
		t.Fatal("active search unlocked details")
	}
	e.observe(ctx, first, 1, nil)
	var next uint64
	select {
	case next = <-result:
		if next != first+1 {
			t.Fatalf("generation: %d", next)
		}
	case <-time.After(time.Second):
		t.Fatal("next search deadlocked")
	}
	e.observe(ctx, first, 1, nil)
	if e.State() != BotResearchSearching {
		t.Fatal("stale observation unlocked next search")
	}
	e.observe(ctx, next, 0, errors.New("unavailable"))
	if e.State() != BotResearchFailed || e.AllowDetail(ctx, run, "parent", "https://example.com") == nil {
		t.Fatal("failed search unlocked details")
	}
}
