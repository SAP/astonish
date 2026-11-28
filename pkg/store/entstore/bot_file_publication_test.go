package entstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type publicationMutableRunDB struct {
	botDB
	run *botStoredRun
}

func (d publicationMutableRunDB) ReadRun(context.Context, uuid.UUID, uuid.UUID) (*botStoredRun, error) {
	return d.run, nil
}

func TestBotFilePublicationCommittedArtifact(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, scoped, ctx, run, h := runtimeFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: run.Version, ActorID: b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "publication", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			artifact := store.BotArtifact{ID: uuid.New(), HomeID: h.home.ID, RelativePath: "reports/answer.md", SHA256: strings.Repeat("a", 64), MediaType: "text/markdown", Title: "Report", SizeBytes: 13, MemorySummary: "PRIVATE_MEMORY", ContentRef: &store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: b.OwnerID.String(), BotID: b.ID}, Kind: store.BotContentArtifact, Key: "PRIVATE_STORAGE_KEY", SHA256: strings.Repeat("a", 64), SizeBytes: 13, Revision: 1}}
			work, err := repo.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			next := work.Clone()
			next.Phase = store.BotWorkFinished
			next.AgentWork.Disposition = store.AgentWorkAnswered
			next.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: uuid.NewString(), Text: "The report is ready.", Artifacts: []store.BotArtifact{artifact}}
			next.Candidate.Digest = next.Candidate.ContentDigest()
			acceptEmployeeReviewFixture(next)
			work, err = repo.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: work.Revision, State: *next})
			if err != nil {
				t.Fatal(err)
			}
			report := store.BotReport{Summary: next.Candidate.Text, Work: store.BuildBotWorkProjection(work), Artifacts: []store.BotArtifact{artifact}}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: report}); err != nil {
				t.Fatal(err)
			}
			pub, err := repo.PublishReport(ctx, store.BotPublicationRequest{BotID: b.ID, RunID: run.ID})
			if err != nil {
				t.Fatal(err)
			}
			again, err := repo.PublishReport(ctx, store.BotPublicationRequest{BotID: b.ID, RunID: run.ID, ReportSequence: pub.ReportSequence})
			if err != nil || again.EventID != pub.EventID {
				t.Fatal("publication not idempotent", err)
			}
			reader := scoped.(store.BotCommittedArtifactStore)
			got, err := reader.ReadCommittedArtifact(ctx, b.ID, pub.ReportSequence, artifact.ID)
			if err != nil || got.ContentRef.Key != artifact.ContentRef.Key {
				t.Fatal("exact artifact", got, err)
			}
			for _, seq := range []int64{0, pub.ReportSequence + 1} {
				if _, err := reader.ReadCommittedArtifact(ctx, b.ID, seq, artifact.ID); err == nil {
					t.Fatal("non-report sequence accepted")
				}
			}
			if _, err := reader.ReadCommittedArtifact(ctx, b.ID, pub.ReportSequence, uuid.New()); err == nil {
				t.Fatal("foreign artifact accepted")
			}
			if _, err := reader.ReadCommittedArtifact(ctx, uuid.New(), pub.ReportSequence, artifact.ID); err == nil {
				t.Fatal("foreign Bot accepted")
			}
			envelope, err := scoped.(store.BotFilePublicationStore).ReadFilePublication(ctx, b.ID, run.ID, pub.ReportSequence)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(envelope)
			if strings.Contains(string(raw), "PRIVATE_") {
				t.Fatal("private metadata in envelope", string(raw))
			}
			// Destroy mutable run report data: exact publication resolution must survive.
			saved, err := repo.db.ReadRun(ctx, b.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			saved.Run.Report = store.BotReport{Summary: "A later mutable outcome"}
			originalDB := repo.db
			repo.db = publicationMutableRunDB{botDB: originalDB, run: saved}
			defer func() { repo.db = originalDB }()
			if got, err := repo.ReadCommittedArtifact(ctx, b.ID, pub.ReportSequence, artifact.ID); err != nil || got.SHA256 != artifact.SHA256 {
				t.Fatal("mutable run selected", err)
			}
		})
	}
}
