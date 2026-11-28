package entstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// SQL owns the operation before this retained-storage lock is entered. A
// transaction-bound guard must not reenter the outer single-connection pool.
type sqlStorageOrderBackend struct {
	*contentHomeBackend
	mu      sync.Mutex
	entered chan struct{}
	prepare atomic.Int32
	inSQL   atomic.Bool
}

func (b *sqlStorageOrderBackend) PrepareBotHomeSession(context.Context, *sandbox.BotHomeBinding, string) error {
	b.prepare.Add(1)
	return errors.New("session repository reentry during storage operation")
}

func (b *sqlStorageOrderBackend) PutBotContent(ctx context.Context, home *sandbox.BotHomeBinding, kind store.BotContentKind, size int64, body io.Reader, revision int64, guard func(func() error) error) (*store.BotContentRef, error) {
	if !b.inSQL.Load() {
		return nil, errors.New("retained write entered outside SQL operation")
	}
	b.entered <- struct{}{}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.contentHomeBackend.PutBotContent(ctx, home, kind, size, body, revision, guard)
}

func (b *sqlStorageOrderBackend) OpenBotContent(ctx context.Context, home *sandbox.BotHomeBinding, ref store.BotContentRef, span store.BotFileRange) (*store.BotFileStream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.contentHomeBackend.OpenBotContent(ctx, home, ref, span)
}

func TestBotSQLiteSQLStorageLockOrderNoSessionReentry(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, base, bot, exact, original := selectionFixture(t, scope)
			if err := r.selectHomeConfiguration(base, exact, original); err != nil {
				t.Fatal(err)
			}
			initial, err := r.db.GetBot(base, bot.ID)
			if err != nil {
				t.Fatal(err)
			}
			sequence := initial.LastSequence
			ctx, cancel := context.WithTimeout(base, 5*time.Second)
			defer cancel()
			backend := &sqlStorageOrderBackend{contentHomeBackend: original, entered: make(chan struct{}, 1)}
			wrap := func(c *botContentCodec) *botContentCodec {
				return &botContentCodec{OpenIntake: c.OpenIntake, Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
					storage, err := c.Open(ctx, id)
					if h, ok := storage.(*botHomeContent); ok && err == nil {
						h.backend = backend
					}
					return storage, err
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
			backend.mu.Lock()
			locked := true
			defer func() {
				if locked {
					backend.mu.Unlock()
				}
			}()
			first := make(chan error, 1)
			go func() {
				first <- r.db.Transaction(ctx, func(tx botDB) error {
					// Acquire the actual SQLite writer lock before requesting storage.
					if err := tx.AdvanceSequence(ctx, bot.ID, sequence); err != nil {
						return fmt.Errorf("acquire SQL writer sequence %d: %w", sequence, err)
					}
					backend.inSQL.Store(true)
					defer backend.inSQL.Store(false)
					v := store.DefaultBotVersion()
					v.BotID = bot.ID
					v.Version = 2
					v.CreatedBy = bot.OwnerID
					v.CreatedAt = time.Now().UTC()
					v.Charter = "lock-order retained bytes"
					if err := tx.InsertVersion(ctx, v, "lock-order-hash"); err != nil {
						return err
					}
					got, err := tx.ReadVersion(ctx, bot.ID, 2)
					if err == nil && got.Charter != v.Charter {
						return fmt.Errorf("readback changed charter: %q", got.Charter)
					}
					return err
				})
			}()
			select {
			case <-backend.entered:
			case err := <-first:
				t.Fatalf("write never contended on storage: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			second := make(chan error, 1)
			started := make(chan struct{})
			acquired := make(chan struct{})
			go func() {
				close(started)
				second <- r.db.Transaction(ctx, func(tx botDB) error {
					close(acquired)
					return tx.AdvanceSequence(ctx, bot.ID, sequence+1)
				})
			}()
			<-started
			select {
			case <-acquired:
				t.Error("second transaction acquired SQL while first held storage contention")
			case err := <-second:
				t.Errorf("second transaction did not queue: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			backend.mu.Unlock()
			locked = false
			for name, done := range map[string]<-chan error{"SQL-to-storage writer": first, "competing SQL writer": second} {
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("%s: %v", name, err)
					}
				case <-ctx.Done():
					t.Fatalf("%s deadlocked: %v", name, ctx.Err())
				}
			}
			if backend.prepare.Load() != 0 {
				t.Fatalf("storage reentered session preparation %d times", backend.prepare.Load())
			}
			row, err := r.db.GetBot(ctx, bot.ID)
			if err != nil || row.LastSequence != sequence+2 {
				t.Fatalf("both SQL writers did not commit: %+v %v", row, err)
			}
			v, err := r.GetVersion(ctx, bot.ID, 2)
			if err != nil || v.Charter != "lock-order retained bytes" {
				t.Fatalf("committed retained readback: %+v %v", v, err)
			}
		})
	}
}
