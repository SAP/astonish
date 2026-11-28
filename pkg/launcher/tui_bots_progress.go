package launcher

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
)

func (b *botPlatformBackend) ContinueBotProgress(ctx context.Context, state store.BotRunState, revision int64, key string) (*store.BotRun, error) {
	selected := b.SelectedBot()
	if selected == nil || selected.ID != state.BotID || state.Progress == nil || state.ValidateProgress() != nil || state.Progress.Phase != store.BotProgressSuspended || state.Progress.Reason != store.BotProgressReasonPaused {
		return nil, fmt.Errorf("invalid owner-paused progress checkpoint binding")
	}
	// Never substitute a polled revision for the revision the owner reviewed.
	return b.bots.ContinueProgress(ctx, state.BotID.String(), state.RunID.String(), revision, state.Progress.Revision, key)
}
