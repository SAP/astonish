package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Control memory is local to the Bot's scoped database, not a replicated
// MemoryStore. Its mutation and operation receipt share the caller's transaction;
// there is no external reconciliation intent to complete speculatively.
// Observe the exact replacement and supersession before the caller records Applied.
func verifyControlMemoryMutation(ctx context.Context, db botDraftDB, p store.BotControlProposal) error {
	key := controlProposalKey(p.ID)
	if routine, _ := ctx.Value(routineMutationKey{}).(bool); routine {
		key = "routine-operation:" + p.ID.String()
	}
	q := p.Payload.Memory
	if p.Payload.Operation != store.BotControlMemoryForget {
		row, err := db.ReadCorrectionMemory(ctx, uuid.NewSHA1(p.ID, []byte("memory")).String())
		if err != nil {
			return err
		}
		if row == nil || row.Snippet != q.Content || row.Category != q.Category || row.Attribution == nil || row.Attribution.BotID != p.BotID || row.Attribution.Source != key || row.Attribution.SupersededBy != "" {
			return store.ErrBotConflict
		}
	}
	if p.Payload.Operation != store.BotControlMemoryRemember {
		row, err := db.ReadCorrectionMemory(ctx, q.MemoryID)
		if err != nil {
			return err
		}
		if row == nil || row.Attribution == nil || row.Attribution.BotID != p.BotID || row.Attribution.SupersededBy != key {
			return store.ErrBotConflict
		}
	}
	return nil
}
