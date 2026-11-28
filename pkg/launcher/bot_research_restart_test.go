package launcher

import (
	"context"
	"github.com/google/uuid"
	"testing"
)

func TestBotSearchFirstRestart(t *testing.T) {
	run := uuid.New()
	ctx := context.Background()
	old, _ := NewBotResearchEpisode(run, "parent", BotResearchOpenWeb, nil)
	generation, _ := old.Begin(ctx, run, "parent")
	old.observe(ctx, generation, 1, nil)
	t.Run("reopen-no-unlock", func(t *testing.T) {
		for _, id := range []uuid.UUID{run, uuid.New()} {
			reopened, err := NewBotResearchEpisode(id, "parent", BotResearchOpenWeb, nil)
			if err != nil {
				t.Fatal(err)
			}
			if reopened.State() != BotResearchNeedsSearch || reopened.AllowDetail(ctx, id, "parent", "https://example.com/source") == nil {
				t.Fatal("prior receipt unlocked reopened episode")
			}
			reopened.observe(ctx, generation, 1, nil)
			if reopened.State() != BotResearchNeedsSearch {
				t.Fatal("old generation unlocked restored episode")
			}
		}
	})
	t.Run("child-cancel-no-mutation", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := old.Begin(cancelled, run, "child"); err == nil {
			t.Fatal("child dispatch admitted")
		}
		if old.State() != BotResearchSearched {
			t.Fatal("cancelled child dispatch mutated parent")
		}
		if err := old.AllowDetail(cancelled, uuid.New(), "parent", "https://example.com/source"); err == nil {
			t.Fatal("foreign run admitted")
		}
		if old.State() != BotResearchSearched {
			t.Fatal("cancelled foreign detail mutated parent")
		}
	})
	t.Run("stale-cancel-no-mutation", func(t *testing.T) {
		e, _ := NewBotResearchEpisode(uuid.New(), "parent", BotResearchOpenWeb, nil)
		first, _ := e.Begin(ctx, e.run, "parent")
		e.observe(ctx, first, 0, nil)
		second, _ := e.Begin(ctx, e.run, "parent")
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		e.observe(cancelled, first, 1, nil)
		if e.State() != BotResearchSearching {
			t.Fatal("stale cancellation mutated current dispatch")
		}
		e.observe(ctx, second, 1, nil)
		if e.State() != BotResearchSearched {
			t.Fatal("current dispatch lost after stale cancellation")
		}
	})
	t.Run("cancel-no-late-unlock", func(t *testing.T) {
		e, _ := NewBotResearchEpisode(uuid.New(), "parent", BotResearchOpenWeb, nil)
		n, _ := e.Begin(ctx, e.run, "parent")
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		e.observe(cancelled, n, 1, nil)
		e.observe(ctx, n, 1, nil)
		if e.State() != BotResearchInterrupted || e.AllowDetail(ctx, e.run, "parent", "https://example.com/source") == nil {
			t.Fatal("late success unlocked interrupted episode")
		}
	})
}
