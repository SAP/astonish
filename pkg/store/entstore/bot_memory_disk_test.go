package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotDiskMemoryExactBackendIdentity(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			_, bots, generic, ctx, b, _ := botMemorySearchFixture(t, scope, "vector")
			provider := bots.(store.BotMemoryStoreProvider)
			disk, err := provider.BotMemoryStore(b.ID)
			if err != nil {
				t.Fatal(err)
			}
			original := disk.(*botDiskMemoryStore)
			foreign := *original
			repo := *original.repo
			foreign.repo = &repo
			switch db := repo.db.(type) {
			case personalBotDB:
				db.content = newBotContentCodec(t.TempDir(), store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: b.OwnerID.String()})
				repo.db = db
			case teamBotDB:
				db.content = newBotContentCodec(t.TempDir(), store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: b.OwnerID.String()})
				repo.db = db
			}
			var typedNil *botDiskMemoryStore
			for _, invalid := range []store.MemoryStore{typedNil, &botDiskMemoryStore{owner: original.owner, id: b.ID}, &foreign} {
				if _, err := store.NewBotMemoryService(bots, invalid, b.ID, scope); !errors.Is(err, store.ErrBotUnsupported) {
					t.Fatalf("composition accepted invalid backend: %v", err)
				}
				if err := bots.(store.BotMemoryMutationStore).MutateBotMemory(ctx, invalid, b.ID, store.BotMemoryMutation{}); !errors.Is(err, store.ErrBotUnsupported) {
					t.Fatalf("mutation accepted invalid backend: %v", err)
				}
				if _, err := bots.(store.BotMemoryReviewStore).ApplyReviewedBotMemory(ctx, invalid, b.ID, store.BotMemoryReview{}); !errors.Is(err, store.ErrBotUnsupported) {
					t.Fatalf("review accepted invalid backend: %v", err)
				}
			}
			if _, err := store.NewBotMemoryService(bots, generic, b.ID, scope); err != nil {
				t.Fatalf("same-scope router composition: %v", err)
			}
		})
	}
}

func TestBotDiskMemoryReviewRevokedDuringStaging(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			_, bots, generic, ctx, b, _ := botMemorySearchFixture(t, scope, "vector")
			var role atomic.Value
			role.Store("member")
			check := func(context.Context, string) (string, error) { return role.Load().(string), nil }
			switch s := bots.(type) {
			case *personalBotStore:
				s.auth.check = check
			case *teamBotStore:
				s.auth.check = check
			}
			svc, err := store.NewBotMemoryService(bots, generic, b.ID, scope)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Add(ctx, store.MemoryEntry{SessionID: b.HomeSessionID.String(), Content: "original"}); err != nil {
				t.Fatal(err)
			}
			rows, err := svc.List(ctx, "", 10, 0)
			if err != nil || len(rows) != 1 {
				t.Fatalf("seed: %+v %v", rows, err)
			}
			q := store.BotMemoryReview{ID: uuid.New(), ExpectedRevision: b.Revision, Kind: store.BotMemoryReviewDocument, Document: "MEMORY.md", Confirmed: true, Reason: "review", Edits: []store.BotMemoryReviewEdit{{SessionID: rows[0].SessionID, Content: "unauthorized", Sources: []store.BotMemoryReviewSource{{ID: rows[0].ID, SessionID: rows[0].SessionID, ExpectedFingerprint: store.MemoryContentFingerprint(rows[0])}}}}}
			entered, release := make(chan struct{}), make(chan struct{})
			blockBotMemoryStaging(t, bots, entered, release)
			done := make(chan error, 1)
			go func() { _, err := svc.ApplyReviewed(ctx, q); done <- err }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("disk staging not reached")
			}
			role.Store("viewer")
			close(release)
			if err := <-done; !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("revoked review: %v", err)
			}
			role.Store("member")
			after, err := svc.List(ctx, "", 10, 0)
			if err != nil || len(after) != 1 || after[0].Snippet != "original" || after[0].Attribution != nil {
				t.Fatalf("partial review: %+v %v", after, err)
			}
			saved, err := bots.Get(ctx, b.ID)
			if err != nil || saved.Revision != b.Revision {
				t.Fatalf("revision changed: %+v %v", saved, err)
			}
		})
	}
}

