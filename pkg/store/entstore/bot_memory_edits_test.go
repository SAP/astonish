package entstore

import (
	"context"
	"encoding/json"
	"entgo.io/ent"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestReviewedBotMemory(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			_, bots, memories, ctx, b, other := botMemorySearchFixture(t, scope, "vector")
			svc, _ := store.NewBotMemoryService(bots, memories, b.ID, scope)
			for _, text := range []string{"original A", "original B"} {
				if err := svc.Add(ctx, store.MemoryEntry{SessionID: b.HomeSessionID.String(), Content: text, Category: "people"}); err != nil {
					t.Fatal(err)
				}
			}
			rows, err := svc.List(ctx, "", 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotMemoryReview{ID: uuid.New(), ExpectedRevision: b.Revision, Kind: store.BotMemoryReviewConsolidate, Confirmed: true, Reason: "owner reviewed merge", Edits: []store.BotMemoryReviewEdit{{SessionID: b.HomeSessionID.String(), Content: "merged", Category: "people"}}}
			for _, row := range rows {
				q.Edits[0].Sources = append(q.Edits[0].Sources, store.BotMemoryReviewSource{ID: row.ID, SessionID: row.SessionID, ExpectedFingerprint: store.MemoryContentFingerprint(row)})
			}
			stale := q
			stale.Edits = append([]store.BotMemoryReviewEdit{}, q.Edits...)
			stale.Edits[0].Sources = append([]store.BotMemoryReviewSource{}, q.Edits[0].Sources...)
			stale.Edits[0].Sources[0].ExpectedFingerprint = store.MemoryContentFingerprint(store.MemorySearchResult{})
			if _, err := svc.ApplyReviewed(ctx, stale); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale: %v", err)
			}
			foreign := q
			foreign.Edits = append([]store.BotMemoryReviewEdit{}, q.Edits...)
			foreign.Edits[0].SessionID = other.HomeSessionID.String()
			if _, err := svc.ApplyReviewed(ctx, foreign); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("foreign: %v", err)
			}
			fail := true
			hook := func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
					if fail {
						return nil, errors.New("receipt failure")
					}
					return next.Mutate(ctx, m)
				})
			}
			switch m := memories.(type) {
			case *personalMemoryStore:
				m.client.BotOutbox.Use(hook)
			case *teamMemoryStore:
				m.client.BotOutbox.Use(hook)
			}
			if _, err := svc.ApplyReviewed(ctx, q); err == nil {
				t.Fatal("receipt failure not rolled back")
			}
			saved, _ := bots.Get(ctx, b.ID)
			after, _ := svc.List(ctx, "", 10, 0)
			if saved.Revision != b.Revision || !reflect.DeepEqual(rows, after) {
				t.Fatal("partial write")
			}
			fail = false
			receipt, err := svc.ApplyReviewed(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := svc.ApplyReviewed(ctx, q)
			if err != nil || !reflect.DeepEqual(receipt, replay) {
				t.Fatalf("replay: %+v %v", replay, err)
			}
			q.Reason = "different"
			if _, err := svc.ApplyReviewed(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("changed replay: %v", err)
			}
			after, err = svc.List(ctx, "", 10, 0)
			if err != nil || len(after) != 3 {
				t.Fatalf("sources deleted: %v %v", after, err)
			}
			for _, before := range rows {
				row, _ := svc.Get(ctx, before.ID)
				if row.Snippet != before.Snippet || row.Attribution == nil || row.Attribution.SupersededBy == "" {
					t.Fatal("evidence lost")
				}
			}
			saved, _ = bots.Get(ctx, b.ID)
			if saved.Revision != b.Revision+1 || saved.Version != b.Version || saved.OwnerID != b.OwnerID || saved.Status != b.Status {
				t.Fatal("authority changed")
			}
		})
	}
}
func TestReviewedBotMemorySourceGuards(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			_, bots, memories, ctx, b, other := botMemorySearchFixture(t, scope, "vector")
			svc, _ := store.NewBotMemoryService(bots, memories, b.ID, scope)
			foreignSvc, _ := store.NewBotMemoryService(bots, memories, other.ID, scope)
			if err := foreignSvc.Add(ctx, store.MemoryEntry{SessionID: other.HomeSessionID.String(), Content: "foreign"}); err != nil {
				t.Fatal(err)
			}
			rows, _ := foreignSvc.List(ctx, "", 10, 0)
			q := store.BotMemoryReview{ID: uuid.New(), ExpectedRevision: b.Revision, Kind: store.BotMemoryReviewDocument, Document: "MEMORY.md", Confirmed: true, Reason: "review", Edits: []store.BotMemoryReviewEdit{{SessionID: b.HomeSessionID.String(), Content: "first"}, {SessionID: b.HomeSessionID.String(), Content: "second", Sources: []store.BotMemoryReviewSource{{ID: rows[0].ID, SessionID: b.HomeSessionID.String(), ExpectedFingerprint: store.MemoryContentFingerprint(rows[0])}}}}}
			if _, err := svc.ApplyReviewed(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("foreign entry: %v", err)
			}
			after, _ := svc.List(ctx, "", 10, 0)
			if len(after) != 0 {
				t.Fatal("partial batch")
			}
			q.Edits = q.Edits[:1]
			q.Document = "notes/2020-02-03.md"
			receipt, err := svc.ApplyReviewed(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			note, _ := svc.Get(ctx, receipt.ReplacementIDs[0])
			if note.CreatedAt[:10] != "2020-02-03" || note.Attribution.Status != store.MemoryKnowledgeInferred {
				t.Fatalf("date or confidence: %+v", note)
			}
			role := "member"
			check := func(context.Context, string) (string, error) { return role, nil }
			switch s := bots.(type) {
			case *personalBotStore:
				s.auth.check = check
			case *teamBotStore:
				s.auth.check = check
			}
			role = "revoked"
			if _, err := svc.ApplyReviewed(ctx, q); err == nil {
				t.Fatal("revoked replay")
			}
			role = "member"
			q.ID = uuid.New()
			q.ExpectedRevision = receipt.Revision
			q.Document = "MEMORY.md"
			hook := func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
					value, err := next.Mutate(ctx, m)
					role = "revoked"
					return value, err
				})
			}
			switch m := memories.(type) {
			case *personalMemoryStore:
				m.client.Memory.Use(hook)
			case *teamMemoryStore:
				m.client.Memory.Use(hook)
			}
			if _, err := svc.ApplyReviewed(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("revocation at commit: %v", err)
			}
			role = "member"
			after, _ = svc.List(ctx, "", 10, 0)
			if len(after) != 1 {
				t.Fatal("revocation partial write")
			}
		})
	}
}

