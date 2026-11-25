package entstore

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotStagingGCCollectsOnlyOldOrphanDocuments(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, _, _, _ := botRunFixture(t, scope)
			draft, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "GC", OwnerID: uuid.MustParse(repo.auth.userID), Version: permanentTestVersion(), IdempotencyKey: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			disk := purgeTestDisk(t, repo, ctx, draft.ID)
			// Unknown layout in another Bot, tenant, template or intake namespace
			// must not be enumerated by this scoped preflight.
			for _, otherScope := range []store.BotContentScope{
				{OrgID: disk.scope.OrgID, TeamID: disk.scope.TeamID, OwnerID: disk.scope.OwnerID, BotID: uuid.New()},
				{OrgID: "other-org", TeamID: disk.scope.TeamID, OwnerID: disk.scope.OwnerID, BotID: draft.ID},
				{OrgID: disk.scope.OrgID, TeamID: disk.scope.TeamID},
				{OrgID: disk.scope.OrgID, TeamID: disk.scope.TeamID, OwnerID: disk.scope.OwnerID, IntakeID: uuid.New()},
			} {
				other := &botDiskContent{root: disk.root, scope: otherScope}
				otherRoot, err := other.scopeRoot(true)
				if err != nil {
					t.Fatal(err)
				}
				if err := otherRoot.Mkdir("foreign-unknown-layout", 0700); err != nil {
					t.Fatal(err)
				}
				_ = otherRoot.Close()
			}
			root, err := disk.scopeRoot(false)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			dir, err := root.Open(".")
			if err != nil {
				t.Fatal(err)
			}
			entries, err := dir.ReadDir(-1)
			_ = dir.Close()
			if err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-72 * time.Hour)
			for _, entry := range entries {
				if err := root.Chtimes(entry.Name(), old, old); err != nil {
					t.Fatal(err)
				}
			}
			put := func(body string) *store.BotContentRef {
				ref, err := disk.Put(ctx, store.BotContentDocument, 1, int64(len(body)), strings.NewReader(body), func(commit func() error) error { return commit() })
				if err != nil {
					t.Fatal(err)
				}
				return ref
			}
			var orphan *store.BotContentRef
			rollback := errors.New("rollback after immutable publication")
			err = repo.db.Transaction(ctx, func(tx botDraftDB) error {
				row, err := tx.GetBot(ctx, draft.ID)
				if err != nil {
					return err
				}
				if err := tx.AdvanceSequence(ctx, draft.ID, row.LastSequence); err != nil {
					return err
				}
				orphan = put("rolled-back revision")
				// The final immutable object is already published and Put's flock
				// released, but its selecting transaction has not yet committed.
				// Even beyond grace the root SQL fence must prevent reclamation.
				pendingName, _ := diskContentName(*orphan)
				if err := root.Chtimes(pendingName, old, old); err != nil {
					return err
				}
				blocked, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
				if _, err := repo.collectStagingOrphans(blocked, draft.ID, 24*time.Hour); err == nil {
					t.Fatal("collected during uncommitted SQL selection")
				}
				if _, err := root.Lstat(pendingName); err != nil {
					t.Fatal("lost uncommitted publication", err)
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				t.Fatalf("rollback: %v", err)
			}
			name, _ := diskContentName(*orphan)
			if err := root.Chtimes(name, old, old); err != nil {
				t.Fatal(err)
			}
			young := put("in-flight published object")
			temp := ".stage-" + uuid.NewString()
			f, err := root.OpenFile(temp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
			if err := root.Chtimes(temp, old, old); err != nil {
				t.Fatal(err)
			}
			before, err := repo.db.GetBot(ctx, draft.ID)
			if err != nil {
				t.Fatal(err)
			}
			report, err := repo.collectStagingOrphans(ctx, draft.ID, 24*time.Hour)
			if err != nil {
				t.Fatalf("collection: %v", err)
			}
			if report == nil || report.Referenced != len(entries) || report.OldUnclassified != 0 || report.Young != 1 || report.InFlight != 1 || report.Removed != 1 {
				t.Fatalf("report: %+v", report)
			}
			// Exercise a real Put paused just before atomic publication. This writer
			// is not under a root SQL fence: preflight must not mistake its temp for
			// garbage and must allow publication to complete after refusing GC.
			_, err = disk.Put(ctx, store.BotContentDocument, 1, 4, strings.NewReader("late"), func(commit func() error) error {
				// Simulate a stream/barrier stalled beyond grace, not merely a young
				// temporary. GC must fail busy without waiting for its SQL barrier.
				listing, err := root.Open(".")
				if err != nil {
					t.Fatal(err)
				}
				staged, err := listing.ReadDir(-1)
				_ = listing.Close()
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range staged {
					if strings.HasPrefix(entry.Name(), ".stage-") {
						if err := root.Chtimes(entry.Name(), old, old); err != nil {
							t.Fatal(err)
						}
					}
				}
				inFlight, err := repo.collectStagingOrphans(ctx, draft.ID, 24*time.Hour)
				if !errors.Is(err, store.ErrBotBusy) || inFlight != nil {
					t.Fatalf("paused publisher preflight: %+v %v", inFlight, err)
				}
				return commit()
			})
			if err != nil {
				t.Fatal("in-flight publication disrupted", err)
			}
			if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("orphan not removed: %v", err)
			}
			for _, ref := range []*store.BotContentRef{young} {
				stream, err := disk.Open(ctx, *ref, store.BotFileRange{})
				if err != nil {
					t.Fatal("lost object", err)
				}
				_ = stream.Close()
			}
			if _, err := root.Lstat(temp); err != nil {
				t.Fatal("lost in-flight temporary", err)
			}
			for _, entry := range entries {
				if _, err := root.Lstat(entry.Name()); err != nil {
					t.Fatal("lost referenced object", err)
				}
			}
			after, err := repo.db.GetBot(ctx, draft.ID)
			if err != nil {
				t.Fatal(err)
			}
			if before.Bot.Revision != after.Bot.Revision || before.LastSequence != after.LastSequence || before.Fence != after.Fence {
				t.Fatal("preflight changed coordination")
			}
			if _, err := repo.collectStagingOrphans(context.Background(), draft.ID, 24*time.Hour); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("anonymous: %v", err)
			}
			if _, err := repo.collectStagingOrphans(ctx, draft.ID, 0); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("missing grace: %v", err)
			}
		})
	}
}

func TestBotStagingGCRejectsUnknownBeforeDeletion(t *testing.T) {
	repo, ctx, _, _, _, _ := botRunFixture(t, store.BotScopePersonal)
	draft, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "GC", OwnerID: uuid.MustParse(repo.auth.userID), Version: permanentTestVersion(), IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	disk := purgeTestDisk(t, repo, ctx, draft.ID)
	root, err := disk.scopeRoot(false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	orphan, err := disk.Put(ctx, store.BotContentDocument, 1, 6, strings.NewReader("orphan"), func(commit func() error) error { return commit() })
	if err != nil {
		t.Fatal(err)
	}
	name, _ := diskContentName(*orphan)
	old := time.Now().Add(-72 * time.Hour)
	if err := root.Chtimes(name, old, old); err != nil {
		t.Fatal(err)
	}
	if err := root.Mkdir("unknown-layout", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.collectStagingOrphans(ctx, draft.ID, 24*time.Hour); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("unknown layout: %v", err)
	}
	if _, err := root.Lstat(name); err != nil {
		t.Fatal("orphan removed before complete layout validation", err)
	}
	if _, err := CollectBotStagingOrphans(ctx, nil, draft.ID, 24*time.Hour); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("unsupported store: %v", err)
	}
}
