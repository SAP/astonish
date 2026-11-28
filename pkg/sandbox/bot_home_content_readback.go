package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"

	"github.com/SAP/astonish/pkg/store"
)

// BotHomeContentReadbackLimit bounds the optional same-helper readback; larger
// content retains the streaming PutBotContent/OpenBotContent path.
const BotHomeContentReadbackLimit int64 = 8 << 20

// BotHomeContentReadbackBackend optionally returns bytes read from the committed
// immutable object by the Put helper, not echoed input. Callers must independently
// verify the returned bytes against their input digest and complete reference.
// A failure must not return usable content, even if physical publication occurred.
type BotHomeContentReadbackBackend interface {
	PutBotContentReadback(context.Context, *BotHomeBinding, store.BotContentKind, int64, io.Reader, int64, func(func() error) error) (*store.BotContentRef, []byte, error)
}

// ReadBotHomeContentReadback validates a bounded raw readback protocol to EOF.
// The backend must still join the helper and propagate its terminal error.
func ReadBotHomeContentReadback(r io.Reader, size int64, digest string) ([]byte, error) {
	if size < 0 || size > BotHomeContentReadbackLimit {
		return nil, store.ErrBotInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if int64(len(raw)) != size || hex.EncodeToString(sum[:]) != digest {
		return nil, store.ErrBotConflict
	}
	return raw, nil
}
