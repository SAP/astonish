package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

type selectionReadbackBackend struct {
	*transactionCountingBackend
	storage         *contentHomeBackend
	puts            int
	corruptReadback bool
}

func (b *selectionReadbackBackend) PutBotContentReadback(ctx context.Context, h *sandbox.BotHomeBinding, k store.BotContentKind, size int64, body io.Reader, revision int64, barrier func(func() error) error) (*store.BotContentRef, []byte, error) {
	b.puts++
	ref, err := b.storage.PutBotContent(ctx, h, k, size, body, revision, barrier)
	if err != nil {
		return nil, nil, err
	}
	stream, err := b.storage.OpenBotContent(ctx, h, *ref, store.BotFileRange{})
	if err != nil {
		return nil, nil, err
	}
	raw, err := io.ReadAll(stream)
	if closeErr := stream.Close(); err == nil {
		err = closeErr
	}
	if b.corruptReadback {
		raw = bytes.Repeat([]byte("!"), len(raw))
	}
	return ref, raw, err
}

func TestBotContentSelectionPutReadback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, corrupt := range []bool{false, true} {
			t.Run(string(scope)+map[bool]string{false: "/valid", true: "/forged-digest"}[corrupt], func(t *testing.T) {
				r, ctx, b, exact, storage := selectionFixture(t, scope)
				seedHomeSelectionCategories(t, r, ctx, b)
				backend := &selectionReadbackBackend{transactionCountingBackend: &transactionCountingBackend{BotHomeFilesBackend: storage}, storage: storage, corruptReadback: corrupt}
				wrap := func(c *botContentCodec) *botContentCodec {
					return &botContentCodec{OpenIntake: c.OpenIntake, Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
						disk, err := c.Open(ctx, id)
						if h, ok := disk.(*botHomeContent); ok && err == nil {
							h.backend = backend
						}
						return disk, err
					}}
				}
				switch d := r.db.(type) {
				case personalBotDB:
					d.content = wrap(d.content)
					r.db = d
				case teamBotDB:
					d.content = wrap(d.content)
					r.db = d
				}
				err := r.selectHomeConfiguration(ctx, exact, backend)
				if corrupt {
					if err == nil {
						t.Fatal("accepted corrupt stored bytes with legitimate digest")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("readback puts=%d separate destination opens=%d objects=%d", backend.puts, backend.opens, len(storage.objects))
				if backend.puts != len(storage.objects) || backend.puts == 0 || backend.opens != 0 {
					t.Fatal("duplicate helper operations")
				}
				storage.corrupt = true
				if _, err := r.GetVersion(ctx, b.ID, 1); err == nil {
					t.Fatal("cache escaped selection")
				}
				storage.corrupt = false
				for k := range storage.objects {
					delete(storage.objects, k)
				}
				if _, err := r.GetVersion(ctx, b.ID, 1); err == nil {
					t.Fatal("accepted missing object in new operation")
				}
			})
		}
	}
}

func TestBotContentReadbackCacheBounds(t *testing.T) {
	raw := []byte("immutable")
	sum := sha256.Sum256(raw)
	key := botTransactionContentKey{home: "home", backend: sandbox.BackendKindDocker, ref: store.BotContentRef{SizeBytes: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}}
	ctx, end := WithBotContentReadOperation(t.Context())
	c := ctx.Value(botContentReadOperationKey{}).(*botTransactionContent)
	if err := c.storeReadback(ctx, key, raw); err != nil {
		t.Fatal(err)
	}
	raw[0] = 'x'
	if string(c.objects[key]) != "immutable" {
		t.Fatal("backend buffer aliased")
	}
	if err := c.storeReadback(ctx, key, raw); err == nil {
		t.Fatal("bad digest accepted")
	}
	c.bytes = botTransactionContentBytes
	if c.canStore(1) {
		t.Fatal("byte bound bypass")
	}
	other := key
	other.home = "other"
	if err := c.storeReadback(ctx, other, []byte("immutable")); err != nil {
		t.Fatal(err)
	}
	if len(c.objects) != 1 {
		t.Fatal("bound exceeded")
	}
	end()
	if c.canStore(0) {
		t.Fatal("closed cache accepts bytes")
	}
	if err := c.storeReadback(ctx, key, []byte("immutable")); err != nil {
		t.Fatal(err)
	}
	if len(c.objects) != 0 {
		t.Fatal("closed cache repopulated")
	}
}
