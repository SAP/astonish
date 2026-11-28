package entstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type progressWriteCountingDisk struct {
	store.BotContentStaging
	puts    *int
	corrupt *bool
}

func (d *progressWriteCountingDisk) Put(ctx context.Context, kind store.BotContentKind, rev, size int64, reader io.Reader, commit func(func() error) error) (*store.BotContentRef, error) {
	if kind == store.BotContentCheckpoint {
		(*d.puts)++
	}
	return d.BotContentStaging.Put(ctx, kind, rev, size, reader, commit)
}
func (d *progressWriteCountingDisk) Open(ctx context.Context, ref store.BotContentRef, span store.BotFileRange) (*store.BotFileStream, error) {
	if *d.corrupt && ref.Kind == store.BotContentCheckpoint {
		return nil, store.ErrBotConflict
	}
	return d.BotContentStaging.Open(ctx, ref, span)
}

func TestBotProgressControlWritePreservesCheckpoint(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, _, ctx, _, lease, now := progressFixture(t, scope)
			q := progressCheckpoint(t, r, ctx, lease, *now)
			var before map[string]any
			read := func() map[string]any {
				switch d := r.db.(type) {
				case personalBotDB:
					row, err := d.client.BotRun.Get(ctx, lease.RunID)
					if err != nil {
						t.Fatal(err)
					}
					return row.Data
				case teamBotDB:
					row, err := d.client.BotRun.Get(ctx, lease.RunID)
					if err != nil {
						t.Fatal(err)
					}
					return row.Data
				}
				panic("scope")
			}
			var puts int
			var corrupt bool
			var base *botContentCodec
			switch d := r.db.(type) {
			case personalBotDB:
				base = d.content
			case teamBotDB:
				base = d.content
			}
			open := base.Open
			base.Open = func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
				disk, err := open(ctx, id)
				return &progressWriteCountingDisk{BotContentStaging: disk, puts: &puts, corrupt: &corrupt}, err
			}
			before = read()
			ref, _, err := r.SaveProgressContext(ctx, q.Mutation, []byte("private working context"))
			if err != nil || ref == "" {
				t.Fatal(err)
			}
			assertSame := func() {
				var a, b botRunRecord
				if err := decodeBotRecord(before, &a); err != nil {
					t.Fatal(err)
				}
				if err := decodeBotRecord(read(), &b); err != nil {
					t.Fatal(err)
				}
				var oldBody, newBody botRunContent
				if err := json.Unmarshal(a.Inline, &oldBody); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(b.Inline, &newBody); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(oldBody.Checkpoint, newBody.Checkpoint) {
					t.Fatal("unchanged checkpoint was rewritten")
				}
			}
			assertSame()
			receipt, err := r.BeginProgressDispatch(ctx, store.BotProgressBeginDispatchRequest{Mutation: q.Mutation, DispatchID: uuid.New(), Kind: store.BotProgressDispatchTool, OperationDigest: store.BotProgressContextDigest([]byte("operation")), ReservedUsage: store.BotUsage{ToolCalls: 1}})
			if err != nil {
				t.Fatal(err)
			}
			_ = receipt
			assertSame()
			run, err := r.db.ReadRun(ctx, lease.BotID, lease.RunID)
			if err != nil || run.ProgressPending != 1 || run.Run.Usage.ToolCalls != 1 {
				t.Fatalf("control not persisted: %+v %v", run, err)
			}
			bad := q.Mutation
			bad.Lease.Fence++
			if _, _, err := r.SaveProgressContext(ctx, bad, []byte("stale")); err == nil {
				t.Fatal("stale fence accepted")
			}
			if puts != 0 {
				t.Fatalf("redundant checkpoint puts: %d", puts)
			}
			var damaged botRunRecord
			if err := decodeBotRecord(read(), &damaged); err != nil {
				t.Fatal(err)
			}
			damaged.Inline[0] ^= 1
			damagedData, err := encodeRuntimeRecord(&damaged)
			if err != nil {
				t.Fatal(err)
			}
			switch d := r.db.(type) {
			case personalBotDB:
				err = d.client.BotRun.UpdateOneID(lease.RunID).SetData(damagedData).Exec(ctx)
			case teamBotDB:
				err = d.client.BotRun.UpdateOneID(lease.RunID).SetData(damagedData).Exec(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := r.SaveProgressContext(ctx, q.Mutation, []byte("fresh read must fail")); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("corrupt checkpoint accepted: %v", err)
			}
			if puts != 0 {
				t.Fatal("corruption triggered write")
			}
		})
	}
}
