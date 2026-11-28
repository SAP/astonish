package bot

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
	"testing"
)

func TestBotSourceReadReceiptBoundary(t *testing.T) {
	identity := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), ActorID: uuid.NewString()}
	ctx := execution.WithBotRunIdentity(context.Background(), identity)
	args := map[string]any{"url": "https://example.test"}
	response := map[string]any{"error": "closed"}
	r := NewToolSourceReadReceipt(ctx, "call", "web_fetch", args, response)
	binding := NewToolNonexecutionOutcome(ctx, "call", "web_fetch", args, ToolFeedbackTerminal).Nonexecution
	if !r.ValidFor(identity, "call", "web_fetch", binding.ArgumentDigest, response) {
		t.Fatal("exact receipt rejected")
	}
	if r.ValidFor(identity, "other", "web_fetch", binding.ArgumentDigest, response) || r.ValidFor(identity, "call", "web_fetch", "other", response) || r.ValidFor(identity, "call", "web_fetch", binding.ArgumentDigest, map[string]any{"error": "changed"}) {
		t.Fatal("mismatch accepted")
	}
	ctx, _ = WithToolOutcomes(ctx)
	RecordToolOutcome(ctx, "call", ToolOutcome{SourceRead: r})
	r.CallID = "mutated"
	o, _ := TakeToolOutcome(ctx, "call")
	if o.SourceRead.CallID != "call" {
		t.Fatal("receipt not copied")
	}
	RecordToolOutcome(ctx, "call", o)
	RecordToolOutcome(ctx, "call", ToolOutcome{Verified: true})
	o, _ = TakeToolOutcome(ctx, "call")
	if !o.ReceiptConflict {
		t.Fatal("duplicate overwritten")
	}
}
