package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

type transactionCountingBackend struct {
	sandbox.BotHomeFilesBackend
	opens int
}

func (b *transactionCountingBackend) OpenBotContent(ctx context.Context, h *sandbox.BotHomeBinding, ref store.BotContentRef, span store.BotFileRange) (*store.BotFileStream, error) {
	b.opens++
	return b.BotHomeFilesBackend.OpenBotContent(ctx, h, ref, span)
}

func TestBotTransactionContentReuse(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, b, exact, backend := selectionFixture(t, scope)
			if err := r.selectHomeConfiguration(ctx, exact, backend); err != nil {
				t.Fatal(err)
			}
			counted := &transactionCountingBackend{BotHomeFilesBackend: backend}
			lookups := 0
			// Wrap the production codec's fresh, scope-checked adapter, not its authorization lookup.
			wrap := func(c *botContentCodec) *botContentCodec {
				return &botContentCodec{OpenIntake: c.OpenIntake, Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
					lookups++
					disk, err := c.Open(ctx, id)
					if h, ok := disk.(*botHomeContent); ok && err == nil {
						h.backend = counted
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
			read := func(tx botDraftDB) error {
				for range 5 {
					v, err := tx.ReadVersion(ctx, b.ID, 1)
					if err != nil {
						return err
					}
					if v.Charter != "retained instructions sentinel" {
						t.Fatal(v.Charter)
					}
				}
				return nil
			}
			for i := 1; i <= 2; i++ {
				if err := r.db.Transaction(ctx, read); err != nil {
					t.Fatal(err)
				}
				if lookups != i*5 {
					t.Fatalf("transaction %d skipped fresh scope/binding lookup: %d", i, lookups)
				}
				if counted.opens != i {
					t.Fatalf("transaction %d performed %d total content opens; want %d", i, counted.opens, i)
				}
			}
			backend.corrupt = true
			if err := r.db.Transaction(ctx, read); err == nil {
				t.Fatal("new transaction accepted corrupt bytes")
			}
			backend.corrupt = false
			for k := range backend.objects {
				delete(backend.objects, k)
			}
			if err := r.db.Transaction(ctx, read); err == nil {
				t.Fatal("new transaction accepted missing bytes")
			}
		})
	}
}

type transactionFailClose struct{ io.Reader }

func (transactionFailClose) Close() error { return io.ErrUnexpectedEOF }

func TestBotTransactionContentBoundsAndIntegrity(t *testing.T) {
	raw := []byte("verified content")
	digest := sha256.Sum256(raw)
	key := botTransactionContentKey{home: "home", backend: sandbox.BackendKindDocker, ref: store.BotContentRef{SizeBytes: int64(len(raw)), SHA256: hex.EncodeToString(digest[:])}}
	for _, failure := range []string{"digest", "truncated", "oversized", "close", "missing"} {
		t.Run(failure, func(t *testing.T) {
			c := &botTransactionContent{objects: make(map[botTransactionContentKey][]byte)}
			_, err := c.open(t.Context(), key, func() (*store.BotFileStream, error) {
				body := raw
				switch failure {
				case "digest":
					body = bytes.Repeat([]byte("x"), len(raw))
				case "truncated":
					body = raw[:1]
				case "oversized":
					body = append(append([]byte{}, raw...), 'x')
				case "close":
					return &store.BotFileStream{ReadCloser: transactionFailClose{bytes.NewReader(raw)}}, nil
				case "missing":
					return nil, store.ErrBotNotFound
				}
				return &store.BotFileStream{ReadCloser: io.NopCloser(bytes.NewReader(body))}, nil
			})
			if err == nil || len(c.objects) != 0 || c.bytes != 0 {
				t.Fatalf("cached failed read: %v %+v", err, c)
			}
		})
	}
	c := &botTransactionContent{objects: make(map[botTransactionContentKey][]byte)}
	opens := 0
	open := func() (*store.BotFileStream, error) {
		opens++
		return &store.BotFileStream{ReadCloser: io.NopCloser(bytes.NewReader(raw))}, nil
	}
	read := func(k botTransactionContentKey) {
		s, err := c.open(t.Context(), k, open)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(s)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, raw) {
			t.Fatal("changed bytes")
		}
	}
	read(key)
	read(key)
	if opens != 1 {
		t.Fatalf("opens=%d", opens)
	}
	other := key
	other.home = "other"
	read(other)
	if opens != 2 {
		t.Fatal("reused bytes across Home bindings")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.open(canceled, key, open); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, bound := range []string{"bytes", "objects"} {
		t.Run(bound, func(t *testing.T) {
			c = &botTransactionContent{objects: make(map[botTransactionContentKey][]byte)}
			if bound == "bytes" {
				c.bytes = botTransactionContentBytes
			} else {
				for i := range botTransactionContentObjects {
					k := key
					k.ref.Revision = int64(i + 1)
					c.objects[k] = nil
				}
			}
			before := opens
			read(key)
			read(key)
			if opens != before+2 {
				t.Fatal("exceeded cache bound")
			}
		})
	}
}
