package entstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotTransactionContentConcurrentReaders(t *testing.T) {
	raw := []byte("concurrent immutable content")
	sum := sha256.Sum256(raw)
	key := botTransactionContentKey{ref: store.BotContentRef{SizeBytes: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}}
	c := &botTransactionContent{objects: make(map[botTransactionContentKey][]byte)}
	var wg sync.WaitGroup
	opens := 0 // Serialized by the transaction content mutex.
	for range 12 {
		wg.Go(func() {
			stream, err := c.open(t.Context(), key, func() (*store.BotFileStream, error) {
				opens++
				return &store.BotFileStream{ReadCloser: io.NopCloser(bytes.NewReader(raw))}, nil
			})
			if err != nil {
				t.Error(err)
				return
			}
			got, err := io.ReadAll(stream)
			if err != nil {
				t.Error(err)
			}
			if err := stream.Close(); err != nil {
				t.Error(err)
			}
			if !bytes.Equal(got, raw) {
				t.Error("independent reader changed bytes")
			}
		})
	}
	wg.Wait()
	if opens != 1 {
		t.Fatalf("concurrent readers performed %d opens", opens)
	}
}
