package launcher

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"
)

func TestBotSearchFirst(t *testing.T) {
	run := uuid.New()
	ctx := context.Background()
	e, err := NewBotResearchEpisode(run, "parent", BotResearchOpenWeb, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.AllowDetail(ctx, run, "parent", "https://example.com") == nil {
		t.Fatal("pre-search fetch allowed")
	}
	n, err := e.Begin(ctx, run, "parent")
	if err != nil {
		t.Fatal(err)
	}
	e.observe(ctx, n, 0, nil)
	if e.State() != BotResearchEmpty || e.AllowDetail(ctx, run, "parent", "https://example.com") == nil {
		t.Fatal("empty search unlocked")
	}
	n, err = e.Begin(ctx, run, "parent")
	if err != nil {
		t.Fatal(err)
	}
	e.observe(ctx, n, 0, errors.New("429"))
	if e.State() != BotResearchFailed {
		t.Fatal("failure unrecorded")
	}
	n, err = e.Begin(ctx, run, "parent")
	if err != nil {
		t.Fatal(err)
	}
	e.observe(ctx, n, 1, nil)
	if e.State() != BotResearchSearched || e.AllowDetail(ctx, run, "parent", "https://example.com") != nil {
		t.Fatal("observed search did not unlock")
	}
	if e.AllowDetail(ctx, run, "child", "https://example.com") == nil {
		t.Fatal("child overwrites parent research")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if e.AllowDetail(cancelled, run, "parent", "https://example.com") == nil {
		t.Fatal("cancelled search unlocked")
	}
	e.observe(ctx, n, 1, nil)
	if e.State() != BotResearchInterrupted {
		t.Fatal("late success unlocked cancellation")
	}
	direct, err := NewBotResearchEpisode(run, "parent", BotResearchExplicitURLs, []string{"https://example.com/source"})
	if err != nil {
		t.Fatal(err)
	}
	if direct.AllowDetail(ctx, run, "parent", "https://example.com/source") != nil || direct.AllowDetail(ctx, run, "parent", "https://example.com/guessed") == nil {
		t.Fatal("direct URL exception expanded")
	}
	local, err := NewBotResearchEpisode(run, "parent", BotResearchProvidedMaterial, nil)
	if err != nil {
		t.Fatal(err)
	}
	if local.AllowDetail(ctx, run, "parent", "https://example.com") == nil {
		t.Fatal("local work silently discovers web")
	}
}
