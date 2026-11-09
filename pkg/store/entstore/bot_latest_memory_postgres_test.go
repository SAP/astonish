//go:build integration

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

func TestIntegrationPostgresBotLatestForgottenRecall(t *testing.T) {
	requireDraftActivationPostgres(t)
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		for _, mode := range []string{"fts", "vector", "hybrid", "forget-during-ranking"} {
			t.Run(mode, func(t *testing.T) {
				f := newPostgresBotServicesFixture(t, scope, 100, 100)
				b, err := f.bots.Get(f.owner, f.run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				v := store.DefaultBotVersion()
				v.Charter = "Unrelated memories"
				peer, err := f.bots.CreateDraft(f.owner, store.BotCreateRequest{Name: "Peer", OwnerID: b.OwnerID, Version: v, IdempotencyKey: "peer"})
				if err != nil {
					t.Fatal(err)
				}
				vector := make([]float32, 384)
				vector[0] = 1
				embed := func(context.Context, string) ([]float32, error) { return vector, nil }
				setEmbed := func(fn store.EmbedFunc) {
					switch m := f.memories.(type) {
					case *personalMemoryStore:
						m.embedFunc = fn
						m.vecIndex = newVectorIndex()
					case *teamMemoryStore:
						m.embedFunc = fn
						m.vecIndex = newVectorIndex()
					}
				}
				if mode == "fts" {
					setEmbed(nil)
				} else {
					setEmbed(embed)
				}
				add := func(sessionID, content, marker string) {
					t.Helper()
					botID := b.ID
					if sessionID == peer.HomeSessionID.String() {
						botID = peer.ID
					}
					if e := f.memories.Add(f.owner, store.MemoryEntry{Content: content, Category: "fact", SessionID: sessionID, Embedding: vector, Attribution: &store.MemoryAttribution{BotID: botID, Source: "owner", ObservedAt: time.Now(), Status: store.MemoryKnowledgeInferred, SupersededBy: marker}}); e != nil {
						t.Fatal(e)
					}
				}
				svc, err := store.NewBotMemoryService(f.bots, f.memories, b.ID, scope)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "forget-during-ranking" {
					add(b.HomeSessionID.String(), "quasar retiring", "")
					rows, e := f.memories.ListBySession(f.owner, b.HomeSessionID.String())
					if e != nil || len(rows) != 1 {
						t.Fatalf("seed: %+v %v", rows, e)
					}
					original := rows[0]
					op := store.BotControlProposal{ID: uuid.New(), BotID: b.ID, SourceUserID: b.OwnerID, Payload: store.BotControlPayload{Operation: store.BotControlMemoryForget, Memory: &store.BotControlMemory{MemoryID: original.ID, ExpectedFingerprint: store.MemoryContentFingerprint(original)}}}
					calls := 0
					setEmbed(func(ctx context.Context, _ string) ([]float32, error) {
						calls++
						ctx = context.WithValue(ctx, routineMutationKey{}, true)
						e := f.repo.db.Transaction(ctx, func(tx botDraftDB) error {
							if e := mutateControlMemory(ctx, tx, op, b.HomeSessionID, b.OwnerID, time.Now()); e != nil {
								return e
							}
							return verifyControlMemoryMutation(ctx, tx, op)
						})
						return vector, e
					})
					got, e := f.memories.(store.BotMemoryPrefilterStore).SearchBySessions(f.owner, []string{b.HomeSessionID.String()}, "quasar", 1, 0, "")
					if calls != 1 || !errors.Is(e, store.ErrBotConflict) || len(got) != 0 {
						t.Fatalf("stale candidate recalled: calls=%d %+v %v", calls, got, e)
					}
					after, e := f.memories.Get(f.owner, original.ID)
					a := *original.Attribution
					a.SupersededBy = "routine-operation:" + op.ID.String()
					original.Attribution = &a
					if e != nil || after == nil || !reflect.DeepEqual(original, *after) {
						t.Fatalf("history/effect: %+v %v", after, e)
					}
					return
				}
				marker := "routine-operation:" + uuid.NewString()
				for range 25 {
					add(b.HomeSessionID.String(), "quasar forgotten", marker)
				}
				add(b.HomeSessionID.String(), "quasar current", "")
				add(b.HomeSessionID.String(), "quasar corrected", "correction:"+uuid.NewString())
				add(peer.HomeSessionID.String(), "quasar peer", "")
				peerBefore, err := f.memories.ListBySession(f.owner, peer.HomeSessionID.String())
				if err != nil {
					t.Fatal(err)
				}
				history, err := svc.List(f.owner, "", 0, 0)
				if err != nil || len(history) != 27 {
					t.Fatalf("history: %d %v", len(history), err)
				}
				query := "quasar"
				if mode == "vector" {
					query = "semantic-only-probe"
				}
				for _, limit := range []int{1, 2} {
					rows, e := svc.Search(f.owner, query, limit, 0)
					if e != nil || len(rows) != limit {
						t.Fatalf("current top-K %d: %+v %v", limit, rows, e)
					}
					for _, row := range rows {
						if row.Snippet != "quasar current" && row.Snippet != "quasar corrected" {
							t.Fatalf("forgotten/peer recall: %+v", row)
						}
					}
				}
				after, err := svc.List(f.owner, "", 0, 0)
				if err != nil || !reflect.DeepEqual(history, after) {
					t.Fatalf("history changed: %v", err)
				}
				peerAfter, err := f.memories.ListBySession(f.owner, peer.HomeSessionID.String())
				if err != nil || !reflect.DeepEqual(peerBefore, peerAfter) {
					t.Fatalf("peer changed: %v", err)
				}
			})
		}
	})
}
