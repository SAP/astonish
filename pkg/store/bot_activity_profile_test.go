package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBotActivityProfilerRequestLocal(t *testing.T) {
	var events []BotActivityTiming
	ctx := WithBotActivityProfiler(context.Background(), func(v BotActivityTiming) { events = append(events, v) })
	ObserveBotActivity(ctx, "content_stream_open", 13)()
	ObserveBotActivity(context.Background(), "unprofiled", 0)()
	if len(events) != 1 || events[0].Stage != "content_stream_open" || events[0].Sequence != 13 || events[0].Duration < 0 {
		t.Fatalf("unexpected timings: %+v", events)
	}
}

func TestBotRuntimeIOProfilerNilAndCoverage(t *testing.T) {
	var nilProfile *BotRuntimeIOProfile
	if got := nilProfile.Snapshot(); got != (BotRuntimeIOSnapshot{}) {
		t.Fatal(got)
	}
	for _, ctx := range []context.Context{nil, context.Background()} {
		ObserveBotRuntimeIO(ctx, BotRuntimeIOOpen, 0)(errors.New("not logged"))
		MarkBotRuntimeIOProfileCovered(ctx)
		ObserveBotActivity(ctx, "disabled", 0)()
	}
	ctx, p := WithBotRuntimeIOProfiler(context.Background(), nil)
	if got := p.Snapshot(); got != (BotRuntimeIOSnapshot{}) {
		t.Fatal(got)
	}
	ObserveBotRuntimeIO(ctx, BotRuntimeIOOperation(255), 0)(nil)
	if got := p.Snapshot(); got.Covered || got.Opens != 0 || got.Puts != 0 {
		t.Fatal(got)
	}
	MarkBotRuntimeIOProfileCovered(ctx)
	if got := p.Snapshot(); got != (BotRuntimeIOSnapshot{Covered: true}) {
		t.Fatal(got)
	}
	// Unrelated request contexts cannot inherit evidence from this request.
	_, other := WithBotRuntimeIOProfiler(context.Background(), nil)
	if got := other.Snapshot(); got != (BotRuntimeIOSnapshot{}) {
		t.Fatal(got)
	}
}

func TestBotRuntimeIOProfilerAttemptsFailuresAndConcurrency(t *testing.T) {
	var observed, failed atomic.Int64
	ctx, p := WithBotRuntimeIOProfiler(context.Background(), func(v BotRuntimeIOTiming) {
		if v.Duration < 0 || v.Sequence < 1 || (v.Operation != BotRuntimeIOOpen && v.Operation != BotRuntimeIOPut) {
			t.Errorf("invalid timing: %+v", v)
		}
		observed.Add(1)
		if v.Failed {
			failed.Add(1)
		}
	})
	var wg sync.WaitGroup
	for i := 1; i <= 100; i++ {
		wg.Add(1)
		go func(sequence int64) {
			defer wg.Done()
			MarkBotRuntimeIOProfileCovered(ctx)
			finish := ObserveBotRuntimeIO(ctx, BotRuntimeIOOpen, sequence)
			_ = p.Snapshot()
			finish(errors.New("sensitive error must not be retained"))
			finish(nil) // Completion is exactly once even if retried.
			ObserveBotRuntimeIO(ctx, BotRuntimeIOPut, sequence)(nil)
		}(int64(i))
	}
	wg.Wait()
	if got := p.Snapshot(); got != (BotRuntimeIOSnapshot{Opens: 100, Puts: 100, Failures: 100, Covered: true}) {
		t.Fatal(got)
	}
	if observed.Load() != 200 || failed.Load() != 100 {
		t.Fatalf("observed=%d failed=%d", observed.Load(), failed.Load())
	}
	finish := ObserveBotRuntimeIO(ctx, BotRuntimeIOOpen, 101)
	if p.Snapshot().Opens != 101 {
		t.Fatal("attempt was not counted at boundary entry")
	}
	finish(nil)
}
