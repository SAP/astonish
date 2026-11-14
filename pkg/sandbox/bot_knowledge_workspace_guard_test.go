package sandbox

import (
	"context"
	"testing"
)

func TestBotKnowledgeCommitBarrier(t *testing.T) {
	c := t.Context()
	if BotKnowledgeCommitBarrierFromContext(c) != nil {
		t.Fatal("unwired barrier")
	}
	held := false
	live := WithBotKnowledgeCommitBarrier(c, func(commit func() error) error { held = true; defer func() { held = false }(); return commit() })
	if err := BotKnowledgeCommitBarrierFromContext(live)(func() error {
		if !held {
			t.Fatal("barrier not held")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if held {
		t.Fatal("barrier not released")
	}
	cancelled, stop := context.WithCancel(live)
	stop()
	if BotKnowledgeCommitBarrierFromContext(cancelled) == nil {
		t.Fatal("derived context lost wiring")
	}
}
