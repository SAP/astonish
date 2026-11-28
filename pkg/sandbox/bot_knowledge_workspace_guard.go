package sandbox

import "context"

type botKnowledgeBarrierKey struct{}

// WithBotKnowledgeCommitBarrier is trusted repository wiring, never model input.
// The barrier must hold the repository's live ownership/cancellation/lease fence
// for the entire operation, including helper approval and physical readback.
// A point-in-time authority callback is not a replacement for this barrier.
func WithBotKnowledgeCommitBarrier(ctx context.Context, barrier BotHomeWorkspaceCommitBarrier) context.Context {
	return context.WithValue(ctx, botKnowledgeBarrierKey{}, barrier)
}

func BotKnowledgeCommitBarrierFromContext(ctx context.Context) BotHomeWorkspaceCommitBarrier {
	barrier, _ := ctx.Value(botKnowledgeBarrierKey{}).(BotHomeWorkspaceCommitBarrier)
	return barrier
}
