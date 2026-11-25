package entstore

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func purgeTestDisk(t *testing.T, repo *botRepository, ctx context.Context, id store.BotID) *botDiskContent {
	t.Helper()
	var codec *botContentCodec
	switch d := repo.db.(type) {
	case personalBotDB:
		codec = d.contentCodec()
	case teamBotDB:
		codec = d.contentCodec()
	default:
		t.Fatal("unexpected adapter")
	}
	content, err := codec.Open(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	disk, ok := content.(*botDiskContent)
	if !ok {
		t.Fatalf("expected draft staging, got %T", content)
	}
	t.Cleanup(func() { _ = disk.Close() })
	return disk
}

func TestBotPermanentDraftPurgesStaging(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, _, _, _ := botRunFixture(t, scope)
			owner := uuid.MustParse(repo.auth.userID)
			draft, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Draft", OwnerID: owner, Version: permanentTestVersion(), IdempotencyKey: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			disk := purgeTestDisk(t, repo, ctx, draft.ID)
			if _, err := disk.root.Lstat(disk.namespace()); err != nil {
				t.Fatal("missing initial staging", err)
			}
			// An interrupted write is not SQL-referenced, but is still owned by this Bot.
			root, err := disk.scopeRoot(false)
			if err != nil {
				t.Fatal(err)
			}
			f, err := root.OpenFile(".stage-"+uuid.NewString(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString("interrupted body"); err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
			_ = root.Close()
			otherScopes := []store.BotContentScope{
				{OrgID: disk.scope.OrgID, TeamID: disk.scope.TeamID, OwnerID: owner.String(), BotID: uuid.New()},
				{OrgID: "other-org", TeamID: disk.scope.TeamID, OwnerID: owner.String(), BotID: draft.ID},
				{OrgID: disk.scope.OrgID, TeamID: "other-team", OwnerID: owner.String(), BotID: draft.ID},
				{OrgID: disk.scope.OrgID, TeamID: disk.scope.TeamID}, // independent template
				{OrgID: disk.scope.OrgID, TeamID: disk.scope.TeamID, OwnerID: owner.String(), IntakeID: uuid.New()},
			}
			var keep []*botDiskContent
			for _, otherScope := range otherScopes {
				other := &botDiskContent{root: disk.root, scope: otherScope}
				kind := store.BotContentDocument
				if otherScope.BotID == uuid.Nil {
					kind = store.BotContentTemplate
				}
				if otherScope.IntakeID != uuid.Nil {
					kind = store.BotContentIntake
				}
				if _, err := other.Put(ctx, kind, 1, 4, strings.NewReader("keep"), func(commit func() error) error { return commit() }); err != nil {
					t.Fatal(err)
				}
				keep = append(keep, other)
			}
			deleted, err := repo.BeginPermanentDelete(ctx, store.BotPermanentDeleteRequest{BotID: draft.ID, ActorID: owner, ExpectedRevision: draft.Revision, Confirm: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.PurgeDeleted(ctx, draft.ID, deleted.Revision); err != nil {
				t.Fatal(err)
			}
			for _, other := range keep {
				if _, err := other.root.Lstat(other.namespace()); err != nil {
					t.Fatalf("independent namespace removed: %v", err)
				}
			}
			if _, err := disk.root.Lstat(disk.namespace()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("staging survived successful permanent draft purge: %v", err)
			}
		})
	}
}
