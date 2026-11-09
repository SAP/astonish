package launcher

import (
	"context"
	"fmt"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

// botMu protects both the shared reducer and the snapshot paired with its root.
func (b *botPlatformBackend) observeBotOperationLocked(v *store.BotOperationView) {
	if b.selected == nil {
		return
	}
	if b.operationHeads == nil || b.operationHeads.BotID != b.selected.ID.String() || b.operationHeads.SessionID != b.selected.HomeSessionID.String() {
		b.operationHeads = events.NewTranscript()
		b.operationHeads.BotID = b.selected.ID.String()
		b.operationHeads.SessionID = b.selected.HomeSessionID.String()
	}
	b.operationHeads.Apply(events.Event{Kind: events.KindBotOperation, BotOperation: v})
}

func (b *botPlatformBackend) botComposerCaughtUpLocked() bool {
	if b.operationHeads == nil {
		return true
	}
	for _, head := range b.operationHeads.BotOperations {
		if head.Status != store.BotOperationApplied {
			continue
		}
		found := false
		if b.botSnapshot != nil && b.botSnapshot.Bot.ID == head.BotID && b.botSnapshot.Bot.HomeSessionID == head.HomeSessionID {
			for _, v := range b.botSnapshot.Operations {
				if v.Validate() == nil && v.OperationID == head.OperationID && v.BotID == head.BotID && v.HomeSessionID == head.HomeSessionID && v.Status == store.BotOperationApplied && v.Revision >= head.Revision && v.StateSequence >= head.StateSequence {
					found = true
					break
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// A delivered Applied envelope is evidence of a commit, not a Bot version.
// Reconcile through the same projection used by the header before admission.
// One additional read tolerates a pre-commit snapshot; no mutation is retried.
func (b *botPlatformBackend) reconcileBotComposer(ctx context.Context, selected *store.Bot, epoch uint64) (*store.Bot, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for reads := 0; ; reads++ {
		b.botMu.Lock()
		if b.selected == nil || b.selected.ID != selected.ID || b.selectionEpoch != epoch || b.sessionID != uuid.Nil || ctx.Err() != nil {
			b.botMu.Unlock()
			return nil, fmt.Errorf("Bot selection changed or request cancelled")
		}
		if b.botComposerCaughtUpLocked() {
			bot := *b.selected
			b.botMu.Unlock()
			return &bot, nil
		}
		b.botMu.Unlock()
		if reads == 2 {
			return nil, fmt.Errorf("Bot change is not yet available in its snapshot; review before retrying")
		}
		if _, err := b.BotSnapshot(ctx); err != nil {
			return nil, err
		}
	}
}
