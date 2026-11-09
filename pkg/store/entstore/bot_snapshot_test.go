package entstore

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotSnapshotPagesAndViewerReadState(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, _ := botRunFixture(t, scope)
			for i := 0; i < 121; i++ {
				rec, err := repo.db.GetBot(ctx, run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				if err := repo.db.Transaction(ctx, func(tx botDraftDB) error {
					if err := tx.AdvanceSequence(ctx, run.BotID, rec.LastSequence); err != nil {
						return err
					}
					return tx.InsertActivity(ctx, store.BotActivity{BotID: run.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityReport, Payload: []byte(`{"summary":"fixture"}`), CreatedAt: repo.now()}, uuid.NewString())
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "Latest durable outcome"}}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := repo.Snapshot(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.LatestSequence < 121 || snapshot.UnreadCount != snapshot.LatestSequence || snapshot.LatestOutcome == nil || snapshot.LatestOutcome.Report.Summary != "Latest durable outcome" {
				t.Fatalf("snapshot: %+v", snapshot)
			}
			var after int64
			count := 0
			for {
				page, err := repo.ListActivity(ctx, run.BotID, after, 50)
				if err != nil {
					t.Fatal(err)
				}
				if len(page) == 0 {
					break
				}
				for _, event := range page {
					if event.Sequence <= after {
						t.Fatal("non-monotonic activity")
					}
					after = event.Sequence
					count++
				}
			}
			if int64(count) != snapshot.LatestSequence {
				t.Fatalf("lost activity: %d/%d", count, snapshot.LatestSequence)
			}
			for _, sequence := range []int64{100, 50, 100} {
				if err := repo.MarkRead(ctx, store.BotReadCursor{BotID: run.BotID, ViewerID: snapshot.ReadCursor.ViewerID, Sequence: sequence}); err != nil {
					t.Fatal(err)
				}
			}
			read, err := repo.ReadCursor(ctx, run.BotID)
			if err != nil || read.Sequence != 100 {
				t.Fatalf("cursor: %+v %v", read, err)
			}
			snapshot, err = repo.Snapshot(ctx, run.BotID)
			if err != nil || snapshot.UnreadCount != snapshot.LatestSequence-100 {
				t.Fatalf("read snapshot: %+v %v", snapshot, err)
			}
			other := uuid.New()
			otherCtx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: other.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			otherSnapshot, err := repo.Snapshot(otherCtx, run.BotID)
			if scope == store.BotScopePersonal {
				if !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("private viewer leaked: %v", err)
				}
			} else {
				if err != nil || otherSnapshot.ReadCursor.Sequence != 0 || otherSnapshot.UnreadCount != snapshot.LatestSequence {
					t.Fatalf("viewer state leaked: %+v %v", otherSnapshot, err)
				}
				if err = repo.MarkRead(otherCtx, store.BotReadCursor{BotID: run.BotID, ViewerID: other, Sequence: 10}); err != nil {
					t.Fatal(err)
				}
				read, err = repo.ReadCursor(ctx, run.BotID)
				if err != nil || read.Sequence != 100 {
					t.Fatal("another viewer changed owner cursor")
				}
			}
			repo.auth.check = func(context.Context, string) (string, error) { return "", nil }
			if _, err = repo.Snapshot(ctx, run.BotID); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("revoked membership: %v", err)
			}
		})
	}
}