func TestBotDiskMemoryReviewAndSQLAbsence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			_, bots, generic, ctx, b, _ := botMemorySearchFixture(t, scope, "vector")
			svc, err := store.NewBotMemoryService(bots, generic, b.ID, scope)
			if err != nil {
				t.Fatal(err)
			}
			sentinel := "BOT_DISK_ONLY_SENTINEL_5e94"
			evidence := &store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: sentinel + " evidence", Source: "observation:1", ObservedAt: time.Now().UTC(), ActorID: b.OwnerID}
			attribution := &store.MemoryAttribution{BotID: b.ID, Source: "owner-observation", ObservedAt: evidence.ObservedAt, Status: store.MemoryKnowledgeVerified, Evidence: evidence}
			if err := svc.Add(ctx, store.MemoryEntry{Content: sentinel, SessionID: b.HomeSessionID.String(), Attribution: attribution, Metadata: map[string]any{"raw_evidence": sentinel + " nested"}}); err != nil {
				t.Fatal(err)
			}
			rows, err := svc.List(ctx, "", 10, 0)
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows %v %v", rows, err)
			}
			old := rows[0]
			if err := svc.Update(ctx, old.ID, "unreviewed rewrite", ""); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("unreviewed: %v", err)
			}
			review := store.BotMemoryReview{ID: uuid.New(), ExpectedRevision: b.Revision, Kind: store.BotMemoryReviewDocument, Document: "MEMORY.md", Confirmed: true, Reason: "review", Edits: []store.BotMemoryReviewEdit{{SessionID: old.SessionID, Content: sentinel + " corrected", Sources: []store.BotMemoryReviewSource{{ID: old.ID, SessionID: old.SessionID, ExpectedFingerprint: store.MemoryContentFingerprint(old)}}}}}
			receipt, err := svc.ApplyReviewed(ctx, review)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := svc.ApplyReviewed(ctx, review)
			if err != nil || replay.RequestFingerprint != receipt.RequestFingerprint {
				t.Fatalf("replay %v", err)
			}
			history, err := svc.Get(ctx, old.ID)
			if err != nil || history.Snippet != sentinel || history.Attribution.SupersededBy == "" || history.Attribution.Evidence.Summary != evidence.Summary {
				t.Fatalf("history %+v %v", history, err)
			}
			current, err := svc.Search(ctx, sentinel, 1, 0)
			if err != nil || len(current) != 1 || current[0].ID != receipt.ReplacementIDs[0] {
				t.Fatalf("current %+v %v", current, err)
			}
			// Reconstruct the service to prove it reads durable bytes, not an in-process cache.
			reopened, err := store.NewBotMemoryService(bots, nil, b.ID, scope)
			if err != nil {
				t.Fatal(err)
			}
			got, err := reopened.Get(ctx, receipt.ReplacementIDs[0])
			if err != nil || got.Snippet != sentinel+" corrected" {
				t.Fatalf("reopen %+v %v", got, err)
			}
			var raw []byte
			switch s := bots.(type) {
			case *personalBotStore:
				n, err := s.client.Memory.Query().Count(ctx)
				if err != nil || n != 0 {
					t.Fatalf("SQL memories=%d %v", n, err)
				}
				bot, err := s.client.Bot.Get(ctx, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				outbox, err := s.client.BotOutbox.Query().All(ctx)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ = json.Marshal([]any{bot.Data, outbox})
			case *teamBotStore:
				n, err := s.client.Memory.Query().Count(ctx)
				if err != nil || n != 0 {
					t.Fatalf("SQL memories=%d %v", n, err)
				}
				bot, err := s.client.Bot.Get(ctx, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				outbox, err := s.client.BotOutbox.Query().All(ctx)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ = json.Marshal([]any{bot.Data, outbox})
			}
			if strings.Contains(string(raw), sentinel) {
				t.Fatalf("memory body leaked into SQL: %s", raw)
			}
		})
	}
}
