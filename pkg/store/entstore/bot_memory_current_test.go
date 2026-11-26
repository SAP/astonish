package entstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type memoryReadHook struct {
	store.BotContentStaging
	after func()
}

func (m memoryReadHook) Open(ctx context.Context, ref store.BotContentRef, span store.BotFileRange) (*store.BotFileStream, error) {
	stream, err := m.BotContentStaging.Open(ctx, ref, span)
	if err == nil && ref.Kind == store.BotContentMemory {
		m.after()
	}
	return stream, err
}

func TestBotMemoryForgetDuringRankingFailsClosed(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			_, bots, memories, ctx, b, _ := botMemorySearchFixture(t, scope, "hybrid")
			svc, _ := store.NewBotMemoryService(bots, memories, b.ID, scope)
			if err := svc.Add(ctx, store.MemoryEntry{Content: "quasar retiring", SessionID: b.HomeSessionID.String(), Embedding: []float32{1, 0}, Attribution: &store.MemoryAttribution{BotID: b.ID, Source: "owner", ObservedAt: time.Now(), Status: store.MemoryKnowledgeInferred}}); err != nil {
				t.Fatal(err)
			}
			rows, err := svc.ListBySession(ctx, b.HomeSessionID.String())
			if err != nil || len(rows) != 1 {
				t.Fatalf("seed: %+v %v", rows, err)
			}
			original := rows[0]
			var db botDB
			switch s := bots.(type) {
			case *personalBotStore:
				db = personalBotDB{client: s.client, content: s.content}
			case *teamBotStore:
				db = teamBotDB{client: s.client, content: s.content}
			}
			op := store.BotControlProposal{ID: uuid.New(), BotID: b.ID, SourceUserID: b.OwnerID, Payload: store.BotControlPayload{Operation: store.BotControlMemoryForget, Memory: &store.BotControlMemory{MemoryID: original.ID, ExpectedFingerprint: store.MemoryContentFingerprint(original)}}}
			// Commit a forget after opening the immutable candidate snapshot, before
			// the ranking result's final current-head validation.
			var codec *botContentCodec
			switch s := bots.(type) {
			case *personalBotStore:
				codec = s.content
			case *teamBotStore:
				codec = s.content
			}
			open := codec.Open
			fired := false
			codec.Open = func(c context.Context, id store.BotID) (store.BotContentStaging, error) {
				disk, err := open(c, id)
				return memoryReadHook{BotContentStaging: disk, after: func() {
					if fired {
						return
					}
					fired = true
					c = context.WithValue(c, routineMutationKey{}, true)
					if err := db.Transaction(c, func(tx botDB) error {
						return mutateControlMemory(c, tx, op, b.HomeSessionID, b.OwnerID, time.Now())
					}); err != nil {
						t.Fatal(err)
					}
				}}, err
			}
			got, err := svc.Search(ctx, "quasar", 1, 0)
			codec.Open = open
			if !errors.Is(err, store.ErrBotConflict) || len(got) != 0 {
				t.Fatalf("stale candidate recalled: %+v %v", got, err)
			}
			after, err := svc.Get(ctx, original.ID)
			a := *original.Attribution
			a.SupersededBy = "routine-operation:" + op.ID.String()
			original.Attribution = &a
			if err != nil || after == nil || !reflect.DeepEqual(original, *after) {
				t.Fatalf("history/effect mismatch: %+v %v", after, err)
			}
		})
	}
}

func TestBotMemoryCurrentBeforeRankingPreservesHistory(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []string{"fts", "vector", "hybrid"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				_, bots, memories, ctx, b, peer := botMemorySearchFixture(t, scope, mode)
				svc, err := store.NewBotMemoryService(bots, memories, b.ID, scope)
				if err != nil {
					t.Fatal(err)
				}
				marker := "routine-operation:" + uuid.NewString()
				for i := 0; i < 25; i++ {
					if err := svc.Add(ctx, store.MemoryEntry{Content: "quasar forgotten", SessionID: b.HomeSessionID.String(), Embedding: []float32{1, 0}, Attribution: &store.MemoryAttribution{BotID: b.ID, Source: "owner", ObservedAt: time.Now(), Status: store.MemoryKnowledgeInferred, SupersededBy: marker}}); err != nil {
						t.Fatal(err)
					}
				}
				for _, m := range []struct{ text, marker string }{{"quasar current", ""}, {"quasar corrected", "correction:" + uuid.NewString()}} {
					if err := svc.Add(ctx, store.MemoryEntry{Content: m.text, SessionID: b.HomeSessionID.String(), Embedding: []float32{1, 0}, Attribution: &store.MemoryAttribution{BotID: b.ID, Source: "owner", ObservedAt: time.Now(), Status: store.MemoryKnowledgeInferred, SupersededBy: m.marker}}); err != nil {
						t.Fatal(err)
					}
				}
				peerSvc, _ := store.NewBotMemoryService(bots, memories, peer.ID, scope)
				if err := peerSvc.Add(ctx, store.MemoryEntry{Content: "quasar peer", SessionID: peer.HomeSessionID.String(), Embedding: []float32{1, 0}}); err != nil {
					t.Fatal(err)
				}
				peerBefore, err := peerSvc.ListBySession(ctx, peer.HomeSessionID.String())
				if err != nil {
					t.Fatal(err)
				}
				history, err := svc.List(ctx, "", 0, 0)
				if err != nil || len(history) != 27 {
					t.Fatalf("history: %d %v", len(history), err)
				}
				for _, limit := range []int{1, 2} {
					rows, err := svc.Search(ctx, "quasar", limit, 0)
					if err != nil || len(rows) != limit {
						t.Fatalf("current top-K %d: %+v %v", limit, rows, err)
					}
					for _, row := range rows {
						if row.Snippet != "quasar current" && row.Snippet != "quasar corrected" {
							t.Fatalf("historical/peer recall: %+v", row)
						}
					}
				}
				after, err := svc.List(ctx, "", 0, 0)
				if err != nil || !reflect.DeepEqual(history, after) {
					t.Fatal("recall changed history")
				}
				peerAfter, err := peerSvc.ListBySession(ctx, peer.HomeSessionID.String())
				if err != nil || !reflect.DeepEqual(peerBefore, peerAfter) {
					t.Fatal("peer changed")
				}
			})
		}
	}
}
