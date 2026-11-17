package store

import (
	"context"
	"crypto/sha256"
	"fmt"
)

// BotProgressContextStore persists private working context independently of public
// transcript/activity projections. A successful write must precede checkpointing;
// an orphaned context write confers no right to resume work.
type BotProgressContextStore interface {
	SaveProgressContext(context.Context, BotProgressMutation, []byte) (string, string, error)
	LoadProgressContext(context.Context, BotProgressMutation, string, string) ([]byte, error)
}

func BotProgressContextDigest(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
