package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotStagingGCTypedNestedClosure(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, _, _, _ := botRunFixture(t, scope)
			draft, err := repo.Create(ctx, store.BotCreateRequest{Name: "GC nested", OwnerID: uuid.MustParse(repo.auth.userID), Version: permanentTestVersion(), IdempotencyKey: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			disk := purgeTestDisk(t, repo, ctx, draft.ID)
			root, err := disk.scopeRoot(false)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			put := func(kind store.BotContentKind, text string) *store.BotContentRef {
				ref, err := disk.Put(ctx, kind, 1, int64(len(text)), strings.NewReader(text), func(commit func() error) error { return commit() })
				if err != nil {
					t.Fatal(err)
				}
				name, _ := diskContentName(*ref)
				old := time.Now().Add(-72 * time.Hour)
				if err := root.Chtimes(name, old, old); err != nil {
					t.Fatal(err)
				}
				return ref
			}
			artifact := put(store.BotContentArtifact, "nested immutable bytes")
			retained := put(store.BotContentCheckpoint, "unknown old checkpoint retained")
			orphan := put(store.BotContentDocument, "orphan")
			payload, err := json.Marshal(store.BotReport{Artifacts: []store.BotArtifact{{ContentRef: artifact, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes}}})
			if err != nil {
				t.Fatal(err)
			}
			err = repo.db.Transaction(ctx, func(tx botDB) error {
				row, err := tx.GetBot(ctx, draft.ID)
				if err != nil {
					return err
				}
				if err := tx.AdvanceSequence(ctx, draft.ID, row.LastSequence); err != nil {
					return err
				}
				return tx.InsertActivity(ctx, store.BotActivity{BotID: draft.ID, Sequence: row.LastSequence + 1, Kind: store.BotActivityReport, Payload: append(append([]byte(`{"report":`), payload...), '}'), CreatedAt: time.Now().UTC()}, uuid.NewString())
			})
			if err != nil {
				t.Fatal(err)
			}
			// Foreign identity cannot remove even a proven old orphan.
			principal, _ := execution.PrincipalFromContext(ctx)
			principal.Subject = uuid.NewString()
			foreign, principalErr := execution.WithPrincipal(ctx, principal)
			if principalErr != nil {
				t.Fatal(principalErr)
			}
			if _, err := repo.collectStagingOrphans(foreign, draft.ID, 24*time.Hour); err == nil {
				t.Fatal("foreign collector authorized")
			}
			name, _ := diskContentName(*orphan)
			if _, err := root.Lstat(name); err != nil {
				t.Fatal("foreign collection deleted bytes", err)
			}
			report, err := repo.collectStagingOrphans(ctx, draft.ID, 24*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			// The activity is now inline SQL, not a third external object.
			if report.Removed != 1 || report.OldUnclassified != 1 || report.Referenced != 2 {
				t.Fatalf("report: %+v", report)
			}
			for _, ref := range []*store.BotContentRef{artifact, retained} {
				name, _ := diskContentName(*ref)
				if _, err := root.Lstat(name); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("orphan retained: %v", err)
			}
			// Missing nested bytes abort the entire sweep, even with an eligible orphan.
			orphan = put(store.BotContentDocument, "second orphan")
			name, _ = diskContentName(*artifact)
			if err := root.Remove(name); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.collectStagingOrphans(ctx, draft.ID, 24*time.Hour); err == nil {
				t.Fatal("missing nested object accepted")
			}
			name, _ = diskContentName(*orphan)
			if _, err := root.Lstat(name); err != nil {
				t.Fatal("deleted before closure validation", err)
			}
		})
	}
}

func TestBotPublicationLockCrossProcess(t *testing.T) {
	// Re-exec this exact test to prove OS, rather than goroutine-only exclusion.
	if directory := os.Getenv("ASTONISH_GC_LOCK_TEST_ROOT"); directory != "" {
		var scope store.BotContentScope
		if err := json.Unmarshal([]byte(os.Getenv("ASTONISH_GC_LOCK_TEST_SCOPE")), &scope); err != nil {
			t.Fatal(err)
		}
		disk, err := newBotDiskContent(directory, scope)
		if err != nil {
			t.Fatal(err)
		}
		defer disk.Close()
		_, err = disk.Put(context.Background(), store.BotContentDocument, 1, 1, strings.NewReader("x"), func(commit func() error) error { return commit() })
		if !errors.Is(err, store.ErrBotBusy) {
			t.Fatalf("cross-process publication not fenced: %v", err)
		}
		return
	}
	directory := t.TempDir()
	scope := store.BotContentScope{OwnerID: uuid.NewString(), BotID: uuid.New()}
	disk, err := newBotDiskContent(directory, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	lock, err := disk.lockPublication(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(scope)
	cmd := exec.Command(os.Args[0], "-test.run=^TestBotPublicationLockCrossProcess$", "-test.timeout=20s")
	cmd.Env = append(os.Environ(), "ASTONISH_GC_LOCK_TEST_ROOT="+directory, "ASTONISH_GC_LOCK_TEST_SCOPE="+string(raw))
	output, runErr := cmd.CombinedOutput()
	_ = lock.Close()
	if runErr != nil {
		t.Fatalf("child: %v\n%s", runErr, output)
	}
	if _, err := disk.Put(context.Background(), store.BotContentDocument, 1, 1, strings.NewReader("x"), func(commit func() error) error { return commit() }); err != nil {
		t.Fatal("lock not released", err)
	}
}