func TestReviewedBotMemoryEvidenceAndContentCAS(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			_, bots, memories, ctx, b, _ := botMemorySearchFixture(t, scope, "vector")
			svc, _ := store.NewBotMemoryService(bots, memories, b.ID, scope)
			if err := svc.Add(ctx, store.MemoryEntry{SessionID: b.HomeSessionID.String(), Content: "observed original"}); err != nil {
				t.Fatal(err)
			}
			rows, err := svc.List(ctx, "", 10, 0)
			if err != nil || len(rows) != 1 {
				t.Fatalf("seed: %v %v", rows, err)
			}
			id := uuid.MustParse(rows[0].ID)
			now := time.Now().UTC().Truncate(time.Second)
			evidence := &store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: "retained observation", Source: "https://example.test/evidence", ObservedAt: now, ActorID: b.OwnerID}
			metadata, err := store.EncodeMemoryAttribution(map[string]any{"raw_evidence": "original payload"}, &store.MemoryAttribution{BotID: b.ID, Source: "observed", ObservedAt: now, Status: store.MemoryKnowledgeVerified, Evidence: evidence})
			if err != nil {
				t.Fatal(err)
			}
			// Seed an observed record via persistence, not the interactive proposal API.
			mutate := func(text string, seed bool) {
				t.Helper()
				switch m := memories.(type) {
				case *personalMemoryStore:
					update := m.client.Memory.UpdateOneID(id).SetChunkText(text)
					if seed {
						update.SetMetadata(metadata)
					}
					err = update.Exec(ctx)
				case *teamMemoryStore:
					update := m.client.Memory.UpdateOneID(id).SetChunkText(text)
					if seed {
						update.SetMetadata(metadata)
					}
					err = update.Exec(ctx)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			mutate("observed original", true)
			row, err := svc.Get(ctx, id.String())
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotMemoryReview{ID: uuid.New(), ExpectedRevision: b.Revision, Kind: store.BotMemoryReviewDocument, Document: "MEMORY.md", Reason: "reviewed", Confirmed: true, Edits: []store.BotMemoryReviewEdit{{SessionID: b.HomeSessionID.String(), Content: "replacement", Sources: []store.BotMemoryReviewSource{{ID: row.ID, SessionID: row.SessionID, ExpectedFingerprint: store.MemoryContentFingerprint(*row)}}}}}
			mutate("concurrently changed", false)
			if _, err := svc.ApplyReviewed(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("content CAS: %v", err)
			}
			saved, err := bots.Get(ctx, b.ID)
			if err != nil || saved.Revision != b.Revision {
				t.Fatalf("stale proposal wrote revision: %v", err)
			}
			row, err = svc.Get(ctx, id.String())
			if err != nil {
				t.Fatal(err)
			}
			q.Edits[0].Sources[0].ExpectedFingerprint = store.MemoryContentFingerprint(*row)
			receipt, err := svc.ApplyReviewed(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			original, err := svc.Get(ctx, id.String())
			if err != nil {
				t.Fatal(err)
			}
			if original.Snippet != row.Snippet || original.Attribution.Status != store.MemoryKnowledgeVerified || !reflect.DeepEqual(original.Attribution.Evidence, evidence) || original.Attribution.SupersededBy == "" {
				t.Fatalf("original evidence changed: %+v", original)
			}
			if len(receipt.Sources) != 1 || !reflect.DeepEqual(receipt.Sources[0].Attribution, row.Attribution) {
				t.Fatal("receipt lost pre-supersession evidence")
			}
			replacement, err := svc.Get(ctx, receipt.ReplacementIDs[0])
			if err != nil {
				t.Fatal(err)
			}
			if replacement.Attribution.Status != store.MemoryKnowledgeInferred || replacement.Attribution.Evidence != nil {
				t.Fatal("review promoted confidence")
			}
			// Verify both independent durable copies, not only the in-memory receipt.
			var originalMetadata, replacementMetadata map[string]any
			switch m := memories.(type) {
			case *personalMemoryStore:
				old, err := m.client.Memory.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				newRow, err := m.client.Memory.Get(ctx, uuid.MustParse(receipt.ReplacementIDs[0]))
				if err != nil {
					t.Fatal(err)
				}
				originalMetadata, replacementMetadata = old.Metadata, newRow.Metadata
			case *teamMemoryStore:
				old, err := m.client.Memory.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				newRow, err := m.client.Memory.Get(ctx, uuid.MustParse(receipt.ReplacementIDs[0]))
				if err != nil {
					t.Fatal(err)
				}
				originalMetadata, replacementMetadata = old.Metadata, newRow.Metadata
			}
			if originalMetadata["raw_evidence"] != "original payload" {
				t.Fatal("unrelated original evidence lost")
			}
			encoded, err := json.Marshal(replacementMetadata["source_records"])
			if err != nil {
				t.Fatal(err)
			}
			var snapshots []store.MemorySearchResult
			if err := json.Unmarshal(encoded, &snapshots); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(snapshots, receipt.Sources) {
				t.Fatal("replacement lost durable source snapshot")
			}
			// Receipt is a durable snapshot, even if another persistence path changes text.
			mutate("later source mutation", false)
			replay, err := svc.ApplyReviewed(ctx, q)
			if err != nil || !reflect.DeepEqual(receipt, replay) {
				t.Fatalf("durable receipt: %+v %v", replay, err)
			}
		})
	}
}

func TestReviewedBotMemoryConcurrentCAS(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			_, bots, memories, ctx, b, _ := botMemorySearchFixture(t, scope, "vector")
			svc, _ := store.NewBotMemoryService(bots, memories, b.ID, scope)
			q := store.BotMemoryReview{ID: uuid.New(), ExpectedRevision: b.Revision, Kind: store.BotMemoryReviewDocument, Document: "MEMORY.md", Reason: "review", Confirmed: true, Edits: []store.BotMemoryReviewEdit{{SessionID: b.HomeSessionID.String(), Content: "new"}}}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					own := q
					own.ID = uuid.New()
					_, err := svc.ApplyReviewed(ctx, own)
					results <- err
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			successes := 0
			for err := range results {
				if err == nil {
					successes++
				}
			}
			if successes != 1 {
				t.Fatalf("%d successful CAS", successes)
			}
			rows, _ := svc.List(ctx, "", 10, 0)
			if len(rows) != 1 {
				t.Fatal("concurrent partial writes")
			}
		})
	}
}
