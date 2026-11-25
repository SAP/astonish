package entstore

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestBotPublicationReadExact(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, run, now, _ := publicationFixture(t, scope, store.BotReport{Summary: "Completed analysis"}, "")
			if _, err := repo.ReadBotPublication(ctx, run.BotID, run.ID, 1); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("unpublished read: %v", err)
			}
			pub, err := repo.PublishReport(ctx, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID, Now: *now})
			if err != nil {
				t.Fatal(err)
			}
			before := publicationTranscriptCount(t, repo, ctx, run.BotID)
			got, err := repo.ReadBotPublication(ctx, run.BotID, run.ID, pub.ReportSequence)
			if err != nil || got.EventID != pub.EventID {
				t.Fatalf("exact read: %v", err)
			}
			if _, err = repo.ReadBotPublication(ctx, run.BotID, run.ID, pub.ReportSequence+1); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("wrong sequence: %v", err)
			}
			if _, err = repo.ReadBotPublication(ctx, run.BotID, uuid.New(), pub.ReportSequence); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("wrong run: %v", err)
			}
			if _, err = repo.ReadBotPublication(context.Background(), run.BotID, run.ID, pub.ReportSequence); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("unauthenticated: %v", err)
			}
			if publicationTranscriptCount(t, repo, ctx, run.BotID) != before {
				t.Fatal("read published an event")
			}
		})
	}
}
